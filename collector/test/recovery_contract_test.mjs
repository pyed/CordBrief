// An information-loss boundary, using the actual renderer and native child harness.
// Characterization succeeds only when it reproduces the contract limitation.
// --require-unconditional preserves the rejected guarantee as a negative control.
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const harness = fileURLToPath(new URL("./gc_readiness_test.mjs", import.meta.url));
const roots = [];
const channel = "1545115236619518001";
const messageID = "1545225000000000202";
function fresh() {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "cordbrief-contract-"));
    roots.push(root);
    return root;
}
function run(root, extra, expected = 0) {
    const child = spawnSync(process.execPath, [harness, "--child"], {
        input: JSON.stringify({ root, ...extra }), encoding: "utf8", timeout: 30000
    });
    assert.equal(child.status, expected, child.stderr || child.stdout);
    if (expected) return;
    const line = child.stdout.split("\n").find(line => line.startsWith("RESULT "));
    assert.ok(line, child.stdout);
    return JSON.parse(line.slice(7));
}
function copies(root) {
    const events = path.join(root, "exchange", "events");
    return fs.readdirSync(events).filter(f => f.endsWith(".ndjson")).flatMap(f =>
        fs.readFileSync(path.join(events, f), "utf8").split("\n").filter(Boolean).map(JSON.parse))
        .filter(record => record.message_id === messageID).length;
}
try {
    // Established channel: moving the moment of first-watch activation cannot fix this.
    const root = fresh();
    run(root, { op: "seed", count: 0 });
    const checkpoint = run(root, { op: "inspect" }).state.channels[channel].checkpoint_message_id;
    assert.ok(BigInt(messageID) > BigInt(checkpoint));
    // Gateway delivers M while REST cannot see history. Its actual handler buffers M.
    // Kill before buffer drain; native contains neither M nor its content.
    run(root, { op: "renderer", ids: [], buffer: [202], beforeDrain: true }, 74);
    assert.equal(copies(root), 0);
    assert.equal(run(root, { op: "inspect" }).state.channels[channel].checkpoint_message_id, checkpoint);
    for (let attempt = 0; attempt < 2; attempt++) run(root, { op: "renderer", ids: [] });
    const hiddenCopies = copies(root);
    assert.equal(hiddenCopies, 0);

    // Positive control: restore actual REST availability and the very same durable
    // state recovers M once. This distinguishes absent evidence from bad exclusion.
    run(root, { op: "renderer", ids: [202] });
    run(root, { op: "renderer", ids: [202] });
    assert.equal(copies(root), 1);
    console.log(`GATEWAY-ONLY CRASH: K unchanged and below M; without later REST copies=${hiddenCopies}; with REST restored copies=1`);
    if (process.argv.includes("--require-unconditional")) {
        assert.equal(hiddenCopies, 1, "Gateway observation alone is not a durable/replayable source after process death");
    }
    console.log("RECOVERY CONTRACT SAFE: durable records or later available REST; vanished sources excluded");
} finally {
    for (const root of roots) fs.rmSync(root, { recursive: true, force: true });
}
