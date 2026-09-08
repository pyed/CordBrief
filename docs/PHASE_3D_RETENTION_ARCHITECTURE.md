# Phase 3D retention decision

Final milestone: **`m13-journal-retention` — implementation complete, disposable
proofs passed, production deletion not exercised.** The read-only production
preflight correctly refused mutation: only active segment 1 existed and Core had
not consumed its remainder. See [production preflight](PHASE_3D_PRODUCTION_PREFLIGHT.md).
No Phase 3D deployment or production deletion is claimed by this milestone.

## Certified physical deletion implemented (not deployed)

The maintenance command now accepts `<through> --delete-certified`. Default
invocation still only publishes evidence. Deletion requires an existing manifest
whose retired prefix equals `through`; it cannot publish new coverage and delete
in one invocation. Both leases and the full native recovery/evidence validation,
Core ack and artifact checks run again. Pending, torn tails, missing/corrupt
evidence, changed raw files, or an unsafe target refuse before unlink.

Before unlink, maintenance fsyncs every referenced sidecar, its directory, the
manifest and exchange directory. This covers a publisher killed after manifest
rename but before directory durability. It also syncs events before resuming an
interrupted collection. Each next regular raw segment is rechecked against its
certified digest, unlinked in ascending order, and followed by events-directory
fsync **before** touching its successor. Errors stop the operation. The highest
raw segment remains protected. Manifest, sidecars, recovery state and Core ack
are never rewritten by deletion. Repeating completed GC deletes zero files.

Linux disposable process tests inject death before/after each unlink and after
its directory fsync, and inject a directory-fsync error at every deleted segment.
They validate and resume through actual native startup, then replay retired IDs.
Invalid-state refusals preserve file bytes. These process tests and explicit
fsync ordering support crash safety; physical power-cut/storage-device behavior
has not been tested. Production deployment is still absent. The foundation-only
status below is historical and superseded by this section.

Executed deletion gate: 7 real writer segments, 6 removable closed segments,
24 injected unlink/sync deaths or failures, 15 byte-preserving invalid-state/lock
refusals. Every resumed run completed with active bytes unchanged and retired
REST replay appending zero. Actual Core then read the remaining event and
committed its exact end cursor (`coreVerified=true`). Publication-only and
129-segment identity-reader gates also remain green. Test containers had only
read-only source/binary mounts and disposable test data, no production volumes.

## Implemented foundation (supersedes future-tense implementation status below)

Native and Core now read the specified version-1 manifest and identity sidecars.
Native identity traversal preserves original positions for recovery witnesses,
floors, historical replay, pending reconciliation, and live dedupe. Duplicate JSON
keys, incomplete positions, bad hashes, conflicting identities, and unexplained
topology fail closed. When raw files remain, their identities and boundaries are
compared exactly against the sidecar. Core excludes the logical retired prefix
and rejects a cursor into it even before physical files disappear.

`collector/retention-publish.sh` acquires/verifies runtime FD9 and Core FD8 under
Linux flock, then runs `retention-publish.mjs`. Native maintenance preflight is
read-only: no recovery migration, tail repair, reconciliation, or status writes.
Publication checks Core ack and artifact citations, projects exact sidecars,
fsyncs/readbacks them, then atomically publishes/fsyncs the manifest. Orphan reuse
fsyncs the file and directory again. Prefix extension is monotonic. No journal
unlink/truncate operation exists. The opt-in Compose retention override mounts
Core data into setup; ordinary setup retains its original private-volume boundary.

Standalone Core journal previews, ingest, and migration dry runs now hold the Core
lease too. Artifacts with no cited IDs bypass legacy transcript reconstruction.
Live dedupe retains channel ownership in its existing bounded recent cache and
checks ownership on historical misses. First-watch and REST progress semantics
are unchanged. Sidecars retain positions, so recovery-state schema stays v2.

Proofs: actual native reader tests on Windows/Linux use 129 real segments, 127
absent retired segments, three-page replay of 250 accepted IDs, genuinely unseen
old IDs, cleared cache, new/re-added/removed watches, pending process death, and
12 corruption refusals preserving recovery bytes. Linux publisher tests use 55
segments/54 sidecars/108 identities, three publication crash boundaries, three
lock refusals, inherited setup lease reuse, incremental publication, orphan reuse,
and republishing a prefix-omitting copy. Go tests cover Core suffix reads, rejected
old cursors, malformed evidence, citation independence, and real CLI lock contention.
An additional Linux publisher run used the actual cross-compiled Core CLI against
its prefix-omitting output: `coreVerified=true`, one retained event read successfully.

