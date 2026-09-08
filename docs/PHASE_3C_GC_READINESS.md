# CORDBRIEF M13 PHASE 3C GC-READINESS REVIEW

Historical investigation below; its failing counterexamples were subsequently
fixed. Current behavior is defined by [RECOVERY_CONTRACT.md](RECOVERY_CONTRACT.md),
and production completion by [PHASE_3C_DEPLOYMENT.md](PHASE_3C_DEPLOYMENT.md).
Destructive journal GC remains unimplemented and unauthorized.

**Disposition: BLOCKED, not ready for destructive GC.** Inspection began at clean commit
`27196c587202c66c7972b6ad04cf424927b7050f`; annotated tag `m13-durable-outbox`
peels to that commit. No production implementation was changed. No commit, tag, push,
deployment, production rotation, or production journal deletion was performed.

The requested stop condition applies: the actual native implementation duplicates an
existing message across recovery pages, without deleting any journal history. The new
disposable harness preserves this counterexample. Its characterization mode passes;
its `--require-safe` mode fails. A passing characterization is not a safety certification.

## 1. First-principles M9 model

Authoritative source: `collector/plugin/index.ts` and `collector/plugin/native.ts`.

The renderer checks the watchlist on `MESSAGE_CREATE`. Unreconciled/recovering channels
buffer live messages in memory; reconciled channels normalize them and call native
`appendEventToJournal`. That native function rotates files and appends synchronously,
but does **not** explicitly fsync live appends or update recovery checkpoints.

`runGapRecovery` loads one watchlist and recovery-state snapshot, then processes channels
sequentially. First watch fetches the latest message with `limit: 1`, saves its ID as an
intentional no-backfill baseline, and appends no historical page. Its fallback also uses
a timestamp-derived snowflake when the response is unsuccessful or empty; those two
conditions are not distinguished. That fallback is not verified Discord history.

Existing channels fetch `after: currentCheckpoint, limit: 100`, sort IDs with `BigInt`,
and commit each page through the native promise-chain mutex. There are at most ten pages
per run. There is no separately persisted recovery-session high-water mark: the page's
newest ID is passed as `newCheckpoint`. A ten-page limit still leads to `chSuccess=true`.

Native page commit:

1. Attempt to reconcile an existing pending page.
2. Capture the current journal end, and choose the earlier of that end and the channel's
   saved physical boundary as the overlap scan start.
3. Save a pending intent containing channel, old/new checkpoint IDs, all page message IDs,
   and the current journal end. No raw transcript is saved in the intent.
4. Scan retained NDJSON from the overlap boundary; combine this with a bounded recent-ID
   cache to omit already-present IDs.
5. Append missing records, rotating as needed and fsyncing each appended record's file.
6. Save the page checkpoint, **set its physical boundary to the current journal end**,
   and clear pending in the same JSON replacement.

Startup initializes/repairs the highest segment, seeds the recent-ID cache from the last
two files (maximum 10,000 IDs), and reconciles pending. If every pending ID appears after
`journal_start`, it advances the checkpoint and clears pending. Otherwise it retains the
intent for REST refetch. The renderer does not explicitly consume the pending range; it
uses channel checkpoints. A new page may overwrite a still-unresolved pending intent.

After recovery, the renderer drains buffered live IDs greater than the saved checkpoint.
IDs at or below it are discarded on the assumption that REST accounted for them.

| State | Purpose and authority |
|---|---|
| `channels[C].checkpoint_message_id` | Correctness-critical REST pagination/drain boundary; intended verified recovery progress, except first-watch exclusion baseline/fallback. Not the latest Gateway ID. |
| `checkpoint_journal_boundary` | Correctness-critical physical overlap scan floor. It says nothing intrinsically about Discord completeness. |
| `pending.channel_id`, `new_checkpoint_message_id`, `message_ids`, `journal_start` | Correctness-critical recovery transaction identity and evidence required to reconcile an interrupted append. |
| `pending.old_checkpoint_message_id` | Persisted but never read by reconciliation or renderer; redundant historical metadata in this implementation. |
| `last_recovery_at`, `last_result`, `last_error`, `recovered_count` | Diagnostics; not sufficient evidence of completeness or safe deletion. Reconciliation does not reconstruct the appended count. |
| `currentCheckpoint`, `pageNewestId` | In-memory pagination progress; not a durable recovery-session ceiling. |
| live queues, reconciled/recovering sets, recent-ID cache | Volatile coordination/dedupe; cannot authorize permanent deletion. |

