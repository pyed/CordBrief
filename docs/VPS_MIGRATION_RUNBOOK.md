# CordBrief M17: VPS Migration Runbook
## Legacy Vencord to Canonical Official Discord RPC Architecture

**Target Baseline:** `dd2e1221d04c0b1082194ef378fb227d9bf26697`  
**Execution Mode:** Reversible, non-destructive, zero-data-loss cutover.  
**Estimated Downtime:** 3 – 5 minutes (including operator interactive Discord sign-in).

---

## 1. Migration Inventory & Classification

All persistent state on the legacy deployment is cataloged and classified below:

| Legacy VPS Volume / Path | Host / Container Location | Contents & Purpose | Migration Classification | Target RPC Stack Destination |
|---|---|---|---|---|
| **`cordbrief_exchange`** | `/var/cordbrief/exchange/events/` | Physical journal segments (`*.ndjson`) containing immutable raw event logs | **MUST PRESERVE** | `cordbrief_rpc_exchange:/var/cordbrief/exchange/events/` |
| **`cordbrief_exchange`** | `/var/cordbrief/exchange/retention/` | Phase 3D certified sidecars (`*.ids.json`) for retired segments | **MUST PRESERVE** | `cordbrief_rpc_exchange:/var/cordbrief/exchange/retention/` |
| **`cordbrief_exchange`** | `/var/cordbrief/exchange/retention-manifest.json` | Authoritative manifest of certified/retired segment boundaries and SHA-256 hashes | **MUST PRESERVE** | `cordbrief_rpc_exchange:/var/cordbrief/exchange/retention-manifest.json` |
| **`cordbrief_exchange`** | `/var/cordbrief/exchange/core-ack.json` | Core's committed journal cursor (`segment`, `offset`) | **MUST PRESERVE** | `cordbrief_rpc_exchange:/var/cordbrief/exchange/core-ack.json` |
| **`cordbrief_exchange`** | `/var/cordbrief/exchange/watchlist.json` | Watched channel IDs and generation counter | **MUST PRESERVE** | `cordbrief_rpc_exchange:/var/cordbrief/exchange/watchlist.json` |
| **`cordbrief_exchange`** | `/var/cordbrief/exchange/catalog.json` | Guild & channel catalog metadata | **MUST PRESERVE** | `cordbrief_rpc_exchange:/var/cordbrief/exchange/catalog.json` |
| **`cordbrief_exchange`** | `/var/cordbrief/exchange/collector-status.json` | Ephemeral runtime status | **SAFE TO ABANDON** | Regenerated dynamically on startup by RPC daemon |
| **`cordbrief_exchange`** | `/var/cordbrief/exchange/collector-command*.json` | Ephemeral IPC commands between Core and Collector | **SAFE TO ABANDON** | Generated on-demand |
| **`cordbrief_core_data`** | `/var/cordbrief/data/digests/` | Historical digest artifacts (`<batch_id>.json`) for Web UI Inbox | **MUST PRESERVE** | `cordbrief_rpc_core_data:/var/cordbrief/data/digests/` |
| **`cordbrief_core_data`** | `/var/cordbrief/data/config.json` | Application configuration (scheduler time/timezone, LLM provider, Telegram settings) | **MUST PRESERVE** | `cordbrief_rpc_core_data:/var/cordbrief/data/config.json` |
| **`cordbrief_core_data`** | `/var/cordbrief/data/secrets.json` | Gemini API key and Telegram bot token (mode `0600`) | **MUST PRESERVE** | `cordbrief_rpc_core_data:/var/cordbrief/data/secrets.json` |
| **`cordbrief_core_data`** | `/var/cordbrief/data/scheduler-state.json` | Scheduler execution state (prevents duplicate daily summaries) | **MUST PRESERVE** | `cordbrief_rpc_core_data:/var/cordbrief/data/scheduler-state.json` |
| **`cordbrief_core_data`** | `/var/cordbrief/data/commit.lock` | Core commit / retention publication advisory lockfile | **MUST PRESERVE** | `cordbrief_rpc_core_data:/var/cordbrief/data/commit.lock` |
| **`cordbrief_collector_data`** | `/var/lib/cordbrief/recovery-state.json` | Channel checkpoint mapping (`last_recovered_message_id`, `checkpoint_journal_boundary`) | **MUST PRESERVE** | `cordbrief_rpc_collector_data:/var/lib/cordbrief/recovery-state.json` |
| **`cordbrief_collector_data`** | `/var/lib/cordbrief/credentials.json` | Discord Developer Application Client ID & Secret (`mode 0600`) | **NEW / CONFIGURE FRESH** | `cordbrief_rpc_collector_data:/var/lib/cordbrief/credentials.json` |
| **`cordbrief_collector_data`** | `/var/lib/cordbrief/oauth-token.json` | Persisted OAuth2 access & refresh tokens (`mode 0600`) | **NEW / FRESH** | Generated upon operator OAuth consent |
| **`cordbrief_discord_profile`** | `/home/cordbrief/.config/discord/` | Old Vencord / Electron profile, cache, and patched scripts | **SAFE TO ABANDON** | Replaced by clean official profile `cordbrief_rpc_discord_profile` |
| **`cordbrief_discord_keyring`** | `/home/cordbrief/.local/share/keyrings/` | Old GNOME Keyring database | **SAFE TO ABANDON** | Replaced by clean official keyring `cordbrief_rpc_discord_keyring` |
| **`cordbrief_discord_runtime`** | `/var/cordbrief/runtime/` | Old Vencord multi-release staging directory (`releases/`, `current/`) | **SAFE TO ABANDON** | Replaced by clean runtime lock volume `cordbrief_rpc_discord_runtime` |
| **Host `.env`** | `/opt/cordbrief/.env` | Environment secrets (`GEMINI_API_KEY`, `TELEGRAM_BOT_TOKEN`, `DISCORD_CLIENT_ID`, `DISCORD_CLIENT_SECRET`) | **MUST PRESERVE / ENRICH** | Updated in place with Discord Application credentials |

