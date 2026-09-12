# CordBrief

Catch up on Discord without reading every message.

CordBrief watches the channels you choose, stores new messages locally, and turns them into short digests with links back to the original conversation. Read digests in the local Web inbox or optionally send them to Telegram.

## What CordBrief v2 uses

CordBrief v2 uses:

- the official, unmodified Discord Desktop client
- Discord local RPC/OAuth
- a small Go Core that uses only the standard library
- local filesystem exchange between services
- optional Telegram delivery over outbound HTTPS

It does **not** use Vencord, user-token scraping, private Discord endpoints, self-bots, CDP/browser automation, a database server, or the Docker socket.

## How it works

**Collector**

Runs Discord Desktop and the local RPC collector. Live watched-channel messages are written to a durable NDJSON journal.

**Core**

Reads the journal, generates digests with Gemini or an OpenAI-compatible endpoint, and serves the local Web inbox.

Local interfaces:

- Inbox: `http://127.0.0.1:28741`
- Setup / Discord login: `http://127.0.0.1:28742`

The setup viewer is only needed when Discord login or authorization requires manual interaction.

## Quick setup

### 1. Create a Discord application

Create an application in the [Discord Developer Portal](https://discord.com/developers/applications).

Add this redirect URI:

```text
http://127.0.0.1:32145/callback
```

Copy the example environment file:

```sh
cp .env.example .env
```

Set your Discord Client ID in `.env`:

```text
DISCORD_CLIENT_ID=your_client_id
```

Store the Client Secret with the included helper.

Linux/macOS:

```sh
./scripts/update_secret.sh
```

Windows:

```powershell
.\scripts\update_secret.ps1
```

### 2. Start CordBrief

```sh
docker compose -f docker/compose.yml up -d
```

### 3. Log in and authorize

Open:

```text
http://127.0.0.1:28742
```

Log in to the official Discord client if needed, then approve CordBrief's local RPC/OAuth authorization prompt.

### 4. Configure CordBrief

Open:

```text
http://127.0.0.1:28741
```

Choose the channels to watch, configure your summary model, and set up optional Telegram delivery.

For remote-server setup and troubleshooting, see [docs/SETUP.md](docs/SETUP.md).

## Recovery

Live Discord events are authoritative while the RPC connection is active.

After an interruption, CordBrief can use Discord `GET_CHANNEL` snapshots to recover messages that are still visible to the local client. Snapshot depth is not fixed, so recovery is **bounded and best-effort**, not guaranteed to be lossless across arbitrary downtime.

CordBrief stores recovery provenance in:

- `recovery-state.json`
- `recovery-state.json.anchors`

If recovery provenance is missing, inconsistent, corrupt, or unreadable, CordBrief fails closed instead of silently creating a newer baseline.

If journal durability becomes uncertain after a write, the collector exits non-zero so the supervisor can restart from durable on-disk state.

See [docs/RECOVERY_CONTRACT.md](docs/RECOVERY_CONTRACT.md) for details.

## Retention

CordBrief rotates journal segments and uses certified retention metadata so old segments can be removed safely.

See [docs/RETENTION.md](docs/RETENTION.md).

## Privacy and safety

Watched messages are stored locally and sent to your configured model when generating a digest.

Keep the Web UI private and only process conversations you have permission to collect.

Back up `recovery-state.json` and `recovery-state.json.anchors` together.

## Documentation

- [Setup](docs/SETUP.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Recovery](docs/RECOVERY_CONTRACT.md)
- [Retention](docs/RETENTION.md)
- [Contributing](CONTRIBUTING.md)

[MIT licensed](LICENSE). Discord and upstream components retain their own licenses and terms.
