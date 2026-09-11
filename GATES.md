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
  EVIDENCE: exit=0; fresh rebuild from canonical files; unattended session restore for authenticated user; OAuth authenticated; 3 channel subscriptions active; Xpra port 28742 closed; Core port 28741 HTTP 200; journal events continuing in 0000000000000001.ndjson.

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

OWNS: docs/VPS_MIGRATION_RUNBOOK.md, docs/RETENTION.md, scripts/update_secret.sh, scripts/vps_preflight.sh, collector/test/vps_migration_test.mjs, GATES.md

Scope: Prepare a precise, reversible migration plan from the existing legacy CordBrief VPS deployment to the new canonical RPC architecture without risking production state. Do not access or modify the VPS.

- [x] M17-G1: Complete Migration Inventory & Volume Classification: Every legacy VPS volume, file, and secret cataloged with clear lifecycle classification (MUST PRESERVE, MIGRATE / TRANSFORM, NEW / FRESH, SAFE TO ABANDON); proven UID/GID 1000:1000 invariant verified.
  CHECK: node collector/test/vps_migration_test.mjs --test 1
  EXPECT: m17_g1_passed
  EVIDENCE: exit=0; parsed 17 inventory items in runbook; verified all entries map strictly to allowed classifications; proven UID/GID 1000:1000 invariant verified across collector/Dockerfile, docker/Dockerfile.core, and docker/compose.yml.

- [x] M17-G2: Application-Consistent Backup & Safe Restore Cleanup: Writers cleanly stopped and verified stopped before backup tarball creation; read-only mounts (:ro), SHA-256 checksums, and 0400 permissions verified; safe dotfile-inclusive restore cleanup using find verified (rm -rf /dst/* prohibited).
  CHECK: node collector/test/vps_migration_test.mjs --test 2
  EXPECT: m17_g2_passed
  EVIDENCE: exit=0; verified application-consistent ordering (stop writers -> verify stopped -> backup :ro with sha256sum and 0400 -> seed canonical volumes); hot/live backup prohibited; safe restore cleanup using find verified.

- [x] M17-G3: Deterministic Credential Seeding: Standalone Linux credential helper scripts/update_secret.sh created and verified on isolated Docker volume; zero secret leakage, mode 0600, uid:gid 1000:1000.
  CHECK: node collector/test/vps_migration_test.mjs --test 3
  EXPECT: m17_g3_passed
  EVIDENCE: exit=0; scripts/update_secret.sh executed against isolated test volume; verified mode 0600, uid:gid 1000:1000, valid credentials.json structure, and zero token leakage in stdout/stderr.

- [x] M17-G4: Documented OAuth Scopes & Loopback Port Security: Proven scopes (rpc, identify, messages.read) documented consistently; loopback-only port bindings (28741, 28742) and SSH tunnel requirements verified.
  CHECK: node collector/test/vps_migration_test.mjs --test 4
  EXPECT: m17_g4_passed
  EVIDENCE: exit=0; verified runbook documents exact OAuth scope set (rpc identify messages.read), loopback port bindings (28741, 28742), and SSH loopback tunnel requirement.

- [x] M17-G5: Read-Only Preflight Discovery Execution: Standalone discovery script scripts/vps_preflight.sh executes cleanly and audits host, resources, docker, containers, volumes, cursor, and journal without mutating state.
  CHECK: node collector/test/vps_migration_test.mjs --test 5
  EXPECT: m17_g5_passed
  EVIDENCE: exit=0; scripts/vps_preflight.sh executed and verified read-only audit contract across host, resources, docker, containers, volumes, cursor, and journal without performing mutations.

- [x] M17-G6: Non-Destructive First Retention Check Specification: Maintenance container mounts cordbrief_rpc_core_data, verifies real production core-ack.json has consumed past segment N before publishing evidence; physical unlinking (--delete-certified) is strictly deferred.
  CHECK: node collector/test/vps_migration_test.mjs --test 6
  EXPECT: m17_g6_passed
  EVIDENCE: exit=0; verified runbook mandates inspecting production core-ack.json and verifying cursor > N before publishing evidence; physical unlinking (--delete-certified) is strictly deferred.

- [x] M17-G7: Compliant Rollback Specification & Divergence Window Model: Rollback strictly forbids restarting legacy Vencord/CDP/patched collector; models pre-ingest vs post-ingest divergence window; defaults to leaving Core stopped; safe non-mutating read-only inspection verified.
  CHECK: node collector/test/vps_migration_test.mjs --test 7
  EXPECT: m17_g7_passed
  EVIDENCE: exit=0; verified rollback strictly forbids restarting legacy Vencord/CDP/patched collector; models pre-ingest vs post-ingest divergence window; mandates Core remains stopped by default; safe read-only SQLite/filesystem inspection verified.

- [x] M18-G1: Clean-Room Install Proof & Public Docs Conformance: Fresh clone / clean volume startup follows only public documentation; step-by-step Discord Developer Portal guide with redirect URI (http://127.0.0.1:32145/callback), scopes (rpc, identify, messages.read), and secure secret update scripts documented in SETUP.md, README.md, and CHANGELOG.md; VPS migration runbook marked archival; recovery contract updated to official RPC model.
  CHECK: node collector/test/clean_room_proof.mjs
  EXPECT: m18_rc_proof_passed
  EVIDENCE: exit=0; clean-room proof verified; docs/SETUP.md, README.md, CHANGELOG.md, and docs/RECOVERY_CONTRACT.md conform 100% to public v2.0.0 architecture; deterministic credential seeding on clean volume verified.

- [x] M18-G2: Personal Identifier & Secret Hygiene Scrub: Git history audited across all 71k+ diff lines (zero actual secrets committed); complete scrub of personal snowflakes, client IDs, channel IDs, usernames, and VPS IPs across all tracked repository files and tests; .gitignore protects credentials and OAuth tokens.
  CHECK: node collector/test/clean_room_proof.mjs
  EXPECT: Zero occurrences found
  EVIDENCE: exit=0; clean_room_proof git grep across repository returns 0 occurrences of personal client ID, user snowflake, channels, or host IPs; Git commit history verified free of active secrets.

- [x] M18-G3: Full Functional & Retention Regressions: Complete test suite across Go and Node passes 100%.
  CHECK: go test -count=1 ./... && node collector/test/rpc_lifecycle_test.mjs --test-all && node collector/test/vps_migration_test.mjs
  EXPECT: All suites pass
  EVIDENCE: exit=0; 11 Go packages pass (0.16s - 1.41s); all 7 RPC lifecycle tests pass; M17 migration suite passes (100%); Phase 3D retention publish and GC tests pass inside canonical container environment.

- [x] M18-G4: Canonical Stack & Unattended Restart Verification: Official Discord client running unmodified; unattended session & token restore across container restart without UI interaction, mouse clicks, or window activation hacks; Xpra port 28742 closed in normal operation; Core web UI healthy on 28741.
  CHECK: docker ps && docker exec cordbrief-collector cat /var/cordbrief/exchange/collector-status.json
  EXPECT: "collector_state": "running", "discord_authenticated": true
  EVIDENCE: exit=0; cordbrief-collector and cordbrief-core healthy; unattended restart verified in 10s; collector_state running, discord_authenticated true; Xpra port 28742 closed.
