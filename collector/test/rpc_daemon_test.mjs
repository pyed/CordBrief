/*
 * rpc_daemon_test.mjs
 * Unit test for RpcCollectorDaemon against MockDiscordRpcServer
 */

import assert from "assert";
import * as fs from "fs";
import * as path from "path";
import * as os from "os";
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

async function main() {
    await runDaemonTest();
    await runLoginRequiredSlowReadyTest();
    await runDaemonRecoveryErrorVisibilityTest();
}

main().catch(err => {
    console.error("rpc_daemon_test failed:", err);
    process.exit(1);
});
