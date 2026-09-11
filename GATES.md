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

- [x] G7: Documentation & Working Tree Cleanliness: README.md updated to reflect only official Discord RPC setup/runtime, git working tree clean, checkpoint commit ready.
  CHECK: git status --porcelain
  EXPECT: 
  EVIDENCE: README.md, docs/SETUP.md, docs/ARCHITECTURE.md, and CONTRIBUTING.md updated to document official Discord RPC architecture only; working tree clean.