## 2. Every durable journal reference

Inventory is grouped by persisted field and its actual consumer, including generated
copies. Searches covered collector, Core, tests, spike code, runtime scripts, and docs.
No production data files were inspected.

| Durable reference | Classification | Consumer / retention consequence |
|---|---|---|
| Exchange `core-ack.json`: `version`, `segment`, `offset` | CORRECTNESS-CRITICAL | Core ingestion resume and outbox promotion. Keep unconsumed bytes. Missing ack defaults to `(1,0)` in Core; GC must independently reject missing required authority. |
| Collector-private `recovery-state.json`: channel `checkpoint_journal_boundary.segment/offset` | CORRECTNESS-CRITICAL, currently unsound | Native overlap scan. A newer value does not currently prove older bytes unnecessary. |
| Same file: `pending.journal_start.segment/offset` | CORRECTNESS-CRITICAL | Native startup reconciliation scans forward from here. All referenced suffix files must remain available. |
| Legacy Discord-profile `cordbrief-recovery-state.json` | HISTORICAL, conditionally CORRECTNESS-CRITICAL | Imported if primary recovery state is missing. Includes the same boundaries/intents; cannot blindly treat an existing legacy fallback as dead. |
| Exchange `events/NNNNNNNNNNNNNNNN.ndjson` filenames | CORRECTNESS-CRITICAL | Writer derives numbering/active file from greatest existing filename; Core discovers segments and reads by cursor. |
| `collector-runtime-status.json.active_segment` (private, fallback exchange) | DIAGNOSTIC | Native telemetry read by supervisor; not writer ownership or closure authority. |
| `collector-status.json.active_segment` | DIAGNOSTIC | Supervisor/entrypoint telemetry consumed by Web/CLI. Startup can publish a placeholder `1`. |
| Digest artifact `cursor_start`, `cursor_end` | CORRECTNESS-CRITICAL for replay identity; HISTORICAL after commit | Compared with regenerated batch boundaries on interrupted transaction replay. Legacy citation reconstruction and migration still read their journal ranges. |
| Fully populated valid artifact `source_refs` | NO LONGER REQUIRES journal AFTER PHASE 3B | Guild/channel/message identity constructs citations directly; refs contain no segment/offset. Validate every cited source, not merely a nonempty map. |
| Delivery `target_cursor_end` | CORRECTNESS-CRITICAL comparison token | PREPARED promotion compares with Core ack. Does not read the target journal bytes. Uncommitted transaction inputs remain covered by Core's older ack. |
| Digest/delivery/scheduler `batch_id` / `digest_batch_id` / `last_batch_id` | HISTORICAL encoded dependency / artifact identity | Batch hash incorporates cursor bounds. No decoding into an independent journal reader. Do not delete cursor identity fields merely because citations are durable. |
| Scheduler slot/retry state | No physical journal reference | Uses shared Core cursor through digest execution; no independent segment pin. |
| Old `cordbrief-state.json.last_window_end` | HISTORICAL time-based API state | Not a journal byte position. |
| Migration report `batch_id`, counts, eligibility | No independent persisted journal boundary | Migration reads artifact cursor ranges; there is no global durable migration-complete certificate in the inspected implementation. |
| Runtime manifests, release/current paths, profile paths, environment exchange/data paths | Path configuration / runtime authority, not journal progress | Staged plugin copies determine executed implementation. ASAR `offset` fields are archive offsets, not journal offsets. |
| Tests, spike audit scripts, docs, CLI output/logs | HISTORICAL or DIAGNOSTIC | Fixtures and printed positions do not authorize production deletion. Error strings can also embed segment/path/offset text. |

