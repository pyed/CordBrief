// Non-destructive retention counterexample using the actual native child harness.
// All evidence loss is simulated in disposable copies; the original stays intact.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const harness = fileURLToPath(new URL("gc_readiness_test.mjs", import.meta.url));
const root = fs.mkdtempSync(path.join(os.tmpdir(), "cordbrief-retired-evidence-"));
const original = path.join(root, "original");
const channel = "1545115236619518001", other = "1545115236619518002";
const id = n => String(1545225000000000000n + BigInt(n));
function run(where, op, extra = {}) {
    const child = spawnSync(process.execPath, [harness, "--child"], {
        input: JSON.stringify({ root: where, op, ...extra }), encoding: "utf8", timeout: 30000
    });
    assert.equal(child.status, 0, child.stderr || child.stdout);
    return JSON.parse(child.stdout.split("\n").find(line => line.startsWith("RESULT ")).slice(7));
}
const events = where => path.join(where, "exchange", "events");
const records = where => fs.readdirSync(events(where)).sort().flatMap(file =>
    fs.readFileSync(path.join(events(where), file), "utf8").split("\n").filter(Boolean).map(JSON.parse));
function copyView(name, first, emptyFirst) {
    const target = path.join(root, name);
    fs.cpSync(original, target, { recursive: true, filter: source => source !== path.join(events(original), first) });
    if (emptyFirst) fs.writeFileSync(path.join(events(target), first), "");
    return target;
}
try {
    run(original, "seed", { count: 101 });
    assert.equal(run(original, "recover", { ids: Array.from({ length: 100 }, (_, i) => i + 1) }).appendedCount, 0);
    assert.equal(run(original, "recover", { ids: [101] }).appendedCount, 0);
    assert.equal(run(original, "recover", { channel: other, ids: Array.from({ length: 8 }, (_, i) => 100001 + i) }).appendedCount, 0);
    const { state, boundary } = run(original, "inspect");
    const files = fs.readdirSync(events(original)).sort();
    const originalRecords = records(original).length;
    assert.equal(originalRecords, 109);
    assert.ok(files.length > 4);
    assert.ok(Object.values(state.channels).every(ch => ch.checkpoint_journal_boundary.segment > 1));
    assert.equal(state.pending, null);
    // Opus's other clauses could all hold: Core ack at boundary, migrated artifacts,
    // active segment = boundary.segment. Yet the forward floors do not cover replay.
    assert.ok(boundary.segment > 1);
    assert.equal(run(original, "recover", { ids: [1] }).appendedCount, 0);
    const missingPrefix = copyView("missing-prefix", files[0], false);
    assert.match(run(missingPrefix, "try-inspect").error, /topology|ENOENT/);
    // Counterfactual evidence-loss view: an empty first file lets the CURRENT native
    // topology check pass. This is not a supported GC format or a production edit.
    const stripped = copyView("stripped-evidence", files[0], true);
    assert.equal(records(stripped).filter(r => r.message_id === id(1)).length, 0);
    const replay = run(stripped, "recover", { ids: [1] });
    assert.equal(replay.success, true, replay.error);
    assert.equal(replay.appendedCount, 1);
    assert.equal(records(stripped).filter(r => r.message_id === id(1)).length, 1);
    assert.equal(records(original).filter(r => r.message_id === id(1)).length, 1);
    // A high-water exclusion would suppress genuinely unseen late-visible history.
    assert.equal(run(original, "recover", { ids: [0] }).appendedCount, 1);
    assert.equal(state.channels[channel].checkpoint_message_id, id(101));
    console.log(JSON.stringify({ segments: files.length, originalRecords,
        intactReplayAppends: 0, missingPrefix: "rejected", strippedEvidenceReplayAppends: 1,
        unseenBelowHighWaterAppends: 1 }));
} finally {
    fs.rmSync(root, { recursive: true, force: true });
}
