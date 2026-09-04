#!/usr/bin/env node
/*
 * CordBrief Collector Supervisor
 * Manages Discord lifecycle, operational modes (SETUP, NORMAL, REAUTH, ERROR),
 * official unpatched login vs patched Vencord collection, CDP route monitoring,
 * and exchange control commands (collector-command.json).
 */

import { spawn, execSync } from "child_process";
import * as fs from "fs";
import * as path from "path";

export const MODES = {
    SETUP: "setup",
    NORMAL: "normal",
    REAUTH: "reauth",
    ERROR: "error"
};

export const DEFAULT_VENCORD_SHIM_CONTENT = (() => {
    // 197-byte standard Vencord asar loader
    const headerObj = {
        files: {
            "index.js": { size: 50, offset: "0" },
            "package.json": { size: 43, offset: "50" }
        }
    };
    const headerStr = JSON.stringify(headerObj);
    const headerBuf = Buffer.from(headerStr, "utf8");
    const payload = Buffer.from('require("/home/cordbrief/vencord/dist/patcher.js"){\n\t"name": "discord",\n\t"main": "index.js"\n}\n', "utf8");
    
    // Header size aligned
    const totalHeaderSize = headerBuf.length;
    const headerSizeBuf = Buffer.alloc(8);
    headerSizeBuf.writeUInt32LE(4, 0);
    headerSizeBuf.writeUInt32LE(totalHeaderSize, 4);

    const magic = Buffer.from([0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00]);
    return Buffer.concat([magic, headerBuf, payload]);
})();

export function getLatestAppDir(discordConfigDir) {
    if (!fs.existsSync(discordConfigDir)) return null;
    const entries = fs.readdirSync(discordConfigDir)
        .filter(e => e.startsWith("app-") && fs.statSync(path.join(discordConfigDir, e)).isDirectory())
        .sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
    if (entries.length === 0) return null;
    return path.join(discordConfigDir, entries[entries.length - 1]);
}

export function isVencordPatched(appDir) {
    if (!appDir) return false;
    const resourcesDir = path.join(appDir, "resources");
    const appAsar = path.join(resourcesDir, "app.asar");
    const originalAsar = path.join(resourcesDir, "_app.asar");
    if (!fs.existsSync(originalAsar) || !fs.existsSync(appAsar)) {
        return false;
    }
    try {
        const content = fs.readFileSync(appAsar, "utf8");
        return content.includes("patcher.js");
    } catch {
        return false;
    }
}

export function patchVencord(appDir, vencordDistDir = "/home/cordbrief/vencord/dist") {
    if (!appDir) throw new Error("No app directory provided to patchVencord");
    const resourcesDir = path.join(appDir, "resources");
    if (!fs.existsSync(resourcesDir)) {
        fs.mkdirSync(resourcesDir, { recursive: true });
    }
    const appAsar = path.join(resourcesDir, "app.asar");
    const originalAsar = path.join(resourcesDir, "_app.asar");
    const shimBackup = path.join(resourcesDir, "app.asar.vencord");

    // If _app.asar does not exist yet, rename current official app.asar to _app.asar
    if (!fs.existsSync(originalAsar)) {
        if (fs.existsSync(appAsar)) {
            fs.renameSync(appAsar, originalAsar);
        } else {
            throw new Error("Cannot patch Vencord: neither app.asar nor _app.asar exists");
        }
    }

    // Now install the Vencord loader into app.asar
    if (fs.existsSync(shimBackup)) {
        fs.copyFileSync(shimBackup, appAsar);
    } else {
        const patcherPath = path.join(vencordDistDir, "patcher.js");
        const loaderJs = `require(${JSON.stringify(patcherPath)})`;
        const pkgJson = '{\n\t"name": "discord",\n\t"main": "index.js"\n}\n';
        const filesObj = {
            files: {
                "index.js": { size: Buffer.byteLength(loaderJs), offset: "0" },
                "package.json": { size: Buffer.byteLength(pkgJson), offset: String(Buffer.byteLength(loaderJs)) }
            }
        };
        const headerJson = JSON.stringify(filesObj);
        const headerBuf = Buffer.from(headerJson, "utf8");
        const payloadBuf = Buffer.concat([Buffer.from(loaderJs, "utf8"), Buffer.from(pkgJson, "utf8")]);

        const buf = Buffer.alloc(16 + headerBuf.length + payloadBuf.length);
        buf.writeUInt32LE(4, 0);
        buf.writeUInt32LE(headerBuf.length + 8, 4);
        buf.writeUInt32LE(headerBuf.length + 4, 8);
        buf.writeUInt32LE(headerBuf.length, 12);
        headerBuf.copy(buf, 16);
        payloadBuf.copy(buf, 16 + headerBuf.length);

        fs.writeFileSync(appAsar, buf);
        fs.writeFileSync(shimBackup, buf);
    }
    return true;
}

