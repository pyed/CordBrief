// Linux process proof: real native journal rotation, publisher, locks and publication crashes.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

if (process.platform !== "linux") throw new Error("Run retention_publish_test under Linux; no skipped pass");
const collector = fileURLToPath(new URL("..", import.meta.url));
const root = fs.mkdtempSync(path.join(os.tmpdir(), "cordbrief-publisher-"));
const seed = spawnSync(process.execPath, [path.join(collector, "test/gc_readiness_test.mjs"), "--child"], {
    input: JSON.stringify({ root, op: "seed", count: 101 }), encoding: "utf8", timeout: 30000
});
assert.equal(seed.status, 0, seed.stderr);
const exchange = path.join(root, "exchange"), core = path.join(root, "core"), runtime = path.join(root, "runtime");
fs.mkdirSync(path.join(core, "digests"), { recursive: true });
fs.mkdirSync(runtime);
const files = fs.readdirSync(path.join(exchange, "events")).sort();
const active = Number(files.at(-1).slice(0, 16));
fs.writeFileSync(path.join(exchange, "core-ack.json"), JSON.stringify({ version: 1, segment: active, offset: 0 }));
const env = { ...process.env, CORDBRIEF_EXCHANGE_DIR: exchange, CORDBRIEF_COLLECTOR_DATA_DIR: path.join(root, "private"), CORDBRIEF_CORE_DATA_DIR: core, CORDBRIEF_RUNTIME_DIR: runtime };
const manifest = path.join(exchange, "retention-manifest.json");
const publish = (extra = {}, through = active - 1) => spawnSync("bash", [path.join(collector, "retention-publish.sh"), String(through)], { env: { ...env, ...extra }, encoding: "utf8", timeout: 30000 });
const denied = spawnSync(process.execPath, [path.join(collector, "retention-publish.mjs"), "1"], { env, encoding: "utf8" });
assert.equal(denied.status, 1, "Unowned descriptors must not publish");
assert.equal(fs.existsSync(manifest), false);
const recoveryPath = path.join(root, "private/recovery-state.json");
const validRecovery = fs.readFileSync(recoveryPath);
const corruptRecovery = Buffer.from('{"version":2,"channels":{},"pending":true}\n');
fs.writeFileSync(recoveryPath, corruptRecovery);
const corrupt = publish();
assert.equal(corrupt.status, 1);
assert.deepEqual(fs.readFileSync(recoveryPath), corruptRecovery, "Corruption must never be rewritten");
assert.equal(fs.existsSync(manifest), false);
assert.equal(fs.existsSync(path.join(exchange, "retention")), false);
fs.writeFileSync(recoveryPath, validRecovery);
// Both independent owners exclude publication; the wrapper never truncates lock files.
for (const lock of [path.join(runtime, "runtime.lock"), path.join(core, "commit.lock")]) {
    const blocked = spawnSync("flock", ["-n", lock, "bash", path.join(collector, "retention-publish.sh"), "1"], { env, encoding: "utf8", timeout: 30000 });
    assert.equal(blocked.status, 1, "Concurrent owner must exclude maintenance");
}
const preload = path.join(root, "crash.cjs");
fs.writeFileSync(preload, `const fs=require('node:fs'); const rename=fs.renameSync; fs.renameSync=(a,b)=>{if(String(b).endsWith(process.env.CRASH_TARGET)){if(process.env.CRASH_AFTER){rename(a,b);} process.exit(73);} return rename(a,b);}; require('node:module').syncBuiltinESMExports();`);
let result = publish({ NODE_OPTIONS: `--require=${preload}`, CRASH_TARGET: "0000000000000001.ids.json", CRASH_AFTER: "1" });
assert.equal(result.status, 73, result.stderr);
assert.equal(fs.existsSync(manifest), false, "Orphan sidecar cannot retire history");
result = publish({ NODE_OPTIONS: `--require=${preload}`, CRASH_TARGET: "retention-manifest.json" });
assert.equal(result.status, 73, result.stderr);
assert.equal(fs.existsSync(manifest), false, "Before commit old topology remains authority");
result = publish({ NODE_OPTIONS: `--require=${preload}`, CRASH_TARGET: "retention-manifest.json", CRASH_AFTER: "1" }, 1);
assert.equal(result.status, 73, result.stderr);
assert.equal(fs.existsSync(manifest), true);
assert.equal(JSON.parse(fs.readFileSync(manifest)).retired_through, 1);
result = publish();
assert.equal(result.status, 0, result.stderr);
const inherited = spawnSync("bash", ["-c", 'exec 9>>"$CORDBRIEF_RUNTIME_DIR/runtime.lock"; flock -n 9 || exit 1; exec bash "$1" "$2"', "fixture", path.join(collector, "retention-publish.sh"), String(active - 1)], { env, encoding: "utf8", timeout: 30000 });
assert.equal(inherited.status, 0, inherited.stderr);
const committed = fs.readFileSync(manifest);
const parsed = JSON.parse(committed);
assert.equal(parsed.retired_through, active - 1);
assert.equal(parsed.segments.length, active - 1);
let identities = 0;
for (const descriptor of parsed.segments) {
    const name = String(descriptor.segment).padStart(16, "0");
    const sidecar = JSON.parse(fs.readFileSync(path.join(exchange, "retention", name + ".ids.json")));
    const raw = fs.readFileSync(path.join(exchange, "events", name + ".ndjson"));
    assert.equal(sidecar.records.length, raw.toString().trim().split("\n").length);
    for (const record of sidecar.records) {
        const event = JSON.parse(raw.subarray(record.offset, record.next_offset));
        assert.equal(record.message_id, event.message_id);
        assert.equal(record.channel_id, event.channel_id);
        identities++;
    }
}
assert.deepEqual(fs.readdirSync(path.join(exchange, "events")).sort(), files, "Publisher deleted nothing");
// Rejected advancement preserves the committed authority byte-for-byte.
result = publish({}, active);
assert.equal(result.status, 1);
assert.deepEqual(fs.readFileSync(manifest), committed);
const firstSidecar = path.join(exchange, "retention/0000000000000001.ids.json");
const validSidecar = fs.readFileSync(firstSidecar);
fs.writeFileSync(firstSidecar, "{}\n");
result = publish();
assert.equal(result.status, 1);
assert.deepEqual(fs.readFileSync(manifest), committed, "Corrupt committed sidecar must not be repaired");
assert.equal(fs.readFileSync(firstSidecar, "utf8"), "{}\n");
fs.writeFileSync(firstSidecar, validSidecar);
// Construct a new view containing only the retained suffix. No original file is removed.
const missing = fs.mkdtempSync(path.join(os.tmpdir(), "cordbrief-publisher-prefix-"));
fs.cpSync(path.join(root, "private"), path.join(missing, "private"), { recursive: true });
fs.mkdirSync(path.join(missing, "exchange/events"), { recursive: true });
fs.cpSync(path.join(exchange, "retention"), path.join(missing, "exchange/retention"), { recursive: true });
for (const file of ["retention-manifest.json", "core-ack.json"]) fs.copyFileSync(path.join(exchange, file), path.join(missing, "exchange", file));
fs.copyFileSync(path.join(exchange, "events", files.at(-1)), path.join(missing, "exchange/events", files.at(-1)));
const afterPrefix = spawnSync("bash", [path.join(collector, "retention-publish.sh"), String(active - 1)], {
    env: { ...env, CORDBRIEF_EXCHANGE_DIR: path.join(missing, "exchange"), CORDBRIEF_COLLECTOR_DATA_DIR: path.join(missing, "private") }, encoding: "utf8", timeout: 30000
});
assert.equal(afterPrefix.status, 0, afterPrefix.stderr);
// Optional cross-language integration: caller supplies a disposable Linux Core
// build. Report explicitly whether it ran; never label absence a passing proof.
let coreVerified = false;
if (process.env.CORDBRIEF_TEST_CORE_BINARY) {
    const consumed = spawnSync(process.env.CORDBRIEF_TEST_CORE_BINARY,
        ["exchange", "ingest", "--exchange-dir", path.join(missing, "exchange"), "--data-dir", core],
        { encoding: "utf8", timeout: 30000 });
    assert.equal(consumed.status, 0, consumed.stderr);
    assert.match(consumed.stdout, /Events Read: 1\b/);
    coreVerified = true;
}
console.log(JSON.stringify({ passed: true, segments: files.length, retired_sidecars: parsed.segments.length, identities, crash_boundaries: 3, lock_refusals: 3, coreVerified, deleted: 0 }));
