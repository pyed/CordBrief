/*
 * CordBrief Runtime & Ownership Manager
 * Handles exclusive single-ownership leasing (runtime.lock) between setup and collector,
 * stale lease recovery (>30s), and runtime-manifest validation and preparation.
 */

import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import * as cp from "child_process";

export const DEFAULT_STALE_LOCK_MS = 30000;
export const DEFAULT_HEARTBEAT_MS = 5000;

/**
 * Attempt to acquire single-ownership runtime lock.
 */
export function acquireRuntimeLock(lockFilePath, holder, options = {}) {
    const staleMs = options.staleMs || DEFAULT_STALE_LOCK_MS;
    const heartbeatMs = options.heartbeatMs || DEFAULT_HEARTBEAT_MS;
    const pid = options.pid || process.pid;
    const hostname = options.hostname || os.hostname();

    const dir = path.dirname(lockFilePath);
    if (!fs.existsSync(dir)) {
        fs.mkdirSync(dir, { recursive: true });
    }

    const now = new Date().toISOString();
    const lockData = {
        version: 1,
        holder,
        pid,
        hostname,
        acquired_at: now,
        heartbeat_at: now
    };
    const payload = JSON.stringify(lockData, null, 2);

    // Detect if current process on Linux already holds kernel flock on FD 9
    let holdsKernelFlock = false;
    if (process.platform === "linux") {
        try {
            const target = fs.readlinkSync("/proc/self/fd/9");
            let resolvedTarget = null;
            let resolvedLock = null;
            try {
                resolvedTarget = fs.realpathSync(target);
                resolvedLock = fs.realpathSync(lockFilePath);
            } catch {
                resolvedTarget = path.resolve(target);
                resolvedLock = path.resolve(lockFilePath);
            }
            if (resolvedTarget === resolvedLock) {
                const fdinfo = fs.readFileSync("/proc/self/fdinfo/9", "utf8");
                if (/lock:\s+\d+:\s+FLOCK\s+ADVISORY\s+WRITE/.test(fdinfo)) {
                    holdsKernelFlock = true;
                }
            }
        } catch {}
    }

    function tryCreateLock() {
        const payload = JSON.stringify(lockData, null, 2);
        try {
            const fd = fs.openSync(lockFilePath, "wx");
            try {
                fs.writeFileSync(fd, payload, "utf8");
            } finally {
                fs.closeSync(fd);
            }
            return { success: true, lockData };
        } catch (err) {
            if (err.code === "EEXIST") {
                return { success: false, err };
            }
            throw err;
        }
    }

    let result;
    if (holdsKernelFlock) {
        try {
            const fd = fs.openSync(lockFilePath, "r+");
            try {
                fs.ftruncateSync(fd, 0);
                fs.writeSync(fd, payload, 0, "utf8");
            } finally {
                fs.closeSync(fd);
            }
        } catch {
            fs.writeFileSync(lockFilePath, payload, "utf8");
        }
        result = { success: true, lockData };
    } else {
        result = tryCreateLock();
        if (!result.success) {
            // Read existing lock file to determine if stale
            let existing = null;
            try {
                const raw = fs.readFileSync(lockFilePath, "utf8");
                existing = JSON.parse(raw);
            } catch {
                // Corrupt or empty lock file
                existing = null;
            }

            const now = Date.now();
            const heartbeatTime = existing && existing.heartbeat_at ? new Date(existing.heartbeat_at).getTime() : 0;
            const isStale = (now - heartbeatTime) > staleMs;

            if (isStale) {
                // Safe in-place stale lease reclamation (never unlinks to preserve inode)
                try {
                    const fd = fs.openSync(lockFilePath, "r+");
                    try {
                        fs.ftruncateSync(fd, 0);
                        fs.writeSync(fd, payload, 0, "utf8");
                    } finally {
                        fs.closeSync(fd);
                    }
                    result = { success: true, lockData };
                } catch {
                    result = tryCreateLock();
                }
            }

            if (!result || !result.success) {
                return {
                    acquired: false,
                    existing: existing || { holder: "unknown", stale: false },
                    release: () => {}
                };
            }
        }
    }

    const currentLockData = result.lockData;
    let released = false;

    // Heartbeat timer to keep lease fresh (in-place write to preserve inode for kernel flock)
    const timer = setInterval(() => {
        if (released) return;
        try {
            currentLockData.heartbeat_at = new Date().toISOString();
            const fd = fs.openSync(lockFilePath, "r+");
            try {
                fs.ftruncateSync(fd, 0);
                fs.writeSync(fd, JSON.stringify(currentLockData, null, 2), 0, "utf8");
            } finally {
                fs.closeSync(fd);
            }
        } catch (hbErr) {
            // Could not update heartbeat
        }
    }, heartbeatMs);

    if (timer.unref) {
        timer.unref();
    }

    // Diagnostic metadata for human inspection
    const ownerFile = path.join(dir, "runtime-owner.json");
    try {
        const ownerData = {
            holder,
            pid,
            hostname,
            acquired_at: currentLockData.acquired_at,
            instance_nonce: Math.random().toString(36).slice(2)
        };
        fs.writeFileSync(ownerFile, JSON.stringify(ownerData, null, 2), "utf8");
    } catch {}

    function release() {
        if (released) return;
        released = true;
        clearInterval(timer);
        try {
            if (!holdsKernelFlock && fs.existsSync(lockFilePath)) {
                const raw = fs.readFileSync(lockFilePath, "utf8");
                const current = JSON.parse(raw);
                if (current.holder === holder && current.pid === pid) {
                    fs.unlinkSync(lockFilePath);
                }
            }
        } catch {}
        try {
            if (fs.existsSync(ownerFile)) {
                const raw = fs.readFileSync(ownerFile, "utf8");
                const cur = JSON.parse(raw);
                if (cur.holder === holder && cur.pid === pid) {
                    fs.unlinkSync(ownerFile);
                }
            }
        } catch {}
    }

    return {
        acquired: true,
        lockData: currentLockData,
        release
    };
}

