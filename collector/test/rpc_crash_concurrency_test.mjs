/*
 * rpc_crash_concurrency_test.mjs
 *
 * Verifies and proves:
 * 1. Active-tail repair for torn progress across crashes at journal-write -> fsync -> checkpoint boundaries.
 * 2. Exact Snowflake replay and deduplication idempotency across crashes and restarts.
 * 3. Mutual exclusion via runtime.lock ensuring retention maintenance never races an active writer.
 */

import assert from "assert";
import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import { DiscordRpcCollector } from "../rpc/collector.mjs";
import { MockDiscordRpcServer } from "./mock_discord_rpc.mjs";
import { RpcTransport } from "../rpc/transport.mjs";
import { DiscordRpcClient } from "../rpc/protocol.mjs";
import { scanJournalRecords } from "../rpc/retention.mjs";
import { acquireRuntimeLock } from "../runtime.mjs";

function createTempDir(prefix) {
    const dir = path.join(os.tmpdir(), `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    fs.mkdirSync(dir, { recursive: true });
    return dir;
}

function makeNormalizedMessage(id, channelId = "2001", content = `Message ${id}`) {
    return {
        version: 1,
        event: "message_create",
        message_id: String(id),
        guild_id: "1001",
        channel_id: String(channelId),
        timestamp: "2026-09-11T00:00:00.000Z",
        captured_at: "2026-09-11T00:00:00.000Z",
        author: { id: "401", name: "alice", display_name: "Alice", bot: false },
        content: String(content),
        reply_to_message_id: null,
        attachments: []
    };
}

// ---------------------------------------------------------------------------
// Suite 1: Torn Progress & Crash Recovery
// ---------------------------------------------------------------------------
async function testTornAndCrash() {
    console.log("Starting torn progress & crash recovery tests...");

    // Test 1: Active-tail repair of torn trailing record on startup
    {
        const tmpExchange = createTempDir("cordbrief-torn-repair");
        const eventsDir = path.join(tmpExchange, "events");
        fs.mkdirSync(eventsDir, { recursive: true });

        const seg1Path = path.join(eventsDir, "0000000000000001.ndjson");
        const msg1 = JSON.stringify(makeNormalizedMessage("100000000000000001")) + "\n";
        const msg2 = JSON.stringify(makeNormalizedMessage("100000000000000002")) + "\n";
        fs.writeFileSync(seg1Path, msg1 + msg2, "utf8");
        const validSize = Buffer.byteLength(msg1 + msg2, "utf8");

        // Simulate crash mid-write: 40 bytes of a torn record without trailing newline
        const tornFragment = Buffer.from('{"version":1,"event":"message_create","message_id":"100000000000000003","content":"Torn');
        fs.appendFileSync(seg1Path, tornFragment);
        assert.strictEqual(fs.statSync(seg1Path).size, validSize + tornFragment.length);

        const collector = new DiscordRpcCollector({
            exchangeDir: tmpExchange,
            enableLock: false
        });

        // Startup journal initialization must detect the torn trailing bytes and truncate back
        collector.initJournal();

        assert.strictEqual(collector.currentSegmentSize, validSize, "Current segment size must be repaired to valid boundary");
        assert.strictEqual(fs.statSync(seg1Path).size, validSize, "File size on disk must be truncated to valid boundary");

        const diskBytes = fs.readFileSync(seg1Path);
        assert.strictEqual(diskBytes[diskBytes.length - 1], 10, "File must end with newline byte");

        // Unified scanner must read all valid records without encountering torn line error
        let scannedCount = 0;
        scanJournalRecords(tmpExchange, { segment: 1, offset: 0 }, { segment: 1, offset: validSize }, new Map(), (record) => {
            scannedCount++;
            assert.ok(["100000000000000001", "100000000000000002"].includes(record.message_id));
        });
        assert.strictEqual(scannedCount, 2, "Scanner must read exactly 2 valid records");

        // Deduplication ledger must have registered valid messages and not the torn one
        assert.strictEqual(collector.recentMessageIds.size, 2);
        assert.ok(collector.recentMessageIds.has("100000000000000001"));
        assert.ok(collector.recentMessageIds.has("100000000000000002"));
        assert.ok(!collector.recentMessageIds.has("100000000000000003"));
        assert.strictEqual(collector.journalMaxID, 100000000000000002n);

        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
        console.log("  ✔ Active-tail repair truncates torn bytes and restores valid boundary.");
    }

    // Test 2: Mid-batch crash during snapshot recovery and idempotent replay
    {
        const tmpExchange = createTempDir("cordbrief-midbatch-crash");
        const eventsDir = path.join(tmpExchange, "events");
        fs.mkdirSync(eventsDir, { recursive: true });

        // Phase A: Write messages 11 and 12, then half-write message 13 (torn)
        const seg1Path = path.join(eventsDir, "0000000000000001.ndjson");
        const msg11 = JSON.stringify(makeNormalizedMessage("100000000000000011")) + "\n";
        const msg12 = JSON.stringify(makeNormalizedMessage("100000000000000012")) + "\n";
        fs.writeFileSync(seg1Path, msg11 + msg12, "utf8");
        const tornFragment = Buffer.from('{"version":1,"event":"message_create","message_id":"100000000000000013","chan');
        fs.appendFileSync(seg1Path, tornFragment);

        // Checkpoint in recovery-state.json was still pending / at baseline before crash
        const recoveryPath = path.join(tmpExchange, "private", "recovery-state.json");
        fs.mkdirSync(path.dirname(recoveryPath), { recursive: true });
        fs.writeFileSync(recoveryPath, JSON.stringify({
            version: 2,
            channels: {
                "2001": {
                    checkpoint_message_id: "100000000000000000",
                    checkpoint_source: "rpc",
                    watch_after: "100000000000000000",
                    scan_after: null,
                    scan_until: null,
                    checkpoint_journal_boundary: { segment: 1, offset: 0 },
                    last_recovery_at: "2026-09-11T00:00:00.000Z",
                    last_result: "success",
                    last_error: null,
                    recovered_count: 0
                }
            },
            pending: null
        }, null, 2), "utf8");

        // Phase B: Restart collector with mock client returning all 4 messages (11, 12, 13, 14)
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
                        { id: "100000000000000011", channel_id: "2001", content: "M11", author: { id: "401", username: "alice" }, timestamp: "2026-09-11T00:00:00Z" },
                        { id: "100000000000000012", channel_id: "2001", content: "M12", author: { id: "401", username: "alice" }, timestamp: "2026-09-11T00:01:00Z" },
                        { id: "100000000000000013", channel_id: "2001", content: "M13", author: { id: "401", username: "alice" }, timestamp: "2026-09-11T00:02:00Z" },
                        { id: "100000000000000014", channel_id: "2001", content: "M14", author: { id: "401", username: "alice" }, timestamp: "2026-09-11T00:03:00Z" }
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

        // Run recovery replay
        const appended = await collector.recoverChannelSnapshot("2001");
        // Messages 11 & 12 are deduplicated; 13 (previously torn) and 14 are appended afresh
        assert.strictEqual(appended, 2, "Only messages 13 and 14 should be appended; 11 and 12 deduplicated");

        // Verify journal content: exactly 4 records in total, no duplicates, no torn lines
        const lines = fs.readFileSync(seg1Path, "utf8").trim().split("\n").map(l => JSON.parse(l));
        assert.strictEqual(lines.length, 4, "Segment must contain exactly 4 complete records");
        assert.strictEqual(lines[0].message_id, "100000000000000011");
        assert.strictEqual(lines[1].message_id, "100000000000000012");
        assert.strictEqual(lines[2].message_id, "100000000000000013");
        assert.strictEqual(lines[3].message_id, "100000000000000014");

        // Verify checkpoint committed
        const recState = collector.loadRecoveryState();
        assert.strictEqual(recState.channels["2001"].checkpoint_message_id, "100000000000000014");
        assert.strictEqual(recState.channels["2001"].last_result, "success");

        transport.close();
        await mock.stop();
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
        console.log("  ✔ Mid-batch crash replay recovers missing messages and deduplicates completed ones.");
    }

    // Test 3: Crash AFTER journal-write & fsync but BEFORE recovery-state checkpoint commit
    {
        const tmpExchange = createTempDir("cordbrief-postwrite-crash");
        const eventsDir = path.join(tmpExchange, "events");
        fs.mkdirSync(eventsDir, { recursive: true });

        // 3 messages already in journal, fsync'd
        const seg1Path = path.join(eventsDir, "0000000000000001.ndjson");
        const msg21 = JSON.stringify(makeNormalizedMessage("100000000000000021")) + "\n";
        const msg22 = JSON.stringify(makeNormalizedMessage("100000000000000022")) + "\n";
        const msg23 = JSON.stringify(makeNormalizedMessage("100000000000000023")) + "\n";
        fs.writeFileSync(seg1Path, msg21 + msg22 + msg23, "utf8");

        // But recovery checkpoint was NOT updated before crash (remains at baseline)
        const recoveryPath = path.join(tmpExchange, "private", "recovery-state.json");
        fs.mkdirSync(path.dirname(recoveryPath), { recursive: true });
        fs.writeFileSync(recoveryPath, JSON.stringify({
            version: 2,
            channels: {
                "2001": {
                    checkpoint_message_id: "100000000000000000",
                    checkpoint_source: "rpc",
                    watch_after: "100000000000000000",
                    scan_after: null,
                    scan_until: null,
                    checkpoint_journal_boundary: { segment: 1, offset: 0 },
                    last_recovery_at: "2026-09-11T00:00:00.000Z",
                    last_result: "success",
                    last_error: null,
                    recovered_count: 0
                }
            },
            pending: null
        }, null, 2), "utf8");

        // Mock returns messages 21, 22, 23 and one new message 24
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
                        { id: "100000000000000021", channel_id: "2001", content: "M21", author: { id: "401", username: "alice" }, timestamp: "2026-09-11T00:00:00Z" },
                        { id: "100000000000000022", channel_id: "2001", content: "M22", author: { id: "401", username: "alice" }, timestamp: "2026-09-11T00:01:00Z" },
                        { id: "100000000000000023", channel_id: "2001", content: "M23", author: { id: "401", username: "alice" }, timestamp: "2026-09-11T00:02:00Z" },
                        { id: "100000000000000024", channel_id: "2001", content: "M24", author: { id: "401", username: "alice" }, timestamp: "2026-09-11T00:03:00Z" }
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

        // Execute recovery replay
        const appended = await collector.recoverChannelSnapshot("2001");
        assert.strictEqual(appended, 1, "Only message 24 should be appended; 21, 22, 23 deduplicated");

        // Verify journal contains exactly 4 records
        const lines = fs.readFileSync(seg1Path, "utf8").trim().split("\n").map(l => JSON.parse(l));
        assert.strictEqual(lines.length, 4, "Segment must have exactly 4 records without duplicate appends");
        assert.strictEqual(lines[0].message_id, "100000000000000021");
        assert.strictEqual(lines[1].message_id, "100000000000000022");
        assert.strictEqual(lines[2].message_id, "100000000000000023");
        assert.strictEqual(lines[3].message_id, "100000000000000024");

        // Verify checkpoint committed
        const recState = collector.loadRecoveryState();
        assert.strictEqual(recState.channels["2001"].checkpoint_message_id, "100000000000000024");

        // Replay a second time: 0 appended, journal unchanged
        const secondReplayAppended = await collector.recoverChannelSnapshot("2001");
        assert.strictEqual(secondReplayAppended, 0, "Second replay must append 0 records");
        const linesAfter = fs.readFileSync(seg1Path, "utf8").trim().split("\n");
        assert.strictEqual(linesAfter.length, 4, "Journal record count remains exactly 4");

        transport.close();
        await mock.stop();
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
        console.log("  ✔ Post-write crash recovery deduplicates previously written messages and advances checkpoint.");
    }

    // Test 4: Crash at segment rotation boundary
    {
        const tmpExchange = createTempDir("cordbrief-rotation-crash");
        const collector = new DiscordRpcCollector({
            exchangeDir: tmpExchange,
            maxSegmentSize: 200, // Message size is ~260 bytes, so segment 1 will exceed limit
            enableLock: false
        });
        collector.initJournal();

        // Write message 31 into segment 1
        collector.appendEvents([makeNormalizedMessage("100000000000000031")]);
        assert.strictEqual(collector.currentSegmentNumber, 1);
        assert.ok(collector.currentSegmentSize >= 200, "Segment 1 size must reach or exceed maxSegmentSize");

        // Simulate restart after crash: segment 1 is at/above maxSegmentSize
        const collector2 = new DiscordRpcCollector({
            exchangeDir: tmpExchange,
            maxSegmentSize: 200,
            enableLock: false
        });
        collector2.initJournal();
        // initJournal should notice segment 1 size >= maxSegmentSize and rotate to segment 2
        assert.strictEqual(collector2.currentSegmentNumber, 2, "Must advance to segment 2 on startup if segment 1 is full");
        assert.strictEqual(collector2.currentSegmentSize, 0);

        // Append message 32: goes to segment 2
        collector2.appendEvents([makeNormalizedMessage("100000000000000032")]);
        assert.strictEqual(collector2.currentSegmentNumber, 2);
        // Replay of message 31 (from segment 1) and message 32 (from segment 2) are deduplicated
        const dup31 = collector2.appendEvents([makeNormalizedMessage("100000000000000031")]);
        assert.strictEqual(dup31, 0, "Replayed message 31 from previous segment must be deduplicated");
        const dup32 = collector2.appendEvents([makeNormalizedMessage("100000000000000032")]);
        assert.strictEqual(dup32, 0, "Replayed message 32 from active segment must be deduplicated");

        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
        console.log("  ✔ Segment rotation boundary recovers cleanly and deduplicates across segment files.");
    }

    console.log("rpc_crash_tests passed");
}

// ---------------------------------------------------------------------------
// Suite 2: Retention Lock Exclusion Model
// ---------------------------------------------------------------------------
async function testLockExclusion() {
    console.log("Starting retention lock exclusion tests...");

    // Test 1: Collector start fails closed when runtime.lock is held by another process
    {
        const tmpExchange = createTempDir("cordbrief-lock-exclusion-1");
        const runtimeDir = path.join(tmpExchange, "runtime");
        fs.mkdirSync(runtimeDir, { recursive: true });
        const lockFile = path.join(runtimeDir, "runtime.lock");

        // External process (e.g. setup or retention publisher) acquires runtime.lock
        const externalLock = acquireRuntimeLock(lockFile, "setup", { pid: 7771 });
        assert.strictEqual(externalLock.acquired, true, "External process should acquire lock");

        const collector = new DiscordRpcCollector({
            exchangeDir: tmpExchange,
            runtimeDir,
            runtimeLockFile: lockFile
        });

        // Attempting to start collector must fail closed
        let startError = null;
        try {
            await collector.start({ clientId: "app_id", accessToken: "token" });
        } catch (err) {
            startError = err;
        }

        assert.ok(startError, "Collector start must fail when runtime.lock is held");
        assert.match(startError.message, /Failed to acquire runtime lock: Runtime lock held by setup/);

        // Collector must record error status and not write journal events
        const statusFile = path.join(tmpExchange, "collector-status.json");
        assert.ok(fs.existsSync(statusFile), "Status file must be written with error");
        const status = JSON.parse(fs.readFileSync(statusFile, "utf8"));
        assert.strictEqual(status.collector_state, "error");
        assert.match(status.last_error, /Runtime lock held by setup/);

        const eventsDir = path.join(tmpExchange, "events");
        assert.ok(!fs.existsSync(eventsDir), "Events dir must not be initialized on lock failure");

        externalLock.release();
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
        console.log("  ✔ Collector fails closed when runtime.lock is held by an active process.");
    }

    // Test 2: Active collector holds runtime.lock and excludes concurrent processes
    {
        const tmpExchange = createTempDir("cordbrief-lock-exclusion-2");
        const runtimeDir = path.join(tmpExchange, "runtime");
        fs.mkdirSync(runtimeDir, { recursive: true });
        const lockFile = path.join(runtimeDir, "runtime.lock");

        const mock = new MockDiscordRpcServer({
            guilds: [{ id: "1001", name: "Guild" }],
            channelsByGuild: { "1001": [{ id: "2001", name: "general", type: 0 }] },
            channelData: { "2001": { id: "2001", name: "general", type: 0, guild_id: "1001", messages: [] } }
        });
        const pipePath = await mock.start();
        const transport = new RpcTransport({ socketPath: pipePath });
        const client = new DiscordRpcClient(transport);

        const collector = new DiscordRpcCollector({
            exchangeDir: tmpExchange,
            runtimeDir,
            runtimeLockFile: lockFile,
            transport,
            client
        });

        // Write valid empty watchlist
        fs.writeFileSync(path.join(tmpExchange, "watchlist.json"), JSON.stringify({ version: 1, generation: 1, channel_ids: [] }));

        await collector.start({ clientId: "app_id", accessToken: "token" });

        // Verify runtime.lock exists and indicates collector is holder
        assert.ok(fs.existsSync(lockFile), "Lock file must exist on disk while collector runs");
        const lockContent = JSON.parse(fs.readFileSync(lockFile, "utf8"));
        assert.strictEqual(lockContent.holder, "collector");
        assert.strictEqual(lockContent.pid, process.pid);

        // Attempt concurrent acquisition (e.g. retention publisher or setup)
        const concurrentAttempt = acquireRuntimeLock(lockFile, "retention-publisher", { pid: 9991 });
        assert.strictEqual(concurrentAttempt.acquired, false, "Concurrent process must be excluded while collector holds lock");
        assert.strictEqual(concurrentAttempt.existing.holder, "collector");

        // Stop collector cleanly
        await collector.stop();
        transport.close();
        await mock.stop();

        // After stop, lock file should be unlinked
        assert.ok(!fs.existsSync(lockFile), "Lock file must be cleanly released and unlinked on stop");

        // External process can now acquire the lock
        const subsequentAcquisition = acquireRuntimeLock(lockFile, "retention-publisher", { pid: 9992 });
        assert.strictEqual(subsequentAcquisition.acquired, true, "External process can acquire lock after collector stops");
        subsequentAcquisition.release();

        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
        console.log("  ✔ Active collector excludes concurrent retention/setup and releases cleanly on stop.");
    }

    // Test 3: Stale lease reclamation after unclean crash
    {
        const tmpExchange = createTempDir("cordbrief-lock-stale");
        const runtimeDir = path.join(tmpExchange, "runtime");
        fs.mkdirSync(runtimeDir, { recursive: true });
        const lockFile = path.join(runtimeDir, "runtime.lock");

        // Write a stale lock file with heartbeat 35 seconds ago
        const staleTimestamp = new Date(Date.now() - 35000).toISOString();
        fs.writeFileSync(lockFile, JSON.stringify({
            version: 1,
            holder: "crashed-collector",
            pid: 12345,
            hostname: os.hostname(),
            acquired_at: staleTimestamp,
            heartbeat_at: staleTimestamp
        }, null, 2), "utf8");

        const mock = new MockDiscordRpcServer({
            guilds: [{ id: "1001", name: "Guild" }],
            channelsByGuild: { "1001": [{ id: "2001", name: "general", type: 0 }] },
            channelData: { "2001": { id: "2001", name: "general", type: 0, guild_id: "1001", messages: [] } }
        });
        const pipePath = await mock.start();
        const transport = new RpcTransport({ socketPath: pipePath });
        const client = new DiscordRpcClient(transport);

        const collector = new DiscordRpcCollector({
            exchangeDir: tmpExchange,
            runtimeDir,
            runtimeLockFile: lockFile,
            transport,
            client
        });
        fs.writeFileSync(path.join(tmpExchange, "watchlist.json"), JSON.stringify({ version: 1, generation: 1, channel_ids: [] }));

        // Startup should reclaim stale lock and run
        await collector.start({ clientId: "app_id", accessToken: "token" });

        assert.ok(fs.existsSync(lockFile));
        const updatedLock = JSON.parse(fs.readFileSync(lockFile, "utf8"));
        assert.strictEqual(updatedLock.holder, "collector");
        assert.strictEqual(updatedLock.pid, process.pid);

        await collector.stop();
        transport.close();
        await mock.stop();
        try { fs.rmSync(tmpExchange, { recursive: true, force: true }); } catch {}
        console.log("  ✔ Stale runtime lock from crashed collector is safely reclaimed on restart.");
    }

    console.log("rpc_lock_exclusion passed");
}

// ---------------------------------------------------------------------------
// Entrypoint dispatcher
// ---------------------------------------------------------------------------
async function main() {
    const args = process.argv.slice(2);
    const runTorn = args.includes("--torn-and-crash") || args.length === 0 || args.includes("--all");
    const runLock = args.includes("--lock-exclusion") || args.length === 0 || args.includes("--all");

    if (runTorn) {
        await testTornAndCrash();
    }
    if (runLock) {
        await testLockExclusion();
    }
}

main().catch(err => {
    console.error("rpc_crash_concurrency_test failed:", err);
    process.exit(1);
});
