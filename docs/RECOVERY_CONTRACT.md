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

The collector durably records initialization before processing messages. Local RPC
event capture starts as soon as the watchlist allows it, streaming `MESSAGE_CREATE`
dispatches directly into native journal persistence.

For a successful initial channel discovery with latest visible message H, the
exclusion boundary is:

```text
watch_after = max(0, ((H >> 22) << 22) - 1)
```

This includes H's entire Discord timestamp millisecond. Messages in earlier
milliseconds are excluded from replay, while already-persisted live messages remain
accepted. Same-millisecond IDs below H remain eligible.

An empty or failed lookup establishes no exclusion ID. An interrupted or retried
initialization keeps `watch_after=0` instead of selecting an unverified cutoff.
Later visibility can therefore bring in pre-watch history. Recoverability takes
priority over guessing an exclusion boundary when no trustworthy anchor exists.

## Local RPC snapshot bounds and dedupe

Unlike arbitrary REST scraping, official Discord RPC exposes a bounded snapshot of
messages cached in the desktop client via `GET_CHANNEL`. Bounded snapshots have no
guarantee of arbitrary historical depth or pagination into deep history; recovery
is best-effort across the client's visible window.

Snapshot overlap and live event reconciliation use exact durable identities. The
collector scans identity evidence at startup; its bounded recent cache is an
optimization. An exact maximum can prove an ID is new, but older cache misses
need a durable scan against journal segments or certified retention sidecars.
Clearing the cache does not change correctness. An ID belonging to another channel
is refused rather than counted as a duplicate.

[Retention](RETENTION.md) substitutes certified identity/position sidecars for
retired transcript segments. This preserves original positions for dedupe floors
and state witnesses. Arbitrary missing segments are never silently skipped.

Watch removal prevents in-flight recovery transactions from committing. Already
accepted native writes can finish. Removal does not erase durable recovery state;
re-adding a channel preserves its watch boundary and recovery progress.

## Durable state: version 2

| Field | Meaning |
|---|---|
| `checkpoint_message_id` | Nondecreasing high-water K of committed REST pages, or an explicitly labelled baseline/legacy value. Not a completeness certificate. |
| `checkpoint_source` | `baseline_rest`: initial exclusion derived from Discord; `baseline_pending`: no exclusion yet; `rest`: REST high-water; `legacy`: preserved v1 value with unknown provenance. |
| `watch_after` | Immutable exclusive replay lower bound. Zero for uncertain initialization and v1 migration. |
| `scan_after` | Last committed page end in the current sweep; null between sweeps. |
| `scan_until` | Saved inclusive upper bound of that sweep; null together with `scan_after`. |
| `checkpoint_journal_boundary` | Every already-journaled channel ID above K is at/after this physical position. It does not cover historical replay at/below K. |
| pending `channel_id` | Owner of the single interrupted page transaction. |
| pending `old_checkpoint_message_id` | K before that page. |
| pending `new_checkpoint_message_id` | The page's greatest ID, possibly below K during historical replay. Committed K is the maximum of old K and this value. |
| pending `message_ids` | Exact ordered identity of the page. |
| pending `journal_start` | Original boundary before the transaction's appends. Preserved across retries. |
| `last_recovery_at`, `last_result`, `last_error`, `recovered_count` | Diagnostics, not authority for deleting history. |

## Transactions and validation

An intent is saved before missing page records are appended. Each append is
synced before checkpoint commit. After a crash, complete durable page evidence
can reconcile the intent. An incomplete transaction can resume when the same
page is refetched, keeping its original `journal_start`. A different page or
another channel cannot overwrite an unresolved intent. A permanently changed
page may require investigation; clearing pending is not a safe recovery procedure.

All state validates before reconciliation, migration, or even active-tail repair.
In particular:

- Every journaled channel has metadata. IDs and physical boundaries are valid.
- `watch_after <= K`. A REST K has a same-channel durable witness; legacy K
  may have unknown provenance. Baseline provenance constrains the permitted ID.
- Sweep bounds are both null or satisfy `watch_after <= scan_after <= scan_until`,
  `scan_until > watch_after`, and `scan_after <= K`. A progressed scan cursor has
  a same-channel witness. `scan_until` may legitimately be below K.
- No local channel ID above K precedes that channel's forward physical floor.
- Pending has a known owner and the current old K, 1–100 strictly increasing
  IDs after the replay cursor, and a page end equal to the final ID. It cannot
  exceed an active sweep's upper bound. Its journal start is not before the floor
  or beyond the journal end. Existing pending IDs belong to the same channel.

Malformed or contradictory state fails before rewriting the original file.
Only a valid active trailing fragment can be repaired; closed segments are
immutable. These checks establish consistency, not authenticity against someone
who can coherently rewrite all local state and its evidence.

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

Actual native/renderer tests use synthetic Gateway and REST inputs, real journal
rotation, fresh processes, and injected crashes. They cover first-watch races,
multi-page replay, cache clearing, removal/re-addition, pending refetch, migration,
state corruption, and certified missing prefixes. See [contributing](../CONTRIBUTING.md)
for the commands. Older copied-model tests are not substitutes for these proofs.

REST sweeps, identity scans, and exact duplicate checks grow with history. Work
per run is capped; total work and memory are not constant. Retention removes
transcript bytes but keeps identity evidence. No physical power-cut or fixed
throughput guarantee is claimed.
