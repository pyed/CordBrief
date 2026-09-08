// Actual native recovery with certified retired identities. No journal deletion:
// tests create disposable views whose copy filter omits a retired prefix.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
const harness = fileURLToPath(new URL("gc_readiness_test.mjs", import.meta.url));
const root = fs.mkdtempSync(path.join(os.tmpdir(), "cordbrief-retention-reader-"));
const base = path.join(root, "base"), channel = "1545115236619518001", other = "1545115236619518002";
const hash = bytes => createHash("sha256").update(bytes).digest("hex");
const id = n => String(1545225000000000000n + BigInt(n));
const file = n => String(n).padStart(16, "0");
const exchange = r => path.join(r, "exchange");
const events = r => path.join(exchange(r), "events");
const stateFile = r => path.join(r, "private", "recovery-state.json");
const manifestFile = r => path.join(exchange(r), "retention-manifest.json");
const indexFile = (r, n) => path.join(exchange(r), "retention", file(n) + ".ids.json");
const readJSON = f => JSON.parse(fs.readFileSync(f));
const writeJSON = (f, value) => fs.writeFileSync(f, JSON.stringify(value));
function run(r, op, extra = {}) {
    const child = spawnSync(process.execPath, [harness, "--child"], {
        input: JSON.stringify({ root: r, op, ...extra }), encoding: "utf8", timeout: 30000
    });
    if (extra.partial) { assert.equal(child.status, 76, child.stderr); return; }
    assert.equal(child.status, 0, child.stderr || child.stdout);
    return JSON.parse(child.stdout.split("\n").find(line => line.startsWith("RESULT ")).slice(7));
}
function view(name, omit = () => false) {
    const result = path.join(root, name);
    fs.cpSync(base, result, { recursive: true, filter: source => !omit(path.relative(base, source).replaceAll("\\", "/")) });
    return result;
}
function updateIndex(r, n, mutate) {
    const sidecar = readJSON(indexFile(r, n));
    mutate(sidecar);
    writeJSON(indexFile(r, n), sidecar);
    const manifest = readJSON(manifestFile(r));
    manifest.segments[n - 1].sidecar_sha256 = hash(fs.readFileSync(indexFile(r, n)));
    writeJSON(manifestFile(r), manifest);
}
try {
    run(base, "seed", { count: 250 });
    for (let first = 1; first <= 250; first += 100) {
        assert.equal(run(base, "recover", { ids: Array.from({ length: Math.min(100, 251 - first) }, (_, i) => first + i) }).appendedCount, 0);
    }
    run(base, "recover", { channel: other, ids: Array.from({ length: 8 }, (_, i) => 100001 + i) });
    const initialState = fs.readFileSync(stateFile(base));
    const segments = fs.readdirSync(events(base)).sort();
    const retiredThrough = segments.length - 2;
    fs.mkdirSync(path.join(exchange(base), "retention"));
    const manifest = { version: 1, retired_through: retiredThrough, segments: [] };
    // Fixture encoder is not the publisher under test; publisher has separate
    // actual Linux publication/crash tests. Here we exercise actual native readers.
    for (let n = 1; n <= retiredThrough; n++) {
        const bytes = fs.readFileSync(path.join(events(base), file(n) + ".ndjson"));
        const records = [];
        let offset = 0;
        while (offset < bytes.length) {
            const next = bytes.indexOf(10, offset) + 1;
            assert.ok(next > offset);
            const record = JSON.parse(bytes.subarray(offset, next));
            records.push({ message_id: record.message_id, channel_id: record.channel_id, offset, next_offset: next });
            offset = next;
        }
        const sidecar = { version: 1, segment: n, size: bytes.length, sha256: hash(bytes), records };
        writeJSON(indexFile(base, n), sidecar);
        manifest.segments.push({ segment: n, size: bytes.length, sha256: sidecar.sha256,
            sidecar_sha256: hash(fs.readFileSync(indexFile(base, n))) });
    }
    writeJSON(manifestFile(base), manifest);
    assert.equal(run(base, "inspect").state.channels[channel].checkpoint_message_id, id(250));
    assert.ok(initialState.equals(fs.readFileSync(stateFile(base))), "publication read changed recovery state");
    const omitted = relative => /^exchange\/events\/\d{16}\.ndjson$/.test(relative) && Number(path.basename(relative).slice(0, 16)) <= retiredThrough;
    const retired = view("retired", omitted);
    assert.equal(run(retired, "inspect").state.channels[channel].checkpoint_message_id, id(250));
    for (let first = 1; first <= 250; first += 100) {
        assert.equal(run(retired, "recover", { ids: Array.from({ length: Math.min(100, 251 - first) }, (_, i) => first + i) }).appendedCount, 0);
    }
    const boundary = run(retired, "inspect").boundary;
    assert.deepEqual(run(retired, "cache-clear-live", { ids: [1, 100, 250] }), boundary);
    assert.equal(run(retired, "wrong-channel-live", { id: 1 }), false);
    assert.equal(run(retired, "wrong-channel-live", { id: 1, clear: true }), false);
    const newChannel = "1545115236619518003";
    run(retired, "renderer", { channel: newChannel, ids: [5000, 5001] });
    const newWatch = run(retired, "renderer", { channel: newChannel, ids: [5000, 5001] });
    assert.equal(newWatch.state.channels[newChannel].checkpoint_message_id, id(5001));
    const watchBefore = newWatch.state.channels[channel].watch_after;
    run(retired, "renderer", { ids: [1, 250], removeWatch: true });
    const readded = run(retired, "renderer", { ids: [1, 250] });
    assert.equal(readded.state.channels[channel].watch_after, watchBefore);
    assert.equal(run(retired, "recover", { ids: [0] }).appendedCount, 1);
    assert.equal(run(retired, "recover", { ids: [0] }).appendedCount, 0);
    run(retired, "recover", { ids: [301, 302, 303], partial: true });
    assert.equal(run(retired, "recover", { ids: [301, 302, 303] }).success, true);
    assert.equal(run(retired, "recover", { ids: [301, 302, 303] }).appendedCount, 0);
    let corruptions = 0;
    function corrupt(name, mutate, omit = omitted) {
        const r = view(name, omit);
        mutate(r);
        const before = fs.readFileSync(stateFile(r));
        const result = run(r, "try-inspect");
        assert.ok(result.error, `${name} accepted`);
        assert.ok(before.equals(fs.readFileSync(stateFile(r))), `${name} changed recovery bytes`);
        corruptions++;
    }
    corrupt("hash", r => fs.appendFileSync(indexFile(r, 1), " "));
    corrupt("position", r => updateIndex(r, 1, s => s.records[0].offset++));
    corrupt("coverage", r => updateIndex(r, 1, s => s.records.pop()));
    corrupt("duplicate", r => updateIndex(r, 1, s => s.records[1].message_id = s.records[0].message_id));
    corrupt("witness-channel", r => updateIndex(r, 125, s => s.records.find(row => row.message_id === id(250)).channel_id = other));
    corrupt("raw-correspondence", r => updateIndex(r, 1, s => s.records[0].message_id = id(999)), () => false);
    corrupt("floor-offset", r => { const s = readJSON(stateFile(r)); s.channels[channel].checkpoint_journal_boundary = { segment: 1, offset: 1 }; writeJSON(stateFile(r), s); });
    corrupt("manifest-schema", r => { const m = readJSON(manifestFile(r)); m.version = 2; writeJSON(manifestFile(r), m); });
    corrupt("duplicate-key", r => fs.writeFileSync(manifestFile(r), fs.readFileSync(manifestFile(r), "utf8").replace('"version":1', '"version":1,"version":1')));
    corrupt("missing-sidecar", () => {}, relative => omitted(relative) || relative === "exchange/retention/0000000000000001.ids.json");
    corrupt("missing-manifest", () => {}, relative => omitted(relative) || relative === "exchange/retention-manifest.json");
    corrupt("suffix-gap", () => {}, relative => omitted(relative) || relative === `exchange/events/${file(retiredThrough + 1)}.ndjson`);
    console.log(`RETENTION EVIDENCE VERIFIED: ${segments.length} real segments; ${retiredThrough} retired; 250 replay IDs deduped; ${corruptions} corruption refusals; cache clear and process death passed`);
} finally { fs.rmSync(root, { recursive: true, force: true }); }
