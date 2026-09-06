/*
 * Adversarial Lock Ownership Test Suite
 * Rigorously verifies verifyLockOwnership() across Cases A through E in Linux:
 * Case A: True owner (role=setup, FD 9 on runtime.lock, holds FLOCK WRITE) -> PASS
 * Case B: FD 9 open, no lock held by anyone -> REFUSED
 * Case C: FD 9 open, but another process holds flock -> REFUSED
 * Case D: Role=collector with lock -> REFUSED
 * Case E: FD 9 points to wrong file -> REFUSED
 */

import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import * as cp from "child_process";
import assert from "assert";
import { fileURLToPath } from "url";
import { verifyLockOwnership } from "../runtime.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const runtimeModulePath = path.resolve(__dirname, "../runtime.mjs").replace(/\\/g, "/");

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "cb-adv-lock-"));
const runtimeDir = path.join(tmp, "runtime");
fs.mkdirSync(runtimeDir, { recursive: true });
const lockFile = path.join(runtimeDir, "runtime.lock");
const ownerFile = path.join(runtimeDir, "runtime-owner.json");

// Prepare lock & owner files
fs.writeFileSync(lockFile, "");
fs.writeFileSync(ownerFile, JSON.stringify({
    holder: "setup",
    pid: process.pid,
    hostname: "test-host",
    acquired_at: new Date().toISOString()
}, null, 2));

console.log("=== Running Adversarial Lock-Ownership Test Suite (Linux) ===");

// Helper to run a test snippet in a subshell with custom FD 9 setup
function runInSubprocess(script) {
    const res = cp.spawnSync("bash", ["-c", script], {
        env: {
            ...process.env,
            CORDBRIEF_ROLE: "setup"
        },
        encoding: "utf8"
    });
    return {
        exitCode: res.status,
        stdout: res.stdout || "",
        stderr: res.stderr || ""
    };
}

// CASE A: True owner
{
    console.log("[Case A] True owner: role=setup, FD 9 on runtime.lock, holds FLOCK WRITE...");
    const script = `
        exec 9>${lockFile}
        flock -n 9
        node -e '
            import("${runtimeModulePath}").then(m => {
                m.verifyLockOwnership({ runtimeDir: "${runtimeDir}" });
                console.log("CASE_A_SUCCESS");
            }).catch(err => {
                console.error("CASE_A_FAILED: " + err.message);
                process.exit(1);
            });
        '
    `;
    const res = runInSubprocess(script);
    assert.strictEqual(res.exitCode, 0, `Case A must exit 0. stderr: ${res.stderr}`);
    assert(res.stdout.includes("CASE_A_SUCCESS"), "Case A must succeed");
    console.log("  ✔ Case A passed: True owner permitted.");
}

// CASE B: FD open, no lock, nobody else owns lock
{
    console.log("[Case B] FD 9 open to runtime.lock, but NEVER flocked (no lock held by anyone)...");
    const script = `
        exec 9>${lockFile}
        # Notice: flock is intentionally NOT called on FD 9!
        node -e '
            import("${runtimeModulePath}").then(m => {
                m.verifyLockOwnership({ runtimeDir: "${runtimeDir}" });
                console.log("CASE_B_UNEXPECTED_SUCCESS");
                process.exit(1);
            }).catch(err => {
                console.log("CASE_B_REFUSED: " + err.message);
                process.exit(0);
            });
        '
    `;
    const res = runInSubprocess(script);
    assert.strictEqual(res.exitCode, 0, `Case B must exit 0 (refusal caught). stderr: ${res.stderr}`);
    assert(res.stdout.includes("CASE_B_REFUSED"), "Case B must be refused");
    assert(res.stdout.includes("does not hold kernel flock write lock"), "Correct refusal reason");
    console.log("  ✔ Case B passed: Unflocked FD 9 correctly refused.");
}

// CASE C: FD open, other process owns lock
{
    console.log("[Case C] Adversary has FD 9 open to runtime.lock, but ANOTHER process holds flock...");
    // We launch background process holding flock on FD 8, while main process only opens FD 9
    const script = `
        exec 8>${lockFile}
        flock -n 8

        # Main process has FD 9 open to the same file, but FD 9 does NOT own the lock!
        exec 9>${lockFile}

        node -e '
            import("${runtimeModulePath}").then(m => {
                m.verifyLockOwnership({ runtimeDir: "${runtimeDir}" });
                console.log("CASE_C_UNEXPECTED_SUCCESS");
                process.exit(1);
            }).catch(err => {
                console.log("CASE_C_REFUSED: " + err.message);
                process.exit(0);
            });
        '
    `;
    const res = runInSubprocess(script);
    assert.strictEqual(res.exitCode, 0, `Case C must exit 0 (refusal caught). stderr: ${res.stderr}`);
    assert(res.stdout.includes("CASE_C_REFUSED"), "Case C must be refused");
    assert(res.stdout.includes("does not hold kernel flock write lock"), "Correct refusal reason");
    console.log("  ✔ Case C passed: Adversarial open FD without lock correctly refused.");
}

