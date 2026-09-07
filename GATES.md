# Gates: Milestone 13 Phase 3B — Durable Citations and Delivery Outbox

OWNS: cmd/** internal/** collector/** docker/** docs/** GATES.md

Scope: Targeted durability and cross-subsystem hardening: durable minimal source references for journal-independent citation rendering, privacy proof, production historical artifact migration, single-writer migration commit lock enforcement, two-phase delivery outbox invariant (PREPARED -> PENDING) before cursor commit, destination snapshotting in artifact and outbox, crash-injection matrix (A through K), distinct OS process restart proof (including Telegram-success-crash-before-local-persist and multipart boundaries), snowflake ID validation, and zero production regressions.

- [x] G1: Minimal Source Reference Schema, Extraction & Discord Snowflake Validation
  CHECK: go test -v -run "TestSourceDisplayMapping|TestSourceRefValidation" ./internal/delivery ./internal/digest
  EXPECT: ok  	cordbrief/internal/digest

- [x] G2: Artifact Privacy Property Assertion
  CHECK: go test -v -run TestArtifactPrivacyProperty ./internal/digest
  EXPECT: ok  	cordbrief/internal/digest

- [x] G3: Production Migration Dry-Run & Full Reconstruction Verification
  CHECK: docker compose -f docker/compose.yml run --rm cordbrief-core migrate --dry-run
  EXPECT: === DRY-RUN MIGRATION COMPLETE (All Eligible: true) ===

- [x] G4: Mutating Migration Single-Writer Lock Enforcement (Refused While Serve Holds Lock)
  CHECK: docker compose -f docker/compose.yml run --rm cordbrief-core migrate
  EXPECT: migrate: acquisition refused: cannot acquire commit lock

- [x] G5: Journal-Independence Test (Journal Removed Completely)
  CHECK: go test -v -run TestArtifactMigrationAndJournalIndependence ./internal/digest
  EXPECT: ok  	cordbrief/internal/digest

- [x] G6: Crash-Injection Matrix (Scenarios A through K Deterministic Proof)
  CHECK: go test -v -run TestCrashInjection ./internal/delivery
  EXPECT: ok  	cordbrief/internal/delivery

- [x] G7: Real Fresh-Process Restart Proof (Distinct OS Process Boundaries: Prepared, Committed, Telegram Success Crash)
  CHECK: go test -v -run TestRealProcessRestartProof ./internal/delivery
  EXPECT: ok  	cordbrief/internal/delivery

- [x] G8: Startup Reconciliation & Outbox Invariant
  CHECK: go test -v -run "TestDeliveryWorker|TestCrashInjection_ScenarioC|TestCrashInjection_ScenarioD" ./internal/delivery
  EXPECT: ok  	cordbrief/internal/delivery

- [x] G9: Live Production Deployment, Journal-Independence & Continuity Baseline
  CHECK: curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:28741/inbox && docker exec cordbrief-core cat /var/cordbrief/exchange/collector-status.json
  EXPECT: 200 and collector mode=normal, collector_state=running, discord_authenticated=true

- [x] G10: Full Automated Regression Bar
  CHECK: node collector/test/writer_test.mjs && node collector/test/recovery_test.mjs && node collector/test/retention_test.mjs && docker run --rm -v "C:\Users\Sheriff\Desktop\src\CordBrief\collector:/collector" docker-cordbrief-setup:latest node /collector/test/adversarial_lock_test.mjs && node collector/test/operational_test.mjs && go test -count=1 -timeout=60s ./... && go vet ./... && go list -m all
  EXPECT: ALL ADVERSARIAL & MAINTENANCE LIFECYCLE TESTS: 100% PASSED, cordbrief

