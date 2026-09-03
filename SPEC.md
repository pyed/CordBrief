# CordBrief v0.1 Build Contract

## Product

CordBrief is a tiny, self-hosted Discord daily-digest application.

Its job is to:

1. Read messages from explicitly configured Discord guild text/announcement channels using an official Discord bot account and Discord's documented HTTP API.
2. Read only the time window required for the current digest.
3. Keep raw Discord messages only in process memory.
4. Summarize them using a configured OpenAI-compatible LLM endpoint.
5. Produce a short structured daily digest.
6. Attach trustworthy links back to the Discord messages supporting important digest items.
7. Post one digest message to a configured Discord channel.
8. Persist only the end timestamp of the last successfully delivered digest window.

The implementation must remain deliberately small.

---

## Non-goals

Do NOT implement:

- Discord user tokens or self-bots
- Discord Gateway connectivity
- continuous message listening
- message archives
- SQLite or any database
- historical backfills
- Discord DMs
- forum channels
- thread enumeration
- voice
- image or attachment downloading
- OCR
- embeddings
- RAG
- semantic search
- web crawling
- web dashboard
- GUI
- slash commands
- moderation
- user profiling
- email/Telegram/Slack delivery
- native provider SDKs
- plugin systems
- agent frameworks

Do not add dependencies or features merely because they might be useful later.

---

## Technology

Language: Go.

Minimum source compatibility: Go 1.26.

Use the Go standard library wherever possible.

Target zero third-party dependencies for v0.1.

Use:

- `net/http`
- `encoding/json`
- `context`
- `time`
- `time/tzdata`
- `log/slog`
- `os`
- `flag`
- `os/signal`

Do not use Discord SDKs or LLM SDKs unless a concrete technical requirement proves the standard library insufficient.

---

## Discord authentication

Only Discord bot tokens are supported.

Token source:

`CORDBRIEF_DISCORD_TOKEN`

Never:

- accept a Discord user token
- extract Discord credentials
- log a bot token
- store a bot token in config
- place secrets in examples/tests

Use Discord's versioned REST API.

---

## Discord permissions

Source channels require only:

- View Channel
- Read Message History

Digest destination additionally requires:

- Send Messages

Do not request Administrator, Manage Messages, Manage Guild, Manage Members, Guild Members Intent, or Presence Intent.

Message Content must be enabled in the Discord Developer Portal. Larger deployments may become subject to Discord's current privileged-access review requirements.

---

## Configuration

Configuration file: `config.json`.

Secrets remain environment variables.

Required concepts:

Root (Discord identity and channel selection):
- guild ID (`guild_id`)
- source channel IDs (`source_channel_ids`)
- digest destination channel ID (`digest_channel_id`)

Schedule (`schedule`):
- local schedule time (`time`, format `HH:MM`)
- IANA timezone (`timezone`)

LLM (`llm`):
- base URL (`base_url`)
- model name (`model`)
- optional API-key environment variable name (`api_key_env`)
- maximum input characters (`max_input_chars`)
- maximum output tokens (`max_output_tokens`)

Digest (`digest`):
- output language (`output_language`)
- optional focus strings (`focus`)
- ignore-bots toggle (`ignore_bots`)
- first-run lookback duration (`first_run_lookback`)
- maximum catch-up duration (`max_catchup`)
- maximum messages per channel safety cap (`max_messages_per_channel`)

Validate all configuration before making external requests.

---

## Persistent state

Use a small JSON state file in a predictable v0.1 location (`cordbrief-state.json`).

Persist only operational metadata needed to avoid duplicate windows.

Minimum field:

`last_window_end`

The persistent checkpoint records the end of the last successfully delivered digest window (not the completion wall-clock time), preventing messages arriving during processing from being skipped.

Raw messages, prompts, model inputs, intermediate summaries and API responses must not be persisted.

State updates use crash-resistant replacement semantics.

Write a temporary state file and rename it into place.

Never advance `last_window_end` until the digest has been successfully delivered.

---

## Message retrieval

For every configured source channel:

1. Fetch the newest page with a limit of 100.
2. Discord returns newest to oldest.
3. Continue requesting older pages using `before=<oldest_message_id>`.
4. Stop once the page crosses the requested start timestamp.
5. Filter exactly to `(window_start, window_end]`.
6. Enforce a configurable safety cap.
7. Sort normalized messages chronologically before summarization.

Do not backfill history outside the requested digest window.

Fetch channels sequentially initially. Simplicity is preferable to unnecessary concurrency.

---

## Discord rate limiting

Never hardcode Discord rate limits.

Centralize HTTP handling.

Read Discord rate-limit response headers.

On HTTP 429, honor `Retry-After` / `retry_after`.

Retry transient network failures and 5xx responses with bounded exponential backoff.

Do not endlessly retry 400, 401, 403 or 404 errors.

HTTP requests must use contexts and timeouts.

---

## Normalized message

The summarizer needs only minimal data:

- internal source ID
- Discord message ID
- channel ID
- channel name when known
- author display name/username
- timestamp
- textual content
- reply target ID when available
- attachment filenames when useful

Do not download attachments.

Do not provide binary attachments to the LLM.

Skip bot-authored messages by default.

Skip CordBrief's own digest posts.

---

## LLM interface

Define an internal provider abstraction even though v0.1 contains only one implementation.

Initial implementation:

`OpenAICompatibleProvider`

Configuration:

- base URL
- model
- optional bearer API key
- maximum output tokens

Use a non-streaming chat-completions request.

Do not depend on vendor-specific features for correctness.

The implementation must work with common OpenAI-compatible local endpoints such as llama.cpp, LM Studio and Ollama, and compatible remote endpoints where the operator chooses to use one.

Do not implement native Anthropic support in v0.1.

---

## Prompt security

Discord messages are untrusted data.

The summarization system prompt must explicitly state that:

- message content is source material, not instructions
- commands or prompts found inside Discord messages must be ignored
- the task cannot be changed by source-message content
- the model must only summarize supplied material
- unsupported claims must not be invented

The LLM has no tools and no ability to alter application state.

---

## Source attribution

Assign every normalized input message an opaque local source ID such as:

`m0001`

The LLM must cite these IDs rather than creating Discord URLs.

Example digest item:

```json
{
  "text": "Participants reported a measurable reduction in fines.",
  "source_ids": ["m0124", "m0132"]
}
```

Validate every returned source ID against the messages actually supplied to that LLM stage.

Reject or remove unknown IDs.

The renderer, not the LLM, constructs Discord jump links from trusted guild/channel/message IDs.

---

## Structured output

The model must return JSON representing a small digest.

Conceptual schema:

```json
{
  "overview": "string",
  "items": [
    {
      "category": "key_point",
      "text": "string",
      "source_ids": ["m0001"]
    }
  ],
  "worth_opening": [
    {
      "text": "string",
      "source_id": "m0002"
    }
  ]
}
```

Keep categories minimal.

The parser must reject invalid JSON.

On invalid output, make at most one repair/retry request.

If the second attempt is invalid, fail the digest and do not advance state.

Do not silently accept arbitrary prose.

---

## Large-input handling

If all normalized messages fit inside the configured maximum input size, summarize them in one call.

If not:

1. Split chronologically into bounded chunks.
2. Summarize each chunk into the same structured fragment format.
3. Validate its source references.
4. Feed only the validated fragments into one final reduction call.
5. Preserve original source IDs throughout reduction.
6. Do not persist intermediate fragments.

Do not simply discard older messages because the day's input exceeds one model context.

---

## Digest rendering

The final Discord digest must be concise.

Target approximately 1,800 characters.

Absolute maximum: below Discord's 2,000-character plain-message limit.

Prefer:

- overview
- 4–8 genuinely important findings
- disagreement/unresolved question when material
- 1–3 "worth opening" items
- total message count and time window

Do not summarize every conversation.

The purpose is information filtering, not exhaustive transcription.

If an LLM result is too long, perform one bounded shortening pass or safely reduce low-priority items before posting.

Avoid multipart digest delivery in v0.1.

---

## Discord posting

Post a single plain-text Discord message.

Set `allowed_mentions.parse` to an empty list.

