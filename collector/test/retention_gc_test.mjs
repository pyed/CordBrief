// Linux-only actual unlink/crash tests, exclusively under a disposable temp root.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import assert from "node:assert/strict";
import crypto from "node:crypto";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
if (process.platform !== "linux") throw new Error("Linux required; no skipped pass");
const collector = fileURLToPath(new URL("..", import.meta.url));
const root = fs.mkdtempSync(path.join(os.tmpdir(), "cordbrief-gc-unlink-"));
const base = path.join(root, "base");
const hash = data => crypto.createHash("sha256").update(data).digest("hex");
const id = n => String(1545225000000000000n + BigInt(n));
const journal = r => path.join(r, "exchange/events");
const files = r => fs.readdirSync(journal(r)).sort();
function native(r, op, extra = {}) {
    const p = spawnSync(process.execPath, [path.join(collector, "test/gc_readiness_test.mjs"), "--child"], {
        input: JSON.stringify({ root: r, op, ...extra }), encoding: "utf8", timeout: 30000
    });
    assert.equal(p.status, 0, p.stderr);
    return JSON.parse(p.stdout.split("\n").find(l => l.startsWith("RESULT ")).slice(7));
}
function env(r) { return { ...process.env, CORDBRIEF_EXCHANGE_DIR: path.join(r, "exchange"),
    CORDBRIEF_COLLECTOR_DATA_DIR: path.join(r, "private"), CORDBRIEF_CORE_DATA_DIR: path.join(r, "core"), CORDBRIEF_RUNTIME_DIR: path.join(r, "runtime") }; }