export function unpatchVencord(appDir) {
    if (!appDir) throw new Error("No app directory provided to unpatchVencord");
    const resourcesDir = path.join(appDir, "resources");
    const appAsar = path.join(resourcesDir, "app.asar");
    const originalAsar = path.join(resourcesDir, "_app.asar");
    const shimBackup = path.join(resourcesDir, "app.asar.vencord");

    if (!fs.existsSync(originalAsar)) {
        // Already unpatched or pure clean Discord
        return true;
    }

    // Backup current Vencord shim if present
    if (fs.existsSync(appAsar)) {
        try {
            const content = fs.readFileSync(appAsar, "utf8");
            if (content.includes("patcher.js")) {
                fs.copyFileSync(appAsar, shimBackup);
            }
        } catch {}
    }

    // Restore official Discord asar to app.asar
    fs.copyFileSync(originalAsar, appAsar);
    return true;
}

export function findMainDiscordWindow(display = process.env.DISPLAY || ":100", execFn = execSync) {
    try {
        const out = execFn("xdotool search --class discord", {
            env: { ...process.env, DISPLAY: display },
            stdio: ["ignore", "pipe", "ignore"],
            encoding: "utf8"
        });
        const wids = String(out).trim().split(/\s+/).filter(Boolean);
        let bestWid = null;
        let maxArea = 0;

        for (const wid of wids) {
            let title = "";
            try {
                title = execFn(`xdotool getwindowname ${wid}`, {
                    env: { ...process.env, DISPLAY: display },
                    stdio: ["ignore", "pipe", "ignore"],
                    encoding: "utf8"
                }).trim();
            } catch {}

            if (title.toLowerCase().includes("updater")) {
                continue; // Ignore splash/updater
            }

            let width = 0, height = 0;
            try {
                const geom = execFn(`xdotool getwindowgeometry --shell ${wid}`, {
                    env: { ...process.env, DISPLAY: display },
                    stdio: ["ignore", "pipe", "ignore"],
                    encoding: "utf8"
                });
                const wMatch = String(geom).match(/WIDTH=(\d+)/);
                const hMatch = String(geom).match(/HEIGHT=(\d+)/);
                if (wMatch) width = parseInt(wMatch[1], 10);
                if (hMatch) height = parseInt(hMatch[1], 10);
            } catch {}

            // Ignore tiny helper windows (10x10, 16x16)
            if (width >= 400 && height >= 300) {
                const area = width * height;
                if (area > maxArea) {
                    maxArea = area;
                    bestWid = wid;
                }
            }
        }
        return bestWid;
    } catch {
        return null;
    }
}

export function ensureMainWindowActive(display = process.env.DISPLAY || ":100", execFn = execSync) {
    const wid = findMainDiscordWindow(display, execFn);
    if (!wid) return null;

    try {
        execFn(`xdotool windowmap ${wid} windowraise ${wid} windowactivate ${wid}`, {
            env: { ...process.env, DISPLAY: display },
            stdio: ["ignore", "ignore", "ignore"]
        });
        return wid;
    } catch {
        return null;
    }
}

export async function triggerCdpReload(port = 9222, timeoutMs = 3000) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
        const resp = await fetch(`http://127.0.0.1:${port}/json`, { signal: controller.signal });
        if (!resp.ok) return false;
        const pages = await resp.json();
        if (!Array.isArray(pages)) return false;
        const appPage = pages.find(item => item.type === "page" && (item.url?.includes("/app") || item.title === "Discord") && !item.url?.includes("splash"));
        if (!appPage || !appPage.webSocketDebuggerUrl || typeof WebSocket === "undefined") return false;

        return await new Promise(resolve => {
            const ws = new WebSocket(appPage.webSocketDebuggerUrl);
            const wsTimer = setTimeout(() => {
                try { ws.close(); } catch {}
                resolve(false);
            }, 2000);

            ws.onopen = () => {
                ws.send(JSON.stringify({
                    id: 99,
                    method: "Page.reload",
                    params: { ignoreCache: true }
                }));
            };

            ws.onmessage = () => {
                clearTimeout(wsTimer);
                try { ws.close(); } catch {}
                resolve(true);
            };

            ws.onerror = () => {
                clearTimeout(wsTimer);
                resolve(false);
            };
        });
    } catch {
        return false;
    } finally {
        clearTimeout(timer);
    }
}