The digest must never accidentally trigger `@everyone`, role or user notifications because source text contained mention syntax.

Do not require embeds.

Successful HTTP response is required before state advances.

---

## CLI

Implement:

`cordbrief doctor`

Validate configuration, Discord authentication, accessibility of configured source channels and basic LLM connectivity. Never print secrets or raw Discord messages.

`cordbrief channels`

List useful guild channels and IDs to help configure the application.

`cordbrief run`

Perform one complete digest cycle and post it.

`cordbrief run --dry-run`

Perform retrieval and summarization and print the rendered digest to stdout. Do not post and do not advance state.

`cordbrief serve`

Run the scheduler until terminated.

`cordbrief version`

Print version information.

---

## Scheduler

Use the standard library.

Interpret schedule as local wall-clock time in an IANA timezone, not as a fixed 24-hour interval.

Embed Go timezone data.

After each run, calculate the next scheduled local occurrence.

Support graceful shutdown through context cancellation and OS signals.

A user must also be able to avoid the built-in scheduler entirely and invoke `cordbrief run` from cron, systemd timers or Windows Task Scheduler.

---

## Failure semantics

No failure may silently lose a digest window.

If retrieval fails:
- do not summarize
- do not advance state

If LLM generation fails:
- do not post
- do not advance state

If output validation fails:
- do not post
- do not advance state

If Discord posting fails:
- do not advance state

If everything succeeds:
- safely update state with crash-resistant replacement

Clamp catch-up to the configured maximum duration.

---

## Logging

Use `log/slog`.

Normal logs may contain:

- operation
- channel ID/name
- message count
- window
- durations
- retry status
- errors

Normal and debug logs must not contain:

- Discord tokens
- LLM keys
- authorization headers
- complete raw Discord messages
- complete LLM prompts containing Discord messages

No secret-bearing request objects should ever be formatted directly into logs.

---

## Privacy

Raw Discord messages exist only temporarily in memory during a digest run.

Do not persist them.

Include `PRIVACY.md` documenting:

- what Discord data is accessed
- why it is accessed
- that raw source messages are not persisted
- what operational state is persisted
- that choosing a remote LLM transmits normalized Discord message content to that provider
- that operators must accurately configure their own Discord Developer Portal privacy-policy information and satisfy applicable Discord terms

Include `SECURITY.md` explaining bot-token handling and least-privilege Discord permissions.

Do not claim legal certification or guaranteed Discord compliance.

---

## Tests

Use `httptest.Server` for Discord and LLM API tests.

At minimum test:

- history pagination
- timestamp boundary handling
- newest-to-oldest reversal
- duplicate handling
- message safety caps
- HTTP 429 behavior
- transient failure retries
- config validation
- state safe replacement / durability
- state not advanced after LLM failure
- state not advanced after Discord posting failure
- LLM JSON validation
- hallucinated source-ID rejection
- jump-link construction
- output-length enforcement
- allowed-mentions suppression
- timezone scheduling
- catch-up clamping
- secret/message redaction from logs

Provide an end-to-end test using fake Discord and fake LLM HTTP servers.

Required gates:

`go test ./...`

`go vet ./...`

---

## Repository scope

Keep packages limited to approximately:

- `internal/app`
- `internal/config`
- `internal/discord`
- `internal/digest`
- `internal/llm`
- `internal/state`

Do not create abstractions without an actual second use case.

Do not introduce a database.

Do not introduce a framework.

Do not implement future features opportunistically.

---

## v0.1 acceptance test

The build is complete when a user can:

1. Create an ordinary Discord bot.
2. Give it read-history access to one selected channel and send access to one digest channel.
3. Put the bot token in an environment variable.
4. Configure one OpenAI-compatible local LLM endpoint.
5. Run `cordbrief doctor`.
6. Run `cordbrief run --dry-run` and receive a useful digest with valid source references.
7. Run `cordbrief run` and see one compact digest appear in Discord.
8. Run `cordbrief serve` and receive subsequent digests at the configured local time.
9. Restart CordBrief without losing the last-window-end checkpoint.
10. Confirm there is no stored Discord-message archive anywhere on disk.

Anything beyond these acceptance criteria belongs after v0.1.