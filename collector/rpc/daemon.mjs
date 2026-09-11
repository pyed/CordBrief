/*
 * CordBrief Official Discord RPC Collector Daemon
 * CLI entrypoint for running the official RPC collector service.
 * Manages 7 explicit lifecycle states, OAuth token refresh, on-demand Xpra shadow viewer,
 * anti-spam prompt retry, exchange command protocol, and unattended restarts.
 */

import * as fs from "fs";
import * as path from "path";
import * as child_process from "child_process";
import { EventEmitter } from "events";
import { RpcTransport, findDiscordIPCPath } from "./transport.mjs";
import { DiscordRpcClient, DEFAULT_REDIRECT_URI } from "./protocol.mjs";
import { DiscordRpcCollector, safeReplaceJSON } from "./collector.mjs";

function getEnv(name, defaultValue = "") {
    return process.env[name] || defaultValue;
}

export const COLLECTOR_STATES = {
    DISCORD_STARTING: "discord_starting",
    DISCORD_LOGIN_REQUIRED: "discord_login_required",
    DISCORD_AUTHENTICATED: "discord_authenticated",
    AUTHORIZATION_REQUIRED: "cordbrief_authorization_required",
    OAUTH_EXCHANGE: "oauth_exchange",
    CATALOG_WATCHLIST_READY: "catalog_watchlist_ready",
    NORMAL_OPERATION: "running"
};

export const MODES = {
    SETUP: "setup",
    REAUTH: "reauth",
    NORMAL: "normal"
};

export class RpcCollectorDaemon extends EventEmitter {
    constructor(options = {}) {
        super();
        this.options = options;
        this.exchangeDir = options.exchangeDir || getEnv("CORDBRIEF_EXCHANGE_DIR", "/var/cordbrief/exchange");
        this.collectorDataDir = options.collectorDataDir || getEnv("CORDBRIEF_COLLECTOR_DATA_DIR", path.join(this.exchangeDir, "private"));
        this.runtimeDir = options.runtimeDir || getEnv("CORDBRIEF_RUNTIME_DIR", "/var/cordbrief/runtime");

        this.clientId = options.clientId || getEnv("DISCORD_CLIENT_ID");
        this.clientSecret = options.clientSecret || getEnv("DISCORD_CLIENT_SECRET");
        this.socketPath = options.socketPath || getEnv("DISCORD_IPC_SOCKET");

        this.reloadCredentials();
        this.tokenPath = options.tokenPath || path.join(this.collectorDataDir, "oauth-token.json");
        this.commandFile = path.join(this.exchangeDir, "collector-command.json");
        this.commandAckFile = path.join(this.exchangeDir, "collector-command-ack.json");

        this.transport = options.transport || new RpcTransport({ socketPath: this.socketPath });
        this.transport.on("error", (err) => {
            console.warn(`[RPC Transport] Socket error: ${err?.message || err}`);
        });
        this.client = options.client || new DiscordRpcClient(this.transport);
        this.collector = null;

        // Lifecycle & State Machine
        this.collectorState = COLLECTOR_STATES.DISCORD_STARTING;
        this.mode = MODES.SETUP;
        this.discordAuthenticated = false;
        this.authenticatedUser = null;
        this.promptState = null;
        this.actionRequired = null;
        this.lastError = null;
        this.catalogState = "unavailable";
        this.catalogUpdatedAt = null;
        this.watchedGeneration = 0;
        this.watchedChannelCount = 0;
        this.activeSegment = 1;
        this.lastEventAt = null;
        this.recoveryState = "idle";
        this.recoveryLastAt = null;
        this.recoveryPendingChannels = 0;
        this.recoveryLastError = null;

        // Xpra State
        this.xpraRunning = false;
        this.mockXpra = !!options.mockXpra;

        // Tuning parameters
        this.startupGracePeriodMs = options.startupGracePeriodMs || 5000;
        this.antiSpamCooldownMs = options.antiSpamCooldownMs || 10000;
        this.commandPollIntervalMs = options.commandPollIntervalMs || 1000;

        // Timers
        this.heartbeatTimer = null;
        this.commandTimer = null;
        this.refreshTimer = null;
        this.stopping = false;
    }

