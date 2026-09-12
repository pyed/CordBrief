// Synthetic RPC only. Exercise the production owner, watcher, and heartbeat paths.
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { EventEmitter } from "node:events";
import { pathToFileURL } from "node:url";
import { syncBuiltinESMExports } from "node:module";
import { RpcCollectorDaemon } from "../rpc/daemon.mjs";
import { deriveFirstWatchBoundary } from "../rpc/retention.mjs";

const id = n => String(1545225000000000000n + BigInt(n) * 4194304n);
const message = (n, channel_id = "2001") => ({ id: id(n), channel_id, author: { id: "1001" }, content: String(n) });
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
async function until(predicate) {
    const deadline = Date.now() + 5000;
    while (!predicate()) {
        assert(Date.now() < deadline, "Timed out waiting for test transition");
        await new Promise(r => setTimeout(r, 10));
    }
}
async function fixture(channels = []) {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "cb-ownership-"));
    const watchPath = path.join(root, "watchlist.json");
    let generation = 0;
    const watch = ids => fs.writeFileSync(watchPath, JSON.stringify({ version: 1, generation: ++generation, channel_ids: ids }));
    watch(channels);
    const transport = new EventEmitter();
    transport.socketPath = path.join(root, "synthetic-socket");
    fs.writeFileSync(transport.socketPath, "");
    transport.connect = async () => ({ user: { id: "1001", username: "synthetic" } });
    transport.close = () => {};
    const client = {
        authenticate: async () => {}, getGuilds: async () => [],
        subscribeMessageCreate: async () => {}, unsubscribeMessageCreate: async () => {},
        getChannel: async channel => ({ id: channel, messages: [message(1, channel)] })
    };
    const options = { exchangeDir: root, collectorDataDir: path.join(root, "private"),
        runtimeDir: path.join(root, "runtime"), transport, client, clientId: "synthetic", clientSecret: "synthetic", mockXpra: true };
    let daemon = new RpcCollectorDaemon(options);
    daemon.saveToken({ accessToken: "synthetic", expiresIn: 3600 });
    await daemon.start();
    let collector = daemon.collector;
    return { root, get daemon() { return daemon; }, get collector() { return collector; }, client, watch,
        restart: async () => {
            await daemon.stop();
            daemon = new RpcCollectorDaemon(options);
            await daemon.start();
            collector = daemon.collector;
        },
        records: () => fs.readdirSync(collector.eventsDir).filter(f => f.endsWith(".ndjson")).sort()
            .flatMap(f => fs.readFileSync(path.join(collector.eventsDir, f), "utf8").trim().split("\n").filter(Boolean).map(JSON.parse)),
        close: async () => { await daemon.stop(); fs.rmSync(root, { recursive: true, force: true }); }
    };
}

