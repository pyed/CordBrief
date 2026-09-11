# CordBrief

Catch up on your Discord communities without reading every message.

CordBrief turns the channels you follow into short digests with links back to
the conversation. Read them in a local Web inbox, or send them to Telegram on a
schedule. You choose the channels, the model, and what the digest should focus on.

## How it works

A collector runs Discord with a Vencord plugin and saves watched messages to a
local journal. A small Go service reads that journal, summarizes with Gemini or
an OpenAI-compatible endpoint, and keeps the digests and their source links.

The two services share files. There's no database server or Docker socket, and
the Go Core uses only the standard library.

## Get started

You'll need an x86-64 machine with Docker's Linux engine and Docker Compose.
From the repository root:

```sh
docker compose -f docker/compose.yml --profile setup build
docker compose -f docker/compose.yml --profile setup up cordbrief-setup
```

Open [the setup screen](http://127.0.0.1:28742) and sign in to Discord. Once setup
has successfully staged a runtime and exited, start CordBrief:

```sh
docker compose -f docker/compose.yml up -d
```

Open [your inbox](http://127.0.0.1:28741), choose channels and configure the model.
Scheduling and Telegram delivery start disabled. See [setup](docs/SETUP.md) for
remote access, credentials, and troubleshooting.

## Before you run it

This is an early, unofficial Discord integration using a signed-in user account.
[Discord prohibits automated user accounts and may terminate them](https://support.discord.com/hc/en-us/articles/115002192352-Automated-User-Accounts-Self-Bots).

Watched messages are stored on disk and sent to your chosen model when generating
a digest. Keep the Web UI private and only collect conversations you have permission
to process. Summaries can be wrong; the source links are there to check them.

[Architecture](docs/ARCHITECTURE.md) · [Recovery](docs/RECOVERY_CONTRACT.md) ·
[Retention](docs/RETENTION.md) · [RPC probe research](docs/DISCORD_RPC_PROBE.md) ·
[Contributing](CONTRIBUTING.md)

[MIT licensed](LICENSE). Discord, Vencord, and other upstream components retain
their own licenses and terms.