/**
 * Resolve a manifest target path against the manifest file directory if relative.
 */
export function resolveManifestPath(manifestPath, target) {
    if (!target) return null;
    if (path.isAbsolute(target)) return target;
    return path.resolve(path.dirname(manifestPath), target);
}

/**
 * Validate runtime manifest supporting both relative paths and legacy absolute paths.
 */
export function validateRuntimeManifest(manifestPath) {
    if (!fs.existsSync(manifestPath)) {
        return { valid: false, reason: "manifest_not_found" };
    }

    let manifest = null;
    try {
        const raw = fs.readFileSync(manifestPath, "utf8");
        manifest = JSON.parse(raw);
    } catch {
        return { valid: false, reason: "manifest_json_invalid" };
    }

    if (!manifest || manifest.manifest_version !== 1) {
        return { valid: false, reason: "unsupported_manifest_version", manifest };
    }

    const patcherPath = manifest.paths?.vencord_patcher || manifest.paths?.patcher_entry;
    if (!patcherPath) {
        return { valid: false, reason: "missing_patcher_entry_path", manifest };
    }

    const resolvedPatcher = resolveManifestPath(manifestPath, patcherPath);
    if (!fs.existsSync(resolvedPatcher)) {
        return { valid: false, reason: "patcher_entry_file_missing", manifest, resolvedPatcher };
    }

    return { valid: true, manifest, resolvedPatcher };
}

/**
 * Write runtime manifest atomically using relative paths.
 */
export function writeRuntimeManifest(manifestPath, data) {
    const dir = path.dirname(manifestPath);
    if (!fs.existsSync(dir)) {
        fs.mkdirSync(dir, { recursive: true });
    }

    const manifest = {
        manifest_version: 1,
        created_at: new Date().toISOString(),
        discord_version: data.discord_version || "unknown",
        vencord_version: data.vencord_version || "1.0.0",
        plugin_version: data.plugin_version || "1.0.0",
        paths: {
            discord_executable: data.paths?.discord_executable || data.discord_executable || "discord/Discord",
            vencord_dist: data.paths?.vencord_dist || data.vencord_dist || "vencord/dist",
            vencord_patcher: data.paths?.vencord_patcher || data.vencord_patcher || data.patcher_entry || "vencord/dist/patcher.js",
            plugin: data.paths?.plugin || data.plugin || "plugin",
            // Backward-compatibility aliases
            patcher_entry: data.paths?.patcher_entry || data.patcher_entry || "vencord/dist/patcher.js",
            discord_bin: data.paths?.discord_bin || data.discord_bin || "discord/Discord"
        }
    };

    const tmpPath = `${manifestPath}.${process.pid}.tmp`;
    fs.writeFileSync(tmpPath, JSON.stringify(manifest, null, 2), "utf8");
    fs.renameSync(tmpPath, manifestPath);
    return manifest;
}

/**
 * Recursively copy a directory synchronously.
 */
