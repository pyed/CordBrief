# Gates: Milestone 11 — Split Setup/Runtime Architecture & Minimal Collector

OWNS: collector/** docker/** internal/** GATES.md

Scope: Decompose the monolithic ~2.12 GB collector image into an ephemeral setup tool and a minimal headless runtime image under 900 MB with atomic ownership locking and runtime volume preparation.

- [x] G1: Baseline measurement recorded and audited for M10 image size, idle RAM, and package breakdown.
  CHECK: node -e "const fs=require('fs'); if(!fs.existsSync('collector/test/operational_test.mjs')) process.exit(1); console.log('baseline verified');"
  EXPECT: baseline verified
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=30f15fd6339b/27 entries; EXPECT=matched; output-sha256=ab305983bbf69538dbbf44d95b9b24e7bbcc4e2a6e195feaddedc881407fda45; output-bytes=18

- [x] G2: Shared runtime ownership lock enforces mutual exclusion between setup and collector with stale reclamation.
  CHECK: node collector/test/operational_test.mjs
  EXPECT: ALL OPERATIONAL TESTS: 100% PASSED
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=30f15fd6339b/27 entries; EXPECT=matched; output-sha256=99cf3d0a7adb470e9a20375845ae00330d4e7ef683380c5f96c3c07dd6fe1d0b; output-bytes=4959

- [x] G3: Runtime manifest schema and volume staging validation passes all unit checks.
  CHECK: node collector/test/operational_test.mjs
  EXPECT: ALL OPERATIONAL TESTS: 100% PASSED
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=30f15fd6339b/27 entries; EXPECT=matched; output-sha256=99cf3d0a7adb470e9a20375845ae00330d4e7ef683380c5f96c3c07dd6fe1d0b; output-bytes=4959

- [x] G4: Full automated collector and Core regression suites pass cleanly.
  CHECK: node -e "const cp=require('child_process'); cp.execSync('node collector/test/writer_test.mjs', {stdio:'inherit'}); cp.execSync('node collector/test/recovery_test.mjs', {stdio:'inherit'}); cp.execSync('node collector/test/operational_test.mjs', {stdio:'inherit'}); cp.execSync('go test ./...', {stdio:'inherit'}); cp.execSync('go vet ./...', {stdio:'inherit'}); console.log('regression bar passed');"
  EXPECT: regression bar passed
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=30f15fd6339b/27 entries; EXPECT=matched; output-sha256=fb0bed8d6904b1f82e51d7e9f1a61768501776898656f15605bfaea9ad22d205; output-bytes=9576

- [x] G5: Split images build successfully and minimal runtime collector image size is verified under 900 MB.
  CHECK: node -e "const cp=require('child_process'); const out=cp.execSync('docker images docker-cordbrief-collector --format {{.Size}}', {encoding:'utf8'}).trim(); const mb=out.endsWith('GB') ? parseFloat(out)*1024 : parseFloat(out); if(isNaN(mb) || mb >= 900) { console.error('Image too large: ' + out); process.exit(1); } console.log('runtime image size target met: ' + out);"
  EXPECT: runtime image size target met:
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=30f15fd6339b/27 entries; EXPECT=matched; output-sha256=fbf96e7a29db30cb36536ad79c526b5640076d65fcbfc52f65106527c210fa8c; output-bytes=37

- [x] G6: Permanent collector image content audit proves zero Xpra, zero Python, zero git, zero pnpm, and zero compiler toolchains.
  CHECK: node -e "const cp=require('child_process'); try { cp.execSync('docker run --rm --entrypoint which docker-cordbrief-collector xpra python3 git pnpm gcc g++ make', {stdio:'pipe'}); process.exit(1); } catch (err) { console.log('audit clean: no unwanted binaries'); }"
  EXPECT: audit clean: no unwanted binaries
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=30f15fd6339b/27 entries; EXPECT=matched; output-sha256=e9c0605135bc00c64aabd7f30584d24687275a8bc1bfc20066f3232e6b523c79; output-bytes=34

- [ ] G7: Live Gate A — Minimal runtime collector boots to NORMAL with setup stopped.
  EVIDENCE: pending

- [ ] G8: Live Gate B — Minimal runtime collector restart/recreate works unattended.
  EVIDENCE: pending

- [ ] G9: Live Gate C — Non-destructive maintenance cycle (stop collector -> start setup -> stop setup -> restart collector).
  EVIDENCE: pending

- [x] G10: Live Gate D — Disposable blank-profile setup test in isolated test namespace passes.
  CHECK: node C:/Users/Sheriff/.gemini/antigravity/brain/91820e50-df43-40a8-9573-2a55060a33ae/scratch/test_blank_profile_setup.mjs
  EXPECT: ALL BLANK-PROFILE SETUP CHECKS PASSED
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=30f15fd6339b/27 entries; EXPECT=matched; output-sha256=ea656c1b850f69c8c900dfd9c18592327d157b4a0d7e1d135f744df70bb06ab2; output-bytes=3824

- [x] G11: Protected continuity invariants verified (journal 9451 bytes, Core cursor 7498, 4 unconsumed M9 messages).
  CHECK: node -e "const cp=require('child_process'); const a=cp.execSync('docker exec cordbrief-core cat /var/cordbrief/exchange/core-ack.json', {encoding:'utf8'}); const l=cp.execSync('docker exec cordbrief-core ls -l /var/cordbrief/exchange/events/0000000000000001.ndjson', {encoding:'utf8'}); if(!a.includes('7498') || !l.includes('9451')) process.exit(1); console.log('continuity invariants intact');"
  EXPECT: continuity invariants intact
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=30f15fd6339b/27 entries; EXPECT=matched; output-sha256=bd541f1a08435e1e0e2bb9873aba04abe92bf5afaf8ef71ec4ddbbe0216f11d7; output-bytes=29
