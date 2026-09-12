// Synthetic RPC only. Exercise the production owner, watcher, and heartbeat paths.
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { EventEmitter } from "node:events";
import { pathToFileURL } from "node:url";
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
    const daemon = new RpcCollectorDaemon({ exchangeDir: root, collectorDataDir: path.join(root, "private"),
        runtimeDir: path.join(root, "runtime"), transport, client, clientId: "synthetic", clientSecret: "synthetic", mockXpra: true });
    daemon.saveToken({ accessToken: "synthetic", expiresIn: 3600 });
    await daemon.start();
    const collector = daemon.collector;
    return { root, daemon, collector, client, watch,
        records: () => fs.readdirSync(collector.eventsDir).filter(f => f.endsWith(".ndjson")).sort()
            .flatMap(f => fs.readFileSync(path.join(collector.eventsDir, f), "utf8").trim().split("\n").filter(Boolean).map(JSON.parse)),
        close: async () => { await daemon.stop(); fs.rmSync(root, { recursive: true, force: true }); }
    };
}

export async function runStateOwnershipTests() {
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