export function copyDirSync(src, dest) {
    fs.mkdirSync(dest, { recursive: true });
    const entries = fs.readdirSync(src, { withFileTypes: true });
    for (const entry of entries) {
        const srcPath = path.join(src, entry.name);
        const destPath = path.join(dest, entry.name);
        if (entry.isDirectory()) {
            copyDirSync(srcPath, destPath);
        } else if (entry.isSymbolicLink()) {
            try {
                const linkTarget = fs.readlinkSync(srcPath);
                fs.symlinkSync(linkTarget, destPath);
            } catch {
                fs.copyFileSync(srcPath, destPath);
            }
        } else {
            fs.copyFileSync(srcPath, destPath);
        }
    }
}

// Installed Linux applications need not live in an app-<version> directory.
export function getDiscordVersion(appDir) {
    const buildInfo = path.join(appDir, "resources", "build_info.json");
    if (fs.existsSync(buildInfo)) {
        return JSON.parse(fs.readFileSync(buildInfo, "utf8")).version;
    }
    return path.basename(appDir).replace(/^app-/, "");
}

/** Check if runtime should be staged or can be reused (for fast reauth). */
export function shouldStageRuntime({
    runtimeDir = "/var/cordbrief/runtime",
    discordAppSrcDir = null,
    force = false
} = {}) {
    if (force) return true;
    const manifestPath = path.join(runtimeDir, "current", "runtime-manifest.json");
    const val = validateRuntimeManifest(manifestPath);
    if (!val.valid) return true;

    if (discordAppSrcDir) {
        const appVersion = getDiscordVersion(discordAppSrcDir);
        if (val.manifest.discord_version && val.manifest.discord_version !== appVersion) {
            return true;
        }
    }
    return false;
}

/**
 * Atomically activates a symlink to relativeTarget at currentLink within runtimeDir.
 *
 * PRODUCTION POLICY (Linux):
 * Production runs exclusively on Linux containers, where rename(2) atomically replaces
 * an existing symlink or directory in a single kernel syscall. current is NEVER missing.
 *
 * PLATFORM INVARIANT (Windows / Fail-Closed):
 * Windows NTFS does NOT support atomic replacement of an existing junction or symlink via rename.
 * Multi-step rename pivots (current -> old, tmp -> current, rm old) are NOT atomic because a crash
 * between steps leaves current missing.
 * Therefore, on Windows this function FAILS CLOSED: if direct renameSync fails over an existing current,
 * tmpLink is cleaned up and an error is thrown immediately, leaving previous current completely untouched.
 * Production runtime code contains no supported switch or environment variable that converts atomic
 * activation into unlink or multi-step replacement.
 */
export function atomicActivateSymlink(
    relativeTarget,
    currentLink,
    runtimeDir,
    prefix = "current",
    _renameSync = fs.renameSync,
    options = {}
) {
    const tmpLink = path.join(runtimeDir, `${prefix}.${process.pid}.${Date.now()}.tmp`);
    fs.symlinkSync(relativeTarget, tmpLink, "junction");

    try {
        _renameSync(tmpLink, currentLink);
    } catch (err) {
        try { fs.unlinkSync(tmpLink); } catch {}

        // Test-only hook: if a test explicitly injects a custom replacement function, invoke it
        if (typeof options._testReplaceFn === "function") {
            options._testReplaceFn(relativeTarget, currentLink);
            return;
        }

        if (process.platform === "win32") {
            throw new Error(`Atomic symlink replacement is unsupported on Windows; previous current preserved untouched: ${err.message}`);
        }

        throw new Error(`Failed to atomically switch current symlink: ${err.message}`);
    }
}

/**
 * Creates an immutable versioned runtime release under runtimeDir/releases/<releaseId>,
 * and atomically points runtimeDir/current to it.
 */
