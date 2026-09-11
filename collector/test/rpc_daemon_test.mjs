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

        console.log("rpc_daemon_test passed");
    } finally {
        await daemon.stop().catch(() => {});
        await mock.stop().catch(() => {});
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
    }
}

runDaemonTest().catch(err => {
    console.error("rpc_daemon_test failed:", err);
    process.exit(1);
});
