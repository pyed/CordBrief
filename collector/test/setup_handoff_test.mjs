import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import http from "node:http";
import { CollectorSupervisor, MODES, isVencordPatched, patchVencord } from "../supervisor.mjs";

const root = fs.mkdtempSync(path.join(os.tmpdir(), "cordbrief-handoff-"));
const profile = path.join(root, "profile");
const runtimeDir = path.join(root, "runtime");
const vencordDir = path.join(root, "vencord");
const oldApp = path.join(profile, "app-1.0.156");
const setupApp = path.join(root, "installed-discord");
let collector;
const cdp = http.createServer((req, res) => {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify([{ type: "page", url: "https://discord.com/channels/@me" }]));
});
try {
    for (const app of [oldApp, setupApp]) {
        fs.mkdirSync(path.join(app, "resources"), { recursive: true });
        fs.writeFileSync(path.join(app, "resources", "app.asar"), `official ${path.basename(app)}`);
        fs.writeFileSync(path.join(app, "Discord"), "binary");
    }
    fs.writeFileSync(path.join(setupApp, "resources", "build_info.json"), JSON.stringify({ version: "1.0.157" }));
    fs.mkdirSync(path.join(vencordDir, "dist"), { recursive: true });
    fs.writeFileSync(path.join(vencordDir, "dist", "patcher.js"), "patcher");
    fs.writeFileSync(path.join(profile, "Cookies"), "preserved session");
    fs.writeFileSync(path.join(profile, "settings.json"), JSON.stringify({ IS_MAXIMIZED: true }));
    const originalProfile = fs.readFileSync(path.join(oldApp, "resources", "app.asar"));

    // Setup must stage the app it authenticated, even when an older app is in the profile.
    const setup = new CollectorSupervisor({ role: "setup", runtimeDir, vencordDir,
        discordConfigDir: profile, exchangeDir: root, exitOnSetupComplete: false });
    setup.getDiscordAppDir = () => setupApp;
    await setup.transitionToNormal();
    const manifest = JSON.parse(fs.readFileSync(path.join(runtimeDir, "current", "runtime-manifest.json")));
    assert.equal(manifest.discord_version, "1.0.157");
    const runtimeApp = path.join(runtimeDir, "current", "discord");
    assert.equal(fs.readFileSync(path.join(runtimeApp, "resources", "_app.asar"), "utf8"), "official installed-discord");
    assert.equal(isVencordPatched(runtimeApp), true, "positive control: staging installs Vencord");

    collector = new CollectorSupervisor({ role: "collector", runtimeDir, discordConfigDir: profile,
        exchangeDir: root, collectorDataDir: root, vencordDir, enableLock: false });
    const launches = [];
    collector.startDiscordProcess = async () => {
        const app = collector.getDiscordAppDir();
        assert.equal(app, runtimeApp);
        assert.equal(isVencordPatched(app), collector.mode === MODES.NORMAL,
            "the launched application must be clean during authentication and patched only in NORMAL");
        launches.push(collector.mode);
    };
    collector.probeAuthRoute = async () => ({ authenticated: true, url: "/app" });
    await collector.start();
    assert.deepEqual(launches, [MODES.SETUP, MODES.NORMAL]);
    assert.equal(collector.discordAuthenticated, true);
    assert.equal(collector.collectorState, "running");
    assert.equal(collector.getVencordDistDir(), path.join(runtimeDir, "current", "vencord", "dist"));
    assert.equal(fs.readFileSync(path.join(profile, "Cookies"), "utf8"), "preserved session");
    assert.deepEqual(fs.readFileSync(path.join(oldApp, "resources", "app.asar")), originalProfile);

    await collector.transitionToReauth();
    assert.equal(isVencordPatched(runtimeApp), false);
    await collector.transitionToNormal();
    assert.equal(isVencordPatched(runtimeApp), true);
    await collector.stop();

    // A slow updater is not evidence of expired authentication.
    collector.probeAuthRoute = async () => ({ authenticated: false, url: null, timeout: true });
    await collector.start();
    assert.equal(collector.mode, MODES.SETUP);
    assert.equal(collector.discordAuthenticated, null);
    assert.equal(collector.collectorState, "starting");
    await new Promise(resolve => cdp.listen(0, "127.0.0.1", resolve));
    collector.cdpPort = cdp.address().port;
    await collector.pollCdpRoute();
    assert.equal(collector.mode, MODES.NORMAL, "late authentication must complete the handoff without a login cycle");
    assert.equal(collector.discordAuthenticated, true);
    await collector.stop();

    // A real login page still requires reauthentication.
    patchVencord(runtimeApp, collector.getVencordDistDir());
    collector.probeAuthRoute = async () => ({ authenticated: false, url: "/login", reason: "login_url" });
    await collector.start();
    assert.equal(collector.mode, MODES.REAUTH);
    assert.equal(collector.discordAuthenticated, false);

    // Exercise the real launch preparation without creating a desktop process.
    collector.spawnDiscord = false;
    await CollectorSupervisor.prototype.startDiscordProcess.call(collector);
    const runtimeSettings = JSON.parse(fs.readFileSync(path.join(profile, "settings.json")));
    assert.equal(runtimeSettings.SKIP_HOST_UPDATE, true);
    assert.equal(runtimeSettings.IS_MAXIMIZED, true);
    setup.spawnDiscord = false;
    await setup.startDiscordProcess();
    assert.equal(JSON.parse(fs.readFileSync(path.join(profile, "settings.json"))).SKIP_HOST_UPDATE, false);
    assert.equal(fs.readFileSync(path.join(profile, "Cookies"), "utf8"), "preserved session");
    const freshSetup = new CollectorSupervisor({ role: "setup", discordConfigDir: path.join(root, "empty") });
    assert.equal(freshSetup.getDiscordAppDir(), null, "a fresh install must use the official bootstrap launcher");
    console.log("Setup handoff regression passed");
} finally {
    if (collector) await collector.stop();
    cdp.close();
    fs.rmSync(root, { recursive: true, force: true });
}