### Architectural Decision: Fresh Discord Profile & OAuth
The legacy profile volume (`cordbrief_discord_profile`) was patched by Vencord, modified across Electron upgrades, and contains legacy cache artifacts. Reusing it risks client crashes or state contamination in the unmodified official Linux Discord client. 

Therefore, **we start with a clean official Discord profile and keyring**. The operator will perform an official one-time Discord sign-in (via QR code) and approve the CordBrief OAuth consent prompt over loopback Xpra via an SSH tunnel.

---

## 2. Pre-Migration Immutable Backup

Before any mutation occurs, all existing Docker volumes and host files must be captured in an immutable, read-only snapshot.

### Step 2.1: Cleanly Stop Legacy Containers
On the VPS host:
```bash
cd /opt/cordbrief
docker compose -f docker/compose.yml stop
```

### Step 2.2: Capture Compressed Volume Snapshot
Mount all legacy volumes **read-only (`:ro`)** into a disposable helper container:
```bash
BACKUP_DIR="/var/backups/cordbrief-$(date +%Y%m%d_%H%M%S)"
mkdir -p "$BACKUP_DIR"

docker run --rm \
  -v cordbrief_exchange:/src/exchange:ro \
  -v cordbrief_core_data:/src/core_data:ro \
  -v cordbrief_collector_data:/src/collector_data:ro \
  -v cordbrief_discord_profile:/src/discord_profile:ro \
  -v cordbrief_discord_keyring:/src/discord_keyring:ro \
  -v cordbrief_discord_runtime:/src/discord_runtime:ro \
  -v "$BACKUP_DIR":/backup \
  debian:bookworm-slim \
  tar -czf /backup/cordbrief_legacy_volumes_complete.tar.gz -C /src .
```

### Step 2.3: Capture Host Repo & Environment Backup
```bash
tar -czf "$BACKUP_DIR/cordbrief_repo_and_env.tar.gz" -C /opt/cordbrief .env docker/compose.yml
```