export async function inspectDiscordAuth(port = 9222, timeoutMs = 2000) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
        const resp = await fetch(`http://127.0.0.1:${port}/json`, { signal: controller.signal });
        if (!resp.ok) return { authenticated: false, url: null, reason: "http_not_ok" };
        const pages = await resp.json();
        if (!Array.isArray(pages)) return { authenticated: false, url: null, reason: "not_array" };

        const splashPage = pages.find(item => item.type === "page" && (item.url?.includes("splash") || item.title?.includes("Updater")));
        const appPage = pages.find(item => item.type === "page" && !item.url?.includes("splash") && (item.url?.includes("/app") || item.url?.includes("/channels") || item.url?.includes("/login") || item.title === "Discord"));

        if (!appPage) {
            if (splashPage) {
                return { authenticated: false, url: splashPage.url, reason: "splash_loading" };
            }
            return { authenticated: false, url: null, reason: "no_page" };
        }

        const url = appPage.url || "";
        if (url.includes("/login") || url.includes("/register")) {
            return { authenticated: false, url, reason: "login_url" };
        }
        if (url.includes("/channels")) {
            return { authenticated: true, url, reason: "channels_url" };
        }

        // Discord desktop /app route: inspect authenticated state in localStorage via DevTools WebSocket
        if (appPage.webSocketDebuggerUrl && typeof WebSocket !== "undefined") {
            return await new Promise(resolve => {
                const ws = new WebSocket(appPage.webSocketDebuggerUrl);
                const wsTimer = setTimeout(() => {
                    try { ws.close(); } catch {}
                    resolve({ authenticated: false, url, reason: "ws_timeout" });
                }, 1500);

                ws.onopen = () => {
                    ws.send(JSON.stringify({
                        id: 1,
                        method: "Runtime.evaluate",
                        params: {
                            expression: "({ hasToken: !!localStorage.getItem('token'), userId: localStorage.getItem('user_id_cache'), pathname: window.location.pathname, appMountChildren: document.getElementById('app-mount')?.childElementCount || 0 })",
                            returnByValue: true
                        }
                    }));
                };

                ws.onmessage = (ev) => {
                    clearTimeout(wsTimer);
                    try { ws.close(); } catch {}
                    try {
                        const parsed = JSON.parse(ev.data);
                        const val = parsed?.result?.result?.value;
                        if (val && val.pathname === "/login") {
                            resolve({ authenticated: false, url: val.pathname, reason: "login_url" });
                        } else if (val && val.hasToken && val.userId) {
                            resolve({
                                authenticated: true,
                                url: val.pathname || url,
                                userId: val.userId,
                                hasToken: true,
                                appMountChildren: val.appMountChildren,
                                reason: val.appMountChildren === 0 ? "app_mount_empty" : null
                            });
                        } else {
                            resolve({ authenticated: false, url: val?.pathname || url, reason: "no_token_or_login" });
                        }
                    } catch {
                        resolve({ authenticated: false, url, reason: "eval_error" });
                    }
                };

                ws.onerror = () => {
                    clearTimeout(wsTimer);
                    resolve({ authenticated: false, url, reason: "ws_error" });
                };
            });
        }

        return { authenticated: false, url, reason: "unrecognized_route" };
    } catch {
        return { authenticated: false, url: null, reason: "network_error" };
    } finally {
        clearTimeout(timer);
    }
}

export async function inspectCdpUrl(port = 9222, timeoutMs = 1500) {
    const auth = await inspectDiscordAuth(port, timeoutMs);
    return auth.url;
}

export function parseCommandFile(filePath) {
    if (!fs.existsSync(filePath)) return null;
    try {
        const raw = fs.readFileSync(filePath, "utf8");
        const parsed = JSON.parse(raw);
        if (parsed.version !== 1) {
            return { error: `Unsupported command version ${parsed.version}` };
        }
        if (!parsed.request_id || typeof parsed.request_id !== "string") {
            return { error: "Missing or invalid request_id" };
        }
        const validCommands = ["enter_reauth", "return_normal", "status"];
        if (!validCommands.includes(parsed.command)) {
            return { error: `Unknown command: ${parsed.command}` };
        }
        return { command: parsed };
    } catch (err) {
        return { error: `Malformed command JSON: ${err.message}` };
    }
}