export async function runStateOwnershipTests() {
    // First-watch provenance must survive loss of recovery-state even BEFORE the
    // first journal record. Exercise the actual subscription gap, restart, and repair.
    {
        const f = await fixture();
        const subscribed = deferred();
        let calls = 0, subscriptions = 0;
        try {
            f.client.getChannel = async channel => ({ id: channel,
                messages: channel === "2002" ? [] : (++calls === 1 ? [message(1)] : [message(1), message(2), message(3)]) });
            f.client.subscribeMessageCreate = async () => { subscriptions++; await subscribed.promise; };
            f.watch(["2001"]);
            const pass = f.daemon.retryRecovery();
            await until(() => subscriptions === 1);
            const valid = fs.readFileSync(f.collector.recoveryStatePath, "utf8");
            const baseline = JSON.parse(valid).channels["2001"].watch_after;
            assert.equal(baseline, deriveFirstWatchBoundary(id(1)));
            assert.equal(f.records().length, 0, "No journal evidence may hide the zero-message bug");
            fs.unlinkSync(f.collector.recoveryStatePath);
            subscribed.resolve();
            await pass;
            assert.equal(f.daemon.collectorState, "error", "Missing initialized state must not become running");
            assert.equal(f.daemon.recoveryState, "error");
            assert(f.daemon.lastError && f.daemon.recoveryLastError);
            assert.equal(fs.existsSync(f.collector.recoveryStatePath), false, "Never manufacture replacement state");
            assert.equal(calls, 1, "Lost state must not cause another baseline observation");
            const anchors = fs.readFileSync(f.collector.recoveryAnchorsPath, "utf8");
            assert.equal(JSON.parse(anchors).channels["2001"], baseline);
            await f.daemon.heartbeat();
            await f.restart(); // A new collector cannot rely on an in-memory baseline.
            assert.equal(f.daemon.collectorState, "error");
            assert.equal(f.daemon.recoveryState, "error");
            assert.equal(calls, 1);
            assert.equal(f.records().length, 0);
            assert.equal(fs.readFileSync(f.collector.recoveryAnchorsPath, "utf8"), anchors);
            // A missing entry, changed cutoff, or malformed primary cannot bypass
            // the anchor either, even with no journal witness in this new process.
            const moved = JSON.parse(valid);
            moved.channels["2001"].watch_after = deriveFirstWatchBoundary(id(3));
            for (const invalid of [JSON.stringify({ version: 2, channels: {} }), JSON.stringify(moved), "{corrupt"]) {
                fs.writeFileSync(f.collector.recoveryStatePath, invalid);
                await f.daemon.retryRecovery();
                assert.equal(f.daemon.collectorState, "error");
                assert.equal(f.daemon.recoveryState, "error");
                assert.equal(calls, 1);
                assert.equal(fs.readFileSync(f.collector.recoveryStatePath, "utf8"), invalid);
                assert.equal(fs.readFileSync(f.collector.recoveryAnchorsPath, "utf8"), anchors);
            }
            fs.writeFileSync(f.collector.recoveryStatePath, valid);
            for (const invalid of ["{corrupt", JSON.stringify({ version: 1, channels: [] })]) {
                fs.writeFileSync(f.collector.recoveryAnchorsPath, invalid);
                await f.daemon.retryRecovery();
                assert.equal(f.daemon.collectorState, "error");
                assert.equal(f.daemon.recoveryState, "error");
                assert.equal(calls, 1);
                assert.equal(fs.readFileSync(f.collector.recoveryAnchorsPath, "utf8"), invalid);
            }
            fs.writeFileSync(f.collector.recoveryAnchorsPath, anchors);
            await f.daemon.heartbeat();
            assert.equal(f.daemon.collectorState, "running");
            assert.equal(f.collector.loadRecoveryState().channels["2001"].watch_after, baseline);
            for (const n of [2, 3]) assert.equal(f.records().filter(r => r.message_id === id(n)).length, 1);

            // Pre-upgrade primary state has no sidecar. Adoption must preserve its
            // established boundary before a newer snapshot is observed.
            fs.unlinkSync(f.collector.recoveryAnchorsPath);
            await f.restart();
            assert.equal(JSON.parse(fs.readFileSync(f.collector.recoveryAnchorsPath)).channels["2001"], baseline);
            for (const n of [2, 3]) assert.equal(f.records().filter(r => r.message_id === id(n)).length, 1);

            // A genuinely new, empty channel still establishes an explicit zero
            // baseline. Removal/re-addition resumes that episode, including restart.
            f.watch(["2001", "2002"]);
            await f.daemon.retryRecovery();
            const emptyBaseline = f.collector.loadRecoveryState().channels["2002"].watch_after;
            assert.equal(emptyBaseline, "0");
            assert.equal(f.records().filter(r => r.channel_id === "2002").length, 0);
            f.watch(["2001"]);
            await f.daemon.retryRecovery();
            await f.restart();
            f.client.getChannel = async channel => ({ messages: channel === "2002" ? [message(4, channel), message(5, channel)] : [message(1), message(2), message(3)] });
            f.watch(["2001", "2002"]);
            await f.daemon.retryRecovery();
            assert.equal(f.collector.loadRecoveryState().channels["2002"].watch_after, emptyBaseline);
            for (const n of [4, 5]) assert.equal(f.records().filter(r => r.message_id === id(n)).length, 1);
        } finally { subscribed.resolve(); await f.close(); }
    }
    // Anchor commits precede primary-state commits. A failure between the two
    // files must survive restart as an error, with no subscription or new query.
    {
        const f = await fixture();
        const rename = fs.renameSync;
        let calls = 0, subscriptions = 0, interrupted = false;
        try {
            f.client.getChannel = async () => { calls++; return { messages: [message(1)] }; };
            f.client.subscribeMessageCreate = async () => { subscriptions++; };
            fs.renameSync = (from, to) => {
                if (to === f.collector.recoveryStatePath) {
                    assert.equal(JSON.parse(fs.readFileSync(f.collector.recoveryAnchorsPath)).channels["2001"], deriveFirstWatchBoundary(id(1)));
                    interrupted = true;
                    throw new Error("Injected primary commit failure after durable anchor");
                }
                return rename(from, to);
            };
            syncBuiltinESMExports();
            f.watch(["2001"]);
            await f.daemon.retryRecovery();
            fs.renameSync = rename;
            syncBuiltinESMExports();
            assert(interrupted, "Must reach the actual primary rename after anchor persistence");
            assert.equal(fs.existsSync(f.collector.recoveryStatePath), false);
            assert.equal(subscriptions, 0);
            await f.restart();
            assert.equal(calls, 1);
            assert.equal(subscriptions, 0);
            assert.equal(f.daemon.collectorState, "error");
            assert.equal(f.daemon.recoveryState, "error");
        } finally { fs.renameSync = rename; syncBuiltinESMExports(); await f.close(); }
    }
    // All real entry points converge: watcher starts a pending baseline, heartbeat and
    // another watchlist edit arrive during it. Subscription must use the FIRST baseline.
    {
        const f = await fixture();
        const baseline = deferred(), subscribed = deferred();
        let calls = 0, subscriptions = 0;
        try {
            f.client.getChannel = async channel => {
                if (channel !== "2001") return { id: channel, messages: [] };
                if (++calls === 1) return baseline.promise;
                return { id: channel, messages: [message(1), message(2), message(3)] };
            };
            f.client.subscribeMessageCreate = async channel => {
                if (channel === "2001") { subscriptions++; await subscribed.promise; }
            };
            f.watch(["2001"]);
            await until(() => calls === 1); // actual fs.watchFile callback owns this pass
            const heartbeatRetry = f.daemon.retryRecovery();
            const sameRetry = f.daemon.retryRecovery();
            assert.equal(heartbeatRetry, sameRetry);
            f.watch(["2001", "2002"]); // must be handled, even though a pass is active
            await new Promise(r => setTimeout(r, 1100));
            assert.equal(calls, 1, "Another entry point started a second baseline query");
            baseline.resolve({ id: "2001", messages: [message(1)] });
            await until(() => subscriptions === 1);
            const first = f.collector.loadRecoveryState().channels["2001"].watch_after;
            assert.equal(first, deriveFirstWatchBoundary(id(1)));
            // X=2 exists after baseline observation, before live subscription is active.
            subscribed.resolve();
            await heartbeatRetry;
            await until(() => f.collector.activeSubscriptions.has("2002"));
            await f.collector.reconcileWatchlist();
            assert.equal(subscriptions, 1);
            assert.equal(f.collector.loadRecoveryState().channels["2001"].watch_after, first);
            assert.equal(f.records().filter(r => r.message_id === id(2)).length, 1, "Subscription-gap X was lost");
            assert.equal(f.collector.recoveryStateStatus, "ready");
        } finally { baseline.resolve({ messages: [] }); subscribed.resolve(); await f.close(); }
    }

    // A is skipped as healthy, B awaits Discord, and a newer live error invalidates
    // the pass. Its old state must neither repair the file nor erase the missed A event.
    {
        const f = await fixture(["2001"]);
        const snapshot = deferred();
        let bCalls = 0;
        try {
            f.client.getChannel = async channel => {
                if (channel === "2001") return { id: channel, messages: [message(1), message(2)] };
                if (++bCalls === 2) return snapshot.promise;
                return { id: channel, messages: [message(3, channel)] };
            };
            f.watch(["2001", "2002"]);
            const pass = f.collector.reconcileWatchlist();
            await until(() => bCalls === 2);
            const valid = fs.readFileSync(f.collector.recoveryStatePath, "utf8");
            fs.writeFileSync(f.collector.recoveryStatePath, "{corrupt");
            f.collector.handleMessageCreate({ channel_id: "2001", message: message(2) });
            snapshot.resolve({ id: "2002", messages: [message(3, "2002")] });
            await pass;
            f.daemon.publishStatus();
            assert.equal(fs.readFileSync(f.collector.recoveryStatePath, "utf8"), "{corrupt", "Stale snapshot overwrote corruption");
            assert.equal(f.daemon.collectorState, "error");
            assert.equal(f.daemon.recoveryState, "error");
            fs.writeFileSync(f.collector.recoveryStatePath, valid);
            await f.daemon.heartbeat(); // real automatic retry path, without directly recovering A
            assert.equal(f.daemon.collectorState, "running");
            assert.equal(f.daemon.recoveryState, "ready");
            assert.equal(f.records().filter(r => r.message_id === id(2)).length, 1);
        } finally { snapshot.resolve({ messages: [] }); await f.close(); }
    }

    // Live capture mutates the checkpoint during an awaited snapshot. Merge the
    // current durable state; never replace it with the pre-await checkpoint.
    {
        const f = await fixture(["2001"]);
        const snapshot = deferred();
        let entered = false;
        try {
            f.client.getChannel = async () => { entered = true; return snapshot.promise; };
            const pass = f.collector.recoverChannelSnapshot("2001");
            await until(() => entered);
            f.collector.handleMessageCreate({ channel_id: "2001", message: message(4) });
            snapshot.resolve({ id: "2001", messages: [message(1), message(2)] });
            await pass;
            assert.equal(f.collector.loadRecoveryState().channels["2001"].checkpoint_message_id, id(4));
            assert.equal(f.records().filter(r => r.message_id === id(2)).length, 1);
            assert.equal(f.records().filter(r => r.message_id === id(4)).length, 1);
        } finally { snapshot.resolve({ messages: [] }); await f.close(); }
    }
    // Shutdown invalidates pending RPC work and discards queued triggers. Neither
    // the old pass nor its daemon retry continuation may revive running status.
    {
        const f = await fixture(["2001"]);
        const baseline = deferred();
        let calls = 0;
        try {
            f.client.getChannel = async () => { calls++; return baseline.promise; };
            f.watch(["2001", "2002"]);
            const pass = f.daemon.retryRecovery();
            await until(() => calls === 1);
            const queued = f.collector.establishFirstWatchBaseline("2003");
            const before = fs.readFileSync(f.collector.recoveryStatePath, "utf8");
            await f.daemon.stop();
            baseline.resolve({ messages: [message(3, "2002")] });
            await Promise.all([pass, queued, f.collector.reconcileWatchlist()]);
            assert.equal(calls, 1, "Shutdown started queued recovery");
            assert.equal(fs.readFileSync(f.collector.recoveryStatePath, "utf8"), before);
            assert.equal(f.collector.activeSubscriptions.size, 0);
            assert.equal(JSON.parse(fs.readFileSync(path.join(f.root, "collector-status.json"))).collector_state, "stopped");
        } finally { baseline.resolve({ messages: [] }); await f.close(); }
    }
    console.log("rpc_state_ownership_tests_passed");
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
    await runStateOwnershipTests();
}