`journal.Record` offsets, `Watermark` segment sizes, batch `start_cursor`/`end_cursor`/
`watermark`, and native `currentSegmentPath/Number/Size` are transient. They matter for
an in-flight reader/writer, but are not additional persisted consumer checkpoints.
Atomic replacement temporary files may contain copies of state; loaders read canonical
paths, not arbitrary leftover temp files. They do not independently authorize GC.

Phase 3B verification: `internal/delivery/service.go` reconstructs legacy sources only
when `SourceRefs` is empty; `internal/web/server.go` has the same guard. `SourceRef.JumpLink`
uses only IDs. `TestArtifactMigrationAndJournalIndependence` removes a disposable journal
and checks every migrated citation. It passed in the full Go suite. This is artifact-level
unit evidence plus source tracing, not production migration verification or a browser test.

## 3. Ancient-checkpoint / pinning answer

**The premise that healthy channels never run recovery is false for this renderer.**
`checkAndReportAuth` calls `runGapRecovery` on authenticated routes every five seconds;
a separate interval also calls it every sixty seconds. `isRecoveryRunning` prevents
concurrent runs. Neither timer tests for an actual gap, nor skips reconciled channels.

Consequently, healthy channels with new messages normally advance their REST checkpoint
and physical boundary on nonempty REST pages. Live events themselves advance neither.
No guaranteed cadence follows if REST stalls, errors, or cannot drain the backlog.

Under the literal hypothetical of six months of live appends **without any REST recovery**,
both values stay unchanged. The actual native writer was exercised across 55 files and
left both channel recovery records unchanged. The test compresses time; it does not run
six months of renderer timers. Future REST starts from the old ID, so it needs identities
of already-journaled messages after that ID, however old, to distinguish them from gaps.
Deleting segment 1 can destroy that distinction. Core consumption does not preserve this
per-message evidence.

A quiet channel can pin an ancient boundary even with successful polling: an empty REST
response breaks the renderer loop without saving a new boundary. Even the native empty-
page branch preserves the old floor. Other channels can rotate indefinitely around it.
The code does not release such a pin. The new harness proves the unchanged floor, not
eventual collectibility. Conversely, a moving floor is currently insufficient safety
evidence, as the counterexample below shows.

## 4. Unnecessary / duplicated state and logic

`pending.old_checkpoint_message_id` is write-only metadata. Recovery diagnostics are not
progress authority. Live and recovery paths duplicate rotation/cache maintenance and have
different fsync behavior. Two timers trigger the same REST workflow. Existing writer and
recovery suites copy implementation logic; their passing assertions do not exercise the
actual native module. None of these observations justifies broad cleanup in this phase;
no production fields or functions were deleted.

## 5. Old architecture problem

The physical floor conflates **completion of one REST page** with **absence of any live
overlap before the current journal end**. Snowflake order is not physical append order.

Reproduction: initialize C, append 101 live C records and eight other-channel records
through the real writer, producing 55 segments. Restart the native process. Recover the
first 100 C messages: all 100 dedupe successfully. This moves C's floor to the end of
segment 55, even though C message 101 was already appended in an earlier file. Recover
message 101: it is outside both the scan and the last-two-file cache and is appended again.
Both copies remain on disk. No GC occurred. Core's `BuildBatch` does not globally eliminate
such repeated IDs; it includes each input record.

Also reproduced: a nonexistent scan start returns `[]`; malformed recovery JSON returns
fresh `{version:1,channels:{},pending:null}`. Missing physical boundaries are filled with
the current end by `loadRecoveryState`, which itself can write state. None is a safe
read-only GC authority loader.

## 6. Chosen minimal correction

**No recovery correction selected or implemented**, honoring the explicit stop condition
on discovered duplication ambiguity. The bounded correction to the investigation is an
actual-code process harness and an explicit failing safety check.

Design constraints derived before choosing a model:

* Recovery requires a REST/exclusion lower bound, identities of previously appended
  records still eligible for refetch, and durable evidence for an interrupted page.
