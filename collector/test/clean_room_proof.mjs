// collector/test/clean_room_proof.mjs
// M18 Release Candidate Verification: Clean-room proof, negative controls,
// privacy sanitization, documentation conformance, and live stack health.

import { readFileSync, existsSync } from "node:fs";
import { execSync } from "node:child_process";
import assert from "node:assert";

console.log("=== M18: CordBrief v2.0.0 Release Candidate Proof ===");

// 1. Personal Identifiers & Privacy Scrub
console.log("[Proof 1] Verifying total privacy scrub across repository...");
const forbiddenStrings = [
    "1547744191122247772",
    "449075508156563477",
    "178281233233608705",
    "191165489400119296",
    "1545114463701835849",
    "85.217.170.247",
    "haskeil",
    "Haskell",
    "sheriff_u"
];

for (const needle of forbiddenStrings) {
    let matches = "";
    try {
        matches = execSync(`git grep -i "${needle}"`, { encoding: "utf8", stdio: ["pipe", "pipe", "ignore"] }).trim();
    } catch {
        // Exit code 1 means no match found, which is what we expect
    }
    assert.strictEqual(matches, "", `Found unscrubbed occurrence of "${needle}":\n${matches}`);
}
console.log("  ✔ Zero personal snowflakes, client IDs, channel IDs, usernames, or IPs found.");

// 2. Secret Hygiene & Git Ignore
console.log("[Proof 2] Verifying secret hygiene and .gitignore protections...");
const gitignore = readFileSync(".gitignore", "utf8");
assert.ok(gitignore.includes("credentials.json"), ".gitignore must ignore credentials.json");
assert.ok(gitignore.includes("oauth-token*.json"), ".gitignore must ignore oauth-token*.json");
console.log("  ✔ .gitignore protects container credentials and OAuth tokens.");

// 3. Documentation & Setup Conformance
console.log("[Proof 3] Verifying public documentation conformance...");
const setupDoc = readFileSync("docs/SETUP.md", "utf8");
assert.ok(setupDoc.includes("http://127.0.0.1:32145/callback"), "SETUP.md must specify canonical redirect URI");
assert.ok(setupDoc.includes("rpc") && setupDoc.includes("identify") && setupDoc.includes("messages.read"), "SETUP.md must specify OAuth scopes");
assert.ok(setupDoc.includes("scripts/update_secret"), "SETUP.md must reference secure secret scripts");

const readmeDoc = readFileSync("README.md", "utf8");
assert.ok(readmeDoc.includes("http://127.0.0.1:32145/callback"), "README.md must reference redirect URI");
assert.ok(readmeDoc.includes("docs/SETUP.md"), "README.md must link to SETUP.md");

const changelog = readFileSync("CHANGELOG.md", "utf8");
assert.ok(changelog.includes("## [2.0.0]"), "CHANGELOG.md must contain [2.0.0] section");
assert.ok(changelog.includes("Official Discord RPC Architecture"), "CHANGELOG.md must document RPC promotion");

const runbook = readFileSync("docs/VPS_MIGRATION_RUNBOOK.md", "utf8");
assert.ok(runbook.includes("ARCHIVAL"), "VPS_MIGRATION_RUNBOOK.md must be marked archival");

const recoveryContract = readFileSync("docs/RECOVERY_CONTRACT.md", "utf8");
assert.ok(recoveryContract.includes("official Discord local RPC"), "RECOVERY_CONTRACT.md must reflect RPC model");
console.log("  ✔ All release documentation conforms to v2.0.0 architecture.");

// 4. Deterministic Credential Seeding on Disposable Volume
console.log("[Proof 4] Testing deterministic credential seeding on clean disposable volume...");
const testVol = "m18_clean_cred_" + Date.now();
try {
    execSync(`docker volume create ${testVol}`, { stdio: "pipe" });
    const out = execSync(`bash scripts/update_secret.sh ${testVol} 123456789012345678`, {
        input: "mock_secret_xyz_123\n",
        encoding: "utf8",
        stdio: ["pipe", "pipe", "pipe"]
    });
    assert.ok(!out.includes("mock_secret_xyz_123"), "Secret value must not be echoed in output");
    assert.ok(out.includes("successfully seeded into volume"), "Success confirmation expected");

    const statOut = execSync(`docker run --rm -v ${testVol}:/v:ro alpine stat -c "%a %u:%g" /v/credentials.json`, { encoding: "utf8" }).trim();
    assert.strictEqual(statOut, "600 1000:1000", "Must have mode 600 and ownership 1000:1000");
} finally {
    try { execSync(`docker volume rm -f ${testVol}`, { stdio: "pipe" }); } catch {}
}
console.log("  ✔ Credential updater is deterministic, secure (0600, 1000:1000), and leaks zero secrets.");

// 5. Canonical Stack Live State & Port Hardening
console.log("[Proof 5] Verifying canonical stack live status and port bindings...");
const statusRaw = execSync("docker exec cordbrief-collector cat /var/cordbrief/exchange/collector-status.json", { encoding: "utf8" });
const status = JSON.parse(statusRaw);
assert.strictEqual(status.version, 1, "Status version must be 1");
assert.strictEqual(status.collector_state, "running", "Collector state must be running");
assert.strictEqual(status.discord_authenticated, true, "Discord must be authenticated via RPC");

// Negative control: Xpra viewer must NOT be accessible during normal operation
let xpraOpen = false;
try {
    execSync("curl -s -o /dev/null -w \"%{http_code}\" http://127.0.0.1:28742/ --connect-timeout 2", { stdio: "pipe" });
    xpraOpen = true;
} catch {}
assert.strictEqual(xpraOpen, false, "Xpra port 28742 must be closed/idle during normal operation");

// Core Web UI must be healthy on port 28741
const coreOut = execSync("curl -s http://127.0.0.1:28741/", { encoding: "utf8" });
assert.ok(coreOut.includes("<title>Overview — CordBrief</title>"), "Core UI must respond on 127.0.0.1:28741");
console.log("  ✔ Live stack is authenticated, running, Core UI is serving, and Xpra is shut off.");

console.log("\n=== ALL M18 CLEAN-ROOM & RC PROOFS PASSED (100%) ===");
console.log("m18_rc_proof_passed");
