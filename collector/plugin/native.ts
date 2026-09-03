/*
 * CordBrief Collector Plugin - Native Process Bridge
 * Runs in Electron main process with full Node.js filesystem access.
 * Manages exchange directory, segmented journal rotation, crash recovery, and fail-closed watchlist loading.
 */

import { IpcMainInvokeEvent } from "electron";
import * as fs from "fs";
import * as path from "path";

export interface WatchlistConfig {
    valid: boolean;
    generation: number;
    channel_ids: string[];
}

export interface CollectorStatusRecord {
    version: number;
    updated_at: string;
    collector_state: "starting" | "running" | "error";
    discord_authenticated: boolean | null;
    catalog_state: "unavailable" | "ready" | "error";
    catalog_updated_at: string | null;
    watched_generation: number;
    watched_channel_count: number;
    active_segment: number;
    last_event_at: string | null;
    last_error: string | null;
}

export interface CatalogChannel {
    id: string;
    name: string;
    type: number;
}

export interface CatalogGuild {
    id: string;
    name: string;
    channels: CatalogChannel[];
}

export interface CatalogRecord {
    version: 1;
    updated_at: string;
    guilds: CatalogGuild[];
}

const EXCHANGE_DIR = process.env.CORDBRIEF_EXCHANGE_DIR || "/var/cordbrief/exchange";
const EVENTS_DIR = path.join(EXCHANGE_DIR, "events");
// Configurable segment rotation limit; default 32 MiB (33,554,432 bytes)
const MAX_SEGMENT_SIZE = parseInt(process.env.CORDBRIEF_MAX_SEGMENT_SIZE || "33554432", 10);

let currentSegmentNumber = 1;
let currentSegmentPath = "";
let currentSegmentSize = 0;
let lastEventTime: string | null = null;
let lastErrorMsg: string | null = null;
let discordAuthenticatedState: boolean | null = null;
let catalogState: "unavailable" | "ready" | "error" = "unavailable";
let catalogUpdatedAt: string | null = null;
let lastCatalogContentHash = "";

// In-memory cached watchlist
let cachedWatchlist: WatchlistConfig = { valid: false, generation: -1, channel_ids: [] };
let lastWatchlistMtime = 0;

function padSegmentNumber(num: number): string {
    return String(num).padStart(16, "0") + ".ndjson";
}

function ensureDirectories(): void {
    if (!fs.existsSync(EXCHANGE_DIR)) {
        fs.mkdirSync(EXCHANGE_DIR, { recursive: true, mode: 0o755 });
    }
    if (!fs.existsSync(EVENTS_DIR)) {
        fs.mkdirSync(EVENTS_DIR, { recursive: true, mode: 0o755 });
    }
}

// Discover the highest existing segment or initialize segment 1, repairing any torn trailing line
export function initSegment(): void {
    ensureDirectories();
    const files = fs.readdirSync(EVENTS_DIR)
        .filter(f => /^\d{16}\.ndjson$/.test(f))
        .sort();

    if (files.length === 0) {
        currentSegmentNumber = 1;
    } else {
        const last = files[files.length - 1];
        const num = parseInt(last.replace(".ndjson", ""), 10);
        currentSegmentNumber = isNaN(num) || num < 1 ? 1 : num;
    }

    currentSegmentPath = path.join(EVENTS_DIR, padSegmentNumber(currentSegmentNumber));
    if (fs.existsSync(currentSegmentPath)) {
        const stat = fs.statSync(currentSegmentPath);
        currentSegmentSize = stat.size;

        // Crash recovery: verify file ends with newline. If torn trailing bytes exist, truncate to last clean boundary.
        if (currentSegmentSize > 0) {
            repairTornTrailingRecord(currentSegmentPath);
        }

        // Rotate if existing segment already reached or exceeded the threshold
        if (currentSegmentSize >= MAX_SEGMENT_SIZE) {
            currentSegmentNumber++;
            currentSegmentPath = path.join(EVENTS_DIR, padSegmentNumber(currentSegmentNumber));
            currentSegmentSize = 0;
        }
    } else {
        currentSegmentSize = 0;
    }
}