These are disposable process/container tests, not power-loss or production proofs.
Physical unlink plus directory-fsync crash tests remain for the next step. Reader
validation remains linear in historical evidence (with repeated scans); exact
duplicate detection uses memory proportional to history as the existing native
scan did. The on-disk ledger grows with accepted history. No total-storage or
large-history performance bound is claimed. No production image was built/deployed.

The remaining text records the architecture and deletion requirements; its former
statements that readers/sidecars were absent describe the preceding review only.

Baseline: `14d002fd6a8f168bd27abff1b37dd02cc1514100` (`m13-recovery-v2`).
This is an implementation specification, not permission to delete current data.
No existing runtime execution path, production state, deployment, or journal was
changed by this review. The new Go assessment is an unconnected read-only helper.

## Decision

Retire a contiguous, Core-consumed prefix of transcript segments only after
durably replacing its **complete identity and position evidence**. Keep Recovery
v2's watch boundary and historical replay contract. Use existing setup maintenance
offline, with both services excluded. Do not introduce a service or API.

This bounds eligible raw transcript storage, not total storage: exact identity
evidence grows with accepted messages. Unconsumed data also cannot be forcibly
bounded while preserving recoverability; disk pressure must stop ingestion rather
than discard it. Age/byte targets select a desired prefix, never override safety.
Retention does not reduce the cost of REST sweeps from `watch_after`.
Use a retained-transcript byte target to choose Q within the safe prefix; if that
target cannot be met, report the excess and retain it. Offline maintenance only
reclaims at explicit maintenance runs, so this is not an automatic hard disk cap.

## Why Opus's predicate fails

`checkpoint_message_id` is high-water, not a completeness certificate. A physical
floor covers local IDs above high-water; it says nothing about replay below it.
`appendRecoveredMessages` selects `(1,0)` for historical pages. Pending
reconciliation, live cache misses, startup dedupe, and state witness validation
also depend on old identities. A successful empty REST response is not permanent
remote completeness. Advancing floors cannot release that evidence.

The actual-native regression `collector/test/gc_replay_evidence_test.mjs` seeds
109 records across 55 real writer segments. Both channels' forward floors advance
beyond segment 1; pending is null. Even with Core beyond 1, migrated artifacts,
and an accurate active segment, the proposed predicate would allow segment 1.
Intact historical replay appends zero. A disposable copy omitting segment 1 is
rejected by current native code. A second copy with an empty segment 1 (explicitly
an unsupported evidence-loss simulation) passes sufficiently far to append the
already-ingested ID again. The original evidence remains untouched. An unseen
ID below high-water is correctly appended, disproving a high-water exclusion fix.
This is not a claim that current unmodified startup silently accepts GC.

The proposal correctly kept deletion absent and recognized Core, active-file,
pending, and artifact dependencies. Its snapshot was not authoritative: private
recovery state was optional, only a few fields were parsed, malformed pending
could disappear, and diagnostic status supplied the active boundary. Tests
mostly restated those assumptions. Core cannot mount recovery state today;
Core's commit lock does not lock Collector. No current service owns all required
state and exclusions.

The artifact helper's nonempty-SourceRefs test was also insufficient: a nonempty
map can omit or contain invalid cited refs. Existing `MigrateArtifacts` already
counts resolvable cited refs. Fully grounded Phase 3B artifacts need no old bytes
for citations, but Web and delivery retain a legacy fallback when refs are empty.
Zero-citation artifacts need no citation bytes semantically; that fallback must
be bypassed for them before retirement. Artifact cursors and delivery target
cursors are historical/commit identities, not independent transcript consumers.

## Minimal replacement evidence

Use immutable per-segment identity sidecars plus one atomically published
retention manifest, stored in exchange. Setup writes them under exclusion;
Collector and Core read them. Keep collector-private recovery state private.

Each sidecar contains only the source segment number, original byte length,
source digest, record count, and an ordered row for **every** original record:
`message_id, channel_id, offset, next_offset`. No text, author, attachments, or
timestamps. Positions allow existing recovery floors and witness validation to
retain their precise meaning without rewriting recovery state. This is an exact
identity ledger, initially sequentially scanned like today's journal; no database,
probabilistic filter, or mandatory in-memory index is needed. Serialization must
preserve snowflakes losslessly. Binary layout is an implementation choice.

