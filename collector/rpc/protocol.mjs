/*
 * Discord RPC Protocol Client
 * High-level RPC commands and OAuth token exchange according to official Discord RPC documentation.
 */

export const DEFAULT_SCOPES = ["rpc", "identify", "messages.read"];
export const DEFAULT_REDIRECT_URI = "http://127.0.0.1:32145/callback";
export const DISCORD_TOKEN_ENDPOINT = "https://discord.com/api/oauth2/token";

export class DiscordRpcClient {
    /**
     * @param {import('./transport.mjs').RpcTransport} transport
     */
    constructor(transport) {
        this.transport = transport;
        this.authenticatedUser = null;
        this.grantedScopes = [];
    }

    /**
     * Request local user authorization through Discord Desktop client.
     * @param {object} params
     * @param {string} params.clientId - Discord Application Client ID
     * @param {string[]} [params.scopes] - Scopes to request (default: rpc, identify, messages.read)
     * @param {string} [params.rpcToken] - Optional previous RPC token
     * @returns {Promise<{code: string}>} Authorization code for OAuth token exchange
     */
    async authorize({ clientId, scopes = DEFAULT_SCOPES, rpcToken = undefined }) {
        const args = {
            client_id: String(clientId),
            scopes
        };
        if (rpcToken) args.rpc_token = rpcToken;

        const data = await this.transport.request("AUTHORIZE", args, null, 120000);
        if (!data || !data.code) {
            throw new Error("Discord AUTHORIZE response did not contain an authorization code");
        }
        return { code: data.code };
    }

    /**
     * Exchange authorization code for access token via Discord's OAuth2 token endpoint.
     * @param {object} params
     * @param {string} params.clientId
     * @param {string} params.clientSecret
     * @param {string} params.code
     * @param {string} [params.redirectUri]
     * @param {string} [params.endpoint] - Optional custom endpoint for testing
     * @returns {Promise<{accessToken: string, refreshToken?: string, expiresIn: number, scope: string}>}
     */
    async exchangeToken({ clientId, clientSecret, code, redirectUri = DEFAULT_REDIRECT_URI, endpoint = DISCORD_TOKEN_ENDPOINT }) {
        const bodyParams = new URLSearchParams({
            client_id: String(clientId),
            client_secret: String(clientSecret),
            grant_type: "authorization_code",
            code: String(code),
            redirect_uri: String(redirectUri)
        });

        const res = await fetch(endpoint, {
            method: "POST",
            headers: {
                "Content-Type": "application/x-www-form-urlencoded"
            },
            body: bodyParams.toString()
        });

        if (!res.ok) {
            const errText = await res.text();
            throw new Error(`OAuth token exchange failed (HTTP ${res.status}): ${errText}`);
        }

        const data = await res.json();
        if (!data.access_token) {
            throw new Error("OAuth token exchange response did not contain access_token");
        }

        return {
            accessToken: data.access_token,
            refreshToken: data.refresh_token,
            expiresIn: data.expires_in,
            scope: data.scope
        };
    }

    /**
     * Refresh OAuth2 access token using a refresh token.
     * @param {object} params
     * @param {string} params.clientId
     * @param {string} params.clientSecret
     * @param {string} params.refreshToken
     * @param {string} [params.endpoint]
     * @returns {Promise<{accessToken: string, refreshToken?: string, expiresIn: number, scope: string}>}
     */
    async refreshToken({ clientId, clientSecret, refreshToken, endpoint = DISCORD_TOKEN_ENDPOINT }) {
        const bodyParams = new URLSearchParams({
            client_id: String(clientId),
            client_secret: String(clientSecret),
            grant_type: "refresh_token",
            refresh_token: String(refreshToken)
        });

        const res = await fetch(endpoint, {
            method: "POST",
            headers: {
                "Content-Type": "application/x-www-form-urlencoded"
            },
            body: bodyParams.toString()
        });

        if (!res.ok) {
            const errText = await res.text();
            throw new Error(`OAuth token refresh failed (HTTP ${res.status}): ${errText}`);
        }

        const data = await res.json();
        if (!data.access_token) {
            throw new Error("OAuth token refresh response did not contain access_token");
        }

        return {
            accessToken: data.access_token,
            refreshToken: data.refresh_token,
            expiresIn: data.expires_in,
            scope: data.scope
        };
    }

    /**
     * Authenticates the RPC connection using an OAuth2 access token.
     * @param {string} accessToken
     * @returns {Promise<object>} User and scope confirmation
     */
    async authenticate(accessToken) {
        const data = await this.transport.request("AUTHENTICATE", {
            access_token: String(accessToken)
        });

        this.authenticatedUser = data.user || null;
        this.grantedScopes = Array.isArray(data.scopes) ? data.scopes : [];

        // Verify required scopes are granted
        for (const reqScope of ["rpc", "messages.read"]) {
            if (!this.grantedScopes.includes(reqScope)) {
                console.warn(`[DiscordRpcClient] Warning: Expected scope ${reqScope} was not confirmed by Discord (got ${this.grantedScopes.join(", ")})`);
            }
        }

        return data;
    }

    /**
     * Returns list of guilds visible to the user.
     * @returns {Promise<Array<{id: string, name: string, icon_url?: string}>>}
     */
    async getGuilds() {
        const data = await this.transport.request("GET_GUILDS");
        return Array.isArray(data?.guilds) ? data.guilds : [];
    }

    /**
     * Returns channels for a given guild.
     * @param {string} guildId
     * @returns {Promise<Array<{id: string, name: string, type: number}>>}
     */
    async getChannels(guildId) {
        const data = await this.transport.request("GET_CHANNELS", {
            guild_id: String(guildId)
        });
        return Array.isArray(data?.channels) ? data.channels : [];
    }

    /**
     * Subscribes to live MESSAGE_CREATE events on a channel.
     * @param {string} channelId
     * @returns {Promise<object>}
     */
    async subscribeMessageCreate(channelId) {
        return this.transport.request("SUBSCRIBE", { channel_id: String(channelId) }, "MESSAGE_CREATE");
    }

    /**
     * Unsubscribes from live MESSAGE_CREATE events on a channel.
     * @param {string} channelId
     * @returns {Promise<object>}
     */
    async unsubscribeMessageCreate(channelId) {
        return this.transport.request("UNSUBSCRIBE", { channel_id: String(channelId) }, "MESSAGE_CREATE");
    }

    /**
     * Fetches channel information and recent message snapshot.
     * Note: Subject to probe's verified limitation - returns recent snapshot, has no pagination.
     * @param {string} channelId
     * @returns {Promise<{id: string, name: string, type: number, guild_id?: string, messages?: Array<any>}>}
     */
    async getChannel(channelId) {
        return this.transport.request("GET_CHANNEL", { channel_id: String(channelId) });
    }
}
