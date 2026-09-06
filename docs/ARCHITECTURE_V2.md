# CordBrief Architecture v2: Collector/Core Foundation

This document defines the production architecture for CordBrief v2, establishing the interface and operational boundaries between the containerized Discord collector and the core Go processing service.

---

## 1. High-Level Two-Service Architecture

CordBrief executes as a single Docker Compose application comprising two mutually isolated services:

```text
┌──────────────────────────────────────────────┐
│             cordbrief-collector              │
│  - Official Discord Desktop (Linux)          │
│  - Virtual Graphical Substrate (Xpra+Openbox)│
│  - Vencord & CordBriefCollector Userplugin   │
│  - In-process Gateway event filter           │
└──────────────────────┬───────────────────────┘
                       │ Writes events/*.ndjson, status, catalog
                       │ Reads watchlist.json
                       ▼
┌──────────────────────────────────────────────┐
│           /var/cordbrief/exchange            │
│         (Shared Filesystem Volume)           │
└──────────────────────┬───────────────────────┘
                       │ Writes watchlist.json, core-ack.json
                       │ Reads events/*.ndjson, status, catalog
                       ▼
┌──────────────────────────────────────────────┐
│                cordbrief-core                │
│  - Go Application (Zero Dependencies)        │
│  - Watchlist Management                      │
│  - Durable Journal Reader & Checkpointer     │
│  - Future: LLM Summarization & Delivery      │
└──────────────────────────────────────────────┘
```

### Locked Principle: Filesystem-Only Integration

The **only** boundary between `cordbrief-collector` and `cordbrief-core` is the shared filesystem exchange directory (`/var/cordbrief/exchange`).
- No inter-container HTTP/REST APIs.
- No WebSocket connections between services.
- No shared database (SQLite/Postgres).
- No Docker socket sharing or container inspection.

---

## 2. Security Boundaries & Credential Isolation

The architecture enforces strict separation of privilege and credentials:

| Capability / Resource | `cordbrief-collector` | `cordbrief-core` |
| :--- | :---: | :---: |
| Discord Desktop Profile (`~/.config/discord`) | **Read/Write** | **NO ACCESS** |
| Discord Keyring & Credentials | **Read/Write** | **NO ACCESS** |
| Discord In-Process Memory / DOM | **Read/Write** | **NO ACCESS** |
| LLM Provider API Keys (OpenAI, Gemini, etc.) | **NO ACCESS** | **Read/Write** |
| Email / Webhook Delivery Credentials | **NO ACCESS** | **Read/Write** |
| Core Data & State Storage | **NO ACCESS** | **Read/Write** |
| Exchange Volume (`/var/cordbrief/exchange`) | **Partitioned Access** | **Partitioned Access** |

- The collector has **zero** knowledge of LLM prompts, summarization logic, scheduling, or delivery credentials.
- Core has **zero** access to the user's Discord session tokens, account password, or keyring.

---

## 3. Exchange Directory Layout & Ownership

The exchange root `/var/cordbrief/exchange` has strictly defined, single-writer file ownership:

```text
exchange/
├── watchlist.json          # OWNER: core (Collector reads)
├── core-ack.json           # OWNER: core (Collector reads / internal)
├── catalog.json            # OWNER: collector (Core reads)
├── collector-status.json   # OWNER: collector (Core reads)
└── events/                 # OWNER: collector (Core reads)
    ├── 0000000000000001.ndjson
    ├── 0000000000000002.ndjson
    └── ...
```

### Safe Replacement for Control and Status Files

Neither service ever performs in-place overwrites on JSON control files (`watchlist.json`, `core-ack.json`, `catalog.json`, `collector-status.json`). Updates are executed via crash-resistant replacement:
1. Write payload to a unique temporary file in the same directory (`*.tmp`).
2. Flush and sync data to disk (`Sync()`).
3. Close the temporary file.
4. Replace destination file via rename (within the same Linux/Docker filesystem volume, this provides crash-resistant replacement).