* A boundary established only when recovery begins misses older live overlap.
* A maximum Gateway snowflake cannot distinguish a skipped message below that maximum.
* A fixed-size ID cache cannot exactly represent arbitrarily many unverified live messages.
  A time window has the same problem without a proven limit on reordering/replay/gap age.
* Periodic REST verification already exists; adding another periodic verifier is not the
  first solution. Existing page floors must remain valid for later pages before periodic
  verification can justify releasing history.
* Separating a validated dedupe floor from verified progress is a candidate, but simply
  renaming the field or advancing it at each page/end/start does not establish the invariant.

For arbitrary six-month gaps, some representation of all unresolved overlap is required.
It need not contain transcript text, but it cannot be bounded merely by preference while
unverified history is unbounded. A correct verification/lifecycle rule could bound it;
that rule is the unresolved prerequisite, not an assumed implementation detail.

## 7. Final recovery state model

Unchanged M9 v1, with the distinction and defects described above. No migration, schema
upgrade, new high-water state, journal dedupe sidecar, or retention certificate was added.
There is no approved final GC-safe recovery model from this run.

## 8. Live MESSAGE_CREATE invariant

Source tracing: the renderer's live handler only calls `appendEventToJournal`; the native
append does not save recovery state. Actual-module testing deep-compares the complete
recovery state before/after 109 live appends and real rotation. It is unchanged.
Concurrent successful REST polling can advance the checkpoint; that is a different path.
This invariant holds, but does not rescue the invalid page-to-physical-floor mapping.

## 9. Real multi-segment results

`node collector/test/gc_readiness_test.mjs` uses Node's built-in TypeScript stripping,
removes only the Electron event-type import, and loads actual `native.ts`. Every child
uses isolated temporary paths and a 2,048-byte segment threshold. Startup and restart
are separate OS processes; journal numbering is never assigned manually. Only the torn
tail fixture directly appends synthetic incomplete bytes. No production volumes mount.

| Required case | Evidence / disposition |
|---|---|
| A | Native live-only path: 55 real files, unchanged checkpoint and physical floor. Renderer polling is a separate source-traced behavior. |
| B | Pending begins beyond 80% of the real segment's threshold; two recovered records rotate into N+1. Process exits with 73 before checkpoint rename. Fresh-process reconciliation leaves one copy of each and clears pending. |
| C | First 100-message REST page dedupes across real files after restart; later page duplicates. Requirement is **not satisfied overall**. |
| D | Two channels retain different floors; quiet channel stays at 1. No deletion eligibility is claimed for either. |
| E | No new end-to-end removal-during-REST test: handoff after stop. Source shows recovery retains its initial watchlist snapshot, and native recovered append does not reload/enforce membership. Safe cancellation is unproven. |
| F | Existing copied first-watch test passes; actual renderer first-watch-at-rotation proof not completed after stop. No claim that it is proven. |
| G | Short torn active tail repaired on fresh import; oldest closed segment remains byte-identical. Not exhaustive tail/power-loss proof. |
| H | Time-compressed live-only/quiet case demonstrates persistent ancient dependency, **not eventual collectibility**. Stock healthy traffic also invokes periodic REST. |

The existing recovery suite reports 23 records across two files. The new counterexample
tests an actual 100-record first page followed by another page, with older live overlap
outside the restart cache. No Discord network interaction was simulated as live proof.

## 10. Crash-boundary results