export function writeAck(ackPath, ack) {
    const tmp = `${ackPath}.${Date.now()}.${Math.random().toString(36).slice(2)}.tmp`;
    fs.writeFileSync(tmp, JSON.stringify(ack, null, 2), "utf8");
    fs.renameSync(tmp, ackPath);
}

export function writeCollectorStatus(statusPath, status) {
    const tmp = `${statusPath}.${Date.now()}.${Math.random().toString(36).slice(2)}.tmp`;
    fs.writeFileSync(tmp, JSON.stringify(status, null, 2), "utf8");
    fs.renameSync(tmp, statusPath);
}

export class CollectorSupervisor {
    constructor(options = {}) {
        this.exchangeDir = options.exchangeDir || process.env.CORDBRIEF_EXCHANGE_DIR || "/var/cordbrief/exchange";
        this.collectorDataDir = options.collectorDataDir || process.env.CORDBRIEF_COLLECTOR_DATA_DIR || "/var/lib/cordbrief";
        this.discordConfigDir = options.discordConfigDir || process.env.DISCORD_CONFIG_DIR || "/home/cordbrief/.config/discord";
        this.vencordDir = options.vencordDir || process.env.VENCORD_DIR || "/home/cordbrief/vencord";
        this.cdpPort = options.cdpPort || 9222;

        this.statusFile = path.join(this.exchangeDir, "collector-status.json");
        this.commandFile = path.join(this.exchangeDir, "collector-command.json");
        this.commandAckFile = path.join(this.exchangeDir, "collector-command-ack.json");

        this.mode = MODES.SETUP;
        this.collectorState = "starting";
        this.discordAuthenticated = null;
        this.discordProcess = null;
        this.lastProcessedRequestId = null;
        this.lastError = null;
        this.isRunning = false;
        this.consecutiveCrashes = 0;
        this.backoffDelayMs = 0;
        this.spawnDiscord = options.spawnDiscord !== false;
        this.sawUnauthenticated = false;
        this.hasAttemptedColdStartReload = false;
        this.discordProcessStartTime = 0;
    }

    loadLastAckedRequestId() {
        try {
            if (fs.existsSync(this.commandAckFile)) {
                const ack = JSON.parse(fs.readFileSync(this.commandAckFile, "utf8"));
                if (ack && typeof ack.request_id === "string") {
                    return ack.request_id;
                }
            }
        } catch {}
        return null;
    }

    hasProfileSessionFiles() {
        const appDir = getLatestAppDir(this.discordConfigDir);
        if (!appDir) return false;
        const storageDir = path.join(this.discordConfigDir, "Local Storage");
        const cookiesFile = path.join(this.discordConfigDir, "Cookies");
        return fs.existsSync(storageDir) || (fs.existsSync(cookiesFile) && fs.statSync(cookiesFile).size > 0);
    }

    readRuntimeStatus() {
        const candidates = [
            path.join(this.collectorDataDir, "collector-runtime-status.json"),
            path.join(this.exchangeDir, "collector-runtime-status.json")
        ];
        for (const p of candidates) {
            try {
                if (fs.existsSync(p)) {
                    return JSON.parse(fs.readFileSync(p, "utf8"));
                }
            } catch {}
        }
        return null;
    }