---

## 4. Watchlist Contract (Fail-Closed)

The watchlist defines the exact set of Discord channel IDs that the collector is permitted to capture.

### Schema (`watchlist.json`)

```json
{
  "version": 1,
  "generation": 12,
  "channel_ids": [
    "123456789012345678",
    "234567890123456789"
  ]
}
```

### Fail-Closed Operational Rule

If `watchlist.json` is:
- Missing,
- Malformed (invalid JSON),
- An unsupported schema version (`version != 1`),
- Invalid (e.g. non-string elements, negative generation), or
- Contains an empty `channel_ids` array,

**The collector must persist ZERO Discord messages.** It must never fall back to capturing all channels or capturing unverified channels.

The collector publishes its currently applied `generation` in `collector-status.json`.

---

## 5. Event Journal Specification

The journal persists normalized events across sequentially numbered, fixed-size segments.

### File Naming and Sequencing

Segments reside in `events/` and are named using 16-digit, zero-padded hexadecimal or decimal integers:
```text
events/0000000000000001.ndjson
events/0000000000000002.ndjson
...
```
Segment numbers increase monotonically without gaps. Exactly **one** collector process writes to the active segment.

### Segment Rotation

- The active segment is strictly append-only.
- When an append causes the active segment to reach or exceed the configured threshold (`MaxSegmentSize`, default 32 MiB in production), the segment is closed and the collector advances to `segment + 1`.
- A rotation **never** splits a single JSON record. Rotation checks occur strictly on complete record boundaries.

### Crash Recovery on Segment Restart

When the collector starts or restarts:
1. It discovers the highest numbered segment in `events/`.
2. It inspects the trailing bytes of the active segment file.
3. If the segment ends without a newline (`\n`), indicating an ungraceful crash or torn write during an append, the collector truncates the file back to the last clean newline boundary, discarding the incomplete record and logging a warning.
4. Subsequent appends always begin on a clean newline, preventing record corruption or line merging.
5. In parallel, the Core reader ignores any un-terminated trailing line at watermark boundaries, guaranteeing the committed cursor remains at a valid record boundary.

### Event Schema v1 (`MESSAGE_CREATE`)

Each line in a segment file contains exactly one newline-terminated JSON object:

```json
{
  "version": 1,
  "event": "message_create",
  "message_id": "900000000000000001",
  "guild_id": "800000000000000001",
  "channel_id": "123456789012345678",
  "timestamp": "2026-09-03T17:02:25.718000+00:00",
  "captured_at": "2026-09-03T17:02:25.211Z",
  "author": {
    "id": "700000000000000001",
    "name": "example_user",
    "display_name": "Example User",
    "bot": false
  },
  "content": "Hello, world!",
  "reply_to_message_id": null,
  "attachments": []
}
```

- **No URL Generation**: Raw jump URLs are omitted. Core constructs jump links deterministically from `guild_id`, `channel_id`, and `message_id`.
- **No Payload Downloads**: Attachments record metadata only (`id`, `filename`, `content_type`, `size`). Payloads are not downloaded.

### Schema Evolution Policy (v1)

- **Additive Forward Compatibility**: Schema v1 explicitly permits unknown additive fields. Readers (`internal/journal/reader.go`) ignore unmapped JSON properties without failing or corrupting batch ingestion.
- **Fail-Closed Version Gate**: The `version` field specifies the wire protocol. Any record with an unknown or unsupported schema version (`version != 1`) fails closed immediately, halting ingestion to prevent silent data corruption or invalid state advancement.
- **Breaking Changes**: Any breaking field alteration, field deletion, or semantic change requires incrementing the schema version (`version: 2`) and updating all consumers simultaneously.

---

## 6. Core Checkpoint & Watermark Semantics

### The Durable Cursor (`core-ack.json`)

Core tracks consumption progress via an exact segment number and byte offset:

```json
{
  "version": 1,
  "segment": 1,
  "offset": 481927
}
```