    reloadCredentials() {
        if (!this.clientId || !this.clientSecret) {
            const credsPath = path.join(this.collectorDataDir, "credentials.json");
            if (fs.existsSync(credsPath)) {
                try {
                    const creds = JSON.parse(fs.readFileSync(credsPath, "utf8"));
                    if (!this.clientId && creds.client_id) this.clientId = creds.client_id;
                    if (!this.clientSecret && creds.client_secret) this.clientSecret = creds.client_secret;
                } catch {}
            }
        }
    }

    /**
     * Start on-demand Xpra shadow viewer only when operator interaction is genuinely needed.
     */
    startXpra() {
        if (this.xpraRunning) return;
        if (this.mockXpra) {
            this.xpraRunning = true;
            this.emit("xpra_started");
            return;
        }
        try {
            const display = process.env.DISPLAY || ":100";
            const port = process.env.XPRA_PORT || "28742";
            const check = child_process.spawnSync("command -v xpra", { shell: true });
            if (check.status === 0) {
                console.log(`[RPC Daemon] Launching on-demand Xpra shadow on 0.0.0.0:${port} for display ${display}...`);
                child_process.spawn("xpra", [
                    "shadow", display,
                    `--bind-tcp=0.0.0.0:${port}`,
                    "--html=on",
                    "--daemon=yes",
                    "--notifications=no",
                    "--bell=no"
                ], { stdio: "ignore" });
                this.xpraRunning = true;
                this.emit("xpra_started");
            }
        } catch (err) {
            console.warn(`[RPC Daemon] Failed to launch on-demand Xpra: ${err.message}`);
        }
    }

    /**
     * Stop on-demand Xpra shadow viewer when entering unattended normal operation.
     */
    stopXpra() {
        if (!this.xpraRunning) return;
        if (this.mockXpra) {
            this.xpraRunning = false;
            this.emit("xpra_stopped");
            return;
        }
        try {
            const display = process.env.DISPLAY || ":100";
            console.log(`[RPC Daemon] Stopping on-demand Xpra shadow on display ${display}...`);
            child_process.spawnSync("xpra", ["stop", display], { stdio: "ignore" });
            this.xpraRunning = false;
            this.emit("xpra_stopped");
        } catch (err) {
            console.warn(`[RPC Daemon] Failed to stop Xpra: ${err.message}`);
        }
    }

    /**
     * Updates internal state and atomically publishes collector-status.json.
     */
    transitionTo(newState, newMode = null, extra = {}) {
        this.collectorState = newState;
        if (newMode) this.mode = newMode;
        if (extra.discordAuthenticated !== undefined) this.discordAuthenticated = extra.discordAuthenticated;
        if (extra.promptState !== undefined) this.promptState = extra.promptState;
        if (extra.actionRequired !== undefined) this.actionRequired = extra.actionRequired;
        if (extra.lastError !== undefined) this.lastError = extra.lastError;

        // Manage Xpra visibility based on operator interaction requirements
        if (this.collectorState === COLLECTOR_STATES.DISCORD_LOGIN_REQUIRED ||
            this.collectorState === COLLECTOR_STATES.AUTHORIZATION_REQUIRED ||
            this.mode === MODES.REAUTH) {
            this.startXpra();
        } else if (this.collectorState === COLLECTOR_STATES.CATALOG_WATCHLIST_READY ||
                   this.collectorState === COLLECTOR_STATES.NORMAL_OPERATION) {
            this.stopXpra();
        }

        this.publishStatus(extra);
        this.emit("state_change", {
            state: this.collectorState,
            mode: this.mode,
            discordAuthenticated: this.discordAuthenticated,
            promptState: this.promptState,
            actionRequired: this.actionRequired
        });
    }