    publishStatus() {
        let watchedGen = 0;
        let watchedCount = 0;
        try {
            const wlPath = path.join(this.exchangeDir, "watchlist.json");
            if (fs.existsSync(wlPath)) {
                const wl = JSON.parse(fs.readFileSync(wlPath, "utf8"));
                watchedGen = wl.generation || 0;
                watchedCount = Array.isArray(wl.channel_ids) ? wl.channel_ids.length : 0;
            }
        } catch {}

        let catalogState = "unavailable";
        let catalogUpdatedAt = null;
        let activeSegment = 1;
        let lastEventAt = null;
        let recoveryState = "idle";
        let recoveryLastAt = null;
        let recoveryPendingChannels = 0;
        let recoveryLastError = null;

        // Authoritative single-writer merge:
        // In NORMAL mode with authenticated session, read plugin runtime telemetry
        if (this.mode === MODES.NORMAL && this.discordAuthenticated === true) {
            const runtime = this.readRuntimeStatus();
            if (runtime) {
                if (runtime.catalog_state) catalogState = runtime.catalog_state;
                if (runtime.catalog_updated_at) catalogUpdatedAt = runtime.catalog_updated_at;
                if (typeof runtime.watched_generation === "number") watchedGen = runtime.watched_generation;
                if (typeof runtime.watched_channel_count === "number") watchedCount = runtime.watched_channel_count;
                if (typeof runtime.active_segment === "number") activeSegment = runtime.active_segment;
                if (runtime.last_event_at) lastEventAt = runtime.last_event_at;
                if (runtime.recovery_state) recoveryState = runtime.recovery_state;
                if (runtime.recovery_last_at) recoveryLastAt = runtime.recovery_last_at;
                if (typeof runtime.recovery_pending_channels === "number") recoveryPendingChannels = runtime.recovery_pending_channels;
                if (runtime.recovery_last_error) recoveryLastError = runtime.recovery_last_error;
            }
        } else {
            // In SETUP, REAUTH, or unauthenticated: catalog and continuity are unavailable
            catalogState = "unavailable";
            catalogUpdatedAt = null;
            recoveryState = "idle";
            recoveryLastAt = null;
            recoveryPendingChannels = 0;
            recoveryLastError = null;
        }

        const status = {
            version: 1,
            updated_at: new Date().toISOString(),
            mode: this.mode,
            collector_state: this.collectorState,
            discord_authenticated: this.discordAuthenticated,
            catalog_state: catalogState,
            catalog_updated_at: catalogUpdatedAt,
            watched_generation: watchedGen,
            watched_channel_count: watchedCount,
            active_segment: activeSegment,
            last_event_at: lastEventAt,
            last_error: this.lastError,
            recovery_state: recoveryState,
            recovery_last_at: recoveryLastAt,
            recovery_pending_channels: recoveryPendingChannels,
            recovery_last_error: recoveryLastError
        };

        try {
            writeCollectorStatus(this.statusFile, status);
        } catch (err) {
            console.error("[Supervisor] Failed to write status file:", err.message);
        }
    }

    async probeAuthRoute(timeoutMs = 15000, intervalMs = 500) {
        const start = Date.now();
        while (Date.now() - start < timeoutMs) {
            const auth = await inspectDiscordAuth(this.cdpPort);
            if (auth.authenticated) {
                return { authenticated: true, url: auth.url, userId: auth.userId };
            }
            if (auth.reason === "login_url" || auth.reason === "no_token_or_login") {
                return { authenticated: false, url: auth.url, reason: auth.reason };
            }
            await new Promise(r => setTimeout(r, intervalMs));
        }
        return { authenticated: false, url: null, timeout: true };
    }

    async handleCommand() {
        const result = parseCommandFile(this.commandFile);
        if (!result) return; // No command file

        if (result.error) {
            console.error("[Supervisor] Invalid command file:", result.error);
            writeAck(this.commandAckFile, {
                version: 1,
                request_id: "unknown",
                command: "unknown",
                status: "rejected",
                applied_at: new Date().toISOString(),
                error: result.error
            });
            try { fs.unlinkSync(this.commandFile); } catch {}
            return;
        }

        const cmd = result.command;
        if (cmd.request_id === this.lastProcessedRequestId) {
            // Already processed before restart or duplicate submission: clean up and return
            writeAck(this.commandAckFile, {
                version: 1,
                request_id: cmd.request_id,
                command: cmd.command,
                status: "ignored_duplicate",
                applied_at: new Date().toISOString(),
                error: null
            });
            try { fs.unlinkSync(this.commandFile); } catch {}
            return;
        }

        this.lastProcessedRequestId = cmd.request_id;
        console.log(`[Supervisor] Executing command: ${cmd.command} (request: ${cmd.request_id})`);

        let ackStatus = "applied";
        let ackError = null;

        try {
            if (cmd.command === "enter_reauth") {
                // State-based idempotency: if already in reauth, do not repeat unpatch/restart cycle
                if (this.mode === MODES.REAUTH) {
                    ackStatus = "already_in_reauth";
                    console.log("[Supervisor] Already in REAUTH mode, acknowledging without re-execution.");
                } else {
                    await this.transitionToReauth();
                }
            } else if (cmd.command === "return_normal") {
                // State-based idempotency: if already in normal, do not repeat patch/restart cycle
                if (this.mode === MODES.NORMAL) {
                    ackStatus = "already_in_normal";
                    console.log("[Supervisor] Already in NORMAL mode, acknowledging without re-execution.");
                } else {
                    // Auth Guard: return_normal is permitted ONLY if clean Discord proves an authenticated route
                    const auth = await inspectDiscordAuth(this.cdpPort);
                    if (auth.authenticated) {
                        await this.transitionToNormal();
                    } else {
                        ackStatus = "rejected";
                        ackError = `Discord session is not authenticated (route: ${auth.url || "unreachable"}). Please complete login in setup viewer first.`;
                        console.warn(`[Supervisor] Rejected return_normal: ${ackError}`);
                    }
                }
            } else if (cmd.command === "status") {
                this.publishStatus();
            }
        } catch (err) {
            ackStatus = "error";
            ackError = err.message || String(err);
            console.error(`[Supervisor] Command execution failed:`, err);
        }

        writeAck(this.commandAckFile, {
            version: 1,
            request_id: cmd.request_id,
            command: cmd.command,
            status: ackStatus,
            applied_at: new Date().toISOString(),
            error: ackError
        });

        try { fs.unlinkSync(this.commandFile); } catch {}
    }

