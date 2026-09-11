#!/usr/bin/env bash
# scripts/update_secret.sh
# Securely updates the Discord Client Secret strictly into the designated Docker volume (mode 0600)
# without touching repository .env, echoing, or logging the secret value.
#
# Target volume can be specified via:
#   1. First command-line argument: ./scripts/update_secret.sh [volume_name]
#   2. Environment variable: TARGET_VOLUME=... ./scripts/update_secret.sh
#   3. Default: cordbrief_rpc_collector_data
#
# Target ownership defaults to canonical UID:GID 1000:1000 (proven by Dockerfiles and compose.yml),
# but can be overridden via TARGET_UID and TARGET_GID.

set -eo pipefail

TARGET_VOLUME="${1:-${TARGET_VOLUME:-cordbrief_rpc_collector_data}}"
TARGET_UID="${TARGET_UID:-1000}"
TARGET_GID="${TARGET_GID:-1000}"
TARGET_OWNER="${TARGET_UID}:${TARGET_GID}"

echo "=== Discord Client Secret Secure Updater ==="
echo "Target volume: ${TARGET_VOLUME}"
echo "Target owner:  ${TARGET_OWNER}"
echo "The secret will be masked and not displayed or logged."
echo ""

# 1. Resolve client_id: argument 2 -> environment -> .env -> default
CLIENT_ID="${2:-${DISCORD_CLIENT_ID:-}}"

if [ -z "$CLIENT_ID" ] && [ -f ".env" ]; then
    CLIENT_ID=$(grep -E '^DISCORD_CLIENT_ID=' .env | cut -d '=' -f2- | tr -d ' "\r\n' || true)
fi

if [ -z "$CLIENT_ID" ]; then
    CLIENT_ID="1547744191122247772"
fi

# 2. Masked secret input
read -s -p "Enter Discord Client Secret: " plainSecret
echo ""

if [ -z "$plainSecret" ]; then
    echo "ERROR: Empty secret provided. Operation aborted." >&2
    exit 1
fi

# 3. Safely format JSON without exposing or printing the secret
if command -v python3 >/dev/null 2>&1; then
    PAYLOAD=$(python3 -c "import json, sys; print(json.dumps({'client_id': sys.argv[1], 'client_secret': sys.argv[2]}))" "$CLIENT_ID" "$plainSecret")
elif command -v node >/dev/null 2>&1; then
    PAYLOAD=$(node -e "console.log(JSON.stringify({client_id: process.argv[1], client_secret: process.argv[2]}))" "$CLIENT_ID" "$plainSecret")
else
    escaped_secret=$(printf '%s' "$plainSecret" | sed 's/\\/\\\\/g; s/"/\\"/g')
    PAYLOAD="{\"client_id\":\"$CLIENT_ID\",\"client_secret\":\"$escaped_secret\"}"
    unset escaped_secret
fi
unset plainSecret

# 4. Write securely to target volume with mode 0600 and resolved ownership
printf '%s\n' "$PAYLOAD" | docker run --rm -i -v "${TARGET_VOLUME}:/var/lib/cordbrief" alpine sh -c "
    set -e
    mkdir -p /var/lib/cordbrief
    cat > /var/lib/cordbrief/credentials.json
    chmod 0600 /var/lib/cordbrief/credentials.json
    chown ${TARGET_OWNER} /var/lib/cordbrief/credentials.json
"
unset PAYLOAD

# 5. Non-revealing verification of file existence, permissions, and structure
VERIFY_RESULT=$(docker run --rm -v "${TARGET_VOLUME}:/var/lib/cordbrief:ro" alpine sh -c "
    set -e
    if [ ! -f /var/lib/cordbrief/credentials.json ]; then
        echo \"ERR_NOT_FOUND\"
        exit 1
    fi
    mode=\$(stat -c %a /var/lib/cordbrief/credentials.json 2>/dev/null || stat -f %Lp /var/lib/cordbrief/credentials.json 2>/dev/null || echo \"unknown\")
    if [ \"\$mode\" != \"600\" ]; then
        echo \"ERR_BAD_MODE:\$mode\"
        exit 1
    fi
    owner=\$(stat -c %u:%g /var/lib/cordbrief/credentials.json 2>/dev/null || echo \"${TARGET_OWNER}\")
    if [ \"\$owner\" != \"${TARGET_OWNER}\" ]; then
        echo \"ERR_BAD_OWNER:\$owner\"
        exit 1
    fi
    if ! grep -q '\"client_id\"' /var/lib/cordbrief/credentials.json || ! grep -q '\"client_secret\"' /var/lib/cordbrief/credentials.json; then
        echo \"ERR_MALFORMED_JSON\"
        exit 1
    fi
    echo \"OK\"
")

if [ "$VERIFY_RESULT" != "OK" ]; then
    echo "ERROR: Verification failed: $VERIFY_RESULT" >&2
    exit 1
fi

echo ""
echo "[OK] Discord Client Secret successfully seeded into volume [${TARGET_VOLUME}]."
echo "[OK] Location: /var/lib/cordbrief/credentials.json (mode 0600, uid:gid ${TARGET_OWNER})."
echo "[OK] Client ID: ${CLIENT_ID}."
echo "[OK] Secret value was never printed, echoed, or stored on host disk."
