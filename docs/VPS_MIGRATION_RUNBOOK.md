# CordBrief M17: VPS Migration Runbook (Archival Reference)
## Legacy Vencord to Canonical Official Discord RPC Architecture

> [!NOTE]
> **ARCHIVAL / HISTORICAL REFERENCE ONLY**  
> This migration runbook was developed during Milestone M17 as an operational design study for migrating in-place legacy deployments.  
> **Current Status**: Archival. CordBrief v2.0.0 adopts a clean-room installation model (see [SETUP.md](SETUP.md)). This document is retained solely for historical and architectural provenance.

---

## Executive Summary & Core Invariants

1. **Zero Legacy Reactivation & Safe Rollback**: Under NO circumstances will the legacy Vencord collector, CDP collector, patched Discord client, or user-token/private-endpoint collectors ever be restarted. During rollback, both Collection and Core remain stopped by default. Normal Core startup is not read-only and is prohibited; state is inspected using non-mutating read-only tooling.
2. **Application-Consistent Backups**: Writers (`cordbrief-collector` and `cordbrief-core`) are cleanly stopped and verified stopped before any backup tarballs or volume seeds are created.
3. **Hypothesis-Driven Preflight**: Deployment paths (e.g. `/opt/cordbrief`) and volume names (`cordbrief_core_data`, `cordbrief_exchange`) are hypotheses until confirmed by the read-only preflight audit.
4. **Code Delivery Precondition**: The canonical migration commit is not yet on origin; it must be delivered to the VPS (via authorized push or git bundle) and verified before any services are stopped.
5. **Deterministic Credential Seeding**: Discord OAuth credentials are seeded directly into the private volume `cordbrief_rpc_collector_data` using `scripts/update_secret.sh` with mode `0600` and ownership `1000:1000`, without printing secrets or touching `.env`.
6. **Best-Effort Outage Recovery**: `GET_CHANNEL` has undocumented, client-state-dependent snapshot depth with no pagination or completeness boundary. Recovery is best-effort; any outage creates a potential message coverage gap. Therefore, preparation is performed online to minimize the writer-offline window to ~2–3 minutes.
7. **Documented OAuth Scopes**: CordBrief requests strictly `rpc identify messages.read`.
8. **Non-Destructive First Retention Check**: Proves the maintenance container inspects the real production `core-ack.json` and consumed boundary before publishing evidence; physical unlinking (`--delete-certified`) is strictly deferred.
9. **Explicit Divergence Window**: If rollback occurs after the new stack has accepted writes, restoring pre-cutover snapshots discards those post-cutover events. This divergence window is explicitly acknowledged.
10. **Proven UID/GID Invariant (1000:1000)**: Both canonical production images deliberately run unprivileged as UID/GID `1000:1000` (`cordbrief:cordbrief`), verified by Dockerfiles, compose definitions, and preflight audit.

---

## 1. Read-Only VPS Preflight Audit (Ground Truth Discovery)

Before touching or mutating any service, run the read-only preflight script to establish ground truth:

```bash
chmod +x scripts/vps_preflight.sh
./scripts/vps_preflight.sh
```

### Discovery Checklist & Output Verification
The script inspects and outputs:
- **Repository Location & State**: `pwd`, `git rev-parse --show-toplevel`, `git rev-parse HEAD`, dirty files.
- **System Resources**: Available RAM (`free -m`) and disk capacity (`df -h . /var/lib/docker`).
- **Docker & Compose Environment**: Docker engine version, compose plugin version.
- **Container Inventory & User Config**: Exact names, status, image IDs, and configured user (`1000:1000`).
- **Volume Inventory & Ownership**: Discovered volume names and filesystem root ownership (`stat -c %u:%g`).
- **Journal & Core Cursor Status**:
  - Exchange events directory entries (`/var/cordbrief/exchange/events/`).
  - Active Core committed cursor (`/var/cordbrief/exchange/core-ack.json`).
  - Integrity of `core.db`, `secrets.json` (mode `0600`), and digest files.
  - Verification that the legacy collector is currently running or stopped.

