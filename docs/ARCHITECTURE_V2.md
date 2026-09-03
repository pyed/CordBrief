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