    /**
     * Publishes strictly validated collector-status.json to exchange directory.
     */
    publishStatus(extra = {}) {
        const statusRecord = {
            version: 1,
            updated_at: new Date().toISOString(),
            mode: this.mode,
            collector_state: this.collectorState,
            discord_authenticated: this.discordAuthenticated,
            catalog_state: this.catalogState,
            catalog_updated_at: this.catalogUpdatedAt,
            watched_generation: this.watchedGeneration,
            watched_channel_count: this.watchedChannelCount,
            active_segment: this.activeSegment,
            last_event_at: this.lastEventAt,
            last_error: extra.lastError !== undefined ? extra.lastError : this.lastError,
            prompt_state: extra.promptState !== undefined ? extra.promptState : this.promptState,
            action_required: extra.actionRequired !== undefined ? extra.actionRequired : this.actionRequired,
            recovery_state: this.recoveryState,
            recovery_last_at: this.recoveryLastAt,
            recovery_pending_channels: this.recoveryPendingChannels,
            recovery_last_error: this.recoveryLastError
        };
        safeReplaceJSON(path.join(this.exchangeDir, "collector-status.json"), statusRecord);
    }

    /**
     * Load existing persisted OAuth token if present.
     * @returns {{accessToken: string, refreshToken?: string, expiresAt?: number}|null}
     */
    loadToken() {
        if (!fs.existsSync(this.tokenPath)) return null;
        try {
            const raw = fs.readFileSync(this.tokenPath, "utf8");
            const data = JSON.parse(raw);
            if (data && typeof data.access_token === "string" && data.access_token.length > 0) {
                return {
                    accessToken: data.access_token,
                    refreshToken: data.refresh_token || null,
                    expiresAt: typeof data.expires_at === "number" ? data.expires_at : null
                };
            }
        } catch {}
        return null;
    }

    /**
     * Persist OAuth token securely with strict 0600 mode.
     */
    saveToken(tokenData) {
        const payload = {
            version: 1,
            access_token: tokenData.accessToken || tokenData.access_token,
            refresh_token: tokenData.refreshToken || tokenData.refresh_token || null,
            scope: tokenData.scope || "rpc identify messages.read",
            saved_at: new Date().toISOString(),
            expires_at: tokenData.expiresIn ? Date.now() + (tokenData.expiresIn * 1000) : (tokenData.expiresAt || null)
        };
        safeReplaceJSON(this.tokenPath, payload, 0o600);
        return payload;
    }

    /**
     * Proactively or reactively refresh OAuth token using refresh_token flow.
     */
    async refreshToken(refreshToken) {
        this.reloadCredentials();
        if (!this.clientId || !this.clientSecret || !refreshToken) {
            throw new Error("Missing client credentials or refresh token for token refresh");
        }
        console.log(`[RPC Daemon] Refreshing OAuth access token via Discord token endpoint...`);
        const refreshed = await this.client.refreshToken({
            clientId: this.clientId,
            clientSecret: this.clientSecret,
            refreshToken: refreshToken
        });
        const saved = this.saveToken(refreshed);
        console.log(`[RPC Daemon] Token refresh succeeded. Next expiration: ${saved.expires_at ? new Date(saved.expires_at).toISOString() : "unknown"}`);
        this.scheduleTokenRefresh(saved.expires_at, saved.refresh_token);
        return refreshed.accessToken;
    }

    /**
     * Schedules proactive background token refresh before the 7-day token expires.
     */
    scheduleTokenRefresh(expiresAt, refreshToken) {
        if (this.refreshTimer) {
            clearTimeout(this.refreshTimer);
            this.refreshTimer = null;
        }
        if (!expiresAt || !refreshToken) return;

        // Refresh 5 minutes before expiration, clamped to valid Node timeout
        const leadTimeMs = 300000;
        const delayMs = Math.max(1000, Math.min(expiresAt - Date.now() - leadTimeMs, 2147483647));
        this.refreshTimer = setTimeout(async () => {
            if (this.stopping) return;
            try {
                const newAccessToken = await this.refreshToken(refreshToken);
                if (this.collector && newAccessToken) {
                    await this.client.authenticate(newAccessToken);
                }
            } catch (err) {
                console.warn(`[RPC Daemon] Background token refresh failed: ${err.message}`);
                this.transitionTo(COLLECTOR_STATES.AUTHORIZATION_REQUIRED, MODES.REAUTH, {
                    lastError: `Token refresh failed: ${err.message}`,
                    actionRequired: "OAuth token refresh failed. Please re-authorize CordBrief."
                });
            }
        }, delayMs);
    }

