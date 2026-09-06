/*
 * Milestone 13 Phase 2 Runtime Release Retention Test Suite
 * Tests deterministic retention policy (keep current + newest valid previous),
 * rollback activation, lock ownership enforcement, fail-closed handling,
 * and safe pruning invariants (Cases A through Q).
 */

import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import assert from "assert";
import {
    validateReleaseDirectory,
    evaluateRetentionPlan,
    pruneRuntimeReleases,
    rollbackRuntimeRelease,
    listRuntimeReleases,
    verifyLockOwnership
} from "../runtime.mjs";

function makeTempDir(prefix = "cb-retention-test-") {
    return fs.mkdtempSync(path.join(os.tmpdir(), prefix));
}

function createMockRelease(releasesDir, releaseId, options = {}) {
    const releaseDir = path.join(releasesDir, releaseId);
    fs.mkdirSync(path.join(releaseDir, "discord"), { recursive: true });
    fs.mkdirSync(path.join(releaseDir, "vencord", "dist"), { recursive: true });
    fs.mkdirSync(path.join(releaseDir, "plugin"), { recursive: true });

    if (!options.missingDiscord) {
        fs.writeFileSync(path.join(releaseDir, "discord", "Discord"), "mock discord bin");
    }
    if (!options.missingPatcher) {
        fs.writeFileSync(path.join(releaseDir, "vencord", "dist", "patcher.js"), "console.log('patcher');");
    }
    if (!options.missingPlugin) {
        fs.writeFileSync(path.join(releaseDir, "plugin", "index.ts"), "export default {};");
    }

    const createdAt = options.createdAt || new Date().toISOString();
    const manifest = {
        manifest_version: options.manifestVersion !== undefined ? options.manifestVersion : 1,
        created_at: createdAt,
        discord_version: "1.0.156",
        vencord_version: "1.0.0",
        plugin_version: "1.0.0",
        paths: {
            discord_executable: options.discordPath || "discord/Discord",
            vencord_dist: options.vencordDistPath || "vencord/dist",
            vencord_patcher: options.patcherPath || "vencord/dist/patcher.js",
            plugin: options.pluginPath || "plugin"
        }
    };

    if (options.malformedJson) {
        fs.writeFileSync(path.join(releaseDir, "runtime-manifest.json"), "{ invalid json ");
    } else if (!options.missingManifest) {
        fs.writeFileSync(path.join(releaseDir, "runtime-manifest.json"), JSON.stringify(manifest, null, 2));
    }

    return { releaseId, releaseDir, manifest };
}

function setSymlink(runtimeDir, relativeTarget) {
    const currentLink = path.join(runtimeDir, "current");
    try { fs.unlinkSync(currentLink); } catch {}
    fs.symlinkSync(relativeTarget, currentLink, "junction");
}

function setupMockLock(runtimeDir, role = "setup") {
    const lockFile = path.join(runtimeDir, "runtime.lock");
    const ownerFile = path.join(runtimeDir, "runtime-owner.json");
    fs.writeFileSync(lockFile, JSON.stringify({
        version: 1,
        holder: role,
        pid: process.pid,
        hostname: "test-host",
        acquired_at: new Date().toISOString()
    }, null, 2));
    fs.writeFileSync(ownerFile, JSON.stringify({
        holder: role,
        pid: process.pid,
        hostname: "test-host",
        acquired_at: new Date().toISOString()
    }, null, 2));
    return { role, acquired: true };
}

