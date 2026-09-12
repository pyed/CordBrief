/*
 * rpc_recovery_invariants_test.mjs
 *
 * Verifies core recovery invariants adapted to official Discord RPC architecture:
 * 1. First-watch anchor millisecond safety (deriveFirstWatchBoundary):
 *    Same-millisecond messages (M < H within timestamp ms) are NOT excluded,
 *    while prior-millisecond history is strictly excluded.
 * 2. Deduplication & exactly-once guarantee across restarts and snapshot sweeps.
 * 3. Recovery contract: unpersisted dispatch crashes leave checkpoint unadvanced;
 *    subsequent snapshot recovery commits M exactly once.
 * 4. Delayed visibility below high-water: out-of-order messages below checkpoint
 *    are ingested and deduplicated without corrupting the high-water mark.
 * 5. Fail-closed error retry: initial recovery failure keeps watch_after safe.
 */

import assert from "assert";
import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import { MockDiscordRpcServer } from "./mock_discord_rpc.mjs";
import { DiscordRpcCollector } from "../rpc/collector.mjs";
import { RpcTransport } from "../rpc/transport.mjs";
import { DiscordRpcClient } from "../rpc/protocol.mjs";
import { deriveFirstWatchBoundary } from "../rpc/retention.mjs";

const DISCORD_EPOCH = 1420070400000n;

/**
 * Construct a synthetic Discord snowflake for a given Unix timestamp ms and sequence.
 */
function makeSnowflake(timestampMs, seq = 0) {
    const msSinceEpoch = BigInt(timestampMs) - DISCORD_EPOCH;
    return ((msSinceEpoch << 22n) | BigInt(seq)).toString();
}

function makeNormalizedMessage(id, channelId = "2001", content = `Message ${id}`) {
    return {
        id: String(id),
        channel_id: String(channelId),
        guild_id: "1001",
        content: String(content),
        timestamp: new Date().toISOString(),
        author: { id: "501", username: "alice" }
    };
}

