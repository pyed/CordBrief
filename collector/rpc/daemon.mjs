/*
 * CordBrief Official Discord RPC Collector Daemon
 * CLI entrypoint for running the official RPC collector service.
 * Manages OAuth token lifecycle, discovers local Discord IPC socket,
 * starts the RPC collector engine, and handles clean shutdown signals.
 */

import * as fs from "fs";
import * as path from "path";
import { RpcTransport, findDiscordIPCPath } from "./transport.mjs";
import { DiscordRpcClient, DEFAULT_REDIRECT_URI } from "./protocol.mjs";
import { DiscordRpcCollector, safeReplaceJSON } from "./collector.mjs";

function getEnv(name, defaultValue = "") {
    return process.env[name] || defaultValue;
}

export class RpcCollectorDaemon {
    constructor(options = {}) {
        this.exchangeDir = options.exchangeDir || getEnv("CORDBRIEF_EXCHANGE_DIR", "/var/cordbrief/exchange");
        this.collectorDataDir = options.collectorDataDir || getEnv("CORDBRIEF_COLLECTOR_DATA_DIR", path.join(this.exchangeDir, "private"));
        this.runtimeDir = options.runtimeDir || getEnv("CORDBRIEF_RUNTIME_DIR", "/var/cordbrief/runtime");

        this.clientId = options.clientId || getEnv("DISCORD_CLIENT_ID");
        this.clientSecret = options.clientSecret || getEnv("DISCORD_CLIENT_SECRET");
        this.socketPath = options.socketPath || getEnv("DISCORD_IPC_SOCKET");

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

        this.reloadCredentials();
        this.tokenPath = options.tokenPath || path.join(this.collectorDataDir, "oauth-token.json");
        this.transport = options.transport || new RpcTransport({ socketPath: this.socketPath });
        this.transport.on("error", (err) => {
            console.warn(`[RPC Transport] Socket error: ${err?.message || err}`);
        });
        this.client = options.client || new DiscordRpcClient(this.transport);
        this.collector = null;
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
     * Writes reauth_required status record.
     */
    writeReauthStatus(reason = "missing_oauth_credentials") {
        const statusRecord = {
            version: 1,
            updated_at: new Date().toISOString(),
            mode: "reauth",
            collector_state: "reauth_required",
            discord_authenticated: false,
            catalog_state: "unavailable",
            catalog_updated_at: null,
            watched_generation: 0,
            watched_channel_count: 0,
            active_segment: 1,
            last_event_at: null,
            last_error: reason,
            recovery_state: "idle",
            recovery_last_at: null,
            recovery_pending_channels: 0,
            recovery_last_error: null
        };
        safeReplaceJSON(path.join(this.exchangeDir, "collector-status.json"), statusRecord);
    }

    /**
     * Connects transport with retry backoff to allow Discord to initialize.
     */
    async connectWithRetry(delayMs = 3000) {
        let attempt = 0;
        while (!this.stopping) {
            attempt++;
            this.reloadCredentials();
            this.writeReauthStatus("awaiting_discord_session");
            const socketPath = this.transport.socketPath || findDiscordIPCPath();
            const socketExists = fs.existsSync(socketPath);
            if (socketExists) {
                try {
                    await this.transport.connect(this.clientId, { timeoutMs: 3000 });
                    console.log(`[RPC Daemon] Connected and received READY from Discord IPC.`);
                    return;
                } catch (err) {
                    if (attempt % 5 === 0) {
                        console.log(`[RPC Daemon] Socket present, awaiting active user session (${err.message})...`);
                    }
                }
            } else {
                if (attempt % 10 === 0) {
                    console.log(`[RPC Daemon] Waiting for Discord to bind IPC socket at ${socketPath}...`);
                }
            }
            await new Promise(r => setTimeout(r, delayMs));
        }
    }

    /**
     * Load existing persisted OAuth token if valid.
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
     * Persist OAuth token securely with 0600 mode.
     */
    saveToken(tokenData) {
        const payload = {
            version: 1,
            access_token: tokenData.accessToken || tokenData.access_token,
            refresh_token: tokenData.refreshToken || tokenData.refresh_token || null,
            scope: tokenData.scope || "rpc identify messages.read",
            saved_at: new Date().toISOString(),
            expires_at: tokenData.expiresIn ? Date.now() + (tokenData.expiresIn * 1000) : null
        };
        safeReplaceJSON(this.tokenPath, payload, 0o600);
    }

    /**
     * Authenticates and starts the collector engine.
     */
    async start() {
        if (!this.clientId) {
            this.clientId = getEnv("DISCORD_CLIENT_ID", "123456789012345678");
        }

        console.log(`[RPC Daemon] Starting Discord RPC collector (exchange: ${this.exchangeDir})...`);

        // 1. Connect local IPC transport with retry
        await this.connectWithRetry();
        console.log(`[RPC Daemon] Connected to Discord IPC socket.`);

        // 2. Check for existing token or execute authorization flow
        let token = this.loadToken();
        let accessToken = token ? token.accessToken : null;

        // If token exists but is expired, attempt refresh
        if (token && token.expiresAt && Date.now() >= (token.expiresAt - 60000)) {
            if (token.refreshToken && this.clientSecret) {
                try {
                    console.log(`[RPC Daemon] Refreshing expired OAuth token...`);
                    const refreshed = await this.client.refreshToken({
                        clientId: this.clientId,
                        clientSecret: this.clientSecret,
                        refreshToken: token.refreshToken
                    });
                    this.saveToken(refreshed);
                    accessToken = refreshed.accessToken;
                    console.log(`[RPC Daemon] OAuth token refreshed successfully.`);
                } catch (refreshErr) {
                    console.warn(`[RPC Daemon] Token refresh failed (${refreshErr.message}), will re-authorize.`);
                    accessToken = null;
                }
            } else {
                accessToken = null;
            }
        }

        if (!accessToken) {
            if (!this.clientSecret) {
                console.log(`[RPC Daemon] No access token or DISCORD_CLIENT_SECRET configured. Running in reauth_required state.`);
                const statusRecord = {
                    version: 1,
                    updated_at: new Date().toISOString(),
                    mode: "reauth",
                    collector_state: "reauth_required",
                    discord_authenticated: false,
                    catalog_state: "unavailable",
                    catalog_updated_at: null,
                    watched_generation: 0,
                    watched_channel_count: 0,
                    active_segment: 1,
                    last_event_at: null,
                    last_error: "missing_oauth_credentials",
                    recovery_state: "idle",
                    recovery_last_at: null,
                    recovery_pending_channels: 0,
                    recovery_last_error: null
                };
                safeReplaceJSON(path.join(this.exchangeDir, "collector-status.json"), statusRecord);

                this.heartbeatTimer = setInterval(() => {
                    if (this.stopping) return;
                    statusRecord.updated_at = new Date().toISOString();
                    safeReplaceJSON(path.join(this.exchangeDir, "collector-status.json"), statusRecord);
                }, 10000);
                return;
            }

            console.log(`[RPC Daemon] Requesting Discord authorization for client ${this.clientId}...`);
            const authResp = await this.client.authorize({
                clientId: this.clientId,
                scopes: ["rpc", "identify", "messages.read"],
                redirectUri: DEFAULT_REDIRECT_URI
            });

            console.log(`[RPC Daemon] Exchanging authorization code for access token...`);
            const tokenResult = await this.client.exchangeToken({
                clientId: this.clientId,
                clientSecret: this.clientSecret,
                code: authResp.code,
                redirectUri: DEFAULT_REDIRECT_URI
            });

            this.saveToken(tokenResult);
            accessToken = tokenResult.accessToken;
            console.log(`[RPC Daemon] OAuth2 token acquired and persisted to ${this.tokenPath}.`);
        }

        // 3. Instantiate and start collector engine
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

        console.log(`[RPC Daemon] Official Discord RPC collector is running.`);
    }

    /**
     * Clean shutdown.
     */
    async stop() {
        if (this.stopping) return;
        this.stopping = true;
        if (this.heartbeatTimer) {
            clearInterval(this.heartbeatTimer);
            this.heartbeatTimer = null;
        }
        console.log(`[RPC Daemon] Stopping collector daemon...`);
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