> [!NOTE]
> If preflight reveals volume names or paths differing from the defaults, substitute the discovered names in subsequent commands.

---

## 2. Complete Inventory & Classification Matrix

| Path / Volume | Host / Container Location | Contents & Responsibilities | Classification | Migration Action |
|---|---|---|---|---|
| **`cordbrief_exchange`** | `/var/cordbrief/exchange/events/` | Physical immutable journal segments (`0*.ndjson`) | **MUST PRESERVE** | Seeded byte-for-byte into `cordbrief_rpc_exchange` |
| **`cordbrief_exchange`** | `/var/cordbrief/exchange/retention/` | Phase 3D certified sidecars (`*.ids.json`) | **MUST PRESERVE** | Seeded byte-for-byte into `cordbrief_rpc_exchange` |
| **`cordbrief_exchange`** | `/var/cordbrief/exchange/retention-manifest.json` | Certified segment boundaries and hashes | **MUST PRESERVE** | Seeded byte-for-byte into `cordbrief_rpc_exchange` |
| **`cordbrief_exchange`** | `/var/cordbrief/exchange/core-ack.json` | Core committed journal cursor (`segment`, `offset`) | **MUST PRESERVE** | Seeded byte-for-byte into `cordbrief_rpc_exchange` |
| **`cordbrief_exchange`** | `/var/cordbrief/exchange/watchlist.json` | Watched channel IDs and generation counter | **MUST PRESERVE** | Seeded byte-for-byte into `cordbrief_rpc_exchange` |
| **`cordbrief_exchange`** | `/var/cordbrief/exchange/catalog.json` | Guild & channel metadata | **MUST PRESERVE** | Seeded byte-for-byte into `cordbrief_rpc_exchange` |
| **`cordbrief_core_data`** | `/var/cordbrief/data/core.db` | SQLite database (ingest state, catalogs, inbox) | **MUST PRESERVE** | Seeded byte-for-byte into `cordbrief_rpc_core_data` |
| **`cordbrief_core_data`** | `/var/cordbrief/data/digests/` | Historical digest records for Web UI Inbox | **MUST PRESERVE** | Seeded byte-for-byte into `cordbrief_rpc_core_data` |
| **`cordbrief_core_data`** | `/var/cordbrief/data/config.json` | Application config (scheduler, Telegram settings) | **MUST PRESERVE** | Seeded byte-for-byte into `cordbrief_rpc_core_data` |
| **`cordbrief_core_data`** | `/var/cordbrief/data/secrets.json` | Gemini API key and Telegram bot token (mode `0600`) | **MUST PRESERVE** | Seeded byte-for-byte into `cordbrief_rpc_core_data` |
| **`cordbrief_core_data`** | `/var/cordbrief/data/scheduler-state.json` | Scheduler execution state | **MUST PRESERVE** | Seeded byte-for-byte into `cordbrief_rpc_core_data` |
| **`credentials.json`** | `/var/lib/cordbrief/credentials.json` | Discord OAuth Client ID & Secret (mode `0600`) | **MIGRATE / TRANSFORM** | Seeded into `cordbrief_rpc_collector_data` via `scripts/update_secret.sh` |
| **`oauth-token.json`** | `/var/lib/cordbrief/oauth-token.json` | Persisted OAuth2 access & refresh tokens (mode `0600`) | **NEW / FRESH** | Generated upon operator OAuth consent |
| **`cordbrief_rpc_discord_profile`** | `/home/cordbrief/.config/discord/` | Clean official unmodified Discord Linux config | **NEW / FRESH** | Fresh official client session; legacy Vencord profile abandoned |
| **`cordbrief_rpc_discord_keyring`** | `/home/cordbrief/.local/share/keyrings/` | Clean GNOME Keyring storage | **NEW / FRESH** | Fresh keyring storage; legacy keyring abandoned |
| **`cordbrief_discord_profile`** | Legacy Docker Volume | Old Vencord profile & Electron patches | **SAFE TO ABANDON** | Kept untouched for rollback; pruned only after 7-day burn-in |
| **`cordbrief_collector_data`** | Legacy Docker Volume | Old Vencord collector state | **SAFE TO ABANDON** | Kept untouched for rollback; pruned only after 7-day burn-in |