The manifest has an explicit format version, `retired_through`, and exactly one
entry per segment 1..retired_through naming its immutable sidecar and digest.
Paths must be canonical filenames, never arbitrary paths. Sidecar metadata and
rows must validate: nonzero IDs, matching segment, offsets starting at zero,
contiguous positive record lengths ending at original byte length, exact count,
and no duplicate IDs or conflicting channel ownership across the logical journal.
At construction compare every identity/position against fully validated raw bytes;
a checksum alone cannot prove completeness. Preserve all earlier entries on every
manifest update. Corrupt/missing referenced evidence blocks startup and GC.

Collector's identity traversal must read sidecars for retired segments and NDJSON
for the suffix, exactly once per logical record even while both files exist.
Every correctness consumer must use this traversal: startup ledger/max/count,
cache-miss membership, REST overlap, pending completion, floor derivation, and
all v2 cross-field/witness/boundary validation. Offset validation for retired
boundaries uses certified row boundaries. Do not rebase floors to current end.
An ID belonging to a different channel is corruption, not a successful dedupe.
Full transcript readers must never receive synthetic messages from sidecars.
Factor the existing native validator into a read-only path for maintenance; do
not reproduce a partial v2 parser in Go or invoke startup reconciliation to plan.

This evidence cannot be discarded on watch removal. Returning an old accepted
message and returning an unseen delayed message must remain distinguishable.
A finite cache, snowflake maximum, time window, or Bloom filter cannot represent
an arbitrary growing set exactly: discarding distinctions creates duplicates or
false exclusions. Strictly bounded total storage would require an explicit weaker
replay contract; this design does not silently impose one.

## Exact future deletion predicate and publication protocol

Let R be the last physically removed segment, P the manifest's retired prefix,
and H the highest existing segment, conservatively protected as active. Native
can reserve an empty successor; retaining H anyway is safe. No status-file field
is authority. Let A be a strictly parsed, existing committed Core ack.

Before extending P to Q, require all of:

1. Exclusive runtime lease and Core lease held; all journal readers participate
   in the exclusion protocol below. Both services stopped. Correct storage roots
   and retention-capable reader versions verified.
2. Complete recovery v2 validation against the logical journal succeeds without
   repair or mutation; pending is null. An active saved replay sweep may remain.
3. Manifest, sidecars, raw suffix, and topology validate; no closed torn records,
   symlinks, unknown journal formats, arbitrary gaps, or mismatched leftovers.
   Active-tail repair is Collector's responsibility, never maintenance GC's.
4. A is a valid record boundary in the retained suffix; P <= Q < A.segment and
   Q < H. Missing/invalid ack is not a fresh cursor. Required Core state validates.
5. Every artifact citation is independently resolvable from its SourceRef, or
   there are no citations and consumers bypass legacy reconstruction. Unknown
   artifact files/schema, incomplete refs, or another consumer needing the prefix
   block retirement. No artifact migration is silently performed by GC.
6. For all newly retired segments P+1..Q, validated complete sidecars are written,
   fsynced, read back and compared to raw records, with directory durability.

Then publish a new manifest with P=Q by durable atomic replace and directory
fsync. This is the logical retirement commit; recovery metadata need not change.
Unknown state never grants deletion. A dry-run snapshot cannot authorize a later
unlink without fresh exclusion and validation.

Under those held exclusions, the machine predicate for the next unlink is:

```
CanDelete(S) = ValidAuthoritativeState
            && RetentionCapableConsumersExcluded
            && PendingIsNull
            && AllArtifactsIndependent
            && S == R + 1
            && S <= P
            && S < A.segment
            && S < H
            && RegularClosedRawFile(S)
            && RawFileMatchesCommittedSidecar(S)
```

`ValidAuthoritativeState` means the exact checks above, including certified
coverage of every missing segment and full cross-field recovery validation.
R is derived from the contiguous physical suffix, never persisted independently.
Delete in ascending order only; fsync the events directory. Never truncate,
compact, or rename the active journal. Retention policy can choose not to delete
an eligible segment. **Current Recovery v2: CanDelete is false for every segment.**

Crash ordering: before manifest commit, raw remains authoritative; incomplete or
unreferenced staged sidecars never authorize removal. After commit but before
unlink, sidecars are authoritative for retired identities and raw leftovers are
ignored for identity counting. A partial ascending unlink leaves a valid physical
suffix; restart validates the manifest and resumes conservatively. An uncertain
manifest save means no unlink. Unexpected leftover bytes or missing sidecars fail
closed. Backups/restores must preserve the manifest, indices, recovery state and
Core state together; restoring an ack into the retired prefix is rejected. Old
runtime rollback after retirement is unsupported without the matching full backup.

