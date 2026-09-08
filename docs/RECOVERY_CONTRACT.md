# CordBrief M13 Phase 3C — recovery contract for deployment review

The pending-bound corruption blocker is repaired. Production deployment and
restart evidence are recorded in [PHASE_3C_DEPLOYMENT.md](PHASE_3C_DEPLOYMENT.md).
Development verification is tracked in `GATES.md`.

This document supersedes the provisional Phase 3C.1–3C.4 designs. Phase 3C was
deployed on 2026-09-08 after explicit authorization. Journal GC remains absent;
no remote push was performed.

## Achievable contract

CordBrief preserves successfully persisted journal records and recovers eligible
messages that remain available through Discord's paginated message history long
enough for a successful replay sweep. Recovery requires continuing successful
requests, intact local state/journal, and resolution of any interrupted page.
An event lost before persistence and unavailable from both REST and Gateway
replay cannot be reconstructed. That limitation is explicitly accepted.

Eligibility is an immutable Discord-ID boundary, `watch_after`, not the largest
message seen, the local clock, or a recent-ID cache. Returning a REST page proves
which records were returned; it does not prove permanent completeness of all
lower IDs. In particular, later visibility changes cannot safely be handled by
querying only after a monotonically increasing high-water mark.

## First-watch activation and historical overlap

Adding a channel requests activation. Before awaiting initial REST, native saves
an open initialization record. Gateway capture starts immediately when the
allowlist permits it; there is no renderer recovery buffer or drain phase.

For a successful initial latest-message response H, the activation boundary is
the start of H's Discord timestamp millisecond, inclusive. Stored numerically:
`watch_after = max(0, ((H >> 22) << 22) - 1)`. Local time is not used. This defines
the server-time start of the watched range. Messages before that activation
range are not promised merely because a watch request was outstanding; any
already journaled live events are nevertheless retained.

Initial lookup writes no transcript. Subsequent replay can include H and other
messages in that same millisecond. This is a deliberate, time-bounded historical
overlap, not a fixed message-count bound. Messages in earlier milliseconds are
excluded in normal initialization. Lower numeric IDs in the anchor millisecond
remain eligible, including the original M=202/H=203 race.

Empty/invisible/error responses establish no exclusion ID. A crash or retry of
an open initialization never chooses a newer cutoff: its `watch_after` remains
zero. History made visible later can therefore be imported, including pre-watch
history. Without a trustworthy initial anchor, the implementation prioritizes
recoverability over guessing a historical exclusion boundary.

## Live capture and exact deduplication

The actual Gateway handler checks the allowlist, normalizes the event, and calls
native append immediately, even during initialization and REST recovery. It does
not change the REST high-water or replay cursor. Native validates recovery state
before accepting live writes and uses the same per-record journal fsync path as
REST. A write error blocks further writes until restart/repair.

Native scans retained history at startup. The bounded recent-ID cache is only an
optimization. A cache miss proves absence only while the cache demonstrably
contains every journal record; otherwise an exact maximum can prove an ID is
new, or native scans the retained journal. Clearing the cache does not change
correctness. No new persistent ID index or raw transcript copy was added.

Only the highest active file is eligible for trailing-record repair. Closed
segments must validate and remain unchanged. Startup rejects malformed records,
historical duplicate IDs, and missing/invalid segment topology.

## Established-channel replay

Each finite sweep snapshots a latest visible ID as `scan_until` and starts at
`watch_after`. A run processes at most ten data pages (100 messages per request),
plus one latest-ID lookup when starting a sweep. The durable `scan_after` lets a
capped run continue instead of repeatedly processing only its first pages.

On reaching the saved upper bound, or receiving an empty visible page, the sweep
ends. A later run starts another sweep from the immutable lower bound. An empty
response ends a visibility scan; it does not certify remote completeness. Errors
preserve retry state. Continuous traffic cannot extend the current sweep's saved
upper bound forever. A channel removed during a REST request has that uncommitted
page refused; already accepted native writes can finish.

**Cost:** sweeps revisit the watched range. Total REST and local scan work grows
with history, although work per run is capped. This is the conservative cost of
recovering later-visible IDs without a server completeness watermark. Startup
and journal scans also scale with retained history; scan memory is not a fixed
constant. No performance or throughput claim was established by these tests.

## Durable fields (schema version 2)

