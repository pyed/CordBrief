// Corrupt pending bounds must be rejected without replacing durable evidence.
// Uses actual native startup reconciliation in a fresh OS process.
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
const harness = fileURLToPath(new URL("./gc_readiness_test.mjs", import.meta.url));
const root = fs.mkdtempSync(path.join(os.tmpdir(), "cordbrief-pending-bounds-"));
const channel = "1545115236619518001";
const id = n => String(1545225000000000000n + BigInt(n));
function run(op, extra = {}) {
    const child = spawnSync(process.execPath, [harness, "--child"], {
        input: JSON.stringify({ root, op, ...extra }), encoding: "utf8", timeout: 30000
    });
    assert.equal(child.status, 0, child.stderr || child.stdout);
    const line = child.stdout.split("\n").find(line => line.startsWith("RESULT "));
    assert.ok(line, child.stdout);
    return JSON.parse(line.slice(7));
}
try {
    run("seed", { count: 0 });
    run("raw", { ids: [201, 202, 203] });
    const state = run("inspect").state;
    state.channels[channel].scan_after = state.channels[channel].watch_after;
    state.channels[channel].scan_until = id(201);
    state.pending = { channel_id: channel, old_checkpoint_message_id: id(0),
        new_checkpoint_message_id: id(203), message_ids: [201, 202, 203].map(id),
        journal_start: { segment: 1, offset: 0 } };
    const file = path.join(root, "private", "recovery-state.json");
    fs.writeFileSync(file, JSON.stringify(state));
    const before = fs.readFileSync(file);
    const result = run("try-inspect");
    assert.ok(result.error, "Inconsistent pending/sweep bounds must fail closed");
    const after = fs.readFileSync(file);
    const changed = !before.equals(after);
    const persisted = JSON.parse(after);
    console.log(`PENDING BOUNDS: page end=203 exceeds sweep end=201; rejected=${Boolean(result.error)}; state changed=${changed}; pending cleared=${persisted.pending === null}; K advanced=${persisted.channels[channel].checkpoint_message_id !== id(0)}`);
    assert.equal(changed, false, "Startup reconciliation overwrote corrupt pending evidence before rejecting it");
} finally {
    fs.rmSync(root, { recursive: true, force: true });
}
