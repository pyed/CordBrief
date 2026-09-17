# Operations Guide

Advanced configuration and operational details for CordBrief.

## File Layout

All persistent files live under a single data directory (default `./data`, override with `CORDBRIEF_DATA_DIR`):

| File | Contents |
| --- | --- |
| `credentials.json` | API keys and tokens (OS-protected) |
| `config.json` | Channels, schedule, LLM settings, prompt, cooldown |
| `state.json` | Persisted channel progress, cursors, and status |
| `dce/` | DiscordChatExporter binaries and update metadata |

Back up the data directory securely. Run only one instance per data directory.

## Credential Security

`credentials.json` is **not encrypted**. CordBrief applies OS-level protections:

- **Unix:** file mode `0600` (owner-only read/write).
- **Windows:** a protected ACL granting access only to the current user account. Storage must support ACLs (e.g. NTFS).

Administrators, software running as your account, and unprotected disk backups can still read the file.

CordBrief does not load `.env` files or accept secrets as command-line arguments. Environment variables override saved values but do not modify the stored file.

## Changing AI Provider

To switch the primary LLM provider:

1. Set the model with `/model model-id`
2. Stop CordBrief
3. Edit `llm.base_url` in `data/config.json`
4. Run `--setup` to update the API key
5. Restart

## Environment Variables

| Variable | Purpose |
| --- | --- |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token |
| `TELEGRAM_OWNER_ID` | Numeric Telegram user ID |
| `DISCORD_TOKEN` | Discord token |
| `LLM_API_KEY` | Primary AI API key |
| `FALLBACK_LLM_API_KEY` | Fallback AI API key |
| `CORDBRIEF_DATA_DIR` | Data directory (default: `./data`) |
| `CORDBRIEF_DCE_VERSION` | Pin DCE release tag, or `latest` for auto-updates |
| `CORDBRIEF_DCE_PATH` | Path to an externally managed bootstrap DCE binary (not managed by CordBrief) |

An explicitly empty variable clears its saved value for that run. Environment-only startup does not save credentials.

## Export Cooldown

`/dcecooldown` controls the minimum interval between DCE export starts (default 15 minutes). Accepted range: 0 to 1 hour in whole seconds.

A manual `/brief` starts immediately unless a previous export's cooldown is still active. Channels are handled sequentially: export, summarize, deliver, then start the next channel's export when its cooldown slot arrives. Summarization and delivery use the time between export starts — they do not add another full cooldown afterward.

## Scheduled Delivery Timing

The scheduled time is the **delivery target**. CordBrief budgets preparation time: one cooldown interval per followed channel before the target, with a minimum of one minute per channel.

Example with 4 channels, 15-minute cooldown, 12:00 schedule:

| Time | Action |
| --- | --- |
| 11:00 | Export channel 1 |
| 11:15 | Export channel 2 |
| 11:30 | Export channel 3 |
| 11:45 | Export channel 4 |
| 12:00 | Deliver all briefs |

Each channel's freshness boundary is its own export time — earlier channels are not re-exported at delivery. Slow exports or summaries may push delivery past the target; CordBrief finishes preparation rather than sending incomplete work.

Keep CordBrief running for scheduled delivery. Prepared text is held in memory; restarting discards it without advancing cursors. If a brief is already running when a scheduled run triggers, the scheduled run is skipped and messages remain available for the next brief.

## DCE Version Management

CordBrief checks for new DiscordChatExporter releases weekly. Downloads must pass SHA-256 verification.

**Candidate testing:** A newly downloaded version runs as a candidate on the next brief. If it succeeds, it replaces the previous version. If it fails, it is rejected and the previous version is retried automatically.

**Pinning:** Use `--dce-version TAG` or `CORDBRIEF_DCE_VERSION=TAG` to pin a specific release. This pauses automatic updates. Use `--dce-version latest` to resume them.

**Constraints:**
- Releases without a published SHA-256 digest are refused.
- A rejected version is not automatically retried — choose another tag.
- A first installation has no previous version to fall back to.
- `CORDBRIEF_DCE_PATH` can supply an externally managed bootstrap DCE binary; CordBrief leaves that file untouched.