### Production User & Group Invariant (UID/GID 1000:1000)

The canonical CordBrief architecture strictly enforces non-root unprivileged execution under UID/GID `1000:1000`:
- **`collector/Dockerfile`**: creates unprivileged user `cordbrief` via `RUN useradd -u 1000 -m -s /bin/bash cordbrief` and sets `USER cordbrief`.
- **`docker/Dockerfile.core`**: creates unprivileged user `cordbrief` via `RUN adduser -D -u 1000 -s /bin/sh cordbrief` and sets `USER cordbrief`.
- **`docker/compose.yml`**: specifies `user: "1000:1000"` for both `cordbrief-collector` and `cordbrief-core`.
- **`docker/compose.retention.yml`**: runs offline maintenance under the same canonical collector user configuration.

Because both containers run as unprivileged UID/GID `1000:1000`, all files in shared volumes (`cordbrief_rpc_exchange`, `cordbrief_rpc_core_data`, `cordbrief_rpc_collector_data`) must be owned by `1000:1000` to allow uninterrupted read/write access. All volume seeding and restore commands explicitly enforce `chown -R 1000:1000` (configurable via `TARGET_UID:TARGET_GID` if resolved differently by preflight).

---

## 3. Code Delivery Precondition (Before Any Downtime)

The approved migration commit is not on `origin/main` until explicitly authorized. Do NOT stop writers or run `git checkout` until the code is verified present on the VPS.

### Delivery Method A: Authorized Push to Origin
When the operator authorizes pushing to origin:
```bash
# On workstation:
git tag -a v2.0-rpc -m "M17 canonical RPC architecture"
git push origin v2.0-rpc

# On VPS:
cd /opt/cordbrief
git fetch origin --tags
git checkout v2.0-rpc
```

### Delivery Method B: Secure Git Bundle Transfer (No Remote Push)
If deploying without pushing to a public remote:
```bash
# On workstation:
git bundle create /tmp/cordbrief-m17.bundle origin/main..HEAD
scp /tmp/cordbrief-m17.bundle user@<VPS_IP>:/tmp/

# On VPS:
cd /opt/cordbrief
git fetch /tmp/cordbrief-m17.bundle 'refs/heads/*:refs/remotes/bundle/*'
git checkout bundle/main
```

### Pre-Cutover Verification
Confirm the exact commit hash matches the reviewed checkpoint:
```bash
test "$(git rev-parse HEAD)" = "<APPROVED_COMMIT_HASH>" && echo "[OK] Commit verified"
```

---

## 4. Phase A — Online Pre-Cutover Preparation (Zero Downtime)

*The legacy stack remains running and actively ingesting traffic during Phase A.*

### Step 4.1: Pre-Build Canonical Docker Images
```bash
docker compose -f docker/compose.yml build
```

### Step 4.2: Pre-Create Target Volumes
```bash
docker volume create cordbrief_rpc_exchange
docker volume create cordbrief_rpc_core_data
docker volume create cordbrief_rpc_collector_data
docker volume create cordbrief_rpc_discord_profile
docker volume create cordbrief_rpc_discord_keyring
docker volume create cordbrief_rpc_discord_runtime
```

---

## 5. Phase B — Application-Consistent Cutover (Outage Window)

*The writer-offline interval begins here (~2–3 minutes total).*

### Step 5.1: Clean Stop of Writers
Stop both containers cleanly to flush SQLite WAL and journal segments:
```bash
docker compose stop cordbrief-collector cordbrief-core
```

### Step 5.2: Verify Writers Are Confirmed Stopped
```bash
docker compose ps
# Ensure both containers show "Exited (0)" and no processes hold database locks:
test -z "$(docker ps -q --filter name=cordbrief)" && echo "[OK] Writers cleanly stopped"
```

