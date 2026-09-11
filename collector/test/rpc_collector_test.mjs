/*
 * rpc_collector_test.mjs
 * End-to-end foundation tests for DiscordRpcCollector against Mock Discord RPC
 */

import assert from "assert";
import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import { MockDiscordRpcServer } from "./mock_discord_rpc.mjs";
import { DiscordRpcCollector } from "../rpc/collector.mjs";
import { RpcTransport } from "../rpc/transport.mjs";
import { DiscordRpcClient } from "../rpc/protocol.mjs";

async function runTest() {
    const tmpExchange = path.join(os.tmpdir(), `cordbrief-exchange-test-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    fs.mkdirSync(tmpExchange, { recursive: true });

    const mock = new MockDiscordRpcServer({
        guilds: [
            { id: "1001", name: "Alpha Guild" }
        ],
        channelsByGuild: {
            "1001": [
                { id: "2001", name: "general", type: 0 },
                { id: "2002", name: "random", type: 0 }
            ]
        },
        channelData: {
            "2001": {
                id: "2001",
                name: "general",
                type: 0,
                guild_id: "1001",
                messages: [
                    {
                        id: "100000000000000001",
                        channel_id: "2001",
                        content: "Historical snapshot message 1",
                        author: { id: "401", username: "alice", global_name: "Alice", bot: false },
                        timestamp: "2026-09-11T00:00:00.000Z",
                        attachments: []
                    },
                    {
                        id: "100000000000000002",
                        channel_id: "2001",
                        content: "Historical snapshot message 2",
                        author: { id: "402", username: "bob", global_name: "Bob", bot: false },
                        timestamp: "2026-09-11T00:01:00.000Z",
                        attachments: [
                            { id: "att1", filename: "doc.txt", content_type: "text/plain", size: 42 }
                        ]
                    }
                ]
            }
        }
    });

    const pipePath = await mock.start();

    // Write initial watchlist.json watching channel 2001
    const watchlistPath = path.join(tmpExchange, "watchlist.json");
    fs.writeFileSync(watchlistPath, JSON.stringify({
        version: 1,
        generation: 1,
        channel_ids: ["2001"]
    }, null, 2), "utf8");

    const transport = new RpcTransport({ socketPath: pipePath });
    const client = new DiscordRpcClient(transport);
    const collector = new DiscordRpcCollector({
        exchangeDir: tmpExchange,
        transport,
        client
    });

    try {
        // 1. Start collector
        await collector.start({
            clientId: "test_app_id_999",
            accessToken: "test_token_abc"
        });

        // 2. Verify catalog.json was written
        const catalogPath = path.join(tmpExchange, "catalog.json");
        assert.ok(fs.existsSync(catalogPath), "catalog.json must exist");
        const catalog = JSON.parse(fs.readFileSync(catalogPath, "utf8"));
        assert.strictEqual(catalog.version, 1);
        assert.strictEqual(catalog.guilds.length, 1);
        assert.strictEqual(catalog.guilds[0].name, "Alpha Guild");
        assert.strictEqual(catalog.guilds[0].channels.length, 2);

        // 3. Verify channel 2001 was subscribed
        assert.ok(mock.subscriptions.has("2001:MESSAGE_CREATE"), "Channel 2001 should be subscribed");

        // 4. Verify snapshot recovery messages were appended to events/0000000000000001.ndjson
        const eventsDir = path.join(tmpExchange, "events");
        const seg1Path = path.join(eventsDir, "0000000000000001.ndjson");
        assert.ok(fs.existsSync(seg1Path), "Segment 1 must exist");

        let lines = fs.readFileSync(seg1Path, "utf8").trim().split("\n").map(l => JSON.parse(l));
        assert.strictEqual(lines.length, 2, "Should have 2 snapshot messages");
        assert.strictEqual(lines[0].message_id, "100000000000000001");
        assert.strictEqual(lines[0].guild_id, "1001");
        assert.strictEqual(lines[0].author.name, "alice");
        assert.strictEqual(lines[1].message_id, "100000000000000002");
        assert.strictEqual(lines[1].attachments.length, 1);

        // 5. Dispatch a live MESSAGE_CREATE event
        mock.dispatchMessage("2001", {
            id: "100000000000000003",
            channel_id: "2001",
            content: "Live message from Discord",
            author: { id: "403", username: "charlie", global_name: "Charlie", bot: false },
            timestamp: "2026-09-11T00:02:00.000Z",
            message_reference: { message_id: "100000000000000002" },
            attachments: []
        });

        // Allow async write
        await new Promise(r => setTimeout(r, 100));

        lines = fs.readFileSync(seg1Path, "utf8").trim().split("\n").map(l => JSON.parse(l));
        assert.strictEqual(lines.length, 3, "Should now have 3 messages");
        assert.strictEqual(lines[2].message_id, "100000000000000003");
        assert.strictEqual(lines[2].reply_to_message_id, "100000000000000002");

        // 6. Test deduplication: re-dispatching message 3 must be ignored
        mock.dispatchMessage("2001", {
            id: "100000000000000003",
            channel_id: "2001",
            content: "Duplicate message 3",
            author: { id: "403", username: "charlie", bot: false },
            timestamp: "2026-09-11T00:02:00.000Z"
        });
        await new Promise(r => setTimeout(r, 100));

        lines = fs.readFileSync(seg1Path, "utf8").trim().split("\n").map(l => JSON.parse(l));
        assert.strictEqual(lines.length, 3, "Duplicate message must not be appended");

        // 7. Test unwatched channel filtering: message on unwatched channel 2002 must be dropped
        mock.dispatchMessage("2002", {
            id: "100000000000000099",
            channel_id: "2002",
            content: "Unwatched message",
            author: { id: "404", username: "dave", bot: false },
            timestamp: "2026-09-11T00:03:00.000Z"
        });
        await new Promise(r => setTimeout(r, 100));

        lines = fs.readFileSync(seg1Path, "utf8").trim().split("\n").map(l => JSON.parse(l));
        assert.strictEqual(lines.length, 3, "Unwatched channel message must not be appended");

        // 8. Verify collector-status.json
        const statusPath = path.join(tmpExchange, "collector-status.json");
        assert.ok(fs.existsSync(statusPath), "collector-status.json must exist");
        const status = JSON.parse(fs.readFileSync(statusPath, "utf8"));
        assert.strictEqual(status.version, 1);
        assert.strictEqual(status.collector_state, "running");
        assert.strictEqual(status.discord_authenticated, true);
        assert.strictEqual(status.catalog_state, "ready");
        assert.strictEqual(status.watched_channel_count, 1);
        assert.strictEqual(status.active_segment, 1);
        assert.strictEqual(status.recovery_state, "ready");

        console.log("rpc_collector_test passed");
    } finally {
        await collector.stop();
        await mock.stop();
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
    }
}

runTest().catch(err => {
    console.error("rpc_collector_test failed:", err);
    process.exit(1);
});
