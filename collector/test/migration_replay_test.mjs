// Focused final-protocol tests against the actual native/renderer child harness.
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
const harness = fileURLToPath(new URL("./gc_readiness_test.mjs", import.meta.url));
const roots = [], C = "1545115236619518001";
const id = n => String(1545225000000000000n + BigInt(n));
const fresh = () => { const r = fs.mkdtempSync(path.join(os.tmpdir(), "cordbrief-migration-")); roots.push(r); return r; };
const stateFile = r => path.join(r, "private", "recovery-state.json");
function run(root, op, extra = {}, exit = 0) {
    const p = spawnSync(process.execPath, [harness, "--child"], { input: JSON.stringify({ root, op, ...extra }), encoding: "utf8", timeout: 60000 });
    assert.equal(p.status, exit, p.stderr || p.stdout);
    if (exit) return;
    const line = p.stdout.split("\n").find(l => l.startsWith("RESULT "));
    assert.ok(line, p.stdout); return JSON.parse(line.slice(7));
}
const files = r => fs.readdirSync(path.join(r, "exchange", "events")).filter(f => f.endsWith(".ndjson")).sort();
const records = r => files(r).flatMap(f => fs.readFileSync(path.join(r, "exchange", "events", f), "utf8").split("\n").filter(Boolean).map(JSON.parse));
const once = (r, ids) => ids.forEach(n => assert.equal(records(r).filter(x => x.message_id === id(n)).length, 1, `ID ${n}`));
try {
    // A newly visible ID below committed K must be revisited, even when its older
    // physical peers have fallen outside the forward-only dedupe floor.
    const replay = fresh(); run(replay, "seed", { count: 3 });
    run(replay, "renderer", { ids: [1, 3, 203] });
    run(replay, "renderer", { ids: [1, 2, 3, 202, 203] });
    run(replay, "renderer", { ids: [1, 2, 3, 202, 203] });
    once(replay, [1, 2, 3, 202, 203]);
    assert.equal(run(replay, "inspect").state.channels[C].checkpoint_message_id, id(203));
    run(replay, "cache-clear-live", { ids: [1, 202, 3] }); once(replay, [1, 2, 3, 202, 203]);
    console.log("REPLAY: delayed visibility below high-water recovered; old overlap and empty-cache live replay exactly once.");
    for (const [hook, exit] of [["afterScanBegin", 80], ["afterPage", 81], ["afterScanFinish", 82]]) {
        const r = fresh(); run(r, "seed", { count: 2 });
        run(r, "renderer", { ids: [1, 2, 201, 202], [hook]: true }, exit);
        run(r, "renderer", { ids: [1, 2, 201, 202] });
        run(r, "renderer", { ids: [1, 2, 201, 202] }); once(r, [1, 2, 201, 202]);
    }
    const interrupted = fresh(); run(interrupted, "renderer", { ids: [], afterBegin: true }, 79);
    run(interrupted, "renderer", { ids: [201, 202] }); once(interrupted, [201, 202]);
    console.log("PROCESS CRASH: initial intent, sweep begin, committed page, sweep finish: exactly once after restart.");

    for (const variant of [{ ids: [] }, { restError: true }, { unusable: true }]) {
        const r = fresh(); run(r, "renderer", { ...variant, clockNow: 4102444800000 });
        assert.equal(run(r, "inspect").state.channels[C].watch_after, "0");
        run(r, "renderer", { ids: [201, 202], buffer: [202, 201] });
        run(r, "renderer", { ids: [201, 202] }); once(r, [201, 202]);
    }
    const first = fresh(); run(first, "renderer", { ids: [-4194304, 203] });
    assert.equal(records(first).length, 0, "Initialization itself must not ingest history");
    run(first, "renderer", { ids: [-4194304, 201, 202, 203], buffer: [202, 201] });
    once(first, [201, 202, 203]);
    assert.equal(records(first).filter(r => r.message_id === id(-4194304)).length, 0);
    console.log("FIRST WATCH: clock-independent empty/error retry, reordered immediate live writes, anchor-millisecond overlap.");

    const removed = fresh(); run(removed, "seed", { count: 2 });
    run(removed, "renderer", { ids: [201, 202], removeWatch: true });
    assert.equal(records(removed).filter(r => r.message_id === id(201)).length, 0);
    assert.equal(run(removed, "inspect").state.channels[C].checkpoint_message_id, id(0));
    run(removed, "renderer", { ids: [201, 202] }); once(removed, [201, 202]);
    const base = fresh(); run(base, "seed", { count: 20 });
    const old = run(base, "inspect"); old.state.version = 1;
    for (const ch of Object.values(old.state.channels)) {
        delete ch.watch_after; delete ch.scan_after; delete ch.scan_until; delete ch.checkpoint_source;
        ch.checkpoint_journal_boundary = old.boundary;
    }
    fs.writeFileSync(stateFile(base), JSON.stringify(old.state));
    const invalid = ["", "{", '{"version":1', "null",
        ...[0, 3, 99].map(version => JSON.stringify({ ...old.state, version }))];
    for (const mutate of [
        s => delete s.channels[C].checkpoint_message_id,
        s => s.channels[C].checkpoint_message_id = "abc",
        s => s.channels[C].checkpoint_message_id = "18446744073709551616",
        s => s.channels[C].checkpoint_journal_boundary.segment = -1,
        s => s.channels[C].checkpoint_journal_boundary.offset = 0.5,
        s => s.pending = { channel_id: C },
        s => s.cookies = "secret-marker",
        s => s.channels[C].content = "transcript-marker"
    ]) { const s = structuredClone(old.state); mutate(s); invalid.push(JSON.stringify(s)); }
    for (const content of invalid) {
        const r = fresh(); fs.cpSync(base, r, { recursive: true }); fs.writeFileSync(stateFile(r), content);
        assert.ok(run(r, "try-inspect").error); assert.equal(fs.readFileSync(stateFile(r), "utf8"), content);
    }
    for (const which of [0, 1]) {
        const r = fresh(); fs.cpSync(base, r, { recursive: true });
        fs.renameSync(path.join(r, "exchange", "events", files(r)[which]), path.join(r, "removed-from-exchange.ndjson"));
        const bytes = fs.readFileSync(stateFile(r));
        assert.ok(run(r, "try-inspect").error); assert.deepEqual(fs.readFileSync(stateFile(r)), bytes);
    }
    const alias = fresh(); fs.cpSync(base, alias, { recursive: true });
    fs.copyFileSync(path.join(alias, "exchange", "events", files(alias)[0]), path.join(alias, "exchange", "events", "1.ndjson"));
    const aliasBytes = fs.readFileSync(stateFile(alias)); assert.ok(run(alias, "try-inspect").error);
    assert.deepEqual(fs.readFileSync(stateFile(alias)), aliasBytes);
    const lost = fresh(); fs.cpSync(base, lost, { recursive: true }); fs.renameSync(stateFile(lost), path.join(lost, "saved-state.json"));
    assert.ok(run(lost, "try-inspect").error); assert.equal(fs.existsSync(stateFile(lost)), false);
    assert.deepEqual(run(fresh(), "inspect").state, { version: 2, channels: {}, pending: null });

    const skewed = fresh(); fs.cpSync(base, skewed, { recursive: true });
    const skewState = structuredClone(old.state); skewState.channels[C].checkpoint_message_id = "1840000000000000000";
    fs.writeFileSync(stateFile(skewed), JSON.stringify(skewState));
    run(skewed, "renderer", { ids: [201, 202] }); once(skewed, [201, 202]);
    assert.equal(run(skewed, "inspect").state.channels[C].checkpoint_message_id, "1840000000000000000");
    console.log("LEGACY CLOCK: oversized checkpoint preserved as high-water, never used to exclude replay.");
    const good = run(base, "inspect").state; assert.equal(good.channels[C].checkpoint_message_id, old.state.channels[C].checkpoint_message_id);
    assert.deepEqual(good.channels[C].checkpoint_journal_boundary, { segment: 1, offset: 0 });
    assert.equal(good.channels[C].watch_after, "0");
    const bytes = fs.readFileSync(stateFile(base)); run(base, "inspect"); assert.deepEqual(fs.readFileSync(stateFile(base)), bytes);
    assert.doesNotMatch(bytes.toString(), /disposable fixture|display_name|attachments|cookies|content|localStorage/);

    const pending = fresh(); run(pending, "seed", { count: 2 });
    run(pending, "recover", { ids: [201, 202, 203], partial: true }, 76);
    const p = run(pending, "inspect"); p.state.version = 1;
    p.state.channels[C].checkpoint_journal_boundary = p.boundary; p.state.pending.journal_start = p.boundary;
    run(pending, "raw", { ids: [202] }); fs.writeFileSync(stateFile(pending), JSON.stringify(p.state));
    const migrated = run(pending, "inspect").state;
    assert.deepEqual(migrated.pending.message_ids, [201, 202, 203].map(id));
    assert.deepEqual(migrated.pending.journal_start, { segment: 1, offset: 0 });
    const pendingBytes = fs.readFileSync(stateFile(pending)); run(pending, "inspect"); assert.deepEqual(fs.readFileSync(stateFile(pending)), pendingBytes);
    assert.equal(run(pending, "recover", { ids: [201, 202, 203] }).success, true); once(pending, [201, 202, 203]);

    for (const bad of [false, true]) {
        const r = fresh(); fs.cpSync(base, r, { recursive: true });
        const legacy = path.join(r, "absent-legacy-profile", "cordbrief-recovery-state.json"); fs.mkdirSync(path.dirname(legacy), { recursive: true });
        fs.renameSync(stateFile(r), legacy); fs.writeFileSync(legacy, bad ? "{" : JSON.stringify(old.state));
        if (bad) { assert.ok(run(r, "try-inspect").error); assert.equal(fs.existsSync(stateFile(r)), false); assert.equal(fs.readFileSync(legacy, "utf8"), "{"); }
        else { assert.equal(run(r, "inspect").state.version, 2); const primary = fs.readFileSync(stateFile(r)); fs.writeFileSync(legacy, "{"); run(r, "inspect"); assert.deepEqual(fs.readFileSync(stateFile(r)), primary); }
    }
    console.log(`MIGRATION: ${invalid.length} malformed-state cases, missing/invalid topology, lost/fresh state, legacy import, partial pending v1, idempotence: PASS`);
    console.log("MIGRATION AND REPLAY SAFE");
} finally { for (const r of roots) fs.rmSync(r, { recursive: true, force: true }); }
