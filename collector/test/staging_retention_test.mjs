// Linux: run as the supervisor in an isolated Setup container, with this file
// mounted over /home/cordbrief/supervisor.mjs and collector/ mounted at /test.
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";

process.argv[1] = "staging_retention_test.mjs"; // Do not start the imported supervisor CLI.
const { stageRuntime } = await import("/test/stage-runtime.mjs");
const { createRuntimeRelease, verifyLockOwnership, acquireRuntimeLock } = await import("/test/runtime.mjs");
const runtimeDir = process.env.CORDBRIEF_RUNTIME_DIR;
const lockFile = path.join(runtimeDir, "runtime.lock");
assert.equal(verifyLockOwnership({ runtimeDir }), true);
assert.equal(spawnSync("xdpyinfo", [], { stdio: "ignore" }).status, 0, "supervisor can use the ready display");
assert.equal(spawnSync("flock", ["-n", lockFile, "true"]).status, 1);
const inode = fs.statSync(lockFile).ino;
const lease = acquireRuntimeLock(lockFile, "setup");
assert.equal(lease.acquired, true);

const app = "/tmp/staging-app";
const vencord = "/tmp/staging-vencord";
const plugin = path.join(vencord, "src", "userplugins", "cordbriefCollector");
fs.mkdirSync(plugin, { recursive: true });
fs.writeFileSync(path.join(plugin, "index.ts"), "plugin");
fs.mkdirSync(path.join(app, "resources"), { recursive: true });
fs.mkdirSync(path.join(vencord, "dist"), { recursive: true });
fs.writeFileSync(path.join(app, "Discord"), "binary");
fs.writeFileSync(path.join(app, "resources", "app.asar"), "official app");
fs.writeFileSync(path.join(vencord, "dist", "patcher.js"), "patcher");
for (const releaseId of ["20200101000000", "20200102000000", "20200103000000", "20200104000000"]) {
    createRuntimeRelease({ runtimeDir, releaseId, discordAppSrcDir: app, vencordDistSrcDir: path.join(vencord, "dist"), pluginSrcDir: plugin });
}
const options = { runtimeDir, discordAppSrcDir: app, vencordSourceDir: vencord };
const current = fs.readlinkSync(path.join(runtimeDir, "current"));
const reused = stageRuntime(options);
assert.equal(reused.reused, true);
assert.equal(fs.readlinkSync(path.join(runtimeDir, "current")), current);
assert.deepEqual(reused.retentionResult.kept.sort(), ["20200103000000", "20200104000000"]);
assert.equal(reused.retentionResult.pruned.length, 2);
assert.equal(stageRuntime(options).retentionResult.pruned.length, 0, "reuse is idempotent");

const staged = stageRuntime({ ...options, force: true });
assert.equal(staged.reused, false);
assert.equal(staged.retentionResult.current, staged.releaseId);
assert.equal(staged.retentionResult.rollback, "20200104000000");
assert.deepEqual(fs.readdirSync(path.join(runtimeDir, "releases")).sort(), staged.retentionResult.kept.sort());
lease.release();
assert.equal(fs.statSync(lockFile).ino, inode, "lease release preserves the kernel lock inode");
assert.equal(spawnSync("flock", ["-n", lockFile, "true"]).status, 1, "kernel exclusion lasts until process exit");
console.log("Setup staging retention regression passed");
