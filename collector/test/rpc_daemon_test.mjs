/*
 * rpc_daemon_test.mjs
 * Unit test for RpcCollectorDaemon against MockDiscordRpcServer
 */

import assert from "assert";
import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import { pathToFileURL } from "url";
import { runStateOwnershipTests } from "./rpc_state_ownership_test.mjs";
import { MockDiscordRpcServer } from "./mock_discord_rpc.mjs";
import { RpcCollectorDaemon } from "../rpc/daemon.mjs";
import { RpcTransport } from "../rpc/transport.mjs";
import { DiscordRpcClient } from "../rpc/protocol.mjs";

async function runDaemonTest() {
    const tmpExchange = path.join(os.tmpdir(), `cordbrief-daemon-test-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    fs.mkdirSync(tmpExchange, { recursive: true });
    const privateDir = path.join(tmpExchange, "private");
    fs.mkdirSync(privateDir, { recursive: true });

    // Seed mock Discord server
    const mock = new MockDiscordRpcServer({
        guilds: [{ id: "1001", name: "Alpha Guild" }],
        channelsByGuild: { "1001": [{ id: "2001", name: "general", type: 0 }] },
        channelData: { "2001": { id: "2001", name: "general", type: 0, guild_id: "1001", messages: [] } }
    });
    const pipePath = await mock.start();

    // Write empty watchlist
    fs.writeFileSync(path.join(tmpExchange, "watchlist.json"), JSON.stringify({ version: 1, generation: 1, channel_ids: [] }));

    const transport = new RpcTransport({ socketPath: pipePath });
    const client = new DiscordRpcClient(transport);

    const daemon = new RpcCollectorDaemon({
        exchangeDir: tmpExchange,
        collectorDataDir: privateDir,
        runtimeDir: path.join(tmpExchange, "runtime"),
        clientId: "test_daemon_client",
        clientSecret: "test_daemon_secret",
        tokenPath: path.join(privateDir, "oauth-token.json"),
        transport,
        client
    });

    // Seed mock token so it doesn't need to hit discord.com/api/oauth2/token in unit test
    daemon.saveToken({
        accessToken: "test_daemon_token_123",
        refreshToken: "test_refresh_token_456",
        expiresIn: 3600
    });

    try {
        await daemon.start();

        // Verify status file written by collector
        const statusFile = path.join(tmpExchange, "collector-status.json");
        assert.ok(fs.existsSync(statusFile));
        const status = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        assert.strictEqual(status.collector_state, "running");
        assert.strictEqual(status.discord_authenticated, true);

        await daemon.stop();

        const statusAfter = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        assert.strictEqual(statusAfter.collector_state, "stopped");

        console.log("rpc_daemon_test (basic) passed");
    } finally {
        await daemon.stop().catch(() => {});
        await mock.stop().catch(() => {});
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
    }
}

async function runLoginRequiredSlowReadyTest() {
    const tmpExchange = path.join(os.tmpdir(), `cordbrief-slowready-test-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    fs.mkdirSync(tmpExchange, { recursive: true });
    const privateDir = path.join(tmpExchange, "private");
    fs.mkdirSync(privateDir, { recursive: true });

    // Seed mock Discord server with READY suppressed (simulates logged-out Discord client)
    const mock = new MockDiscordRpcServer({
        suppressReady: true,
        guilds: [{ id: "1001", name: "Alpha Guild" }],
        channelsByGuild: { "1001": [{ id: "2001", name: "general", type: 0 }] },
        channelData: { "2001": { id: "2001", name: "general", type: 0, guild_id: "1001", messages: [] } }
    });
    const pipePath = await mock.start();

    // Write empty watchlist
    fs.writeFileSync(path.join(tmpExchange, "watchlist.json"), JSON.stringify({ version: 1, generation: 1, channel_ids: [] }));

    const transport = new RpcTransport({ socketPath: pipePath });
    const client = new DiscordRpcClient(transport);

    const daemon = new RpcCollectorDaemon({
        exchangeDir: tmpExchange,
        collectorDataDir: privateDir,
        runtimeDir: path.join(tmpExchange, "runtime"),
        clientId: "test_daemon_client",
        clientSecret: "test_daemon_secret",
        tokenPath: path.join(privateDir, "oauth-token.json"),
        transport,
        client,
        mockXpra: true,
        connectTimeoutMs: 150,
        connectPollIntervalMs: 50,
        startupGracePeriodMs: 300,
        statusHeartbeatIntervalMs: 150
    });

    daemon.saveToken({
        accessToken: "test_daemon_token_123",
        refreshToken: "test_refresh_token_456",
        expiresIn: 3600
    });

    let unexpectedCloseFired = false;
    daemon.on("unexpected_close", () => {
        unexpectedCloseFired = true;
    });

    try {
        // Start daemon in background - connect will repeatedly time out waiting for READY
        const startPromise = daemon.start();

        // Wait past grace period
        await new Promise(r => setTimeout(r, 450));

        // Verify daemon has entered DISCORD_LOGIN_REQUIRED and has NOT exited or restarted
        assert.strictEqual(unexpectedCloseFired, false, "Must not fire unexpected_close during login wait");
        assert.strictEqual(daemon.collectorState, "discord_login_required");
        assert.strictEqual(daemon.discordAuthenticated, false);

        const statusFile = path.join(tmpExchange, "collector-status.json");
        assert.ok(fs.existsSync(statusFile));
        const status1 = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        assert.strictEqual(status1.collector_state, "discord_login_required");
        assert.strictEqual(status1.discord_authenticated, false);
        const ts1 = new Date(status1.updated_at).getTime();

        // Wait for status heartbeat while still waiting for login
        await new Promise(r => setTimeout(r, 300));
        const status2 = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        const ts2 = new Date(status2.updated_at).getTime();
        assert.ok(ts2 > ts1, `updated_at must advance during login wait (ts1=${ts1}, ts2=${ts2})`);
        assert.strictEqual(unexpectedCloseFired, false, "Must still not fire unexpected_close");

        // Now operator logs in: mock server begins emitting READY
        mock.suppressReady = false;

        // Daemon should now detect READY, transition to authenticated, and reach running
        await startPromise;

        assert.strictEqual(daemon.collectorState, "running");
        assert.strictEqual(daemon.discordAuthenticated, true);
        const status3 = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        assert.strictEqual(status3.collector_state, "running");
        assert.strictEqual(status3.discord_authenticated, true);

        await daemon.stop();
        console.log("rpc_daemon_test (slow-ready / login-required) passed");
    } finally {
        await daemon.stop().catch(() => {});
        await mock.stop().catch(() => {});
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
    }
}

async function runDaemonRecoveryErrorVisibilityTest() {
    const tmpExchange = path.join(os.tmpdir(), `cordbrief-daemon-rec-err-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    fs.mkdirSync(tmpExchange, { recursive: true });
    const privateDir = path.join(tmpExchange, "private");
    fs.mkdirSync(privateDir, { recursive: true });

    const DISCORD_EPOCH = 1420070400000n;
    const t0 = 1750000000000n;
    const msg1 = (((t0 - DISCORD_EPOCH) << 22n) | 1n).toString();

    // 1. Seed existing journal so channel 2001 has prior durable collection evidence
    const eventsDir = path.join(tmpExchange, "events");
    fs.mkdirSync(eventsDir, { recursive: true });
    const seg1 = path.join(eventsDir, "0000000000000001.ndjson");
    fs.writeFileSync(seg1, JSON.stringify({
        version: 1,
        event: "message_create",
        id: msg1,
        message_id: msg1,
        channel_id: "2001",
        guild_id: "1001",
        content: "Prior msg",
        timestamp: new Date().toISOString()
    }) + "\n");

    // 2. Corrupt/unanchored recovery state for channel 2001: baseline_pending
    const recoveryStateFile = path.join(privateDir, "recovery-state.json");
    fs.writeFileSync(recoveryStateFile, JSON.stringify({
        version: 2,
        channels: {
            "2001": {
                checkpoint_message_id: "",
                checkpoint_source: "baseline_pending",
                watch_after: ""
            }
        },
        pending: null
    }));

    // 3. Watchlist watching channel 2001
    fs.writeFileSync(path.join(tmpExchange, "watchlist.json"), JSON.stringify({
        version: 1,
        generation: 1,
        channel_ids: ["2001"]
    }));

    // 4. Seed mock Discord server
    const mock = new MockDiscordRpcServer({
        guilds: [{ id: "1001", name: "Alpha Guild" }],
        channelsByGuild: { "1001": [{ id: "2001", name: "general", type: 0 }] },
        channelData: {
            "2001": {
                id: "2001",
                name: "general",
                type: 0,
                guild_id: "1001",
                messages: [
                    { id: msg1, channel_id: "2001", content: "Prior msg", timestamp: new Date().toISOString() }
                ]
            }
        }
    });
    const pipePath = await mock.start();

    const transport = new RpcTransport({ socketPath: pipePath });
    const client = new DiscordRpcClient(transport);

    const daemon = new RpcCollectorDaemon({
        exchangeDir: tmpExchange,
        collectorDataDir: privateDir,
        runtimeDir: path.join(tmpExchange, "runtime"),
        clientId: "test_daemon_client",
        clientSecret: "test_daemon_secret",
        tokenPath: path.join(privateDir, "oauth-token.json"),
        transport,
        client,
        mockXpra: true
    });

    daemon.saveToken({
        accessToken: "test_daemon_token_123",
        refreshToken: "test_refresh_token_456",
        expiresIn: 3600
    });

    const statusFile = path.join(tmpExchange, "collector-status.json");

    try {
        await daemon.start();

        // Verify that recovery failure on channel 2001 caused daemon to enter error state
        assert.strictEqual(daemon.collectorState, "error", "Daemon collectorState must be 'error'");
        assert.ok(fs.existsSync(statusFile));
        const status1 = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        assert.strictEqual(status1.collector_state, "error", "status.json collector_state must be 'error'");
        assert.strictEqual(status1.recovery_state, "error", "status.json recovery_state must be 'error'");
        assert.ok(status1.last_error && status1.last_error.includes("baseline_pending"), `last_error must contain recovery failure reason: ${status1.last_error}`);
        assert.ok(status1.recovery_last_error && status1.recovery_last_error.includes("baseline_pending"));

        // Verify heartbeats do NOT overwrite error state with running
        daemon.publishStatus();
        const statusHb = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        assert.strictEqual(statusHb.collector_state, "error", "Heartbeat must preserve collector_state: 'error'");
        assert.strictEqual(statusHb.recovery_state, "error", "Heartbeat must preserve recovery_state: 'error'");
        assert.ok(statusHb.last_error, "Heartbeat must preserve last_error");

        // Verify recovery: repair recovery-state.json with valid checkpoint
        fs.writeFileSync(recoveryStateFile, JSON.stringify({
            version: 2,
            channels: {
                "2001": {
                    checkpoint_message_id: msg1,
                    checkpoint_source: "rpc",
                    watch_after: msg1
                }
            },
            pending: null
        }));

        // Reconcile and recover
        await daemon.collector.reconcileWatchlist();
        daemon.publishStatus();

        const statusRecovered = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        assert.strictEqual(statusRecovered.collector_state, "running", "Once recovery succeeds, status returns to 'running'");
        assert.strictEqual(statusRecovered.recovery_state, "ready", "recovery_state must return to 'ready'");
        assert.strictEqual(statusRecovered.last_error, null, "last_error must be cleared upon clean recovery");

        await daemon.stop();
        console.log("rpc_daemon_test (recovery error visibility) passed");
    } finally {
        await daemon.stop().catch(() => {});
        await mock.stop().catch(() => {});
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
    }
}

async function runDaemonRuntimeRecoveryErrorAndRetryTest() {
    const tmpExchange = path.join(os.tmpdir(), `cordbrief-runtime-err-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    fs.mkdirSync(tmpExchange, { recursive: true });
    const privateDir = path.join(tmpExchange, "private");
    fs.mkdirSync(privateDir, { recursive: true });

    const t0 = 1750000000000n;
    const msg1 = ((t0 << 22n) | 1n).toString();
    const msg2 = (((t0 + 5000n) << 22n) | 1n).toString();

    const validState = {
        version: 2,
        channels: {
            "2001": {
                checkpoint_message_id: msg1,
                checkpoint_source: "rpc",
                watch_after: msg1
            }
        },
        pending: null
    };
    const recoveryStateFile = path.join(privateDir, "recovery-state.json");
    fs.writeFileSync(recoveryStateFile, JSON.stringify(validState));

    fs.writeFileSync(path.join(tmpExchange, "watchlist.json"), JSON.stringify({
        version: 1,
        generation: 1,
        channel_ids: ["2001"]
    }));

    const mock = new MockDiscordRpcServer({
        guilds: [{ id: "1001", name: "Alpha Guild" }],
        channelsByGuild: { "1001": [{ id: "2001", name: "general", type: 0 }] },
        channelData: {
            "2001": {
                id: "2001",
                name: "general",
                type: 0,
                guild_id: "1001",
                messages: [
                    { id: msg1, channel_id: "2001", content: "Initial msg", timestamp: new Date().toISOString() }
                ]
            }
        }
    });
    const pipePath = await mock.start();
    const transport = new RpcTransport({ socketPath: pipePath });
    const client = new DiscordRpcClient(transport);

    const daemon = new RpcCollectorDaemon({
        exchangeDir: tmpExchange,
        collectorDataDir: privateDir,
        runtimeDir: path.join(tmpExchange, "runtime"),
        clientId: "test_daemon_client",
        clientSecret: "test_daemon_secret",
        tokenPath: path.join(privateDir, "oauth-token.json"),
        transport,
        client,
        mockXpra: true
    });
    daemon.saveToken({ accessToken: "token123", refreshToken: "refresh456", expiresIn: 3600 });

    const statusFile = path.join(tmpExchange, "collector-status.json");

    try {
        await daemon.start();
        assert.strictEqual(daemon.collectorState, "running");
        const statusInit = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        assert.strictEqual(statusInit.collector_state, "running");
        assert.strictEqual(statusInit.recovery_state, "ready");

        // Corrupt recovery-state.json at runtime
        fs.writeFileSync(recoveryStateFile, "{\"version\": 2, \"channels\": { CORRUPT_RUNTIME...");

        // Inject live message
        daemon.collector.handleMessageCreate({
            channel_id: "2001",
            message: { id: msg2, channel_id: "2001", content: "live msg", timestamp: new Date().toISOString() }
        });

        // Verify message capture refused and daemon entered error state
        assert.strictEqual(daemon.collectorState, "error");
        assert.strictEqual(daemon.recoveryState, "error");
        const statusErr = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        assert.strictEqual(statusErr.collector_state, "error");
        assert.strictEqual(statusErr.recovery_state, "error");
        assert.ok(statusErr.last_error && statusErr.last_error.includes("Corrupt recovery-state"));

        // Heartbeat preserves error state
        daemon.publishStatus();
        const statusHb = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        assert.strictEqual(statusHb.collector_state, "error");
        assert.strictEqual(statusHb.recovery_state, "error");

        // Restore valid recovery-state.json and exercise retryRecovery() (Option B)
        fs.writeFileSync(recoveryStateFile, JSON.stringify(validState));
        await daemon.retryRecovery();

        // Status returns to running/ready
        assert.strictEqual(daemon.collectorState, "running");
        assert.strictEqual(daemon.recoveryState, "ready");
        const statusRepaired = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        assert.strictEqual(statusRepaired.collector_state, "running");
        assert.strictEqual(statusRepaired.recovery_state, "ready");
        assert.strictEqual(statusRepaired.last_error, null);

        await daemon.stop();
        console.log("rpc_daemon_test (runtime recovery error & Option B retry) passed");
    } finally {
        await daemon.stop().catch(() => {});
        await mock.stop().catch(() => {});
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
    }
}

async function runDaemonFatalErrorExitTest() {
    const tmpExchange = path.join(os.tmpdir(), `cordbrief-daemon-fatal-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    fs.mkdirSync(tmpExchange, { recursive: true });
    const privateDir = path.join(tmpExchange, "private");
    fs.mkdirSync(privateDir, { recursive: true });

    const daemonUrl = pathToFileURL(path.resolve("collector/rpc/daemon.mjs")).href;
    const mockUrl = pathToFileURL(path.resolve("collector/test/mock_discord_rpc.mjs")).href;
    const transportUrl = pathToFileURL(path.resolve("collector/rpc/transport.mjs")).href;
    const protoUrl = pathToFileURL(path.resolve("collector/rpc/protocol.mjs")).href;

    const harnessScript = path.join(tmpExchange, "harness.mjs");
    fs.writeFileSync(harnessScript, `
        import { RpcCollectorDaemon } from ${JSON.stringify(daemonUrl)};
        import { MockDiscordRpcServer } from ${JSON.stringify(mockUrl)};
        import { RpcTransport } from ${JSON.stringify(transportUrl)};
        import { DiscordRpcClient } from ${JSON.stringify(protoUrl)};
        import * as fs from "fs";
        import * as path from "path";

        const mock = new MockDiscordRpcServer({
            guilds: [{ id: "1001", name: "Alpha Guild" }],
            channelsByGuild: { "1001": [] },
            channelData: {}
        });
        const pipePath = await mock.start();
        const transport = new RpcTransport({ socketPath: pipePath });
        const client = new DiscordRpcClient(transport);

        const daemon = new RpcCollectorDaemon({
            exchangeDir: ${JSON.stringify(tmpExchange)},
            collectorDataDir: ${JSON.stringify(privateDir)},
            runtimeDir: path.join(${JSON.stringify(tmpExchange)}, "runtime-" + process.argv[2]),
            clientId: "test_daemon_client",
            clientSecret: "test_daemon_secret",
            tokenPath: ${JSON.stringify(path.join(privateDir, "oauth-token.json"))},
            transport,
            client,
            mockXpra: true,
            exitOnClose: false
        });
        daemon.saveToken({ accessToken: "token", refreshToken: "refresh", expiresIn: 3600 });
        fs.writeFileSync(${JSON.stringify(path.join(tmpExchange, "watchlist.json"))}, JSON.stringify({ version: 1, generation: 1, channel_ids: [] }));

        await daemon.start();
        console.log("READY_FOR_FATAL_TEST");

        // Reaching an operation that could block is itself a failure: no hung
        // filesystem/watchdog is needed to prove termination precedes diagnostic I/O.
        const forbiddenIO = () => {
            if (process.argv[2] === "throws") throw new Error("ENOSPC: synthetic status failure");
            process.exit(42);
        };
        daemon.publishStatus = forbiddenIO;
        daemon.collector.writeStatus = forbiddenIO;
        console.error = forbiddenIO;

        // Trigger collector fatal error
        daemon.collector.failStop("Simulated write uncertainty with broken status publication");
    `);

    const { spawn } = await import("child_process");
    for (const mode of ["no-io", "throws"]) {
        const child = spawn(process.execPath, [harnessScript, mode], { stdio: ["ignore", "pipe", "pipe"] });

        let stdoutData = "";
        let stderrData = "";
        let readySeen = false;

        child.stdout.on("data", (chunk) => {
            stdoutData += chunk.toString();
            if (stdoutData.includes("READY_FOR_FATAL_TEST")) {
                readySeen = true;
            }
        });

        child.stderr.on("data", (chunk) => {
            stderrData += chunk.toString();
        });

        const exitCode = await new Promise((resolve, reject) => {
            const timer = setTimeout(() => {
                child.kill();
                reject(new Error("Daemon failed to exit within 5000ms after fatal_error"));
            }, 5000);

            child.on("exit", (code) => {
                clearTimeout(timer);
                resolve(code);
            });
        });

        if (!readySeen) {
            console.error("DEBUG stdout:", stdoutData);
            console.error("DEBUG stderr:", stderrData);
        }
        assert.ok(readySeen, "Child daemon must signal READY_FOR_FATAL_TEST before fatal error injection");
        assert.strictEqual(exitCode, 1, "Production daemon process must exit with code 1 even if status I/O throws");
    }

    try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
    console.log("rpc_daemon_test (fatal error exit code 1 despite status failure) passed");
}

async function runDaemonTwoChannelRecoveryTest() {
    const tmpExchange = path.join(os.tmpdir(), `cordbrief-daemon-twoch-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    fs.mkdirSync(tmpExchange, { recursive: true });
    const privateDir = path.join(tmpExchange, "private");
    fs.mkdirSync(privateDir, { recursive: true });

    const msgA_init = "1545220000000001001";
    const msgB_init = "1545220000000002001";
    const msgA_outage = "1545225000000001002";
    const msgB_outage = "1545225000000002002";

    const mock = new MockDiscordRpcServer({
        guilds: [{ id: "1001", name: "Alpha Guild" }],
        channelsByGuild: {
            "1001": [
                { id: "2001", name: "channel-a", type: 0 },
                { id: "2002", name: "channel-b", type: 0 }
            ]
        },
        channelData: {
            "2001": {
                id: "2001",
                name: "channel-a",
                type: 0,
                guild_id: "1001",
                messages: [
                    { id: msgA_init, channel_id: "2001", content: "Init A", timestamp: new Date().toISOString() }
                ]
            },
            "2002": {
                id: "2002",
                name: "channel-b",
                type: 0,
                guild_id: "1001",
                messages: [
                    { id: msgB_init, channel_id: "2002", content: "Init B", timestamp: new Date().toISOString() }
                ]
            }
        }
    });

    const pipePath = await mock.start();
    const transport = new RpcTransport({ socketPath: pipePath });
    const client = new DiscordRpcClient(transport);

    const daemon = new RpcCollectorDaemon({
        exchangeDir: tmpExchange,
        collectorDataDir: privateDir,
        runtimeDir: path.join(tmpExchange, "runtime"),
        clientId: "test_daemon_client",
        clientSecret: "test_daemon_secret",
        tokenPath: path.join(privateDir, "oauth-token.json"),
        transport,
        client,
        mockXpra: true
    });
    daemon.saveToken({ accessToken: "token123", refreshToken: "refresh456", expiresIn: 3600 });

    fs.writeFileSync(path.join(tmpExchange, "watchlist.json"), JSON.stringify({
        version: 1,
        generation: 1,
        channel_ids: ["2001", "2002"]
    }));

    const statusFile = path.join(tmpExchange, "collector-status.json");
    const recoveryStateFile = path.join(privateDir, "recovery-state.json");

    try {
        await daemon.start();
        assert.strictEqual(daemon.collectorState, "running");
        assert.strictEqual(daemon.recoveryState, "ready");

        const initialValidState = JSON.parse(fs.readFileSync(recoveryStateFile, "utf8"));

        // Introduce recovery state error
        fs.writeFileSync(recoveryStateFile, "{\"version\": 2, \"channels\": { CORRUPT...");

        // Inject live message to channel A during error -> causes fail-closed error state
        daemon.collector.handleMessageCreate({
            channel_id: "2001",
            message: { id: msgA_outage, channel_id: "2001", content: "Outage A", timestamp: new Date().toISOString() }
        });

        assert.strictEqual(daemon.collectorState, "error");
        assert.strictEqual(daemon.recoveryState, "error");

        // Outage messages posted to Discord for BOTH A and B
        mock.channelData["2001"].messages.push({
            id: msgA_outage, channel_id: "2001", content: "Outage A", timestamp: new Date().toISOString()
        });
        mock.channelData["2002"].messages.push({
            id: msgB_outage, channel_id: "2002", content: "Outage B", timestamp: new Date().toISOString()
        });

        // Restore valid recovery state file
        fs.writeFileSync(recoveryStateFile, JSON.stringify(initialValidState));

        // Inspect status before B responds: A alone cannot complete global recovery.
        let stateAfterA = null;
        const origGet = client.getChannel.bind(client);
        client.getChannel = async chId => {
            if (chId === "2002") {
                daemon.publishStatus();
                stateAfterA = {
                    collectorState: daemon.collectorState,
                    recoveryState: daemon.recoveryState
                };
            }
            return origGet(chId);
        };

        // Heartbeat triggers retryRecovery automatically
        await daemon.heartbeat();

        // Verify global state was NOT ready when only channel A had completed
        assert.ok(stateAfterA, "Channel A must have completed first");
        assert.notStrictEqual(stateAfterA.recoveryState, "ready", "Global recovery_state must NOT become ready before all channels finish");
        assert.notStrictEqual(stateAfterA.collectorState, "running", "Running requires the whole pass");

        // After both finish, verify status transitioned to running and ready
        assert.strictEqual(daemon.collectorState, "running");
        assert.strictEqual(daemon.recoveryState, "ready");
        assert.strictEqual(daemon.lastError, null);

        const statusRepaired = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        assert.strictEqual(statusRepaired.collector_state, "running");
        assert.strictEqual(statusRepaired.recovery_state, "ready");
        assert.strictEqual(statusRepaired.last_error, null);

        // Verify both outage messages exist in journal exactly once
        const eventsDir = path.join(tmpExchange, "events");
        const journal = fs.readdirSync(eventsDir)
            .filter(f => /^\d{16}\.ndjson$/.test(f))
            .flatMap(f => fs.readFileSync(path.join(eventsDir, f), "utf8").trim().split("\n").filter(Boolean).map(JSON.parse));

        assert.strictEqual(journal.filter(r => r.message_id === msgA_outage).length, 1, "msgA_outage must exist exactly once");
        assert.strictEqual(journal.filter(r => r.message_id === msgB_outage).length, 1, "msgB_outage must exist exactly once");

        await daemon.stop();
        console.log("rpc_daemon_test (two-channel automatic recovery) passed");
    } finally {
        await daemon.stop().catch(() => {});
        await mock.stop().catch(() => {});
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
    }
}

async function runDaemonOneSucceedsOneFailsTest() {
    const tmpExchange = path.join(os.tmpdir(), `cordbrief-daemon-partial-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    fs.mkdirSync(tmpExchange, { recursive: true });
    const privateDir = path.join(tmpExchange, "private");
    fs.mkdirSync(privateDir, { recursive: true });

    const msgA_init = "1545220000000001001";
    const msgB_init = "1545220000000002001";
    const msgA_new = "1545225000000001002";

    const mock = new MockDiscordRpcServer({
        guilds: [{ id: "1001", name: "Alpha Guild" }],
        channelsByGuild: {
            "1001": [
                { id: "2001", name: "channel-a", type: 0 },
                { id: "2002", name: "channel-b", type: 0 }
            ]
        },
        channelData: {
            "2001": {
                id: "2001",
                name: "channel-a",
                type: 0,
                guild_id: "1001",
                messages: [
                    { id: msgA_init, channel_id: "2001", content: "Init A", timestamp: new Date().toISOString() }
                ]
            },
            "2002": {
                id: "2002",
                name: "channel-b",
                type: 0,
                guild_id: "1001",
                messages: [
                    { id: msgB_init, channel_id: "2002", content: "Init B", timestamp: new Date().toISOString() }
                ]
            }
        }
    });

    const pipePath = await mock.start();
    const transport = new RpcTransport({ socketPath: pipePath });
    const client = new DiscordRpcClient(transport);

    const daemon = new RpcCollectorDaemon({
        exchangeDir: tmpExchange,
        collectorDataDir: privateDir,
        runtimeDir: path.join(tmpExchange, "runtime"),
        clientId: "test_daemon_client",
        clientSecret: "test_daemon_secret",
        tokenPath: path.join(privateDir, "oauth-token.json"),
        transport,
        client,
        mockXpra: true
    });
    daemon.saveToken({ accessToken: "token123", refreshToken: "refresh456", expiresIn: 3600 });

    fs.writeFileSync(path.join(tmpExchange, "watchlist.json"), JSON.stringify({
        version: 1,
        generation: 1,
        channel_ids: ["2001", "2002"]
    }));

    const statusFile = path.join(tmpExchange, "collector-status.json");

    try {
        await daemon.start();
        assert.strictEqual(daemon.collectorState, "running");

        // Add message to channel A
        mock.channelData["2001"].messages.push({
            id: msgA_new, channel_id: "2001", content: "Msg A new", timestamp: new Date().toISOString()
        });

        // Make getChannel fail for channel B (2002)
        const origGetChannel = client.getChannel.bind(client);
        client.getChannel = async (id) => {
            if (id === "2002") {
                throw new Error("Simulated Discord 500 Internal Error for channel B");
            }
            return origGetChannel(id);
        };

        // Trigger recovery error to simulate an outage needing recovery
        daemon.collector.markRecoveryError("Simulated outage requiring recovery pass");
        daemon.collectorState = "error";
        daemon.recoveryState = "error";

        // Force reconciliation / recovery pass
        await daemon.retryRecovery();

        // Verify:
        // 1. Channel A's checkpoint was updated
        const recState = daemon.collector.loadRecoveryState();
        assert.strictEqual(recState.channels["2001"].checkpoint_message_id, msgA_new);
        assert.strictEqual(recState.channels["2001"].last_result, "success");

        // 2. Channel B recorded error
        assert.strictEqual(recState.channels["2002"].last_result, "error");

        // 3. Global states REMAIN ERROR
        assert.strictEqual(daemon.collectorState, "error", "Collector state must remain error when any channel fails");
        assert.strictEqual(daemon.recoveryState, "error", "Recovery state must remain error when any channel fails");
        assert.ok(daemon.lastError.includes("2002"), "Last error must mention failed channel 2002");

        const statusDisk = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        assert.strictEqual(statusDisk.collector_state, "error");
        assert.strictEqual(statusDisk.recovery_state, "error");

        // 4. Channel A's message is safely in journal
        const eventsDir = path.join(tmpExchange, "events");
        const journal = fs.readdirSync(eventsDir)
            .filter(f => /^\d{16}\.ndjson$/.test(f))
            .flatMap(f => fs.readFileSync(path.join(eventsDir, f), "utf8").trim().split("\n").filter(Boolean).map(JSON.parse));
        assert.ok(journal.some(r => r.message_id === msgA_new), "Channel A's message must be in journal");

        // Now repair channel B and retry
        client.getChannel = origGetChannel;
        await daemon.retryRecovery();

        assert.strictEqual(daemon.collectorState, "running");
        assert.strictEqual(daemon.recoveryState, "ready");
        assert.strictEqual(daemon.lastError, null);

        await daemon.stop();
        console.log("rpc_daemon_test (one succeeds / one fails partial recovery) passed");
    } finally {
        await daemon.stop().catch(() => {});
        await mock.stop().catch(() => {});
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
    }
}

async function runDaemonRetrySingleFlightTest() {
    const tmpExchange = path.join(os.tmpdir(), `cordbrief-daemon-singleflight-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    fs.mkdirSync(tmpExchange, { recursive: true });
    const privateDir = path.join(tmpExchange, "private");
    fs.mkdirSync(privateDir, { recursive: true });

    const mock = new MockDiscordRpcServer({
        guilds: [{ id: "1001", name: "Alpha Guild" }],
        channelsByGuild: { "1001": [] },
        channelData: {}
    });
    const pipePath = await mock.start();
    const transport = new RpcTransport({ socketPath: pipePath });
    const client = new DiscordRpcClient(transport);

    const daemon = new RpcCollectorDaemon({
        exchangeDir: tmpExchange,
        collectorDataDir: privateDir,
        runtimeDir: path.join(tmpExchange, "runtime"),
        clientId: "test_daemon_client",
        clientSecret: "test_daemon_secret",
        tokenPath: path.join(privateDir, "oauth-token.json"),
        transport,
        client,
        mockXpra: true
    });
    daemon.saveToken({ accessToken: "token", refreshToken: "refresh", expiresIn: 3600 });
    fs.writeFileSync(path.join(tmpExchange, "watchlist.json"), JSON.stringify({ version: 1, generation: 1, channel_ids: [] }));

    try {
        await daemon.start();

        let reconcileCalls = 0;
        const origReconcile = daemon.collector.reconcileWatchlist.bind(daemon.collector);
        daemon.collector.reconcileWatchlist = async () => {
            reconcileCalls++;
            await new Promise(r => setTimeout(r, 150));
            return origReconcile();
        };

        // Fire 3 concurrent calls to retryRecovery()
        const p1 = daemon.retryRecovery();
        const p2 = daemon.retryRecovery();
        const p3 = daemon.retryRecovery();

        assert.strictEqual(p1, p2, "Concurrent retryRecovery calls must return the identical Promise instance");
        assert.strictEqual(p2, p3, "Concurrent retryRecovery calls must return the identical Promise instance");

        await Promise.all([p1, p2, p3]);

        assert.strictEqual(reconcileCalls, 1, "reconcileWatchlist must execute exactly once during single flight");

        // Subsequent call after completion runs a new flight
        const p4 = daemon.retryRecovery();
        await p4;
        assert.strictEqual(reconcileCalls, 2, "Subsequent retryRecovery after completion executes a new pass");

        await daemon.stop();
        console.log("rpc_daemon_test (retryRecovery single-flight) passed");
    } finally {
        await daemon.stop().catch(() => {});
        await mock.stop().catch(() => {});
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
    }
}

async function main() {
    await runDaemonTest();
    await runLoginRequiredSlowReadyTest();
    await runDaemonRecoveryErrorVisibilityTest();
    await runDaemonRuntimeRecoveryErrorAndRetryTest();
    await runDaemonFatalErrorExitTest();
    await runDaemonTwoChannelRecoveryTest();
    await runDaemonOneSucceedsOneFailsTest();
    await runDaemonRetrySingleFlightTest();
    await runStateOwnershipTests();
}

main().catch(err => {
    console.error("rpc_daemon_test failed:", err);
    process.exit(1);
});
