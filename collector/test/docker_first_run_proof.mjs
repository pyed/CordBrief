/*
 * Real First-Run Product Proof Runner
 * Orchestrates and monitors the fresh setup and unattended restart flow:
 * 1. Reset dedicated RPC volumes to fresh state (preserving credentials.json and watchlist.json)
 * 2. Observe discord_starting -> discord_login_required
 * 3. Verify on-demand Xpra starts on 127.0.0.1:28742
 * 4. Wait for operator Discord login
 * 5. Observe discord_authenticated -> cordbrief_authorization_required (waiting_operator_approval)
 * 6. Wait for operator OAuth approval
 * 7. Observe oauth_exchange -> catalog_watchlist_ready -> running
 * 8. Verify Xpra stops and port 28742 closes
 * 9. Restart container (docker restart cordbrief-collector)
 * 10. Verify unattended reconnect directly to running with Xpra remaining off
 */

import * as child_process from "child_process";
import * as net from "net";

function exec(cmd) {
    return child_process.execSync(cmd, { encoding: "utf8" }).trim();
}

async function isXpraServing(timeoutMs = 1500) {
    try {
        const controller = new AbortController();
        const timer = setTimeout(() => controller.abort(), timeoutMs);
        const res = await fetch("http://127.0.0.1:28742/", { signal: controller.signal });
        clearTimeout(timer);
        return res.status === 200;
    } catch {
        return false;
    }
}

function getXpraProcesses() {
    try {
        return exec('docker exec cordbrief-collector sh -c "pgrep -f \\"[x]pra shadow\\" || true"');
    } catch {
        return "";
    }
}

function getCollectorStatus() {
    try {
        const raw = exec("docker exec cordbrief-collector cat /var/cordbrief/exchange/collector-status.json");
        return JSON.parse(raw);
    } catch {
        return null;
    }
}

async function waitForState(targetStates, maxWaitSeconds = 60, intervalMs = 1000) {
    const start = Date.now();
    const targets = Array.isArray(targetStates) ? targetStates : [targetStates];
    while ((Date.now() - start) < maxWaitSeconds * 1000) {
        const status = getCollectorStatus();
        if (status && targets.includes(status.collector_state)) {
            return status;
        }
        await new Promise(r => setTimeout(r, intervalMs));
    }
    return null;
}

export async function resetToFreshState() {
    console.log("[Proof] Stopping cordbrief-collector...");
    try { exec("docker compose -f docker/compose.rpc.yml stop cordbrief-collector"); } catch {}

    console.log("[Proof] Resetting volumes to fresh state...");
    const cleanupCmd = 'docker run --rm -v cordbrief_rpc_collector_data:/collector -v cordbrief_rpc_discord_profile:/discord -v cordbrief_rpc_discord_keyring:/keyrings -v cordbrief_rpc_exchange:/exchange debian:bookworm-slim sh -c "rm -f /collector/oauth-token* /collector/recovery-state*; rm -rf /keyrings/*; rm -rf \\"/discord/Local Storage\\" \\"/discord/Session Storage\\" /discord/Cookies* \\"/discord/Network Persistent State\\" /discord/SharedStorage* /discord/WebStorage /discord/Preferences \\"/discord/Local State\\" /discord/blob_storage /discord/Cache \\"/discord/Code Cache\\" /discord/GPUCache /discord/Dawn* /discord/DIPS* /discord/Trust* /discord/Singleton* /discord/logs/*; rm -rf /exchange/events/* /exchange/collector-status.json /exchange/collector-command*"';
    exec(cleanupCmd);

    console.log("[Proof] Starting cordbrief-collector with fresh state...");
    exec("docker compose -f docker/compose.rpc.yml start cordbrief-collector");
}

