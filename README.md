# CordBrief

CordBrief turns the Discord channels you follow into concise AI-generated Telegram briefs — on demand or on a daily schedule.

## Quick Start

### 1. Install

Download your platform archive from [Releases](https://github.com/pyed/CordBrief/releases) and extract it.

### 2. Run

**Windows:**
```powershell
.\cordbrief.exe
```

**Linux / macOS:**
```sh
./cordbrief
```

On first launch CordBrief walks you through setup (input is hidden). You need:

- A **Telegram bot token** from [@BotFather](https://t.me/BotFather)
- Your **numeric Telegram user ID**
- A **Discord token** — the [DiscordChatExporter docs](https://github.com/Tyrrrz/DiscordChatExporter/tree/master/.docs) explain how to get a token and find channel IDs
- An **AI API key** (Gemini by default; leave blank for an unauthenticated local server)

CordBrief saves your credentials and automatically downloads [DiscordChatExporter](https://github.com/Tyrrrz/DiscordChatExporter).

### 3. Follow your first channel

Open your Telegram bot and send:

```
/follow 123456789012345678 general
```

Replace the ID with your channel ID. Choose **From now** or **Last 24 hours**.

### 4. Run your first brief

Send `/brief`. CordBrief exports new messages, summarizes them, and delivers the result. This first brief also activates channel discovery.

### 5. Add more channels with Discover

After your first brief, send `/discover` to browse servers and channels directly — no channel IDs needed. Pick a server, pick a channel, tap **Follow**.

## Commands

| Command | What it does |
| --- | --- |
| `/brief [channel_id]` | Summarize all followed channels, or one. |
| `/channels` | List followed channels. |
| `/discover` | Browse servers and channels to follow or hide. |
| `/hidden` | View and unhide hidden channels. |
| `/follow <channel_id> [name]` | Follow a channel by ID (alternative to Discover). |
| `/unfollow [channel_id]` | Unfollow a channel (with confirmation). |
| `/model [model_id]` | Browse or set the AI model. |
| `/fallback [on\|off\|model <id>]` | View or control the optional fallback LLM. |
| `/schedule HH:MM [timezone]` | Set daily brief delivery time. |
| `/schedule off` | Disable daily briefs. |
| `/dcecooldown [duration]` | Show or set export spacing (default 15 min; 0 disables). |
| `/prompt` | View, edit, or reset the summary prompt. |
| `/status` | Show current settings and system state. |

## Channel Discovery

`/discover` lists your Discord servers, then channels within each server. Tap a channel to **Follow** or **Hide** it.

- Followed and hidden channels are filtered out of the browser.
- `/hidden` lets you unhide channels so they reappear.
- **Hide is a CordBrief browser filter only** — it does not detect Discord permissions or channel visibility.
- The catalog may include channels not shown in Discord's UI. CordBrief cannot determine actual access.
- `/follow <channel_id> [name]` still works if you already know the channel ID.

## Briefs

`/brief` summarizes new messages since each channel's last delivered position.

- A channel's progress advances **only after successful delivery** — failures never silently consume unread messages.
- Both manual (`/brief`) and scheduled briefs are supported.

## Models and Fallback

`/model` opens a paginated browser of models reported by your AI provider. You can also set one directly:

```
/model your-model-id
```

### Optional fallback LLM

A secondary AI provider can take over when the primary one fails. To set it up:

1. Stop CordBrief
2. Add a `fallback` block to `data/config.json`:
   ```json
   "fallback": {
     "enabled": false,
     "base_url": "https://your-provider.example/v1",
     "model": "your-fallback-model"
   }
   ```
3. Enter the fallback key with `--setup`, or set `FALLBACK_LLM_API_KEY`
4. Restart and send `/fallback on`

The fallback key is always separate — it never inherits the primary key, even when both endpoints are the same. Use `/fallback off` to disable or `/fallback model <id>` to change its model. API keys are never entered through Telegram.

## Scheduling

```
/schedule 08:00 Asia/Riyadh
```

The scheduled time is the **delivery target**. CordBrief starts collecting channels earlier — one export-cooldown interval per channel before delivery. With 4 channels and the default 15-minute cooldown, collection starts about an hour early. Keep CordBrief running for scheduled delivery.

`/schedule off` disables the schedule. `/schedule` (no arguments) shows the current setting.

## Configuration and Credentials

### Changing credentials

Stop CordBrief and run `--setup`:

```sh
./cordbrief --setup
```

Enter keeps an existing value; `-` clears either optional AI key. Setup saves and exits without starting the bot.

### Credential storage

Credentials live in `data/credentials.json`. The file is **not encrypted** but is protected by OS permissions (Unix `0600` / Windows per-user ACL).

### Environment variables

Environment variables override saved credentials for that run:

| Variable | Purpose |
| --- | --- |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token |
| `TELEGRAM_OWNER_ID` | Numeric Telegram user ID |
| `DISCORD_TOKEN` | Discord token |
| `LLM_API_KEY` | Primary AI API key |
| `FALLBACK_LLM_API_KEY` | Fallback AI API key |
| `CORDBRIEF_DATA_DIR` | Data directory (default: `./data`) |

### Headless deployment

Run `--setup` once as the service account. Set `CORDBRIEF_DATA_DIR` to a permanent path if needed. Non-interactive launches never prompt — missing credentials cause an error and exit.

## DiscordChatExporter

CordBrief downloads and manages the official [DiscordChatExporter CLI](https://github.com/Tyrrrz/DiscordChatExporter) automatically. Downloads are SHA-256 verified. New versions are tested on the next brief before replacing the previous working copy.

To pin a specific release:

```sh
./cordbrief --dce-version 2.48
```

To resume automatic updates:

```sh
./cordbrief --dce-version latest
```

See [docs/OPERATIONS.md](docs/OPERATIONS.md) for advanced DCE management and version recovery.

## Development

Requires Go 1.26+:

```sh
git clone https://github.com/pyed/CordBrief.git
cd CordBrief
go test ./...
go build ./cmd/cordbrief
```

[MIT License](LICENSE)