    /**
     * Connects to Discord IPC socket and determines whether user session is active.
     */
    async connectAndInspectSession(pollIntervalMs = 1000) {
        const startTime = Date.now();
        this.transitionTo(COLLECTOR_STATES.DISCORD_STARTING, MODES.SETUP, {
            discordAuthenticated: false,
            actionRequired: null,
            lastError: null
        });

        let attempt = 0;
        while (!this.stopping) {
            attempt++;
            this.reloadCredentials();
            const socketPath = this.transport.socketPath || findDiscordIPCPath();
            const socketExists = fs.existsSync(socketPath);

            if (socketExists) {
                try {
                    const readyData = await this.transport.connect(this.clientId || "123456789012345678", { timeoutMs: 3000 });
                    if (readyData && readyData.user && readyData.user.id) {
                        this.authenticatedUser = readyData.user;
                        const userTag = readyData.user.username;
                        console.log(`[RPC Daemon] Discord user authenticated: ${userTag} (ID: ${readyData.user.id})`);
                        this.transitionTo(COLLECTOR_STATES.DISCORD_AUTHENTICATED, MODES.SETUP, {
                            discordAuthenticated: true,
                            actionRequired: null,
                            lastError: null
                        });
                        return readyData.user;
                    }
                } catch (err) {
                    // Socket open but session not ready
                }
            }

            // If past grace period and no session, transition to DISCORD_LOGIN_REQUIRED
            if (Date.now() - startTime >= this.startupGracePeriodMs && this.collectorState !== COLLECTOR_STATES.DISCORD_LOGIN_REQUIRED) {
                console.log(`[RPC Daemon] Discord login required. Opening Xpra viewer on loopback...`);
                this.transitionTo(COLLECTOR_STATES.DISCORD_LOGIN_REQUIRED, MODES.SETUP, {
                    discordAuthenticated: false,
                    actionRequired: "Discord login required. Open http://127.0.0.1:28742/ to log into Discord via QR code or credentials.",
                    lastError: "Discord user session not found"
                });
            }

            await new Promise(r => setTimeout(r, pollIntervalMs));
        }
    }

    /**
     * Executes OAuth authorization with anti-spam retry cooldown.
     */
    async requestAuthorizationWithAntiSpam() {
        while (!this.stopping) {
            this.reloadCredentials();
            if (!this.clientSecret) {
                this.transitionTo(COLLECTOR_STATES.AUTHORIZATION_REQUIRED, MODES.SETUP, {
                    promptState: "missing_credentials",
                    actionRequired: "Discord Client Secret missing. Run scripts/update_secret.ps1 to configure credentials.",
                    lastError: "missing_client_secret"
                });
                await new Promise(r => setTimeout(r, 2000));
                continue;
            }

            console.log(`[RPC Daemon] CordBrief OAuth consent prompt waiting inside Discord. Approve at http://127.0.0.1:28742/`);
            this.transitionTo(COLLECTOR_STATES.AUTHORIZATION_REQUIRED, MODES.SETUP, {
                promptState: "waiting_operator_approval",
                actionRequired: "Approve the CordBrief authorization prompt inside Discord (http://127.0.0.1:28742/).",
                lastError: null
            });

            try {
                const authResp = await this.client.authorize({
                    clientId: this.clientId,
                    scopes: ["rpc", "identify", "messages.read"],
                    redirectUri: DEFAULT_REDIRECT_URI
                });

                if (authResp && authResp.code) {
                    return authResp.code;
                }
            } catch (authErr) {
                console.warn(`[RPC Daemon] Authorization prompt dismissed or timed out: ${authErr.message}. Entering anti-spam cooldown...`);
                this.transitionTo(COLLECTOR_STATES.AUTHORIZATION_REQUIRED, MODES.SETUP, {
                    promptState: "cancelled_or_expired",
                    actionRequired: "Authorization prompt was cancelled or expired. Waiting before retry...",
                    lastError: authErr.message
                });
                // Anti-spam cooldown to avoid prompt spam in Discord client
                await new Promise(r => setTimeout(r, this.antiSpamCooldownMs));
            }
        }
        return null;
    }

