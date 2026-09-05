/*
 * CordBrief Collector Plugin - Renderer Process Component
 * Hooks Discord Gateway MESSAGE_CREATE events, enforces fail-closed channel filtering,
 * normalizes events to Schema v1, manages startup gap recovery, and delegates disk I/O to native bridge.
 */

import definePlugin, { PluginNative } from "@utils/types";
import { Devs } from "@utils/constants";
import type * as NativeTypes from "./native";

interface RawDiscordAttachment {
    id: string;
    filename: string;
    size: number;
    content_type?: string;
}

interface RawDiscordMessage {
    id: string;
    channel_id: string;
    guild_id?: string;
    timestamp: string;
    content: string;
    author?: {
        id: string;
        username: string;
        global_name?: string;
        bot?: boolean;
    };
    message_reference?: {
        message_id?: string;
    };
    attachments?: RawDiscordAttachment[];
}

interface NormalizedAttachment {
    id: string;
    filename: string;
    content_type: string;
    size: number;
}

interface CordBriefEventV1 {
    version: 1;
    event: "message_create";
    message_id: string;
    guild_id: string;
    channel_id: string;
    timestamp: string;
    captured_at: string;
    author: {
        id: string;
        name: string;
        display_name: string;
        bot: boolean;
    };
    content: string;
    reply_to_message_id: string | null;
    attachments: NormalizedAttachment[];
}

const Native = VencordNative.pluginHelpers.CordBriefCollector as PluginNative<typeof NativeTypes>;

const recoveringChannels = new Set<string>();
const reconciledChannels = new Set<string>();
const pendingLiveMessages = new Map<string, RawDiscordMessage[]>();
let isRecoveryRunning = false;

function getGuildIdForChannel(channelId: string): string | null {
    try {
        const W = (window as any).Vencord?.Webpack;
        if (!W) return null;
        const ChannelStore = W.findStore("ChannelStore");
        if (ChannelStore) {
            if (typeof ChannelStore.getChannel === "function") {
                const ch = ChannelStore.getChannel(channelId);
                if (ch) {
                    const gId = ch.guild_id || (typeof ch.getGuildId === "function" ? ch.getGuildId() : null);
                    if (gId && typeof gId === "string" && gId.trim().length > 0) {
                        return gId.trim();
                    }
                }
            }
            if (typeof ChannelStore.getBasicChannel === "function") {
                const ch = ChannelStore.getBasicChannel(channelId);
                if (ch && ch.guild_id && typeof ch.guild_id === "string" && ch.guild_id.trim().length > 0) {
                    return ch.guild_id.trim();
                }
            }
        }
        const GuildStore = W.findStore("GuildStore");
        if (GuildStore && ChannelStore && typeof GuildStore.getGuilds === "function" && typeof ChannelStore.getMutableGuildChannelsForGuild === "function") {
            const guilds = GuildStore.getGuilds();
            if (guilds) {
                for (const g of Object.values(guilds) as any[]) {
                    if (!g || !g.id) continue;
                    const map = ChannelStore.getMutableGuildChannelsForGuild(g.id);
                    if (map && map[channelId]) {
                        return String(g.id).trim();
                    }
                }
            }
        }
        return null;
    } catch {
        return null;
    }
}

function normalizeMessage(message: RawDiscordMessage, explicitGuildId?: string): CordBriefEventV1 {
    const guildId = explicitGuildId || String(message.guild_id || "");
    const attachments: NormalizedAttachment[] = (message.attachments || []).map(a => ({
        id: String(a.id),
        filename: String(a.filename || "unnamed"),
        content_type: String(a.content_type || "application/octet-stream"),
        size: Number(a.size) || 0
    }));

    return {
        version: 1,
        event: "message_create",
        message_id: String(message.id),
        guild_id: guildId,
        channel_id: String(message.channel_id),
        timestamp: String(message.timestamp || new Date().toISOString()),
        captured_at: new Date().toISOString(),
        author: {
            id: String(message.author?.id || ""),
            name: String(message.author?.username || "unknown"),
            display_name: String(message.author?.global_name || message.author?.username || "unknown"),
            bot: Boolean(message.author?.bot)
        },
        content: String(message.content || ""),
        reply_to_message_id: message.message_reference?.message_id ? String(message.message_reference.message_id) : null,
        attachments
    };
}