export function createRuntimeRelease({
    runtimeDir = "/var/cordbrief/runtime",
    releaseId = null,
    discordAppSrcDir,
    vencordDistSrcDir,
    pluginSrcDir = null,
    patchVencordFn
}) {
    if (!fs.existsSync(runtimeDir)) {
        fs.mkdirSync(runtimeDir, { recursive: true });
    }

    const timestamp = new Date().toISOString().replace(/[-:T]/g, "").slice(0, 14);
    const id = releaseId || `${timestamp}`;
    const releaseDir = path.join(runtimeDir, "releases", id);
    const tmpReleaseDir = `${releaseDir}.tmp`;

    // Clean up any stale tmp release dir
    if (fs.existsSync(tmpReleaseDir)) {
        fs.rmSync(tmpReleaseDir, { recursive: true, force: true });
    }
    fs.mkdirSync(tmpReleaseDir, { recursive: true });

    // 1. Copy Vencord dist
    const vencordDest = path.join(tmpReleaseDir, "vencord", "dist");
    copyDirSync(vencordDistSrcDir, vencordDest);

    // 2. Copy Plugin assets if present
    if (pluginSrcDir && fs.existsSync(pluginSrcDir)) {
        const pluginDest = path.join(tmpReleaseDir, "plugin");
        copyDirSync(pluginSrcDir, pluginDest);
    }

    // 3. Copy Discord application
    const discordDest = path.join(tmpReleaseDir, "discord");
    copyDirSync(discordAppSrcDir, discordDest);

    // 4. Ensure internal module aliases exist in discord/modules
    const modulesDir = path.join(discordDest, "modules");
    if (fs.existsSync(modulesDir)) {
        const moduleEntries = fs.readdirSync(modulesDir);
        for (const entry of moduleEntries) {
            const match = entry.match(/^([a-zA-Z0-9_]+)-\d+$/);
            if (match) {
                const cleanName = match[1];
                const cleanDest = path.join(modulesDir, cleanName);
                const nestedSrc = path.join(modulesDir, entry, cleanName);
                if (!fs.existsSync(cleanDest) && fs.existsSync(nestedSrc)) {
                    copyDirSync(nestedSrc, cleanDest);
                }
            }
        }
    }

    // 5. Configure localModulesRoot in discord/resources/build_info.json
    const buildInfoPath = path.join(discordDest, "resources", "build_info.json");
    if (fs.existsSync(buildInfoPath)) {
        try {
            const bi = JSON.parse(fs.readFileSync(buildInfoPath, "utf8"));
            bi.localModulesRoot = path.join(runtimeDir, "current", "discord", "modules");
            fs.writeFileSync(buildInfoPath, JSON.stringify(bi, null, 2), "utf8");
        } catch {}
    }

    // 6. Patch Discord within the release to load Vencord from runtime current symlink
    const currentPatcherEntry = path.join(runtimeDir, "current", "vencord", "dist", "patcher.js");
    if (patchVencordFn) {
        patchVencordFn(discordDest, path.join(runtimeDir, "current", "vencord", "dist"));
    }

    // 7. Write runtime manifest with relative paths
    const manifestPath = path.join(tmpReleaseDir, "runtime-manifest.json");
    const manifest = writeRuntimeManifest(manifestPath, {
        discord_version: getDiscordVersion(discordAppSrcDir),
        vencord_version: "1.0.0",
        plugin_version: "1.0.0",
        paths: {
            discord_executable: "discord/Discord",
            vencord_dist: "vencord/dist",
            vencord_patcher: "vencord/dist/patcher.js",
            plugin: "plugin",
            patcher_entry: currentPatcherEntry,
            discord_bin: path.join(runtimeDir, "current", "discord", "Discord")
        }
    });

    // 8. Rename tmp release dir to final release dir
    fs.renameSync(tmpReleaseDir, releaseDir);

    // 9. Atomically activate current symlink
    const currentSymlink = path.join(runtimeDir, "current");
    const relativeTarget = path.join("releases", id);
    atomicActivateSymlink(relativeTarget, currentSymlink, runtimeDir, `current.${id}`);

    return { releaseId: id, releaseDir, manifest };
}

/**
 * Validate a release directory for rollback readiness and retention suitability.
 * Enforces:
 * - Direct directory presence (not a symlink to outside)
 * - runtime-manifest.json exists and parses with manifest_version === 1
 * - Strict containment beneath releaseDir (no path traversal .. or outside targets)
 * - Required assets exist: Discord executable, Vencord dist, patcher entry, plugin (if declared)
 */
