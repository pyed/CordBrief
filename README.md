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