// Inspects the end of an existing segment. If the file ends with an incomplete record (no \n),
// truncates the file back to the last clean newline boundary to prevent record merging on append.
function repairTornTrailingRecord(filePath: string): void {
    try {
        const fd = fs.openSync(filePath, "r+");
        try {
            const stat = fs.fstatSync(fd);
            if (stat.size === 0) {
                currentSegmentSize = 0;
                return;
            }

            // Read up to last 64 KiB to find the last newline
            const checkSize = Math.min(stat.size, 65536);
            const buffer = Buffer.alloc(checkSize);
            fs.readSync(fd, buffer, 0, checkSize, stat.size - checkSize);

            if (buffer[checkSize - 1] === 0x0A) {
                // File ends with clean newline; no repair needed
                currentSegmentSize = stat.size;
                return;
            }

            // Find position of the last newline byte
            let lastNewlineOffsetInBuf = -1;
            for (let i = checkSize - 2; i >= 0; i--) {
                if (buffer[i] === 0x0A) {
                    lastNewlineOffsetInBuf = i;
                    break;
                }
            }

            const cleanByteLength = lastNewlineOffsetInBuf >= 0
                ? (stat.size - checkSize) + lastNewlineOffsetInBuf + 1
                : 0; // If no newline in the entire file, truncate to 0

            console.warn(`[CordBriefNative] Detected torn trailing record in ${filePath}. Truncating from ${stat.size} to ${cleanByteLength} bytes.`);
            fs.ftruncateSync(fd, cleanByteLength);
            currentSegmentSize = cleanByteLength;
        } finally {
            fs.closeSync(fd);
        }
    } catch (err) {
        console.error(`[CordBriefNative] Failed to verify/repair segment trailing record:`, err);
    }
}

// Crash-resistant safe replacement for JSON control/status files:
// writes to a temp file in the same directory, syncs to disk, closes, and renames over destination.
export function safeReplaceJSON(destinationPath: string, data: any): void {
    const dir = path.dirname(destinationPath);
    if (!fs.existsSync(dir)) {
        fs.mkdirSync(dir, { recursive: true, mode: 0o755 });
    }
    const tmpPath = path.join(dir, `.${path.basename(destinationPath)}.${Date.now()}.${Math.random().toString(36).slice(2)}.tmp`);
    const payload = JSON.stringify(data, null, 2) + "\n";

    const fd = fs.openSync(tmpPath, "w", 0o644);
    try {
        fs.writeSync(fd, payload);
        fs.fsyncSync(fd);
    } finally {
        fs.closeSync(fd);
    }
    fs.renameSync(tmpPath, destinationPath);
}

// Load and validate watchlist.json with strict fail-closed semantics
export function loadWatchlist(): WatchlistConfig {
    const watchlistFile = path.join(EXCHANGE_DIR, "watchlist.json");
    try {
        if (!fs.existsSync(watchlistFile)) {
            cachedWatchlist = { valid: false, generation: -1, channel_ids: [] };
            return cachedWatchlist;
        }

        const stat = fs.statSync(watchlistFile);
        if (cachedWatchlist.valid && stat.mtimeMs === lastWatchlistMtime) {
            return cachedWatchlist;
        }

        const raw = fs.readFileSync(watchlistFile, "utf8");
        const parsed = JSON.parse(raw);

        // Strict schema validation (Version 1)
        if (
            typeof parsed !== "object" || parsed === null ||
            parsed.version !== 1 ||
            typeof parsed.generation !== "number" || parsed.generation < 0 ||
            !Array.isArray(parsed.channel_ids)
        ) {
            // Malformed or invalid schema -> fail closed immediately
            cachedWatchlist = { valid: false, generation: -1, channel_ids: [] };
            lastWatchlistMtime = stat.mtimeMs;
            return cachedWatchlist;
        }

        // Validate channel IDs (non-empty strings only)
        const validIds = new Set<string>();
        for (const item of parsed.channel_ids) {
            if (typeof item === "string" && item.trim().length > 0) {
                validIds.add(item.trim());
            } else {
                // Invalid item inside array -> fail closed immediately
                cachedWatchlist = { valid: false, generation: -1, channel_ids: [] };
                lastWatchlistMtime = stat.mtimeMs;
                return cachedWatchlist;
            }
        }

        if (validIds.size === 0) {
            // Empty channel list -> fail closed
            cachedWatchlist = { valid: false, generation: parsed.generation, channel_ids: [] };
            lastWatchlistMtime = stat.mtimeMs;
            return cachedWatchlist;
        }

        lastWatchlistMtime = stat.mtimeMs;
        cachedWatchlist = {
            valid: true,
            generation: parsed.generation,
            channel_ids: Array.from(validIds).sort()
        };
        return cachedWatchlist;
    } catch {
        // Any file read / JSON parse exception -> fail closed immediately
        cachedWatchlist = { valid: false, generation: -1, channel_ids: [] };
        return cachedWatchlist;
    }
}

// Safe update of collector-status.json
export function writeStatus(state: "starting" | "running" | "error" = "running", authenticated?: boolean | null): void {
    try {
        if (typeof authenticated !== "undefined") {
            discordAuthenticatedState = authenticated;
        }
        const wl = loadWatchlist();
        const statusRecord: CollectorStatusRecord = {
            version: 1,
            updated_at: new Date().toISOString(),
            collector_state: state,
            discord_authenticated: discordAuthenticatedState,
            catalog_state: catalogState,
            catalog_updated_at: catalogUpdatedAt,
            watched_generation: wl.generation,
            watched_channel_count: wl.valid ? wl.channel_ids.length : 0,
            active_segment: currentSegmentNumber,
            last_event_at: lastEventTime,
            last_error: lastErrorMsg
        };
        safeReplaceJSON(path.join(EXCHANGE_DIR, "collector-status.json"), statusRecord);
    } catch (err: any) {
        lastErrorMsg = err?.message || String(err);
    }
}