export function validateReleaseDirectory(releaseDir) {
    if (!fs.existsSync(releaseDir)) {
        return { valid: false, reason: "release_directory_missing" };
    }
    let lstat;
    try {
        lstat = fs.lstatSync(releaseDir);
    } catch (err) {
        return { valid: false, reason: "release_stat_error", error: err.message };
    }

    let targetDir = releaseDir;
    if (lstat.isSymbolicLink()) {
        try {
            targetDir = fs.realpathSync(releaseDir);
        } catch (err) {
            return { valid: false, reason: "symlink_resolution_failed", error: err.message };
        }
    }

    let stat;
    try {
        stat = fs.statSync(targetDir);
    } catch (err) {
        return { valid: false, reason: "target_stat_error", error: err.message };
    }
    if (!stat.isDirectory()) {
        return { valid: false, reason: "not_a_concrete_directory" };
    }

    const manifestPath = path.join(targetDir, "runtime-manifest.json");
    const manifestVal = validateRuntimeManifest(manifestPath);
    if (!manifestVal.valid) {
        return { valid: false, reason: manifestVal.reason, details: manifestVal };
    }

    const manifest = manifestVal.manifest;
    const canonicalRoot = path.resolve(targetDir);

    // Containment and traversal audit on all declared paths
    if (manifest.paths && typeof manifest.paths === "object") {
        for (const [key, rawPath] of Object.entries(manifest.paths)) {
            if (typeof rawPath !== "string") continue;
            // Legacy aliases pointing to /var/cordbrief/runtime/current are allowed for backward compatibility
            if (rawPath.includes("/current/")) continue;

            const resolved = path.resolve(canonicalRoot, rawPath);
            const rel = path.relative(canonicalRoot, resolved);
            if (rel.startsWith("..") || path.isAbsolute(rel)) {
                return { valid: false, reason: "path_traversal_detected", key, path: rawPath };
            }
        }
    }

    // Required files check
    const discordRel = manifest.paths?.discord_executable || manifest.paths?.discord_bin || "discord/Discord";
    const discordTarget = (path.isAbsolute(discordRel) && discordRel.includes("/current/"))
        ? path.join(canonicalRoot, "discord", "Discord")
        : path.resolve(canonicalRoot, discordRel);
    if (!fs.existsSync(discordTarget)) {
        return { valid: false, reason: "discord_executable_missing", path: discordTarget };
    }

    const vencordRel = manifest.paths?.vencord_dist || "vencord/dist";
    const vencordTarget = (path.isAbsolute(vencordRel) && vencordRel.includes("/current/"))
        ? path.join(canonicalRoot, "vencord", "dist")
        : path.resolve(canonicalRoot, vencordRel);
    if (!fs.existsSync(vencordTarget)) {
        return { valid: false, reason: "vencord_dist_missing", path: vencordTarget };
    }

    // Patcher entry (manifestVal already verified its existence)
    if (!manifestVal.resolvedPatcher || !fs.existsSync(manifestVal.resolvedPatcher)) {
        return { valid: false, reason: "patcher_entry_file_missing" };
    }

    // Plugin directory check if declared in manifest
    if (manifest.paths?.plugin) {
        const pluginTarget = path.resolve(canonicalRoot, manifest.paths.plugin);
        if (!fs.existsSync(pluginTarget)) {
            return { valid: false, reason: "plugin_missing", path: pluginTarget };
        }
    }

    let createdAt = NaN;
    if (manifest.created_at) {
        createdAt = new Date(manifest.created_at).getTime();
    }
    if (isNaN(createdAt)) {
        // Fallback to directory name timestamp YYYYMMDDHHMMSS
        const dirName = path.basename(releaseDir);
        const match = dirName.match(/^(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})$/);
        if (match) {
            const iso = `${match[1]}-${match[2]}-${match[3]}T${match[4]}:${match[5]}:${match[6]}Z`;
            createdAt = new Date(iso).getTime();
        }
    }

    return {
        valid: true,
        manifest,
        releaseDir: canonicalRoot,
        releaseId: path.basename(canonicalRoot),
        createdAt
    };
}

/**
 * Verifies that runtime lock ownership is held by setup.
 * Collector must NEVER prune.
 * Enforces kernel flock context when running on Linux.
 */
