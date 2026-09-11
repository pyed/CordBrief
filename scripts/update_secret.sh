#!/usr/bin/env bash
# scripts/update_secret.sh
# Securely updates the Discord Client Secret strictly in container private storage (0600)
# without touching repository .env, echoing, or logging the secret value.

set -eo pipefail

echo "=== Discord Client Secret Secure Updater ==="
echo "The secret will be masked and not displayed or logged."
echo ""

# 1. Masked secret input
read -s -p "Enter new Discord Client Secret: " plainSecret
echo ""

if [ -z "$plainSecret" ]; then
    echo "ERROR: Empty secret provided. Operation aborted." >&2
    exit 1
fi

# 2. Resolve client_id: existing container credentials -> .env -> default
CLIENT_ID=""
if docker exec cordbrief-collector test -f /var/lib/cordbrief/credentials.json 2>/dev/null; then
    CLIENT_ID=$(docker exec cordbrief-collector python3 -c "import json; print(json.load(open('/var/lib/cordbrief/credentials.json')).get('client_id', ''))" 2>/dev/null || true)
fi

if [ -z "$CLIENT_ID" ] && [ -f ".env" ]; then
    CLIENT_ID=$(grep -E '^DISCORD_CLIENT_ID=' .env | cut -d '=' -f2- | tr -d ' "\r\n' || true)
fi

if [ -z "$CLIENT_ID" ]; then
    CLIENT_ID="1547744191122247772"
fi

# 3. Write securely to container private storage with mode 0600
PAYLOAD=$(python3 -c "import json, sys; print(json.dumps({'client_id': sys.argv[1], 'client_secret': sys.argv[2]}))" "$CLIENT_ID" "$plainSecret")
unset plainSecret

echo "$PAYLOAD" | docker exec -i cordbrief-collector sh -c 'cat > /var/lib/cordbrief/credentials.json && chmod 0600 /var/lib/cordbrief/credentials.json'
unset PAYLOAD

# 4. Verify
docker exec cordbrief-collector python3 -c "
import json, os, stat
path = '/var/lib/cordbrief/credentials.json'
assert os.path.exists(path), 'File does not exist'
st = os.stat(path)
assert stat.S_IMODE(st.st_mode) == 0o600, 'Incorrect mode'
data = json.load(open(path, 'r'))
assert 'client_id' in data and len(data['client_id']) > 0, 'Invalid client_id'
assert 'client_secret' in data and len(data['client_secret']) > 0, 'Invalid client_secret'
"

echo ""
echo "[OK] Discord Client Secret successfully updated in container private storage (/var/lib/cordbrief/credentials.json)."
echo "[OK] Permissions verified (0600). Client ID preserved ($CLIENT_ID). Secret was NOT written to .env, echoed, or logged."