    async transitionToReauth() {
        console.log("[Supervisor] Transitioning to REAUTH mode (unpatching Vencord)...");
        this.mode = MODES.REAUTH;
        this.collectorState = "reauth_required";
        this.sawUnauthenticated = false;
        this.hasAttemptedColdStartReload = false;
        this.publishStatus();

        await this.stopDiscordProcess();
        const appDir = getLatestAppDir(this.discordConfigDir);
        if (appDir) {
            unpatchVencord(appDir);
        }
        await this.startDiscordProcess();
        this.publishStatus();
    }

    async transitionToNormal() {
        console.log("[Supervisor] Transitioning to NORMAL mode (patching Vencord)...");
        this.mode = MODES.NORMAL;
        this.collectorState = "starting";
        this.publishStatus();

        await this.stopDiscordProcess();
        const appDir = getLatestAppDir(this.discordConfigDir);
        if (appDir) {
            patchVencord(appDir, path.join(this.vencordDir, "dist"));
        }
        await this.startDiscordProcess();
    }

    async startDiscordProcess() {
        this.hasAttemptedColdStartReload = false;
        this.sawUnauthenticated = false;
        this.discordProcessStartTime = Date.now();

        if (!this.spawnDiscord || this.discordProcess) return;

        const discordBin = path.join(this.discordConfigDir, "Discord");
        const bin = fs.existsSync(discordBin) ? discordBin : "discord";

        console.log(`[Supervisor] Launching Discord (${this.mode} mode): ${bin}`);
        const child = spawn(bin, ["--no-sandbox", `--remote-debugging-port=${this.cdpPort}`, "--enable-logging"], {
            stdio: "inherit",
            env: {
                ...process.env,
                DISPLAY: process.env.DISPLAY || ":100",
                ELECTRON_DISABLE_SANDBOX: "1"
            }
        });

        this.discordProcess = child;

        child.on("exit", (code, signal) => {
            console.log(`[Supervisor] Discord process exited (code=${code}, signal=${signal})`);
            this.discordProcess = null;
            if (this.isRunning) {
                this.consecutiveCrashes++;
                this.backoffDelayMs = Math.min(1000 * Math.pow(2, this.consecutiveCrashes - 1), 30000);
                console.log(`[Supervisor] Scheduling Discord restart in ${this.backoffDelayMs}ms (crashes=${this.consecutiveCrashes})`);
                setTimeout(() => {
                    if (this.isRunning && !this.discordProcess) {
                        this.startDiscordProcess();
                    }
                }, this.backoffDelayMs);
            }
        });
    }

    async stopDiscordProcess(timeoutMs = 5000) {
        if (!this.discordProcess) return;
        const child = this.discordProcess;
        this.discordProcess = null;

        console.log("[Supervisor] Stopping Discord process gracefully...");
        try {
            if (process.platform === "win32") {
                child.kill();
            } else {
                child.kill("SIGTERM");
            }
        } catch {}

        const exitPromise = new Promise(resolve => child.once("exit", resolve));
        const timerPromise = new Promise(resolve => setTimeout(resolve, timeoutMs));

        await Promise.race([exitPromise, timerPromise]);

        try {
            if (process.platform !== "win32") {
                process.kill(child.pid, 0);
                child.kill("SIGKILL");
            }
        } catch {
            // Already dead
        }

        // Brief delay to allow TCP socket (CDP port) to be released by kernel
        await new Promise(r => setTimeout(r, 1000));
    }