### Step 2.4: Generate SHA-256 Checksums and Lock Permissions
```bash
sha256sum "$BACKUP_DIR"/*.tar.gz > "$BACKUP_DIR/SHA256SUMS"
chmod 0400 "$BACKUP_DIR"/*
echo "[OK] Immutable pre-migration backup completed at $BACKUP_DIR"
cat "$BACKUP_DIR/SHA256SUMS"
```

---

## 3. Reversible Volume Seeding & Code Cutover

The new canonical Compose file utilizes isolated volumes with the `cordbrief_rpc_*` prefix. The legacy `cordbrief_*` volumes remain untouched as an immediate rollback guarantee.

### Step 3.1: Create New Canonical Volumes
```bash
docker volume create cordbrief_rpc_exchange
docker volume create cordbrief_rpc_core_data
docker volume create cordbrief_rpc_collector_data
docker volume create cordbrief_rpc_discord_profile
docker volume create cordbrief_rpc_discord_keyring
docker volume create cordbrief_rpc_discord_runtime
```

### Step 3.2: Seed Data from Legacy Volumes to RPC Volumes
Run a transient helper container to copy only the durable, classified state into the new volumes with strict ownership and permission preservation:

```bash
docker run --rm \
  -v cordbrief_exchange:/old_exchange:ro \
  -v cordbrief_core_data:/old_core:ro \
  -v cordbrief_collector_data:/old_collector:ro \
  -v cordbrief_rpc_exchange:/new_exchange \
  -v cordbrief_rpc_core_data:/new_core \
  -v cordbrief_rpc_collector_data:/new_collector \
  debian:bookworm-slim bash -c "
    set -eo pipefail
    
    echo 'Seeding exchange volume...'
    mkdir -p /new_exchange/events /new_exchange/retention
    cp -a /old_exchange/events/* /new_exchange/events/ 2>/dev/null || true
    cp -a /old_exchange/retention/* /new_exchange/retention/ 2>/dev/null || true
    [ -f /old_exchange/retention-manifest.json ] && cp -p /old_exchange/retention-manifest.json /new_exchange/
    [ -f /old_exchange/core-ack.json ] && cp -p /old_exchange/core-ack.json /new_exchange/
    [ -f /old_exchange/watchlist.json ] && cp -p /old_exchange/watchlist.json /new_exchange/
    [ -f /old_exchange/catalog.json ] && cp -p /old_exchange/catalog.json /new_exchange/
    chown -R 1000:1000 /new_exchange
    
    echo 'Seeding core_data volume...'
    mkdir -p /new_core/digests
    [ -d /old_core/digests ] && cp -a /old_core/digests/* /new_core/digests/ 2>/dev/null || true
    [ -f /old_core/config.json ] && cp -p /old_core/config.json /new_core/
    [ -f /old_core/secrets.json ] && cp -p /old_core/secrets.json /new_core/
    [ -f /old_core/scheduler-state.json ] && cp -p /old_core/scheduler-state.json /new_core/
    touch /new_core/commit.lock
    chown -R 1000:1000 /new_core
    [ -f /new_core/secrets.json ] && chmod 0600 /new_core/secrets.json || true
    
    echo 'Seeding collector_data volume...'
    mkdir -p -m 0700 /new_collector
    [ -f /old_collector/recovery-state.json ] && cp -p /old_collector/recovery-state.json /new_collector/
    chown -R 1000:1000 /new_collector
    chmod 0700 /new_collector
    
    echo 'Volume seeding complete.'
  "
```

### Step 3.3: Verify Seeded Volume Contents
```bash
docker run --rm \
  -v cordbrief_rpc_exchange:/ex:ro \
  -v cordbrief_rpc_core_data:/core:ro \
  -v cordbrief_rpc_collector_data:/col:ro \
  debian:bookworm-slim bash -c "
    echo '--- Exchange Files ---' && ls -la /ex /ex/events
    echo '--- Core Files ---' && ls -la /core
    echo '--- Collector Files ---' && ls -la /col
  "
```

