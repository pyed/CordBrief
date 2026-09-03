# CordBrief Collector Feasibility Spike

This directory contains the experimental infrastructure and feasibility spike for CordBrief's target two-service architecture:
1. **`cordbrief-collector`**: Headless official Discord Desktop for Linux under Xpra, patched with Vencord, capturing in-process Discord Gateway events to a shared NDJSON journal.
2. **`cordbrief-core`**: The Go application consuming the journal, summarizing via LLM, and delivering digests.

---

## Structure

- **`Dockerfile`**: Minimal Debian Bookworm Slim base with Xpra, audio/video runtime dependencies for Electron, and official Discord Desktop for Linux. Excludes full desktop environments (GNOME, KDE, XFCE).
- **`docker-compose.yml`**: Compose specification with isolated non-root user (`1000:1000`), dedicated persistent volumes, and port binding restricted to localhost.
- **`entrypoint.sh`**: Startup script managing D-Bus session, Xpra seamless display (`:100`), password store configuration (`--password-store=basic`), and Vencord plugin registration.
- **`plugin/index.ts`**: The CordBrief Collector Vencord userplugin listening to `MESSAGE_CREATE` on Discord's internal FluxDispatcher, strictly enforcing the channel allowlist, and appending events to the shared NDJSON journal.
- **`test-harness.mjs`**: Verification harness proving concurrent append safety and strict channel allowlist filtering.
- **`FEASIBILITY_REPORT.md`**: Comprehensive engineering evaluation answering the 10 core spike questions, the 10 acceptance gates, and identifying host blockers.

---

## Setup & Running the Spike

### Prerequisites
A Linux or macOS/Windows host with Docker and Docker Compose installed.

### 1. Build and Launch
```bash
cd collector-spike
docker compose up -d --build
```

### 2. Authenticate
1. Open a browser and navigate to:
   ```
   http://127.0.0.1:14500/
   ```
2. The Xpra HTML5 canvas will display the official Discord login screen.
3. Open the official Discord mobile app on your phone, navigate to **Settings > Scan QR Code**, and scan the QR code displayed on the screen.
4. Discord logs in and loads your servers.
5. Close the browser tab. Discord and Xpra continue running headlessly in the container.

### 3. Inspect Ingested Journal
```bash
docker compose exec cordbrief-collector tail -f /var/cordbrief/journal/discord_events.ndjson
```
Events will stream in real time for allowlisted channels configured in `CORDBRIEF_WATCHED_CHANNELS`.