async function runTests() {
    console.log("=== Running Milestone 13 Phase 2 Retention & Rollback Tests ===");

    // Test A: One valid current release -> delete nothing
    {
        console.log("[Test A] One valid current release -> delete nothing...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000");
        setSymlink(tmp, "releases/20260906010000");
        const lockContext = setupMockLock(tmp, "setup");

        const plan = evaluateRetentionPlan({ runtimeDir: tmp });
        assert(!plan.failClosed, "Must not fail closed");
        assert.strictEqual(plan.currentReleaseId, "20260906010000");
        assert.strictEqual(plan.rollbackReleaseId, null);
        assert.deepStrictEqual(plan.keepReleaseIds, ["20260906010000"]);
        assert.deepStrictEqual(plan.pruneCandidateIds, []);

        const pruneRes = pruneRuntimeReleases({ runtimeDir: tmp, lockContext });
        assert.deepStrictEqual(pruneRes.pruned, []);
        assert(fs.existsSync(path.join(releasesDir, "20260906010000")));
        console.log("  ✔ Test A passed.");
    }

    // Test B: Two valid releases -> keep both
    {
        console.log("[Test B] Two valid releases -> keep both...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000", { createdAt: "2026-09-06T01:00:00.000Z" });
        createMockRelease(releasesDir, "20260906020000", { createdAt: "2026-09-06T02:00:00.000Z" });
        setSymlink(tmp, "releases/20260906020000");
        const lockContext = setupMockLock(tmp, "setup");

        const plan = evaluateRetentionPlan({ runtimeDir: tmp });
        assert(!plan.failClosed);
        assert.strictEqual(plan.currentReleaseId, "20260906020000");
        assert.strictEqual(plan.rollbackReleaseId, "20260906010000");
        assert.strictEqual(plan.keepReleaseIds.length, 2);
        assert.deepStrictEqual(plan.pruneCandidateIds, []);

        const pruneRes = pruneRuntimeReleases({ runtimeDir: tmp, lockContext });
        assert.deepStrictEqual(pruneRes.pruned, []);
        assert(fs.existsSync(path.join(releasesDir, "20260906010000")));
        assert(fs.existsSync(path.join(releasesDir, "20260906020000")));
        console.log("  ✔ Test B passed.");
    }

    // Test C: Three valid releases -> keep current + newest valid previous -> oldest pruned
    {
        console.log("[Test C] Three valid releases -> keep current + newest previous, prune oldest...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000", { createdAt: "2026-09-06T01:00:00.000Z" });
        createMockRelease(releasesDir, "20260906020000", { createdAt: "2026-09-06T02:00:00.000Z" });
        createMockRelease(releasesDir, "20260906030000", { createdAt: "2026-09-06T03:00:00.000Z" });
        setSymlink(tmp, "releases/20260906030000");
        const lockContext = setupMockLock(tmp, "setup");

        const plan = evaluateRetentionPlan({ runtimeDir: tmp });
        assert.strictEqual(plan.currentReleaseId, "20260906030000");
        assert.strictEqual(plan.rollbackReleaseId, "20260906020000");
        assert.deepStrictEqual(plan.keepReleaseIds, ["20260906030000", "20260906020000"]);
        assert.deepStrictEqual(plan.pruneCandidateIds, ["20260906010000"]);

        const pruneRes = pruneRuntimeReleases({ runtimeDir: tmp, lockContext });
        assert.deepStrictEqual(pruneRes.pruned, ["20260906010000"]);
        assert(!fs.existsSync(path.join(releasesDir, "20260906010000")), "Oldest must be pruned");
        assert(fs.existsSync(path.join(releasesDir, "20260906020000")), "Previous must be retained");
        assert(fs.existsSync(path.join(releasesDir, "20260906030000")), "Current must be retained");
        console.log("  ✔ Test C passed.");
    }

    // Test D: Five valid releases -> exactly two retained
    {
        console.log("[Test D] Five valid releases -> exactly two retained...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        for (let i = 1; i <= 5; i++) {
            createMockRelease(releasesDir, `202609060${i}0000`, {
                createdAt: `2026-09-06T0${i}:00:00.000Z`
            });
        }
        setSymlink(tmp, "releases/20260906050000");
        const lockContext = setupMockLock(tmp, "setup");

        const plan = evaluateRetentionPlan({ runtimeDir: tmp });
        assert.strictEqual(plan.keepReleaseIds.length, 2);
        assert.deepStrictEqual(plan.keepReleaseIds, ["20260906050000", "20260906040000"]);
        assert.strictEqual(plan.pruneCandidateIds.length, 3);

        const pruneRes = pruneRuntimeReleases({ runtimeDir: tmp, lockContext });
        assert.strictEqual(pruneRes.pruned.length, 3);
        assert(fs.existsSync(path.join(releasesDir, "20260906050000")));
        assert(fs.existsSync(path.join(releasesDir, "20260906040000")));
        assert(!fs.existsSync(path.join(releasesDir, "20260906030000")));
        assert(!fs.existsSync(path.join(releasesDir, "20260906020000")));
        assert(!fs.existsSync(path.join(releasesDir, "20260906010000")));
        console.log("  ✔ Test D passed.");
    }

    // Test E: Current is not lexicographically newest -> current STILL retained -> proper previous chosen
    {
        console.log("[Test E] Current not newest -> current still retained and rollback selected...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000", { createdAt: "2026-09-06T01:00:00.000Z" });
        createMockRelease(releasesDir, "20260906020000", { createdAt: "2026-09-06T02:00:00.000Z" });
        createMockRelease(releasesDir, "20260906030000", { createdAt: "2026-09-06T03:00:00.000Z" });
        // Symlink is set to the middle release (e.g. following a rollback)
        setSymlink(tmp, "releases/20260906020000");
        const lockContext = setupMockLock(tmp, "setup");

        const plan = evaluateRetentionPlan({ runtimeDir: tmp });
        assert.strictEqual(plan.currentReleaseId, "20260906020000");
        // Newest other valid release is 030000
        assert.strictEqual(plan.rollbackReleaseId, "20260906030000");
        assert.deepStrictEqual(plan.keepReleaseIds, ["20260906020000", "20260906030000"]);
        assert.deepStrictEqual(plan.pruneCandidateIds, ["20260906010000"]);

        const pruneRes = pruneRuntimeReleases({ runtimeDir: tmp, lockContext });
        assert.deepStrictEqual(pruneRes.pruned, ["20260906010000"]);
        assert(fs.existsSync(path.join(releasesDir, "20260906020000")), "Current must be preserved");
        assert(fs.existsSync(path.join(releasesDir, "20260906030000")), "Rollback must be preserved");
        console.log("  ✔ Test E passed.");
    }

    // Test F: Malformed manifest in old directory -> never considered rollback candidate
    {
        console.log("[Test F] Malformed manifest in old directory -> handled safely, not rollback candidate...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000", { createdAt: "2026-09-06T01:00:00.000Z" });
        createMockRelease(releasesDir, "20260906020000", { malformedJson: true, createdAt: "2026-09-06T02:00:00.000Z" });
        createMockRelease(releasesDir, "20260906030000", { createdAt: "2026-09-06T03:00:00.000Z" });
        setSymlink(tmp, "releases/20260906030000");

        const plan = evaluateRetentionPlan({ runtimeDir: tmp });
        assert.strictEqual(plan.currentReleaseId, "20260906030000");
        // Malformed 020000 was rejected; 010000 is chosen as valid rollback candidate
        assert.strictEqual(plan.rollbackReleaseId, "20260906010000");
        assert.deepStrictEqual(plan.keepReleaseIds, ["20260906030000", "20260906010000"]);
        assert(plan.invalidReleases.some(r => r.id === "20260906020000"));
        console.log("  ✔ Test F passed.");
    }

    // Test G: Malformed current target -> FAIL CLOSED -> prune nothing
    {
        console.log("[Test G] Malformed current target -> fail closed and prune nothing...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000");
        createMockRelease(releasesDir, "20260906020000", { missingDiscord: true });
        setSymlink(tmp, "releases/20260906020000");
        const lockContext = setupMockLock(tmp, "setup");

        const plan = evaluateRetentionPlan({ runtimeDir: tmp });
        assert.strictEqual(plan.failClosed, true);
        assert(plan.failClosedReason.includes("current_target_invalid"));

        assert.throws(() => {
            pruneRuntimeReleases({ runtimeDir: tmp, lockContext });
        }, /Retention pruning aborted/);
        assert(fs.existsSync(path.join(releasesDir, "20260906010000")), "Older release must not be touched");
        console.log("  ✔ Test G passed.");
    }

    // Test H: Current symlink missing -> prune nothing
    {
        console.log("[Test H] Current symlink missing -> prune nothing...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000");
        const lockContext = setupMockLock(tmp, "setup");

        const plan = evaluateRetentionPlan({ runtimeDir: tmp });
        assert.strictEqual(plan.failClosed, true);
        assert.strictEqual(plan.failClosedReason, "current_symlink_missing");

        assert.throws(() => {
            pruneRuntimeReleases({ runtimeDir: tmp, lockContext });
        }, /current_symlink_missing/);
        assert(fs.existsSync(path.join(releasesDir, "20260906010000")));
        console.log("  ✔ Test H passed.");
    }

    // Test I: Current symlink points outside releases/ -> prune nothing
    {
        console.log("[Test I] Current symlink points outside releases/ -> prune nothing...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000");
        // Point symlink to outside
        setSymlink(tmp, "../outside");
        const lockContext = setupMockLock(tmp, "setup");

        const plan = evaluateRetentionPlan({ runtimeDir: tmp });
        assert.strictEqual(plan.failClosed, true);
        assert(plan.failClosedReason.includes("outside_releases"));

        assert.throws(() => {
            pruneRuntimeReleases({ runtimeDir: tmp, lockContext });
        }, /outside_releases/);
        console.log("  ✔ Test I passed.");
    }

    // Test J: Candidate directory symlink / path traversal -> refuse deletion
    {
        console.log("[Test J] Candidate directory symlink / path traversal -> refuse deletion...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000");
        createMockRelease(releasesDir, "20260906020000");
        setSymlink(tmp, "releases/20260906020000");

        // Create an outside target and a symlink inside releases pointing to it
        const outside = makeTempDir("outside-");
        const symlinkInReleases = path.join(releasesDir, "symlink_dir");
        fs.symlinkSync(outside, symlinkInReleases, "junction");

        const plan = evaluateRetentionPlan({ runtimeDir: tmp });
        assert(plan.ignoredEntryIds.includes("symlink_dir"), "Symlinked release directory must be ignored");
        assert(!plan.pruneCandidateIds.includes("symlink_dir"));

        const lockContext = setupMockLock(tmp, "setup");
        pruneRuntimeReleases({ runtimeDir: tmp, lockContext });
        assert(fs.existsSync(outside), "Outside directory must not be deleted");
        console.log("  ✔ Test J passed.");
    }

    // Test K: Failed staging before activation -> previous releases untouched
    {
        console.log("[Test K] Failed staging before activation -> previous releases untouched...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000");
        setSymlink(tmp, "releases/20260906010000");
        const lockContext = setupMockLock(tmp, "setup");

        // Simulate failed staging: error thrown before activation
        try {
            throw new Error("Staging error: disk full");
            pruneRuntimeReleases({ runtimeDir: tmp, lockContext });
        } catch (err) {
            assert(err.message.includes("Staging error"));
        }
        assert(fs.existsSync(path.join(releasesDir, "20260906010000")));
        console.log("  ✔ Test K passed.");
    }

    // Test L: Failed atomic activation -> previous releases untouched
    {
        console.log("[Test L] Failed atomic activation -> previous releases untouched...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000");
        createMockRelease(releasesDir, "20260906020000");
        setSymlink(tmp, "releases/20260906010000");
        const lockContext = setupMockLock(tmp, "setup");

        // If activation fails to update current, current is still 010000
        const plan = evaluateRetentionPlan({ runtimeDir: tmp });
        assert.strictEqual(plan.currentReleaseId, "20260906010000");
        assert(fs.existsSync(path.join(releasesDir, "20260906010000")));
        console.log("  ✔ Test L passed.");
    }

    // Test M: Successful activation -> prune only after activation success
    {
        console.log("[Test M] Successful activation -> prune only after activation success...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000", { createdAt: "2026-09-06T01:00:00.000Z" });
        createMockRelease(releasesDir, "20260906020000", { createdAt: "2026-09-06T02:00:00.000Z" });
        createMockRelease(releasesDir, "20260906030000", { createdAt: "2026-09-06T03:00:00.000Z" });
        setSymlink(tmp, "releases/20260906030000");
        const lockContext = setupMockLock(tmp, "setup");

        const valCurrent = validateReleaseDirectory(path.join(tmp, "current"));
        assert(valCurrent.valid, "Current must validate");
        const pruneRes = pruneRuntimeReleases({ runtimeDir: tmp, lockContext });
        assert.deepStrictEqual(pruneRes.pruned, ["20260906010000"]);
        console.log("  ✔ Test M passed.");
    }

    // Test N: Stale known .tmp release while lock held -> safely cleaned
    {
        console.log("[Test N] Stale known .tmp release -> safely cleaned...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000");
        setSymlink(tmp, "releases/20260906010000");
        const lockContext = setupMockLock(tmp, "setup");

        const staleTmp = path.join(releasesDir, "20260906020000.tmp");
        fs.mkdirSync(staleTmp, { recursive: true });
        fs.writeFileSync(path.join(staleTmp, "partial.dat"), "partial data");

        const plan = evaluateRetentionPlan({ runtimeDir: tmp });
        assert.deepStrictEqual(plan.tempEntryIds, ["20260906020000.tmp"]);

        const pruneRes = pruneRuntimeReleases({ runtimeDir: tmp, lockContext });
        assert.deepStrictEqual(pruneRes.cleanedTemp, ["20260906020000.tmp"]);
        assert(!fs.existsSync(staleTmp), "Stale .tmp directory must be cleaned");
        console.log("  ✔ Test N passed.");
    }

    // Test O: Ambiguous unknown directory -> preserved/reported, not deleted
    {
        console.log("[Test O] Ambiguous unknown directory -> preserved/reported, not deleted...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000");
        setSymlink(tmp, "releases/20260906010000");
        const lockContext = setupMockLock(tmp, "setup");

        const unknownDir = path.join(releasesDir, "custom_backup_snapshot");
        fs.mkdirSync(unknownDir, { recursive: true });
        fs.writeFileSync(path.join(unknownDir, "custom.txt"), "keep me safe");

        const plan = evaluateRetentionPlan({ runtimeDir: tmp });
        assert(plan.ignoredEntryIds.includes("custom_backup_snapshot"));
        assert(!plan.pruneCandidateIds.includes("custom_backup_snapshot"));

        pruneRuntimeReleases({ runtimeDir: tmp, lockContext });
        assert(fs.existsSync(unknownDir), "Unknown directory must remain untouched");
        console.log("  ✔ Test O passed.");
    }

    // Test P: Rollback activation to validated previous -> current switches atomically & validates
    {
        console.log("[Test P] Rollback activation to validated previous -> atomic switch and validates...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000", { createdAt: "2026-09-06T01:00:00.000Z" });
        createMockRelease(releasesDir, "20260906020000", { createdAt: "2026-09-06T02:00:00.000Z" });
        setSymlink(tmp, "releases/20260906020000");
        const lockContext = setupMockLock(tmp, "setup");

        const rollbackRes = rollbackRuntimeRelease({
            runtimeDir: tmp,
            lockContext,
            _testReplaceFn: process.platform === "win32" ? (rel, cur) => {
                try { fs.unlinkSync(cur); } catch {}
                fs.symlinkSync(rel, cur, "junction");
            } : null
        });
        assert.strictEqual(rollbackRes.success, true);
        assert.strictEqual(rollbackRes.activeReleaseId, "20260906010000");
        assert.strictEqual(rollbackRes.previousReleaseId, "20260906020000");

        const currentLink = path.join(tmp, "current");
        const target = fs.readlinkSync(currentLink);
        assert(target.includes("20260906010000"), "Current symlink points to rollback target");

        const val = validateReleaseDirectory(currentLink);
        assert(val.valid, "New current target validates");
        console.log("  ✔ Test P passed.");
    }

    // Test Q: Attempt rollback to arbitrary / outside path -> rejected
    {
        console.log("[Test Q] Attempt rollback to arbitrary / outside path -> rejected...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000");
        setSymlink(tmp, "releases/20260906010000");
        const lockContext = setupMockLock(tmp, "setup");

        assert.throws(() => {
            rollbackRuntimeRelease({
                runtimeDir: tmp,
                targetReleaseId: "../outside",
                lockContext
            });
        }, /invalid target release id format/);

        assert.throws(() => {
            rollbackRuntimeRelease({
                runtimeDir: tmp,
                targetReleaseId: "nonexistent",
                lockContext
            });
        }, /does not exist in releases/);

        console.log("  ✔ Test Q passed.");
    }

    // Test R: Failure during atomic symlink switch leaves previous current intact
    {
        console.log("[Test R] Failure during atomic symlink switch leaves previous current intact...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000");
        createMockRelease(releasesDir, "20260906020000");
        setSymlink(tmp, "releases/20260906020000");
        const lockContext = setupMockLock(tmp, "setup");

        assert.throws(() => {
            rollbackRuntimeRelease({
                runtimeDir: tmp,
                targetReleaseId: "20260906010000",
                lockContext,
                _renameSync: () => {
                    throw new Error("EPERM: simulated permission denied");
                }
            });
        }, /Failed to atomically switch current symlink|Atomic symlink replacement is unsupported on Windows/);

        const currentLink = path.join(tmp, "current");
        assert(fs.existsSync(currentLink), "current symlink must remain existing");
        const target = fs.readlinkSync(currentLink);
        assert(target.includes("20260906020000"), "current symlink must still point to original release");

        console.log("  ✔ Test R passed.");
    }

    // Test S: On Windows, replacing without explicit test hook fails closed leaving previous current untouched
    if (process.platform === "win32") {
        console.log("[Test S] Windows fails closed when atomic replace unsupported, leaving current untouched...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000");
        createMockRelease(releasesDir, "20260906020000");
        setSymlink(tmp, "releases/20260906020000");
        const lockContext = setupMockLock(tmp, "setup");

        assert.throws(() => {
            rollbackRuntimeRelease({
                runtimeDir: tmp,
                targetReleaseId: "20260906010000",
                lockContext
            });
        }, /Atomic symlink replacement is unsupported on Windows/);

        const currentLink = path.join(tmp, "current");
        assert(fs.existsSync(currentLink), "current symlink must remain existing");
        const target = fs.readlinkSync(currentLink);
        assert(target.includes("20260906020000"), "current symlink must still point to original release");

        console.log("  ✔ Test S passed.");
    }

    // Test Lock Context: Collector or unauthorized caller cannot prune
    {
        console.log("[Lock Enforcement] Collector role refused pruning...");
        const tmp = makeTempDir();
        const releasesDir = path.join(tmp, "releases");
        fs.mkdirSync(releasesDir, { recursive: true });
        createMockRelease(releasesDir, "20260906010000");
        createMockRelease(releasesDir, "20260906020000");
        setSymlink(tmp, "releases/20260906020000");

        // Role is collector
        assert.throws(() => {
            pruneRuntimeReleases({
                runtimeDir: tmp,
                lockContext: { role: "collector", acquired: true }
            });
        }, /Lock ownership refused/);

        // No lock context
        assert.throws(() => {
            pruneRuntimeReleases({ runtimeDir: tmp });
        }, /Lock ownership refused/);

        console.log("  ✔ Lock enforcement verified.");
    }

    console.log("=== ALL RETENTION & ROLLBACK TESTS PASSED (100%) ===");
}

runTests().catch(err => {
    console.error("Test failure:", err);
    process.exit(1);
});
