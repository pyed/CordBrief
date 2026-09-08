# Gates: Phase 3D implementation complete

Annotated milestone `m13-journal-retention` checkpoints completed retention/GC
implementation and disposable proofs. Production deletion is explicitly deferred:
the read-only preflight correctly refused mutation because no eligible closed
segment existed. This milestone does not certify deployment or production GC.
The historical Phase 3C and staged Phase 3D evidence follows.

## Current status: production recovery v2 deployed

User authorization expanded to deployment and a local commit/annotated milestone.
Earlier sections below are historical gate evidence, not current prohibitions or
production status. See docs/PHASE_3C_DEPLOYMENT.md for deployment, backup, and
rollback details. No remote push or journal GC is authorized or performed.

- [x] D1: Consistent rollback archives and retained previous runtime/images.
  EVIDENCE: six stopped-source volume archives compared with originals, restored
  into an isolated volume, and compared again. Previous release 20260906013759
  remains intact; backup volume cordbrief_m13_3c_rollback_20260908 retained.
- [x] D2: Correct-layout rehearsal and actual production v1-to-v2 migration.
  EVIDENCE: real native loader; 2 channels; checkpoints preserved; pending null;
  original journal byte-identical; second load byte-identical. Prior rehearsal
  path mistake corrected before deployment. New release 20260908001305.
- [x] D3: Real replay, restart, and Core continuity.
  EVIDENCE: 21 to 62 unique records, original prefix intact; fresh REST sweeps
  after collector restart kept identical journal hash. Both services healthy;
  collector FD 9 flock verified; Core dry-run reads 41 new records. Existing
  artifacts/deliveries/scheduler unchanged; inbox and 3 digest pages HTTP 200.
- [x] D4: Final validation on deployed source.
  EVIDENCE: Linux pending-bounds, state-invariants, first-watch safe,
  migration/replay, GC-readiness safe, retention, adversarial lock/maintenance
  all exited 0 using new setup image. Go tests (14 packages), vet, module list
  passed; module exactly cordbrief. Runtime production hashes unchanged from
  the fully tested Windows/Linux development tree; no implementation patch
  during deployment. git diff --check passed before checkpoint.

## Current fixture repair acceptance

- [x] F1: Linux retention uses a real inherited FD 9 flock, asserts kernel evidence and production validation, and passes all applicable cases; teardown releases the lock.
  CHECK: docker run --rm --pull never --network none --mount "type=bind,source=C:\Users\Sheriff\Desktop\src\CordBrief\collector,target=/collector,readonly" --entrypoint node docker-cordbrief-setup:latest /collector/test/retention_test.mjs
  EXPECT: ALL RETENTION & ROLLBACK TESTS PASSED
- [x] F2: Linux adversarial locking/maintenance and actual-code recovery remain green; final Windows collector and Go regression bar pass.
  CHECK: run the explicitly requested isolated Linux and Windows commands recorded below.
  EXPECT: every required process exits zero; module output exactly cordbrief.
- [x] F3: Production runtime/recovery bytes unchanged this turn; no bypass, dependency, deployment, migration, GC, commit/tag/push; git diff --check passes.
  CHECK: compare production file SHA256 before/after and run git diff --check.
  EXPECT: identical production hashes and exit zero.

OWNS: collector/plugin/native.ts, collector/test/gc_readiness_test.mjs, collector/test/pending_bounds_test.mjs, collector/test/recovery_state_invariants_test.mjs, docs/RECOVERY_CONTRACT.md, GATES.md

No deployment, production mutation, journal GC, commit, tag, or push.
VERDICT: READY FOR DEPLOYMENT REVIEW. Recovery gates pass on Windows and Linux;
the stale retention fixture now uses real Linux kernel locks and passes.
No accepted safety test remains red. No deployment is authorized or performed.

- [x] G1: Impossible pending/sweep relationships preserve all durable evidence.
  CHECK: node collector/test/pending_bounds_test.mjs
  EXPECT: state changed=false; pending cleared=false; K advanced=false
  EVIDENCE: exit 0; match yes; SHA256 d57d20e3a295eb4082ecd710299f7e1ab116d9df74158908a522e5992ba64082.
