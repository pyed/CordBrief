// collector/test/vps_migration_test.mjs
// Rigorous automated validation of Milestone M17 VPS migration runbook, preflight discovery,
// credential helper determinism, and rollback invariants.

import { readFileSync } from "node:fs";
import { execSync } from "node:child_process";
import assert from "node:assert";

const runbookPath = "docs/VPS_MIGRATION_RUNBOOK.md";
const runbook = readFileSync(runbookPath, "utf8");

// -----------------------------------------------------------------------------
// Test 1: Complete Migration Inventory & Volume Classification (M17-G1)
// -----------------------------------------------------------------------------
function test1() {
  console.log("[Test 1] Validating migration inventory table and volume classifications...");
  const tableMatch = runbook.match(/\| Path \/ Volume \|[\s\S]*?(?=\n---)/);
  assert(tableMatch, "Inventory table must be present in runbook");
  const tableLines = tableMatch[0].trim().split("\n").slice(2);
  const allowedClassifications = ["MUST PRESERVE", "MIGRATE / TRANSFORM", "NEW / FRESH", "SAFE TO ABANDON"];

  const inventory = tableLines.map(line => {
    const parts = line.split("|").map(p => p.replace(/\*\*/g, "").trim()).filter(Boolean);
    return {
      pathOrVolume: parts[0],
      location: parts[1],
      purpose: parts[2],
      classification: parts[3],
      action: parts[4]
    };
  });

  assert(inventory.length >= 15, `Expected at least 15 inventory entries, found ${inventory.length}`);

  for (const item of inventory) {
    assert(
      allowedClassifications.includes(item.classification),
      `Invalid classification "${item.classification}" for item "${item.pathOrVolume}"`
    );
  }

  const preserveItems = inventory.filter(i => i.classification === "MUST PRESERVE");
  assert(preserveItems.some(i => i.pathOrVolume.includes("cordbrief_exchange")), "cordbrief_exchange must be MUST PRESERVE");
  assert(preserveItems.some(i => i.pathOrVolume.includes("cordbrief_core_data")), "cordbrief_core_data must be MUST PRESERVE");

  const abandonItems = inventory.filter(i => i.classification === "SAFE TO ABANDON");
  assert(abandonItems.some(i => i.pathOrVolume.includes("cordbrief_discord_profile")), "cordbrief_discord_profile must be SAFE TO ABANDON");
  assert(abandonItems.some(i => i.pathOrVolume.includes("cordbrief_collector_data")), "cordbrief_collector_data must be SAFE TO ABANDON");

  console.log(`  ✔ Verified ${inventory.length} inventory items against allowed lifecycle classifications.`);
  console.log("m17_g1_passed");
}

// -----------------------------------------------------------------------------
// Test 2: Application-Consistent Pre-Migration Backup Sequence (M17-G2)
// -----------------------------------------------------------------------------
function test2() {
  console.log("[Test 2] Validating application-consistent backup sequence and ordering...");
  const s5Idx = runbook.indexOf("## 5. Phase B — Application-Consistent Cutover");
  const s6Idx = runbook.indexOf("## 6. Interactive Operator Sign-In");
  assert(s5Idx !== -1 && s6Idx !== -1, "Phase B cutover section must exist");
  const phaseB = runbook.substring(s5Idx, s6Idx);

  const stopWritersIdx = phaseB.indexOf("Step 5.1: Clean Stop of Writers");
  const verifyStoppedIdx = phaseB.indexOf("Step 5.2: Verify Writers Are Confirmed Stopped");
  const backupIdx = phaseB.indexOf("Step 5.3: Application-Consistent Checksummed Backup");
  const seedIdx = phaseB.indexOf("Step 5.4: Seed Canonical Volumes");

  assert(stopWritersIdx !== -1, "Step 5.1 must exist");
  assert(verifyStoppedIdx !== -1, "Step 5.2 must exist");
  assert(backupIdx !== -1, "Step 5.3 must exist");
  assert(seedIdx !== -1, "Step 5.4 must exist");

  assert(stopWritersIdx < verifyStoppedIdx, "Step 5.1 (stop writers) must precede Step 5.2 (verify stopped)");
  assert(verifyStoppedIdx < backupIdx, "Step 5.2 (verify stopped) must precede Step 5.3 (backup)");
  assert(backupIdx < seedIdx, "Step 5.3 (backup) must precede Step 5.4 (seed canonical volumes)");

  assert(phaseB.includes(":ro"), "Backup container must mount volumes read-only (:ro)");
  assert(phaseB.includes("sha256sum"), "Backup procedure must compute SHA-256 checksums");
  assert(phaseB.includes("chmod -R 0400"), "Backup directory must be locked down read-only (0400)");

  console.log("  ✔ Verified application-consistent backup ordering: stop -> verify stopped -> backup -> seed.");
  console.log("m17_g2_passed");
}

