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
    recovery_state?: "idle" | "recovering" | "ready" | "error";
    recovery_last_at?: string | null;
    recovery_pending_channels?: number;
    recovery_last_error?: string | null;
}

export interface JournalStartBoundary {
    segment: number;
    offset: number;
}

export interface ChannelRecoveryCheckpoint {
    checkpoint_message_id: string;
    checkpoint_journal_boundary?: JournalStartBoundary;
    last_recovery_at: string;
    last_result: "success" | "error";
    last_error: string | null;
    recovered_count: number;
}

export interface PendingRecoveryPage {
    channel_id: string;
    old_checkpoint_message_id: string;
    new_checkpoint_message_id: string;
    message_ids: string[];
    journal_start: JournalStartBoundary;
}

export interface RecoveryStateRecord {
    version: 1;
    channels: Record<string, ChannelRecoveryCheckpoint>;
    pending?: PendingRecoveryPage | null;
}

export interface AppendRecoveredResult {
    success: boolean;
    appendedCount: number;
    skippedDuplicates: number;
    error?: string;
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

const COLLECTOR_DATA_DIR = process.env.CORDBRIEF_COLLECTOR_DATA_DIR || "/var/lib/cordbrief";
const RECOVERY_STATE_PATH = process.env.CORDBRIEF_RECOVERY_STATE_PATH || path.join(COLLECTOR_DATA_DIR, "recovery-state.json");
const LEGACY_RECOVERY_STATE_PATH = path.join(
    process.env.DISCORD_CONFIG_DIR || path.join(process.env.HOME || "/home/cordbrief", ".config", "discord"),
    "cordbrief-recovery-state.json"
);
const MAX_DEDUPE_ENTRIES = 10000;

let recoveryStateStatus: "idle" | "recovering" | "ready" | "error" = "idle";
let recoveryLastAt: string | null = null;
let recoveryPendingChannels = 0;
let recoveryLastError: string | null = null;

// In-memory deduplication ledger seeded from recent journal segments
const recentJournalMessageIds = new Set<string>();

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
    initDedupeLedger();
}

// Scans the active segment (and previous segment if present) to populate the in-memory deduplication ledger
export function initDedupeLedger(): void {
    recentJournalMessageIds.clear();
    try {
        if (!fs.existsSync(EVENTS_DIR)) return;
        const files = fs.readdirSync(EVENTS_DIR)
            .filter(f => /^\d{16}\.ndjson$/.test(f))
            .sort();
        if (files.length === 0) return;

        // Scan the last 2 segments to seed the deduplication set
        const toScan = files.slice(-2);
        for (const file of toScan) {
            const filePath = path.join(EVENTS_DIR, file);
            if (!fs.existsSync(filePath)) continue;
            const content = fs.readFileSync(filePath, "utf8");
            const lines = content.split("\n");
            for (const line of lines) {
                if (!line.trim()) continue;
                const match = line.match(/"message_id"\s*:\s*"([^"]+)"/);
                if (match) {
                    recentJournalMessageIds.add(match[1]);
                    if (recentJournalMessageIds.size > MAX_DEDUPE_ENTRIES) {
                        const oldest = recentJournalMessageIds.values().next().value;
                        if (oldest) recentJournalMessageIds.delete(oldest);
                    }
                }
            }
        }
    } catch (err) {
        console.error("[CordBrief] Error initializing dedupe ledger:", err);
    }
}

