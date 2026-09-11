/*
 * docker_real_proof.mjs
 * Host test runner executing real Discord proof checks against Docker containers.
 * Corresponds to Gates G1 through G7 in GATES.md.
 */

import assert from "assert";
import { execSync } from "child_process";
import * as path from "path";
import * as fs from "fs";

function runDocker(cmd, timeoutMs = 120000) {
    try {
        const out = execSync(cmd, { encoding: "utf8", timeout: timeoutMs, stdio: ["inherit", "pipe", "pipe"] });
        return out.trim();
    } catch (err) {
        const stdout = err.stdout ? err.stdout.toString() : "";
        const stderr = err.stderr ? err.stderr.toString() : "";
        throw new Error(`Command failed: ${cmd}\n${stdout}\n${stderr}\n${err.message}`);
    }
}

function syncCollectorCode() {
    // Copy latest collector code into container
    runDocker("docker cp collector/. cordbrief-collector:/home/cordbrief/collector/");
}

async function checkSession() {
    syncCollectorCode();
    console.log("[G1] Verifying official Discord client user session...");
    const out = runDocker("docker exec cordbrief-collector node /home/cordbrief/collector/rpc/real_proof_helper.mjs --check-session");
    console.log(out);
    assert.ok(out.includes("discord_user_session_ready"), "Discord user session must be ready");
}

async function checkOAuth() {
    syncCollectorCode();
    console.log("[G2] Verifying Discord OAuth authorization with scopes...");
    const out = runDocker("docker exec cordbrief-collector node /home/cordbrief/collector/rpc/real_proof_helper.mjs --check-oauth", 150000);
    console.log(out);
    assert.ok(out.includes("discord_authenticated_with_scopes"), "Discord OAuth authorization must succeed with scopes");
}

async function checkSubscribe(channelId = "") {
    syncCollectorCode();
    console.log("[G3] Verifying channel subscription for live MESSAGE_CREATE...");
    const arg = channelId ? ` --channel ${channelId}` : "";
    const out = runDocker(`docker exec cordbrief-collector node /home/cordbrief/collector/rpc/real_proof_helper.mjs --check-subscribe${arg}`);
    console.log(out);
    assert.ok(out.includes("discord_channel_subscribed_live"), "Channel subscription must succeed");
}

async function checkLiveMessage(channelId = "") {
    syncCollectorCode();
    console.log("[G4] Listening for natural Discord message arrival and journal write...");
    const arg = channelId ? ` --channel ${channelId}` : "";
    const out = runDocker(`docker exec cordbrief-collector node /home/cordbrief/collector/rpc/real_proof_helper.mjs --check-live-message${arg}`, 240000);
    console.log(out);
    assert.ok(out.includes("natural_discord_message_journaled"), "Natural message must be journaled");
}

async function checkCoreConsumption() {
    syncCollectorCode();
    console.log("[G5] Verifying CordBrief Core consumed the natural journal record...");
    let isRunning = false;
    try {
        const ps = runDocker("docker inspect -f \"{{.State.Running}}\" cordbrief-core");
        isRunning = ps.trim() === "true";
    } catch {}

    if (isRunning) {
        runDocker("docker stop cordbrief-core");
    }

    let ingestOut = "";
    try {
        ingestOut = runDocker("docker run --rm --volumes-from cordbrief-core docker-cordbrief-core exchange ingest -commit --exchange-dir /var/cordbrief/exchange --data-dir /var/cordbrief/data");
        console.log(ingestOut);
    } finally {
        if (isRunning) {
            runDocker("docker start cordbrief-core");
        }
    }

    assert.ok(ingestOut.includes("[PASS] Committed new cursor to core-ack.json") || ingestOut.includes("Events Read:"), "Core ingest must succeed");

    // Read and verify core-ack.json
    const ackRaw = runDocker("docker exec cordbrief-core cat /var/cordbrief/exchange/core-ack.json");
    const ack = JSON.parse(ackRaw);
    assert.ok(ack && ack.version === 1, "core-ack.json must have version 1");
    assert.ok(ack.segment >= 1, "core-ack.json segment must be >= 1");
    assert.ok(ack.offset > 0, "core-ack.json offset must be > 0");

    console.log(`  ✔ CordBrief Core consumed journal: segment ${ack.segment}, byte offset ${ack.offset}.`);
    console.log("core_consumed_natural_message");
}

async function checkOutageRecovery(channelId = "") {
    syncCollectorCode();
    console.log("[G6] Verifying outage recovery sweep via GET_CHANNEL and deduplication...");
    const arg = channelId ? ` --channel ${channelId}` : "";
    const out = runDocker(`docker exec cordbrief-collector node /home/cordbrief/collector/rpc/real_proof_helper.mjs --check-outage-recovery${arg}`);
    console.log(out);
    assert.ok(out.includes("outage_recovery_snapshot_verified"), "Outage recovery sweep must deduplicate and verify");
}

async function checkFullRestart() {
    syncCollectorCode();
    console.log("[G7] Restarting container and verifying reconnect without re-auth...");
    runDocker("docker restart cordbrief-collector");

    // Poll until collector-status.json reports running and authenticated
    console.log("  Waiting for container services and Discord IPC to recover...");
    let recovered = false;
    for (let i = 0; i < 40; i++) {
        await new Promise(r => setTimeout(r, 2000));
        try {
            const raw = runDocker("docker exec cordbrief-collector cat /var/cordbrief/exchange/collector-status.json");
            const status = JSON.parse(raw);
            if (status.collector_state === "running" && status.discord_authenticated === true && status.watched_channel_count >= 1) {
                recovered = true;
                console.log(`  ✔ Collector daemon recovered: state=${status.collector_state}, authenticated=${status.discord_authenticated}, channels=${status.watched_channel_count}`);
                break;
            }
        } catch {}
    }

    assert.ok(recovered, "Collector must recover authenticated state automatically on container restart");

    // Verify live traffic reception after restart
    console.log("  Verifying live message receipt post-restart...");
    const liveOut = runDocker("docker exec cordbrief-collector node /home/cordbrief/collector/rpc/real_proof_helper.mjs --check-live-message", 360000);
    console.log(liveOut);
    assert.ok(liveOut.includes("natural_discord_message_journaled"), "Live message must be captured after restart");

    console.log("authenticated_reconnect_verified");
}

const CHECKS = {
    "--check-session": () => checkSession(),
    "--check-oauth": () => checkOAuth(),
    "--check-subscribe": (ch) => checkSubscribe(ch),
    "--check-live-message": (ch) => checkLiveMessage(ch),
    "--check-core-consumption": () => checkCoreConsumption(),
    "--check-outage-recovery": (ch) => checkOutageRecovery(ch),
    "--check-full-restart": () => checkFullRestart()
};

async function main() {
    const args = process.argv.slice(2);
    const flag = args[0];

    // Extract optional --channel <id>
    let channelId = "";
    const chIdx = args.indexOf("--channel");
    if (chIdx !== -1 && args[chIdx + 1]) {
        channelId = args[chIdx + 1];
    }

    if (flag && CHECKS[flag]) {
        await CHECKS[flag](channelId);
    } else {
        console.log("Usage: node collector/test/docker_real_proof.mjs <flag> [--channel <id>]");
        console.log("Flags: " + Object.keys(CHECKS).join(", "));
        process.exit(1);
    }
}

main().catch(err => {
    console.error("\nCheck failed:", err.message);
    process.exit(1);
});