| Field | Exact meaning |
|---|---|
| `checkpoint_message_id` | Nondecreasing high-water K of committed REST pages, or an explicitly labelled initial/legacy value. It is **not** the immutable replay exclusion bound or a permanent completeness claim. |
| `checkpoint_source` | `baseline_rest`: Discord-derived initial exclusion value; `baseline_pending`: no exclusion ID yet; `rest`: REST high-water; `legacy`: preserved v1 value with unknown provenance. |
| `watch_after` | Immutable exclusive replay lower bound. Zero for uncertain initialization and v1 migration. |
| `scan_after` | Last page end durably committed in the current finite sweep; null between sweeps. |
| `scan_until` | Saved inclusive upper bound for that sweep; null together with `scan_after`. |
| `checkpoint_journal_boundary` | Forward-recovery floor: every local channel ID greater than K is at/after this position. It does **not** cover replay of IDs at/below K. |
| pending `channel_id` | Owner of the single interrupted page transaction. |
| pending `old_checkpoint_message_id` | K before that page. |
| pending `new_checkpoint_message_id` | Retained wire name for that page's greatest ID. During historical replay it can be below K; committed K is the maximum of old K and this ID. |
| pending `message_ids` | Exact ordered page identity, bounded by the REST page size in the renderer. No message bodies. |
| pending `journal_start` | Original append boundary of the transaction; retained on exact refetch. |
| `last_recovery_at`, `last_result`, `last_error`, `recovered_count` | Diagnostics, not authority for exclusion or deletion. Reconciliation can undercount recovered records after a crash. |

For a forward page, deduplication scans from the older of the forward floor and
pending start. For a replay page touching IDs at/below K, it scans from `(1,0)`.
Floor updates inspect local records rather than assuming REST empty means all
history was seen. The 101-message, three-page, quiet-floor, and page-cap cases
retain their original exactly-once assertions.

**No GC predicate follows from the forward floor alone.** Historical replay is
an additional journal consumer. All production journal files remain retained;
this work is recovery readiness, not authorization for destructive retention.

## Pending transactions and crashes

Native serializes page transactions. A new intent is saved before page append;
each missing record is written and fsynced; only then are K, the floor and replay
cursor committed together with clearing the intent. On restart, an all-present
intent is reconciled from journal evidence. A partial intent requires exact
channel/page-ID refetch and retains its original start. Mismatches and unrelated
pages cannot overwrite it.

If Discord permanently changes or removes the outstanding page, recovery can
remain blocked with the original intent intact. This is an explicit fail-closed
limitation, not a claim of successful automatic repair. Existing-channel live
capture remains independent. No intent-abandonment mechanism was introduced.

| Crash boundary | Durable state and restart behavior | Evidence |
|---|---|---|
| Initial open record saved, before REST | No exclusion; restart replays from zero. Historical overlap may be broad. | Fresh-process tested |
| H saved, late M received before native append | Anchor millisecond remains eligible; REST replay recovers M once. | Original actual-code counterexample now passes |
| Before/after an immediate live append | Before: requires later source; after fsync: exact journal dedupe prevents replay duplicates. | Fresh-process tested |
| Sweep bounds saved | Resume from saved lower/upper bounds; no page progress asserted. | Fresh-process tested |
| Page intent saved / partial append | Old cursor remains; exact refetch scans and appends only missing IDs. | Partial-fsync process-death tested; zero-prefix follows same intent path |
| All records saved, before checkpoint replacement | Intent reconciliation commits only after all IDs are found. | Cross-segment process-death tested |
| Page metadata committed | Resume from durable page end; future sweeps still revisit lower IDs. | Fresh-process tested |
| Sweep finish committed | Next sweep restarts from immutable `watch_after`; dedupe uses durable history. | Fresh-process tested |
| Torn active tail | Repair active trailing bytes, then validate retained history. | Actual-writer test; closed file byte identity checked |

Linux syncs files and parent directory entries for new journal files and JSON
replacement. Windows Node directory-fsync is unavailable and is explicitly
skipped. Tests simulate process death, not sudden power loss or faulty storage;
Linux filesystem durability ordering is source-reviewed, not power-cut proven.

## Migration and corruption handling

V1 K values are preserved, but v1 cannot distinguish a clock-derived baseline
from real REST progress. Consequently migration sets `watch_after=0`, resets
physical floors and pending starts to `(1,0)`, and resets sweep bounds. It does
not use an oversized legacy K to exclude lower-ID recovery.

**Deployment consequence:** migration can import previously uncaptured pre-watch
Discord history. Existing journal records deduplicate. Avoiding that import by
trusting an unproven v1 cutoff would preserve the old exclusion risk. This choice
is deliberate and must be visible in deployment review.

Migration validates the retained prefix/topology, every channel and pending
identity before replacing state. Valid unsafe legacy coordinates are reset;
structurally invalid coordinates are refused. A second load is byte-identical.
Legacy-profile import uses the same validation; an existing primary file wins,
including when it is malformed. Invalid primary state cannot be bypassed by a
stale profile copy. Unknown or provisional v2 state lacking the final replay
fields is refused; v2 was not deployed during this work.

