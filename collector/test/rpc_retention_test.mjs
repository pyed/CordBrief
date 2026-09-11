/*
 * rpc_retention_test.mjs
 * Comprehensive tests for Discord RPC collector retention, restart, replay, and deduplication safety.
 */

import assert from "assert";
import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import { createHash } from "crypto";
import { MockDiscordRpcServer } from "./mock_discord_rpc.mjs";
import { DiscordRpcCollector, normalizeDiscordMessage } from "../rpc/collector.mjs";
import { RpcTransport } from "../rpc/transport.mjs";
import { DiscordRpcClient } from "../rpc/protocol.mjs";
import { padSegmentNumber, sha256 } from "../rpc/retention.mjs";

const id = n => String(1545225000000000000n + BigInt(n));

function makeTestEvent(n, channelId = "2001", guildId = "1001") {
    return {
        version: 1,
        event: "message_create",
        message_id: id(n),
        guild_id: guildId,
        channel_id: channelId,
        timestamp: "2026-09-11T00:00:00.000Z",
        captured_at: "2026-09-11T00:00:00.000Z",
        author: { id: "501", name: "user" + n, display_name: "User " + n, bot: false },
        content: "Message content " + n,
        reply_to_message_id: null,
        attachments: []
    };
}

async function runTests() {
    const tmpRoot = path.join(os.tmpdir(), `cordbrief-rpc-retention-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    const exchangeDir = path.join(tmpRoot, "exchange");
    const privateDir = path.join(tmpRoot, "private");
    fs.mkdirSync(exchangeDir, { recursive: true });
    fs.mkdirSync(privateDir, { recursive: true });

    const mock = new MockDiscordRpcServer();
    const pipePath = await mock.start();

    // Write watchlist
    fs.writeFileSync(path.join(exchangeDir, "watchlist.json"), JSON.stringify({
        version: 1,
        generation: 1,
        channel_ids: ["2001"]
    }, null, 2), "utf8");

    try {
        console.log("Starting RPC retention test suite...");

        // -------------------------------------------------------------
        // Phase 1: Segment rotation across multiple physical segments
        // -------------------------------------------------------------
        const transport1 = new RpcTransport({ socketPath: pipePath });
        const client1 = new DiscordRpcClient(transport1);
        const collector1 = new DiscordRpcCollector({
            exchangeDir,
            collectorDataDir: privateDir,
            maxSegmentSize: 1024, // Small segment limit to force rotation
            transport: transport1,
            client: client1
        });

        await collector1.start({ clientId: "test_client", accessToken: "test_token" });

        // Append 15 events to generate multiple segments (1, 2, 3)
        for (let i = 1; i <= 15; i++) {
            collector1.appendEvents([makeTestEvent(i)]);
        }

        const eventsDir = path.join(exchangeDir, "events");
        const segmentsBefore = fs.readdirSync(eventsDir).filter(f => /^\d{16}\.ndjson$/.test(f)).sort();
        assert.ok(segmentsBefore.length >= 3, `Expected at least 3 segments, got ${segmentsBefore.length}`);
        assert.strictEqual(collector1.currentSegmentNumber, segmentsBefore.length);

        await collector1.stop();

        // -------------------------------------------------------------
        // Phase 2: Restart test across unpruned physical segments
        // -------------------------------------------------------------
        const transport2 = new RpcTransport({ socketPath: pipePath });
        const client2 = new DiscordRpcClient(transport2);
        const collector2 = new DiscordRpcCollector({
            exchangeDir,
            collectorDataDir: privateDir,
            maxSegmentSize: 1024,
            transport: transport2,
            client: client2
        });

        collector2.initJournal();
        assert.strictEqual(collector2.currentSegmentNumber, segmentsBefore.length);
        assert.strictEqual(collector2.journalRecordCount, 15);
        assert.strictEqual(collector2.journalMaxID, BigInt(id(15)));

        // -------------------------------------------------------------
        // Phase 3: Create certified retention manifest & delete retired segment 1
        // -------------------------------------------------------------
        const retiredThrough = 1;
        const retentionDir = path.join(exchangeDir, "retention");
        fs.mkdirSync(retentionDir, { recursive: true });

        // Build sidecar for segment 1
        const seg1Path = path.join(eventsDir, padSegmentNumber(1));
        const seg1Bytes = fs.readFileSync(seg1Path);
        const seg1Records = [];
        let offset = 0;
        while (offset < seg1Bytes.length) {
            const next = seg1Bytes.indexOf(10, offset) + 1;
            const rec = JSON.parse(seg1Bytes.subarray(offset, next));
            seg1Records.push({
                message_id: rec.message_id,
                channel_id: rec.channel_id,
                offset,
                next_offset: next
            });
            offset = next;
        }

        const sidecar1 = {
            version: 1,
            segment: 1,
            size: seg1Bytes.length,
            sha256: sha256(seg1Bytes),
            records: seg1Records
        };
        const sidecar1Path = path.join(retentionDir, "0000000000000001.ids.json");
        const sidecar1Buf = Buffer.from(JSON.stringify(sidecar1, null, 2), "utf8");
        fs.writeFileSync(sidecar1Path, sidecar1Buf);

        // Publish retention-manifest.json
        const manifest = {
            version: 1,
            retired_through: 1,
            segments: [
                {
                    segment: 1,
                    size: seg1Bytes.length,
                    sha256: sidecar1.sha256,
                    sidecar_sha256: sha256(sidecar1Buf)
                }
            ]
        };
        fs.writeFileSync(path.join(exchangeDir, "retention-manifest.json"), JSON.stringify(manifest, null, 2), "utf8");

        // Physically delete segment 1 from events/
        fs.unlinkSync(seg1Path);
        assert.ok(!fs.existsSync(seg1Path), "Segment 1 must be deleted");

        // -------------------------------------------------------------
        // Phase 4: Restart collector with retired prefix & verify deduplication
        // -------------------------------------------------------------
        const transport3 = new RpcTransport({ socketPath: pipePath });
        const client3 = new DiscordRpcClient(transport3);
        const collector3 = new DiscordRpcCollector({
            exchangeDir,
            collectorDataDir: privateDir,
            maxSegmentSize: 1024,
            transport: transport3,
            client: client3
        });

        // Initialize journal: must discover retired evidence and validate contiguous suffix
        collector3.initJournal();
        assert.strictEqual(collector3.retiredEvidence.size, 1);
        assert.strictEqual(collector3.journalRecordCount, 15);
        assert.strictEqual(collector3.journalMaxID, BigInt(id(15)));

        // Test deduplication against deleted segment 1:
        // Message 1 was in segment 1 (now deleted!). Attempting to append it must be ignored!
        const reAppendedCount = collector3.appendEvents([makeTestEvent(1)]);
        assert.strictEqual(reAppendedCount, 0, "Retired segment message 1 must not be duplicated");

        // Message 2 was also in segment 1. Clear in-memory cache to force durable scan fallback!
        collector3.recentMessageIds.clear();
        const reAppendedCount2 = collector3.appendEvents([makeTestEvent(2)]);
        assert.strictEqual(reAppendedCount2, 0, "Retired message 2 must be deduped via durable scan");

        // Test wrong-channel rejection against retired history
        assert.throws(() => {
            collector3.appendEvents([makeTestEvent(2, "9999")]);
        }, /belongs to another journal channel/);

        // -------------------------------------------------------------
        // Phase 5: Snapshot recovery with outage overlap
        // -------------------------------------------------------------
        await transport3.connect("test_client");
        // Simulate Discord returning snapshot with 1 old message (15) and 2 new outage messages (16, 17)
        mock.channelData["2001"] = {
            id: "2001",
            name: "general",
            type: 0,
            messages: [
                {
                    id: id(15),
                    channel_id: "2001",
                    content: "Message content 15",
                    author: { id: "501", username: "user15" },
                    timestamp: "2026-09-11T00:15:00.000Z"
                },
                {
                    id: id(16),
                    channel_id: "2001",
                    content: "Outage message 16",
                    author: { id: "501", username: "user16" },
                    timestamp: "2026-09-11T00:16:00.000Z"
                },
                {
                    id: id(17),
                    channel_id: "2001",
                    content: "Outage message 17",
                    author: { id: "501", username: "user17" },
                    timestamp: "2026-09-11T00:17:00.000Z"
                }
            ]
        };

        const recoveredCount = await collector3.recoverChannelSnapshot("2001");
        assert.strictEqual(recoveredCount, 2, "Should append exactly 2 new messages from outage snapshot");
        assert.strictEqual(collector3.journalRecordCount, 17);
        assert.strictEqual(collector3.journalMaxID, BigInt(id(17)));

        // Verify recovery-state.json has valid v2 invariants
        const recState = collector3.loadRecoveryState();
        assert.strictEqual(recState.version, 2);
        assert.strictEqual(recState.pending, null);
        assert.strictEqual(recState.channels["2001"].checkpoint_message_id, id(17));
        assert.strictEqual(recState.channels["2001"].last_result, "success");

        // -------------------------------------------------------------
        // Phase 6: Corruption & uncertified gap rejection (fail-closed)
        // -------------------------------------------------------------
        // 6a: Corrupt sidecar hash in manifest
        const corruptDir = path.join(tmpRoot, "corrupt-exchange");
        fs.cpSync(exchangeDir, corruptDir, { recursive: true });
        const corruptManifest = JSON.parse(fs.readFileSync(path.join(corruptDir, "retention-manifest.json"), "utf8"));
        corruptManifest.segments[0].sidecar_sha256 = "0000000000000000000000000000000000000000000000000000000000000000";
        fs.writeFileSync(path.join(corruptDir, "retention-manifest.json"), JSON.stringify(corruptManifest, null, 2));

        const corruptCollector = new DiscordRpcCollector({ exchangeDir: corruptDir });
        assert.throws(() => corruptCollector.initJournal(), /Retention sidecar checksum mismatch/);

        // 6b: Uncertified prefix gap (delete segment 2 without updating manifest)
        const gapDir = path.join(tmpRoot, "gap-exchange");
        fs.cpSync(exchangeDir, gapDir, { recursive: true });
        fs.unlinkSync(path.join(gapDir, "events", padSegmentNumber(2)));
        const gapCollector = new DiscordRpcCollector({ exchangeDir: gapDir });
        assert.throws(() => gapCollector.initJournal(), /Uncertified journal prefix gap|Invalid journal segment sequence/);

        console.log("rpc_retention_test passed");
    } finally {
        await mock.stop();
        try { fs.rmSync(tmpRoot, { recursive: true, force: true }); } catch {}
    }
}

runTests().catch(err => {
    console.error("rpc_retention_test failed:", err);
    process.exit(1);
});