// -----------------------------------------------------------------------------
// Test 3: Deterministic Credential Seeding & Verification (M17-G3)
// -----------------------------------------------------------------------------
function test3() {
  console.log("[Test 3] Testing deterministic credential updater on isolated test volume...");
  const testVolName = "m17_test_cred_vol_" + Date.now();
  try {
    execSync(`docker volume create ${testVolName}`, { stdio: "pipe" });
    const mockClientId = "1547744191122247772";
    const mockSecret = "super_secret_discord_token_xyz987";

    const cmd = `bash scripts/update_secret.sh ${testVolName} ${mockClientId}`;
    const output = execSync(cmd, {
      input: mockSecret + "\n",
      encoding: "utf8",
      stdio: ["pipe", "pipe", "pipe"]
    });

    assert(!output.includes(mockSecret), "Secret MUST NOT be echoed or logged in stdout");
    assert(output.includes("successfully seeded into volume"), "Success message expected");

    const inspectOutput = execSync(
      `docker run --rm -v ${testVolName}:/v:ro alpine sh -c "cat /v/credentials.json && stat -c %a /v/credentials.json"`,
      { encoding: "utf8" }
    ).trim();

    const lines = inspectOutput.split("\n");
    const mode = lines[lines.length - 1].trim();
    const jsonStr = lines.slice(0, lines.length - 1).join("\n");
    const parsed = JSON.parse(jsonStr);

    assert.strictEqual(mode, "600", "credentials.json permissions must be exactly 0600");
    assert.strictEqual(parsed.client_id, mockClientId, "client_id must match provided ID");
    assert.strictEqual(parsed.client_secret, mockSecret, "client_secret must match provided secret");

    console.log("  ✔ scripts/update_secret.sh verified: deterministic volume seed, mode 0600, zero token leakage.");
    console.log("m17_g3_passed");
  } finally {
    try {
      execSync(`docker volume rm -f ${testVolName}`, { stdio: "ignore" });
    } catch {}
  }
}

// -----------------------------------------------------------------------------
// Test 4: Documented OAuth Scopes & Loopback Port Security (M17-G4)
// -----------------------------------------------------------------------------
function test4() {
  console.log("[Test 4] Validating documented OAuth scopes and loopback port hardening...");
  const expectedScopes = ["rpc", "identify", "messages.read"];
  for (const scope of expectedScopes) {
    assert(runbook.includes(scope), `Runbook must document OAuth scope "${scope}"`);
  }
  assert(runbook.includes("28741"), "Core Web UI port 28741 must be documented");
  assert(runbook.includes("28742"), "Temporary Xpra viewer port 28742 must be documented");
  assert(runbook.includes("127.0.0.1"), "Ports must be bound to 127.0.0.1");
  assert(runbook.includes("ssh -N -L"), "SSH port-forwarding loopback tunnel must be specified");

  console.log("  ✔ Verified OAuth scopes (rpc identify messages.read) and loopback port binding (28741, 28742).");
  console.log("m17_g4_passed");
}