| Crash point | Durable survivors and restart behavior | Proof limitation |
|---|---|---|
| Before intent replacement | Old channel state; no recovery append yet. Refetch from old checkpoint. | Source-traced; existing copied save-failure test passes. |
| After intent persisted | Old checkpoint plus pending IDs/start; no appended page required yet. Startup asks for refetch unless all IDs appear. | Existing copied tests; native zero-prefix crash not added. |
| REST page received | Until native intent, page is memory only; old checkpoint causes refetch. | REST contents may change between requests; exact recovery is not universally proven. |
| Overlap scan complete | Intent survives; scan/cache are volatile. Restart rescans/refetches. | Missing history is silently accepted today; cannot claim fail-closed behavior. |
| Some missing records appended | Intent plus a prefix. Startup retains pending; future refetch should dedupe prefix. | Unresolved intent can be overwritten; full cross-channel/removal proof absent. |
| Journal fsync completed | Intent plus all newly appended records. For wholly new page, startup finds every ID and commits without append. | New real-process case B passes across rotation. A page partly deduped before `journal_start` need not qualify as all-present. |
| Checkpoint metadata save | Before rename: pending remains. After rename: new ID, new floor, pending null appear together at process-crash level. | New injection covers before rename. Floor can already be semantically wrong. |
| Physical boundary update | No separate durable step: field shares the checkpoint JSON replacement. | Atomic grouping does not prove its value correct. |
| Pending clear | No separate durable step: same replacement. Startup sees either intent or committed state for ordinary process crashes. | Power-loss durability unproven. |

Native `safeReplaceJSON` fsyncs the temporary file but does not fsync its parent directory.
New journal files likewise lack directory fsync. Live appends use `appendFileSync` without
explicit fsync. Thus no power-loss guarantee is inferred from process tests. Torn-tail
repair checks only the final 64 KiB and truncates to zero if no newline is found there;
the new short-tail case does not validate large torn records. These are source observations,
not newly executed power-loss or large-tail tests.

## 11. Exact future CanDelete(segment) predicate

The only proven machine-implementable decision for the current model is:

```text
CanDelete(S) = false
reason = recovery_safety_unproven
```

This is a stop result, not a useful retention implementation. The proposed conjunction
cannot yet be certified. A future nontrivial predicate must establish:

```text
AuthoritativeSnapshotValid
AND WriterExcludedOrSerialized
AND RegularClosedSegment(S)           // never active, never truncate/compact active
AND S < ValidCommittedCoreAck.segment
AND NoUnverifiedLiveOverlapIn(S)
AND NoPendingRecoveryNeeds(S)
AND NoLegacyArtifactOrOtherConsumerNeeds(S)
```

The strict Core segment comparison is a conservative sufficient Core rule, deliberately
retaining the ack's own segment even at EOF. It is insufficient for the other clauses.
Neither `S < checkpoint_journal_boundary.segment` nor `S < pending.journal_start.segment`
proves the whole conjunction. A fully migrated artifact needs no journal for citations;
an incomplete legacy artifact still does. Unknown/missing/malformed state must produce
retain, never be normalized using current recovery loaders. Status cannot establish closure.
Required path/offset validity, missing-file handling, recovery-floor semantics, legacy
consumer certification and snapshot concurrency remain prerequisites to any useful predicate.

## 12. Future GC owner

**Preferred future owner: setup maintenance under the existing runtime kernel flock**, with
the collector stopped/excluded. Its current maintenance branch runs without launching
Discord/Xpra and already shares exchange, collector-private state, and runtime storage.
This avoids racing native appends, rotation, startup repair, or pending commits.

Core currently lacks collector-private recovery state and the runtime lock volume.
Collector has writer authority and ack access, but lacks Core-private artifact migration
evidence. Setup also lacks Core-private artifact access today. Therefore no existing
component currently has *all* required proof automatically. Setup would need validated
artifact-independence evidence, e.g. read-only Core-data access during maintenance, plus
a defined consistent Core-state snapshot. This is a reasoned ownership preference with
explicit prerequisites, not a proven or implemented deletion owner. No new mount or API
was added; no new service is needed.

## 13. Read-only planner

Not implemented. A planner reporting eligible files from the current floors would encode
the defect as authority. The characterization test is disposable and is not a planner.

## 14. Production read-only findings

Not inspected. The requested ordering was to inspect production after all automated /
disposable checks pass. `--require-safe` fails and triggers the stop condition. Therefore
actual production segment count, active segment, Core cursor, recovery summary, pending
summary, runtime/source equivalence, and artifact migration status are **unobserved**.
No production Discord content, credentials, journal files, or state were read or changed.
Docker was used only for the disposable lock-test container, with repository source mounted
read-only and no production volumes or network.

## 15. Regression results

