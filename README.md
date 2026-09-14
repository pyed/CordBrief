# CordBrief

CordBrief is a lightweight, background Telegram-controlled Discord briefing bot.

At idle, CordBrief is a single Go process running Telegram long polling and an internal timer. It operates with zero inbound ports and requires no Web UI, no Docker, no database server, and no browser or Discord runtime.

## Architecture

- **Control Plane:** 100% Telegram-controlled. All status checks, channel subscriptions, and configuration occur through Telegram bot commands and menus.
- **Discord Collection:** Relies on [`DiscordChatExporter.Cli`](https://github.com/Tyrrrz/DiscordChatExporter) as an external CLI collector. CordBrief knows nothing about Discord gateway, RPC, or session internals.
- **Bounded Exports & Independent Cursors:** Each followed Discord channel maintains an independent cursor. When generating a brief, CordBrief invokes DiscordChatExporter to fetch only the interval from the channel's stored cursor to a fixed cutoff timestamp.
- **At-Least-Once Delivery:** A channel's durable cursor advances *only after* its brief has been successfully sent to Telegram. If any stage fails, the cursor remains untouched and the interval is retried on the next run.
- **Disposable Raw Exports:** Raw Discord message exports are temporary working data deleted immediately after processing. No raw messages are retained in databases or journals.
- **Independent Channel Delivery:** Each followed channel produces its own separate Telegram brief and fails independently.
- **LLM Abstraction:** Built on standard Go `net/http` targeting OpenAI-compatible chat completion APIs, with Google Gemini via its OpenAI-compatible endpoint as the default. Configurable base URL, model, and API key.

## Persistence & State Contract

- **Config (`config.json`):** Declarative user intent (followed channels, brief schedule, timezone, non-secret LLM configuration). Contains no secrets or cursors.
- **State (`state.json`):** Durable operational facts learned at runtime, strictly keyed by Discord channel ID. Contains per-channel cursors, last success timestamps, and error history.
- **Cursor Lifecycle:** A newly followed channel initially begins with an explicit `timestamp` cursor (RFC3339). Once messages are processed, it advances to a Discord `message_id` snowflake cursor.
- **Advance Invariant:** A channel's cursor advances *only after* its brief has been successfully delivered to Telegram. If any step fails, the cursor remains untouched so the next run retries the interval.
- **Disposable Working Data:** Raw Discord message exports are temporary working files deleted immediately after brief generation. They are never retained as durable state.

## Telegram Control Plane

CordBrief operates as an owner-only, private Telegram bot. Interactions from unauthorized users or non-private chats are silently ignored.

## Discord Collection Boundary

Discord collection is performed strictly through [`DiscordChatExporter.Cli`](https://github.com/Tyrrrz/DiscordChatExporter) (pinned and validated on version 2.48).

CordBrief itself does not implement Discord protocols (no Gateway, REST, RPC, scraping, or browser automation). It delegates collection entirely to the external DCE executable:

- **Bounded Collection:** Invoked strictly with `--after` (persisted cursor) and `--before` (fixed UTC cutoff) to guarantee bounded intervals without message loss.
- **Disposable Working Files:** Raw JSON exports are written to temporary files, parsed into normalized domain types, and deleted immediately. Raw exports are never retained as durable state or application history.
- **On-Demand Invocation:** DCE is executed only during active collection jobs, never on status checks or idle polling.
- **Collection Only:** The collection adapter is strictly read-only and never mutates cursors or durable state.

### Environment Configuration

- `TELEGRAM_BOT_TOKEN`: Telegram bot token from @BotFather (required for bot).
- `TELEGRAM_OWNER_ID`: Telegram user ID of the authorized owner (required for bot, positive integer).
- `CORDBRIEF_DATA_DIR`: Directory path for `config.json` and `state.json` (optional, defaults to `./data`).
- `CORDBRIEF_DCE_PATH`: Path to the pinned `DiscordChatExporter.Cli` executable (required for collection).
- `DISCORD_TOKEN`: Discord authentication token passed directly to child DCE process via environment without appearing in command-line arguments (required for collection).

### Commands

- `/start`: Display the main menu and overview.
- `/status`: Show current followed channel count, schedule, timezone, and LLM model.
- `/channels`: List followed Discord channels with interactive follow and unfollow controls.
- `/follow <channel_id> [display name]`: Follow a new channel with interactive start mode selection (`From now` or `Last 24 hours`).
- `/unfollow [channel_id]`: Remove a channel from followed configuration and purge its operational state after confirmation.

## Project Structure

```text
CordBrief/
├── cmd/
│   └── cordbrief/     # Application entrypoint
├── internal/
│   ├── bot/           # Telegram bot control plane
│   ├── brief/         # Transcript compaction & brief assembly
│   ├── dce/           # DiscordChatExporter execution & parsing
│   ├── llm/           # OpenAI-compatible LLM client
│   ├── scheduler/     # Timing & job scheduling
│   └── state/         # Configuration & cursor persistence
├── deploy/            # Systemd service & deployment assets
├── go.mod
├── README.md
├── LICENSE
└── .gitignore
```

## Non-Goals

- No Web UI
- No Docker or container orchestration required
- No SQLite or external database
- No Discord client, browser automation, or electron wrappers
- No inbound open network ports
