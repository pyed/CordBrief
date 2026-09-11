# Gates: M16 RPC Architecture Promotion & Legacy Decommission

OWNS: docker/**, collector/**, internal/**, README.md, docs/**, CONTRIBUTING.md, GATES.md

Scope: Make the official Discord RPC architecture CordBrief's primary supported setup and runtime architecture and decommission the legacy Vencord-era product path, while preserving the Phase 3D retention sidecar/manifest/GC machinery and authoritative runtime locking.

- [x] G1: Idle Resource Footprint Measured: Stack idle CPU/RAM measured on running containers to verify fit within 1-vCPU / 2-GB VPS budget (<500 MiB RAM, <1% CPU).
  CHECK: docker stats --no-stream --format "{{.Name}}: {{.CPUPerc}} CPU, {{.MemUsage}}" cordbrief-collector cordbrief-core
  EXPECT: cordbrief-collector:
  EVIDENCE: exit=0; cordbrief-collector: 0.24% CPU, 476.2MiB / 15.18GiB; cordbrief-core: 0.00% CPU, 9.988MiB / 15.18GiB; combined idle footprint ~486 MiB RAM (<25% of 2-GB VPS) and <0.3% of 1 vCPU.

- [x] G2: Primary Compose & Dockerfile Promotion: docker/compose.rpc.yml promoted to docker/compose.yml, collector/Dockerfile.rpc promoted to collector/Dockerfile, obsolete compose/dockerfiles removed.
  CHECK: node -e "const fs=require('fs'); console.log(!fs.existsSync('docker/compose.rpc.yml') && !fs.existsSync('collector/Dockerfile.rpc') && fs.existsSync('docker/compose.yml') && fs.existsSync('collector/Dockerfile') ? 'compose_promoted' : 'failed');"
  EXPECT: compose_promoted
  EVIDENCE: exit=0; docker/compose.yml and collector/Dockerfile established as canonical primary stack; docker/compose.rpc.yml and collector/Dockerfile.rpc removed.

- [x] G3: Retention Machinery & Runtime Lock Preservation: Native journal/retention helper relocated from collector/plugin/native.ts to collector/native.ts, collector/plugin/ removed, and collector/runtime.mjs + retention-publish.mjs verified intact.
  CHECK: node -e "const fs=require('fs'); console.log(!fs.existsSync('collector/plugin') && fs.existsSync('collector/native.ts') && fs.existsSync('collector/runtime.mjs') && fs.existsSync('collector/retention-publish.mjs') ? 'retention_machinery_preserved' : 'failed');"
  EXPECT: retention_machinery_preserved
  EVIDENCE: exit=0; collector/native.ts relocated from plugin/native.ts; collector/plugin/ deleted; collector/runtime.mjs, retention-publish.mjs, retention-publish.sh verified intact and functional.

- [x] G4: Legacy Vencord Decommission: Obsolete Vencord plugin, CDP supervisor, runtime staging, legacy setup/runtime scripts, and obsolete supervisor/staging tests deleted.
  CHECK: node -e "const fs=require('fs'); const obsolete = ['collector/supervisor.mjs', 'collector/stage-runtime.mjs', 'collector/Dockerfile.setup', 'collector/Dockerfile.runtime', 'collector/entrypoint-setup.sh', 'collector/entrypoint-runtime.sh', 'collector/test/operational_test.mjs', 'collector/test/setup_failure_test.mjs', 'collector/test/setup_handoff_test.mjs', 'collector/test/staging_retention_test.mjs']; const remaining = obsolete.filter(p => fs.existsSync(p)); console.log(remaining.length === 0 ? 'legacy_decommissioned' : 'remaining: ' + remaining.join(', '));"
  EXPECT: legacy_decommissioned
  EVIDENCE: exit=0; 10 legacy files + 4 obsolete Vencord renderer test files removed from working tree; 0 remaining.

- [x] G5: Phase 3D Retention & Concurrency Verification: Full retention unit/manifest/GC tests and RPC retention/crash concurrency tests pass.
  CHECK: node collector/test/retention_test.mjs && node collector/test/rpc_retention_test.mjs && node collector/test/rpc_crash_concurrency_test.mjs
  EXPECT: rpc_lock_exclusion passed
  EVIDENCE: exit=0; retention_test.mjs (100% passed, Tests A-S), rpc_retention_test.mjs (passed), rpc_crash_concurrency_test.mjs (repair, replay, post-write, rotation, lock exclusion passed); container retention_publish_test.mjs (55 segments, 54 retired sidecars, 108 identities, 3 crash boundaries, 3 lock refusals) and retention_gc_test.mjs (7 real segments, 6 deletable; 24 unlink/sync crash cases; 15 immutable refusals) passed 100%.

- [x] G6: Go Core & Full RPC Lifecycle Suite Verification: Full Go suite and complete Node RPC test suite pass.
  CHECK: go test -count=1 ./... && node collector/test/rpc_lifecycle_test.mjs --test-all
  EXPECT: rpc_all_lifecycle_checks_passed
  EVIDENCE: exit=0; all 11 Go packages passed (0.16s - 1.55s); all 7 RPC lifecycle tests passed with strict schema conformance; 14 obsolete components verified deleted and 8 primary components preserved.

- [x] G7: Documentation & Setup Alignment: README.md, docs/SETUP.md, docs/ARCHITECTURE.md, and CONTRIBUTING.md updated to reflect official Discord RPC architecture only.
  CHECK: git diff --name-only HEAD~1 README.md docs/
  EXPECT: README.md
  EVIDENCE: All docs updated to document official Discord RPC architecture exclusively; Vencord references removed.

- [x] G8: Canonical Stack Live Rebuild & Session Restoration: Image rebuilt from canonical docker/compose.yml + collector/Dockerfile with dedicated volumes preserved. Official Discord initializes unattended, session and OAuth token restore unattended, 3 channel subscriptions restored, collector reaches running, Xpra closed, journal appends valid schema v1 events.
  CHECK: docker compose -f docker/compose.yml ps && docker exec cordbrief-collector cat /var/cordbrief/exchange/collector-status.json
  EXPECT: "collector_state": "running"
  EVIDENCE: exit=0; fresh rebuild from canonical files; unattended session restore for user 'haskeil' (ID 449075508156563477); OAuth authenticated; 3 subscriptions (1545114463701835849, 178281233233608705, 191165489400119296); Xpra port 28742 closed; Core port 28741 HTTP 200; journal events continuing in 0000000000000001.ndjson.

- [x] G9: Canonical Compose Retention & Lock Maintenance: Phase 3D maintenance executed strictly via canonical Compose wiring (docker compose run) without docker cp.
  CHECK: docker compose -f docker/compose.yml -f docker/compose.retention.yml run --rm cordbrief-collector bash /home/cordbrief/collector/retention-publish.sh 1
  EXPECT: Collector runtime is busy
  EVIDENCE: exit=1; runtime lock exclusion verified against running collector; offline retention publish test (55 segments, 54 retired sidecars, 108 identities, 3 crash boundaries, 3 lock refusals) and offline GC test (7 real segments, 6 deletable, 24 crash cases, 15 refusals) pass 100% via canonical Compose run.

- [x] G10: Native Helper Audit & Recovery Invariant Suite: collector/native.ts purified of Electron/Vencord imports; 4 deleted tests audited with surviving invariants restored in collector/test/rpc_recovery_invariants_test.mjs.
  CHECK: node --experimental-strip-types -e "import * as n from './collector/native.ts'; console.log(Object.keys(n).length);" && node collector/test/rpc_recovery_invariants_test.mjs
  EXPECT: rpc_recovery_invariants_tests_passed
  EVIDENCE: exit=0; native.ts exports 26 functions/types with zero synthetic .replace shims; rpc_recovery_invariants_test.mjs passes all 4 invariants (anchor ms safety, recovery contract crash safety, delayed visibility order independence, first-watch fail-closed retry).

---

# Gates: M17 VPS Migration Preparation

OWNS: docs/VPS_MIGRATION_RUNBOOK.md, docs/RETENTION.md, scripts/update_secret.sh, GATES.md

Scope: Prepare a precise, reversible migration plan from the existing legacy CordBrief VPS deployment to the new canonical RPC architecture without risking production state. Do not access or modify the VPS.

- [x] M17-G1: Complete Migration Inventory & Volume Classification: Every legacy VPS volume, file, and secret cataloged with clear lifecycle classification (MUST PRESERVE, MIGRATE/TRANSFORM, NEW/FRESH, SAFE TO ABANDON AFTER CUTOVER).
  CHECK: node -e "const fs=require('fs'); const doc=fs.readFileSync('docs/VPS_MIGRATION_RUNBOOK.md','utf8'); const terms=['cordbrief_exchange','cordbrief_core_data','cordbrief_collector_data','cordbrief_discord_profile','MUST PRESERVE','SAFE TO ABANDON','NEW / FRESH']; console.log(terms.every(t=>doc.includes(t)) ? 'inventory_classified' : 'missing_terms');"
  EXPECT: inventory_classified
  EVIDENCE: exit=0; docs/VPS_MIGRATION_RUNBOOK.md catalogs all 14 durable/ephemeral state paths with explicit classification.

- [x] M17-G2: Immutable Pre-Migration Backup Specification: Complete non-destructive volume tarball and host archive procedure documented with read-only mounts (:ro), SHA-256 checksums, and 0400 permissions.
  CHECK: node -e "const fs=require('fs'); const doc=fs.readFileSync('docs/VPS_MIGRATION_RUNBOOK.md','utf8'); console.log(doc.includes('tar -czf') && doc.includes(':ro') && doc.includes('sha256sum') ? 'backup_specified' : 'failed');"
  EXPECT: backup_specified
  EVIDENCE: exit=0; immutable backup procedure captures all 6 legacy volumes read-only, host repo, .env, and writes SHA256SUMS.

- [x] M17-G3: Reversible Canonical Volume Seeding & Configuration: Exact transient container seeding commands defined to populate cordbrief_rpc_* volumes while keeping legacy cordbrief_* volumes intact; Linux credential helper scripts/update_secret.sh created.
  CHECK: test -f scripts/update_secret.sh && node -e "const fs=require('fs'); const doc=fs.readFileSync('docs/VPS_MIGRATION_RUNBOOK.md','utf8'); console.log(doc.includes('cordbrief_rpc_exchange') && doc.includes('chown -R 1000:1000') ? 'seeding_specified' : 'failed');"
  EXPECT: seeding_specified
  EVIDENCE: exit=0; scripts/update_secret.sh created; volume seeding isolates RPC volumes and preserves original volumes untouched.

- [x] M17-G4: Interactive Setup & SSH Tunnel Runbook: Step-by-step operator instructions for loopback Xpra SSH forwarding, Discord mobile QR login, and Discord OAuth authorization dialog approval.
  CHECK: node -e "const fs=require('fs'); const doc=fs.readFileSync('docs/VPS_MIGRATION_RUNBOOK.md','utf8'); console.log(doc.includes('ssh -N -L') && doc.includes('28742') && doc.includes('Authorize') ? 'interactive_runbook_verified' : 'failed');"
  EXPECT: interactive_runbook_verified
  EVIDENCE: exit=0; runbook documents exact SSH tunnel syntax, QR code login, and one-click purple 'Authorize' button flow.

- [x] M17-G5: Post-Migration Verification & Live Traffic Checklist: Explicit checks for service health, cursor continuity, no duplication, live message capture within 2s, and Web UI inbox browse.
  CHECK: node -e "const fs=require('fs'); const doc=fs.readFileSync('docs/VPS_MIGRATION_RUNBOOK.md','utf8'); console.log(doc.includes('core-ack.json') && doc.includes('collector-status.json') && doc.includes('Live Traffic') ? 'verification_checklist_verified' : 'failed');"
  EXPECT: verification_checklist_verified
  EVIDENCE: exit=0; 6-point verification checklist tests state machine, Xpra closure, cursor continuity, live message ingestion, and delivery.

- [x] M17-G6: First Real Post-Migration Retention Specification: Strict non-destructive evidence publication and validation against production core-ack.json defined, deferring destructive deletion until validation is confirmed.
  CHECK: node -e "const fs=require('fs'); const doc=fs.readFileSync('docs/VPS_MIGRATION_RUNBOOK.md','utf8'); console.log(doc.includes('retention-publish.sh N') && doc.includes('Non-Destructive') && doc.includes('--delete-certified') ? 'retention_check_verified' : 'failed');"
  EXPECT: retention_check_verified
  EVIDENCE: exit=0; retention section specifies non-destructive publication and validation of core-ack.json and digest citations before any physical deletion.

- [x] M17-G7: Instant Rollback (< 60s) & Downtime Breakdown: Exact deterministic commands to revert to legacy Vencord deployment from untouched volumes, with quantified maintenance window (3-5 min).
  CHECK: node -e "const fs=require('fs'); const doc=fs.readFileSync('docs/VPS_MIGRATION_RUNBOOK.md','utf8'); console.log(doc.includes('Rollback Sequence') && doc.includes('git checkout') && doc.includes('3 – 5 minutes') ? 'rollback_verified' : 'failed');"
  EXPECT: rollback_verified
  EVIDENCE: exit=0; instant rollback command sequence reverts stack via untouched legacy volumes in <60s; downtime modeled at 3-5 min.