- [x] G2: Sibling contradictions reject before startup, repair, or replacement.
  CHECK: node collector/test/recovery_state_invariants_test.mjs
  EXPECT: STATE INVARIANTS SAFE
  EVIDENCE: exit 0; match yes; SHA256 8a39a52c7f5227cff44b6eafdd31d9abcba1ae5ec80492e3d69c0d5eef53aca3. 20 impossible persisted states; whole-tree file hashes unchanged on startup and recovery; invalid proposed saves (including undefined fields), invalid legacy import, and 3 valid controls pass.
- [x] G3: Original recovery/rotation/crash cases remain exactly once.
  CHECK: node collector/test/gc_readiness_test.mjs --require-safe
  EXPECT: GC RECOVERY SAFETY VERIFIED
  EVIDENCE: exit 0; match yes; SHA256 824d8d6c6f8c5b65460f8f2974633a4fac7d7b6d3465eb744a7e9fb61eae56cb. Original 101, 250, 1050 IDs exactly once; real rotation and process restarts.
- [x] G4: Linux actual-code recovery and adversarial locking/maintenance pass.
  EVIDENCE: 2026-09-08 Docker Linux server 29.7.2. Pending-bounds ran first and exited 0. Actual-code first-watch (default/safe), recovery-contract (default), gc-readiness (default/safe), migration/replay, and recovery-state-invariants all exited 0. Adversarial lock cases A-E and maintenance lifecycle cases A-D passed without skips. Linux writer, recovery, and operational commands exited 0; operational staging retention reported its mock-lock skip, which is not counted as retention coverage.
- [x] G6: No known red safety test.
  RESOLVED: real-lock fixture passes all 19 applicable Linux cases; original failure evidence below is historical.
  EVIDENCE: Additional Linux retention_test.mjs invocation exited 1 at Test A: FD 9 points to anon_inode:[eventpoll], not the fixture runtime.lock. setupMockLock only writes metadata and returns {role, acquired:true}; it does not acquire flock. This fixture is unchanged from canonical HEAD. The Linux runtime correctly refuses it. No bypass, test weakening, or implementation patch was applied.
- [x] G5: Full Windows collector and Go regression bar passes.
  EVIDENCE: all 15 commands exited 0: pending_bounds; recovery_state_invariants; migration_replay; first_watch_race (default/safe); recovery_contract (safe); gc_readiness (default/safe); writer; recovery; retention; operational; go test -count=1 -timeout=60s ./...; go vet ./...; go list -m all. Module output exactly cordbrief. Final undefined-value guards were then verified by rerunning both focused corruption suites.

Execution: Windows PowerShell, Python subprocess argument arrays, cwd C:/Users/Sheriff/Desktop/src/CordBrief. Hashes are SHA256 of combined captured stdout+stderr (UTF-8 with normalized newlines). Go test: 0bf66a10d906be146eb41c900bb3e4ec32a07de36a57d81fa837648001535481; vet: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855; modules: 3d84841b65f62733068210327055c176fda7404ffd86c9daf0c1e4e1b4678dd8.

Historical Linux blocker: Docker endpoints were unavailable during the preceding review. The engine is now available and the current-code results above supersede that missing evidence.

Operational mock Discord ENOENT and the Windows staging-prune warning are expected characterization output, not live proof. No production reads/writes, deployment, GC, commit, tag, or push. HEAD and peeled canonical tag remain 27196c587202c66c7972b6ad04cf424927b7050f.

## Final Linux verification (2026-09-08)

The preceding no-production-reads statement describes the prior review. This turn read Docker metadata only: both production containers were already healthy, started at 2026-09-07T23:17:45Z, with unless-stopped restart policies. This is consistent with automatic startup; the initiating event was not independently observed. Neither container was stopped, restarted, rebuilt, or entered. No production volumes or persisted recovery state were accessed.

