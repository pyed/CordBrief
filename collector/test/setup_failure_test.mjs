import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { CollectorSupervisor, MODES } from "../supervisor.mjs";

const root = fs.mkdtempSync(path.join(os.tmpdir(), "cordbrief-setup-failure-"));
try {
    const supervisor = new CollectorSupervisor({
        role: "setup", exchangeDir: root, runtimeDir: root,
        collectorDataDir: root, discordConfigDir: root, vencordDir: root,
        spawnDiscord: false, exitOnSetupComplete: false
    });
    let released = false;
    supervisor.lockHandle = { release() { released = true; } };
    await assert.rejects(supervisor.transitionToNormal(), /Vencord dist source directory not found/);
    const status = JSON.parse(fs.readFileSync(supervisor.statusFile, "utf8"));
    assert.equal(supervisor.mode, MODES.SETUP);
    assert.equal(supervisor.collectorState, "error");
    assert.match(JSON.stringify(status), /runtime_staging_failed/);
    assert.equal(released, false, "failed setup must not release ownership as though it succeeded");
    console.log("Setup failure regression passed");
} finally {
    fs.rmSync(root, { recursive: true, force: true });
}