async function runGapRecovery(): Promise<void> {
    if (isRecoveryRunning) return;
    isRecoveryRunning = true;

    try {
        const W = (window as any).Vencord?.Webpack;
        const RestAPI = W?.Common?.RestAPI;
        if (!RestAPI) {
            await Native.reportRecoveryState("error", 0, "Discord RestAPI unavailable");
            return;
        }

        const wl = await Native.getWatchlistConfig();
        if (!wl || !wl.valid || !Array.isArray(wl.channel_ids) || wl.channel_ids.length === 0) {
            await Native.reportRecoveryState("ready", 0, null);
            return;
        }

        const recoveryState = await Native.getRecoveryState();
        const watchedChannels = wl.channel_ids;
        await Native.reportRecoveryState("recovering", watchedChannels.length, null);

        let pendingCount = watchedChannels.length;

        for (const channelId of watchedChannels) {
            recoveringChannels.add(channelId);
            let chSuccess = false;
            try {
                // Invariant: Watched guild channels MUST have trusted non-empty guild ownership.
                // If guild ID cannot be established, fail recovery safely rather than append malformed events.
                const guildId = getGuildIdForChannel(channelId);
                if (!guildId) {
                    throw new Error(`Cannot resolve guild ownership for watched channel ${channelId}`);
                }

                const chState = recoveryState.channels[channelId];
                if (!chState || !chState.checkpoint_message_id) {
                    // First-watch policy: initialize checkpoint from latest message without historical backfill
                    console.log(`[CordBrief] Initializing first-watch checkpoint for channel ${channelId}`);
                    const resp = await RestAPI.get({
                        url: `/channels/${channelId}/messages`,
                        query: { limit: 1 }
                    });
                    let initialCheckpoint = "";
                    if (resp && resp.ok && Array.isArray(resp.body) && resp.body.length > 0) {
                        initialCheckpoint = String(resp.body[0].id);
                    } else {
                        // Empty channel or no messages: use current snowflake from timestamp
                        initialCheckpoint = String((BigInt(Date.now()) - 1420070400000n) << 22n);
                    }
                    await Native.saveChannelCheckpoint(channelId, {
                        checkpoint_message_id: initialCheckpoint,
                        last_recovery_at: new Date().toISOString(),
                        last_result: "success",
                        last_error: null,
                        recovered_count: 0
                    });
                    chSuccess = true;
                } else {
                    // Existing checkpoint: fetch missed history with bounded pagination
                    let currentCheckpoint = chState.checkpoint_message_id;
                    let pageCount = 0;
                    const maxPages = 10;

                    while (pageCount < maxPages) {
                        pageCount++;
                        const resp = await RestAPI.get({
                            url: `/channels/${channelId}/messages`,
                            query: { after: currentCheckpoint, limit: 100 }
                        });

                        if (!resp || !resp.ok) {
                            throw new Error(`Discord REST returned HTTP ${resp?.status || "unknown"}`);
                        }

                        const messages: RawDiscordMessage[] = resp.body;
                        if (!Array.isArray(messages) || messages.length === 0) {
                            break; // Gap fully caught up
                        }

                        // Deterministic sort: oldest -> newest by snowflake ID
                        messages.sort((a, b) => {
                            const diff = BigInt(a.id) - BigInt(b.id);
                            return diff < 0n ? -1 : (diff > 0n ? 1 : 0);
                        });

                        const pageNewestId = String(messages[messages.length - 1].id);
                        const jsonLines = messages.map(m => {
                            const norm = normalizeMessage(m, guildId);
                            if (!norm.guild_id) {
                                throw new Error(`Missing guild_id for message ${m.id} in channel ${channelId}`);
                            }
                            return JSON.stringify(norm);
                        });

                        const res = await Native.appendRecoveredMessages(channelId, jsonLines, pageNewestId);
                        if (!res.success) {
                            throw new Error(res.error || "Failed appending recovered messages");
                        }

                        currentCheckpoint = pageNewestId;
                        if (messages.length < 100) {
                            break;
                        }
                    }
                    chSuccess = true;
                }
            } catch (err: any) {
                console.error(`[CordBrief] Gap recovery failed for channel ${channelId}:`, err);
                const prev = recoveryState.channels[channelId];
                await Native.saveChannelCheckpoint(channelId, {
                    checkpoint_message_id: prev?.checkpoint_message_id || "",
                    last_recovery_at: new Date().toISOString(),
                    last_result: "error",
                    last_error: String(err?.message || err),
                    recovered_count: prev?.recovered_count || 0
                });
                chSuccess = false;
            } finally {
                // Drain any live messages queued while this channel was recovering
                const queue = pendingLiveMessages.get(channelId);
                pendingLiveMessages.delete(channelId);
                if (queue && queue.length > 0) {
                    const curState = await Native.getRecoveryState();
                    let latestCheckpoint = curState.channels[channelId]?.checkpoint_message_id || "0";
                    queue.sort((a, b) => {
                        const diff = BigInt(a.id) - BigInt(b.id);
                        return diff < 0n ? -1 : (diff > 0n ? 1 : 0);
                    });
                    const qGuildId = getGuildIdForChannel(channelId) || "";
                    for (const msg of queue) {
                        if (BigInt(msg.id) <= BigInt(latestCheckpoint)) {
                            continue; // Already accounted for in REST response
                        }
                        const jsonLine = JSON.stringify(normalizeMessage(msg, qGuildId));
                        await Native.appendEventToJournal(jsonLine);
                        // Invariant: Drained live events remain after latestCheckpoint's journal boundary
                        // and do NOT advance the verified recovery checkpoint.
                    }
                }
                recoveringChannels.delete(channelId);
                if (chSuccess) {
                    reconciledChannels.add(channelId);
                } else {
                    reconciledChannels.delete(channelId);
                }
                pendingCount--;
                await Native.reportRecoveryState("recovering", pendingCount, null);
            }
        }

        await Native.reportRecoveryState("ready", 0, null);
    } catch (err: any) {
        console.error("[CordBrief] Gap recovery encountered unexpected error:", err);
        await Native.reportRecoveryState("error", 0, String(err?.message || err));
    } finally {
        isRecoveryRunning = false;
    }
}