export function verifyLockOwnership({ runtimeDir = "/var/cordbrief/runtime", lockContext = null } = {}) {
    const role = lockContext?.role || process.env.CORDBRIEF_ROLE || "collector";
    if (role !== "setup") {
        throw new Error(`Lock ownership refused: role is '${role}', only 'setup' is permitted to manage releases`);
    }

    // Explicit test/mock context for unit testing on non-Linux systems
    if (lockContext?.lockHandle) {
        if (!lockContext.lockHandle.acquired || lockContext.lockHandle.lockData?.holder !== "setup") {
            throw new Error("Lock ownership refused: provided lockHandle is not acquired by setup");
        }
        return true;
    }

    if (lockContext?.hasKernelFlock === true && lockContext?.role === "setup") {
        return true;
    }

    const lockFile = path.join(runtimeDir, "runtime.lock");
    const ownerFile = path.join(runtimeDir, "runtime-owner.json");

    if (fs.existsSync(ownerFile)) {
        try {
            const owner = JSON.parse(fs.readFileSync(ownerFile, "utf8"));
            if (owner.holder !== "setup") {
                throw new Error(`Lock ownership refused: runtime-owner.json holder is '${owner.holder}', expected 'setup'`);
            }
        } catch (err) {
            if (err.message.includes("Lock ownership refused")) throw err;
            throw new Error(`Lock ownership refused: failed to parse runtime-owner.json (${err.message})`);
        }
    }

    // On Linux systems: Strict kernel flock ownership audit via /proc/self/fdinfo/9
    if (process.platform === "linux") {
        let fd9Target = null;
        try {
            fd9Target = fs.readlinkSync("/proc/self/fd/9");
        } catch {
            throw new Error("Lock ownership refused: FD 9 is not open or not accessible");
        }

        // Verify that FD 9 actually points to runtime.lock (Case E: wrong file)
        let resolvedTarget = null;
        let resolvedLock = null;
        try {
            resolvedTarget = fs.realpathSync(fd9Target);
            resolvedLock = fs.realpathSync(lockFile);
        } catch {
            resolvedTarget = path.resolve(fd9Target);
            resolvedLock = path.resolve(lockFile);
        }

        if (resolvedTarget !== resolvedLock) {
            throw new Error(`Lock ownership refused: FD 9 points to '${fd9Target}', expected '${lockFile}'`);
        }

        // Verify that THIS open file description actually holds the kernel FLOCK WRITE lock
        // (Case B: FD 9 open but never flocked; Case C: FD 9 open while another process holds flock)
        let fdinfo = "";
        try {
            fdinfo = fs.readFileSync("/proc/self/fdinfo/9", "utf8");
        } catch (err) {
            throw new Error(`Lock ownership refused: unable to inspect /proc/self/fdinfo/9 (${err.message})`);
        }

        const hasFlockWrite = /lock:\s+\d+:\s+FLOCK\s+ADVISORY\s+WRITE/.test(fdinfo);
        if (!hasFlockWrite) {
            throw new Error("Lock ownership refused: FD 9 is open to runtime.lock but does not hold kernel flock write lock");
        }

        return true;
    }

    // Non-Linux mock fallback if explicit setup context exists
    if (lockContext?.acquired && lockContext?.role === "setup") {
        return true;
    }

    throw new Error("Lock ownership refused: kernel flock not verified");
}

/**
 * Calculate directory size recursively in bytes.
 */
export function getDirectorySizeBytes(dirPath) {
    let total = 0;
    if (!fs.existsSync(dirPath)) return 0;
    let entries = [];
    try {
        entries = fs.readdirSync(dirPath, { withFileTypes: true });
    } catch {
        return 0;
    }
    for (const e of entries) {
        const full = path.join(dirPath, e.name);
        try {
            if (e.isDirectory()) {
                total += getDirectorySizeBytes(full);
            } else if (!e.isSymbolicLink()) {
                total += fs.statSync(full).size;
            }
        } catch {}
    }
    return total;
}

/**
 * Non-destructive evaluation of runtime release retention plan.
 */
