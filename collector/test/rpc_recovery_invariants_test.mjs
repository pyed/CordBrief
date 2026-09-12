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
import * as crypto from "crypto";
import { MockDiscordRpcServer } from "./mock_discord_rpc.mjs";
import { DiscordRpcCollector } from "../rpc/collector.mjs";
import { RpcTransport } from "../rpc/transport.mjs";
import { DiscordRpcClient } from "../rpc/protocol.mjs";
import { deriveFirstWatchBoundary } from "../rpc/retention.mjs";

function sha256(buf) {
    return crypto.createHash("sha256").update(buf).digest("hex");
}

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
        version: 1,
        event: "message_create",
        id: String(id),
        message_id: String(id),
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

    // -------------------------------------------------------------
    // Invariant 6: Corrupt recovery state fail-closed protection
    // An existing watched channel with events in the journal cannot be
    // converted into a fresh first-watch anchor if recovery-state.json
    // becomes malformed or corrupt. Outage messages must NOT be masked.
    // -------------------------------------------------------------
    {
        console.log("\n[Invariant 6] Corrupt recovery-state fail-closed protection...");
        const tmpExchange = createTempDir("cb-inv6");
        const privateDir = path.join(tmpExchange, "private");
        fs.mkdirSync(privateDir, { recursive: true });

        const mock = new MockDiscordRpcServer();
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

        // Step 1: Channel 2001 is initially watched and captures message 1
        const t0 = 1750000000000n;
        const msg1 = ((t0 << 22n) | 1n).toString();
        const msg2 = (((t0 + 5000n) << 22n) | 1n).toString(); // 5000ms later (distinct millisecond timestamp)
        mock.channelData["2001"] = {
            id: "2001",
            name: "general",
            type: 0,
            messages: [makeNormalizedMessage(msg1, "2001", "Seed message")]
        };
        await collector.recoverChannelSnapshot("2001");
        assert.strictEqual(readAllJournalRecords(tmpExchange).length, 1);
        const validState = collector.loadRecoveryState();
        assert.strictEqual(validState.channels["2001"].checkpoint_message_id, msg1);

        // Step 2: Collector offline. Outage message 2 occurs in Discord (distinct millisecond)
        mock.channelData["2001"].messages.push(makeNormalizedMessage(msg2, "2001", "Outage message 2"));

        // Step 3: recovery-state.json becomes corrupt (truncated/invalid JSON)
        const recoveryStateFile = path.join(privateDir, "recovery-state.json");
        fs.writeFileSync(recoveryStateFile, "{\"version\": 2, \"channels\": { CORRUPT_DATA...");

        // Verify loadRecoveryState() refuses to return clean fresh state and throws fail-closed error
        assert.throws(() => {
            collector.loadRecoveryState();
        }, /Corrupt recovery-state\.json/);

        // Verify beginChannelInitialization() refuses to re-initialize an existing journal channel with corrupt state
        assert.throws(() => {
            collector.beginChannelInitialization("2001");
        }, /Corrupt recovery-state\.json|Cannot initialize existing/);

        // Verify beginChannelInitialization() refuses to re-anchor an existing journal channel missing checkpoint
        fs.writeFileSync(recoveryStateFile, JSON.stringify({ version: 2, channels: { "2001": {} } }));
        assert.throws(() => {
            collector.beginChannelInitialization("2001");
        }, /Cannot initialize existing journal channel 2001|Corrupt recovery-state\.json/);

        // Re-corrupt recovery-state.json for snapshot and live-append fail-closed tests
        fs.writeFileSync(recoveryStateFile, "{\"version\": 2, \"channels\": { CORRUPT_DATA...");

        // Verify recoverChannelSnapshot fails closed:
        // Sets recoveryStateStatus to 'error', does NOT overwrite corrupt file, does NOT re-anchor to msg2
        const recoveredCount = await collector.recoverChannelSnapshot("2001");
        assert.strictEqual(recoveredCount, 0, "Recovery must fail closed on corrupt recovery state");
        assert.strictEqual(collector.recoveryStateStatus, "error");
        assert.strictEqual(collector.collectorState, "error");
        assert.ok(collector.recoveryLastError.includes("Corrupt recovery-state.json"));

        // Verify the corrupt recovery-state file was NOT overwritten with a clean baseline
        const onDiskRaw = fs.readFileSync(recoveryStateFile, "utf8");
        assert.ok(onDiskRaw.includes("CORRUPT_DATA"), "Corrupt file must not be silently replaced");

        // Verify live message handler also fails closed against corrupt recovery state
        collector.activeSubscriptions.add("2001");
        collector.handleMessageCreate({
            channel_id: "2001",
            message: makeNormalizedMessage((((t0 + 10000n) << 22n) | 1n).toString(), "2001", "Live message during corruption")
        });
        assert.ok(collector.lastError.includes("Recovery state error"), "Must refuse live appends against corrupt recovery state");

        // Step 4: Repair recovery state by restoring valid state
        fs.writeFileSync(recoveryStateFile, JSON.stringify(validState));
        const recoveredAfterFix = await collector.recoverChannelSnapshot("2001");
        assert.strictEqual(recoveredAfterFix, 1, "Outage message 2 must be captured once recovery state is restored");
        assert.strictEqual(readAllJournalRecords(tmpExchange).length, 2, "Both msg1 and msg2 must be in journal");

        transport.close();
        await mock.stop();
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
        console.log("  ✔ Corrupt recovery state fail-closed protection verified: outage messages cannot be lost.");
    }

    // =========================================================================
    // =========================================================================
    // Invariant 7: Uncertain journal append fail-stop (file fsync EIO & directory fsync EIO)
    // =========================================================================
    {
        console.log("\n[Invariant 7A] Uncertain journal append fail-stop after file fsync failure...");
        const tmpExchangeA = path.join(os.tmpdir(), `cordbrief-invar7a-${Date.now()}-${Math.random().toString(36).slice(2)}`);
        fs.mkdirSync(tmpExchangeA, { recursive: true });
        const privateDirA = path.join(tmpExchangeA, "private");
        fs.mkdirSync(privateDirA, { recursive: true });

        let fileSyncFail = false;
        const faultFsync = (fd) => {
            if (fileSyncFail) {
                const err = new Error("EIO: input/output error during file fsync");
                err.code = "EIO";
                throw err;
            }
            fs.fsyncSync(fd);
        };

        const collector1A = new DiscordRpcCollector({
            exchangeDir: tmpExchangeA,
            collectorDataDir: privateDirA,
            runtimeDir: path.join(tmpExchangeA, "runtime"),
            enableLock: false,
            fsyncFn: faultFsync
        });
        collector1A.initJournal();

        const t0 = 1750000000000n;
        const msgA = ((t0 << 22n) | 1n).toString();
        const evtA = makeNormalizedMessage(msgA, "2001", "Uncertain append message (file sync)");

        fileSyncFail = true;
        let fatalFiredA = false;
        collector1A.on("fatal_error", () => { fatalFiredA = true; });

        // Appending must throw fatal journal error because bytes were written to file before fsync failed
        assert.throws(() => {
            collector1A.appendEvents([evtA]);
        }, /Fatal journal error: sync failure after writing record/);

        assert.strictEqual(fatalFiredA, true, "Must emit fatal_error on uncertain append");
        assert.strictEqual(collector1A.collectorState, "error");
        assert.ok(collector1A.fatalError.includes("sync failure after writing record"));
        assert.strictEqual(collector1A.stopping, true);

        // Subsequent append refused in fail-stop state
        assert.throws(() => {
            collector1A.appendEvents([evtA]);
        }, /Collector is in fail-stop state/);

        // Verify status file on disk
        const statusFileA = path.join(tmpExchangeA, "collector-status.json");
        assert.ok(fs.existsSync(statusFileA));
        const statusRecordA = JSON.parse(fs.readFileSync(statusFileA, "utf8"));
        assert.strictEqual(statusRecordA.collector_state, "error");
        assert.ok(statusRecordA.last_error.includes("Fatal journal error"));

        // Record reached disk physically
        const rawRecordsA = readAllJournalRecords(tmpExchangeA);
        assert.strictEqual(rawRecordsA.length, 1, "Record reached disk before fsync threw");
        assert.strictEqual(rawRecordsA[0].message_id, msgA);

        // Restarted collector rebuilds and dedupes
        fileSyncFail = false;
        const collector2A = new DiscordRpcCollector({
            exchangeDir: tmpExchangeA,
            collectorDataDir: privateDirA,
            runtimeDir: path.join(tmpExchangeA, "runtime"),
            enableLock: false,
            fsyncFn: faultFsync
        });
        collector2A.initJournal();
        assert.strictEqual(collector2A.journalRecordCount, 1);
        assert.strictEqual(collector2A.recentMessageIds.has(msgA), true);
        const retryAppendedA = collector2A.appendEvents([evtA]);
        assert.strictEqual(retryAppendedA, 0, "Restarted collector must dedupe existing record");
        assert.strictEqual(readAllJournalRecords(tmpExchangeA).length, 1, "Exactly one record on disk");
        try { fs.rmSync(tmpExchangeA, { recursive: true, force: true }); } catch {}

        console.log("\n[Invariant 7B] Uncertain journal append fail-stop after directory fsync failure...");
        const tmpExchangeB = path.join(os.tmpdir(), `cordbrief-invar7b-${Date.now()}-${Math.random().toString(36).slice(2)}`);
        fs.mkdirSync(tmpExchangeB, { recursive: true });
        const privateDirB = path.join(tmpExchangeB, "private");
        fs.mkdirSync(privateDirB, { recursive: true });

        let dirSyncFail = false;
        const faultSyncDir = (dir) => {
            if (dirSyncFail) {
                const err = new Error("EIO: input/output error during directory sync");
                err.code = "EIO";
                throw err;
            }
        };

        const collector1B = new DiscordRpcCollector({
            exchangeDir: tmpExchangeB,
            collectorDataDir: privateDirB,
            runtimeDir: path.join(tmpExchangeB, "runtime"),
            enableLock: false,
            syncDirectoryFn: faultSyncDir
        });
        collector1B.initJournal();

        const msgB = (((t0 + 1000n) << 22n) | 1n).toString();
        const evtB = makeNormalizedMessage(msgB, "2001", "Uncertain append message (dir sync)");

        dirSyncFail = true;
        let fatalFiredB = false;
        collector1B.on("fatal_error", () => { fatalFiredB = true; });

        assert.throws(() => {
            collector1B.appendEvents([evtB]);
        }, /Fatal journal error: sync failure after writing record/);

        assert.strictEqual(fatalFiredB, true, "Must emit fatal_error on uncertain append");
        assert.strictEqual(collector1B.collectorState, "error");
        assert.ok(collector1B.fatalError.includes("sync failure after writing record"));
        assert.strictEqual(collector1B.stopping, true);

        assert.throws(() => {
            collector1B.appendEvents([evtB]);
        }, /Collector is in fail-stop state/);

        const statusFileB = path.join(tmpExchangeB, "collector-status.json");
        assert.ok(fs.existsSync(statusFileB));
        const statusRecordB = JSON.parse(fs.readFileSync(statusFileB, "utf8"));
        assert.strictEqual(statusRecordB.collector_state, "error");

        const rawRecordsB = readAllJournalRecords(tmpExchangeB);
        assert.strictEqual(rawRecordsB.length, 1);
        assert.strictEqual(rawRecordsB[0].message_id, msgB);

        dirSyncFail = false;
        const collector2B = new DiscordRpcCollector({
            exchangeDir: tmpExchangeB,
            collectorDataDir: privateDirB,
            runtimeDir: path.join(tmpExchangeB, "runtime"),
            enableLock: false,
            syncDirectoryFn: faultSyncDir
        });
        collector2B.initJournal();
        assert.strictEqual(collector2B.journalRecordCount, 1);
        assert.strictEqual(collector2B.recentMessageIds.has(msgB), true);
        const retryAppendedB = collector2B.appendEvents([evtB]);
        assert.strictEqual(retryAppendedB, 0);
        assert.strictEqual(readAllJournalRecords(tmpExchangeB).length, 1);
        try { fs.rmSync(tmpExchangeB, { recursive: true, force: true }); } catch {}

        console.log("  ✔ Uncertain append fail-stop & restart deduplication verified (file & directory fsync).");
    }

    // =========================================================================
    // Invariant 8: Pending-baseline restart protection against re-anchoring existing channels
    // =========================================================================
    {
        console.log("\n[Invariant 8] Pending-baseline restart protection against re-anchoring existing channels...");
        const tmpExchange = path.join(os.tmpdir(), `cordbrief-invar8-${Date.now()}-${Math.random().toString(36).slice(2)}`);
        fs.mkdirSync(tmpExchange, { recursive: true });
        const privateDir = path.join(tmpExchange, "private");
        fs.mkdirSync(privateDir, { recursive: true });

        const t0 = 1750000000000n;
        const msg1 = ((t0 << 22n) | 1n).toString();
        const msg2 = (((t0 + 5000n) << 22n) | 1n).toString(); // 5s later
        const msg3 = (((t0 + 10000n) << 22n) | 1n).toString(); // 10s later
        const msgNewCh = (((t0 + 15000n) << 22n) | 1n).toString(); // 15s later

        const mock = new MockDiscordRpcServer({
            guilds: [{ id: "1001", name: "Alpha Guild" }],
            channelsByGuild: { "1001": [
                { id: "2001", name: "test-channel", type: 0 },
                { id: "3001", name: "brand-new-channel", type: 0 }
            ] },
            channelData: {
                "2001": {
                    id: "2001",
                    name: "test-channel",
                    type: 0,
                    guild_id: "1001",
                    messages: [
                        makeNormalizedMessage(msg1, "2001", "Msg 1"),
                        makeNormalizedMessage(msg2, "2001", "Msg 2 (outage)"),
                        makeNormalizedMessage(msg3, "2001", "Msg 3 (latest)")
                    ]
                },
                "3001": {
                    id: "3001",
                    name: "brand-new-channel",
                    type: 0,
                    guild_id: "1001",
                    messages: [
                        makeNormalizedMessage(msgNewCh, "3001", "New channel seed")
                    ]
                }
            }
        });
        const pipePath = await mock.start();
        const transport = new RpcTransport({ socketPath: pipePath });
        const client = new DiscordRpcClient(transport);
        await transport.connect("test_app");
        await client.authenticate("test_token");

        // 1. Pre-seed durable journal with Msg 1 for channel 2001
        const collectorSetup = new DiscordRpcCollector({
            exchangeDir: tmpExchange,
            collectorDataDir: privateDir,
            runtimeDir: path.join(tmpExchange, "runtime"),
            transport,
            client,
            enableLock: false
        });
        collectorSetup.initJournal();
        collectorSetup.appendEvents([makeNormalizedMessage(msg1, "2001", "Msg 1")]);
        assert.strictEqual(readAllJournalRecords(tmpExchange).length, 1);

        // 2. Simulate interrupted initialization / crash:
        // recovery-state.json has baseline_pending for channel 2001 (unanchored / no checkpoint)
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

        // 3. Process restarts: new collector starts against existing durable journal
        const collector = new DiscordRpcCollector({
            exchangeDir: tmpExchange,
            collectorDataDir: privateDir,
            runtimeDir: path.join(tmpExchange, "runtime"),
            transport,
            client,
            enableLock: false
        });
        collector.initJournal();

        // Verify channel 2001 has prior collection evidence in physical journal
        assert.strictEqual(collector.hasPriorCollectionEvidence("2001"), true);

        // 4. recoverChannelSnapshot("2001") MUST FAIL CLOSED:
        // Must NOT silently re-anchor to msg3 and skip outage msg2!
        const recovered = await collector.recoverChannelSnapshot("2001");
        assert.strictEqual(recovered, 0, "Recovery must fail closed");
        assert.strictEqual(collector.recoveryStateStatus, "error");
        assert.strictEqual(collector.collectorState, "error");
        assert.ok(collector.recoveryLastError.includes("baseline_pending") || collector.recoveryLastError.includes("existing durable"));

        // Verify state on disk was NOT updated to re-anchor to msg3
        const diskState = JSON.parse(fs.readFileSync(recoveryStateFile, "utf8"));
        assert.notStrictEqual(diskState.channels["2001"].checkpoint_message_id, msg3);
        assert.notStrictEqual(diskState.channels["2001"].watch_after, msg3);

        // 5. Test adding a genuinely new channel (3001) to an existing installation:
        // First repair 2001 state so recovery state is valid
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
        assert.strictEqual(collector.hasPriorCollectionEvidence("3001"), false);
        const newChRecovered = await collector.recoverChannelSnapshot("3001");
        assert.strictEqual(newChRecovered, 1, "Baseline anchor consumes seed message in anchor millisecond");
        const diskStateAfter = JSON.parse(fs.readFileSync(recoveryStateFile, "utf8"));
        assert.strictEqual(diskStateAfter.channels["3001"].checkpoint_source, "rpc");
        assert.ok(diskStateAfter.channels["3001"].checkpoint_message_id.length > 0);

        // 5B. Astra Gap B: Interrupted first-watch initialization & restart protection against re-anchoring
        const tsH1 = t0 + 100000n;
        const tsH2 = t0 + 101000n;
        const tsM1 = t0 + 105000n;
        const tsM2 = t0 + 106000n;
        const tsM3 = t0 + 107000n;

        const msgH1 = ((tsH1 << 22n) | 1n).toString();
        const msgH2 = ((tsH2 << 22n) | 1n).toString();
        const msgM1 = ((tsM1 << 22n) | 1n).toString();
        const msgM2 = ((tsM2 << 22n) | 1n).toString();
        const msgM3 = ((tsM3 << 22n) | 1n).toString();

        const expectedBaseline = deriveFirstWatchBoundary(msgH2);

        mock.channelsByGuild["1001"].push({ id: "5001", name: "gap-b-channel", type: 0 });
        mock.channelData["5001"] = {
            id: "5001",
            name: "gap-b-channel",
            type: 0,
            guild_id: "1001",
            messages: [
                makeNormalizedMessage(msgH1, "5001", "Historical H1"),
                makeNormalizedMessage(msgH2, "5001", "Historical H2")
            ]
        };

        // 1. Establish ORIGINAL durable first-watch message baseline
        await collector.establishFirstWatchBaseline("5001");
        const baseState = collector.loadRecoveryState();
        assert.strictEqual(baseState.channels["5001"].watch_after, expectedBaseline);
        assert.strictEqual(baseState.channels["5001"].checkpoint_source, "rpc");
        assert.strictEqual(baseState.channels["5001"].last_result, "pending");

        // 2. Live message M1 arrives and is captured in journal
        collector.activeSubscriptions.add("5001");
        collector.handleMessageCreate({
            channel_id: "5001",
            message: makeNormalizedMessage(msgM1, "5001", "Live M1")
        });
        const journalPreCrash = readAllJournalRecords(tmpExchange).filter(r => r.channel_id === "5001");
        assert.strictEqual(journalPreCrash.length, 1, "M1 must be captured in journal before crash");
        assert.strictEqual(journalPreCrash[0].message_id, msgM1);

        // 3. Crash before initialization completes (simulate process restart)
        // During downtime, M2 and M3 arrive on Discord
        // As specified by Astra: restart recovery snapshot contains M1/M2/M3
        mock.channelData["5001"].messages = [
            makeNormalizedMessage(msgM1, "5001", "Live M1"),
            makeNormalizedMessage(msgM2, "5001", "Outage M2"),
            makeNormalizedMessage(msgM3, "5001", "Outage M3")
        ];

        // Restart: new collector instance pointing to same exchangeDir and privateDir
        const restartedCollector = new DiscordRpcCollector({
            exchangeDir: tmpExchange,
            collectorDataDir: privateDir,
            runtimeDir: path.join(tmpExchange, "runtime"),
            transport,
            client,
            enableLock: false
        });
        restartedCollector.initJournal();

        // 4. Run recovery snapshot on restarted collector
        const recoveredRestart = await restartedCollector.recoverChannelSnapshot("5001");
        assert.strictEqual(recoveredRestart, 2, "Must recover M2 and M3 (M1 deduplicated, H1/H2 excluded)");

        // 5. Verify original baseline is UNCHANGED (did not shift to M3)
        const restartState = restartedCollector.loadRecoveryState();
        assert.strictEqual(restartState.channels["5001"].watch_after, expectedBaseline, "Original watch_after baseline must remain unchanged");
        assert.strictEqual(restartState.channels["5001"].checkpoint_message_id, msgM3, "Checkpoint must advance to latest eligible message M3");
        assert.strictEqual(restartState.channels["5001"].last_result, "success");

        // 6. Verify final journal contains M1, M2, M3 in order, exactly once
        const finalJournal5001 = readAllJournalRecords(tmpExchange).filter(r => r.channel_id === "5001");
        assert.strictEqual(finalJournal5001.length, 3, "Journal must contain exactly M1, M2, M3");
        assert.strictEqual(finalJournal5001[0].message_id, msgM1);
        assert.strictEqual(finalJournal5001[1].message_id, msgM2);
        assert.strictEqual(finalJournal5001[2].message_id, msgM3);
        assert.ok(!finalJournal5001.some(r => r.message_id === msgH1 || r.message_id === msgH2), "Historical messages must not be in journal");

        // 6. Test legitimate fresh install (clean installation: zero journal, zero recovery state)
        const freshExchange = path.join(os.tmpdir(), `cordbrief-fresh-${Date.now()}-${Math.random().toString(36).slice(2)}`);
        fs.mkdirSync(freshExchange, { recursive: true });
        const freshPrivate = path.join(freshExchange, "private");
        fs.mkdirSync(freshPrivate, { recursive: true });

        const freshCollector = new DiscordRpcCollector({
            exchangeDir: freshExchange,
            collectorDataDir: freshPrivate,
            runtimeDir: path.join(freshExchange, "runtime"),
            transport,
            client,
            enableLock: false
        });
        freshCollector.initJournal();
        assert.strictEqual(freshCollector.hasPriorCollectionEvidence("3001"), false);
        const freshRecovered = await freshCollector.recoverChannelSnapshot("3001");
        assert.strictEqual(freshRecovered, 1);
        assert.strictEqual(freshCollector.recoveryStateStatus, "ready");
        const freshState = freshCollector.loadRecoveryState();
        assert.strictEqual(freshState.channels["3001"].checkpoint_source, "rpc");

        // 7. Test retired journal evidence: retired sidecars count as prior collection evidence
        const retiredExchange = path.join(os.tmpdir(), `cordbrief-retired-${Date.now()}-${Math.random().toString(36).slice(2)}`);
        fs.mkdirSync(retiredExchange, { recursive: true });
        const retiredEvents = path.join(retiredExchange, "events");
        fs.mkdirSync(retiredEvents, { recursive: true });
        fs.writeFileSync(path.join(retiredEvents, "0000000000000002.ndjson"), "");
        const retentionDir = path.join(retiredExchange, "retention");
        fs.mkdirSync(retentionDir, { recursive: true });

        const seg1Records = [
            {
                message_id: msg1,
                channel_id: "4001",
                offset: 0,
                next_offset: 100
            }
        ];
        const sidecar1 = {
            version: 1,
            segment: 1,
            size: 100,
            sha256: "0".repeat(64),
            records: seg1Records
        };
        const sidecar1Buf = Buffer.from(JSON.stringify(sidecar1, null, 2), "utf8");
        fs.writeFileSync(path.join(retentionDir, "0000000000000001.ids.json"), sidecar1Buf);

        const manifest = {
            version: 1,
            retired_through: 1,
            segments: [
                {
                    segment: 1,
                    size: 100,
                    sha256: "0".repeat(64),
                    sidecar_sha256: sha256(sidecar1Buf)
                }
            ]
        };
        fs.writeFileSync(path.join(retiredExchange, "retention-manifest.json"), JSON.stringify(manifest, null, 2), "utf8");

        const retiredCollector = new DiscordRpcCollector({
            exchangeDir: retiredExchange,
            collectorDataDir: path.join(retiredExchange, "private"),
            runtimeDir: path.join(retiredExchange, "runtime"),
            transport,
            client,
            enableLock: false
        });
        retiredCollector.initJournal();
        assert.strictEqual(retiredCollector.hasPriorCollectionEvidence("4001"), true, "Retired sidecar evidence must be recognized");
        assert.throws(
            () => retiredCollector.beginChannelInitialization("4001"),
            /Cannot initialize existing journal channel 4001 as new first-watch channel/
        );

        transport.close();
        await mock.stop();
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
        try { fs.rmSync(freshExchange, { recursive: true, force: true }); } catch {}
        try { fs.rmSync(retiredExchange, { recursive: true, force: true }); } catch {}
        console.log("  ✔ Pending-baseline restart protection verified: cannot re-anchor existing channels.");
    }

    console.log("rpc_recovery_invariants_tests_passed");
}

runTests().catch(err => {
    console.error("rpc_recovery_invariants_test failed:", err);
    process.exit(1);
});
