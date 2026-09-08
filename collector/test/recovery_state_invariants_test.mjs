// Relationships, not just scalar types: actual native preflight must reject
// impossible states before startup reconciliation, migration, or tail repair.
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
const harness = fileURLToPath(new URL("./gc_readiness_test.mjs", import.meta.url));
const roots = [], C = "1545115236619518001", D = "1545115236619518002";
const id = n => String(1545225000000000000n + BigInt(n));
const fresh = () => { const r = fs.mkdtempSync(path.join(os.tmpdir(), "cordbrief-state-invariants-")); roots.push(r); return r; };
const stateFile = r => path.join(r, "private", "recovery-state.json");
const events = r => path.join(r, "exchange", "events");
function run(root, op, extra = {}) {
    const p = spawnSync(process.execPath, [harness, "--child"], { input: JSON.stringify({ root, op, ...extra }), encoding: "utf8", timeout: 60000 });
    assert.equal(p.status, 0, p.stderr || p.stdout);
    const line = p.stdout.split("\n").find(l => l.startsWith("RESULT "));
    assert.ok(line, p.stdout); return JSON.parse(line.slice(7));
}
function snapshot(root, dir = root) {
    return fs.readdirSync(dir, { withFileTypes: true }).sort((a,b) => a.name.localeCompare(b.name)).flatMap(entry => {
        const file = path.join(dir, entry.name);
        return entry.isDirectory() ? snapshot(root, file) : [[path.relative(root, file), createHash("sha256").update(fs.readFileSync(file)).digest("hex")]];
    });
}
try {
    const base = fresh(); run(base, "seed", { count: 0 });
    const start = run(base, "inspect").boundary;
    run(base, "raw", { ids: [201, 202, 203] });
    const saved = run(base, "inspect");
    const good = structuredClone(saved.state);
    good.channels[C].scan_after = "0"; good.channels[C].scan_until = id(203);
    good.pending = { channel_id: C, old_checkpoint_message_id: id(0), new_checkpoint_message_id: id(203),
        message_ids: [201,202,203].map(id), journal_start: start };
    const cases = [
        ["pending exceeds sweep", s => s.channels[C].scan_until = id(201)],
        ["cursor exceeds K", s => s.channels[C].scan_after = id(202)],
        ["cursor exceeds sweep", s => s.channels[C].scan_after = id(204)],
        ["missing cursor witness", s => { s.channels[C].checkpoint_message_id = id(1000); s.pending.old_checkpoint_message_id = id(1000); s.channels[C].scan_after = id(200); }],
        ["missing REST witness", s => { s.channels[C].checkpoint_source = "rest"; s.channels[C].checkpoint_message_id = id(200); s.pending.old_checkpoint_message_id = id(200); }],
        ["REST witness in wrong channel", s => { s.channels[C].checkpoint_source = "rest"; s.channels[C].checkpoint_message_id = id(100001); s.pending.old_checkpoint_message_id = id(100001); }],
        ["floor hides overlap", s => { s.channels[C].checkpoint_journal_boundary = saved.boundary; s.pending.journal_start = saved.boundary; }],
        ["pending start precedes floor", s => { s.channels[C].checkpoint_journal_boundary = start; s.pending.journal_start = { segment: 1, offset: 0 }; }],
        ["old K differs", s => s.pending.old_checkpoint_message_id = id(1)],
        ["pending does not follow cursor", s => { s.channels[C].checkpoint_message_id = id(202); s.pending.old_checkpoint_message_id = id(202); s.channels[C].scan_after = id(201); }],
        ["pending owner absent", s => s.pending.channel_id = "1545115236619518999"],
        ["pending ID in wrong channel", s => { s.pending.message_ids = [id(100001)]; s.pending.new_checkpoint_message_id = id(100001); s.channels[C].scan_until = id(100001); }],
        ["legacy exclusion introduced", s => s.channels[C].watch_after = (((BigInt(id(0)) >> 22n) << 22n) - 1n).toString()],
        ["pending baseline claims K", s => s.channels[C].checkpoint_source = "baseline_pending"],
        ["REST baseline disagrees with watch", s => s.channels[C].checkpoint_source = "baseline_rest"],
        ["half sweep", s => s.channels[C].scan_after = null],
        ["empty sweep range", s => s.channels[C].scan_until = "0"],
        ["missing global pending", s => delete s.pending],
        ["oversized page", s => { s.pending.message_ids = Array.from({length:101}, (_,i) => id(201+i)); s.pending.new_checkpoint_message_id = id(301); s.channels[C].scan_until = id(301); }],
        ["invalid sibling before reconciliation", s => { s.channels[D].checkpoint_source = "rest"; s.channels[D].checkpoint_message_id = id(999); }]
    ];
    for (const [name, mutate] of cases) {
        const r = fresh(); fs.cpSync(base, r, { recursive: true });
        const corrupt = structuredClone(good); mutate(corrupt); fs.writeFileSync(stateFile(r), JSON.stringify(corrupt));
        // The same invalid state must block active-tail repair, too.
        const last = fs.readdirSync(events(r)).sort().at(-1);
        fs.appendFileSync(path.join(events(r), last), "torn-tail-must-stay");
        const before = snapshot(r);
        assert.ok(run(r, "try-inspect").error, name);
        assert.deepEqual(snapshot(r), before, `${name}: startup mutated files`);
        // A mutation request after failed startup must preserve the same evidence.
        assert.equal(run(r, "recover", { ids: [201,202,203] }).success, false, name);
        assert.deepEqual(snapshot(r), before, `${name}: recovery mutated files`);
    }
    const invalidSave = structuredClone(saved.state); invalidSave.channels[C].scan_after = id(204); invalidSave.channels[C].scan_until = id(205);
    const refused = run(base, "try-save", { state: invalidSave }); assert.ok(refused.error); assert.equal(refused.unchanged, true);
    for (const field of ["undefinedSweep", "undefinedPending"]) {
        const result = run(base, "try-save", { state: saved.state, [field]: true });
        assert.ok(result.error, field); assert.equal(result.unchanged, true, field);
    }

    // Positive controls: until need not exceed K; page end need not advance K;
    // a legacy/direct pending page need not have an active sweep.
    for (const mode of ["normal", "below-high-water", "no-sweep"]) {
        const r = fresh(); fs.cpSync(base, r, { recursive: true });
        const s = structuredClone(good);
        if (mode !== "normal") { s.channels[C].checkpoint_message_id = id(1000); s.pending.old_checkpoint_message_id = id(1000); }
        if (mode === "no-sweep") { s.channels[C].scan_after = null; s.channels[C].scan_until = null; }
        fs.writeFileSync(stateFile(r), JSON.stringify(s));
        const last = fs.readdirSync(events(r)).sort().at(-1), file = path.join(events(r), last);
        const clean = fs.readFileSync(file); fs.appendFileSync(file, "torn");
        const result = run(r, "inspect").state;
        assert.equal(result.pending, null, mode);
        assert.equal(result.channels[C].checkpoint_message_id, mode === "normal" ? id(203) : id(1000));
        assert.deepEqual(fs.readFileSync(file), clean);
        const bytes = fs.readFileSync(stateFile(r)); run(r, "inspect"); assert.deepEqual(fs.readFileSync(stateFile(r)), bytes);
    }
    // All legacy channels validate before migration writes a new primary file.
    const legacy = fresh(); fs.cpSync(base, legacy, { recursive: true });
    const badLegacy = structuredClone(saved.state); badLegacy.version = 1; delete badLegacy.channels[D].checkpoint_message_id;
    const legacyPath = path.join(legacy, "absent-legacy-profile", "cordbrief-recovery-state.json");
    fs.mkdirSync(path.dirname(legacyPath), { recursive: true }); fs.renameSync(stateFile(legacy), legacyPath);
    fs.writeFileSync(legacyPath, JSON.stringify(badLegacy));
    const before = snapshot(legacy); assert.ok(run(legacy, "try-inspect").error); assert.deepEqual(snapshot(legacy), before);
    console.log(`STATE INVARIANTS SAFE: ${cases.length} contradictory states preserve ALL files through startup/recovery; invalid save and legacy import refused; 3 valid reconciliation/tail-repair controls pass.`);
} finally { for (const r of roots) fs.rmSync(r, { recursive: true, force: true }); }
