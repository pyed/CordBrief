# Gates: Milestone 13 Phase 3A — Adversarial Audit Correctness Hardening

OWNS: cmd/** internal/** collector/** docker/** docs/** GATES.md

Scope: Targeted correctness and hardening sweep addressing valid adversarial audit findings: canonical batch-ID validation, collector command durability/concurrency, multi-segment backlog calculation, bounded web requests/server timeouts, runtime symlink safety, collision-safe artifact writes, centralized durable directory fsync, journal schema evolution, CSRF threat model hardening, and cross-process committing lock.

- [x] G1: Canonical Batch ID Validation & Path Boundary Hardening
  CHECK: go test -v -run TestBatchIDValidation ./internal/digest ./internal/delivery ./internal/web ./internal/inbox
  EXPECT: ok  	cordbrief/internal/web

- [x] G2: Collector Command Durability & Atomic Publication (TOCTOU Proof)
  CHECK: go test -v -run TestCollectorCommandDurability ./internal/journal
  EXPECT: ok  	cordbrief/internal/journal

- [x] G3: Multi-Segment Backlog Calculation
  CHECK: go test -v -run TestCalculateBacklog ./internal/journal ./internal/web
  EXPECT: ok  	cordbrief/internal/web

- [x] G4: Bounded Web Request Body & Server Timeouts
  CHECK: go test -v -run TestWebRequestLimitsAndTimeouts ./internal/web ./cmd/cordbrief
  EXPECT: ok  	cordbrief/cmd/cordbrief

- [x] G5: Runtime Current-Symlink Atomic Activation
  CHECK: node collector/test/retention_test.mjs
  EXPECT: ALL RETENTION & ROLLBACK TESTS PASSED (100%)

- [x] G6: Collision-Safe Artifact Temp Creation
  CHECK: go test -v -run TestArtifactConcurrentTempSafety ./internal/digest
  EXPECT: ok  	cordbrief/internal/digest

- [x] G7: Centralized Durable Atomic Write Helper
  CHECK: go test -v -run TestDurableAtomicWrite ./internal/durable
  EXPECT: ok  	cordbrief/internal/durable

- [x] G8: Journal Schema Evolution Compatibility (Additive Policy)
  CHECK: go test -v -run TestJournalSchemaEvolution ./internal/journal
  EXPECT: ok  	cordbrief/internal/journal

- [x] G9: CSRF / Browser Threat Model Hardening
  CHECK: go test -v -run TestCSRFProtectionAndThreatModel ./internal/web
  EXPECT: ok  	cordbrief/internal/web

- [x] G10: Committing Core Single-Writer Lock
  CHECK: go test -v -run TestCommitLock ./internal/journal ./cmd/cordbrief
  EXPECT: ok  	cordbrief/cmd/cordbrief

- [x] G11: Small Cleanups (strings.Title, filepath.Join, error logging)
  CHECK: go test -v -run TestSmallCleanups ./internal/delivery ./internal/journal
  EXPECT: ok  	cordbrief/internal/journal

- [x] G12: Full Automated Regression Suite (AUTOMATED PASS)
  CHECK: node collector/test/writer_test.mjs && node collector/test/recovery_test.mjs && node collector/test/retention_test.mjs && node collector/test/operational_test.mjs && go test -count=1 -timeout=60s ./... && go vet ./... && go list -m all
  EXPECT: regression bar passed

- [x] G13: Live Cross-Container Commit-Lock Contention on Linux Disposable Volume (LIVE PASS)
  CHECK: docker volume create cb-disposable-commit-data && docker run -d --name cb-lock-holder -v cb-disposable-commit-data:/var/cordbrief/data docker-cordbrief-core lock-probe --data-dir /var/cordbrief/data --hold 60 && docker run --rm -v cb-disposable-commit-data:/var/cordbrief/data docker-cordbrief-core lock-probe --data-dir /var/cordbrief/data (returns exit code 1) && docker kill cb-lock-holder && docker rm cb-lock-holder && docker run --rm -v cb-disposable-commit-data:/var/cordbrief/data docker-cordbrief-core lock-probe --data-dir /var/cordbrief/data (returns exit code 0) && docker volume rm cb-disposable-commit-data
  EXPECT: exit 1 on active lock contention; exit 0 immediately after kill without lockfile deletion

- [x] G14: Live Linux Concurrent Exclusive-Publication Race on Disposable Volume (LIVE PASS)
  CHECK: docker volume create cb-disposable-excl-data && concurrent docker run exclusive-write --dest /data/target.json (Contestant A vs B)
  EXPECT: exactly 1 winner (exit 0), exactly 1 loser (exit 2, destination already exists), complete valid JSON written, overwrite rejected (exit 2)

- [x] G15: Live Linux Adversarial Lock-Ownership Test Suite in Setup Container (LIVE PASS)
  CHECK: docker run --rm -v "C:\Users\Sheriff\Desktop\src\CordBrief\collector:/collector" docker-cordbrief-setup node /collector/test/adversarial_lock_test.mjs
  EXPECT: ALL ADVERSARIAL & MAINTENANCE LIFECYCLE TESTS: 100% PASSED (unflocked FD 9 refused, wrong file refused, collector role refused, true flock owner permitted)

- [x] G16: Live Production Deployment, Continuity Baseline & Live Contention Probe (LIVE PASS)
  CHECK: docker compose up -d --no-deps cordbrief-core && docker compose run --rm cordbrief-core lock-probe
  EXPECT: cordbrief-core healthy on port 28741; probe rejected with exit 1; collector un-restarted; journal/cursor/watchlist/digests/deliveries/scheduler identical to baseline


