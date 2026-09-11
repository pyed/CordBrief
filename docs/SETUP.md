# Running CordBrief

## Prerequisites & Requirements

- x86-64 host with Docker's Linux engine and Docker Compose v2.24 or newer.
- 1 vCPU and 2 GB RAM minimum.
- A Discord account.

## 1. Discord Developer Application Setup

CordBrief connects to your local Discord desktop client via official local Discord RPC over a Unix domain socket. To enable local RPC authorization:

1. Open the [Discord Developer Portal](https://discord.com/developers/applications) and sign in.
2. Click **New Application**, enter a name (e.g., `CordBrief`), and create the application.
3. In the left navigation, click **OAuth2**:
   - Under **Redirects**, click **Add Redirect** and add:
     ```text
     http://127.0.0.1:32145/callback
     ```
   - Click **Save Changes**.
4. Copy your **Client ID**.
5. Under **Client Secret**, click **Reset Secret** (or copy existing) to get your secret.

### Configure Credentials

Copy `.env.example` to `.env`:

```sh
cp .env.example .env
```

Set your Client ID in `.env`:
```env
DISCORD_CLIENT_ID=your_client_id_here
```

For the **Client Secret**, choose one of two methods:
- **Recommended (Secure In-Volume Storage)**: Store the secret directly in private container volume storage (`0600`) without saving it in plaintext `.env` on disk:
  ```sh
  # Linux / macOS
  ./scripts/update_secret.sh cordbrief_rpc_collector_data

  # Windows PowerShell
  .\scripts\update_secret.ps1
  ```
- **Direct Environment**: Set `DISCORD_CLIENT_SECRET=your_client_secret` in `.env`.

> [!NOTE]
> CordBrief requests strictly the following OAuth2 scopes: `rpc`, `identify`, `messages.read`.

## 2. First Start & Setup Flow

Run from the repository root:

```sh
docker compose -f docker/compose.yml up -d
```

### Setup Sequence

CordBrief enforces a clean, discrete setup sequence:

1. **Discord Login**: If not already logged into Discord, the collector opens an on-demand Xpra shadow viewer at <http://127.0.0.1:28742>. Open that URL in your browser to sign in via QR code or credentials.
2. **CordBrief Authorization**: Once Discord authenticates, CordBrief requests local RPC permissions. A consent prompt appears inside Discord. Approve the prompt.
3. **Running**: Upon authorization, CordBrief saves credentials privately (`0600`), shuts down Xpra to eliminate background resource usage, and transitions to headless collection.

Open <http://127.0.0.1:28741> for your inbox. Select the channels to watch, configure
your summary model, and generate a test digest.

## Models and Delivery

Settings supports Gemini and an OpenAI-compatible endpoint that requires no API
key, such as a local model server. Select a model your provider actually makes available; the bundled
default may not be available to your account. `localhost` inside Core means the
container, not your host. Use an address reachable from that container.

You can enter the Gemini key and optional Telegram bot token in Settings. They
are stored in Core's private volume. Alternatively, copy `.env.example` to `.env`
in the repository root and fill in the values; nonempty environment values take
precedence over saved credentials. Recreate Core after changing `.env`:

```sh
docker compose -f docker/compose.yml up -d --force-recreate cordbrief-core
```

Telegram is optional. Configure its destination and enable delivery explicitly.
The inbox works without it. A remote model receives the selected message text;
Telegram receives the resulting digest when delivery is enabled.

## Headless Hosts and Privacy

Both screens bind to the host's loopback interface. They have no application
login and must not be exposed directly to the Internet. For a remote host, keep
those bindings and use an SSH tunnel:

```sh
ssh -L 28741:127.0.0.1:28741 -L 28742:127.0.0.1:28742 user@your-host
```

Then open the same local URLs. The setup viewer controls a signed-in Discord
session; keep it private.

## Restarts, Reauthorization, and Data

If Discord ever requires reauthorization or session repair:
- The Core Web UI displays an alert guiding you to open the on-demand viewer.
- The collector activates Xpra only while waiting for interaction, then automatically stops it once normal operation resumes.

Core and Collector keep their state in named Docker volumes. `docker compose stop`
preserves them. **Do not use `down -v` to troubleshoot:** it removes persistent data.

Before an update, stop both services and back up all six volumes as one matching
set: exchange, Core data, Collector data, runtime, Discord profile, and keyring.

Old journal cleanup is not automatic. Read [retention](RETENTION.md) before any
maintenance. Never remove segments by hand.

For problems, start with `docker compose -f docker/compose.yml ps` and the relevant
service's logs:

```sh
docker compose -f docker/compose.yml logs cordbrief-collector
docker compose -f docker/compose.yml logs cordbrief-core
```
