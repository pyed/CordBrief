# CordBrief

Discord catch-up briefs, delivered to your private Telegram chat. Follow channels, choose an AI model, and request a summary on demand or schedule one each day.

## Install and start

1. Download and extract your archive from [Releases](https://github.com/pyed/CordBrief/releases). Choose `windows`, `linux`, or `darwin` (macOS), then `amd64` for Intel/AMD or `arm64` for Apple Silicon/ARM.
2. Open a terminal in the extracted folder and run CordBrief:

   **Windows PowerShell:**
   ```powershell
   .\cordbrief.exe
   ```

   **Linux / macOS:**
   ```sh
   ./cordbrief
   ```

3. On first launch, follow the setup prompts. You need:
   - A Telegram bot token from [BotFather](https://t.me/BotFather).
   - Your numeric Telegram user ID.
   - A Discord token with access to your channels. See the [DiscordChatExporter documentation](https://github.com/Tyrrrz/DiscordChatExporter/tree/master/.docs) for token and channel-ID instructions.
   - An AI API key. The default provider is Google's Gemini; a local server without authentication can leave this blank.

Input is hidden. CordBrief saves your credentials and automatically downloads the appropriate official DiscordChatExporter CLI. Keep CordBrief running for scheduled briefs. Ctrl+C stops it.

The first installation needs access to GitHub. Metadata requests have a 30-second deadline. Release assets get up to three fresh download attempts, each limited to 3 minutes, with short waits between transient failures. Startup preparation is limited to 10 minutes and Ctrl+C cancels it. Every attempt must pass SHA-256 verification; partial downloads are discarded. Later starts reuse the downloaded copy, so GitHub being unavailable does not prevent startup. The exporter may still require the system libraries listed in its platform documentation.

## Get your first brief

Open your Telegram bot and send:

```text
/start
/follow 123456789012345678 general
```

Replace the example with your channel ID. Choose **From now** or **Last 24 hours**, then use `/model` to select an available chat model and `/brief` to summarize new messages.

| Command | What it does |
| --- | --- |
| `/brief [channel_id]` | Summarize all followed channels, or one channel. |
| `/channels` | List followed channels. |
| `/discover` | Browse discovered servers and channels; follow or hide a channel. |
| `/hidden` | View hidden channels and unhide them. |
| `/unfollow [channel_id]` | Stop following a channel after confirmation. |
| `/schedule 08:00 Asia/Riyadh` | Target daily brief delivery around that local time; preparation starts earlier. |
| `/schedule off` | Disable daily briefs; `/schedule` shows the current setting. |
| `/dcecooldown [0\|5m\|15m\|1h]` | Show or save the minimum interval between DCE export starts (default 15m; 0 disables it; maximum 1h). |
| `/model [model_id]` | Browse models or set one directly. |
| `/fallback [on|off|model <model_id>]` | Show or change the optional fallback profile; never accepts API keys. |
| `/prompt` | View/edit instructions; `/prompt reset` restores the default. |
| `/status` | Show settings, exporter version, and current job status. |

Scheduling starts disabled, with UTC as the initial timezone. Only the configured owner can control CordBrief, in a private Telegram chat.

### Discover channels

Use **Discover** from `/start` or `/channels`, or send `/discover`. Select a server, then a channel, then **Follow**. This opens the same **From now / Last 24 hours** selection used by direct `/follow <channel_id> [name]`.

DCE's catalog can be much larger than the channels shown in Discord's UI. CordBrief cannot infer UI visibility or permission-hidden channels from these lists. Select **Hide** for irrelevant entries. Normal channel pages omit followed and hidden entries. `/hidden` lets you unhide entries even after a restart or when they disappear from the catalog. Hiding affects only browsing, never an existing follow or cursor.

Server and per-server channel lists are cached in memory until restart or explicit **Refresh**. Paging, going back and reopening a cached server make no DCE calls. Refresh replaces only the selected list after success; failures retain the previous list. There is no background discovery or TTL. Discovery lists ordinary non-voice channels, without enumerating threads or direct messages; direct follow by ID remains available.

Metadata calls are sequential, limited to 30 seconds and 1 MiB of output, and refused while a brief is running or DCE is busy. Cached browsing still works during a brief; follow/hide/unhide changes are refused until it finishes. Discovery uses only the active DCE version and never changes export cooldown or candidate probation state. On a first installation with only a candidate, use direct follow and complete a normal brief first. Discovery never exports messages. Unknown or truncated metadata fails closed and leaves cached data intact.

### Export spacing and scheduled delivery

`/dcecooldown` reports the current interval. For example, `/dcecooldown 5m` saves five minutes; `/dcecooldown 0` disables spacing. Whole-second durations from 0 to 1 hour are accepted. The setting is stored in `config.json`; older configurations default to 15 minutes. The last export-start time also survives restarts. Failed exports and candidate rollback attempts share this interval. Configuration changes are frozen while a brief is running.

A manual `/brief` starts immediately unless a previous export's cooldown is still active. Channels are handled sequentially: collect, summarize, deliver, then collect the next channel when its slot is available. Summarization and delivery use the time between export starts; they do not add another full cooldown afterward.

Scheduled times mean **delivery around that time**. CordBrief starts preparation one interval per followed channel ahead of delivery, using at least one minute per channel when the cooldown is shorter. With four channels, a 15-minute cooldown and a 12:00 schedule, exports normally start around 11:00, 11:15, 11:30 and 11:45. All channels finish preparation before delivery begins at 12:00. Slow exports or summaries can make delivery late; CordBrief finishes preparation rather than sending unfinished work at the deadline. Preparation looks ahead at most one day.

Each channel's freshness boundary is its own export time. Earlier channels are **not exported again** at delivery time. A channel's cursor advances only to the highest collected message ID, and only after every part of that channel's brief has been delivered successfully. Later arrivals remain for the next brief. Failed channels retain their cursors and report an error; other channels can succeed independently.

Keep the app running for scheduled delivery. Prepared text is held in memory; restarting discards it without consuming messages. Starting inside the preparation window begins collection immediately and may deliver late. An already-running brief keeps the existing single-job behavior: a colliding scheduled run is skipped, and undelivered messages remain available for the next brief.

## Credentials and headless operation

To change credentials, stop CordBrief and run `./cordbrief --setup` (Windows: `.\cordbrief.exe --setup`). Enter keeps an existing value; `-` clears either optional AI key. Setup saves and exits without contacting Discord or testing exports.

Credentials live in `data/credentials.json`, separate from `config.json` and `state.json`. The file is **not encrypted**: Unix permissions restrict it to the owner (`0600`), and Windows uses a protected ACL granting access only to the current account. Windows storage must support ACLs, such as NTFS. Administrators, software running as your account, and anyone with access to an unprotected disk/backup can still read it.

For a headless service, run setup once as the service account. Use the same working directory on subsequent starts, or set `CORDBRIEF_DATA_DIR` to a permanent data folder. Noninteractive launches never prompt: missing required credentials produce an error and exit.

Existing environment deployments still work. `TELEGRAM_BOT_TOKEN`, `TELEGRAM_OWNER_ID`, `DISCORD_TOKEN`, `LLM_API_KEY`, and `FALLBACK_LLM_API_KEY` override saved values; an explicitly empty variable clears its saved value for that run. Environment-only startup does not save credentials. CordBrief does not load `.env` files or accept secrets as command-line arguments.

To change AI providers, save a setting with `/model model-id`, stop CordBrief, and edit `llm.base_url` and `llm.model` in `data/config.json`. Use `--setup` to change the API key, then restart.

### Optional fallback LLM

While stopped, add one non-secret `fallback` object to `config.json`, keeping your other settings:

```json
"fallback": {
  "enabled": false,
  "base_url": "https://your-other-provider.example/v1",
  "model": "your-fallback-model"
}
```

Use `--setup` to enter the separate fallback key, or set `FALLBACK_LLM_API_KEY`, then restart. A local unauthenticated fallback can have an empty key; it never inherits the primary key, even when both endpoints are identical. Endpoint changes stay in the offline configuration path. Neither profile follows HTTP redirects. Do not put credentials in endpoint URLs or Telegram messages.

Send `/fallback` to see the profile and `/fallback on` to enable it. `/fallback off` retains the profile while disabling failover; `/fallback model <model_id>` changes its model. Configuration changes are refused during a brief. A successful primary never contacts fallback. If a completion fails after the existing bounded retry policy, and the job is still valid, fallback receives that same collected request. Each provider retains at most two attempts for transient errors; permanent errors are not retried. No failover causes a Discord export. Zero-message channels make no AI calls. Once fallback succeeds, all remaining completions in that job use it; the next job tries primary again. Stopping CordBrief or cancelling a brief suppresses fallback; provider timeouts or remote errors are treated as provider failures and can still fail over. Failed summaries leave their channel cursors unchanged.

Config schema 4 adds only optional `fallback` and `hidden_channels` fields. Older configurations migrate automatically (v2 -> v3 -> v4), preserving existing channels, custom prompt, schedule, timezone, LLM settings, cooldown, and cursors. Fallback starts absent, and hidden channels start empty. Cursor storage is unchanged. The separate private credential file gains `fallback_llm_api_key`; older credential files remain valid.

Back up the data folder securely and run only one instance per folder. Messages are sent to your configured AI provider. Temporary exports are deleted, and channel progress advances only after every summary part reaches Telegram. A partly delivered brief may repeat on retry.

## DCE versions and recovery

CordBrief normally uses the latest stable official release and checks weekly for updates. Downloads must pass SHA-256 verification before extraction. New versions remain candidates until the next real brief succeeds; a failed candidate is rejected and the previous working version is retried. A first installation has no previous version to fall back to.

To request a particular official release, stop the app and launch with `./cordbrief --dce-version TAG` (Windows: `.\cordbrief.exe --dce-version TAG`), replacing `TAG` with the exact release tag. The choice is saved and automatic upgrades remain paused. To resume them, launch with `./cordbrief --dce-version latest`. `CORDBRIEF_DCE_VERSION` provides the same override for services.

Older releases without a published SHA-256 digest are refused. Choose another official tag; verification is never bypassed. A rejected version is not automatically retried. A failed download keeps the existing active/candidate files and version selection.

Existing `CORDBRIEF_DCE_PATH` installations can still provide a bootstrap executable. Leave it unset for automatic installation. No exporter is executed as a setup probe; manually supplied bootstrap binaries may be queried with `--version`.

## Development and releases

With Go 1.26 or newer:

```sh
git clone https://github.com/pyed/CordBrief.git
cd CordBrief
go test ./...
go vet ./...
go build ./cmd/cordbrief
```

Default tests use local fixtures and fake process runners; live-service tests are opt-in. CI runs tests with race detection, static analysis, and builds on Linux, macOS, and Windows. Every pushed tag publishes a release after checks pass, with six platform archives and SHA-256 checksums.

[MIT License](LICENSE)