export function loadRecoveryState(): RecoveryStateRecord {
    try {
        if (!fs.existsSync(RECOVERY_STATE_PATH)) {
            // Migrate from legacy path if present
            if (fs.existsSync(LEGACY_RECOVERY_STATE_PATH)) {
                try {
                    const legacyRaw = fs.readFileSync(LEGACY_RECOVERY_STATE_PATH, "utf8");
                    const legacyParsed = JSON.parse(legacyRaw);
                    if (legacyParsed && legacyParsed.version === 1 && typeof legacyParsed.channels === "object") {
                        saveRecoveryState(legacyParsed);
                        return legacyParsed;
                    }
                } catch {
                    // Ignore legacy parse failure
                }
            }
            return { version: 1, channels: {}, pending: null };
        }
        const raw = fs.readFileSync(RECOVERY_STATE_PATH, "utf8");
        const parsed = JSON.parse(raw);
        if (
            typeof parsed !== "object" || parsed === null ||
            parsed.version !== 1 ||
            typeof parsed.channels !== "object" || parsed.channels === null
        ) {
            return { version: 1, channels: {}, pending: null };
        }
        let modified = false;
        for (const chId of Object.keys(parsed.channels)) {
            const ch = parsed.channels[chId];
            if (ch && !ch.checkpoint_journal_boundary) {
                ch.checkpoint_journal_boundary = getCurrentJournalBoundary();
                modified = true;
            }
        }
        if (modified) {
            saveRecoveryState(parsed as RecoveryStateRecord);
        }
        return parsed as RecoveryStateRecord;
    } catch {
        return { version: 1, channels: {}, pending: null };
    }
}

export function saveRecoveryState(state: RecoveryStateRecord): void {
    const parentDir = path.dirname(RECOVERY_STATE_PATH);
    if (!fs.existsSync(parentDir)) {
        fs.mkdirSync(parentDir, { recursive: true, mode: 0o700 });
    }
    safeReplaceJSON(RECOVERY_STATE_PATH, state, 0o600);
}

// Scans the journal from the specified segment and byte offset forward across any rotated segments,
// returning an array of extracted message IDs in exact physical append order.
export function getAppendedMessageIdsSince(startSegment: number, startOffset: number): string[] {
    const ids: string[] = [];
    if (!fs.existsSync(EVENTS_DIR)) return ids;
    const files = fs.readdirSync(EVENTS_DIR)
        .filter(f => /^\d{16}\.ndjson$/.test(f))
        .sort();

    for (const file of files) {
        const segNum = parseInt(file.replace(".ndjson", ""), 10);
        if (isNaN(segNum) || segNum < startSegment) continue;

        const filePath = path.join(EVENTS_DIR, file);
        if (!fs.existsSync(filePath)) continue;

        const stat = fs.statSync(filePath);
        const offset = segNum === startSegment ? Math.min(startOffset, stat.size) : 0;
        if (offset >= stat.size) continue;

        const buffer = Buffer.alloc(stat.size - offset);
        const fd = fs.openSync(filePath, "r");
        try {
            fs.readSync(fd, buffer, 0, stat.size - offset, offset);
        } finally {
            fs.closeSync(fd);
        }

        const text = buffer.toString("utf8");
        const lines = text.split("\n");
        for (const line of lines) {
            if (!line.trim()) continue;
            const match = line.match(/"message_id"\s*:\s*"([^"]+)"/);
            if (match) {
                ids.push(match[1]);
            }
        }
    }
    return ids;
}

// Appends raw NDJSON lines to the active journal segment with durable fsync and automatic segment rotation
export function appendRawLinesToJournal(lines: string[], ids: string[]): void {
    if (lines.length === 0) return;
    if (!currentSegmentPath) initSegment();

    for (let i = 0; i < lines.length; i++) {
        const line = lines[i].endsWith("\n") ? lines[i] : lines[i] + "\n";
        const buf = Buffer.from(line, "utf8");

        if (currentSegmentSize > 0 && (currentSegmentSize + buf.length) > MAX_SEGMENT_SIZE) {
            currentSegmentNumber++;
            currentSegmentPath = path.join(EVENTS_DIR, padSegmentNumber(currentSegmentNumber));
            currentSegmentSize = 0;
        }

        const fd = fs.openSync(currentSegmentPath, "a");
        try {
            fs.writeSync(fd, buf);
            fs.fsyncSync(fd);
        } finally {
            fs.closeSync(fd);
        }

        currentSegmentSize += buf.length;
        lastEventTime = new Date().toISOString();

        if (ids[i]) {
            recentJournalMessageIds.add(ids[i]);
            if (recentJournalMessageIds.size > MAX_DEDUPE_ENTRIES) {
                const oldest = recentJournalMessageIds.values().next().value;
                if (oldest) recentJournalMessageIds.delete(oldest);
            }
        }
    }
}

