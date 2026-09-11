#!/usr/bin/env bash
# scripts/vps_preflight.sh
# Read-only VPS preflight discovery script for CordBrief M17 migration.
# Collects host, container, volume, cursor, ownership, and resource state without any mutations.

set -eo pipefail

echo "========================================================"
echo "      CordBrief M17: VPS Read-Only Preflight Audit      "
echo "========================================================"
echo "Timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo ""

# 1. Host & Repository Diagnostics
echo "--- [1/6] Host & Repository State ---"
echo "Working Directory: $(pwd)"
echo "Current User:      $(id -u -n) (uid=$(id -u), gid=$(id -g))"
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo "Git Root:          $(git rev-parse --show-toplevel)"
    echo "Current Commit:    $(git rev-parse HEAD)"
    echo "Current Branch:    $(git branch --show-current 2>/dev/null || echo 'detached')"
    DIRTY_COUNT=$(git status --porcelain | wc -l | tr -d ' ')
    echo "Dirty Files Count: ${DIRTY_COUNT}"
else
    echo "Git:               Not a git repository"
fi
echo ""

# 2. System Resources
echo "--- [2/6] System Resources ---"
echo "Available Memory:"
if command -v free >/dev/null 2>&1; then
    free -m
else
    echo "free command unavailable"
fi
echo ""
echo "Filesystem Space:"
df -h . /var/lib/docker 2>/dev/null || df -h .
echo ""

# 3. Docker Environment
echo "--- [3/6] Docker & Compose Runtime ---"
if command -v docker >/dev/null 2>&1; then
    docker --version
    docker compose version 2>/dev/null || docker-compose --version 2>/dev/null || echo "docker compose plugin not found"
else
    echo "ERROR: docker command not found" >&2
    exit 1
fi
echo ""

# 4. Container Inventory & Image User Configuration
echo "--- [4/6] Containers & Compose Projects ---"
echo "Active & Stopped Containers:"
docker ps -a --format "table {{.Names}}\t{{.Status}}\t{{.Image}}\t{{.Ports}}"
echo ""
echo "Canonical Image Target User: 1000:1000 (cordbrief:cordbrief in Dockerfile & compose.yml)"
for c in cordbrief-collector cordbrief-core; do
    if docker inspect "$c" >/dev/null 2>&1; then
        c_user=$(docker inspect --format '{{.Config.User}}' "$c" 2>/dev/null || true)
        echo "  ${c} container config user: '${c_user:-<default root>}'"
    fi
done
echo ""

# 5. Volume Inventory & Discovered Ownership
echo "--- [5/6] Volume Inventory & Ownership Audit ---"
VOLUMES=$(docker volume ls --format "{{.Name}}")
echo "Discovered Volumes:"
echo "$VOLUMES" | grep -E "cordbrief" || echo "No cordbrief volumes discovered"
echo ""

# 6. Read-Only State Inspection (Exchange & Core)
echo "--- [6/6] Journal & Core Cursor Status (Read-Only) ---"

EXCHANGE_VOL=""
for v in cordbrief_exchange cordbrief_rpc_exchange; do
    if echo "$VOLUMES" | grep -qx "$v"; then
        EXCHANGE_VOL="$v"
        break
    fi
done

if [ -n "$EXCHANGE_VOL" ]; then
    echo "Inspecting exchange volume: [${EXCHANGE_VOL}]"
    docker run --rm -v "${EXCHANGE_VOL}:/ex:ro" alpine sh -c '
        ex_owner=$(stat -c %u:%g /ex 2>/dev/null || stat -f %u:%g /ex 2>/dev/null || echo "unknown")
        echo "  Volume root ownership (UID:GID): ${ex_owner}"
        echo "  Events directory entries:"
        ls -la /ex/events 2>/dev/null || echo "  No /ex/events directory"
        if [ -f /ex/core-ack.json ]; then
            echo "  core-ack.json content:"
            cat /ex/core-ack.json
            echo ""
        else
            echo "  core-ack.json: NOT PRESENT"
        fi
        if [ -f /ex/recovery-state.json ]; then
            echo "  recovery-state.json: PRESENT"
        fi
        if [ -f /ex/retention-manifest.json ]; then
            echo "  retention-manifest.json: PRESENT"
        fi
    '
else
    echo "Notice: No exchange volume found to inspect."
fi

CORE_VOL=""
for v in cordbrief_core_data cordbrief_rpc_core_data; do
    if echo "$VOLUMES" | grep -qx "$v"; then
        CORE_VOL="$v"
        break
    fi
done

if [ -n "$CORE_VOL" ]; then
    echo "Inspecting core_data volume: [${CORE_VOL}]"
    docker run --rm -v "${CORE_VOL}:/core:ro" alpine sh -c '
        core_owner=$(stat -c %u:%g /core 2>/dev/null || stat -f %u:%g /core 2>/dev/null || echo "unknown")
        echo "  Volume root ownership (UID:GID): ${core_owner}"
        if [ -f /core/core.db ]; then
            echo "  core.db: PRESENT ($(stat -c %s /core/core.db 2>/dev/null || stat -f %z /core/core.db) bytes)"
        else
            echo "  core.db: NOT PRESENT"
        fi
        if [ -d /core/digests ]; then
            echo "  digests directory: $(ls -1 /core/digests 2>/dev/null | wc -l | tr -d " ") files"
        fi
        if [ -f /core/secrets.json ]; then
            mode=$(stat -c %a /core/secrets.json 2>/dev/null || stat -f %Lp /core/secrets.json)
            echo "  secrets.json: PRESENT (mode $mode)"
        fi
    '
else
    echo "Notice: No core_data volume found to inspect."
fi
echo ""

echo "[PREFLIGHT COMPLETE] Read-only assessment finished. Zero mutations performed."