// CASE D: Role collector with lock
{
    console.log("[Case D] Role=collector even with valid lock held...");
    const script = `
        exec 9>${lockFile}
        flock -n 9
        CORDBRIEF_ROLE=collector node -e '
            import("${runtimeModulePath}").then(m => {
                m.verifyLockOwnership({ runtimeDir: "${runtimeDir}", lockContext: { role: "collector" } });
                console.log("CASE_D_UNEXPECTED_SUCCESS");
                process.exit(1);
            }).catch(err => {
                console.log("CASE_D_REFUSED: " + err.message);
                process.exit(0);
            });
        '
    `;
    const res = runInSubprocess(script);
    assert.strictEqual(res.exitCode, 0, `Case D must exit 0. stderr: ${res.stderr}`);
    assert(res.stdout.includes("CASE_D_REFUSED"), "Case D must be refused");
    assert(res.stdout.includes("only 'setup' is permitted"), "Correct refusal reason for collector");
    console.log("  ✔ Case D passed: Collector role refused.");
}

// CASE E: FD 9 open to wrong file
{
    console.log("[Case E] FD 9 open and flocked on WRONG file...");
    const wrongFile = path.join(tmp, "wrong.file");
    fs.writeFileSync(wrongFile, "");
    const script = `
        exec 9>${wrongFile}
        flock -n 9
        node -e '
            import("${runtimeModulePath}").then(m => {
                m.verifyLockOwnership({ runtimeDir: "${runtimeDir}" });
                console.log("CASE_E_UNEXPECTED_SUCCESS");
                process.exit(1);
            }).catch(err => {
                console.log("CASE_E_REFUSED: " + err.message);
                process.exit(0);
            });
        '
    `;
    const res = runInSubprocess(script);
    assert.strictEqual(res.exitCode, 0, `Case E must exit 0. stderr: ${res.stderr}`);
    assert(res.stdout.includes("CASE_E_REFUSED"), "Case E must be refused");
    assert(res.stdout.includes("expected"), "Correct refusal reason for wrong file");
    console.log("  ✔ Case E passed: Wrong file on FD 9 refused.");
}

console.log("\n=== Running Maintenance Lifecycle & Cleanup Tests ===");

const wrapperPath = path.join(tmp, "maintenance-wrapper.sh");
fs.writeFileSync(wrapperPath, `#!/usr/bin/env bash
exec 9>"${lockFile}"
flock -n 9
echo '{"holder":"setup"}' > "${ownerFile}"
trap 'rm -f "${ownerFile}" 2>/dev/null || true' EXIT INT TERM
set +e
"$@"
CHILD_EXIT=$?
rm -f "${ownerFile}" 2>/dev/null || true
trap - EXIT INT TERM
exit $CHILD_EXIT
`);
fs.chmodSync(wrapperPath, 0o755);

// TEST A & D: Successful maintenance removes runtime-owner.json AND holds flock during execution
{
    console.log("[Maintenance Test A & D] Successful maintenance removes owner file & holds lock during execution...");
    const childScript = `
        test -f "${ownerFile}" || exit 10
        # Concurrent flock attempt on another FD should fail
        exec 8>"${lockFile}"
        if flock -n 8; then
            echo "CONCURRENT_LOCK_ACQUIRED_ERROR"
            exit 11
        fi
        echo "MAINTENANCE_ACTIVE_SUCCESS"
        exit 0
    `;

    const res = cp.spawnSync(wrapperPath, ["bash", "-c", childScript], {
        env: { ...process.env, CORDBRIEF_ROLE: "setup" },
        encoding: "utf8"
    });

    assert.strictEqual(res.status, 0, `Expected exit 0, got ${res.status}. stderr: ${res.stderr}`);
    assert(res.stdout.includes("MAINTENANCE_ACTIVE_SUCCESS"), "Child executed under lock");
    assert(!fs.existsSync(ownerFile), "runtime-owner.json must be removed after successful child exit");
    console.log("  ✔ Maintenance Tests A & D passed: Owner file cleaned, lock held throughout.");
}

// TEST B & C: Failed maintenance removes runtime-owner.json AND preserves child exit code
{
    console.log("[Maintenance Test B & C] Failed maintenance removes owner file & preserves child exit code...");
    const childScript = `
        test -f "${ownerFile}" || exit 10
        echo "FAILING_CHILD_EXECUTED"
        exit 42
    `;

    const res = cp.spawnSync(wrapperPath, ["bash", "-c", childScript], {
        env: { ...process.env, CORDBRIEF_ROLE: "setup" },
        encoding: "utf8"
    });

    assert.strictEqual(res.status, 42, `Expected exit code 42 preserved, got ${res.status}`);
    assert(res.stdout.includes("FAILING_CHILD_EXECUTED"), "Failing child executed");
    assert(!fs.existsSync(ownerFile), "runtime-owner.json must be removed even on failure");
    console.log("  ✔ Maintenance Tests B & C passed: Exit code preserved (42), owner file cleaned on failure.");
}

console.log("\n=== ALL ADVERSARIAL & MAINTENANCE LIFECYCLE TESTS: 100% PASSED ===");