Linux commands used docker-cordbrief-setup:latest with --rm, --pull never, --network none, read-only /collector source, no production mounts, no host ports, and no supplied credentials. Operational launch fixtures encountered blocked DNS/display errors inside the disposable container; these are not authenticated Discord or production recovery evidence.

After the Linux focused recovery gates passed, all 12 final Windows collector invocations exited 0: writer, recovery, gc_readiness default/safe, first_watch_race default/safe, recovery_contract default, pending_bounds, migration_replay, recovery_state_invariants, retention, operational. Go tests passed in all 14 packages, go vet exited 0, and go list -m all printed exactly cordbrief. git diff --check exited 0. Operational staging-prune skips on both platforms are explicitly excluded from passed retention evidence.

This turn changed only GATES.md. Native SHA256 remained 0294F28A3A7F6DC21970F8042D5223F3BECC73FD7E32044DBE244B5F40788A72; renderer SHA256 remained 8CD61ACE18C0B7C9068C2D02E73C8D7731C601006447B4AED0BA26D9C212CA57. No dependency or implementation changes; no test bypass added; removed renderer buffers remain removed. The deliberately rejected unconditional-recovery negative control is not the accepted contract gate. All accepted recovery counterexamples are green; the extra Linux retention test remains failed, not skipped or relabelled passed.


## Retention fixture repair closure (2026-09-08)

This closure supersedes the preceding historical retention failure. F1-F3: 3 met,
0 unmet, 0 abandoned. This turn changed only collector/test/retention_test.mjs
and GATES.md. Production lock validation, runtime entrypoints, and recovery code
were not modified. Runtime SHA256 remains
c77f9773729c6106e8f1e7c17eeb317b17974549d1fceadf6bb03a8ff5e53faf;
native/renderer hashes remain the values recorded above.

F1 evidence: the exact formerly failing Linux Docker invocation exits 0, matching
ALL RETENTION & ROLLBACK TESTS PASSED. Each case inherits FD 9 through Bash exec
and flock -n 9. Before execution, assertions check its real path, FLOCK WRITE
record, production validation, and rejection of an independent flock attempt.
Production validation runs again after the case; the parent verifies that a new
lock can be acquired after child exit, then removes its own disposable directory.
All 19 Linux cases pass (A-R plus lock enforcement); Windows-only S is not counted
as a Linux pass. Windows preserves the existing non-Linux mock context.
Linux retention output SHA256: 69dc10c05ee2c58a4e5c9337357b5d42c75051e1a47b6b706bd15e161e9af64d.
Windows retention output SHA256: 9c0d3c464b93a85a1cc84d38445f2fdbf2b9fc1edfc2b7591061231b4decb265.

F2 evidence: final Linux run has 13 zero-exit invocations, Windows has 12:
pending_bounds; first_watch_race default/safe; recovery_contract default;
gc_readiness default/safe; migration_replay; recovery_state_invariants; writer;
recovery; retention; operational; plus Linux adversarial_lock. Safety success
markers match. Original 101/250/1050 cases remain exactly once. Recovery output
hashes match the preceding recorded hashes. Linux lock/maintenance output SHA256:
185538ccd51016ac6fa1116f848f6fc92d32d1fe51af533f132c95365653fe8a.
Linux operational SHA256: 41c1de511be81cf7f6caeb8766323c6b096448a8a0aeaae452acb6f9fb1a1e9c.
Windows operational SHA256: 71bbf7714ef451843744c90cab8c2319ec0a720e54e1303bccd25892f9bc4817.
Operational staging-prune skips are not counted as passes; dedicated retention
coverage supplies the required evidence. No authenticated service was tested.

Go test -count=1 -timeout=60s ./... passes all 14 packages; output SHA256:
7ec508b3c49454d287075011833721f35d243934093bf91b0bd0675fbe64ad88.
Go vet exits 0 (empty output); go list -m all exits 0 and is asserted exactly
cordbrief. Their fingerprints match the previous evidence.

