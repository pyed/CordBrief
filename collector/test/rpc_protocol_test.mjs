/*
 * rpc_protocol_test.mjs
 * Automated tests verifying Discord RPC Transport and Protocol Client against Mock Server
 */

import assert from "assert";
import { MockDiscordRpcServer } from "./mock_discord_rpc.mjs";
import { RpcTransport } from "../rpc/transport.mjs";
import { DiscordRpcClient } from "../rpc/protocol.mjs";

async function runTest() {
    const mock = new MockDiscordRpcServer();
    const pipePath = await mock.start();

    const transport = new RpcTransport({ socketPath: pipePath });
    const client = new DiscordRpcClient(transport);

    try {
        // 1. Connect & Handshake
        const readyData = await transport.connect("1122334455");
        assert.ok(readyData);
        assert.strictEqual(readyData.user?.username, "cordbrief_test_user");

        // 2. Authorize
        const auth = await client.authorize({ clientId: "1122334455" });
        assert.ok(auth.code.startsWith("mock_auth_code_"));

        // 3. Authenticate
        const authResult = await client.authenticate("mock_access_token_abc");
        assert.ok(authResult.user);
        assert.deepStrictEqual(client.grantedScopes, ["rpc", "identify", "messages.read"]);

        // 4. Guilds and Channels discovery
        const guilds = await client.getGuilds();
        assert.strictEqual(guilds.length, 2);
        assert.strictEqual(guilds[0].name, "Alpha Guild");

        const channels1 = await client.getChannels(guilds[0].id);
        assert.strictEqual(channels1.length, 2);
        assert.strictEqual(channels1[0].name, "general");

        // 5. Subscription & Live Dispatch
        let receivedMessage = null;
        transport.on("dispatch", event => {
            if (event.evt === "MESSAGE_CREATE") {
                receivedMessage = event.data;
            }
        });

        await client.subscribeMessageCreate("2001");
        assert.ok(mock.subscriptions.has("2001:MESSAGE_CREATE"));

        const testMsg = {
            id: "123456789012345678",
            channel_id: "2001",
            content: "Live test message over RPC",
            author: { id: "555", username: "alice", global_name: "Alice", bot: false },
            timestamp: new Date().toISOString(),
            attachments: []
        };
        mock.dispatchMessage("2001", testMsg);

        // Await dispatch delivery
        await new Promise(r => setTimeout(r, 100));
        assert.ok(receivedMessage);
        assert.strictEqual(receivedMessage.channel_id, "2001");
        assert.strictEqual(receivedMessage.message.content, "Live test message over RPC");

        // 6. Unsubscribe
        await client.unsubscribeMessageCreate("2001");
        assert.ok(!mock.subscriptions.has("2001:MESSAGE_CREATE"));

        // 7. Get Channel snapshot
        mock.channelData["2001"] = {
            id: "2001",
            name: "general",
            type: 0,
            messages: [
                { id: "100", content: "Snapshot msg 1", author: { username: "bob" } },
                { id: "101", content: "Snapshot msg 2", author: { username: "charlie" } }
            ]
        };
        const ch = await client.getChannel("2001");
        assert.strictEqual(ch.id, "2001");
        assert.strictEqual(ch.messages.length, 2);

        console.log("rpc_protocol_test passed");
    } finally {
        transport.close();
        await mock.stop();
    }
}

runTest().catch(err => {
    console.error("rpc_protocol_test failed:", err);
    process.exit(1);
});