function invoke(r, args, extra = {}) { return spawnSync("bash", [path.join(collector, "retention-publish.sh"), ...args], {
    env: { ...env(r), ...extra }, encoding: "utf8", timeout: 30000
}); }
function snapshot(r) {
    const out = {};
    function walk(dir) { for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
        const f = path.join(dir, e.name);
        if (e.isDirectory()) walk(f); else out[path.relative(r, f)] = hash(fs.readFileSync(f));
    } }
    walk(r); return out;
}
function clone(label, omit = "") {
    const r = path.join(root, label);
    fs.cpSync(base, r, { recursive: true, filter: source => path.relative(base, source) !== omit || !omit });
    return r;
}
try {
    native(base, "seed", { count: 5 });
    fs.mkdirSync(path.join(base, "core/digests"), { recursive: true });
    fs.mkdirSync(path.join(base, "runtime"));
    const initial = files(base), active = Number(initial.at(-1).slice(0, 16)), through = active - 1;
    assert.ok(through >= 3);
    fs.writeFileSync(path.join(base, "exchange/core-ack.json"), JSON.stringify({ version: 1, segment: active, offset: 0 }));
    const published = invoke(base, [String(through)]);
    assert.equal(published.status, 0, published.stderr);
    const args = [String(through), "--delete-certified"];
    const preload = path.join(root, "inject.cjs");
    fs.writeFileSync(preload, `
const fs=require('node:fs'); const unlink=fs.unlinkSync, sync=fs.fsyncSync;
let n=0; const target=Number(process.env.FAIL_N); const mode=process.env.FAIL_MODE;
fs.unlinkSync=f=>{if(String(f).endsWith('.ndjson')) {n++; if(n===target&&mode==='before-unlink') process.exit(73); unlink(f); if(n===target&&mode==='after-unlink') process.exit(73);} else unlink(f);};
fs.fsyncSync=fd=>{const file=fs.readlinkSync('/proc/self/fd/'+fd);
if(mode==='authority-error'&&file.endsWith('retention-manifest.json')) throw new Error('injected authority sync error');
if(file.endsWith('/events')&&n===target&&mode==='sync-error') throw new Error('injected directory sync error');
sync(fd); if(file.endsWith('/events')&&n===target&&mode==='after-sync') process.exit(73);};
require('node:module').syncBuiltinESMExports();`);
    let crashes = 0;
    for (let n = 1; n <= through; n++) for (const mode of ["before-unlink", "after-unlink", "after-sync", "sync-error"]) {
        const r = clone(`crash-${n}-${mode}`), before = snapshot(r);
        const died = invoke(r, args, { NODE_OPTIONS: `--require=${preload}`, FAIL_N: String(n), FAIL_MODE: mode });
        assert.equal(died.status, mode === "sync-error" ? 1 : 73, died.stderr);
        const removed = mode === "before-unlink" ? n - 1 : n;
        assert.deepEqual(files(r), initial.slice(removed), "unlink order or failure stop violated");
        native(r, "inspect"); // Actual restart accepts only the certified remaining topology.
        const resumed = invoke(r, args);
        assert.equal(resumed.status, 0, resumed.stderr);
        assert.deepEqual(files(r), [initial.at(-1)]);
        const after = snapshot(r);
        for (const [file, digest] of Object.entries(before)) {
            if (!file.startsWith("exchange/events/") || file.endsWith(initial.at(-1))) {
                // Native inspect may update only diagnostic runtime status.
                if (!file.endsWith("collector-runtime-status.json")) assert.equal(after[file], digest, `${file} changed`);
            }
        }
        const replay = native(r, "recover", { ids: [1, 2, 3, 4, 5] });
        assert.equal(replay.success, true, replay.error);
        assert.equal(replay.appendedCount, 0);
        const again = invoke(r, args);
        assert.equal(again.status, 0, again.stderr);
        assert.equal(JSON.parse(again.stdout).deleted, 0);
        crashes++;
    }
    let refusals = 0;
    function refuse(label, mutate = () => {}, omit = "", extra = {}, command = args) {
        const r = clone(label, omit); mutate(r);
        const before = snapshot(r), p = invoke(r, command, extra);
        assert.equal(p.status, 1, `${label}: ${p.stdout} ${p.stderr}`);
        assert.deepEqual(snapshot(r), before, `${label}: invalid state was changed`);
        refusals++;
    }
    refuse("no-manifest", undefined, "exchange/retention-manifest.json");
    refuse("no-sidecar", undefined, "exchange/retention/0000000000000001.ids.json");
    refuse("sidecar-corrupt", r => fs.appendFileSync(path.join(r, "exchange/retention/0000000000000001.ids.json"), " "));
    refuse("closed-corrupt", r => fs.appendFileSync(path.join(journal(r), initial[through - 1]), " "));
    refuse("active-torn", r => fs.appendFileSync(path.join(journal(r), initial.at(-1)), "torn"));
    refuse("gap", undefined, `exchange/events/${initial[1]}`);
    refuse("symlink", r => fs.symlinkSync(path.join(journal(base), initial[0]), path.join(journal(r), initial[0])), `exchange/events/${initial[0]}`);
    refuse("recovery-corrupt", r => fs.writeFileSync(path.join(r, "private/recovery-state.json"), "{}"));
    refuse("pending", r => {
        const p = path.join(r, "private/recovery-state.json"), state = JSON.parse(fs.readFileSync(p));
        state.pending = { channel_id: "1545115236619518001", old_checkpoint_message_id: id(0),
            new_checkpoint_message_id: id(99), message_ids: [id(99)], journal_start: { segment: active, offset: 0 } };
        fs.writeFileSync(p, JSON.stringify(state));
    });
    refuse("ack-behind", r => fs.writeFileSync(path.join(r, "exchange/core-ack.json"), JSON.stringify({ version: 1, segment: through, offset: 0 })));
    refuse("artifact", r => fs.writeFileSync(path.join(r, "core/digests", "a".repeat(64) + ".json"), JSON.stringify({ version: 1, batch_id: "a".repeat(64), digest: { items: [{ source_ids: ["s1"] }] } })));
    refuse("authority-sync-error", undefined, "", { NODE_OPTIONS: `--require=${preload}`, FAIL_MODE: "authority-error" });
    refuse("active-request", undefined, "", {}, [String(active), "--delete-certified"]);
    for (const kind of ["runtime/runtime.lock", "core/commit.lock"]) {
        const r = clone(`lock-${refusals}`), before = snapshot(r);
        const p = spawnSync("flock", ["-n", path.join(r, kind), "bash", path.join(collector, "retention-publish.sh"), ...args], { env: env(r), encoding: "utf8" });
        assert.equal(p.status, 1, p.stderr); assert.deepEqual(snapshot(r), before); refusals++;
    }
    assert.deepEqual(files(base), initial, "Control journal changed");
    let coreVerified = false;
    if (process.env.CORDBRIEF_TEST_CORE_BINARY) {
        const r = clone("core-after-unlink");
        const collected = invoke(r, args);
        assert.equal(collected.status, 0, collected.stderr);
        const consumed = spawnSync(process.env.CORDBRIEF_TEST_CORE_BINARY,
            ["exchange", "ingest", "--exchange-dir", path.join(r, "exchange"), "--data-dir", path.join(r, "core"), "--commit"],
            { encoding: "utf8", timeout: 30000 });
        assert.equal(consumed.status, 0, consumed.stderr);
        assert.match(consumed.stdout, /Events Read: 1\b/);
        const ack = JSON.parse(fs.readFileSync(path.join(r, "exchange/core-ack.json")));
        assert.equal(ack.segment, active);
        assert.equal(ack.offset, fs.statSync(path.join(journal(r), initial.at(-1))).size);
        coreVerified = true;
    }
    console.log(`CERTIFIED GC VERIFIED: ${initial.length} real segments, ${through} deletable; ${crashes} unlink/sync crash cases; ${refusals} immutable refusals; replay and idempotent restart passed; coreVerified=${coreVerified}`);
} finally { fs.rmSync(root, { recursive: true, force: true }); }