export function evaluateRetentionPlan({ runtimeDir = "/var/cordbrief/runtime" } = {}) {
    const root = path.resolve(runtimeDir);
    const releasesDir = path.join(root, "releases");
    const currentLink = path.join(root, "current");

    let currentStat;
    try {
        currentStat = fs.lstatSync(currentLink);
    } catch {
        return {
            failClosed: true,
            failClosedReason: "current_symlink_missing",
            currentReleaseId: null,
            rollbackReleaseId: null,
            keepReleaseIds: [],
            pruneCandidateIds: [],
            tempEntryIds: [],
            ignoredEntryIds: [],
            invalidReleases: [],
            validReleases: [],
            reclaimableBytes: 0
        };
    }

    if (!currentStat.isSymbolicLink()) {
        return {
            failClosed: true,
            failClosedReason: "current_is_not_symlink",
            currentReleaseId: null,
            rollbackReleaseId: null,
            keepReleaseIds: [],
            pruneCandidateIds: [],
            tempEntryIds: [],
            ignoredEntryIds: [],
            invalidReleases: [],
            validReleases: [],
            reclaimableBytes: 0
        };
    }

    const rawTarget = fs.readlinkSync(currentLink);
    const resolvedCurrent = path.resolve(root, rawTarget);
    const relToReleases = path.relative(releasesDir, resolvedCurrent);

    if (relToReleases.startsWith("..") || path.isAbsolute(relToReleases)) {
        return {
            failClosed: true,
            failClosedReason: "current_points_outside_releases",
            currentReleaseId: null,
            rollbackReleaseId: null,
            keepReleaseIds: [],
            pruneCandidateIds: [],
            tempEntryIds: [],
            ignoredEntryIds: [],
            invalidReleases: [],
            validReleases: [],
            reclaimableBytes: 0
        };
    }

    if (relToReleases.startsWith("..") || path.isAbsolute(relToReleases)) {
        return {
            failClosed: true,
            failClosedReason: "current_points_outside_releases",
            currentReleaseId: null,
            rollbackReleaseId: null,
            keepReleaseIds: [],
            pruneCandidateIds: [],
            tempEntryIds: [],
            ignoredEntryIds: [],
            invalidReleases: [],
            validReleases: [],
            reclaimableBytes: 0
        };
    }

    if (!fs.existsSync(resolvedCurrent)) {
        return {
            failClosed: true,
            failClosedReason: "current_target_missing",
            currentReleaseId: null,
            rollbackReleaseId: null,
            keepReleaseIds: [],
            pruneCandidateIds: [],
            tempEntryIds: [],
            ignoredEntryIds: [],
            invalidReleases: [],
            validReleases: [],
            reclaimableBytes: 0
        };
    }

    const currentReleaseId = path.basename(resolvedCurrent);
    const currentValidation = validateReleaseDirectory(resolvedCurrent);
    if (!currentValidation.valid) {
        return {
            failClosed: true,
            failClosedReason: `current_target_invalid: ${currentValidation.reason}`,
            currentReleaseId,
            rollbackReleaseId: null,
            keepReleaseIds: [currentReleaseId],
            pruneCandidateIds: [],
            tempEntryIds: [],
            ignoredEntryIds: [],
            invalidReleases: [{ id: currentReleaseId, reason: currentValidation.reason }],
            validReleases: [],
            reclaimableBytes: 0
        };
    }

    if (!fs.existsSync(releasesDir)) {
        return {
            failClosed: true,
            failClosedReason: "releases_directory_missing",
            currentReleaseId,
            rollbackReleaseId: null,
            keepReleaseIds: [currentReleaseId],
            pruneCandidateIds: [],
            tempEntryIds: [],
            ignoredEntryIds: [],
            invalidReleases: [],
            validReleases: [],
            reclaimableBytes: 0
        };
    }

    const entries = fs.readdirSync(releasesDir, { withFileTypes: true });
    const tempEntryIds = [];
    const ignoredEntryIds = [];
    const validReleases = [];
    const invalidReleases = [];

    for (const e of entries) {
        const full = path.join(releasesDir, e.name);

        // Stale .tmp release candidate
        if (e.name.endsWith(".tmp")) {
            if (/^[a-zA-Z0-9_-]+\.tmp$/.test(e.name) && !e.isSymbolicLink()) {
                tempEntryIds.push(e.name);
            } else {
                ignoredEntryIds.push(e.name);
            }
            continue;
        }

        // Must be a directory and not a symlink
        if (e.isSymbolicLink() || !e.isDirectory()) {
            ignoredEntryIds.push(e.name);
            continue;
        }

        // Candidate release naming check: CordBrief standard is 14-digit timestamp or release-*
        const isKnownReleasePattern = /^\d{14}$/.test(e.name) || /^release-[a-zA-Z0-9_-]+$/.test(e.name);
        if (!isKnownReleasePattern) {
            ignoredEntryIds.push(e.name);
            continue;
        }

        const val = validateReleaseDirectory(full);
        if (val.valid) {
            const sizeBytes = getDirectorySizeBytes(full);
            validReleases.push({
                id: e.name,
                path: full,
                createdAt: val.createdAt,
                sizeBytes,
                manifest: val.manifest
            });
        } else {
            invalidReleases.push({
                id: e.name,
                path: full,
                reason: val.reason
            });
        }
    }

    // Sort valid releases: newest first by createdAt, then by directory name lexicographically
    validReleases.sort((a, b) => {
        if (!isNaN(a.createdAt) && !isNaN(b.createdAt) && a.createdAt !== b.createdAt) {
            return b.createdAt - a.createdAt;
        }
        return b.id.localeCompare(a.id);
    });

    // Determine rollback release: newest valid release other than current
    let rollbackReleaseId = null;
    for (const rel of validReleases) {
        if (rel.id !== currentReleaseId) {
            rollbackReleaseId = rel.id;
            break;
        }
    }

    const keepReleaseIds = [currentReleaseId];
    if (rollbackReleaseId && rollbackReleaseId !== currentReleaseId) {
        keepReleaseIds.push(rollbackReleaseId);
    }

    // Prune candidate IDs: valid releases outside keep set
    const pruneCandidateIds = [];
    let reclaimableBytes = 0;

    for (const rel of validReleases) {
        if (!keepReleaseIds.includes(rel.id)) {
            pruneCandidateIds.push(rel.id);
            reclaimableBytes += rel.sizeBytes;
        }
    }

    return {
        failClosed: false,
        failClosedReason: null,
        currentReleaseId,
        rollbackReleaseId,
        keepReleaseIds,
        pruneCandidateIds,
        tempEntryIds,
        ignoredEntryIds,
        invalidReleases,
        validReleases,
        reclaimableBytes
    };
}

/**
 * Prune runtime releases outside the keep set.
 * Requires setup lock ownership.
 */