// Helper to get the current physical journal boundary
export function getCurrentJournalBoundary(): JournalStartBoundary {
    if (!currentSegmentPath) initSegment();
    return {
        segment: currentSegmentNumber,
        offset: currentSegmentSize
    };
}

// Reconciles any uncommitted pending recovery page found in recovery-state.json on startup or before new append
export function reconcilePendingRecovery(): { reconciled: boolean; caseResult: string; appendedCount: number } {
    try {
        const state = loadRecoveryState();
        if (!state.pending) {
            return { reconciled: false, caseResult: "none", appendedCount: 0 };
        }

        const pending = state.pending;
        const pendingIds = pending.message_ids || [];

        if (pendingIds.length === 0) {
            state.pending = null;
            saveRecoveryState(state);
            return { reconciled: true, caseResult: "empty_pending", appendedCount: 0 };
        }

        // Deterministically inspect what actually reached the journal since journal_start
        const alreadyAppendedIds = getAppendedMessageIdsSince(
            pending.journal_start.segment,
            pending.journal_start.offset
        );

        const alreadySet = new Set(alreadyAppendedIds);
        const allPresent = pendingIds.length > 0 && pendingIds.every(id => alreadySet.has(id));

        if (allPresent) {
            // Case A: ALL of pending page was appended before crash
            const newBoundary = getCurrentJournalBoundary();
            const existing = state.channels[pending.channel_id];
            state.channels[pending.channel_id] = {
                checkpoint_message_id: pending.new_checkpoint_message_id,
                checkpoint_journal_boundary: newBoundary,
                last_recovery_at: new Date().toISOString(),
                last_result: "success",
                last_error: null,
                recovered_count: existing?.recovered_count || 0
            };
            state.pending = null;
            saveRecoveryState(state);
            console.log(`[CordBriefNative] Reconciled pending recovery: case_a_all_present, 0 appended.`);
            return { reconciled: true, caseResult: "case_a_all_present", appendedCount: 0 };
        } else {
            // Case B/C: Prefix or none present.
            // Raw message content is not stored in recovery-state.json to respect data boundary.
            // Pending intent is left in place for Discord REST refetch during gap recovery.
            console.log(`[CordBriefNative] Pending recovery page for channel ${pending.channel_id} requires REST refetch (prefix/none present).`);
            return { reconciled: false, caseResult: "refetch_required", appendedCount: 0 };
        }
    } catch (err: any) {
        console.error("[CordBriefNative] Error reconciling pending recovery:", err);
        return { reconciled: false, caseResult: "error", appendedCount: 0 };
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
export function safeReplaceJSON(destinationPath: string, data: any, mode: number = 0o644): void {
    const dir = path.dirname(destinationPath);
    if (!fs.existsSync(dir)) {
        fs.mkdirSync(dir, { recursive: true, mode: 0o755 });
    }
    const tmpPath = path.join(dir, `.${path.basename(destinationPath)}.${Date.now()}.${Math.random().toString(36).slice(2)}.tmp`);
    const payload = JSON.stringify(data, null, 2) + "\n";

    const fd = fs.openSync(tmpPath, "w", mode);
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
            last_error: lastErrorMsg,
            recovery_state: recoveryStateStatus,
            recovery_last_at: recoveryLastAt,
            recovery_pending_channels: recoveryPendingChannels,
            recovery_last_error: recoveryLastError
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

        // Live deduplication check against recent journal events
        const idMatch = eventJson.match(/"message_id"\s*:\s*"([^"]+)"/);
        if (idMatch && recentJournalMessageIds.has(idMatch[1])) {
            return true; // Already recorded; drop duplicate silently
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

        if (idMatch) {
            recentJournalMessageIds.add(idMatch[1]);
            if (recentJournalMessageIds.size > MAX_DEDUPE_ENTRIES) {
                const oldest = recentJournalMessageIds.values().next().value;
                if (oldest) recentJournalMessageIds.delete(oldest);
            }
        }

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

export async function getRecoveryState(_?: IpcMainInvokeEvent): Promise<RecoveryStateRecord> {
    return loadRecoveryState();
}

export async function saveChannelCheckpoint(
    _: IpcMainInvokeEvent | undefined,
    channelId: string,
    checkpoint: ChannelRecoveryCheckpoint
): Promise<boolean> {
    try {
        if (!channelId || typeof channelId !== "string" || !checkpoint) {
            return false;
        }
        const state = loadRecoveryState();
        const existing = state.channels[channelId];
        // Ensure snowflake never regresses
        if (existing && existing.checkpoint_message_id && checkpoint.checkpoint_message_id) {
            try {
                if (BigInt(checkpoint.checkpoint_message_id) < BigInt(existing.checkpoint_message_id)) {
                    checkpoint.checkpoint_message_id = existing.checkpoint_message_id;
                }
            } catch {
                // Non-fatal comparison fallback
            }
        }
        if (!checkpoint.checkpoint_journal_boundary) {
            checkpoint.checkpoint_journal_boundary = existing?.checkpoint_journal_boundary || getCurrentJournalBoundary();
        }
        state.channels[channelId] = checkpoint;
        saveRecoveryState(state);
        return true;
    } catch (err: any) {
        console.error("[CordBrief] Failed saving channel checkpoint:", err);
        return false;
    }
}

// Mutex for global serialization of recovery page commit transactions
let pageCommitMutex: Promise<any> = Promise.resolve();

export async function appendRecoveredMessages(
    _: IpcMainInvokeEvent | undefined,
    channelId: string,
    messagesJson: string[],
    newCheckpoint: string
): Promise<AppendRecoveredResult> {
    const acquireLock = pageCommitMutex;
    let releaseLock: () => void;
    pageCommitMutex = new Promise(resolve => { releaseLock = resolve; });
    await acquireLock.catch(() => {});
    try {
        return await doAppendRecoveredMessages(channelId, messagesJson, newCheckpoint);
    } finally {
        releaseLock!();
    }
}

async function doAppendRecoveredMessages(
    channelId: string,
    messagesJson: string[],
    newCheckpoint: string
): Promise<AppendRecoveredResult> {
    try {
        if (!channelId || typeof channelId !== "string") {
            return { success: false, appendedCount: 0, skippedDuplicates: 0, error: "Invalid channel ID" };
        }

        if (!currentSegmentPath) {
            initSegment();
        }

        // Reconcile any existing pending recovery transaction first
        let state = loadRecoveryState();
        if (state.pending) {
            reconcilePendingRecovery();
            state = loadRecoveryState();
        }

        if (!messagesJson || messagesJson.length === 0) {
            if (newCheckpoint) {
                const curState = loadRecoveryState();
                const prev = curState.channels[channelId];
                curState.channels[channelId] = {
                    checkpoint_message_id: newCheckpoint,
                    checkpoint_journal_boundary: prev?.checkpoint_journal_boundary || getCurrentJournalBoundary(),
                    last_recovery_at: new Date().toISOString(),
                    last_result: "success",
                    last_error: null,
                    recovered_count: prev?.recovered_count || 0
                };
                saveRecoveryState(curState);
            }
            return { success: true, appendedCount: 0, skippedDuplicates: 0 };
        }

        // Extract message IDs (oldest -> newest)
        const messageIds: string[] = [];
        for (const raw of messagesJson) {
            const match = raw.match(/"message_id"\s*:\s*"([^"]+)"/);
            if (match) {
                messageIds.push(match[1]);
            } else {
                try {
                    const parsed = JSON.parse(raw);
                    messageIds.push(String(parsed.message_id || ""));
                } catch {
                    messageIds.push("");
                }
            }
        }

        const journalStart = getCurrentJournalBoundary();
        const oldState = state.channels[channelId];
        const oldCheckpoint = oldState?.checkpoint_message_id || "";
        const oldBoundary = oldState?.checkpoint_journal_boundary || journalStart;

        // Scan from the earliest boundary between old verified checkpoint and current journal start
        let scanSeg = oldBoundary.segment;
        let scanOff = oldBoundary.offset;
        if (journalStart.segment < scanSeg || (journalStart.segment === scanSeg && journalStart.offset < scanOff)) {
            scanSeg = journalStart.segment;
            scanOff = journalStart.offset;
        }

        // 4. Persist pending recovery intent BEFORE journal append (NO raw_messages stored)
        const pendingIntent: PendingRecoveryPage = {
            channel_id: channelId,
            old_checkpoint_message_id: oldCheckpoint,
            new_checkpoint_message_id: newCheckpoint,
            message_ids: messageIds,
            journal_start: journalStart
        };

        state.pending = pendingIntent;
        try {
            saveRecoveryState(state);
        } catch (err: any) {
            // DO NOT append anything to journal if pending state cannot be persisted!
            lastErrorMsg = `Failed to persist pending recovery intent: ${err?.message || err}`;
            writeStatus("error", true);
            return { success: false, appendedCount: 0, skippedDuplicates: 0, error: lastErrorMsg };
        }

        // 5. Append missing page records using physical journal boundary inspection
        const alreadyAppended = new Set(getAppendedMessageIdsSince(scanSeg, scanOff));
        const linesToAppend: string[] = [];
        const idsToAppend: string[] = [];
        let skippedDuplicates = 0;

        for (let i = 0; i < messagesJson.length; i++) {
            const id = messageIds[i];
            if (!id) continue;
            if (alreadyAppended.has(id)) {
                skippedDuplicates++;
                continue;
            }
            // Optimization / fast-check: recentJournalMessageIds
            if (recentJournalMessageIds.has(id)) {
                skippedDuplicates++;
                continue;
            }
            linesToAppend.push(messagesJson[i]);
            idsToAppend.push(id);
        }

        if (linesToAppend.length > 0) {
            appendRawLinesToJournal(linesToAppend, idsToAppend);
        }

        const appendedCount = idsToAppend.length;

        // Capture the exact physical journal boundary corresponding to completion through high-water mark H
        const newCheckpointBoundary = getCurrentJournalBoundary();

        // 7. Persist committed recovery checkpoint and 8. Clear pending intent
        const commitState = loadRecoveryState();
        const prev = commitState.channels[channelId];
        commitState.channels[channelId] = {
            checkpoint_message_id: newCheckpoint,
            checkpoint_journal_boundary: newCheckpointBoundary,
            last_recovery_at: new Date().toISOString(),
            last_result: "success",
            last_error: null,
            recovered_count: (prev?.recovered_count || 0) + appendedCount
        };
        commitState.pending = null;

        try {
            saveRecoveryState(commitState);
        } catch (err: any) {
            // State persistence failed after journal append.
            // Pending intent remains in file; restart reconciliation will repair it!
            lastErrorMsg = `Failed to persist committed recovery checkpoint: ${err?.message || err}`;
            writeStatus("error", true);
            return { success: false, appendedCount, skippedDuplicates, error: lastErrorMsg };
        }

        writeStatus("running", true);
        return { success: true, appendedCount, skippedDuplicates };
    } catch (err: any) {
        lastErrorMsg = `appendRecoveredMessages failed: ${err?.message || String(err)}`;
        writeStatus("error", true);
        return { success: false, appendedCount: 0, skippedDuplicates: 0, error: lastErrorMsg };
    }
}

export function clearDedupeLedgerForTesting(): void {
    recentJournalMessageIds.clear();
}

export async function reportRecoveryState(
    _: IpcMainInvokeEvent | undefined,
    state: "idle" | "recovering" | "ready" | "error",
    pendingChannels?: number,
    error?: string | null
): Promise<boolean> {
    recoveryStateStatus = state;
    if (typeof pendingChannels === "number") {
        recoveryPendingChannels = pendingChannels;
    }
    if (error !== undefined) {
        recoveryLastError = error;
    }
    if (state === "ready" || state === "idle") {
        recoveryLastAt = new Date().toISOString();
        recoveryPendingChannels = 0;
    }
    writeStatus();
    return true;
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
    reconcilePendingRecovery();
    writeStatus("starting", true);
    startWatchlistMonitor();
} catch {
    // Tolerated during initial import before dirs exist
}
