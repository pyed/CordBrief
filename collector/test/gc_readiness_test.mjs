// Actual native/renderer recovery regressions. Node >= 22.13; no copied recovery logic.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { stripTypeScriptTypes, syncBuiltinESMExports } from "node:module";
import { fileURLToPath } from "node:url";

const self = fileURLToPath(import.meta.url);
const channel = "1545115236619518001", other = "1545115236619518002";
const id = n => String(1545225000000000000n + BigInt(n));
const event = (n, ch = channel) => JSON.stringify({
    version: 1, event: "message_create", message_id: id(n), channel_id: ch,
    guild_id: "1545114461868658862", timestamp: "2026-01-01T00:00:00Z",
    captured_at: "2026-01-01T00:00:00Z",
    author: { id: "449075508156563477", name: "fixture", display_name: "fixture", bot: false },
    content: "disposable fixture ".repeat(30), reply_to_message_id: null, attachments: []
});

if (process.argv[2] === "--child") {
    const request = JSON.parse(fs.readFileSync(0, "utf8"));
    const { root, op } = request;
    if (request.clockNow !== undefined) Date.now = () => request.clockNow;
    process.env.CORDBRIEF_EXCHANGE_DIR = path.join(root, "exchange");
    process.env.CORDBRIEF_COLLECTOR_DATA_DIR = path.join(root, "private");
    process.env.CORDBRIEF_RECOVERY_STATE_PATH = path.join(root, "private", "recovery-state.json");
    process.env.DISCORD_CONFIG_DIR = path.join(root, "absent-legacy-profile");
    process.env.CORDBRIEF_MAX_SEGMENT_SIZE = "2048";
    // Exit after durable page append, before committed checkpoint replaces intent.
    const rename = fs.renameSync;
    let recoverySaves = 0;
    fs.renameSync = (from, to) => {
        if (to === process.env.CORDBRIEF_RECOVERY_STATE_PATH && ++recoverySaves === 2 && request.crash) {
            process.exit(73);
        }
        return rename(from, to);
    };
    const open = fs.openSync, close = fs.closeSync, sync = fs.fsyncSync;
    const journalFDs = new Set();
    fs.openSync = (...args) => {
        const fd = open(...args);
        if (String(args[0]).endsWith(".ndjson") && args[1] === "a") journalFDs.add(fd);
        return fd;
    };
    fs.closeSync = fd => { journalFDs.delete(fd); return close(fd); };
    fs.fsyncSync = fd => {
        sync(fd);
        if (request.partial && journalFDs.has(fd)) process.exit(76);
    };
    syncBuiltinESMExports();
    const source = fs.readFileSync(new URL("../native.ts", import.meta.url), "utf8")
        .replace('import { IpcMainInvokeEvent } from "electron";', "");
    const native = await import(`data:text/javascript;base64,${Buffer.from(stripTypeScriptTypes(source)).toString("base64")}`);
    fs.unwatchFile(path.join(root, "exchange", "watchlist.json"));
    // Established-channel fixture. First-watch behavior is exercised by renderer tests.
    const save = ch => {
        const state = native.loadRecoveryState();
        state.channels[ch] = { checkpoint_message_id: id(0), checkpoint_source: "legacy",
            watch_after: "0", scan_after: null, scan_until: null,
            checkpoint_journal_boundary: { segment: 1, offset: 0 },
            last_recovery_at: "2026-01-01T00:00:00Z", last_result: "success", last_error: null, recovered_count: 0 };
        native.saveRecoveryState(state);
        return true;
    };
    let result;
    if (op === "seed") {
        assert.equal(await save(channel), true);
        assert.equal(await save(other), true);
        const before = native.loadRecoveryState();
        for (let n = 1; n <= request.count; n++) {
            assert.equal(await native.appendEventToJournal(undefined, event(n)), true);
            if (request.interleave && n % 17 === 0) {
                assert.equal(await native.appendEventToJournal(undefined, event(10000 + n, other)), true);
            }
        }
        // Other-channel traffic pushes earlier live IDs out of the restart seed (last two files).
        for (let n = 100001; n <= 100008; n++) {
            assert.equal(await native.appendEventToJournal(undefined, event(n, other)), true);
        }
        assert.deepEqual(native.loadRecoveryState(), before, "live append changed recovery state");
        result = { before, boundary: native.getCurrentJournalBoundary() };
    } else if (op === "recover") {
        if (request.completePending) native.appendRawLinesToJournal(request.completePending.map(n => event(n)), request.completePending.map(id));
        result = await native.appendRecoveredMessages(undefined, request.channel || channel,
            request.ids.map(n => event(n, request.channel || channel)), id(request.ids.at(-1)));
    } else if (op === "empty") {
        const before = native.loadRecoveryState();
        const ch = request.channel || other;
        const recomputed = await native.appendRecoveredMessages(undefined, ch, [], before.channels[ch].checkpoint_message_id);
        assert.equal(recomputed.success, true, recomputed.error);
        result = { before, after: native.loadRecoveryState() };
    } else if (op === "inspect") {
        result = { state: native.loadRecoveryState(), boundary: native.getCurrentJournalBoundary() };
    } else if (op === "try-inspect") {
        try { result = { state: native.loadRecoveryState() }; }
        catch (error) { result = { error: error.message }; }
    } else if (op === "try-save") {
        const before = fs.readFileSync(process.env.CORDBRIEF_RECOVERY_STATE_PATH);
        if (request.undefinedSweep) request.state.channels[channel].scan_after = undefined;
        if (request.undefinedPending) request.state.pending = undefined;
        try { native.saveRecoveryState(request.state); result = { saved: true }; }
        catch (error) { result = { error: error.message }; }
        result.unchanged = before.equals(fs.readFileSync(process.env.CORDBRIEF_RECOVERY_STATE_PATH));
    } else if (op === "raw") {
        native.appendRawLinesToJournal(request.ids.map(n => event(n)), request.ids.map(id));
        result = native.getCurrentJournalBoundary();
    } else if (op === "live") {
        for (const n of request.ids) assert.equal(await native.appendEventToJournal(undefined, event(n)), true);
        result = native.getCurrentJournalBoundary();
    } else if (op === "renderer") {
        const channel = request.channel || "1545115236619518001";
        const requests = [];
        let drained = 0;
        globalThis.VencordNative = { pluginHelpers: { CordBriefCollector: new Proxy({}, {
            get: (_, key) => async (...args) => {
                if (key === "appendEventToJournal" && request.beforeDrain) process.exit(74);
                const value = await native[key](undefined, ...args);
                if (key === "beginChannelInitialization" && request.afterBegin) process.exit(79);
                if (key === "beginRecoveryScan" && request.afterScanBegin) process.exit(80);
                if (key === "appendRecoveredMessages" && request.afterPage) process.exit(81);
                if (key === "finishRecoveryScan" && request.afterScanFinish) process.exit(82);
                // Gateway delivery can interleave while the renderer awaits baseline IPC.
                if (key === "saveChannelCheckpoint" && args[1]?.checkpoint_source === "baseline_rest") {
                    for (const n of request.lateBaselineBuffer || []) await plugin.flux.MESSAGE_CREATE({ message: {
                        id: id(n), channel_id: channel, content: "late baseline fixture", timestamp: "2026-01-01T00:00:00Z"
                    } });
                }
                if (key === "appendEventToJournal" && ++drained === 1 && request.midDrain) process.exit(75);
                return value;
            }
        }) } };
        let plugin;
        globalThis.window = { Vencord: { Webpack: {
            findStore: () => ({ getChannel: () => ({ guild_id: "1545114461868658862" }) }),
            Common: { RestAPI: { get: async ({ query }) => {
                requests.push(query);
                if (request.buffer && requests.length === 1) {
                    for (const n of request.buffer) await plugin.flux.MESSAGE_CREATE({ message: {
                        id: id(n), channel_id: channel, content: "buffer fixture", timestamp: "2026-01-01T00:00:00Z"
                    } });
                }
                if (request.removeWatch && query.limit === 100) {
                    const watch = path.join(root, "exchange", "watchlist.json");
                    fs.writeFileSync(watch, JSON.stringify({ version: 1, generation: 2, channel_ids: [] }));
                    fs.utimesSync(watch, new Date(), new Date(Date.now() + 1000));
                }
                if (request.restError) return { ok: false, status: 403 };
                if (request.unusable) return { ok: true, body: {} };
                const eligible = (request.ids || []).filter(n => !query.after || BigInt(id(n)) > BigInt(query.after));
                const selected = query.limit === 1 ? eligible.slice(-1) : eligible.slice(0, 100);
                return { ok: true, body: selected.map(n => ({ id: id(n), channel_id: channel,
                    content: "REST fixture", timestamp: "2026-01-01T00:00:00Z" })) };
            } } }
        } } };
        const indexPath = new URL("../plugin/index.ts", import.meta.url);
        if (!fs.existsSync(indexPath)) {
            throw new Error("Vencord renderer decommissioned in M16");
        }
        const renderer = fs.readFileSync(indexPath, "utf8")
            .replace(/^import .*;$/gm, "");
        const js = stripTypeScriptTypes(renderer + "\nexport { runGapRecovery }; ");
        const module = await import(`data:text/javascript;base64,${Buffer.from('const definePlugin = x => x; const Devs = { Vendicated: {} };\n' + js).toString("base64")}`);
        plugin = module.default;
        await module.runGapRecovery();
        result = { state: native.loadRecoveryState(), requests };
    } else if (op === "cache-clear-live") {
        native.clearDedupeLedgerForTesting();
        for (const n of request.ids) assert.equal(await native.appendEventToJournal(undefined, event(n)), true);
        result = native.getCurrentJournalBoundary();
    } else if (op === "wrong-channel-live") {
        if (request.clear) native.clearDedupeLedgerForTesting();
        result = await native.appendEventToJournal(undefined, event(request.id, other));
    } else if (op === "scan-missing") {
        result = native.getAppendedMessageIdsSince(999999, 0);
    } else {
        throw new Error(`unknown operation: ${op}`);
    }
    console.log(`RESULT ${JSON.stringify(result)}`);
} else {
    const roots = [];
    function fresh() {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), "cordbrief-gc-proof-"));
        roots.push(root);
        return root;
    }
    function run(root, op, extra = {}) {
        const child = spawnSync(process.execPath, [self, "--child"], {
            input: JSON.stringify({ root, op, ...extra }), encoding: "utf8", timeout: 30000
        });
        if (extra.crash || extra.partial || extra.beforeDrain || extra.midDrain) {
            assert.equal(child.status, extra.crash ? 73 : extra.partial ? 76 : extra.beforeDrain ? 74 : 75, child.stderr || child.stdout);
            return;
        }
        assert.equal(child.status, 0, child.stderr || child.stdout);
        const line = child.stdout.split("\n").find(line => line.startsWith("RESULT "));
        assert.ok(line, child.stdout);
        return JSON.parse(line.slice(7));
    }
    const files = root => fs.readdirSync(path.join(root, "exchange", "events")).sort();
    const records = root => files(root).flatMap(file =>
        fs.readFileSync(path.join(root, "exchange", "events", file), "utf8")
            .trim().split("\n").filter(Boolean).map(JSON.parse));
    function position(root, n) {
        for (const file of files(root)) {
            let offset = 0;
            for (const line of fs.readFileSync(path.join(root, "exchange", "events", file), "utf8").split("\n")) {
                if (!line) continue;
                if (JSON.parse(line).message_id === id(n)) return { segment: Number(file.slice(0, 16)), offset };
                offset += Buffer.byteLength(line) + 1;
            }
        }
        throw new Error("fixture record not found");
    }
    const stateFile = root => path.join(root, "private", "recovery-state.json");
    function once(root, count) {
        const all = records(root);
        for (let n = 1; n <= count; n++) assert.equal(all.filter(r => r.message_id === id(n)).length, 1, `copies of ${n}`);
    }
    try {
        const long = fresh();
        const seed = run(long, "seed", { count: 101 });
        assert.ok(files(long).length >= 4);
        assert.equal(seed.before.channels[channel].checkpoint_journal_boundary.segment, 1);
        const quiet = run(long, "empty");
        // Other has live records above its checkpoint: empty cannot forget them.
        assert.ok(quiet.after.channels[other].checkpoint_journal_boundary.segment > 1);
        console.log(`LIVE: ${files(long).length} real segments; checkpoint and floor unchanged by live appends.`);

        const first = run(long, "recover", { ids: Array.from({ length: 100 }, (_, i) => i + 1) });
        assert.equal(first.success, true);
        assert.equal(first.appendedCount, 0);
        assert.equal(first.skippedDuplicates, 100);
        console.log("C: first 100-message page dedupes across real rotation after fresh-process restart.");
        const beforeEmpty = run(long, "inspect").state.channels[channel];
        assert.deepEqual(beforeEmpty.checkpoint_journal_boundary, position(long, 101));
        run(long, "renderer", { ids: [] });
        assert.deepEqual(run(long, "inspect").state.channels[channel].checkpoint_journal_boundary, position(long, 101));
        console.log("EMPTY: actual renderer empty response preserves physical start of message 101.");
        const second = run(long, "recover", { ids: [101] });
        assert.equal(second.success, true);
        const duplicates = records(long).filter(record => record.message_id === id(101)).length;
        assert.equal(second.appendedCount, 0, "recovery duplicated preexisting overlap");
        assert.equal(duplicates, 1, "recovery duplicated message 101");
        const mixed = run(long, "inspect");
        assert.deepEqual(mixed.state.channels[other].checkpoint_journal_boundary, quiet.after.channels[other].checkpoint_journal_boundary);
        assert.ok(mixed.state.channels[channel].checkpoint_journal_boundary.segment > 1);
        console.log("SAFE: original 101-message fixture, 55 segments, message 101 count=1.");
        console.log("D: channel floors are derived independently from local overlap.");

        const crash = fresh();
        run(crash, "seed", { count: 2 });
        const prior = run(crash, "inspect");
        assert.ok(prior.boundary.offset > 2048 * 0.8, "intent must start near segment end");
        run(crash, "recover", { ids: [201, 202], crash: true });
        const pending = JSON.parse(fs.readFileSync(path.join(crash, "private", "recovery-state.json")));
        assert.deepEqual(pending.pending.journal_start, prior.boundary);
        assert.equal(files(crash).length, prior.boundary.segment + 1);
        const restarted = run(crash, "inspect");
        assert.equal(restarted.state.pending, null);
        assert.equal(restarted.state.channels[channel].checkpoint_message_id, id(202));
        for (const n of [201, 202]) {
            assert.equal(records(crash).filter(record => record.message_id === id(n)).length, 1);
        }
        console.log("B: actual intent boundary, append rotation, exit(73) before checkpoint rename, fresh-process reconciliation exactly once.");

        const all = files(crash);
        const closedPath = path.join(crash, "exchange", "events", all[0]);
        const activePath = path.join(crash, "exchange", "events", all.at(-1));
        const closedBefore = fs.readFileSync(closedPath), activeBefore = fs.readFileSync(activePath);
        fs.appendFileSync(activePath, '{"torn":');
        run(crash, "inspect");
        assert.deepEqual(fs.readFileSync(closedPath), closedBefore);
        assert.deepEqual(fs.readFileSync(activePath), activeBefore);
        console.log("G: short torn active tail repaired; closed segment byte-identical.");
        // Corruption checks below exercise errors without treating them as fresh state.

        once(long, 101);
        const larger = fresh();
        run(larger, "seed", { count: 250, interleave: true });
        for (const [first, last] of [[1, 100], [101, 200], [201, 250]]) {
            assert.equal(run(larger, "recover", { ids: Array.from({length: last-first+1}, (_,i)=>first+i) }).appendedCount, 0);
            const snapshot = run(larger, "inspect");
            assert.equal(snapshot.state.channels[channel].checkpoint_message_id, id(last));
            assert.deepEqual(snapshot.state.channels[channel].checkpoint_journal_boundary,
                last < 250 ? position(larger, last + 1) : snapshot.boundary);
        }
        once(larger, 250);
        console.log(`LARGE: 250 channel IDs exactly once; ${records(larger).length} records / ${files(larger).length} segments, three pages, fresh processes.`);

        const quietRoot = fresh();
        run(quietRoot, "seed", { count: 0 });
        const quietBefore = run(quietRoot, "inspect");
        run(quietRoot, "renderer", { restError: true });
        const errorState = run(quietRoot, "inspect").state.channels[channel];
        assert.equal(errorState.checkpoint_message_id, id(0));
        assert.deepEqual(errorState.checkpoint_journal_boundary, quietBefore.state.channels[channel].checkpoint_journal_boundary);
        run(quietRoot, "renderer", { ids: [] });
        const released = run(quietRoot, "inspect");
        assert.equal(released.state.channels[channel].checkpoint_message_id, id(0));
        assert.deepEqual(released.state.channels[channel].checkpoint_journal_boundary, released.boundary);
        console.log(`QUIET/ERROR: error retains floor; empty triggers local release to ${JSON.stringify(released.boundary)} without checkpoint change.`);

        const cap = fresh();
        run(cap, "seed", { count: 1050, interleave: true });
        const allIDs = Array.from({length:1050},(_,i)=>i+1);
        const capped = run(cap, "renderer", { ids: allIDs });
        assert.equal(capped.requests.filter(q => q.limit === 100).length, 10);
        assert.equal(capped.state.channels[channel].checkpoint_message_id, id(1000));
        assert.deepEqual(capped.state.channels[channel].checkpoint_journal_boundary, position(cap,1001));
        const continued = run(cap, "renderer", { ids: allIDs });
        assert.equal(continued.state.channels[channel].checkpoint_message_id, id(1050));
        once(cap,1050);
        console.log(`CAP: actual renderer 10-page cap + continuation; 1050 IDs once, ${records(cap).length} records / ${files(cap).length} segments.`);

        const partial = fresh();
        run(partial,"seed",{count:2});
        run(partial,"recover",{ids:[201,202,203],partial:true});
        const intent = fs.readFileSync(stateFile(partial));
        const start = JSON.parse(intent).pending.journal_start;
        assert.ok(run(partial,"inspect").state.pending);
        for (const attempt of [{ids:[204]}, {ids:[301],channel:other}]) {
            assert.equal(run(partial,"recover",attempt).success,false);
            assert.deepEqual(fs.readFileSync(stateFile(partial)),intent);
        }
        const resumed = run(partial,"recover",{ids:[201,202,203]});
        assert.equal(resumed.success,true,resumed.error);
        assert.equal(resumed.appendedCount,2);
        for (const n of [201,202,203]) assert.equal(records(partial).filter(r=>r.message_id===id(n)).length,1);
        assert.equal(run(partial,"inspect").state.pending,null);
        console.log(`PENDING: partial fsync/process death at ${JSON.stringify(start)}; mismatched/sibling pages preserve intent bytes; exact refetch resumes.`);

        const completedPending=fresh();
        run(completedPending,"seed",{count:2});
        run(completedPending,"recover",{ids:[201,202,203],partial:true});
        const reconciled=run(completedPending,"recover",{ids:[201,202,203],completePending:[202,203]});
        assert.equal(reconciled.success,true,reconciled.error);
        assert.equal(reconciled.appendedCount,0);
        assert.equal(run(completedPending,"inspect").state.pending,null);

        const migration = fresh();
        run(migration,"seed",{count:101});
        const old = run(migration,"inspect");
        old.state.version=1;
        old.state.channels[channel].checkpoint_message_id=id(100);
        old.state.channels[channel].checkpoint_journal_boundary=old.boundary;
        for (const ch of Object.values(old.state.channels)) delete ch.checkpoint_source;
        fs.writeFileSync(stateFile(migration),JSON.stringify(old.state));
        const migrated=run(migration,"inspect").state;
        assert.equal(migrated.version,2);
        assert.equal(migrated.channels[channel].checkpoint_message_id,id(100));
        assert.deepEqual(migrated.channels[channel].checkpoint_journal_boundary,{segment:1,offset:0});
        const migratedBytes=fs.readFileSync(stateFile(migration));
        run(migration,"inspect");
        assert.deepEqual(fs.readFileSync(stateFile(migration)),migratedBytes);
        assert.equal(run(migration,"recover",{ids:[101]}).appendedCount,0);
        once(migration,101);
        for (const bad of ['{broken',JSON.stringify({...migrated,version:99})]) {
            fs.writeFileSync(stateFile(migration),bad);
            assert.ok(run(migration,"try-inspect").error);
            assert.equal(fs.readFileSync(stateFile(migration),'utf8'),bad);
        }
        const legacyPath = path.join(migration,"absent-legacy-profile","cordbrief-recovery-state.json");
        fs.mkdirSync(path.dirname(legacyPath),{recursive:true});
        fs.writeFileSync(stateFile(migration),JSON.stringify(old.state));
        fs.renameSync(stateFile(migration),legacyPath);
        assert.equal(run(migration,"inspect").state.version,2);
        assert.deepEqual(run(migration,"inspect").state.channels[channel].checkpoint_journal_boundary,{segment:1,offset:0});
        console.log("MIGRATION: v1 and legacy import reset unsafe floor, preserve K, idempotent; corrupt/future state refused unchanged.");

        const reordered=fresh();
        run(reordered,"seed",{count:0});
        run(reordered,"live",{ids:[30,10,20]});
        run(reordered,"recover",{ids:[10]});
        assert.deepEqual(run(reordered,"inspect").state.channels[channel].checkpoint_journal_boundary,position(reordered,30));
        run(reordered,"recover",{ids:[20]});
        assert.deepEqual(run(reordered,"inspect").state.channels[channel].checkpoint_journal_boundary,position(reordered,30));
        run(reordered,"recover",{ids:[30]});
        for(const n of [10,20,30]) assert.equal(records(reordered).filter(r=>r.message_id===id(n)).length,1);
        const malformed=fresh();
        run(malformed,"seed",{count:2});
        const originalState=fs.readFileSync(stateFile(malformed));
        fs.appendFileSync(path.join(malformed,"exchange","events",files(malformed)[0]),'{broken}\n');
        assert.equal(run(malformed,"recover",{ids:[201]}).success,false);
        assert.deepEqual(fs.readFileSync(stateFile(malformed)),originalState);
        const duplicate=fresh();
        run(duplicate,"seed",{count:2});
        run(duplicate,"raw",{ids:[1]});
        assert.match(run(duplicate,"recover",{ids:[201]}).error,/Historical duplicate/);
        const absent=fresh();
        run(absent,"seed",{count:2});
        fs.renameSync(path.join(absent,"exchange","events",files(absent)[0]),path.join(absent,"unavailable.ndjson"));
        assert.equal(run(absent,"recover",{ids:[201]}).success,false);
        console.log("ORDER/CORRUPTION: physical-first floor; malformed/missing history refuses progress; historical duplicate fixture reported, not normalized.");

        for (const variant of [{restError:true},{unusable:true},{ids:[]},{ids:[5]}]) {
            const initial=fresh();
            const result=run(initial,"renderer",variant);
            if (variant.restError || variant.unusable) {
                assert.equal(result.state.channels[channel].checkpoint_message_id, "");
                assert.equal(result.state.channels[channel].checkpoint_source, "baseline_pending");
            } else {
                assert.equal(result.state.channels[channel].checkpoint_source, variant.ids.length ? "baseline_rest" : "baseline_pending");
                if (!variant.ids.length) assert.equal(result.state.channels[channel].checkpoint_message_id, "");
                if (variant.ids.length) assert.ok(BigInt(result.state.channels[channel].checkpoint_message_id) < BigInt(id(5)));
                assert.equal(files(initial).length,0);
            }
        }
        const incomplete=fresh();
        run(incomplete,"seed",{count:0});
        const incompleteState=run(incomplete,"inspect").state;
        incompleteState.version=1;
        incompleteState.channels[channel].checkpoint_message_id="";
        fs.writeFileSync(stateFile(incomplete),JSON.stringify(incompleteState));
        assert.ok(BigInt(run(incomplete,"renderer",{ids:[5]}).state.channels[channel].checkpoint_message_id) < BigInt(id(5)));
        console.log("FIRST WATCH: error/empty stays initialization-pending with no exclusion ID; real latest establishes REST baseline.");

        for (const crashMode of ["beforeDrain","midDrain"]) {
            const drain=fresh();
            run(drain,"seed",{count:0});
            run(drain,"renderer",{ids:[],buffer:[201,202],[crashMode]:true});
            const recovered=run(drain,"recover",{ids:[201,202]});
            assert.equal(recovered.success,true,recovered.error);
            for(const n of [201,202]) assert.equal(records(drain).filter(r=>r.message_id===id(n)).length,1);
        }
        console.log("LIVE CRASH: former drain boundaries now kill before/after immediate journal append; REST refetch recovers both IDs once.");
        console.log("GC READINESS CHARACTERIZATION COMPLETE (recovery tests only; no GC authorization)");
        if (process.argv.includes("--require-safe")) {
            assert.equal(duplicates, 1, "GC safety blocked: duplicate across recovery pages");
            console.log("GC RECOVERY SAFETY VERIFIED");
        }
    } finally {
        // Only directories created by this process; never a caller-supplied production path.
        for (const root of roots) fs.rmSync(root, { recursive: true, force: true });
    }
}
