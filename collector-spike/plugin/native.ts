/*
 * CordBrief Collector Plugin - Native Process Bridge
 * Runs in Electron main process with full Node.js filesystem access.
 */

import { IpcMainInvokeEvent } from "electron";
import * as fs from "fs";
import * as path from "path";

export async function appendEventToJournal(_: IpcMainInvokeEvent, journalPath: string, line: string): Promise<void> {
    try {
        const dir = path.dirname(journalPath);
        if (!fs.existsSync(dir)) {
            fs.mkdirSync(dir, { recursive: true });
        }
        fs.appendFileSync(journalPath, line, { encoding: "utf8", flag: "a" });
    } catch (err) {
        console.error("[CordBriefNative] Failed to append event to journal:", err);
    }
}

export async function getWatchedChannelsConfig(_: IpcMainInvokeEvent): Promise<string> {
    return process.env.CORDBRIEF_WATCHED_CHANNELS || "";
}

export async function getJournalPathConfig(_: IpcMainInvokeEvent): Promise<string> {
    return process.env.CORDBRIEF_JOURNAL_PATH || "/var/cordbrief/journal/discord_events.ndjson";
}