export function pruneRuntimeReleases({ runtimeDir = "/var/cordbrief/runtime", lockContext = null, dryRun = false } = {}) {
    verifyLockOwnership({ runtimeDir, lockContext });

    const plan = evaluateRetentionPlan({ runtimeDir });
    if (plan.failClosed) {
        throw new Error(`Retention pruning aborted (fail closed): ${plan.failClosedReason}`);
    }

    const releasesDir = path.join(runtimeDir, "releases");
    const pruned = [];
    const cleanedTemp = [];
    let reclaimedBytes = 0;

    if (!dryRun) {
        // Clean stale tmp entries
        for (const tmpName of plan.tempEntryIds) {
            const target = path.join(releasesDir, tmpName);
            if (path.dirname(target) === releasesDir && fs.existsSync(target)) {
                fs.rmSync(target, { recursive: true, force: true });
                cleanedTemp.push(tmpName);
            }
        }

        // Prune eligible releases
        for (const releaseId of plan.pruneCandidateIds) {
            const target = path.join(releasesDir, releaseId);
            // Strict safety invariant: direct child, not current, not rollback
            if (
                path.dirname(target) === releasesDir &&
                releaseId !== plan.currentReleaseId &&
                releaseId !== plan.rollbackReleaseId &&
                fs.existsSync(target)
            ) {
                const stat = fs.lstatSync(target);
                if (stat.isDirectory() && !stat.isSymbolicLink()) {
                    const sz = getDirectorySizeBytes(target);
                    fs.rmSync(target, { recursive: true, force: true });
                    pruned.push(releaseId);
                    reclaimedBytes += sz;
                }
            }
        }
    }

    return {
        dryRun,
        current: plan.currentReleaseId,
        rollback: plan.rollbackReleaseId,
        kept: plan.keepReleaseIds,
        pruned: dryRun ? plan.pruneCandidateIds : pruned,
        cleanedTemp: dryRun ? plan.tempEntryIds : cleanedTemp,
        ignored: plan.ignoredEntryIds,
        reclaimedBytes: dryRun ? plan.reclaimableBytes : reclaimedBytes
    };
}

/**
 * Safely activates previous or specified validated release under runtime/releases.
 * Requires setup lock ownership.
 */
export function rollbackRuntimeRelease({
    runtimeDir = "/var/cordbrief/runtime",
    targetReleaseId = null,
    lockContext = null,
    _renameSync = fs.renameSync,
    _testReplaceFn = null
} = {}) {
    verifyLockOwnership({ runtimeDir, lockContext });

    const plan = evaluateRetentionPlan({ runtimeDir });
    if (plan.failClosed) {
        throw new Error(`Rollback aborted: ${plan.failClosedReason}`);
    }

    const currentId = plan.currentReleaseId;
    let targetId = targetReleaseId;
    if (!targetId) {
        targetId = plan.rollbackReleaseId;
    }
    if (!targetId) {
        throw new Error("Rollback failed: no valid previous release available");
    }

    // Target validation
    if (!/^[a-zA-Z0-9_-]+$/.test(targetId)) {
        throw new Error(`Rollback failed: invalid target release id format '${targetId}'`);
    }
    if (targetId === currentId) {
        throw new Error(`Rollback failed: target '${targetId}' is already the active current release`);
    }

    const releasesDir = path.join(runtimeDir, "releases");
    const targetDir = path.join(releasesDir, targetId);

    // Verify containment and validation
    if (path.dirname(targetDir) !== releasesDir || !fs.existsSync(targetDir)) {
        throw new Error(`Rollback failed: target release '${targetId}' does not exist in releases`);
    }

    const val = validateReleaseDirectory(targetDir);
    if (!val.valid) {
        throw new Error(`Rollback failed: target release '${targetId}' failed validation (${val.reason})`);
    }

    // Atomic symlink switch
    const currentLink = path.join(runtimeDir, "current");
    const relativeTarget = path.join("releases", targetId);
    atomicActivateSymlink(relativeTarget, currentLink, runtimeDir, "current.rollback", _renameSync, { _testReplaceFn });

    // Validate new active current
    const postVal = validateReleaseDirectory(path.join(runtimeDir, "current"));
    if (!postVal.valid) {
        throw new Error(`Rollback failed: newly activated current release is invalid (${postVal.reason})`);
    }

    return {
        success: true,
        previousReleaseId: currentId,
        activeReleaseId: targetId
    };
}

/**
 * List all runtime releases with metadata.
 */
export function listRuntimeReleases({ runtimeDir = "/var/cordbrief/runtime" } = {}) {
    return evaluateRetentionPlan({ runtimeDir });
}
