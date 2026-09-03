/*
 * CordBrief Collector Plugin - Renderer Process Component
 * Hooks Discord Gateway MESSAGE_CREATE events and delegates disk I/O to native bridge.
 */

import definePlugin, { PluginNative } from "@utils/types";
import { Devs } from "@utils/constants";

interface RawDiscordMessage {
    id: string;
    channel_id: string;
    guild_id?: string;
    timestamp: string;
    content: string;
    author?: {
        id: string;
        username: string;
        bot?: boolean;
    };
}

interface CordBriefJournalEvent {
    event: "MESSAGE_CREATE";
    message_id: string;
    guild_id: string;
    channel_id: string;
    timestamp: string;
    author_id: string;
    author_name: string;
    content: string;
    captured_at: string;
}

// Access native-side helpers compiled by Vencord
const Native = VencordNative.pluginHelpers.CordBriefCollector as PluginNative<typeof import("./native")>;

let cachedWatched: Set<string> | null = null;
let cachedJournalPath: string | null = null;

async function initConfig(): Promise<{ watched: Set<string>; journalPath: string; }> {
    if (cachedWatched === null) {
        try {
            const raw = await Native.getWatchedChannelsConfig();
            const ids = raw
                .split(",")
                .map(s => s.trim())
                .filter(s => s.length > 0);
            cachedWatched = new Set(ids);
        } catch {
            cachedWatched = new Set();
        }
    }
    if (cachedJournalPath === null) {
        try {
            cachedJournalPath = await Native.getJournalPathConfig();
        } catch {
            cachedJournalPath = "/var/cordbrief/journal/discord_events.ndjson";
        }
    }
    return { watched: cachedWatched, journalPath: cachedJournalPath };
}

export default definePlugin({
    name: "CordBriefCollector",
    description: "Captures in-process Discord Gateway events for allowlisted channels and writes them to a shared NDJSON journal via native IPC.",
    authors: [Devs.Vendicated],
    required: true,

    flux: {
        async MESSAGE_CREATE({ message }: { message: RawDiscordMessage }) {
            if (!message || !message.channel_id) return;

            const { watched, journalPath } = await initConfig();

            // Strict channel allowlist enforcement: unwatched channels are immediately rejected
            if (watched.size > 0 && !watched.has(message.channel_id)) {
                return;
            }

            const record: CordBriefJournalEvent = {
                event: "MESSAGE_CREATE",
                message_id: message.id,
                guild_id: message.guild_id || "",
                channel_id: message.channel_id,
                timestamp: message.timestamp,
                author_id: message.author?.id || "",
                author_name: message.author?.username || "unknown",
                content: message.content || "",
                captured_at: new Date().toISOString()
            };

            const line = JSON.stringify(record) + "\n";
            await Native.appendEventToJournal(journalPath, line);
            console.log(`[CordBrief] Captured message ${record.message_id} in channel ${record.channel_id}`);
        }
    },

    start() {
        console.log("[CordBrief] Collector plugin started.");
        initConfig().then(({ watched, journalPath }) => {
            console.log(`[CordBrief] Watched channels: [${Array.from(watched).join(", ")}], Journal: ${journalPath}`);
        });
    },

    stop() {
        console.log("[CordBrief] Collector plugin stopped.");
    }
});