Missing state with existing journal files is refused. A true empty installation
can initialize. Missing channel metadata for journaled channels is refused.
Malformed JSON, unknown versions, invalid IDs/bounds, and unknown metadata fields
are rejected without replacing the original state file. Legacy error text is
replaced by a generic diagnostic rather than copying arbitrary error contents.
No message bodies, author names, attachments, credentials or session material are
added to recovery metadata.

## Complete state preflight

`validateRecoveryState` is shared by disk loads and proposed replacements. It
performs no writes. Startup first builds a read-only complete-record journal
snapshot, including the complete prefix of a torn active tail. Only after the
entire state validates may it repair that tail, migrate v1, reconcile pending,
or publish status. Failed startup/recovery leaves even the torn bytes and
existing status files unchanged. Valid tail repair still fsyncs the active file.

The v2 cross-field invariants are:

- `watch_after <= K` (empty K means zero). A nonzero watch bound is the end of
  a Discord timestamp millisecond. Legacy provenance has watch bound zero.
- Pending initialization has empty K and watch bound zero. REST-derived baseline
  provenance has K equal to the watch bound. REST progress has K strictly above
  the watch bound and a durable message with that ID in the same channel.
- Sweep fields are present and jointly null or jointly valid IDs. For an active
  sweep: `watch_after <= scan_after <= scan_until`, `scan_until > watch_after`,
  and `scan_after <= K`. A noninitial cursor has a durable same-channel witness.
- The sweep upper bound is remote evidence, so it need not be journaled and
  **may be below K**. Historical replay need not advance K.
- Every journaled channel has state. Physical boundaries are valid record
  boundaries within the complete retained snapshot. No local channel ID above
  K may precede that channel's forward dedupe floor.
- Pending has an existing owner, exactly the current old K, 1-100 strictly
  increasing IDs after its replay cursor (or watch bound without a sweep), and
  a page-end ID equal to the last ID. With a sweep, page end cannot exceed
  `scan_until`. Without a sweep, legacy/direct pending transactions remain valid.
- Pending start is within the journal and not before its channel's floor. Any
  already-journaled pending ID must belong to the pending channel. Missing page
  IDs remain legitimate interrupted-write evidence, not corruption.
- All channels and the pending transaction pass preflight before any one of them
  can be reconciled. V1 transformations occur only in memory until validation of
  the complete migrated state succeeds. Unknown or contradictory state cannot
  be repaired by discarding a field or clearing pending.

The original pending-bounds regression is preserved. A second actual-code suite
checks 20 sibling contradictions with file hashes before and after startup and
an attempted recovery, including a torn active tail in every case. It also checks
invalid direct saves, invalid legacy import, and three valid controls: normal
reconciliation, replay below K, and pending without an active sweep. These
controls prevent rejecting legitimate historical replay merely to fail closed.

These checks establish internal consistency and journal evidence. They cannot
authenticate a coherently forged metadata history without an independent source
of truth; they do not claim to detect every possible bit change that happens to
produce a different internally valid state.

## Verification and limits

Actual renderer/native code is loaded by the Node child harness; REST and Gateway
inputs are mocked. Disposable journals use the actual writer and real rotation.
The original first-watch test still injects M after baseline IPC and kills the
process before M is persisted. Its old K-equals-H assertion was replaced by the
required invariant that M remains eligible; its exactly-one-copy oracle remains.
Old buffered-drain hooks now target before/after immediate persistence because
the buffer was deleted. The original 101-message fixture and uniqueness oracle
were retained.

Measured cases: 101 channel IDs exactly once across 55 segments; 250 IDs across
272 total records/136 segments; 1,050 IDs across 1,119 records/560 segments with
ten-page continuation. Focused tests add delayed visibility below K, empty-cache
live replay, first-watch error/empty/reordered arrival, watch removal, four new
process-crash points, an oversized legacy cutoff, 15 malformed-state cases,
missing/invalid topology, lost-versus-fresh state, legacy import, partial pending
v1 migration, and byte-identical reloads. Windows and Linux actual-code runs are
required before marking the accompanying gates complete.

The writer/recovery legacy suites include copied-model characterizations; their
passes are not substituted for actual-code proof. Runtime retention tests concern
disposable staged releases, not journal GC. Operational tests use mock Discord
launches; ENOENT messages and the Windows staging-prune warning are not live
runtime evidence. Linux locking/maintenance is tested in a disposable container.
Go tests, vet and module listing cover unchanged Core; module listing is exactly
`cordbrief`.

The development tests above use mocked Discord inputs. Separate production
observation and real REST replay are documented in PHASE_3C_DEPLOYMENT.md, including
the intentional historical overlap. Recurring replay costs remain as stated above.
The accepted missing-source limitation remains a negative control in
`recovery_contract_test.mjs --require-unconditional`; normal/safe mode checks the
achievable contract instead.
