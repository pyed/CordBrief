// collector/test/rpc_disconnect_reconnect_test.mjs
// Regression test: Verifies that Discord IPC disconnect during active operation is detected,
// false 'running' status never persists, the daemon terminates non-zero, the supervisor restarts it,
// AUTHENTICATE succeeds unattended, subscriptions and recovery restore, and post-reconnect
// MESSAGE_CREATE events are captured exactly once with zero manual action.

import assert from "assert";
import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import { spawn } from "child_process";
import { MockDiscordRpcServer } from "./mock_discord_rpc.mjs";

async function runDisconnectReconnectTest() {
    console.log("=== Running RPC Disconnect / Reconnect Regression Test ===");

    const rootDir = fs.mkdtempSync(path.join(os.tmpdir(), "cb-recon-test-"));
    const exchangeDir = path.join(rootDir, "exchange");
    const privateDir = path.join(rootDir, "private");
    const runtimeDir = path.join(rootDir, "runtime");
    const socketPath = process.platform === "win32"
        ? `\\\\.\\pipe\\cb-recon-test-${path.basename(rootDir)}`
        : path.join(rootDir, "discord-ipc-0");

    fs.mkdirSync(exchangeDir, { recursive: true });
    fs.mkdirSync(path.join(exchangeDir, "events"), { recursive: true });
    fs.mkdirSync(privateDir, { recursive: true });
    fs.mkdirSync(runtimeDir, { recursive: true });

    // Seed watchlist
    fs.writeFileSync(path.join(exchangeDir, "watchlist.json"), JSON.stringify({
        version: 1,
        generation: 1,
        channel_ids: ["2001"]
    }));

    // Seed credentials and token
    fs.writeFileSync(path.join(privateDir, "credentials.json"), JSON.stringify({
        client_id: "mock_client_id_recon",
        client_secret: "mock_client_secret_recon"
    }), { mode: 0o600 });

    fs.writeFileSync(path.join(privateDir, "oauth-token.json"), JSON.stringify({
        version: 1,
        access_token: "mock_token_recon_123",
        refresh_token: "mock_refresh_recon_456",
        scope: "rpc identify messages.read",
        expires_at: Date.now() + 86400000
    }), { mode: 0o600 });

    // Helper to start fake Discord IPC
    async function startFakeDiscord() {
        try { if (fs.existsSync(socketPath)) fs.unlinkSync(socketPath); } catch {}
        const server = new MockDiscordRpcServer({
            guilds: [{ id: "1001", name: "Alpha Guild" }],
            channelsByGuild: { "1001": [{ id: "2001", name: "test-channel", type: 0 }] },
            channelData: {
                "2001": {
                    id: "2001",
                    name: "test-channel",
                    type: 0,
                    guild_id: "1001",
                    messages: [{
                        id: "1545225000000000100",
                        channel_id: "2001",
                        guild_id: "1001",
                        content: "Seed channel message",
                        timestamp: new Date().toISOString(),
                        author: { id: "u0", username: "system" }
                    }]
                }
            }
        });
        server.pipePath = socketPath;
        const net = await import("net");
        server.server = net.createServer(s => server._handleClient(s));
        await new Promise((resolve, reject) => {
            server.server.listen(socketPath, resolve);
            server.server.on("error", reject);
        });
        return server;
    }

    let mockServer = await startFakeDiscord();
    console.log("[Phase 1] Fake Discord IPC started on socket.");

    // Supervisor loop matching collector/entrypoint-rpc.sh run_daemon()
    let supervisorActive = true;
    let activeDaemonProc = null;
    let daemonRestartCount = 0;
    let daemonExitCodes = [];

    const env = {
        ...process.env,
        CORDBRIEF_EXCHANGE_DIR: exchangeDir,
        CORDBRIEF_COLLECTOR_DATA_DIR: privateDir,
        CORDBRIEF_RUNTIME_DIR: runtimeDir,
        DISCORD_IPC_SOCKET: socketPath,
        DISCORD_CLIENT_ID: "mock_client_id_recon",
        DISCORD_CLIENT_SECRET: "mock_client_secret_recon"
    };

    function startSupervisedDaemon() {
        if (!supervisorActive) return;
        daemonRestartCount++;
        const proc = spawn(process.execPath, [
            path.join(process.cwd(), "collector/rpc/daemon.mjs")
        ], { env, stdio: ["pipe", "pipe", "pipe"] });

        proc.stdout.on("data", d => process.stdout.write(`[DAEMON-OUT] ${d}`));
        proc.stderr.on("data", d => process.stderr.write(`[DAEMON-ERR] ${d}`));

        proc.on("exit", (code, sig) => {
            daemonExitCodes.push({ code, sig, time: Date.now() });
            activeDaemonProc = null;
            if (supervisorActive) {
                // 1.5s backoff matching entrypoint 2s backoff
                setTimeout(() => startSupervisedDaemon(), 1500);
            }
        });

        activeDaemonProc = proc;
    }

    startSupervisedDaemon();
    console.log("[Phase 2] Supervised daemon launched.");

    const statusPath = path.join(exchangeDir, "collector-status.json");

    // Wait for running state
    async function waitForState(expectedState, maxSeconds = 15) {
        for (let i = 0; i < maxSeconds * 10; i++) {
            await new Promise(r => setTimeout(r, 100));
            if (fs.existsSync(statusPath)) {
                try {
                    const stat = JSON.parse(fs.readFileSync(statusPath, "utf8"));
                    if (stat.collector_state === expectedState) return stat;
                } catch {}
            }
        }
        throw new Error(`Timed out waiting for collector_state = '${expectedState}'`);
    }

    const stat1 = await waitForState("running", 15);
    assert.strictEqual(stat1.discord_authenticated, true, "Must be authenticated");
    assert.strictEqual(stat1.last_error, null, "Must have null error");
    console.log("  ✔ Daemon initial boot reached 'running' and authenticated.");

    // Dispatch message 1
    const msg1 = {
        id: "1545225000000000201",
        channel_id: "2001",
        guild_id: "1001",
        content: "Pre-disconnect live event 1",
        timestamp: new Date().toISOString(),
        author: { id: "u1", username: "alice" }
    };
    mockServer.dispatchMessage("2001", msg1);

    await new Promise(r => setTimeout(r, 1000));
    const eventsFile = path.join(exchangeDir, "events", "0000000000000001.ndjson");
    let journal = fs.readFileSync(eventsFile, "utf8");
    assert.ok(journal.includes("1545225000000000201"), "Message 1 must be in journal");
    console.log("  ✔ Message 1 captured in journal before disconnect.");

    // Kill fake Discord IPC
    console.log("[Phase 3] Terminating fake Discord IPC (simulating Discord crash)...");
    await mockServer.stop();
    try { if (fs.existsSync(socketPath)) fs.unlinkSync(socketPath); } catch {}

    // Verify disconnect detected and false running status never persists
    const disconnectStat = await waitForState("discord_starting", 10);
    assert.strictEqual(disconnectStat.discord_authenticated, false, "Must report unauthenticated upon disconnect");
    assert.strictEqual(disconnectStat.last_error, "Discord IPC connection closed unexpectedly");
    console.log("  ✔ Disconnect detected immediately: status updated to 'discord_starting', last_error populated, false 'running' cleared.");

    // Verify the daemon process exited with code 1
    await new Promise(r => setTimeout(r, 1000));
    assert.ok(daemonExitCodes.length >= 1, "Daemon process must exit non-zero on unexpected disconnect");
    assert.strictEqual(daemonExitCodes[0].code, 1, "Exit code must be 1 for supervisor restart");
    console.log("  ✔ Daemon terminated with exit code 1 for clean supervisor backoff.");

    // Restart fake Discord IPC
    console.log("[Phase 4] Restarting fake Discord IPC on same socket (simulating Discord restart)...");
    mockServer = await startFakeDiscord();

    // Supervisor will restart daemon automatically
    console.log("[Phase 5] Waiting for supervised daemon auto-restart and re-authentication...");
    const stat2 = await waitForState("running", 20);
    assert.strictEqual(stat2.discord_authenticated, true, "Must re-authenticate after restart");
    assert.strictEqual(stat2.last_error, null, "Error must be cleared on successful reconnection");
    console.log("  ✔ Supervisor restarted daemon; re-authenticated unattended and reached 'running'.");

    // Dispatch message 2 post-reconnect
    console.log("[Phase 6] Dispatching post-reconnect MESSAGE_CREATE #2...");
    const msg2 = {
        id: "1545225000000000202",
        channel_id: "2001",
        guild_id: "1001",
        content: "Post-reconnect live event 2",
        timestamp: new Date().toISOString(),
        author: { id: "u2", username: "bob" }
    };
    mockServer.dispatchMessage("2001", msg2);

    await new Promise(r => setTimeout(r, 1500));
    journal = fs.readFileSync(eventsFile, "utf8");
    assert.ok(journal.includes("1545225000000000202"), "Message 2 must be captured post-reconnect");

    // Verify exact-once count: msg1 count === 1, msg2 count === 1
    const lines = journal.trim().split("\n").map(l => JSON.parse(l));
    const count1 = lines.filter(l => l.message_id === "1545225000000000201").length;
    const count2 = lines.filter(l => l.message_id === "1545225000000000202").length;
    assert.strictEqual(count1, 1, "Message 1 must appear exactly once");
    assert.strictEqual(count2, 1, "Message 2 must appear exactly once");
    console.log("  ✔ Both messages captured exactly once in physical journal without duplicates.");

    // Cleanup
    supervisorActive = false;
    if (activeDaemonProc) {
        try { activeDaemonProc.kill("SIGTERM"); } catch {}
    }
    await mockServer.stop().catch(() => {});
    try { fs.rmSync(rootDir, { recursive: true, force: true }); } catch {}

    console.log("\n=== ALL DISCONNECT / RECONNECT INVARIANTS VERIFIED (100%) ===");
    console.log("rpc_disconnect_reconnect_test_passed");
}

runDisconnectReconnectTest().catch(err => {
    console.error("rpc_disconnect_reconnect_test failed:", err);
    process.exit(1);
});