    /**
     * Processes Core-to-Collector exchange commands from collector-command.json.
     */
    async pollCommands() {
        if (!fs.existsSync(this.commandFile)) return;
        try {
            const raw = fs.readFileSync(this.commandFile, "utf8");
            const cmd = JSON.parse(raw);
            if (!cmd || !cmd.command || !cmd.request_id) return;

            let ackStatus = "applied";
            let ackError = null;

            if (cmd.command === "enter_reauth") {
                console.log(`[RPC Daemon] Received enter_reauth command (${cmd.request_id}).`);
                this.transitionTo(COLLECTOR_STATES.AUTHORIZATION_REQUIRED, MODES.REAUTH, {
                    promptState: "reauth_requested",
                    actionRequired: "Reauthorization requested. Open http://127.0.0.1:28742/ to authenticate."
                });
            } else if (cmd.command === "return_normal") {
                console.log(`[RPC Daemon] Received return_normal command (${cmd.request_id}).`);
                if (this.discordAuthenticated) {
                    this.transitionTo(COLLECTOR_STATES.NORMAL_OPERATION, MODES.NORMAL, {
                        promptState: null,
                        actionRequired: null
                    });
                } else {
                    ackStatus = "rejected";
                    ackError = "Cannot return to normal: Discord session not authenticated.";
                }
            } else if (cmd.command === "status") {
                this.publishStatus();
            } else {
                ackStatus = "unknown_command";
                ackError = `Unrecognized command: ${cmd.command}`;
            }

            // Write acknowledgement atomically
            const ack = {
                version: 1,
                request_id: cmd.request_id,
                command: cmd.command,
                status: ackStatus,
                applied_at: new Date().toISOString(),
                error: ackError
            };
            safeReplaceJSON(this.commandAckFile, ack);
            try { fs.unlinkSync(this.commandFile); } catch {}
            this.emit("command_processed", { command: cmd.command, status: ackStatus, error: ackError });
        } catch (err) {
            console.warn(`[RPC Daemon] Command processing error: ${err.message}`);
        }
    }