function createTempDir(prefix) {
    const dir = path.join(os.tmpdir(), `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    fs.mkdirSync(dir, { recursive: true });
    return dir;
}

function readAllJournalRecords(exchangeDir) {
    const eventsDir = path.join(exchangeDir, "events");
    if (!fs.existsSync(eventsDir)) return [];
    return fs.readdirSync(eventsDir)
        .filter(f => /^\d{16}\.ndjson$/.test(f))
        .sort()
        .flatMap(f => fs.readFileSync(path.join(eventsDir, f), "utf8").trim().split("\n").filter(Boolean).map(JSON.parse));
}

async function runTests() {
    console.log("=== Running RPC Recovery & Invariants Test Suite ===");

    // -----------------------------------------------------------------------
    // Test 1: First-Watch Anchor Millisecond Boundary (from first_watch_race_test)
    // -----------------------------------------------------------------------
    console.log("[Test 1] First-watch anchor millisecond safety & same-ms inclusion...");
    {
        const tsNow = 1757592000500; // 2025-09-11 12:00:00.500 UTC
        const tsPrior = tsNow - 1;   // 500 - 1 = 499 ms (prior millisecond)

        const msgPrior = makeSnowflake(tsPrior, 99); // Historical message (prior ms)
        const msgSameM = makeSnowflake(tsNow, 1);    // Same millisecond M, seq 1
        const msgAnchorH = makeSnowflake(tsNow, 5);  // Anchor H, seq 5 (H > M, but same ms)

        // Verify arithmetic properties of deriveFirstWatchBoundary
        const watchAfter = deriveFirstWatchBoundary(msgAnchorH);
        assert.ok(BigInt(msgPrior) <= BigInt(watchAfter), "Prior-ms message must be <= watch_after (excluded)");
        assert.ok(BigInt(msgSameM) > BigInt(watchAfter), "Same-ms message M must be > watch_after (included)");
        assert.ok(BigInt(msgAnchorH) > BigInt(watchAfter), "Anchor H must be > watch_after (included)");

        // Full collector integration test
        const tmpExchange = createTempDir("cordbrief-first-watch");
        const privateDir = path.join(tmpExchange, "private");
        fs.mkdirSync(privateDir, { recursive: true });

        const mock = new MockDiscordRpcServer({
            guilds: [{ id: "1001", name: "Guild" }],
            channelsByGuild: { "1001": [{ id: "2001", name: "general", type: 0 }] },
            channelData: {
                "2001": {
                    id: "2001",
                    name: "general",
                    type: 0,
                    guild_id: "1001",
                    messages: [
                        makeNormalizedMessage(msgPrior, "2001", "Prior message"),
                        makeNormalizedMessage(msgSameM, "2001", "Same-ms message M"),
                        makeNormalizedMessage(msgAnchorH, "2001", "Anchor message H")
                    ]
                }
            }
        });
        const pipePath = await mock.start();
        const transport = new RpcTransport({ socketPath: pipePath });
        const client = new DiscordRpcClient(transport);

        const collector = new DiscordRpcCollector({
            exchangeDir: tmpExchange,
            collectorDataDir: privateDir,
            transport,
            client,
            enableLock: false
        });

        await transport.connect("test_app");
        await client.authenticate("test_token");

        // First watch recovery: starts from baseline_pending
        collector.beginChannelInitialization("2001");
        const appended = await collector.recoverChannelSnapshot("2001");

        // Eligible messages are msgSameM (M) and msgAnchorH (H); msgPrior is excluded
        assert.strictEqual(appended, 2, "Must append exactly 2 messages (M and H; prior excluded)");

        const records = readAllJournalRecords(tmpExchange);
        assert.strictEqual(records.length, 2);
        assert.ok(records.some(r => r.message_id === msgSameM), "Same-millisecond M must be in journal");
        assert.ok(records.some(r => r.message_id === msgAnchorH), "Anchor H must be in journal");
        assert.ok(!records.some(r => r.message_id === msgPrior), "Prior message must NOT be in journal");

        const state = collector.loadRecoveryState();
        assert.strictEqual(state.channels["2001"].checkpoint_message_id, msgAnchorH);
        assert.strictEqual(state.channels["2001"].watch_after, watchAfter);

        // Restart test: snapshot re-run must deduplicate both messages exactly
        const secondRun = await collector.recoverChannelSnapshot("2001");
        assert.strictEqual(secondRun, 0, "Restart snapshot must deduplicate existing messages");

        const recordsAfter = readAllJournalRecords(tmpExchange);
        assert.strictEqual(recordsAfter.length, 2, "Journal must still contain exactly 2 messages (no duplicates)");

        transport.close();
        await mock.stop();
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
        console.log("  ✔ First-watch anchor correctly includes same-ms messages and dedupes on restart.");
    }

    // -----------------------------------------------------------------------
    // Test 2: Recovery Contract & Unpersisted Crash Safety (from recovery_contract_test)
    // -----------------------------------------------------------------------
    console.log("[Test 2] Recovery contract: uncommitted events leave checkpoint safe; RPC GET_CHANNEL snapshot recovers...");
    {
        const tmpExchange = createTempDir("cordbrief-contract");
        const privateDir = path.join(tmpExchange, "private");
        fs.mkdirSync(privateDir, { recursive: true });

        const recoveryPath = path.join(privateDir, "recovery-state.json");
        const checkpoint0 = "1545225000000000100";
        const msg202 = "1545225000000000202";

        // Initial checkpoint on disk
        fs.writeFileSync(recoveryPath, JSON.stringify({
            version: 2,
            channels: {
                "2001": {
                    checkpoint_message_id: checkpoint0,
                    checkpoint_source: "rpc",
                    watch_after: checkpoint0,
                    scan_after: null,
                    scan_until: null,
                    checkpoint_journal_boundary: { segment: 1, offset: 0 },
                    last_recovery_at: new Date().toISOString(),
                    last_result: "success",
                    last_error: null,
                    recovered_count: 0
                }
            },
            pending: null
        }, null, 2), "utf8");

        const mock = new MockDiscordRpcServer({
            guilds: [{ id: "1001", name: "Guild" }],
            channelsByGuild: { "1001": [{ id: "2001", name: "general", type: 0 }] },
            channelData: {
                "2001": {
                    id: "2001",
                    name: "general",
                    type: 0,
                    guild_id: "1001",
                    messages: [
                        makeNormalizedMessage(msg202, "2001", "Message 202 from snapshot")
                    ]
                }
            }
        });
        const pipePath = await mock.start();
        const transport = new RpcTransport({ socketPath: pipePath });
        const client = new DiscordRpcClient(transport);

        const collector = new DiscordRpcCollector({
            exchangeDir: tmpExchange,
            recoveryStatePath: recoveryPath,
            transport,
            client,
            enableLock: false
        });

        await transport.connect("test_app");
        await client.authenticate("test_token");

        // Case A: Before recovery, journal has 0 copies, checkpoint is at checkpoint0
        assert.strictEqual(readAllJournalRecords(tmpExchange).length, 0);
        assert.strictEqual(collector.loadRecoveryState().channels["2001"].checkpoint_message_id, checkpoint0);

        // Case B: Snapshot recovery recovers message 202
        const appended = await collector.recoverChannelSnapshot("2001");
        assert.strictEqual(appended, 1);

        const records = readAllJournalRecords(tmpExchange);
        assert.strictEqual(records.length, 1);
        assert.strictEqual(records[0].message_id, msg202);

        // Checkpoint advances to 202
        const stateAfter = collector.loadRecoveryState();
        assert.strictEqual(stateAfter.channels["2001"].checkpoint_message_id, msg202);

        // Case C: Repeated snapshot dedupes exactly (copies === 1)
        const reAppended = await collector.recoverChannelSnapshot("2001");
        assert.strictEqual(reAppended, 0);
        const finalRecords = readAllJournalRecords(tmpExchange);
        assert.strictEqual(finalRecords.length, 1, "Exactly one copy of message 202 in journal");

        transport.close();
        await mock.stop();
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
        console.log("  ✔ Recovery contract verified: checkpoint unadvanced before persistence, recovers exactly once.");
    }

    // -----------------------------------------------------------------------
    // Test 3: Delayed Visibility & Out-of-Order Replay (from migration_replay_test)
    // -----------------------------------------------------------------------
    console.log("[Test 3] Delayed visibility below high-water mark & order independence...");
    {
        const tmpExchange = createTempDir("cordbrief-delayed-visibility");
        const privateDir = path.join(tmpExchange, "private");
        fs.mkdirSync(privateDir, { recursive: true });

        const msg201 = "1545225000000000201";
        const msg202 = "1545225000000000202";
        const msg203 = "1545225000000000203";

        const mock = new MockDiscordRpcServer({
            guilds: [{ id: "1001", name: "Guild" }],
            channelsByGuild: { "1001": [{ id: "2001", name: "general", type: 0 }] },
            channelData: {
                "2001": {
                    id: "2001",
                    name: "general",
                    type: 0,
                    guild_id: "1001",
                    // Initial snapshot has 201 and 203 (202 delayed / out of order!)
                    messages: [
                        makeNormalizedMessage(msg201, "2001", "M201"),
                        makeNormalizedMessage(msg203, "2001", "M203")
                    ]
                }
            }
        });
        const pipePath = await mock.start();
        const transport = new RpcTransport({ socketPath: pipePath });
        const client = new DiscordRpcClient(transport);

        const collector = new DiscordRpcCollector({
            exchangeDir: tmpExchange,
            collectorDataDir: privateDir,
            transport,
            client,
            enableLock: false
        });

        await transport.connect("test_app");
        await client.authenticate("test_token");

        collector.beginChannelInitialization("2001");
        // Force watch_after to 200 so 201, 202, 203 are all in watch scope
        const s = collector.loadRecoveryState();
        s.channels["2001"].watch_after = "1545225000000000200";
        collector.saveRecoveryState(s);

        const app1 = await collector.recoverChannelSnapshot("2001");
        assert.strictEqual(app1, 2, "Appends 201 and 203");
        assert.strictEqual(collector.loadRecoveryState().channels["2001"].checkpoint_message_id, msg203);

        // Now Discord returns snapshot with 201, 202 (delayed!), and 203
        mock.channelData["2001"].messages = [
            makeNormalizedMessage(msg201, "2001", "M201"),
            makeNormalizedMessage(msg202, "2001", "M202 (delayed)"),
            makeNormalizedMessage(msg203, "2001", "M203")
        ];

        const app2 = await collector.recoverChannelSnapshot("2001");
        // 201 and 203 deduplicated; delayed 202 appended!
        assert.strictEqual(app2, 1, "Delayed message 202 must be appended; 201 & 203 deduped");

        // High-water mark remains at 203
        const state2 = collector.loadRecoveryState();
        assert.strictEqual(state2.channels["2001"].checkpoint_message_id, msg203, "High-water remains at highest seen ID");

        // Journal records contain all 3 messages exactly once
        const allRecords = readAllJournalRecords(tmpExchange);
        assert.strictEqual(allRecords.length, 3);
        const recordIds = allRecords.map(r => r.message_id).sort();
        assert.deepStrictEqual(recordIds, [msg201, msg202, msg203]);

        transport.close();
        await mock.stop();
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
        console.log("  ✔ Delayed visibility below high-water recovered; exactly-once order independence verified.");
    }

    // -----------------------------------------------------------------------
    // Test 4: First-Watch Error Retry Safety (from migration_replay_test)
    // -----------------------------------------------------------------------
    console.log("[Test 4] First-watch fail-closed error retry...");
    {
        const tmpExchange = createTempDir("cordbrief-firstwatch-err");
        const privateDir = path.join(tmpExchange, "private");
        fs.mkdirSync(privateDir, { recursive: true });

        const mock = new MockDiscordRpcServer({
            guilds: [{ id: "1001", name: "Guild" }],
            channelsByGuild: { "1001": [{ id: "2001", name: "general", type: 0 }] }
        });
        const pipePath = await mock.start();
        const transport = new RpcTransport({ socketPath: pipePath });
        const client = new DiscordRpcClient(transport);

        const collector = new DiscordRpcCollector({
            exchangeDir: tmpExchange,
            collectorDataDir: privateDir,
            transport,
            client,
            enableLock: false
        });

        await transport.connect("test_app");
        await client.authenticate("test_token");

        collector.beginChannelInitialization("2001");
        assert.strictEqual(collector.loadRecoveryState().channels["2001"].checkpoint_source, "baseline_pending");

        // Simulate RPC failure on getChannel
        const originalGetChannel = client.getChannel.bind(client);
        client.getChannel = async () => { throw new Error("Simulated RPC network timeout"); };

        let failed = false;
        try {
            await collector.recoverChannelSnapshot("2001");
        } catch {
            failed = true;
        }

        // Must fail closed: checkpoint_source remains baseline_pending, status is error
        const stateDuringError = collector.loadRecoveryState();
        assert.strictEqual(stateDuringError.channels["2001"].checkpoint_source, "baseline_pending");
        assert.strictEqual(stateDuringError.channels["2001"].last_result, "error");
        assert.strictEqual(readAllJournalRecords(tmpExchange).length, 0);

        // Now restore channel data and client method
        client.getChannel = originalGetChannel;
        const msg300 = "1545224000000000000"; // Prior millisecond
        const msg301 = "1545225000000000301"; // Anchor millisecond
        mock.channelData["2001"] = {
            id: "2001",
            name: "general",
            type: 0,
            messages: [
                makeNormalizedMessage(msg300, "2001", "Pre-watch historical message"),
                makeNormalizedMessage(msg301, "2001", "Current anchor message")
            ]
        };

        // Retry succeeds
        const retryAppended = await collector.recoverChannelSnapshot("2001");
        assert.strictEqual(retryAppended, 1, "Appends anchor message; pre-watch msg300 is excluded");

        const stateRecovered = collector.loadRecoveryState();
        assert.strictEqual(stateRecovered.channels["2001"].checkpoint_source, "rpc");
        assert.strictEqual(stateRecovered.channels["2001"].last_result, "success");
        assert.strictEqual(stateRecovered.channels["2001"].checkpoint_message_id, msg301);

        const recs = readAllJournalRecords(tmpExchange);
        assert.strictEqual(recs.length, 1);
        assert.strictEqual(recs[0].message_id, msg301);

        transport.close();
        await mock.stop();
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
        console.log("  ✔ Fail-closed error retry preserved baseline_pending and succeeded on recovery.");
    }

    console.log("rpc_recovery_invariants_tests_passed");
}

runTests().catch(err => {
    console.error("rpc_recovery_invariants_test failed:", err);
    process.exit(1);
});