Execution: PowerShell in C:/Users/Sheriff/Desktop/src/CordBrief, Python subprocess
argument arrays; SHA256 covers UTF-8 combined output with normalized newlines.
Linux uses existing docker-cordbrief-setup:latest, read-only source mount,
--network none, --rm, no production volumes/ports/credentials. F3: production
hashes unchanged; runtime/entrypoint/module diff empty; git diff --check exits 0.
No dependency, production bypass, deployment, migration, journal GC, commit, tag,
or push. Canonical HEAD/tag are unchanged. Stop for deployment review.
# Phase 3D non-destructive foundation (current work)

- [x] Exact sidecars preserve actual-native identity/position decisions with absent retired prefixes.
  CHECK: node collector/test/retention_evidence_test.mjs
  EXPECT: RETENTION EVIDENCE VERIFIED
  EVIDENCE: Windows PowerShell and Linux container exited 0: 129 actual segments,
  127 retired, 250 replay IDs, 12 corrupt-state refusals; new/re-added watches,
  wrong-channel live replay, cache clear, partial-page process death pass.
- [x] Manifest publication and real Linux lock/crash gates pass before any deletion code exists.
  CHECK: docker run --rm --pull never --network none --mount "type=bind,source=C:\Users\Sheriff\Desktop\src\CordBrief\collector,target=/collector,readonly" --entrypoint node docker-cordbrief-setup:latest /collector/test/retention_publish_test.mjs
  EXPECT: "passed":true
  EVIDENCE: Linux exit 0; 55 segments, 54 sidecars, 108 identities, 3 crash
  boundaries, 3 lock refusals, orphan/incremental publication and inherited lease
  reuse. Additional run mounted disposable cross-compiled Core binary via
  CORDBRIEF_TEST_CORE_BINARY: coreVerified=true, real Core suffix read passed.
- [x] Core validates certified topology, refuses retired cursors, and excludes standalone readers.
  CHECK: go test ./internal/journal ./internal/digest ./cmd/cordbrief ./internal/web ./internal/delivery
  EXPECT: exit 0
  EVIDENCE: Windows and Linux Go targeted packages exit 0, including process lock
  contention; Linux additionally tests symlink refusal. Targeted vet exits 0;
  go list -m all prints only cordbrief. Windows actual-native --require-safe
  exits 0, including 1050-ID cap continuation. No deletion code or deployment.
# Phase 3D certified physical deletion (current work)

- [x] Certified ascending unlink, directory durability, process restart and invalid-state refusal.
  CHECK: docker run --rm --pull never --network none --mount "type=bind,source=C:\Users\Sheriff\Desktop\src\CordBrief\collector,target=/collector,readonly" --entrypoint node docker-cordbrief-setup:latest /collector/test/retention_gc_test.mjs
  EXPECT: CERTIFIED GC VERIFIED
  EVIDENCE: Linux exit 0; 7 actual segments, 6 deleted prefix candidates, 24
  per-unlink/sync crashes/failures, 15 byte-preserving refusals. Resumed GC is
  idempotent, native retired replay appends zero, active bytes remain identical.
  Additional disposable Core binary mount: coreVerified=true; real Core reads
  retained event after actual unlink and commits the expected cursor.
- [x] Existing publication/identity reader regression gates remain green.
  CHECK: Linux container runs of retention_publish_test.mjs and retention_evidence_test.mjs
  EXPECT: exit 0
  EVIDENCE: Both exit 0: publication still deletes zero by default; reader gate
  retains 250-ID exact dedupe and 12 corruption refusals. Go journal tests and
  git diff --check exit 0. No production data mounts, deployment, or push.
# Phase 3D production eligibility — blocked, not completed

- [ ] Controlled production deployment, certified deletion, continuity and rollback proof.
  EVIDENCE: 2026-09-08 read-only preflight found only active segment 1 (25,121
  bytes), Core cursor (1,9451), recovery v2 with no pending/sweep, both services
  healthy. No closed/Core-safe prefix exists. Production left unchanged; no
  deployment/GC/checkpoint claimed. See docs/PHASE_3D_PRODUCTION_PREFLIGHT.md.