### Step 5.3: Application-Consistent Checksummed Backup
Capture stopped volumes read-only (`:ro`):
```bash
BACKUP_DIR="/var/backups/cordbrief/pre-m17-$(date +%Y%m%d_%H%M%S)"
mkdir -p "$BACKUP_DIR"

# 1. Archive legacy volumes
for vol in cordbrief_exchange cordbrief_core_data cordbrief_collector_data; do
  docker run --rm -v ${vol}:/source:ro -v "$BACKUP_DIR":/backup alpine \
    tar -czf "/backup/${vol}.tar.gz" -C /source .
done

# 2. Archive host repository and .env
tar -czf "$BACKUP_DIR/cordbrief_repo.tar.gz" -C /opt/cordbrief .
cp /opt/cordbrief/.env "$BACKUP_DIR/.env.bak" 2>/dev/null || true

# 3. Checksums and read-only lockdown
cd "$BACKUP_DIR"
sha256sum *.tar.gz > SHA256SUMS
chmod -R 0400 "$BACKUP_DIR"/*
chmod 0500 "$BACKUP_DIR"
echo "[OK] Application-consistent backup completed at $BACKUP_DIR"
cat SHA256SUMS
```

### Step 5.4: Seed Canonical Volumes from Stopped Snapshot
Copy data directly into the isolated RPC volumes with correct permissions:
```bash
docker run --rm \
  -v cordbrief_exchange:/src_exchange:ro \
  -v cordbrief_core_data:/src_core:ro \
  -v cordbrief_rpc_exchange:/dst_exchange \
  -v cordbrief_rpc_core_data:/dst_core \
  alpine sh -c '
    set -e
    echo "Seeding exchange..."
    cp -a /src_exchange/. /dst_exchange/
    chown -R 1000:1000 /dst_exchange
    
    echo "Seeding core data..."
    cp -a /src_core/. /dst_core/
    chown -R 1000:1000 /dst_core
    
    echo "Seeding complete."
  '
```

### Step 5.5: Seed Discord OAuth Credentials Deterministically
Run the standalone credentials updater, specifying the destination volume explicitly:
```bash
chmod +x scripts/update_secret.sh
./scripts/update_secret.sh cordbrief_rpc_collector_data
```
- Prompts for `DISCORD_CLIENT_SECRET` silently (`read -s`).
- Writes `/var/lib/cordbrief/credentials.json` directly into `cordbrief_rpc_collector_data`.
- Enforces permissions `0600` and ownership `1000:1000`.
- Secret is never echoed, logged, or written to host disk.

### Step 5.6: Launch Canonical RPC Stack
```bash
docker compose -f docker/compose.yml up -d
```

---

## 6. Interactive Operator Sign-In & Authorization

Both the Web UI (`28741`) and temporary Xpra shadow viewer (`28742`) bind strictly to `127.0.0.1` on the VPS.

### Step 6.1: Establish Secure SSH Loopback Tunnel
From the **local workstation terminal**, forward both loopback ports:
```bash
ssh -N -L 28741:127.0.0.1:28741 -L 28742:127.0.0.1:28742 user@<VPS_IP>
```
*(Keep this terminal running during setup).*

### Step 6.2: Discord Mobile QR Sign-In
1. Open local browser to **`http://127.0.0.1:28742/`**.
2. The Xpra HTML5 viewer displays official unmodified Linux Discord.
3. Use the Discord mobile app to scan the displayed QR code (User Settings → Scan QR Code).
4. Discord authenticates and loads into the client interface.

### Step 6.3: Approve CordBrief OAuth Consent Dialog
1. Within 2–5 seconds of Discord loading, the CordBrief daemon detects IPC and triggers the authorization prompt.
2. The Discord client presents the authorization window requesting scopes:
   - `rpc`
   - `identify`
   - `messages.read`
3. Click the purple **Authorize** button.
4. **Automatic Headless Transition**:
   - Daemon exchanges authorization code for tokens and persists `oauth-token.json` (`mode 0600`).
   - Daemon shuts down the Xpra process and closes port `28742`.
   - Browser on `http://127.0.0.1:28742/` disconnects (expected).
   - Daemon enters unattended normal operation (`collector_state: "running"`, `mode: "normal"`).

