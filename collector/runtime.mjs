/*
 * CordBrief Runtime & Ownership Manager
 * Handles exclusive single-ownership leasing (runtime.lock) between setup and collector,
 * stale lease recovery (>30s), and runtime-manifest validation and preparation.
 */

import * as fs from "fs";
import * as path from "path";
import * as os from "os";

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

    function tryCreateLock() {
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

    let result = tryCreateLock();
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
            // Safe stale lease reclamation
            try {
                fs.unlinkSync(lockFilePath);
            } catch {}
            result = tryCreateLock();
        }

        if (!result.success) {
            return {
                acquired: false,
                existing: existing || { holder: "unknown", stale: false },
                release: () => {}
            };
        }
    }

    const currentLockData = result.lockData;
    let released = false;

    // Heartbeat timer to keep lease fresh
    const timer = setInterval(() => {
        if (released) return;
        try {
            currentLockData.heartbeat_at = new Date().toISOString();
            const tmpFile = `${lockFilePath}.${pid}.tmp`;
            fs.writeFileSync(tmpFile, JSON.stringify(currentLockData, null, 2), "utf8");
            fs.renameSync(tmpFile, lockFilePath);
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
            if (fs.existsSync(lockFilePath)) {
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

/**
 * Check if runtime should be staged or can be reused (for fast reauth).
 */
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
        const appVersion = path.basename(discordAppSrcDir).replace(/^app-/, "");
        if (val.manifest.discord_version && val.manifest.discord_version !== appVersion) {
            return true;
        }
    }
    return false;
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
        discord_version: path.basename(discordAppSrcDir).replace(/^app-/, ""),
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
    const tmpSymlink = path.join(runtimeDir, `current.${id}.tmp`);
    const relativeTarget = path.join("releases", id);
    try {
        fs.symlinkSync(relativeTarget, tmpSymlink, "junction");
        fs.renameSync(tmpSymlink, currentSymlink);
    } catch {
        try { fs.unlinkSync(currentSymlink); } catch {}
        fs.symlinkSync(relativeTarget, currentSymlink, "junction");
    }

    return { releaseId: id, releaseDir, manifest };
}
