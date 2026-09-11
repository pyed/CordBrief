# Gates: M14 Official Discord RPC Collector Foundation & Correctness

OWNS: collector/rpc/**, collector/test/rpc_*, collector/test/mock_discord_rpc.mjs, internal/journal/rpc_interop_test.go, GATES.md

Scope: Implement the smallest solid foundation for the official Discord RPC collector and prove retention, replay, and deduplication correctness.

- [x] G1: Low-level Discord IPC v1 wire framing codec encodes, decodes, and parses stream chunk boundaries.
  CHECK: node collector/test/rpc_frame_test.mjs
  EXPECT: rpc_frame_test passed
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=01de83f3e4a27f4a993ab99b4d82553293b66c04cf4ad9ad557ec17574ae6728; output-bytes=22

- [x] G2: Mock Discord RPC server and protocol client complete handshake, authentication, catalog queries, subscriptions, and snapshots.
  CHECK: node collector/test/rpc_protocol_test.mjs
  EXPECT: rpc_protocol_test passed
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=c44c87bed6fb339a2f50ffb0e9df7d9f4867c524ee67fd0e4ebb84547748224c; output-bytes=25

- [x] G3: RPC collector reconciles watchlist, discovers catalog, captures live MESSAGE_CREATE events to segmented NDJSON, and updates collector-status.json.
  CHECK: node collector/test/rpc_collector_test.mjs
  EXPECT: rpc_collector_test passed
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=f8fbf5dca0f84ef54026f2ce6ab9594b632bff9fb508f9bec807075f60d17cb4; output-bytes=26

- [x] G4: Go Core strictly parses and validates the catalog, collector status, and journal segments produced by the RPC collector.
  CHECK: go test -v -run TestRPCCollectorInterop ./internal/journal/...
  EXPECT: PASS: TestRPCCollectorInterop
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=8710f7d6cf1007b00b6fd4b0c070c1f9ae601cef9a0407e42b5a7b64f75e56e6; output-bytes=241

- [x] G5: All existing Go packages pass without regression.
  CHECK: go test ./...
  EXPECT: ok  	cordbrief/internal/web
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=d7644dc442fce0b84efa4f9afe7368f150a93dc3a96d23cb1b79fc31f1486228; output-bytes=439

- [x] G6: RPC collector validates journal topology, loads certified retired sidecars, and prevents replay duplicates across restart.
  CHECK: node collector/test/rpc_retention_test.mjs
  EXPECT: rpc_retention_test passed
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=b2eca00078e24ca71d9236d69cafd072521dc3b3d459fd9eb9960ae8f07ade3c; output-bytes=63

- [x] G7: Existing retention evidence and recovery contract suites pass without regression.
  CHECK: node collector/test/retention_evidence_test.mjs
  EXPECT: RETENTION EVIDENCE VERIFIED
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=dca606067e7fbaf503023b706b1e54d75b181e7e5a983916a413a1f679f31a4f; output-bytes=146

- [x] G8: Torn-progress and crash boundaries across journal write, fsync, and recovery checkpoint are repaired and proved idempotent via replay and deduplication.
  CHECK: node collector/test/rpc_crash_concurrency_test.mjs --torn-and-crash
  EXPECT: rpc_crash_tests passed
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=b5009c970583d203dd01f249715f87de6c32ec0c63d692e2fc3ef3f1f375759d; output-bytes=421

- [x] G9: RPC collector enforces mutual exclusion via runtime.lock, preventing concurrent journal writes during retention maintenance.
  CHECK: node collector/test/rpc_crash_concurrency_test.mjs --lock-exclusion
  EXPECT: rpc_lock_exclusion passed
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=e2dd6d6b6572d600cd572c8fb0cbce7b8dc86873e183392847e1f6115b8e280c; output-bytes=315
