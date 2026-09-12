# Recovery contract

CordBrief preserves durably accepted message identities and captures eligible
messages via official Discord local RPC. Under the official RPC architecture,
the collector connects to the running Discord client via a local IPC socket,
subscribing to live `MESSAGE_CREATE` events for watched channels and performing
bounded snapshot recovery (`GET_CHANNEL`). Recovery depends on successful RPC
responses, intact local evidence, and resolution of any interrupted transaction.
A message lost before local persistence and unavailable in Discord's local client
cache cannot be reconstructed.

Eligibility depends on the immutable `watch_after` boundary, not the largest ID
seen or the local clock.

## First watch and snapshot recovery

The collector queries pre-watch Discord history and durably records its exclusion
baseline before subscribing to live `MESSAGE_CREATE` events. After subscribing,
it reconciles a second snapshot against that same baseline and deduplicates any
overlap with live capture. Messages arriving between the baseline observation and
subscription remain eligible if the snapshot exposes them.

For a successful initial channel discovery with latest visible message H, the
exclusion boundary is:

```text
watch_after = max(0, ((H >> 22) << 22) - 1)
```

This includes H's entire Discord timestamp millisecond. Messages in earlier
milliseconds are excluded from replay, while already-persisted live messages remain
accepted. Same-millisecond IDs below H remain eligible.

A successful empty lookup establishes the explicit baseline `watch_after="0"`.
A failed lookup leaves initialization incomplete and does not activate a live
subscription. Once persisted, the baseline is write-once: retries and restarts
reuse it, including an established empty baseline. Later visibility after an empty
lookup can therefore bring in pre-watch history; no newer cutoff is guessed.

Immutable provenance is stored independently in `recovery-state.json.anchors`
(the configured recovery-state path plus `.anchors`). This private JSON file has
`version: 1` and a `channels` map from channel ID to its original `watch_after`.
It is atomically replaced and synced with mode `0600` **before** the corresponding
recovery-state commit and before subscription. Its entries survive even when no
user message has ever been journaled. Valid existing recovery-state is adopted
into this provenance file before recovery proceeds on upgrade.
Reading an existing anchor also re-syncs its containing directory before the
anchor can authorize operation. A visible rename after a failed directory sync
does not suffice: retries remain closed until that durability barrier succeeds.

A channel ID identifies the continuing watch episode: removing a channel suspends
collection; re-adding it resumes the original baseline. Restart, state loss, and
journal retirement never create a new episode or remove its anchor. A genuinely
new channel with no anchor or journal evidence can initialize normally.

If recovery-state is missing, malformed, or lacks an anchored channel's original
baseline/checkpoint, collection fails visibly closed. A crash after anchor commit
but before recovery-state commit also fails closed. The collector does not
automatically reconstruct recovery-state or query a replacement cutoff. Restore
valid recovery-state consistent with the anchors; the normal heartbeat retry then
reconciles against those same baselines. Malformed anchor data is also refused.
Back up both files together. Simultaneous loss of both authoritative recovery
files may make recovery impossible. An anchor already observed by the running
collector, or surviving status showing successful recovery with watched channels,
is a conservative tripwire: both files missing then means error, not fresh
initialization. That diagnosed error is retained through subsequent status updates
and restarts. Status never supplies message IDs, a baseline, or a checkpoint;
it can only refuse operation. When no such evidence survives, it cannot distinguish
total historical erasure from a truly fresh installation. Fresh installations
and genuinely new channels with intact installation provenance still initialize
normally. No additional authoritative recovery store is used.

All asynchronous recovery operations share one collector-owned queue. Startup,
watchlist changes, and daemon retries use the same reconciliation owner; triggers
received during a pass request another pass over the latest watchlist. Live
capture remains synchronous. After awaiting Discord, recovery reloads durable
state and merges live checkpoints instead of saving an older object. A newer
recovery error or shutdown invalidates pending work. Global running/ready is
published only after the current watched set has recovered successfully. Persistent
recoverable errors are retried by the daemon's five-second heartbeat.

Once journal uncertainty is classified fatal, the collector signals the daemon
without further I/O, and the daemon immediately exits nonzero for supervisor
restart. Fatal status publication and logging are deliberately omitted: they
cannot be allowed to block termination. The last status file may consequently
remain unchanged until the replacement process starts.

## Canonical v2 Official RPC Recovery Architecture

In CordBrief v2.0.0, the collection and recovery architecture is strictly based on the official Discord desktop client local RPC protocol:

1. **Authoritative Live Capture**: When connected, live `MESSAGE_CREATE` RPC dispatch events are authoritative.
2. **Bounded Snapshot Recovery**: On initial channel watch or after reconnection, the collector performs bounded, best-effort recovery via `GET_CHANNEL`. The desktop client returns its currently visible cached window of messages.
3. **No Deep Pagination Guarantee**: Official local RPC provides bounded recent client cache visibility, not an arbitrary historical pagination API. Long outages exceeding the local client's in-memory buffer depth cannot be retrieved via local RPC; no lossless arbitrary-outage claim is made.
4. **Append-Before-Checkpoint Ordering**: Recovery transactions strictly follow physical write ordering:
   - Visible messages are sorted ascending by Snowflake and filtered against `watch_after`.
   - Each eligible candidate is checked against the authoritative deduplication ledger (in-memory cache + physical journal scan if candidate ID $\le$ `journalMaxID`).
   - New messages are appended to the active NDJSON segment with synchronous file flush (`fsync`).
   - Only after all eligible events are durably persisted to disk is `checkpoint_message_id` advanced to the highest message Snowflake and `checkpoint_journal_boundary` updated.
   - `recovery-state.json` is atomically replaced via `safeReplaceJSON` with mode `0600`.

## Durable state: Schema version 2

### Active Canonical RPC Fields

| Field | Meaning |
|---|---|
| `checkpoint_message_id` | Nondecreasing high-water K of durably committed messages, or an explicitly labelled baseline value. Updated only after physical journal sync. |
| `checkpoint_source` | `rpc`: official Discord RPC snapshot recovery; `baseline_pending`: initial watch anchor pending. |
| `watch_after` | Immutable exclusive replay lower bound established at first watch anchor. Pre-watch history ($\le$ `watch_after`) is excluded. |
| `checkpoint_journal_boundary` | Physical journal segment number and byte offset where events through K are safely persisted. |
| `last_recovery_at`, `last_result`, `last_error`, `recovered_count` | Operational telemetry and diagnostics. |

### Legacy / Historical Compatibility Fields

These fields are preserved in Schema v2 for backward compatibility when loading historical state from prior versions; in the canonical v2 RPC collector, they remain `null`:

| Field | Status in v2 | Historical Meaning |
|---|---|---|
| `scan_after`, `scan_until` | `null` | Historical multi-page sweep cursors from legacy scraping paths. |
| `pending` | `null` | Historical single-page incomplete transaction intent descriptor. |
| `checkpoint_source` (`legacy`, `rest`, `baseline_rest`) | Historical | Provenance markers from pre-v2 installations. |

## Invariants and Validation

All recovery state validates before reconciliation, reload, or tail repair:

- Every watched channel has an entry in `channels`.
- `watch_after <= checkpoint_message_id`. An RPC checkpoint has a durable journal witness.
- Active-tail repair truncates only torn trailing fragments in the active segment; closed physical segments are immutable.
- A conflicting message ID mapped to a different channel triggers a fail-closed error to prevent journal corruption.

## Legacy migration

V1 did not distinguish verified REST progress from clock-derived initialization.
Migration preserves K, marks it `legacy`, sets `watch_after=0`, resets physical
floors and pending starts to `(1,0)`, and clears sweep bounds. It validates the
complete result before saving; a second load is byte-identical. This can import
previously unseen pre-watch history, but never excludes lower IDs using an
unproven legacy cutoff.

An existing primary state file always wins, including when corrupt. Missing
state with existing journal evidence is refused; a genuinely empty installation
can initialize. Unknown or incomplete v2 schemas are not treated as fresh state.

## Evidence and limits

The RPC state-ownership and daemon tests exercise real watchlist callbacks,
overlapping retry calls, corruption during an awaited snapshot, live checkpoint
merging, shutdown, and fatal-exit ordering with synthetic local RPC inputs.

Actual native/renderer tests use synthetic Gateway and REST inputs, real journal
rotation, fresh processes, and injected crashes. They cover first-watch races,
multi-page replay, cache clearing, removal/re-addition, pending refetch, migration,
state corruption, and certified missing prefixes. See [contributing](../CONTRIBUTING.md)
for the commands. Older copied-model tests are not substitutes for these proofs.

Recovery snapshot sweeps, identity scans, and exact duplicate checks grow with history. Work
per run is capped; total work and memory are not constant. Retention removes
transcript bytes but keeps identity evidence. No physical power-cut or fixed
throughput guarantee is claimed.