export default definePlugin({
    name: "CordBriefCollector",
    description: "Captures in-process Discord Gateway events for allowlisted channels and persists them to segmented NDJSON via native IPC.",
    authors: [Devs.Vendicated],
    required: true,
    startAt: "DOMContentLoaded",

    flux: {
        async MESSAGE_CREATE({ message }: { message: RawDiscordMessage }) {
            if (!message || !message.channel_id) return;

            // Fail-closed watchlist check
            let wl: NativeTypes.WatchlistConfig;
            try {
                wl = await Native.getWatchlistConfig();
            } catch {
                return; // Fail closed
            }

            if (!wl || !wl.valid || !Array.isArray(wl.channel_ids) || wl.channel_ids.length === 0) {
                return; // Fail closed: zero persistence if watchlist is missing, malformed, or empty
            }

            // Strict allowlist: channel must be explicitly present in watched list
            if (!wl.channel_ids.includes(message.channel_id)) {
                return;
            }

            // Invariant: Live messages MUST NOT advance checkpoint across an unreconciled range.
            // If the channel has not completed startup reconciliation, or is actively recovering, buffer it.
            if (!reconciledChannels.has(message.channel_id) || recoveringChannels.has(message.channel_id)) {
                let queue = pendingLiveMessages.get(message.channel_id);
                if (!queue) {
                    queue = [];
                    pendingLiveMessages.set(message.channel_id, queue);
                }
                queue.push(message);
                return;
            }

            // Channel is fully reconciled and up to date
            let liveGuildId = message.guild_id ? String(message.guild_id) : "";
            if (!liveGuildId) {
                liveGuildId = getGuildIdForChannel(message.channel_id) || "";
            }
            const record = normalizeMessage(message, liveGuildId);
            const jsonLine = JSON.stringify(record);
            await Native.appendEventToJournal(jsonLine);

            // CRITICAL INVARIANT: Ordinary live MESSAGE_CREATE must NOT advance verified recovery checkpoint!
            // Only successful REST reconciliation may advance checkpoint_message_id.
        }
    },

    start() {
        console.log("[CordBrief] Production Collector plugin initialized.");

        // Mark all currently watched channels as needing startup reconciliation before live events can advance checkpoints
        Native.getWatchlistConfig().then(wl => {
            if (wl && wl.valid && Array.isArray(wl.channel_ids)) {
                for (const id of wl.channel_ids) {
                    recoveringChannels.add(id);
                }
            }
        }).catch(() => {});

        const extractAndPublishCatalog = async () => {
            try {
                const W = (window as any).Vencord?.Webpack;
                if (!W) return;
                const GuildStore = W.findStore("GuildStore");
                const ChannelStore = W.findStore("ChannelStore");
                if (!GuildStore || !ChannelStore || !GuildStore.getGuilds) return;

                const guildsObj = GuildStore.getGuilds();
                if (!guildsObj) return;

                const guildsList = Object.values(guildsObj) as any[];
                const catalogGuilds: NativeTypes.CatalogGuild[] = [];

                for (const g of guildsList) {
                    if (!g || !g.id || !g.name) continue;
                    let channels: NativeTypes.CatalogChannel[] = [];
                    if (ChannelStore.getMutableGuildChannelsForGuild) {
                        const map = ChannelStore.getMutableGuildChannelsForGuild(g.id);
                        if (map) {
                            channels = (Object.values(map) as any[])
                                .filter(c => c && (c.type === 0 || c.type === 5)) // text or announcement only
                                .map(c => ({
                                    id: String(c.id),
                                    name: String(c.name || "unnamed"),
                                    type: Number(c.type) || 0
                                }));
                        }
                    }
                    catalogGuilds.push({
                        id: String(g.id),
                        name: String(g.name),
                        channels
                    });
                }

                if (catalogGuilds.length > 0) {
                    await Native.publishCatalog(catalogGuilds);
                }
            } catch (err) {
                console.error("[CordBrief] Catalog extraction error:", err);
            }
        };

        const checkAndReportAuth = () => {
            try {
                if (typeof window !== "undefined" && window.location) {
                    const path = window.location.pathname || "";
                    const hasAuthToken = typeof localStorage !== "undefined" && !!localStorage.getItem("token");
                    if (path.startsWith("/channels") || (path.startsWith("/app") && hasAuthToken)) {
                        Native.reportAuthState(true);
                        extractAndPublishCatalog();
                        // Trigger startup gap recovery
                        runGapRecovery();
                    } else if (path.startsWith("/login") || path.startsWith("/register")) {
                        Native.reportAuthState(false);
                    } else {
                        Native.reportAuthState(null);
                    }
                }
            } catch {
                // Ignore during early startup
            }
        };

        checkAndReportAuth();
        const authInterval = setInterval(checkAndReportAuth, 5000);
        const catalogInterval = setInterval(extractAndPublishCatalog, 30000);
        const recoveryInterval = setInterval(runGapRecovery, 60000);

        (this as any)._authInterval = authInterval;
        (this as any)._catalogInterval = catalogInterval;
        (this as any)._recoveryInterval = recoveryInterval;
    },

    stop() {
        console.log("[CordBrief] Production Collector plugin stopped.");
        if ((this as any)._authInterval) {
            clearInterval((this as any)._authInterval);
        }
        if ((this as any)._catalogInterval) {
            clearInterval((this as any)._catalogInterval);
        }
        if ((this as any)._recoveryInterval) {
            clearInterval((this as any)._recoveryInterval);
        }
    }
});