## Ownership and synchronization

First implementation belongs to **setup maintenance**, offline. It already holds
`runtime.lock` before maintenance commands and has exchange/collector-private
volumes. Add an explicit Core-data mount and acquisition of its existing
`commit.lock` for maintenance. Lock order: runtime, then Core; failure releases
and exits without writes. Do not expose those volumes to the normal Core service,
add a Docker socket, or start Discord in maintenance mode.

This is not sufficient in today's code: read-only Core previews and migration
dry-runs deliberately bypass commit.lock. All CLI journal readers must honor the
maintenance exclusion too (the minimal first implementation can take the same
exclusive Core lock). The serving process already owns its Core lock; internal
readers use that ownership. Collector/native activity is excluded by runtime.lock.
Test real contention, including a reader opened before maintenance; don't rely
on a snapshot of Docker status. Unsupported external raw-filesystem readers are
outside the service protocol. No online periodic deletion is proposed here.

## Topology, watches, and replay

Logical segments remain 1..H: sidecars 1..P, raw P+1..H. Physical raw files must
be a contiguous suffix R+1..H with 0 <= R <= P. Certified but not-yet-deleted raw
files R+1..P are legitimate crash leftovers; arbitrary holes are never accepted.
Without a manifest, only the pre-GC topology contiguous from 1 is accepted.
Core must reject cursors in a retired prefix and unexplained holes, rather than
merely iterating discovered filenames. Historical identity positions remain
meaningful; archived transcript preview is explicitly unavailable.

New watches retain the current same-millisecond anchor semantics and uncertain
initialization fallback (`watch_after=0`). Old retired IDs dedupe globally; unseen
eligible old IDs still append into the current segment. Re-added channels retain
their existing immutable watch boundary and replay progress, as today. Removing
a channel does not erase its recovery metadata or ledger evidence. Restart/capped
sweeps still scan below high-water and must consult the ledger. A saved sweep,
legacy high-water, zero watch boundary, or cleared cache cannot bypass membership.

## Proof gates for implementation

Before enabling any unlink, implement and test identity-sidecar reading with
raw files still present. Compare all actual-native decisions/state against an
intact raw-only reference, including randomly varied identities/positions.
Then use disposable prefix-omitting copies to prove:

- The 55-segment regression: replay of retired accepted ID appends zero; unseen
  below-high-water ID appends once. Repeat with cache cleared, restart, three
  pages, page cap, concurrent Gateway overlap, same-millisecond IDs, two channels.
- New/re-added/removed watches and v1 migration with old clock-derived K preserve
  semantics. Pending refetch after rotation/process death uses union evidence;
  maintenance refuses unresolved pending without changing it.
- Missing/altered sidecars, false coverage, duplicate IDs, channel mismatch, wrong
  offset/size/hash, missing manifest, arbitrary gaps, malformed ack/artifacts,
  and impossible recovery relationships all fail before mutation, byte-for-byte.
- Process death at sidecar write/fsync, manifest rename/fsync, each unlink/fsync,
  and restart gives either intact raw authority or complete certified authority.
- Actual Linux contention excludes Collector, Core, and standalone read-only
  readers. Core resumes at the same ack, citations render without raw history,
  old cursor restore fails, highest active bytes never change.

These future sidecar and deletion gates are not yet run: there is no sidecar
implementation. The model is ready to implement in that order, not ready to
delete production data. The present Go read-only assessment reports every
discovered segment ineligible; it claims neither complete inventory nor topology
validation. Removed the unsafe positive predicate, partial recovery-state parser,
diagnostic certificate scaffold, and duplicate artifact-status helpers. No
runtime retention behavior was added.

## Executed verification

- Windows and Linux-container actual-native evidence regression: passed; 55
  segments, 109 original records, intact replay 0 appends, missing prefix refused,
  stripped-evidence replay 1 append, genuinely unseen below-high-water ID 1 append.
- Windows `gc_readiness_test.mjs --require-safe`: passed, including original 101
  IDs, three-page 250 IDs, 1050-ID capped continuation, process death, pending
  identity refusal, migration, corruption, and first-watch cases.
- `go test ./internal/journal ./internal/digest`: passed. The read-only assessment
  refuses eligibility even for malformed contents and gaps and preserves bytes.

These are disposable tests, not production observations. No deletion, sidecar
crash protocol, or future maintenance synchronization is claimed tested.