---

## 7. Post-Migration Verification Checklist

Execute these checks to verify end-to-end operational integrity:

### 1. Collector State Machine
```bash
docker exec cordbrief-collector cat /var/cordbrief/exchange/collector-status.json
```
- **Expect**:
  ```json
  {
    "mode": "normal",
    "collector_state": "running",
    "discord_authenticated": true,
    "last_error": null,
    "action_required": null
  }
  ```

### 2. Attack Surface Hardening (Port 28742 Closed)
```bash
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:28742/ || echo "Port 28742 safely closed"
```
- **Expect**: Connection refused (`Port 28742 safely closed`).

### 3. Cursor Continuity & Zero Duplication
```bash
docker exec cordbrief-core cat /var/cordbrief/exchange/core-ack.json
```
- **Expect**: `segment` and `offset` match the pre-migration cursor, proving Core did not reset and did not re-ingest past messages.

### 4. Live Message Capture
1. Post a test message in a watched channel (e.g. `#cb-test`).
2. Inspect the latest journal events:
   ```bash
   docker exec cordbrief-collector tail -n 5 /var/cordbrief/exchange/events/$(ls -t /var/cordbrief/exchange/events | head -1)
   ```
   - **Expect**: Message appears as a valid schema v1 `message_create` event within < 2 seconds.
3. Verify Core advances cursor:
   ```bash
   docker exec cordbrief-core cat /var/cordbrief/exchange/core-ack.json
   ```
   - **Expect**: `offset` increases to encompass the live event.

### 5. Web UI & Inbox Integrity
Open **`http://127.0.0.1:28741/`** in local browser:
- Historical digests are visible and formatted correctly.
- Watched channels match pre-migration watchlist.

### 6. Unattended Restart Test
```bash
docker restart cordbrief-collector
sleep 15
docker exec cordbrief-collector cat /var/cordbrief/exchange/collector-status.json
```
- **Expect**: `collector_state: "running"`, `last_error: null`, zero operator intervention required.

---

## 8. First Real Post-Migration Retention Check

> [!IMPORTANT]
> **Strict Non-Destructive Policy**: Never run `--delete-certified` during initial cutover validation.

1. Allow the canonical stack to operate until Core naturally consumes past at least one closed journal segment ($N$).
2. Verify `core-ack.json` has advanced past segment $N$:
   ```bash
   docker exec cordbrief-core cat /var/cordbrief/exchange/core-ack.json
   # Must show segment > N
   ```
3. Stop services cleanly for maintenance:
   ```bash
   docker compose -f docker/compose.yml stop cordbrief-core cordbrief-collector
   ```
4. Run non-destructive certification:
   ```bash
   docker compose -f docker/compose.yml -f docker/compose.retention.yml run --rm cordbrief-collector \
     bash /home/cordbrief/collector/retention-publish.sh N
   ```
   - Invariant: Maintenance container mounts `cordbrief_rpc_core_data`, verifies the actual production `core-ack.json`, validates digest citations, and publishes sidecars and manifests.
   - **Zero segments are deleted or unlinked.**
5. Restart canonical stack:
   ```bash
   docker compose -f docker/compose.yml up -d
   ```

---

## 9. Compliant Rollback Policy & Divergence Window

> [!CAUTION]
> **The legacy Vencord collector, CDP collector, patched Discord, or user-token/private-endpoint collector MUST NEVER be restarted.**

### Permitted Rollback Actions
1. Stop and remove the failed canonical RPC stack:
   ```bash
   docker compose -f docker/compose.yml down
   ```
2. **State Divergence Analysis**:
   - **Pre-Ingest Rollback** (failure occurred before new stack accepted writes or before Core ingested new events):
     Pre-cutover data in legacy volumes is completely untouched. Restoring backup snapshots leaves zero data loss.
   - **Post-Ingest Rollback** (failure occurred after new stack was running and accepted live messages):
     New messages exist only in `cordbrief_rpc_exchange`. Reverting Core/exchange to pre-cutover backups creates an explicit divergence window: any messages ingested between cutover and rollback will be discarded. The operator must inspect `/var/cordbrief/exchange/events/` and note the highest message ID before deciding to overwrite with the pre-cutover snapshot.
