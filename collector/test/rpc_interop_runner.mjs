/*
 * rpc_interop_runner.mjs
 * Generates official RPC collector output in a target exchange directory for Go Core interop tests.
 */

import * as fs from "fs";
import * as path from "path";
import { MockDiscordRpcServer } from "./mock_discord_rpc.mjs";
import { DiscordRpcCollector } from "../rpc/collector.mjs";
import { RpcTransport } from "../rpc/transport.mjs";
import { DiscordRpcClient } from "../rpc/protocol.mjs";

const exchangeDir = process.argv[2];
if (!exchangeDir) {
    console.error("Usage: node rpc_interop_runner.mjs <exchangeDir>");
    process.exit(1);
}

async function main() {
    const mock = new MockDiscordRpcServer({
        guilds: [
            { id: "1001", name: "Alpha Guild" }
        ],
        channelsByGuild: {
            "1001": [
                { id: "2001", name: "general", type: 0 }
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
                        content: "Historical snapshot message",
                        author: { id: "401", username: "alice", global_name: "Alice", bot: false },
                        timestamp: "2026-09-11T00:00:00.000Z",
                        attachments: []
                    }
                ]
            }
        }
    });

    const pipePath = await mock.start();

    // Write initial watchlist
    fs.writeFileSync(path.join(exchangeDir, "watchlist.json"), JSON.stringify({
        version: 1,
        generation: 1,
        channel_ids: ["2001"]
    }, null, 2), "utf8");

    const transport = new RpcTransport({ socketPath: pipePath });
    const client = new DiscordRpcClient(transport);
    const collector = new DiscordRpcCollector({
        exchangeDir,
        transport,
        client
    });

    await collector.start({
        clientId: "interop_test_client",
        accessToken: "interop_test_token"
    });

    // Emit a live event with reply and attachment
    mock.dispatchMessage("2001", {
        id: "100000000000000002",
        channel_id: "2001",
        content: "Live message from RPC",
        author: { id: "402", username: "bob", global_name: "Bob", bot: false },
        timestamp: "2026-09-11T00:01:00.000Z",
        message_reference: { message_id: "100000000000000001" },
        attachments: [
            { id: "att1", filename: "test.png", content_type: "image/png", size: 1024 }
        ]
    });

    await new Promise(r => setTimeout(r, 150));

    // Force status update
    collector.writeStatus("running");

    await collector.stop();
    await mock.stop();
}

main().catch(err => {
    console.error(err);
    process.exit(1);
});