// -----------------------------------------------------------------------------
// Test 5: Read-Only Preflight Discovery Execution (M17-G5)
// -----------------------------------------------------------------------------
function test5() {
  console.log("[Test 5] Executing scripts/vps_preflight.sh to verify read-only audit...");
  const preflightOutput = execSync("bash scripts/vps_preflight.sh", { encoding: "utf8" });
  assert(preflightOutput.includes("CordBrief M17: VPS Read-Only Preflight Audit"), "Header expected");
  assert(preflightOutput.includes("[1/6] Host & Repository State"), "Step 1 expected");
  assert(preflightOutput.includes("[2/6] System Resources"), "Step 2 expected");
  assert(preflightOutput.includes("[3/6] Docker & Compose Runtime"), "Step 3 expected");
  assert(preflightOutput.includes("[4/6] Containers & Compose Projects"), "Step 4 expected");
  assert(preflightOutput.includes("[5/6] Volume Inventory"), "Step 5 expected");
  assert(preflightOutput.includes("[6/6] Journal & Core Cursor Status"), "Step 6 expected");
  assert(preflightOutput.includes("Zero mutations performed"), "Read-only non-mutation guarantee expected");

  console.log("  ✔ scripts/vps_preflight.sh executed cleanly and confirmed read-only discovery contract.");
  console.log("m17_g5_passed");
}

// -----------------------------------------------------------------------------
// Test 6: Non-Destructive First Retention Check Specification (M17-G6)
// -----------------------------------------------------------------------------
function test6() {
  console.log("[Test 6] Validating non-destructive first retention check specification...");
  const s8Idx = runbook.indexOf("## 8. First Real Post-Migration Retention Check");
  const s9Idx = runbook.indexOf("## 9. Compliant Rollback Policy");
  assert(s8Idx !== -1 && s9Idx !== -1, "Section 8 (Retention) must exist");
  const retentionSec = runbook.substring(s8Idx, s9Idx);

  assert(retentionSec.includes("Strict Non-Destructive Policy"), "Must declare strict non-destructive policy");
  assert(retentionSec.includes("core-ack.json"), "Must inspect production core-ack.json");
  assert(retentionSec.includes("retention-publish.sh N"), "Must invoke retention-publish.sh N");
  assert(!retentionSec.includes("retention-publish.sh N --delete-certified") || retentionSec.includes("Never run `--delete-certified`"), "Must not execute delete-certified during cutover validation");
  assert(retentionSec.includes("Zero segments are deleted or unlinked"), "Must guarantee zero deletions during initial check");

  console.log("  ✔ Verified non-destructive first retention verification invariant.");
  console.log("m17_g6_passed");
}

// -----------------------------------------------------------------------------
// Test 7: Compliant Rollback Model & Divergence Window (M17-G7)
// -----------------------------------------------------------------------------
function test7() {
  console.log("[Test 7] Validating compliant rollback model and state divergence window...");
  const s9Idx = runbook.indexOf("## 9. Compliant Rollback Policy");
  const s9End = runbook.indexOf("## 10. Downtime Minimization");
  assert(s9Idx !== -1 && s9End !== -1, "Section 9 (Rollback) must exist");
  const rollbackSec = runbook.substring(s9Idx, s9End);

  assert(
    rollbackSec.includes("MUST NEVER be restarted") || rollbackSec.includes("must never be restarted"),
    "Rollback must explicitly state legacy collector must never be restarted"
  );
  assert(!rollbackSec.includes("compose.legacy.yml"), "Must not reference non-existent compose.legacy.yml");
  assert(rollbackSec.includes("docker compose -f docker/compose.yml down"), "Must stop failed stack");
  assert(rollbackSec.includes("Divergence") || rollbackSec.includes("divergence"), "Must explain state divergence window");
  assert(rollbackSec.includes("cordbrief-core"), "Permits starting standalone safe components (Core)");

  console.log("  ✔ Verified compliant rollback model: zero legacy collector reactivation, explicit divergence window.");
  console.log("m17_g7_passed");
}

// -----------------------------------------------------------------------------
// Dispatcher
// -----------------------------------------------------------------------------
const arg = process.argv[2];
if (arg === "--test" && process.argv[3]) {
  const testNum = parseInt(process.argv[3], 10);
  switch (testNum) {
    case 1: test1(); break;
    case 2: test2(); break;
    case 3: test3(); break;
    case 4: test4(); break;
    case 5: test5(); break;
    case 6: test6(); break;
    case 7: test7(); break;
    default: throw new Error(`Unknown test number: ${testNum}`);
  }
} else {
  console.log("=== Running Complete M17 Test Suite ===");
  test1();
  test2();
  test3();
  test4();
  test5();
  test6();
  test7();
  console.log("\n=== ALL M17 VPS MIGRATION TESTS PASSED (100%) ===");
  console.log("m17_vps_migration_suite_passed");
}
