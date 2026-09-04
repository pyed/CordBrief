/*
 * Operational & Lifecycle Test Suite for Milestone 10
 * Tests mode management, clean Discord vs Vencord patching,
 * startup authentication probe, return_normal auth guard,
 * state-based command idempotency, replay protection on restart,
 * single-writer status publishing, and recovery state invariance.
 */

import * as http from "http";
import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import {
    MODES,
    getLatestAppDir,
    isVencordPatched,
    patchVencord,
    unpatchVencord,
    inspectDiscordAuth,
    inspectCdpUrl,
    triggerCdpReload,
    findMainDiscordWindow,
    ensureMainWindowActive,
    parseCommandFile,
    writeAck,
    writeCollectorStatus,
    CollectorSupervisor
} from "../supervisor.mjs";

function assert(condition, message) {
    if (!condition) {
        console.error(`  ✖ Assertion Failed: ${message}`);
        throw new Error(message);
    }
}

function makeTempDir(prefix) {
    return fs.mkdtempSync(path.join(os.tmpdir(), prefix));
}

async function runTests() {
    console.log("=== Running Milestone 10 Operational & Lifecycle Test Suite ===");

    // [Test 1] Deterministic Vencord Patching & Unpatching
    {
        console.log("[Test 1] Deterministic Vencord Patching & Unpatching...");
        const tmp = makeTempDir("cb-m10-patch-");
        const appDir = path.join(tmp, "app-1.0.156");
        const resourcesDir = path.join(appDir, "resources");
        fs.mkdirSync(resourcesDir, { recursive: true });

        const officialAsar = path.join(resourcesDir, "app.asar");
        fs.writeFileSync(officialAsar, "OFFICIAL_DISCORD_CLEAN_BINARY_PAYLOAD_HERE");

        assert(!isVencordPatched(appDir), "Initially unpatched");

        // 1. Patch Vencord
        patchVencord(appDir, "/home/cordbrief/vencord/dist");
        assert(isVencordPatched(appDir), "Should be patched after patchVencord");
        assert(fs.existsSync(path.join(resourcesDir, "_app.asar")), "_app.asar must hold original binary");
        assert(fs.readFileSync(path.join(resourcesDir, "_app.asar"), "utf8") === "OFFICIAL_DISCORD_CLEAN_BINARY_PAYLOAD_HERE", "Original binary preserved");

        // 2. Unpatch Vencord (returns to clean Discord for setup/reauth)
        unpatchVencord(appDir);
        assert(!isVencordPatched(appDir), "Should be unpatched after unpatchVencord");
        assert(fs.readFileSync(path.join(resourcesDir, "app.asar"), "utf8") === "OFFICIAL_DISCORD_CLEAN_BINARY_PAYLOAD_HERE", "Official Discord asar restored to app.asar");

        // 3. Re-patch Vencord (returns to normal mode)
        patchVencord(appDir, "/home/cordbrief/vencord/dist");
        assert(isVencordPatched(appDir), "Re-patched successfully");

        // 4. Repeated patch is idempotent
        patchVencord(appDir, "/home/cordbrief/vencord/dist");
        assert(isVencordPatched(appDir), "Repeated patch remains patched");
        console.log("  ✔ Vencord patching/unpatching verified without network dependency.");
    }

    // [Test 2] CDP Route Inspection (/login vs /channels/@me)
    {
        console.log("[Test 2] CDP Route Inspection...");
        let mockPageUrl = "https://discord.com/login";

        const server = http.createServer((req, res) => {
            if (req.url === "/json") {
                res.writeHead(200, { "Content-Type": "application/json" });
                res.end(JSON.stringify([
                    { type: "page", url: mockPageUrl, title: "Discord" },
                    { type: "worker", url: "" }
                ]));
            } else {
                res.writeHead(404);
                res.end();
            }
        });

        await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
        const port = server.address().port;

        try {
            // Unauthenticated route
            const url1 = await inspectCdpUrl(port);
            assert(url1 === "https://discord.com/login", "Should detect /login route");

            // Authenticated route
            mockPageUrl = "https://discord.com/channels/@me";
            const url2 = await inspectCdpUrl(port);
            assert(url2 === "https://discord.com/channels/@me", "Should detect /channels/@me route");

            // Unreachable port returns null gracefully
            const url3 = await inspectCdpUrl(port + 1, 200);
            assert(url3 === null, "Unreachable port returns null");
        } finally {
            server.close();
        }
        console.log("  ✔ CDP route inspection verified.");
    }

    // [Test 3] Startup Authentication Semantics & Clean Probe
    {
        console.log("[Test 3] Startup Authentication Semantics & Clean Probe...");
        const tmp = makeTempDir("cb-m10-auth-");
        const discordConfigDir = path.join(tmp, "discord");
        const appDir = path.join(discordConfigDir, "app-1.0.156");
        fs.mkdirSync(path.join(appDir, "resources"), { recursive: true });
        fs.writeFileSync(path.join(appDir, "resources/app.asar"), "OFFICIAL_ASAR");
        fs.writeFileSync(path.join(discordConfigDir, "Cookies"), "session_data");

        let currentMockRoute = "https://discord.com/login";
        const server = http.createServer((req, res) => {
            if (req.url === "/json") {
                res.writeHead(200, { "Content-Type": "application/json" });
                res.end(JSON.stringify([{ type: "page", url: currentMockRoute, title: "Discord" }]));
            } else {
                res.writeHead(404);
                res.end();
            }
        });
        await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
        const cdpPort = server.address().port;

        try {
            // Case A: Persisted profile exists, but route is /login (expired session)
            // MUST stay in REAUTH and keep Vencord unpatched
            currentMockRoute = "https://discord.com/login";
            const supExpired = new CollectorSupervisor({
                discordConfigDir,
                exchangeDir: path.join(tmp, "ex1"),
                collectorDataDir: path.join(tmp, "data1"),
                cdpPort,
                spawnDiscord: false
            });
            fs.mkdirSync(path.join(tmp, "ex1"), { recursive: true });
            fs.mkdirSync(path.join(tmp, "data1"), { recursive: true });

            await supExpired.start();
            assert(supExpired.mode === MODES.REAUTH, "Expired profile must enter REAUTH, not NORMAL");
            assert(supExpired.discordAuthenticated === false, "Must report discordAuthenticated: false");
            assert(!isVencordPatched(appDir), "Vencord must remain UNPATCHED on /login");
            await supExpired.stop();

            // Case B: Persisted profile exists and route is /channels/@me (valid session)
            // MUST transition to NORMAL and patch Vencord
            currentMockRoute = "https://discord.com/channels/@me";
            const supAuthed = new CollectorSupervisor({
                discordConfigDir,
                exchangeDir: path.join(tmp, "ex2"),
                collectorDataDir: path.join(tmp, "data2"),
                cdpPort,
                spawnDiscord: false
            });
            fs.mkdirSync(path.join(tmp, "ex2"), { recursive: true });
            fs.mkdirSync(path.join(tmp, "data2"), { recursive: true });

            await supAuthed.start();
            assert(supAuthed.mode === MODES.NORMAL, "Valid profile must enter NORMAL mode");
            assert(supAuthed.discordAuthenticated === true, "Must report discordAuthenticated: true");
            assert(isVencordPatched(appDir), "Vencord must be patched in NORMAL mode");
            await supAuthed.stop();

            // Case C: Empty profile with no session files
            // MUST enter SETUP mode, clean Discord
            const emptyDir = path.join(tmp, "empty-discord");
            fs.mkdirSync(emptyDir, { recursive: true });
            const supEmpty = new CollectorSupervisor({
                discordConfigDir: emptyDir,
                exchangeDir: path.join(tmp, "ex3"),
                collectorDataDir: path.join(tmp, "data3"),
                cdpPort,
                spawnDiscord: false
            });
            fs.mkdirSync(path.join(tmp, "ex3"), { recursive: true });
            fs.mkdirSync(path.join(tmp, "data3"), { recursive: true });

            await supEmpty.start();
            assert(supEmpty.mode === MODES.SETUP, "Empty profile must enter SETUP mode");
            assert(supEmpty.discordAuthenticated === false, "Must report unauthenticated");
            await supEmpty.stop();
        } finally {
            server.close();
        }
        console.log("  ✔ Startup authentication semantics & clean probe verified.");
    }

    // [Test 4] Guarded return_normal Command
    {
        console.log("[Test 4] Guarded return_normal Command...");
        const tmp = makeTempDir("cb-m10-guard-");
        const exchangeDir = path.join(tmp, "exchange");
        const discordConfigDir = path.join(tmp, "discord");
        const appDir = path.join(discordConfigDir, "app-1.0.156");
        fs.mkdirSync(exchangeDir, { recursive: true });
        fs.mkdirSync(path.join(appDir, "resources"), { recursive: true });
        fs.writeFileSync(path.join(appDir, "resources/app.asar"), "OFFICIAL_ASAR");

        let currentMockRoute = "https://discord.com/login";
        const server = http.createServer((req, res) => {
            if (req.url === "/json") {
                res.writeHead(200, { "Content-Type": "application/json" });
                res.end(JSON.stringify([{ type: "page", url: currentMockRoute, title: "Discord" }]));
            } else {
                res.writeHead(404);
                res.end();
            }
        });
        await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
        const cdpPort = server.address().port;

        try {
            const sup = new CollectorSupervisor({
                exchangeDir,
                discordConfigDir,
                cdpPort,
                spawnDiscord: false
            });
            sup.mode = MODES.REAUTH;
            sup.collectorState = "reauth_required";
            sup.discordAuthenticated = false;

            // 1. In REAUTH + route is /login: return_normal MUST be rejected
            fs.writeFileSync(sup.commandFile, JSON.stringify({
                version: 1,
                command: "return_normal",
                request_id: "req-rej-1"
            }));
            await sup.handleCommand();

            let ack = JSON.parse(fs.readFileSync(sup.commandAckFile, "utf8"));
            assert(ack.status === "rejected", "return_normal on /login must be rejected");
            assert(ack.error && ack.error.includes("not authenticated"), "Ack must expose descriptive unauthenticated reason");
            assert(sup.mode === MODES.REAUTH, "Supervisor must remain in REAUTH mode");
            assert(!isVencordPatched(appDir), "Vencord must remain unpatched");

            // 2. User logs in -> route becomes /channels/@me: return_normal MUST succeed
            currentMockRoute = "https://discord.com/channels/@me";
            fs.writeFileSync(sup.commandFile, JSON.stringify({
                version: 1,
                command: "return_normal",
                request_id: "req-acc-1"
            }));
            await sup.handleCommand();

            ack = JSON.parse(fs.readFileSync(sup.commandAckFile, "utf8"));
            assert(ack.status === "applied", "return_normal on /channels/@me must be applied");
            assert(sup.mode === MODES.NORMAL, "Supervisor must transition to NORMAL mode");
            assert(isVencordPatched(appDir), "Vencord must be patched");
        } finally {
            server.close();
        }
        console.log("  ✔ Guarded return_normal command verified.");
    }

    // [Test 5] State-Based Command Idempotency & Replay Protection
    {
        console.log("[Test 5] State-Based Command Idempotency & Replay Protection...");
        const tmp = makeTempDir("cb-m10-idemp-");
        const exchangeDir = path.join(tmp, "exchange");
        const discordConfigDir = path.join(tmp, "discord");
        const appDir = path.join(discordConfigDir, "app-1.0.156");
        fs.mkdirSync(exchangeDir, { recursive: true });
        fs.mkdirSync(path.join(appDir, "resources"), { recursive: true });
        fs.writeFileSync(path.join(appDir, "resources/app.asar"), "OFFICIAL_ASAR");

        const sup = new CollectorSupervisor({
            exchangeDir,
            discordConfigDir,
            spawnDiscord: false
        });

        // 1. enter_reauth from NORMAL -> transitions
        sup.mode = MODES.NORMAL;
        fs.writeFileSync(sup.commandFile, JSON.stringify({
            version: 1,
            command: "enter_reauth",
            request_id: "req-first"
        }));
        await sup.handleCommand();
        assert(sup.mode === MODES.REAUTH, "Transitioned to REAUTH");

        // 2. enter_reauth again with DIFFERENT request_id while already REAUTH -> state-based no-op
        fs.writeFileSync(sup.commandFile, JSON.stringify({
            version: 1,
            command: "enter_reauth",
            request_id: "req-second-different"
        }));
        await sup.handleCommand();
        let ack = JSON.parse(fs.readFileSync(sup.commandAckFile, "utf8"));
        assert(ack.status === "already_in_reauth", "Already in reauth acknowledged without repeated execution");
        assert(ack.request_id === "req-second-different", "Ack matches second request ID");

        // 3. return_normal with DIFFERENT request_id while already NORMAL -> state-based no-op
        sup.mode = MODES.NORMAL;
        fs.writeFileSync(sup.commandFile, JSON.stringify({
            version: 1,
            command: "return_normal",
            request_id: "req-third-different"
        }));
        await sup.handleCommand();
        ack = JSON.parse(fs.readFileSync(sup.commandAckFile, "utf8"));
        assert(ack.status === "already_in_normal", "Already in normal acknowledged without repeated execution");

        // 4. Supervisor restarts after executing a command: same command file on disk does not re-execute
        // Simulate command file left on disk matching the ack file
        fs.writeFileSync(sup.commandFile, JSON.stringify({
            version: 1,
            command: "return_normal",
            request_id: "req-third-different"
        }));
        const supRestarted = new CollectorSupervisor({
            exchangeDir,
            discordConfigDir,
            spawnDiscord: false
        });
        supRestarted.lastProcessedRequestId = supRestarted.loadLastAckedRequestId();
        assert(supRestarted.lastProcessedRequestId === "req-third-different", "Loaded last acked request ID on startup");

        await supRestarted.handleCommand();
        ack = JSON.parse(fs.readFileSync(supRestarted.commandAckFile, "utf8"));
        assert(ack.status === "ignored_duplicate", "Uncleaned command on restart safely ignored without double execution");
        assert(!fs.existsSync(supRestarted.commandFile), "Command file cleaned up");
        console.log("  ✔ State-based command idempotency & replay protection verified.");
    }

    // [Test 6] Single-Writer Status File Ownership & Telemetry Merge
    {
        console.log("[Test 6] Single-Writer Status File Ownership...");
        const tmp = makeTempDir("cb-m10-status-");
        const exchangeDir = path.join(tmp, "exchange");
        const collectorDataDir = path.join(tmp, "collector-data");
        fs.mkdirSync(exchangeDir, { recursive: true });
        fs.mkdirSync(collectorDataDir, { recursive: true });

        // Seed watchlist
        fs.writeFileSync(path.join(exchangeDir, "watchlist.json"), JSON.stringify({
            version: 1,
            generation: 4,
            channel_ids: ["1545115236619518014", "1545115308556030042"]
        }));

        // 1. Write runtime status from plugin into collector-private directory
        const runtimeStatusPath = path.join(collectorDataDir, "collector-runtime-status.json");
        fs.writeFileSync(runtimeStatusPath, JSON.stringify({
            version: 1,
            updated_at: "2026-09-04T12:00:00.000Z",
            catalog_state: "ready",
            catalog_updated_at: "2026-09-04T11:59:00.000Z",
            watched_generation: 4,
            watched_channel_count: 2,
            active_segment: 1,
            last_event_at: "2026-09-04T11:59:30.000Z",
            last_error: null,
            recovery_state: "ready",
            recovery_last_at: "2026-09-04T11:58:00.000Z",
            recovery_pending_channels: 0,
            recovery_last_error: null
        }));

        const sup = new CollectorSupervisor({
            exchangeDir,
            collectorDataDir,
            spawnDiscord: false
        });

        // Case A: In NORMAL mode with authenticated: true -> supervisor merges runtime telemetry
        sup.mode = MODES.NORMAL;
        sup.collectorState = "running";
        sup.discordAuthenticated = true;
        sup.publishStatus();

        let pubStatus = JSON.parse(fs.readFileSync(sup.statusFile, "utf8"));
        assert(pubStatus.mode === "normal", "Published mode is normal");
        assert(pubStatus.recovery_state === "ready", "Merged recovery_state from plugin");
        assert(pubStatus.catalog_state === "ready", "Merged catalog_state from plugin");
        assert(pubStatus.watched_channel_count === 2, "Watched channel count 2");

        // Case B: In REAUTH mode -> supervisor sets catalog/recovery to unavailable/idle
        sup.mode = MODES.REAUTH;
        sup.collectorState = "reauth_required";
        sup.discordAuthenticated = false;
        sup.publishStatus();

        pubStatus = JSON.parse(fs.readFileSync(sup.statusFile, "utf8"));
        assert(pubStatus.mode === "reauth", "Published mode is reauth");
        assert(pubStatus.catalog_state === "unavailable", "Catalog must be unavailable in REAUTH mode");
        assert(pubStatus.recovery_state === "idle", "Recovery must be idle in REAUTH mode");
        console.log("  ✔ Single-writer status file ownership and telemetry merge verified.");
    }

    // [Test 7] Recovery State Invariance Across Reauth
    {
        console.log("[Test 7] Recovery State Invariance Across Reauth...");
        const tmp = makeTempDir("cb-m10-invar-");
        const collectorDataDir = path.join(tmp, "collector-data");
        const exchangeDir = path.join(tmp, "exchange");
        const discordConfigDir = path.join(tmp, "discord");
        const appDir = path.join(discordConfigDir, "app-1.0.156");
        fs.mkdirSync(path.join(appDir, "resources"), { recursive: true });
        fs.mkdirSync(collectorDataDir, { recursive: true });
        fs.mkdirSync(exchangeDir, { recursive: true });

        fs.writeFileSync(path.join(appDir, "resources/app.asar"), "OFFICIAL_ASAR");
        patchVencord(appDir);

        const recoveryFile = path.join(collectorDataDir, "recovery-state.json");
        const initialRecoveryState = {
            version: 1,
            channels: {
                "1545115236619518014": {
                    checkpoint_message_id: "1545240528923394110",
                    checkpoint_journal_boundary: { segment: 1, offset: 8463 },
                    last_recovery_at: "2026-09-04T01:15:00.401Z",
                    last_result: "success",
                    last_error: null,
                    recovered_count: 2
                },
                "1545115308556030042": {
                    checkpoint_message_id: "1545240650792833144",
                    checkpoint_journal_boundary: { segment: 1, offset: 9451 },
                    last_recovery_at: "2026-09-04T01:15:00.683Z",
                    last_result: "success",
                    last_error: null,
                    recovered_count: 2
                }
            },
            pending: null
        };
        fs.writeFileSync(recoveryFile, JSON.stringify(initialRecoveryState, null, 2), "utf8");

        const sup = new CollectorSupervisor({
            collectorDataDir,
            exchangeDir,
            discordConfigDir,
            spawnDiscord: false
        });

        // Transition to REAUTH
        await sup.transitionToReauth();
        assert(sup.mode === MODES.REAUTH, "Supervisor mode is REAUTH");
        assert(!isVencordPatched(appDir), "Discord unpatched for reauth");

        const afterReauth = JSON.parse(fs.readFileSync(recoveryFile, "utf8"));
        assert(JSON.stringify(afterReauth) === JSON.stringify(initialRecoveryState), "Recovery state must remain untouched by reauth!");

        // Transition back to NORMAL
        await sup.transitionToNormal();
        assert(sup.mode === MODES.NORMAL, "Supervisor mode is NORMAL");
        assert(isVencordPatched(appDir), "Discord re-patched for normal collection");

        const afterNormal = JSON.parse(fs.readFileSync(recoveryFile, "utf8"));
        assert(JSON.stringify(afterNormal) === JSON.stringify(initialRecoveryState), "Recovery state must remain untouched returning to normal!");
        console.log("  ✔ Recovery state invariance strictly proven across reauth transitions.");
    }

    // [Test 8] Splash Route Classification & App Mount Route Inspection
    {
        console.log("[Test 8] Splash Route Classification & App Mount Route Inspection...");
        let mockPages = [];

        const server = http.createServer((req, res) => {
            if (req.url === "/json") {
                res.writeHead(200, { "Content-Type": "application/json" });
                res.end(JSON.stringify(mockPages));
            } else {
                res.writeHead(404);
                res.end();
            }
        });

        await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
        const port = server.address().port;

        try {
            // Case A: Only splash page present -> classified as splash_loading, NOT unauthenticated
            mockPages = [
                {
                    type: "page",
                    title: "Discord Updater",
                    url: "file:///home/cordbrief/.config/discord/app-1.0.156/resources/app.asar/splash/index.html",
                    webSocketDebuggerUrl: `ws://127.0.0.1:${port}/splash-ws`
                }
            ];

            const splashAuth = await inspectDiscordAuth(port);
            assert(!splashAuth.authenticated, "Splash is not authenticated");
            assert(splashAuth.reason === "splash_loading", `Expected reason 'splash_loading', got '${splashAuth.reason}'`);
            assert(splashAuth.url.includes("splash"), "Splash URL retained");

            // Case B: Main /app page present, WebSocket evaluates DOM
            const origWebSocket = globalThis.WebSocket;
            try {
                // Subcase B1: appMountChildren === 0 -> app_mount_empty
                globalThis.WebSocket = class MockWebSocket {
                    constructor(url) {
                        this.url = url;
                        setTimeout(() => {
                            if (this.onopen) this.onopen();
                        }, 5);
                    }
                    send(data) {
                        const parsed = JSON.parse(data);
                        if (parsed.method === "Runtime.evaluate") {
                            setTimeout(() => {
                                if (this.onmessage) {
                                    this.onmessage({
                                        data: JSON.stringify({
                                            id: parsed.id,
                                            result: {
                                                result: {
                                                    value: {
                                                        hasToken: true,
                                                        userId: "1234567890",
                                                        pathname: "/app",
                                                        appMountChildren: 0
                                                    }
                                                }
                                            }
                                        })
                                    });
                                }
                            }, 5);
                        }
                    }
                    close() {}
                };

                mockPages = [
                    {
                        type: "page",
                        title: "Discord",
                        url: "https://discord.com/app",
                        webSocketDebuggerUrl: `ws://127.0.0.1:${port}/app-ws`
                    }
                ];

                const emptyMountAuth = await inspectDiscordAuth(port);
                assert(emptyMountAuth.authenticated === true, "Session with token is authenticated even when app mount is unmounted");
                assert(emptyMountAuth.reason === "app_mount_empty", `Expected 'app_mount_empty', got '${emptyMountAuth.reason}'`);
                assert(emptyMountAuth.hasToken === true, "Token presence preserved");
                assert(emptyMountAuth.appMountChildren === 0, "appMountChildren is 0");

                // Subcase B2: appMountChildren > 0 -> authenticated true
                globalThis.WebSocket = class MockWebSocketMounted {
                    constructor(url) {
                        this.url = url;
                        setTimeout(() => {
                            if (this.onopen) this.onopen();
                        }, 5);
                    }
                    send(data) {
                        const parsed = JSON.parse(data);
                        if (parsed.method === "Runtime.evaluate") {
                            setTimeout(() => {
                                if (this.onmessage) {
                                    this.onmessage({
                                        data: JSON.stringify({
                                            id: parsed.id,
                                            result: {
                                                result: {
                                                    value: {
                                                        hasToken: true,
                                                        userId: "1234567890",
                                                        pathname: "/app",
                                                        appMountChildren: 3
                                                    }
                                                }
                                            }
                                        })
                                    });
                                }
                            }, 5);
                        }
                    }
                    close() {}
                };

                const mountedAuth = await inspectDiscordAuth(port);
                assert(mountedAuth.authenticated === true, "Mounted app with token is authenticated");
                assert(mountedAuth.userId === "1234567890", "User ID extracted");
                assert(mountedAuth.appMountChildren === 3, "appMountChildren verified");
            } finally {
                globalThis.WebSocket = origWebSocket;
            }
        } finally {
            server.close();
        }
        console.log("  ✔ Splash route classification & app mount route inspection verified.");
    }

    // [Test 9] Top-Level Discord Normal Window Identification & Activation
    {
        console.log("[Test 9] Top-Level Discord Normal Window Identification & Activation...");
        const executedCommands = [];

        const mockExec = (cmd, opts) => {
            executedCommands.push(cmd);
            if (cmd.includes("xdotool search --class discord")) {
                // Returns 4 windows: helper 10x10, helper 16x16, updater 300x350, main 1280x800
                return "8388609\n8388611\n6291459\n6291465\n";
            }
            if (cmd.includes("getwindowname 8388609")) return "Discord";
            if (cmd.includes("getwindowgeometry --shell 8388609")) return "WINDOW=8388609\nX=0\nY=0\nWIDTH=10\nHEIGHT=10\n";

            if (cmd.includes("getwindowname 8388611")) return "Discord";
            if (cmd.includes("getwindowgeometry --shell 8388611")) return "WINDOW=8388611\nX=0\nY=0\nWIDTH=16\nHEIGHT=16\n";

            if (cmd.includes("getwindowname 6291459")) return "Discord Updater";
            if (cmd.includes("getwindowgeometry --shell 6291459")) return "WINDOW=6291459\nX=490\nY=117\nWIDTH=300\nHEIGHT=350\n";

            if (cmd.includes("getwindowname 6291465")) return "Discord";
            if (cmd.includes("getwindowgeometry --shell 6291465")) return "WINDOW=6291465\nX=0\nY=0\nWIDTH=1280\nHEIGHT=800\n";

            if (cmd.includes("xdotool windowmap")) return "";
            return "";
        };

        const targetWid = findMainDiscordWindow(":100", mockExec);
        assert(targetWid === "6291465", `Expected window 6291465, got ${targetWid}`);

        const activatedWid = ensureMainWindowActive(":100", mockExec);
        assert(activatedWid === "6291465", `Expected activated window 6291465, got ${activatedWid}`);

        const activationCmd = executedCommands.find(c => c.includes("windowactivate 6291465"));
        assert(!!activationCmd, "Must execute windowmap, windowraise, windowactivate on main window");
        assert(!executedCommands.some(c => c.includes("windowactivate 6291459")), "Must NOT activate updater window");
        assert(!executedCommands.some(c => c.includes("windowactivate 8388609")), "Must NOT activate 10x10 helper window");
        console.log("  ✔ Top-level Discord normal window identification & activation verified.");
    }

    // [Test 10] Bounded Cold-Start Recovery (Clean Discord Only)
    {
        console.log("[Test 10] Bounded Cold-Start Recovery (Clean Discord Only)...");
        const tmp = makeTempDir("cb-m10-cold-");
        const discordConfigDir = path.join(tmp, "discord");
        const appDir = path.join(discordConfigDir, "app-1.0.156");
        fs.mkdirSync(path.join(appDir, "resources"), { recursive: true });
        fs.writeFileSync(path.join(appDir, "resources/app.asar"), "OFFICIAL_ASAR");

        const sup = new CollectorSupervisor({
            discordConfigDir,
            exchangeDir: path.join(tmp, "exchange"),
            collectorDataDir: path.join(tmp, "data"),
            spawnDiscord: false
        });
        sup.mode = MODES.REAUTH;

        let reloadCalls = 0;
        const cdpServer = http.createServer((req, res) => {
            if (req.url === "/json") {
                res.writeHead(200, { "Content-Type": "application/json" });
                res.end(JSON.stringify([{
                    type: "page",
                    title: "Discord",
                    url: "https://discord.com/app",
                    webSocketDebuggerUrl: "ws://127.0.0.1:9999/dummy"
                }]));
            } else {
                res.writeHead(404);
                res.end();
            }
        });
        await new Promise(r => cdpServer.listen(0, "127.0.0.1", r));
        sup.cdpPort = cdpServer.address().port;

        const origWebSocket = globalThis.WebSocket;
        try {
            globalThis.WebSocket = class MockReloadWS {
                constructor() {
                    setTimeout(() => { if (this.onopen) this.onopen(); }, 5);
                }
                send(msg) {
                    const parsed = JSON.parse(msg);
                    if (parsed.method === "Page.reload") {
                        reloadCalls++;
                        setTimeout(() => { if (this.onmessage) this.onmessage({}); }, 5);
                    }
                }
                close() {}
            };

            // Case A: Before grace period (<8s) -> no reload
            sup.discordProcessStartTime = Date.now() - 3000; // 3s ago
            const resultEarly = await sup.checkColdStartRecovery({ reason: "app_mount_empty" });
            assert(resultEarly === false, "Must not reload before 8s grace period");
            assert(reloadCalls === 0, "No reload triggered early");
            assert(sup.hasAttemptedColdStartReload === false, "Flag not set before grace period");

            // Case B: After grace period (>=8s) with empty mount -> fires exactly one reload
            sup.discordProcessStartTime = Date.now() - 9000; // 9s ago
            const resultGrace = await sup.checkColdStartRecovery({ reason: "app_mount_empty" });
            assert(resultGrace === true, "Cold-start reload must fire after grace period");
            assert(reloadCalls === 1, "Exactly one reload triggered");
            assert(sup.hasAttemptedColdStartReload === true, "Allowance consumed");

            // Case C: Subsequent check of same process -> allowance consumed, no second reload
            const resultDuplicate = await sup.checkColdStartRecovery({ reason: "app_mount_empty" });
            assert(resultDuplicate === false, "Must not trigger second reload on same process");
            assert(reloadCalls === 1, "Reload count remains 1");

            // Case D: New Discord process resets reload allowance
            sup.startDiscordProcess(); // resets hasAttemptedColdStartReload to false
            assert(sup.hasAttemptedColdStartReload === false, "Reload allowance reset on new process");
            sup.discordProcessStartTime = Date.now() - 9000;
            const resultNewProc = await sup.checkColdStartRecovery({ reason: "app_mount_empty" });
            assert(resultNewProc === true, "New process can trigger reload once");
            assert(reloadCalls === 2, "Reload triggered on new process");

            // Case E: In NORMAL mode -> never fires reload
            sup.mode = MODES.NORMAL;
            sup.hasAttemptedColdStartReload = false;
            sup.discordProcessStartTime = Date.now() - 9000;
            const resultNormal = await sup.checkColdStartRecovery({ reason: "app_mount_empty" });
            assert(resultNormal === false, "NORMAL mode must never fire cold-start reload");
            assert(reloadCalls === 2, "Reload count unaffected in NORMAL mode");

            // Case F: Patched Vencord -> never fires reload
            sup.mode = MODES.REAUTH;
            patchVencord(appDir);
            sup.hasAttemptedColdStartReload = false;
            sup.discordProcessStartTime = Date.now() - 9000;
            const resultPatched = await sup.checkColdStartRecovery({ reason: "app_mount_empty" });
            assert(resultPatched === false, "Patched Vencord must never trigger cold-start reload");
            assert(reloadCalls === 2, "Reload count unaffected for patched Vencord");
        } finally {
            globalThis.WebSocket = origWebSocket;
            cdpServer.close();
        }
        console.log("  ✔ Bounded cold-start recovery (clean Discord only) verified.");
    }

    // [Test 11] Interactive Mode Window Activation & Recovery Guarantees
    {
        console.log("[Test 11] Interactive Mode Window Activation & Recovery Guarantees...");
        const tmp = makeTempDir("cb-m10-guar-");
        const sup = new CollectorSupervisor({
            exchangeDir: path.join(tmp, "ex"),
            collectorDataDir: path.join(tmp, "data"),
            spawnDiscord: false
        });
        fs.mkdirSync(path.join(tmp, "ex"), { recursive: true });

        sup.mode = MODES.NORMAL;
        sup.collectorState = "running";
        sup.discordAuthenticated = true;

        let serverRoute = "splash_loading";
        const cdpServer = http.createServer((req, res) => {
            if (req.url === "/json") {
                res.writeHead(200, { "Content-Type": "application/json" });
                if (serverRoute === "splash_loading") {
                    res.end(JSON.stringify([{
                        type: "page",
                        title: "Discord Updater",
                        url: "file:///splash/index.html"
                    }]));
                } else if (serverRoute === "channels") {
                    res.end(JSON.stringify([{
                        type: "page",
                        title: "Discord",
                        url: "https://discord.com/channels/@me"
                    }]));
                }
            } else {
                res.writeHead(404);
                res.end();
            }
        });
        await new Promise(r => cdpServer.listen(0, "127.0.0.1", r));
        sup.cdpPort = cdpServer.address().port;

        try {
            // Case A: Splash loading in REAUTH mode does NOT cause auth failure
            sup.mode = MODES.REAUTH;
            sup.collectorState = "reauth_required";
            sup.sawUnauthenticated = false;
            serverRoute = "splash_loading";

            await sup.pollCdpRoute();
            assert(sup.mode === MODES.REAUTH, "Remains in REAUTH");
            assert(sup.sawUnauthenticated === false, "splash_loading must not set sawUnauthenticated");

            // Case B: In NORMAL mode with valid channels route -> remains healthy running
            sup.mode = MODES.NORMAL;
            serverRoute = "channels";
            await sup.pollCdpRoute();
            assert(sup.mode === MODES.NORMAL, "Remains in NORMAL");
            assert(sup.discordAuthenticated === true, "Remains authenticated");
            assert(sup.collectorState === "running", "Remains running");

            // Case C: Authenticated route in REAUTH mode reflects discord_authenticated=true
            sup.mode = MODES.REAUTH;
            sup.collectorState = "reauth_required";
            sup.discordAuthenticated = false;
            sup.sawUnauthenticated = false;
            serverRoute = "channels";
            await sup.pollCdpRoute();
            assert(sup.mode === MODES.REAUTH, "Remains in REAUTH when sawUnauthenticated is false");
            assert(sup.collectorState === "reauth_required", "State is reauth_required");
            assert(sup.discordAuthenticated === true, "Decoupled auth truth: discord_authenticated must be true when route is authenticated");
        } finally {
            cdpServer.close();
        }
        console.log("  ✔ Interactive mode window activation & recovery guarantees verified.");
    }

    console.log("=== ALL OPERATIONAL TESTS: 100% PASSED ===");
}

runTests().catch(err => {
    console.error("Operational test suite failed:", err);
    process.exit(1);
});