- `segment`: 1-based index of the journal segment file.
- `offset`: byte offset pointing precisely to the beginning of the next complete record.

### Watermark Snapshot Semantics

To prevent race conditions with in-flight collector writes during batch processing:
1. Core captures a finite **Watermark** (listing current segments and their immutable byte lengths at snapshot time).
2. Core consumes records up to the watermark boundary. Any records appended by the collector after the snapshot belong to the subsequent batch.
3. Incomplete trailing lines (writes without a final `\n`) are ignored until the line is complete.
4. Malformed complete records fail loudly with file and offset context; data is never silently dropped.
5. Core commits the advanced cursor to `core-ack.json` **only after** downstream processing succeeds.

---

## 7. Operational Lifecycle

### Clean Login Mode vs Collector Mode

1. **Initial Setup (Clean Login)**:
   - On first deployment, Discord runs in clean desktop mode under Xpra.
   - The user opens `http://127.0.0.1:14500/` and authenticates using official Discord QR login.
   - Discord's authenticated session and encryption keys persist into the Docker named volumes (`discord_profile`, `discord_keyring`).
2. **Collector Mode (Headless Background)**:
   - Container reboots directly into the authenticated session with Vencord + `CordBriefCollector` loaded.
   - No browser client needs to remain attached.
   - The collector monitors Gateway `MESSAGE_CREATE` events in the background, applies `watchlist.json`, and writes segments to `/var/cordbrief/exchange/events/`.

---

## 8. Operational Telemetry & Authentication Status

In `collector-status.json`, the field `discord_authenticated` is a nullable boolean (`true`, `false`, or `null`):
- `true`: The collector renderer observes that Discord is currently mounted and active on the authenticated app route (`window.location.pathname.startsWith("/channels")`).
- `false`: The collector renderer observes an explicit unauthenticated route (`/login` or `/register`).
- `null`: Indeterminate or initial startup state before renderer route observation.

> [!NOTE]
> This is an **operational observation** of the client's rendered application route, **not** a cryptographic token inspection or verification. CordBrief deliberately avoids inspecting, extracting, or validating private Discord authentication tokens.

---

## 9. Known Future Hardening Items

The following architectural items are intentionally deferred beyond the foundational milestones:

1. **Electron Sandbox Hardening**: The production collector currently executes with `ELECTRON_DISABLE_SANDBOX=1` to allow Discord and updater relaunches inside standard unprivileged Docker containers. Future hardening should evaluate Linux user namespaces (`CLONE_NEWUSER`) or explicit unprivileged sandbox capabilities (`SYS_ADMIN` / seccomp filters).
2. **Journal Retention & Garbage Collection**: Segments are append-only and monotonically numbered. A future lifecycle worker will safely delete segments where `segment < core_committed_segment` and older than the raw-message retention policy.
3. **History Gap Recovery Across Downtime**: If the collector container is stopped during message delivery, Discord Gateway does not backfill missed events upon reconnect. A gap-recovery mechanism will be evaluated.
4. **Discord Reauthentication Lifecycle**: Handling session expiration or credential revocation through automated notification or health alerts rather than manual inspection.
5. **Discord & Vencord Update Lifecycle**: Automating upstream Discord `.deb` updates and Vencord git bumps without manual container rebuilds.
6. **Production Image Optimization**: Multi-stage image minimization to prune intermediate Node/pnpm build caches and unneeded build tools from the final collector image.
7. **Channel Catalog Live Publication**: The `catalog.json` schema and reader are implemented; live in-process extraction via Discord client stores is deferred to future UI milestones.
8. **Long-Duration Soak Testing**: Multi-day stress testing under high-traffic multi-guild scenarios.

---

## 10. The Digest Processing Transaction & Idempotency

### Canonical 10-Step Processing Transaction

To guarantee crash resilience and zero message loss, the Core digest generation executes as a strict one-way transaction:

1. **Load Committed Cursor**: Read committed `(segment, offset)` from `/var/cordbrief/exchange/core-ack.json`.
2. **Capture Watermark**: Read current segment list and exact immutable byte boundaries (`CaptureWatermark`).
3. **Read Records**: Read complete journal records from the cursor to the watermark boundary.
4. **Construct Deterministic DigestBatch**:
   - Filter excluded events (e.g. bots when `ignore_bots = true`).
   - Assign sequential local source IDs (`S000001`, `S000002`...) mapping to `(guild_id, channel_id, message_id)`.
   - Calculate deterministic `BatchID = SHA-256(version, start_cursor, end_cursor, canonical_messages)`.
5. **Check Existing Artifact (Idempotency Check)**:
   - Check if `/var/cordbrief/data/digests/<batch-id>.json` already exists.
   - If an artifact exists with identical cursor boundaries: validate it and advance the cursor immediately **without** calling the LLM.
   - If an artifact exists with mismatched boundaries: fail loudly (never overwrite conflicting history).
6. **Generate Digest via LLM**:
   - Single-Chunk Fast Path: If total input characters <= `max_input_chars`, execute a single completion call.
   - Chunk/Reduce Path: If over budget, split messages deterministically into chunks without splitting individual records, generate structured chunk summaries, and reduce into the final digest while preserving original `Sxxxxxx` source IDs.
7. **Strict Validation**:
   - Verify every cited source ID exists in the batch.
   - Reject unknown IDs, invalid item kinds, or ungrounded substantive claims.
8. **Persist Digest Artifact**:
   - Write `/var/cordbrief/data/digests/<batch-id>.json` using crash-resistant safe replacement (`.tmp` write, sync, close, rename).
9. **Verify Artifact on Disk**: Ensure file exists and is readable.
10. **Commit Journal Cursor**:
    - Write the advanced cursor to `/var/cordbrief/exchange/core-ack.json` using crash-resistant replacement.

> [!IMPORTANT]
> If any step prior to Step 10 fails (provider timeout, network error, malformed JSON, invalid citations, disk error), `core-ack.json` is **never** updated. On subsequent runs, the pipeline restarts from the previous uncommitted cursor.

### Prompt-Injection Defense Boundary

Discord message content is untrusted user input:
- The model has **zero tools** or code execution capabilities.
- Discord messages are packaged as structured data payloads, clearly separated from system instructions.
- System prompts instruct the model that messages are conversation content, not instructions, and to ignore any commands inside message text.
- No Discord message content is ever interpolated into the system prompt.

---

## 11. Supported LLM Providers & Configuration

CordBrief officially supports two primary provider options sharing a single, minimal `net/http` OpenAI-compatible transport:

### 1. Gemini (Cloud Default)
- **Transport**: Official Google OpenAI-compatible chat completions endpoint (`https://generativelanguage.googleapis.com/v1beta/openai/chat/completions`).
- **Default Model**: `gemini-3.7-flash` (configurable).
- **Authentication**: `GEMINI_API_KEY` environment variable passed via `Authorization: Bearer <KEY>`. Keys are never logged or stored in digest artifacts.
- **Data Path & Privacy**: Gemini API keys are available via Google AI Studio. The free tier may use input data to improve Google products. CordBrief transmits selected Discord messages to the configured provider; users requiring strict on-premises data isolation should select **Local LLM**.

### 2. Local LLM (Self-Hosted / Private)
- **Transport**: Standard OpenAI-compatible HTTP chat completions (`/v1/chat/completions` or `/chat/completions`).
- **Target Runtimes**: llama.cpp server, LM Studio, vLLM, Ollama (OpenAI compatibility mode).
- **Configuration**: Requires `base_url` (e.g. `http://host.docker.internal:8081/v1` when Core runs in Docker) and `model`. API key is optional.
- **Docker Networking**: Inside Docker containers, servers running on the host machine must typically be reached via `host.docker.internal` rather than `localhost`.