### Step 3.4: Switch Code to Canonical Baseline & Configure Credentials
Fetch the canonical baseline and update `.env` with the Discord Application Client ID & Secret:
```bash
cd /opt/cordbrief
git fetch origin
git checkout dd2e1221d04c0b1082194ef378fb227d9bf26697

# Ensure Discord Developer Application credentials are configured in .env:
# (Replace values with your registered Discord Developer Application)
cat << 'EOF' >> .env
DISCORD_CLIENT_ID=1547744191122247772
DISCORD_CLIENT_SECRET=YOUR_DISCORD_CLIENT_SECRET
EOF
chmod 0600 .env
```

### Step 3.5: Build and Start Canonical Stack
```bash
docker compose -f docker/compose.yml build
docker compose -f docker/compose.yml up -d
```

---

## 4. Interactive Operator Sign-In & Authorization

Both the Web UI (`28741`) and Xpra shadow viewer (`28742`) bind strictly to `127.0.0.1` on the VPS.

### Step 4.1: Establish Secure SSH Tunnel
From the **local workstation terminal**, forward both loopback ports:
```bash
ssh -N -L 28741:127.0.0.1:28741 -L 28742:127.0.0.1:28742 user@vps-ip
```
*(Leave this terminal window running.)*

### Step 4.2: Perform Discord Sign-In (Interactive)
1. Open local browser at: **<http://127.0.0.1:28742/>**
2. The Xpra HTML5 viewer displays the official unmodified Discord desktop client login screen.
3. Use the Discord mobile app to scan the QR code (or enter email/password + 2FA).
4. Discord loads into its main client interface.

### Step 4.3: Approve CordBrief OAuth Authorization (Interactive)
1. Within 2–5 seconds of Discord loading, the CordBrief daemon connects via local IPC.
2. An OAuth consent dialog pops up inside the Discord window:  
   *"CordBrief wants to access your Discord account"*
3. Click the purple **"Authorize"** button.
4. **Automatic Handoff**:
   - The daemon captures the authorization code, exchanges it for access & refresh tokens, and saves `/var/lib/cordbrief/oauth-token.json` (`mode 0600`).
   - The daemon immediately terminates the on-demand Xpra shadow viewer.
   - The browser at `127.0.0.1:28742` will show connection closed/disconnected.
   - Collector transitions to `collector_state: "running"`, `mode: "normal"`.

---

## 5. Post-Migration Verification Checklist

Execute these checks on the VPS host to confirm full operational integrity:

### 1. Container & Process Status
```bash
docker compose -f docker/compose.yml ps
```
- **Expect:** Both `cordbrief-collector` and `cordbrief-core` are `Up` and `(healthy)`.

### 2. Collector State Machine & Xpra Port Closure
```bash
docker exec cordbrief-collector cat /var/cordbrief/exchange/collector-status.json
```
- **Expect:**
  ```json
  {
    "mode": "normal",
    "collector_state": "running",
    "discord_authenticated": true,
    "last_error": null,
    "action_required": null
  }
  ```
- Verify Xpra is shut down on loopback:
  ```bash
  curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:28742/ || echo "Port closed (expected)"
  ```
  - **Expect:** Connection refused / failure.

### 3. Core Health & Inbox History Continuity
Open **<http://127.0.0.1:28741/>** in the local browser via the SSH tunnel:
- **Inbox:** All historical digests generated prior to migration are visible, browsable, and formatted correctly.
- **Watchlist:** Active watched channels match the pre-migration watchlist.
- **Settings:** LLM configuration, schedule time, and Telegram chat destination match pre-migration values.

### 4. Journal Cursor & No-Duplication Check
Inspect Core cursor and active segment:
```bash
docker exec cordbrief-core cat /var/cordbrief/exchange/core-ack.json
```
- **Expect:** The `segment` and `offset` match the pre-migration values, proving Core did not reset to `0` and did not re-ingest past messages.

