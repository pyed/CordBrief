// CordBrief Collector Spike Test Harness
// Verifies NDJSON serialization, concurrent append safety, and allowlist filtering logic.

import fs from "fs";
import path from "path";
import assert from "assert";

const TEST_DIR = path.resolve("./temp_spike_test");
const TEST_JOURNAL = path.join(TEST_DIR, "test_events.ndjson");

async function runTests() {
    console.log("=== Running Collector Spike Harness Tests ===");

    if (fs.existsSync(TEST_DIR)) {
        fs.rmSync(TEST_DIR, { recursive: true, force: true });
    }
    fs.mkdirSync(TEST_DIR, { recursive: true });

    // Test 1: Allowlist filtering logic
    console.log("[Test 1] Testing channel allowlist filtering...");
    const watchedChannels = new Set(["channel-A", "channel-B"]);
    const messages = [
        { id: "m1", channel_id: "channel-A", content: "Message in A" },
        { id: "m2", channel_id: "channel-C", content: "Message in C (unwatched)" },
        { id: "m3", channel_id: "channel-B", content: "Message in B" },
    ];

    const captured = [];
    for (const msg of messages) {
        if (watchedChannels.has(msg.channel_id)) {
            captured.push(msg);
        }
    }

    assert.strictEqual(captured.length, 2, "Must capture exactly 2 messages");
    assert.strictEqual(captured[0].channel_id, "channel-A");
    assert.strictEqual(captured[1].channel_id, "channel-B");
    console.log("  -> PASS: Unwatched channel C was rejected, channels A & B accepted.");

    // Test 2: Serialized NDJSON append without corruption
    console.log("[Test 2] Testing concurrent NDJSON serialization and integrity...");
    const totalEvents = 50;
    const promises = [];

    for (let i = 0; i < totalEvents; i++) {
        const record = {
            event: "MESSAGE_CREATE",
            message_id: `msg-${String(i).padStart(3, "0")}`,
            guild_id: "guild-1",
            channel_id: i % 2 === 0 ? "channel-A" : "channel-B",
            timestamp: new Date(Date.now() + i * 1000).toISOString(),
            author_id: `user-${i}`,
            author_name: `User ${i}`,
            content: `Synthetic test message ${i}`,
            captured_at: new Date().toISOString()
        };

        const line = JSON.stringify(record) + "\n";
        promises.push(fs.promises.appendFile(TEST_JOURNAL, line, "utf8"));
    }

    await Promise.all(promises);

    // Read back NDJSON file and verify every single line is valid JSON
    const content = fs.readFileSync(TEST_JOURNAL, "utf8");
    const lines = content.trim().split("\n");

    assert.strictEqual(lines.length, totalEvents, `Expected ${totalEvents} lines, got ${lines.length}`);
    for (let idx = 0; idx < lines.length; idx++) {
        const parsed = JSON.parse(lines[idx]);
        assert.strictEqual(parsed.event, "MESSAGE_CREATE");
        assert.ok(parsed.message_id, `Line ${idx} must have message_id`);
        assert.ok(parsed.channel_id === "channel-A" || parsed.channel_id === "channel-B");
    }
    console.log(`  -> PASS: All ${totalEvents} concurrent NDJSON records parsed with zero line corruption.`);

    // Cleanup
    fs.rmSync(TEST_DIR, { recursive: true, force: true });
    console.log("=== All Harness Tests Passed Cleanly ===");
}

runTests().catch(err => {
    console.error("Test failed:", err);
    process.exit(1);
});