    async checkColdStartRecovery(auth) {
        // Active ONLY in SETUP and REAUTH modes
        if (this.mode !== MODES.SETUP && this.mode !== MODES.REAUTH) {
            return false;
        }
        // ONLY for clean Discord (never patched Vencord)
        const appDir = getLatestAppDir(this.discordConfigDir);
        if (appDir && isVencordPatched(appDir)) {
            return false;
        }
        // AT MOST ONE reload attempt per Discord process
        if (this.hasAttemptedColdStartReload) {
            return false;
        }
        // Grace period >= 8s
        if (Date.now() - this.discordProcessStartTime < 8000) {
            return false;
        }
        // Trigger ONLY if app mount remains empty while route is /app
        if (auth && auth.reason === "app_mount_empty") {
            console.log("[Supervisor] Cold-start recovery triggered: /app mount is empty after >=8s. Firing single CDP Page.reload...");
            this.hasAttemptedColdStartReload = true;
            const reloaded = await triggerCdpReload(this.cdpPort);
            console.log(`[Supervisor] Cold-start Page.reload result: ${reloaded}`);
            return reloaded;
        }
        return false;
    }

    async pollCdpRoute() {
        const auth = await inspectDiscordAuth(this.cdpPort);
        if (!auth.url && !auth.reason) return;

        // Reset crash backoff if process is responsive and healthy
        this.consecutiveCrashes = 0;
        this.backoffDelayMs = 0;

        // Update actual observed authentication state directly from CDP evidence
        if (auth.authenticated === true) {
            this.discordAuthenticated = true;
        } else if (auth.reason === "login_url" || auth.reason === "no_token_or_login") {
            this.discordAuthenticated = false;
        }

        if (this.mode === MODES.SETUP || this.mode === MODES.REAUTH) {
            // Proactive window activation helper (interactive modes only)
            try {
                ensureMainWindowActive();
            } catch {}

            // Bounded cold-start recovery check (clean Discord only)
            try {
                await this.checkColdStartRecovery(auth);
            } catch {}

            if (auth.reason === "splash_loading" || auth.reason === "app_mount_empty") {
                return;
            }

            if (this.mode === MODES.SETUP) {
                if (auth.authenticated) {
                    console.log(`[Supervisor] Authentication detected in SETUP mode (${auth.url})! Auto-transitioning to NORMAL.`);
                    await this.transitionToNormal();
                } else if (auth.reason === "login_url" || auth.reason === "no_token_or_login") {
                    this.collectorState = "setup_required";
                    this.publishStatus();
                }
            } else if (this.mode === MODES.REAUTH) {
                // In REAUTH mode: only auto-transition if user was observed unauthenticated first
                if (this.sawUnauthenticated && auth.authenticated) {
                    console.log(`[Supervisor] Authentication completed after reauth (${auth.url})! Auto-transitioning to NORMAL.`);
                    this.sawUnauthenticated = false;
                    await this.transitionToNormal();
                } else if (auth.reason === "login_url" || auth.reason === "no_token_or_login") {
                    this.sawUnauthenticated = true;
                    this.collectorState = "reauth_required";
                    this.publishStatus();
                } else if (auth.authenticated) {
                    // Valid session while in REAUTH mode: publish status reflecting discord_authenticated=true
                    this.publishStatus();
                }
            }
        } else if (this.mode === MODES.NORMAL) {
            // NORMAL mode never runs ensureMainWindowActive() or checkColdStartRecovery()
            if (!auth.authenticated && (auth.reason === "login_url" || auth.reason === "no_token_or_login")) {
                console.warn("[Supervisor] Discord session expired; redirected to /login. Reporting reauth_required.");
                this.collectorState = "reauth_required";
                this.publishStatus();
            } else if (auth.authenticated) {
                if (this.collectorState !== "running") {
                    this.collectorState = "running";
                    this.publishStatus();
                }
            }
        }
    }