### 5. Live Traffic End-to-End Ingestion Check
1. Send a controlled live message (e.g., `"M17 VPS cutover live probe"`) in one of the watched Discord channels.
2. Inspect the latest journal events:
   ```bash
   docker exec cordbrief-collector tail -n 5 /var/cordbrief/exchange/events/$(ls -t /var/cordbrief/exchange/events | head -1)
   ```
   - **Expect:** The message appears as a valid schema v1 `message_create` event within 1–2 seconds.
3. Verify Core advances its cursor:
   ```bash
   docker exec cordbrief-core cat /var/cordbrief/exchange/core-ack.json
   ```
   - **Expect:** `offset` increases to encompass the new live record.

### 6. Outbound Delivery Check
In the Web UI (<http://127.0.0.1:28741/>), click **"Send Test Message"** under Telegram Settings (if Telegram is enabled) to verify outbound delivery connectivity.

---

## 6. First Real Post-Migration Retention Check

Do not run garbage collection immediately following migration. Wait until Core has naturally progressed past at least one closed journal segment.

### Step 6.1: Verify Core Has Consumed Past Segment N
Check `core-ack.json`:
```bash
cat /var/cordbrief/exchange/core-ack.json
```
Ensure `segment > N` (e.g., `segment == 2`, allowing retirement of segment `1`).

### Step 6.2: Non-Destructive Certification Run (NO GC)
Stop the running services to release kernel locks:
```bash
cd /opt/cordbrief
docker compose -f docker/compose.yml stop
```

Execute the maintenance container to certify segment `N` without deletion:
```bash
docker compose -f docker/compose.yml -f docker/compose.retention.yml run --rm cordbrief-collector \
  bash /home/cordbrief/collector/retention-publish.sh N
```
- **Verified Invariants:**
  - Maintenance container mounts `cordbrief_rpc_core_data` and inspects `core-ack.json`.
  - Confirms Core has consumed past segment `N`.
  - Validates digest citations in `/var/cordbrief/data/digests/`.
  - Publishes sidecar `/var/cordbrief/exchange/retention/000000000000000N.ids.json`.
  - Publishes updated `/var/cordbrief/exchange/retention-manifest.json` with `retired_through: N`.
  - **No segments are unlinked or deleted.**

### Step 6.3: Inspect Published Evidence & Resume Services
```bash
cat /var/cordbrief/exchange/retention-manifest.json
docker compose -f docker/compose.yml start
```
Confirm the stack returns to `collector_state: "running"` and Core continues operating normally with the published manifest.

### Step 6.4: Subsequent Certified Deletion (Optional)
Only after observing stable operation across an additional digest cycle may physical unlinking be performed:
```bash
docker compose -f docker/compose.yml stop
docker compose -f docker/compose.yml -f docker/compose.retention.yml run --rm cordbrief-collector \
  bash /home/cordbrief/collector/retention-publish.sh N --delete-certified
docker compose -f docker/compose.yml start
```

---

## 7. Rollback Sequence (< 60 Seconds)

If any unrecoverable issue arises during cutover (e.g. operator unable to authenticate Discord, network failure, or client incompatibility), rollback is instantaneous because the legacy `cordbrief_*` volumes were never mutated.

```bash
cd /opt/cordbrief

# 1. Stop the canonical RPC stack
docker compose -f docker/compose.yml down

# 2. Revert working tree to pre-migration baseline
git checkout 244f4a47f9c322cae0c8355f93ee4f39d28a6995

# 3. Start legacy Vencord services using untouched original volumes
docker compose -f docker/compose.yml up -d

# 4. Confirm legacy stack restored
docker compose -f docker/compose.yml ps
docker logs --tail 20 cordbrief-core
```

---

## 8. Post-Cutover Volume Cleanup (Deferred)

Only after the RPC architecture has operated reliably in production for at least 48 hours without issue:
```bash
# Remove obsolete legacy volumes after verified cutover
docker volume rm cordbrief_discord_runtime
docker volume rm cordbrief_discord_profile
docker volume rm cordbrief_discord_keyring
docker volume rm cordbrief_collector_data
docker volume rm cordbrief_exchange
docker volume rm cordbrief_core_data
```
*(Keep the immutable backup in `/var/backups/` according to your retention policy.)*