Environment: Windows, Node v24.19.0, Go go1.26.7 windows/amd64; Docker server 29.7.2.

| Command | Result / evidence class |
|---|---|
| `node collector/test/writer_test.mjs` | Exit 0, Windows; copied-model unit tests. |
| `node collector/test/recovery_test.mjs` | Exit 0, Windows; copied-model unit tests, 23 records / two files. |
| `node collector/test/retention_test.mjs` | Exit 0, Windows; runtime-release retention, **not journal retention**. |
| `node collector/test/operational_test.mjs` | Exit 0, Windows; disposable lifecycle tests. Mock Discord binary ENOENT messages and staging lock/pruning warning occurred; not real Discord startup proof. |
| `adversarial_lock_test.mjs` in `docker-cordbrief-setup:latest` | Exit 0, Linux container; cases A-E plus maintenance lifecycle passed, not Windows-skipped. Network disabled, only collector source mounted read-only. |
| `go test -count=1 -timeout=60s ./...` | Exit 0; all 14 listed packages passed on Windows. |
| `go vet ./...` | Exit 0, no output. |
| `go list -m all` | Exit 0, exactly `cordbrief`; no external Go modules. |
| `node collector/test/gc_readiness_test.mjs` | Exit 0; actual-native characterization and fresh-process restart on Windows. Defects reproduced. |
| `node collector/test/gc_readiness_test.mjs --require-safe` | **Exit 1**, duplicate-count assertion; required safety gate fails. |

The new native harness was not run inside Linux or Electron. No browser, production,
live REST, six-month soak, or physical power-loss result is claimed. No regression suite
was reported as passed merely because it skipped on Windows.

## 16. Files changed

* `collector/test/gc_readiness_test.mjs`: actual-native disposable rotation/restart harness,
  known-defect characterization and explicit failing safety mode.
* `docs/PHASE_3C_GC_READINESS.md`: this report.
* `GATES.md`: replaces the prior phase's ledger with this phase's evidence and handoffs.
  Prior ledger remains in the unchanged canonical commit.

## 17. Diff summary

Two new files and one updated ledger. Zero production implementation changes, zero new
dependencies, zero schema changes, zero pruning commands. The harness creates and cleans
only its own temporary fixtures; production journal segments are never deleted.

## 18. Unresolved risks / required handoff

1. Correct the page-progress / physical-floor invariant before treating any floor as a
   retention certificate; retain the counterexample as a failing safety check until fixed.
2. Define fail-closed state validation and reconciliation for missing/corrupt state/history.
3. Prove watchlist removal, first-watch-at-rotation, partial/refetched pending pages, and
   eventual ancient-segment collectibility with the actual renderer/native path.
4. Complete crash and power-loss durability review; directory sync and live append behavior
   are materially different from Core's durable state helper.
5. Establish authoritative artifact migration evidence, active-writer exclusion, and a
   consistent snapshot before deriving a useful deletion predicate.
6. Production observation remains a handoff after the safety prerequisite passes.

Gate disposition: G1/G3/G4/G6 met as characterization/reporting evidence; G2 and G5 handed
off under the user's stop condition. This is a blocker report, not completed GC readiness.

## Phase 3C.2 implementation addendum (supersedes the blocker disposition above)

The earlier sections document the original investigation; they are not the final behavior
of the uncommitted collector code. Phase 3C.2 repairs local recovery deduplication, not GC.

- Recovery state is now version 2. `checkpoint_message_id` is the REST/exclusion lower
  bound. `checkpoint_source` records `baseline`, `rest`, or `legacy` provenance. A REST
  page does not undo earlier baseline exclusions; the field records its latest origin.
- `checkpoint_journal_boundary` is a conservative physical floor: every already-local
  channel message above the checkpoint must start at or after it. Recalculation chooses
  the earliest physical qualifying record, or the captured journal end if none exists.
  Live appends preserve the floor, so it need not remain minimal between recalculations.
- One synchronous native scanner validates record boundaries, identities, duplicate IDs,
  retained segment continuity, and the captured end. The scan/save body has no await, so
  native appends cannot interleave. A later append is at/after the captured end.