// IPC Handlers callable from renderer userplugin

export async function reportAuthState(_: IpcMainInvokeEvent, auth: boolean | null): Promise<boolean> {
    discordAuthenticatedState = auth;
    writeStatus("running", auth);
    return true;
}

export async function getWatchlistConfig(_?: IpcMainInvokeEvent): Promise<WatchlistConfig> {
    return loadWatchlist();
}

export async function appendEventToJournal(_: IpcMainInvokeEvent, eventJson: string): Promise<boolean> {
    try {
        if (!currentSegmentPath) {
            initSegment();
        }

        // Complete record with trailing newline
        const line = eventJson.endsWith("\n") ? eventJson : eventJson + "\n";
        const lineBuffer = Buffer.from(line, "utf8");

        // Rotate segment if write would exceed limit and file already has content
        if (currentSegmentSize > 0 && (currentSegmentSize + lineBuffer.length) > MAX_SEGMENT_SIZE) {
            currentSegmentNumber++;
            currentSegmentPath = path.join(EVENTS_DIR, padSegmentNumber(currentSegmentNumber));
            currentSegmentSize = 0;
        }

        // Serialized append
        fs.appendFileSync(currentSegmentPath, lineBuffer, { flag: "a" });
        currentSegmentSize += lineBuffer.length;
        lastEventTime = new Date().toISOString();

        writeStatus("running", true);
        return true;
    } catch (err: any) {
        lastErrorMsg = `append failed: ${err?.message || String(err)}`;
        writeStatus("error", true);
        return false;
    }
}

export async function publishCatalog(_: IpcMainInvokeEvent, guilds: CatalogGuild[]): Promise<boolean> {
    try {
        if (!Array.isArray(guilds)) {
            catalogState = "error";
            lastErrorMsg = "publishCatalog received invalid non-array payload";
            writeStatus();
            return false;
        }

        // Validate and clean guilds and channels
        const validatedGuilds: CatalogGuild[] = [];
        for (const g of guilds) {
            if (!g || typeof g.id !== "string" || !g.id || typeof g.name !== "string") continue;
            const validChannels: CatalogChannel[] = [];
            for (const ch of (g.channels || [])) {
                if (!ch || typeof ch.id !== "string" || !ch.id || typeof ch.name !== "string") continue;
                validChannels.push({
                    id: ch.id,
                    name: ch.name,
                    type: typeof ch.type === "number" ? ch.type : 0
                });
            }
            // Deterministic sort: channels by name then ID
            validChannels.sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id));
            validatedGuilds.push({
                id: g.id,
                name: g.name,
                channels: validChannels
            });
        }

        // Deterministic sort: guilds by name then ID
        validatedGuilds.sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id));

        const canonicalPayload = JSON.stringify(validatedGuilds);
        const catalogPath = path.join(EXCHANGE_DIR, "catalog.json");

        // Avoid rewriting catalog.json and status continuously when nothing changed
        if (canonicalPayload === lastCatalogContentHash && fs.existsSync(catalogPath)) {
            catalogState = "ready";
            return true;
        }

        const now = new Date().toISOString();
        const record: CatalogRecord = {
            version: 1,
            updated_at: now,
            guilds: validatedGuilds
        };

        safeReplaceJSON(catalogPath, record);
        lastCatalogContentHash = canonicalPayload;
        catalogState = "ready";
        catalogUpdatedAt = now;
        writeStatus();
        return true;
    } catch (err: any) {
        catalogState = "error";
        lastErrorMsg = `publishCatalog failed: ${err?.message || String(err)}`;
        writeStatus();
        return false;
    }
}

// Background filesystem watcher: dynamically reloads watchlist and updates status when watchlist.json changes
function startWatchlistMonitor(): void {
    const watchlistFile = path.join(EXCHANGE_DIR, "watchlist.json");
    try {
        fs.watchFile(watchlistFile, { interval: 1000 }, (curr, prev) => {
            if (curr.mtimeMs !== prev.mtimeMs) {
                loadWatchlist();
                writeStatus("running", true);
            }
        });
    } catch {
        // Tolerated if directory is created asynchronously
    }
}

// Initialize on load
try {
    initSegment();
    writeStatus("starting", true);
    startWatchlistMonitor();
} catch {
    // Tolerated during initial import before dirs exist
}
