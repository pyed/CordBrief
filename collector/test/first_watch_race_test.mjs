// Actual renderer/native counterexample, now an exactly-once regression in both modes.
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const harness = fileURLToPath(new URL("./gc_readiness_test.mjs", import.meta.url));
const root = fs.mkdtempSync(path.join(os.tmpdir(), "cordbrief-first-watch-race-"));
const channel = "1545115236619518001";
const id = n => String(1545225000000000000n + BigInt(n));
function run(extra, expected = 0) {
    const child = spawnSync(process.execPath, [harness, "--child"], {
        input: JSON.stringify({ root, ...extra }), encoding: "utf8", timeout: 30000
    });
    assert.equal(child.status, expected, child.stderr || child.stdout);
    if (expected) return;
    const line = child.stdout.split("\n").find(line => line.startsWith("RESULT "));
    assert.ok(line, child.stdout);
    return JSON.parse(line.slice(7));
}
try {
    // Same-millisecond M arrives after the queued-message snapshot while baseline
    // IPC is outstanding. H > M does not prove that M was historically excluded.
    run({ op: "renderer", ids: [203], lateBaselineBuffer: [202], beforeDrain: true }, 74);
    const saved = run({ op: "inspect" });
    assert.ok(BigInt(saved.state.channels[channel].checkpoint_message_id) < BigInt(id(202)), "Activation must not exclude same-millisecond M");
    const recovered = run({ op: "renderer", ids: [202, 203] });
    assert.ok(recovered.requests.some(q => q.after && BigInt(q.after) < BigInt(id(202))), "Restart must revisit M");
    const events = path.join(root, "exchange", "events");
    const records = fs.readdirSync(events).filter(f => f.endsWith(".ndjson")).flatMap(f =>
        fs.readFileSync(path.join(events, f), "utf8").split("\n").filter(Boolean).map(JSON.parse));
    const copies = records.filter(r => r.message_id === id(202)).length;
    console.log(`FIRST-WATCH IPC RACE: baseline H=203; observed M=202; restart includes M; M copies=${copies}`);
    assert.equal(copies, 1, "Observed post-watch M is permanently below the exclusion checkpoint");
} finally {
    // Only this test's newly created disposable directory is removed.
    fs.rmSync(root, { recursive: true, force: true });
}