    /**
     * Starts the full 7-state productized collector lifecycle.
     */
    async start() {
        if (!this.clientId) {
            this.clientId = getEnv("DISCORD_CLIENT_ID", "123456789012345678");
        }

        console.log(`[RPC Daemon] Starting productized Discord RPC collector (exchange: ${this.exchangeDir})...`);

        // Start command polling loop
        this.commandTimer = setInterval(() => this.pollCommands(), this.commandPollIntervalMs);

        // State 1 & 2: Discord Starting -> Discord Login Required (if needed) -> Discord Authenticated
        await this.connectAndInspectSession();

        // Check for existing OAuth token
        let token = this.loadToken();
        let accessToken = token ? token.accessToken : null;

        // If token exists but is expired or near expiry, attempt refresh
        if (token && token.expiresAt && Date.now() >= (token.expiresAt - 300000)) {
            if (token.refreshToken && this.clientSecret) {
                try {
                    accessToken = await this.refreshToken(token.refreshToken);
                } catch (refreshErr) {
                    console.warn(`[RPC Daemon] Token refresh failed (${refreshErr.message}), requiring new authorization.`);
                    accessToken = null;
                }
            } else {
                accessToken = null;
            }
        }

        // Test existing token with AUTHENTICATE
        if (accessToken) {
            try {
                await this.client.authenticate(accessToken);
                console.log(`[RPC Daemon] Re-authenticated unattended with existing valid OAuth token.`);
            } catch (authErr) {
                console.warn(`[RPC Daemon] Existing token rejected by Discord (${authErr.message}). Attempting refresh...`);
                if (token && token.refreshToken && this.clientSecret) {
                    try {
                        accessToken = await this.refreshToken(token.refreshToken);
                        await this.client.authenticate(accessToken);
                    } catch (rErr) {
                        accessToken = null;
                    }
                } else {
                    accessToken = null;
                }
            }
        }

        // States 4 & 5: Authorization Required & OAuth Exchange (if no valid token)
        if (!accessToken) {
            const authCode = await this.requestAuthorizationWithAntiSpam();
            if (!authCode) return;

            this.transitionTo(COLLECTOR_STATES.OAUTH_EXCHANGE, MODES.SETUP, {
                promptState: "exchanging_code",
                actionRequired: null
            });

            console.log(`[RPC Daemon] Exchanging authorization code for OAuth token...`);
            const tokenResult = await this.client.exchangeToken({
                clientId: this.clientId,
                clientSecret: this.clientSecret,
                code: authCode,
                redirectUri: DEFAULT_REDIRECT_URI
            });

            const saved = this.saveToken(tokenResult);
            accessToken = tokenResult.accessToken;
            if (saved.expires_at) {
                this.scheduleTokenRefresh(saved.expires_at, saved.refresh_token);
            }
            console.log(`[RPC Daemon] Authenticating RPC session with newly acquired token...`);
            await this.client.authenticate(accessToken);
        }

        // State 6: Catalog & Watchlist Ready
        this.transitionTo(COLLECTOR_STATES.CATALOG_WATCHLIST_READY, MODES.SETUP, {
            promptState: null,
            actionRequired: null
        });

        this.collector = new DiscordRpcCollector({
            exchangeDir: this.exchangeDir,
            collectorDataDir: this.collectorDataDir,
            runtimeDir: this.runtimeDir,
            transport: this.transport,
            client: this.client
        });

        await this.collector.start({
            clientId: this.clientId,
            accessToken
        });

        // State 7: Normal Operation
        this.transitionTo(COLLECTOR_STATES.NORMAL_OPERATION, MODES.NORMAL, {
            promptState: null,
            actionRequired: null,
            lastError: null
        });

        console.log(`[RPC Daemon] Official Discord RPC collector is running in normal operation.`);

        // Background status heartbeat
        this.heartbeatTimer = setInterval(() => {
            if (this.stopping) return;
            if (this.collector) {
                this.activeSegment = this.collector.activeSegment;
                this.watchedGeneration = this.collector.watchedGeneration;
                this.watchedChannelCount = this.collector.watchedChannels ? this.collector.watchedChannels.size : 0;
                this.catalogState = this.collector.catalog ? "ready" : "unavailable";
                this.catalogUpdatedAt = this.collector.catalogUpdatedAt;
                this.lastEventAt = this.collector.lastEventAt;
                this.recoveryState = this.collector.recoveryState;
                this.recoveryPendingChannels = this.collector.recoveryPendingChannels;
            }
            this.publishStatus();
        }, 5000);
    }

    /**
     * Clean shutdown.
     */
    async stop() {
        if (this.stopping) return;
        this.stopping = true;
        if (this.heartbeatTimer) clearInterval(this.heartbeatTimer);
        if (this.commandTimer) clearInterval(this.commandTimer);
        if (this.refreshTimer) clearTimeout(this.refreshTimer);

        console.log(`[RPC Daemon] Stopping collector daemon...`);
        this.stopXpra();

        if (this.collector) {
            await this.collector.stop();
            this.collector = null;
        } else if (this.transport) {
            this.transport.close();
        }
        console.log(`[RPC Daemon] Shutdown complete.`);
    }
}

// Entrypoint execution when called directly
if (process.argv[1] && process.argv[1].endsWith("daemon.mjs")) {
    const daemon = new RpcCollectorDaemon();

    const shutdown = async (sig) => {
        console.log(`\n[RPC Daemon] Received ${sig}, initiating graceful shutdown...`);
        await daemon.stop();
        process.exit(0);
    };

    process.on("SIGTERM", () => shutdown("SIGTERM"));
    process.on("SIGINT", () => shutdown("SIGINT"));

    daemon.start().catch(async (err) => {
        console.error(`[RPC Daemon] Fatal error on startup:`, err);
        await daemon.stop().catch(() => {});
        process.exit(1);
    });
}

