# CordBrief

Catch up on your Discord communities without reading every message.

CordBrief turns the channels you follow into short digests with links back to
the conversation. Read them in a local Web inbox, or send them to Telegram on a
schedule. You choose the channels, the model, and what the digest should focus on.

## How it works

The collector runs the official, unmodified Discord Linux desktop client and a
local Discord RPC collector daemon. A small Go service reads the journal,
summarizes with Gemini or an OpenAI-compatible endpoint, and keeps the digests
and their source links.

The two services communicate via local filesystem exchange. There is no database
server, no Vencord plugin, no client patching, and no Docker socket. The Go Core
uses only the standard library.

## Setup & First-Run Flow

CordBrief's setup uses the official Discord OAuth/RPC protocol and models discrete
lifecycle states:

1. **Discord starting**: Containers boot headlessly with Xvfb and local IPC.
2. **Discord login**: If not yet logged in, the on-demand Xpra shadow viewer opens at `http://127.0.0.1:28742/` so you can scan a QR code or enter credentials.
3. **CordBrief authorization**: Once Discord is signed in, CordBrief requests local RPC permissions. You approve the prompt inside Discord.
4. **Running**: Xpra automatically turns off. CordBrief streams watched channels headlessly in the background.

### Quickstart

1. **Configure Discord Application**: Create an app in the [Discord Developer Portal](https://discord.com/developers/applications) with redirect URI `http://127.0.0.1:32145/callback`. Copy `.env.example` to `.env`, set `DISCORD_CLIENT_ID`, and seed your Client Secret via `./scripts/update_secret.sh` (Linux/macOS) or `.\scripts\update_secret.ps1` (Windows). (See [Setup Guide](docs/SETUP.md)).
2. **Start Services**:
   ```sh
   docker compose -f docker/compose.yml up -d
   ```
3. **Authorize**:
   - Open [the setup screen](http://127.0.0.1:28742) in your browser to log into Discord and approve the authorization prompt.
   - Open [your inbox](http://127.0.0.1:28741) to select channels and configure your summary model.

See [Setup Guide](docs/SETUP.md) for remote server access, credentials storage, and troubleshooting.

## Before you run it

Watched messages are stored on disk and sent to your chosen model when generating
a digest. Keep the Web UI private and only collect conversations you have permission
to process. Summaries can be wrong; the source links are there to check them.

[Architecture](docs/ARCHITECTURE.md) · [Recovery](docs/RECOVERY_CONTRACT.md) ·
[Retention](docs/RETENTION.md) · [Contributing](CONTRIBUTING.md)

[MIT licensed](LICENSE). Discord and upstream components retain their own licenses and terms.