export async function step1_freshStartup() {
    console.log("\n--- STEP 1: Fresh Startup & Login Detection ---");
    const status = await waitForState(["discord_starting", "discord_login_required"], 30);
    console.log(`  Initial status: state=${status?.collector_state}, auth=${status?.discord_authenticated}`);

    const loginRequired = await waitForState("discord_login_required", 45);
    if (!loginRequired) {
        throw new Error("Failed to reach discord_login_required");
    }
    console.log(`  Reached state: ${loginRequired.collector_state}`);
    console.log(`  Action required: ${loginRequired.action_required}`);

    // Verify Xpra is actively serving HTTP
    let xpraServing = false;
    for (let i = 0; i < 15; i++) {
        xpraServing = await isXpraServing();
        if (xpraServing) break;
        await new Promise(r => setTimeout(r, 1000));
    }
    console.log(`  Xpra HTTP server serving on 127.0.0.1:28742: ${xpraServing}`);
    if (!xpraServing) throw new Error("Xpra is not serving on port 28742");

    return { status: loginRequired, xpraServing };
}

export async function step2_afterLogin() {
    console.log("\n--- STEP 2: Discord Authentication & Consent Prompt ---");
    const authStatus = await waitForState(["discord_authenticated", "cordbrief_authorization_required"], 120);
    console.log(`  Detected state: ${authStatus?.collector_state}`);

    const promptStatus = await waitForState("cordbrief_authorization_required", 30);
    if (!promptStatus) {
        throw new Error("Failed to reach cordbrief_authorization_required");
    }
    console.log(`  Reached state: ${promptStatus.collector_state}`);
    console.log(`  Prompt state: ${promptStatus.prompt_state}`);
    console.log(`  Action required: ${promptStatus.action_required}`);

    return promptStatus;
}

export async function step3_afterApproval() {
    console.log("\n--- STEP 3: OAuth Exchange & Running Transition ---");
    const runningStatus = await waitForState("running", 60);
    if (!runningStatus) {
        throw new Error("Failed to reach running state after approval");
    }
    console.log(`  Reached state: ${runningStatus.collector_state} (mode: ${runningStatus.mode})`);
    console.log(`  Discord authenticated: ${runningStatus.discord_authenticated}`);

    // Verify Xpra has stopped
    await new Promise(r => setTimeout(r, 2000));
    const xpraServing = await isXpraServing();
    console.log(`  Xpra HTTP server serving on 127.0.0.1:28742: ${xpraServing} (expected false)`);

    const pgrep = getXpraProcesses();
    console.log(`  Xpra processes running: '${pgrep}' (expected empty)`);

    // Verify token permissions
    const tokenStat = exec("docker exec cordbrief-collector stat -c '%a' /var/lib/cordbrief/oauth-token.json");
    console.log(`  oauth-token.json file mode: ${tokenStat} (expected 600)`);

    return { runningStatus, xpraStopped: !xpraServing && !pgrep, tokenStat };
}

export async function step4_unattendedRestart() {
    console.log("\n--- STEP 4: Complete Container Restart & Unattended Reconnect ---");
    console.log("  Restarting container: docker restart cordbrief-collector...");
    exec("docker restart cordbrief-collector");

    // Wait 8 seconds for new container instance to initialize
    await new Promise(r => setTimeout(r, 8000));

    const status = await waitForState("running", 45);
    if (!status) {
        throw new Error("Failed to reconnect to running state on restart");
    }
    console.log(`  Post-restart state: ${status.collector_state} (mode: ${status.mode})`);
    console.log(`  Discord authenticated: ${status.discord_authenticated}`);

    // Verify Xpra remained completely off
    const xpraServing = await isXpraServing();
    console.log(`  Xpra HTTP server serving on 127.0.0.1:28742: ${xpraServing} (expected false)`);

    const pgrep = getXpraProcesses();
    console.log(`  Xpra processes running: '${pgrep}' (expected empty)`);

    return { status, xpraRemainedOff: !xpraServing && !pgrep };
}

if (process.argv.includes("--reset")) {
    await resetToFreshState();
} else if (process.argv.includes("--check-login-required")) {
    await step1_freshStartup();
} else if (process.argv.includes("--check-auth-required")) {
    await step2_afterLogin();
} else if (process.argv.includes("--check-running")) {
    await step3_afterApproval();
} else if (process.argv.includes("--check-restart")) {
    await step4_unattendedRestart();
} else if (process.argv.includes("--status")) {
    console.log(JSON.stringify(getCollectorStatus(), null, 2));
}
