# Running CordBrief

## First start

Use an x86-64 host with Docker's Linux engine and Docker Compose v2.24 or newer
(the Compose file uses optional environment files). The collector runs the Linux
Discord desktop client; ARM NAS systems are not currently supported by this build.
Allow time and disk space for the first build: setup includes Discord, Vencord,
and its build tools. Builds download upstream components and aren't reproducibly pinned.

Run these from the repository root:

```sh
docker compose -f docker/compose.yml --profile setup build
docker compose -f docker/compose.yml --profile setup up cordbrief-setup
```

Open <http://127.0.0.1:28742> to sign in. Setup prepares the runtime and exits after
authentication. Check its output for a successfully staged release; a staging
error needs resolving before you start the collector.

```sh
docker compose -f docker/compose.yml up -d
docker compose -f docker/compose.yml ps
```

Open <http://127.0.0.1:28741>. Select the channels to watch, configure the model,
and generate a digest before enabling its schedule. New watches normally start
around the latest visible Discord message. If initialization cannot establish a
trustworthy boundary, later recovery may include older history; see [recovery](RECOVERY_CONTRACT.md).

## Models and delivery

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

## Headless hosts and privacy

Both screens bind to the host's loopback interface. They have no application
login and must not be exposed directly to the Internet. For a remote host, keep
those bindings and use an SSH tunnel:

```sh
ssh -L 28741:127.0.0.1:28741 -L 28742:127.0.0.1:28742 user@your-host
```

Then open the same local URLs. The setup screen controls a signed-in Discord
session; keep it private and stop setup when finished.

## Restarts, updates, and data

If Discord needs authentication again, stop Collector before starting setup:

```sh
docker compose -f docker/compose.yml stop cordbrief-collector
docker compose -f docker/compose.yml --profile setup up cordbrief-setup
docker compose -f docker/compose.yml up -d cordbrief-collector
```

Core and Collector keep their state in named Docker volumes. `docker compose stop`
preserves them. **Do not use `down -v` to troubleshoot:** it removes persistent data.
The volume names are fixed, so changing the Compose project name does not create
an independent installation on the same Docker engine.

Before an update, stop both services and back up all six volumes as one matching
set: exchange, Core data, Collector data, runtime, Discord profile, and keyring.
Verify that the backup can be restored into separate volumes. A newer image alone
does not replace the prepared Collector runtime; setup must stage it. Don't run
an old reader against state produced by newer retention code.

Old journal cleanup is not automatic. Read [retention](RETENTION.md) before any
maintenance. Never remove segments by hand.

Old bot-style seed config files are no longer supported. Remove `guild_id`,
`source_channel_ids`, `digest_channel_id`, and the digest settings
`first_run_lookback`, `max_catchup`, and `max_messages_per_channel` from files
passed with `--config`. Channel selection and recovery belong to the collector's
watchlist. Existing Web settings load without those unused fields.

For problems, start with `docker compose -f docker/compose.yml ps` and the relevant
service's logs. Keep logs private until you have checked them for credentials,
channel information, message text, and provider responses.