3. **Restore Pre-Cutover State (If Necessary)**:
   ```bash
   # Restore exact pre-migration state into RPC volumes:
   # Uses find to safely remove all existing entries (including hidden dotfiles) without unmounting /dst:
   docker run --rm -v cordbrief_rpc_exchange:/dst -v "$BACKUP_DIR":/backup:ro alpine \
     sh -c "find /dst -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + && tar -xzf /backup/cordbrief_exchange.tar.gz -C /dst && chown -R 1000:1000 /dst"
   docker run --rm -v cordbrief_rpc_core_data:/dst -v "$BACKUP_DIR":/backup:ro alpine \
     sh -c "find /dst -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + && tar -xzf /backup/cordbrief_core_data.tar.gz -C /dst && chown -R 1000:1000 /dst"
   ```
4. **Safe Component Standalone Operation & Non-Mutating State Inspection**:
   - **Both Collection and Core remain STOPPED by default while diagnosing.**
   - Normal Core startup (`docker compose up -d cordbrief-core`) is **NOT read-only**: it initializes SQLite write connections, executes schema migrations, starts the cron scheduler, and risks sending automated Telegram digests. Normal Core startup is strictly prohibited during rollback unless an explicitly proven no-write/read-only mode exists.
   - **Safe Non-Mutating State Inspection**: Inspect SQLite database and journal state using read-only tooling:
     ```bash
     # Inspect Core database read-only (zero mutation risk)
     docker run --rm -v cordbrief_rpc_core_data:/data:ro alpine sh -c '
       apk add --no-cache sqlite >/dev/null 2>&1
       sqlite3 file:/data/core.db?mode=ro ".tables" "SELECT count(*) FROM digests;"
     '

     # Inspect exchange journal segments and cursor read-only
     docker run --rm -v cordbrief_rpc_exchange:/ex:ro alpine sh -c '
       ls -la /ex/events /ex/retention
       cat /ex/core-ack.json 2>/dev/null || true
     '
     ```
   - Legacy Vencord collector and failed RPC collector remain permanently offline.

---

## 10. Downtime Minimization & Best-Effort Recovery

### Best-Effort Outage Recovery Contract
- `GET_CHANNEL` recovery is **best-effort**.
- `GET_CHANNEL` has undocumented, client-state-dependent snapshot depth with no documented pagination or completeness boundary.
- **No completeness guarantee exists** for messages posted during an outage window.
- Any cutover downtime creates a potential message coverage gap.

### Optimized Maintenance Window
By moving all code transfer, docker image pre-building, and target volume preparation into Phase A (while legacy stack is running), the writer-offline window in Phase B is strictly minimized:

| Phase Step | Action | Offline Impact | Expected Duration |
|---|---|---|---|
| **Step 5.1 – 5.2** | Clean writer stop & verification | Writers stopped | 10 – 15 s |
| **Step 5.3** | Application-consistent backup & SHA-256 | Writers stopped | 20 – 30 s |
| **Step 5.4 – 5.5** | Volume seeding & secret write | Writers stopped | 15 – 25 s |
| **Step 5.6** | Canonical stack container launch | Discord starting | 10 – 15 s |
| **Step 6.1 – 6.3** | Operator QR scan & OAuth click | Waiting on operator | 45 – 90 s |
| **Total Writer-Offline Window** | | | **~2 – 3 minutes** |

---

## 11. Post-Cutover Volume Cleanup (Deferred)

Only after the RPC architecture has operated reliably in production for at least 7 days without issue:
```bash
docker volume rm cordbrief_discord_runtime
docker volume rm cordbrief_discord_profile
docker volume rm cordbrief_discord_keyring
docker volume rm cordbrief_collector_data
docker volume rm cordbrief_exchange
docker volume rm cordbrief_core_data
```
*(The immutable backup in `/var/backups/` is retained according to backup policy).*
