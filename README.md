# CordBrief

Discord catch-up briefs, delivered to your private Telegram chat. Follow channels, choose an AI model, and request a summary on demand or schedule one each day.

CordBrief runs on your computer or server. It uses DiscordChatExporter to collect messages, an OpenAI-compatible API to summarize them, and Telegram to deliver the result.

## 1. Install

Download and extract the archive for your system from [Releases](https://github.com/pyed/CordBrief/releases):

- `linux`, `darwin` (macOS), or `windows`.
- `amd64` for Intel/AMD; `arm64` for Apple Silicon and ARM machines.

Also download the matching **CLI** archive from [DiscordChatExporter](https://github.com/Tyrrrz/DiscordChatExporter/releases) and extract the entire archive into a permanent folder. Run its executable with `--version` to check that it works. See its [setup documentation](https://github.com/Tyrrrz/DiscordChatExporter/tree/master/.docs) for platform requirements, Discord tokens, and channel IDs.

## 2. Set credentials and start

You need a Telegram bot token from [BotFather](https://t.me/BotFather), your numeric Telegram user ID, a Discord token with access to the channels, and an API key for your AI provider.

**Linux / macOS**: replace the example values, then run from the extracted CordBrief folder:

```sh
export TELEGRAM_BOT_TOKEN='your-bot-token'
export TELEGRAM_OWNER_ID='123456789'
export DISCORD_TOKEN='your-discord-token'
export CORDBRIEF_DCE_PATH='/absolute/path/to/DiscordChatExporter.Cli'
export LLM_API_KEY='your-api-key'
chmod +x cordbrief
./cordbrief
```

**Windows PowerShell:**

```powershell
$env:TELEGRAM_BOT_TOKEN = 'your-bot-token'
$env:TELEGRAM_OWNER_ID = '123456789'
$env:DISCORD_TOKEN = 'your-discord-token'
$env:CORDBRIEF_DCE_PATH = 'C:\Tools\DCE\DiscordChatExporter.Cli.exe'
$env:LLM_API_KEY = 'your-api-key'
.\cordbrief.exe
```

These variables apply to the current terminal. CordBrief does not load `.env` files. Keep the process running for scheduled briefs; press Ctrl+C to stop. Only the configured Telegram owner can control it, in a private chat.

## 3. Follow a channel

Open your Telegram bot and send:

```text
/start
/follow 123456789012345678 general
```

Choose **From now** or **Last 24 hours** using the buttons. Then send `/model` to choose an available chat model and `/brief` to get your first summary.

| Command | What it does |
| --- | --- |
| `/brief [channel_id]` | Summarize new messages from all followed channels, or one channel. |
| `/channels` | List followed channels. |
| `/unfollow [channel_id]` | Stop following a channel after confirmation. |
| `/schedule 08:00 Asia/Riyadh` | Enable a daily brief at that local time. |
| `/schedule off` | Disable daily briefs; `/schedule` shows the current setting. |
| `/model [model_id]` | Browse available models or set one directly. |
| `/prompt` | View or edit the summary instructions; `/prompt reset` restores the default. |
| `/status` | Show configuration, exporter status, and whether a brief is running. |

Scheduling starts disabled. Without a timezone argument, `/schedule` keeps the current timezone, initially UTC.

## Settings and data

Settings and channel progress are saved under `./data`. Set `CORDBRIEF_DATA_DIR` to use another folder. Back it up and run only one CordBrief instance per data folder.

The default AI endpoint is Google's Gemini OpenAI-compatible API. To use another provider, first save a setting with `/model model-id`, stop CordBrief, and edit `llm.base_url` and `llm.model` in `data/config.json`. Restart with that provider's `LLM_API_KEY`; a local server that needs no authentication can leave the key unset.

Messages go to your configured AI provider for summarization. Temporary exports are deleted after collection. Channel progress advances only after every summary part reaches Telegram, so failures can be retried. A partially delivered brief may repeat on retry.

CordBrief checks weekly for DiscordChatExporter updates, verifies download checksums, and tries a new version on the next export. If it fails, it retries with the previous version.

## Build and release

With Go 1.26 or newer installed:

```sh
git clone https://github.com/pyed/CordBrief.git
cd CordBrief
go test ./...
go build ./cmd/cordbrief
```

The default tests run without credentials or live services. GitHub Actions runs tests, race checks, static analysis, and a build on Linux, macOS, and Windows for pushes and pull requests.

After committing and pushing your changes, push a new tag to publish a release. For example, using an unused version:

```sh
git tag v3.0.0
git push origin v3.0.0
```

Every pushed tag triggers a release after CI passes, with Linux, macOS, and Windows archives for both architectures and a `checksums.txt` file. GitHub Actions uses its built-in token; no extra release secret is needed.

[MIT License](LICENSE)