    async start() {
        this.isRunning = true;
        this.lastProcessedRequestId = this.loadLastAckedRequestId();

        const appDir = getLatestAppDir(this.discordConfigDir);
        const hasSession = this.hasProfileSessionFiles();

        const initCmd = parseCommandFile(this.commandFile);
        if (initCmd?.command?.command === "enter_reauth" || process.env.CORDBRIEF_INITIAL_MODE === "reauth") {
            console.log("[Supervisor] Initial mode REAUTH requested -> entering REAUTH mode (clean Discord)...");
            this.mode = MODES.REAUTH;
            this.collectorState = "reauth_required";
            this.discordAuthenticated = false;
            if (appDir && isVencordPatched(appDir)) {
                unpatchVencord(appDir);
            }
            this.publishStatus();
            if (this.spawnDiscord) {
                await this.startDiscordProcess();
            }
            this.cdpInterval = setInterval(() => this.pollCdpRoute(), 2500);
            this.commandInterval = setInterval(() => this.handleCommand(), 1000);
            this.statusInterval = setInterval(() => this.publishStatus(), 5000);
            return;
        }

        if (!appDir || !hasSession) {
            console.log("[Supervisor] Profile absent/uninitialized -> entering SETUP mode (clean Discord)...");
            this.mode = MODES.SETUP;
            this.collectorState = "setup_required";
            this.discordAuthenticated = false;
            if (appDir && isVencordPatched(appDir)) {
                unpatchVencord(appDir);
            }
            this.publishStatus();
            await this.startDiscordProcess();
        } else {
            console.log("[Supervisor] Persisted profile found. Launching clean Discord auth probe...");
            this.mode = MODES.SETUP;
            this.collectorState = "starting";
            this.discordAuthenticated = null;
            // Always ensure clean unpatched Discord for the auth probe
            if (isVencordPatched(appDir)) {
                console.log("[Supervisor] Unpatching Vencord for startup auth probe...");
                unpatchVencord(appDir);
            }
            this.publishStatus();

            if (this.spawnDiscord) {
                await this.startDiscordProcess();
                console.log("[Supervisor] Probing Discord authentication route via CDP...");
                const probe = await this.probeAuthRoute();
                console.log(`[Supervisor] Auth probe result: authenticated=${probe.authenticated}, url=${probe.url}`);

                if (probe.authenticated) {
                    console.log("[Supervisor] Authentication proven! Transitioning to NORMAL mode...");
                    await this.stopDiscordProcess();
                    patchVencord(appDir, path.join(this.vencordDir, "dist"));
                    this.mode = MODES.NORMAL;
                    this.collectorState = "running";
                    this.discordAuthenticated = true;
                    this.publishStatus();
                    await this.startDiscordProcess();
                } else {
                    console.log(`[Supervisor] Profile unauthenticated or expired (${probe.url || "timeout"}). Keeping Discord clean in REAUTH mode.`);
                    this.mode = MODES.REAUTH;
                    this.collectorState = "reauth_required";
                    this.discordAuthenticated = false;
                    this.publishStatus();
                }
            } else {
                // In non-spawning mode (unit tests), inspect route via mock CDP
                const auth = await inspectDiscordAuth(this.cdpPort);
                if (auth.authenticated) {
                    this.mode = MODES.NORMAL;
                    this.collectorState = "running";
                    this.discordAuthenticated = true;
                    patchVencord(appDir, path.join(this.vencordDir, "dist"));
                } else {
                    this.mode = MODES.REAUTH;
                    this.collectorState = "reauth_required";
                    this.discordAuthenticated = false;
                    unpatchVencord(appDir);
                }
                this.publishStatus();
            }
        }

        // Background supervisor loops
        this.cdpInterval = setInterval(() => this.pollCdpRoute(), 2500);
        this.commandInterval = setInterval(() => this.handleCommand(), 1000);
        this.statusInterval = setInterval(() => this.publishStatus(), 5000);
    }

    async stop() {
        console.log("[Supervisor] Stopping supervisor...");
        this.isRunning = false;
        if (this.cdpInterval) clearInterval(this.cdpInterval);
        if (this.commandInterval) clearInterval(this.commandInterval);
        if (this.statusInterval) clearInterval(this.statusInterval);

        await this.stopDiscordProcess();
        console.log("[Supervisor] Teardown complete.");
    }
}

// Entrypoint execution if executed directly as a script
if (process.argv[1] && process.argv[1].endsWith("supervisor.mjs")) {
    const supervisor = new CollectorSupervisor();
    supervisor.start().catch(err => {
        console.error("[Supervisor] Fatal error on start:", err);
        process.exit(1);
    });

    const shutdown = async () => {
        console.log("\n[Supervisor] Received shutdown signal.");
        await supervisor.stop();
        process.exit(0);
    };

    process.on("SIGTERM", shutdown);
    process.on("SIGINT", shutdown);
}