- Nonempty page commit and pending reconciliation derive the floor locally. Empty REST
  only triggers that same calculation and cannot advance the checkpoint. Recovery does
  not rely on the recent-ID cache. Physical order, not minimum snowflake, picks the floor.
- v1 primary and legacy-path import validate retained history from segment 1, preserve
  checkpoint IDs, reset channel floors and pending scan starts to (1,0), mark provenance
  legacy, and save v2. Missing prefix/interior history refuses migration. There has been
  no GC, so an unexplained missing prefix is not treated as a legitimate retained suffix.
- Corrupt/future state is refused, not rewritten as fresh. Missing state with journal
  files is refused. Existing error/status updates cannot move checkpoints or floors.
- An unresolved pending page accepts only the same channel, checkpoint and ordered IDs,
  retaining its original start; differing pages leave it intact. Already-complete pending
  pages can reconcile first. Refetch changes caused by remote deletion may require operator
  review: this implementation deliberately blocks rather than discards pending evidence.
- First-watch successful visible history establishes an exclusion baseline at the latest
  returned ID. Successful no-visible-history establishes an explicitly labelled baseline
  cutoff captured before the request; it does not claim Discord completeness. Failed or
  unusable REST leaves initialization retryable. The timestamp fallback still depends on
  local clock accuracy; no server-clock equivalence is claimed.
- Renderer cap remains ten requests/run. A cap does not finalize the floor. Subsequent
  runs continue from committed progress with local overlap intact.

Measured actual-code fixtures on Windows Node 24.19.0 and Linux Node 22.23.2:

| Fixture | Result |
|---|---|
| Original 101 messages | One copy of every channel ID; 109 records / 55 segments |
| 250 messages, interleaved other channel, three pages with fresh processes | One copy each; 272 records / 136 segments; exact floor checked after every page |
| Permission-empty trigger with local 101 above checkpoint 100 | Actual renderer empty path preserves record 101's physical start; later recovery adds no duplicate |
| Quiet channel | Floor releases to segment 4 offset 1864; checkpoint unchanged; REST-error control retains floor |
| Actual renderer cap and continuation | 1050 channel IDs once; 1119 records / 560 segments; first run commits 1000 and retains record 1001 |
| Partial pending process death across rotation | Same page resumes; different/sibling page refuses and leaves state bytes unchanged |
| All page writes, crash before checkpoint rename | Fresh process reconciles exactly once |
| Before/mid buffered-live drain process death | Fresh-process REST fixture recovers both IDs exactly once |
| v1 unsafe floor and legacy import | Reset, checkpoint preserved, idempotent; subsequent 101 deduped |
| Corrupt/future state and malformed/missing journal | Progress refused; corruption not normalized |
| Historical duplicate fixture | Explicit error, no silent normalization |
| Reordered physical messages | Floor follows physical start, not snowflake minimum |

Both harness modes pass. Actual renderer execution uses stripped repository source with
fake REST/IPC surroundings; native writer/recovery logic is not copied. OS process exits
exercise process-crash boundaries, not power loss or real Electron scheduling. Native
synchronous ordering is source-proven; recovery of never-drained messages is conditional
on those messages remaining available through subsequent REST. Remote deletion before
capture cannot be repaired by this floor invariant.

Existing writer/recovery suites remain copied-model tests. The new harness is the actual-
code evidence. No production state was read, migrated, or deployed. No GC or planner was
added. Existing directory-fsync/live-fsync limitations, large torn-tail repair behavior,
watchlist-change lifecycle, and global pending availability limitations remain outside
this correction. Journal GC still requires a separate consumer/ownership proof.

Final regression evidence: Windows writer/recovery/retention/operational suites all exit 0;
Linux adversarial lock and maintenance suites exit 0 without skips. Windows Go test passes
all 14 packages, vet exits 0, and module listing is exactly `cordbrief`. Operational mock
Discord ENOENT messages and the Windows staging-pruning warning are not live runtime proof.
All four Phase 3C.2 gates are met; production deployment and GC are not acceptance claims.
