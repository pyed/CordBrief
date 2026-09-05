/*
 * CordBrief Runtime Staging Tool
 * Copies Discord app and compiled Vencord dist into an immutable release under
 * /var/cordbrief/runtime/releases/<releaseId>, patches app.asar, writes manifest,
 * and atomically activates the release via /var/cordbrief/runtime/current symlink.
 */

import * as fs from "fs";
import * as path from "path";
import { createRuntimeRelease, validateRuntimeManifest, shouldStageRuntime } from "./runtime.mjs";
import { getLatestAppDir, patchVencord } from "./supervisor.mjs";

export function stageRuntime({
    runtimeDir = process.env.CORDBRIEF_RUNTIME_DIR || "/var/cordbrief/runtime",
    vencordSourceDir = "/home/cordbrief/vencord",
    discordConfigDir = "/home/cordbrief/.config/discord",
    force = false
} = {}) {
    const vencordDistSrc = path.join(vencordSourceDir, "dist");
    if (!fs.existsSync(vencordDistSrc)) {
        throw new Error(`Vencord dist source directory not found: ${vencordDistSrc}`);
    }

    const appDir = getLatestAppDir(discordConfigDir);
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

    return {
        reused: false,
        manifest: release.manifest,
        releaseId: release.releaseId,
        vencordDistDest: path.join(runtimeDir, "current", "vencord", "dist"),
        patcherEntry: path.join(runtimeDir, "current", "vencord", "dist", "patcher.js"),
        discordBin: path.join(runtimeDir, "current", "discord", "Discord")
    };
}

// CLI execution support
if (process.argv[1] && path.resolve(process.argv[1]) === path.resolve(new URL(import.meta.url).pathname)) {
    try {
        const force = process.argv.includes("--force");
        console.log(`[StageRuntime] Staging runtime assets into volume (force=${force})...`);
        const res = stageRuntime({ force });
        console.log(`[StageRuntime] Successfully staged release: ${res.releaseId}`);
        console.log(`[StageRuntime] Discord binary: ${res.discordBin}`);
        console.log(`[StageRuntime] Patcher entry: ${res.patcherEntry}`);
        process.exit(0);
    } catch (err) {
        console.error(`[StageRuntime] Staging failed: ${err.message}`);
        process.exit(1);
    }
}
