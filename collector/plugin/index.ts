/*
 * CordBrief Collector Plugin - Renderer Process Component
 * Hooks Discord Gateway MESSAGE_CREATE events, enforces fail-closed channel filtering,
 * normalizes events to Schema v1, and delegates disk I/O to native bridge.
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

export default definePlugin({
    name: "CordBriefCollector",
    description: "Captures in-process Discord Gateway events for allowlisted channels and persists them to segmented NDJSON via native IPC.",
    authors: [Devs.Vendicated],
    required: true,

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

            // Normalize event to Schema v1
            const attachments: NormalizedAttachment[] = (message.attachments || []).map(a => ({
                id: String(a.id),
                filename: String(a.filename || "unnamed"),
                content_type: String(a.content_type || "application/octet-stream"),
                size: Number(a.size) || 0
            }));

            const record: CordBriefEventV1 = {
                version: 1,
                event: "message_create",
                message_id: String(message.id),
                guild_id: String(message.guild_id || ""),
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

            const jsonLine = JSON.stringify(record);
            await Native.appendEventToJournal(jsonLine);
        }
    },

    start() {
        console.log("[CordBrief] Production Collector plugin initialized.");
        const checkAndReportAuth = () => {
            try {
                if (typeof window !== "undefined" && window.location) {
                    const path = window.location.pathname || "";
                    if (path.startsWith("/channels")) {
                        Native.reportAuthState(true);
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
        (this as any)._authInterval = authInterval;
    },

    stop() {
        console.log("[CordBrief] Production Collector plugin stopped.");
        if ((this as any)._authInterval) {
            clearInterval((this as any)._authInterval);
        }
    }
});
