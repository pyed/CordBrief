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

The first installation needs access to GitHub. Later starts reuse the downloaded copy, so GitHub being unavailable does not prevent startup. The exporter may still require the system libraries listed in its platform documentation.

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
| `/unfollow [channel_id]` | Stop following a channel after confirmation. |
| `/schedule 08:00 Asia/Riyadh` | Enable a daily brief at that local time. |
| `/schedule off` | Disable daily briefs; `/schedule` shows the current setting. |
| `/model [model_id]` | Browse models or set one directly. |
| `/prompt` | View/edit instructions; `/prompt reset` restores the default. |
| `/status` | Show settings, exporter version, and current job status. |

Scheduling starts disabled, with UTC as the initial timezone. Only the configured owner can control CordBrief, in a private Telegram chat.

## Credentials and headless operation

To change credentials, stop CordBrief and run `./cordbrief --setup` (Windows: `.\cordbrief.exe --setup`). Enter keeps an existing value; `-` clears the optional AI key. Setup saves and exits without contacting Discord or testing exports.

Credentials live in `data/credentials.json`, separate from `config.json` and `state.json`. The file is **not encrypted**: Unix permissions restrict it to the owner (`0600`), and Windows uses a protected ACL granting access only to the current account. Windows storage must support ACLs, such as NTFS. Administrators, software running as your account, and anyone with access to an unprotected disk/backup can still read it.

For a headless service, run setup once as the service account. Use the same working directory on subsequent starts, or set `CORDBRIEF_DATA_DIR` to a permanent data folder. Noninteractive launches never prompt: missing required credentials produce an error and exit.

Existing environment deployments still work. `TELEGRAM_BOT_TOKEN`, `TELEGRAM_OWNER_ID`, `DISCORD_TOKEN`, and `LLM_API_KEY` override saved values; an explicitly empty variable clears its saved value for that run. Environment-only startup does not save credentials. CordBrief does not load `.env` files or accept secrets as command-line arguments.

To change AI providers, save a setting with `/model model-id`, stop CordBrief, and edit `llm.base_url` and `llm.model` in `data/config.json`. Use `--setup` to change the API key, then restart.

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
