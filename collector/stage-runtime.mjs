/*
 * CordBrief Runtime Staging Tool
 * Copies Discord app and compiled Vencord dist into an immutable release under
 * /var/cordbrief/runtime/releases/<releaseId>, patches app.asar, writes manifest,
 * and atomically activates the release via /var/cordbrief/runtime/current symlink.
 */

import * as fs from "fs";
import * as path from "path";
import {
    createRuntimeRelease,
    validateRuntimeManifest,
    shouldStageRuntime,
    pruneRuntimeReleases,
    rollbackRuntimeRelease,
    listRuntimeReleases
} from "./runtime.mjs";
import { getSetupAppDir, patchVencord } from "./supervisor.mjs";

export function stageRuntime({
    runtimeDir = process.env.CORDBRIEF_RUNTIME_DIR || "/var/cordbrief/runtime",
    vencordSourceDir = "/home/cordbrief/vencord",
    discordConfigDir = "/home/cordbrief/.config/discord",
    discordAppSrcDir = getSetupAppDir(discordConfigDir),
    force = false
} = {}) {
    const vencordDistSrc = path.join(vencordSourceDir, "dist");
    if (!fs.existsSync(vencordDistSrc)) {
        throw new Error(`Vencord dist source directory not found: ${vencordDistSrc}`);
    }

    const appDir = discordAppSrcDir;
    if (!appDir) {
        throw new Error(`No Discord app directory found in ${discordConfigDir}`);
    }

    // Fast check: if compatible runtime already active, reuse it
    if (!force && !shouldStageRuntime({ runtimeDir, discordAppSrcDir: appDir, force })) {
        const manifestPath = path.join(runtimeDir, "current", "runtime-manifest.json");
        const val = validateRuntimeManifest(manifestPath);
        return {
            reused: true,
            manifest: val.manifest,
            releaseId: "current",
            vencordDistDest: path.join(runtimeDir, "current", "vencord", "dist"),
            patcherEntry: path.join(runtimeDir, "current", "vencord", "dist", "patcher.js"),
            discordBin: path.join(runtimeDir, "current", "discord", "Discord")
        };
    }

    const pluginSrcDir = path.join(vencordSourceDir, "src", "userplugins", "cordbriefCollector");

    // Create immutable versioned release and atomically update 'current' symlink
    const release = createRuntimeRelease({
        runtimeDir,
        discordAppSrcDir: appDir,
        vencordDistSrcDir: vencordDistSrc,
        pluginSrcDir: fs.existsSync(pluginSrcDir) ? pluginSrcDir : null,
        patchVencordFn: (destAppDir, distDir) => patchVencord(destAppDir, distDir)
    });

    const manifestPath = path.join(runtimeDir, "current", "runtime-manifest.json");
    const validation = validateRuntimeManifest(manifestPath);
    if (!validation.valid) {
        throw new Error(`Staged runtime failed manifest validation: ${validation.reason}`);
    }

    // Step 5: Prune releases only AFTER staging, validation, and atomic current activation succeed
    let retentionResult = null;
    try {
        retentionResult = pruneRuntimeReleases({
            runtimeDir,
            lockContext: { role: process.env.CORDBRIEF_ROLE || "setup" }
        });
        console.log(`[StageRuntime] Release retention: kept [${retentionResult.kept.join(", ")}], pruned [${retentionResult.pruned.join(", ")}]`);
    } catch (retErr) {
        console.warn(`[StageRuntime] Retention pruning warning/skipped: ${retErr.message}`);
    }

    return {
        reused: false,
        manifest: release.manifest,
        releaseId: release.releaseId,
        vencordDistDest: path.join(runtimeDir, "current", "vencord", "dist"),
        patcherEntry: path.join(runtimeDir, "current", "vencord", "dist", "patcher.js"),
        discordBin: path.join(runtimeDir, "current", "discord", "Discord"),
        retentionResult
    };
}

// CLI execution support
if (process.argv[1] && path.resolve(process.argv[1]) === path.resolve(new URL(import.meta.url).pathname)) {
    try {
        const args = process.argv.slice(2);
        const runtimeDir = process.env.CORDBRIEF_RUNTIME_DIR || "/var/cordbrief/runtime";

        if (args.includes("--list") || args.includes("--plan")) {
            const plan = listRuntimeReleases({ runtimeDir });
            console.log(JSON.stringify(plan, null, 2));
            process.exit(0);
        }

        if (args.includes("--prune")) {
            const dryRun = args.includes("--dry-run");
            const res = pruneRuntimeReleases({
                runtimeDir,
                lockContext: { role: process.env.CORDBRIEF_ROLE || "setup" },
                dryRun
            });
            console.log(JSON.stringify(res, null, 2));
            process.exit(0);
        }

        if (args.includes("--rollback")) {
            const idx = args.indexOf("--rollback");
            const targetId = (args[idx + 1] && !args[idx + 1].startsWith("--")) ? args[idx + 1] : null;
            const res = rollbackRuntimeRelease({
                runtimeDir,
                targetReleaseId: targetId,
                lockContext: { role: process.env.CORDBRIEF_ROLE || "setup" }
            });
            console.log(`[StageRuntime] Successfully rolled back to release: ${res.activeReleaseId} (previous was ${res.previousReleaseId})`);
            process.exit(0);
        }

        const force = args.includes("--force");
        console.log(`[StageRuntime] Staging runtime assets into volume (force=${force})...`);
        const res = stageRuntime({ force, runtimeDir });
        console.log(`[StageRuntime] Successfully staged release: ${res.releaseId}`);
        console.log(`[StageRuntime] Discord binary: ${res.discordBin}`);
        console.log(`[StageRuntime] Patcher entry: ${res.patcherEntry}`);
        process.exit(0);
    } catch (err) {
        console.error(`[StageRuntime] Failed: ${err.message}`);
        process.exit(1);
    }
}
