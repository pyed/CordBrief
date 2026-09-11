/*
 * CordBrief Collector Plugin - Native Process Bridge
 * Runs in Electron main process with full Node.js filesystem access.
 * Manages exchange directory, segmented journal rotation, crash recovery, and fail-closed watchlist loading.
 */

import { IpcMainInvokeEvent } from "electron";
import * as fs from "fs";
import * as path from "path";
import { createHash } from "crypto";

export interface WatchlistConfig {
    valid: boolean;
    generation: number;
    channel_ids: string[];
}

export interface CollectorStatusRecord {
    version: number;
    updated_at: string;
    mode?: "setup" | "normal" | "reauth" | "error";
    collector_state: "starting" | "running" | "setup_required" | "reauth_required" | "error";
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
    // Immutable exclusion bound. Replay revisits the watched range; K is a high-water,
    // not a claim that Discord will never expose an earlier message later.
    watch_after?: string;
    scan_after?: string | null;
    scan_until?: string | null;
    // All already-journaled channel IDs above checkpoint are at/after this floor.
    checkpoint_journal_boundary?: JournalStartBoundary;
    checkpoint_source?: "rest" | "baseline_rest" | "baseline_pending" | "legacy";
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
    version: 2;
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
const recentJournalMessageIds = new Map<string, string>();
let journalMaxID = 0n;
let journalRecordCount = 0;
let journalValidated = false;
let journalValidationError = "Journal initialization/validation failed";
const journalChannels = new Set<string>();
let deferredTail: { file: string; size: number } | null = null;
let recoveryReady = false;

// In-memory cached watchlist
let cachedWatchlist: WatchlistConfig = { valid: false, generation: -1, channel_ids: [] };
let lastWatchlistMtime = 0;

function padSegmentNumber(num: number): string {
    return String(num).padStart(16, "0") + ".ndjson";
}

function ensureDirectories(): void {
    if (process.env.CORDBRIEF_RETENTION_INSPECT === "1") {
        if (!fs.statSync(EVENTS_DIR).isDirectory()) throw new Error("Missing journal directory");
        return;
    }
    if (!fs.existsSync(EXCHANGE_DIR)) {
        fs.mkdirSync(EXCHANGE_DIR, { recursive: true, mode: 0o755 });
    }
    if (!fs.existsSync(EVENTS_DIR)) {
        fs.mkdirSync(EVENTS_DIR, { recursive: true, mode: 0o755 });
    }
}

// Discover a complete-record snapshot without mutating an unvalidated journal.
export function initSegment(): void {
    ensureDirectories();
    deferredTail = null;
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

        // Build a read-only complete-record snapshot. Invalid recovery metadata
        // must not authorize even active-tail repair.
        if (currentSegmentSize > 0) {
            const bytes = fs.readFileSync(currentSegmentPath);
            const complete = bytes.lastIndexOf(10) + 1;
            if (complete !== bytes.length) {
                deferredTail = { file: currentSegmentPath, size: complete };
                currentSegmentSize = complete;
            }
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

// Scan retained records to establish exact startup identity evidence.
export function initDedupeLedger(): void {
    recentJournalMessageIds.clear();
    journalMaxID = 0n;
    journalRecordCount = 0;
    journalChannels.clear();
    journalValidated = false;
    scanJournal({ segment: 1, offset: 0 }, getCurrentJournalBoundary(), record => {
        journalRecordCount++;
        journalChannels.add(record.channel_id);
        const n = BigInt(record.message_id);
        if (n > journalMaxID) journalMaxID = n;
        recentJournalMessageIds.set(record.message_id, record.channel_id);
        if (recentJournalMessageIds.size > MAX_DEDUPE_ENTRIES) recentJournalMessageIds.delete(recentJournalMessageIds.keys().next().value!);
    });
    journalValidated = true;
}

function isID(value: any, allowEmpty = false): boolean {
    return typeof value === "string" && ((allowEmpty && value === "") || (/^(0|[1-9][0-9]{0,19})$/.test(value) && BigInt(value) <= 18446744073709551615n));
}

function requireKeys(value: any, keys: string[]): void {
    if (!value || typeof value !== "object" || Array.isArray(value) ||
        Object.keys(value).some(key => !keys.includes(key))) throw new Error("Unexpected recovery metadata fields");
}

function validateJournalTopology(): string[] {
    const files = fs.readdirSync(EVENTS_DIR).filter(f => f.endsWith(".ndjson")).sort();
    const retired = readRetiredEvidence();
    const first = files.length ? Number(files[0].slice(0, 16)) : 1;
    if (first > retired.size + 1 || (retired.size && (!files.length || Number(files.at(-1)!.slice(0, 16)) <= retired.size))) {
        throw new Error("Invalid retired journal topology");
    }
    for (let i = 0; i < files.length; i++) {
        if (files[i] !== padSegmentNumber(first + i) || !fs.lstatSync(path.join(EVENTS_DIR, files[i])).isFile()) {
            throw new Error("Invalid or missing journal segment topology");
        }
    }
    return files;
}

interface RetiredIdentity { message_id: string; channel_id: string; offset: number; next_offset: number }
interface RetiredSegment { version: number; segment: number; size: number; sha256: string; records: RetiredIdentity[] }
const sha256 = (bytes: Buffer) => createHash("sha256").update(bytes).digest("hex");
function exactKeys(value: any, keys: string[]): void {
    requireKeys(value, keys);
    if (Object.keys(value).length !== keys.length) throw new Error("Missing retention fields");
}
function readRegular(file: string): Buffer {
    if (!fs.lstatSync(file).isFile()) throw new Error("Non-regular retention evidence");
    return fs.readFileSync(file);
}
function parseRetentionJSON(bytes: Buffer): any {
    const text = bytes.toString("utf8"), value = JSON.parse(text);
    // JSON.parse silently accepts duplicate object keys. Reject them so Core and
    // Collector cannot give different meanings to the same correctness document.
    const stack: ({ keys: Set<string>; key: boolean } | null)[] = [];
    for (const [token] of text.matchAll(/"(?:\\.|[^"\\])*"|[{}\[\],:]/g)) {
        if (token === "{") stack.push({ keys: new Set(), key: true });
        else if (token === "[") stack.push(null);
        else if (token === "}" || token === "]") stack.pop();
        else {
            const object = stack.at(-1);
            if (token === "," && object) object.key = true;
            else if (token.startsWith('"') && object?.key) {
                const key = JSON.parse(token);
                if (object.keys.has(key)) throw new Error("Duplicate retention JSON key");
                object.keys.add(key);
                object.key = false;
            }
        }
    }
    return value;
}

// Immutable evidence replaces identities, never transcripts. No cache: missing or
// corrupted evidence must fail even if a previous request loaded a valid manifest.
function readRetiredEvidence(): Map<number, RetiredSegment> {
    const result = new Map<number, RetiredSegment>();
    const manifestFile = path.join(EXCHANGE_DIR, "retention-manifest.json");
    let manifestBytes: Buffer;
    try { manifestBytes = readRegular(manifestFile); }
    catch (error: any) { if (error.code === "ENOENT") return result; throw error; }
    const manifest = parseRetentionJSON(manifestBytes);
    exactKeys(manifest, ["version", "retired_through", "segments"]);
    if (manifest.version !== 1 || !Number.isSafeInteger(manifest.retired_through) || manifest.retired_through < 1 ||
        !Array.isArray(manifest.segments) || manifest.segments.length !== manifest.retired_through) throw new Error("Invalid retention manifest");
    const directory = path.join(EXCHANGE_DIR, "retention");
    if (!fs.lstatSync(directory).isDirectory()) throw new Error("Invalid retention directory");
    for (let i = 0; i < manifest.segments.length; i++) {
        const entry = manifest.segments[i];
        exactKeys(entry, ["segment", "size", "sha256", "sidecar_sha256"]);
        if (entry.segment !== i + 1 || !Number.isSafeInteger(entry.size) || entry.size < 0 ||
            !/^[a-f0-9]{64}$/.test(entry.sha256) || !/^[a-f0-9]{64}$/.test(entry.sidecar_sha256)) throw new Error("Invalid retention entry");
        const bytes = readRegular(path.join(directory, String(entry.segment).padStart(16, "0") + ".ids.json"));
        if (sha256(bytes) !== entry.sidecar_sha256) throw new Error("Retention sidecar checksum mismatch");
        const sidecar = parseRetentionJSON(bytes);
        exactKeys(sidecar, ["version", "segment", "size", "sha256", "records"]);
        if (sidecar.version !== 1 || sidecar.segment !== entry.segment || sidecar.size !== entry.size ||
            sidecar.sha256 !== entry.sha256 || !Array.isArray(sidecar.records)) throw new Error("Invalid retention sidecar");
        let offset = 0;
        for (const record of sidecar.records) {
            exactKeys(record, ["message_id", "channel_id", "offset", "next_offset"]);
            if (!isID(record.message_id) || record.message_id === "0" || !isID(record.channel_id) || record.channel_id === "0" ||
                record.offset !== offset || !Number.isSafeInteger(record.next_offset) || record.next_offset <= offset || record.next_offset > sidecar.size) {
                throw new Error("Invalid retired identity/position");
            }
            offset = record.next_offset;
        }
        if (offset !== sidecar.size) throw new Error("Incomplete retention sidecar");
        const raw = path.join(EVENTS_DIR, padSegmentNumber(entry.segment));
        if (fs.existsSync(raw)) {
            const original = readRegular(raw);
            if (sha256(original) !== entry.sha256 || original.length !== entry.size) throw new Error("Retired raw file checksum mismatch");
            for (const record of sidecar.records) {
                if (original[record.next_offset - 1] !== 10 || original.indexOf(10, record.offset) !== record.next_offset - 1) throw new Error("Retired position differs from raw record");
                const source = JSON.parse(original.subarray(record.offset, record.next_offset - 1).toString("utf8"));
                if (source.version !== 1 || source.event !== "message_create" || source.message_id !== record.message_id || source.channel_id !== record.channel_id) throw new Error("Retired identity differs from raw record");
            }
        }
        result.set(entry.segment, sidecar);
    }
    return result;
}

function validateBoundary(value: JournalStartBoundary): void {
    requireKeys(value, ["segment", "offset"]);
    if (!value || !Number.isSafeInteger(value.segment) || value.segment < 1 ||
        !Number.isSafeInteger(value.offset) || value.offset < 0) throw new Error("Invalid journal boundary");
}

function compareBoundary(a: JournalStartBoundary, b: JournalStartBoundary): number {
    return a.segment - b.segment || a.offset - b.offset;
}

// Synchronous snapshot traversal: no native append can interleave with scan/save.
// ponytail: scans retained overlap; add an exact index only if measured scan cost requires it.
function scanJournal(start: JournalStartBoundary, end: JournalStartBoundary,
    visit: (record: any, position: JournalStartBoundary) => void): void {
    validateBoundary(start);
    validateBoundary(end);
    if (compareBoundary(start, end) > 0) throw new Error("Journal boundary beyond end");
    const seen = new Set<string>();
    const retired = readRetiredEvidence();
    for (let segment = start.segment; segment <= end.segment; segment++) {
        const file = path.join(EVENTS_DIR, padSegmentNumber(segment));
        const offset = segment === start.segment ? start.offset : 0;
        const sidecar = retired.get(segment);
        if (sidecar) {
            const limit = segment === end.segment ? end.offset : sidecar.size;
            const boundary = (n: number) => n === sidecar.size || sidecar.records.some(r => r.offset === n);
            if (offset > limit || !boundary(offset) || !boundary(limit)) throw new Error("Invalid retired record boundary");
            for (const record of sidecar.records) {
                if (record.offset < offset || record.offset >= limit) continue;
                if (seen.has(record.message_id)) throw new Error("Historical duplicate journal message ID");
                seen.add(record.message_id);
                visit({ version: 1, event: "message_create", ...record }, { segment, offset: record.offset });
            }
            continue;
        }
        // Writer can reserve an as-yet uncreated empty active segment.
        if (segment === end.segment && end.offset === 0 && offset === 0 && !fs.existsSync(file)) continue;
        const bytes = fs.readFileSync(file);
        const limit = segment === end.segment ? end.offset : deferredTail?.file === file ? deferredTail.size : bytes.length;
        if (limit > bytes.length || offset > limit || (offset > 0 && bytes[offset - 1] !== 10)) {
            throw new Error("Invalid journal record boundary");
        }
        let cursor = offset;
        while (cursor < limit) {
            const newline = bytes.indexOf(10, cursor);
            if (newline < 0 || newline >= limit) throw new Error("Incomplete journal record in recovery snapshot");
            let record: any;
            try { record = JSON.parse(bytes.subarray(cursor, newline).toString("utf8")); }
            catch { throw new Error("Malformed journal JSON"); }
            if (!record || record.version !== 1 || record.event !== "message_create" ||
                !isID(record.message_id) || record.message_id === "0" || !isID(record.channel_id) || record.channel_id === "0") throw new Error("Invalid journal recovery identity");
            if (seen.has(record.message_id)) throw new Error("Historical duplicate journal message ID");
            seen.add(record.message_id);
            visit(record, { segment, offset: cursor });
            cursor = newline + 1;
        }
    }
}

export function deriveRecoveryFloor(channelId: string, checkpoint: string,
    start: JournalStartBoundary, end = getCurrentJournalBoundary()): JournalStartBoundary {
    let floor = end;
    let found = false;
    scanJournal(start, end, (record, position) => {
        if (!found && record.channel_id === channelId && BigInt(record.message_id) > BigInt(checkpoint || "0")) {
            floor = position;
            found = true;
        }
    });
    return floor;
}

export function loadRecoveryState(): RecoveryStateRecord {
    try {
        if (!journalValidated) throw new Error(journalValidationError);
        const primary = fs.existsSync(RECOVERY_STATE_PATH);
        const source = primary ? RECOVERY_STATE_PATH : LEGACY_RECOVERY_STATE_PATH;
        const files = validateJournalTopology();
        if (!fs.existsSync(source)) {
            if (files.length) {
                throw new Error("Recovery state missing for existing journal");
            }
            return { version: 2, channels: {}, pending: null };
        }
        let state: any;
        try { state = JSON.parse(fs.readFileSync(source, "utf8")); }
        catch { throw new Error("Malformed recovery state JSON"); }
        const legacy = state?.version === 1;
        validateRecoveryState(state);
        repairValidatedTail();
        if (legacy || !primary) persistRecoveryState(state);
        return state;
    } catch (err) {
        recoveryReady = false;
        throw err;
    }
}

// Single preflight for disk loads AND proposed state replacements. No writes.
function validateRecoveryState(state: any): void {
    if (!journalValidated) throw new Error(journalValidationError);
    validateJournalTopology();
    if (!state || ![1, 2].includes(state.version) || !state.channels ||
        typeof state.channels !== "object" || Array.isArray(state.channels)) throw new Error("Invalid recovery state schema");
    requireKeys(state, ["version", "channels", "pending"]);
    const legacy = state.version === 1;
    if (!legacy && (!Object.hasOwn(state, "pending") || state.pending === undefined)) throw new Error("Missing pending recovery state");
    for (const channel of journalChannels) {
        if (!Object.hasOwn(state.channels, channel)) throw new Error("Recovery channel missing for existing journal");
    }
    const beginning = { segment: 1, offset: 0 };
    // GC has never existed: a missing prefix/interior segment is not a safe migration.
    if (legacy) scanJournal(beginning, getCurrentJournalBoundary(), () => {});
    for (const [channelId, raw] of Object.entries(state.channels)) {
        const ch = raw as ChannelRecoveryCheckpoint;
        requireKeys(ch, ["checkpoint_message_id", "checkpoint_journal_boundary", "checkpoint_source",
            "watch_after", "scan_after", "scan_until", "last_recovery_at", "last_result", "last_error", "recovered_count"]);
        if (!isID(channelId) || channelId === "0" || !isID(ch.checkpoint_message_id, true) ||
            !["success", "error"].includes(ch.last_result) ||
            typeof ch.last_recovery_at !== "string" || !Number.isFinite(Date.parse(ch.last_recovery_at)) ||
            !(ch.last_error === null || typeof ch.last_error === "string") ||
            !Number.isSafeInteger(ch.recovered_count) || ch.recovered_count < 0) throw new Error("Invalid channel checkpoint");
        // Missing legacy fields can migrate; structurally corrupt coordinates cannot.
        if (ch.checkpoint_journal_boundary !== undefined) validateBoundary(ch.checkpoint_journal_boundary);
        if (legacy) {
            ch.checkpoint_journal_boundary = beginning;
            ch.checkpoint_source = "legacy";
            // V1 did not distinguish REST progress from clock-derived initialization.
            // Preserve K, but it cannot safely become an immutable exclusion bound.
            ch.watch_after = "0";
            ch.scan_after = null;
            ch.scan_until = null;
            if (ch.last_error !== null) ch.last_error = "Legacy recovery error (details omitted)";
        }
        if (!isID(ch.watch_after) || BigInt(ch.watch_after!) > BigInt(ch.checkpoint_message_id || "0") ||
            (ch.watch_after !== "0" && (BigInt(ch.watch_after!) & 4194303n) !== 4194303n) ||
            !Object.hasOwn(ch, "scan_after") || !Object.hasOwn(ch, "scan_until") ||
            ch.scan_after === undefined || ch.scan_until === undefined ||
            ((ch.scan_after == null) !== (ch.scan_until == null)) ||
            (ch.scan_after != null && (!isID(ch.scan_after) || !isID(ch.scan_until) ||
                BigInt(ch.scan_after) < BigInt(ch.watch_after!) || BigInt(ch.scan_after) > BigInt(ch.scan_until!) ||
                BigInt(ch.scan_after) > BigInt(ch.checkpoint_message_id || "0") || BigInt(ch.scan_until!) <= BigInt(ch.watch_after!)))) {
            throw new Error("Invalid recovery replay bounds");
        }
        validateBoundary(ch.checkpoint_journal_boundary!);
        scanJournal(ch.checkpoint_journal_boundary!, ch.checkpoint_journal_boundary!, () => {});
        if (!["rest", "baseline_rest", "baseline_pending", "legacy"].includes(ch.checkpoint_source!)) throw new Error("Invalid checkpoint provenance");
        if (ch.checkpoint_source === "baseline_pending" && (ch.checkpoint_message_id !== "" || ch.watch_after !== "0")) throw new Error("Invalid pending baseline");
        if (ch.checkpoint_source === "legacy" && ch.watch_after !== "0") throw new Error("Invalid legacy exclusion bound");
        if (ch.checkpoint_source === "rest" && (!ch.checkpoint_message_id || BigInt(ch.checkpoint_message_id) <= BigInt(ch.watch_after!))) throw new Error("REST high-water requires a message above the watch bound");
        if (ch.checkpoint_source === "baseline_rest" && (!ch.checkpoint_message_id || ch.checkpoint_message_id !== ch.watch_after ||
            (ch.watch_after !== "0" && (BigInt(ch.watch_after!) & 4194303n) !== 4194303n))) throw new Error("Invalid REST-derived baseline");
        if (compareBoundary(ch.checkpoint_journal_boundary!, getCurrentJournalBoundary()) > 0) throw new Error("Recovery floor beyond journal end");
    }
    const pending = state.pending;
    if (pending != null) {
        requireKeys(pending, ["channel_id", "old_checkpoint_message_id", "new_checkpoint_message_id", "message_ids", "journal_start"]);
        validateBoundary(pending.journal_start);
        const ch = state.channels[pending.channel_id];
        if (!ch || !isID(pending.channel_id) || !isID(pending.old_checkpoint_message_id, true) || !isID(pending.new_checkpoint_message_id) ||
            !Array.isArray(pending.message_ids) || !pending.message_ids.length || pending.message_ids.length > 100 ||
            pending.message_ids.some((id: any) => !isID(id)) ||
            new Set(pending.message_ids).size !== pending.message_ids.length ||
            pending.message_ids.at(-1) !== pending.new_checkpoint_message_id ||
            pending.old_checkpoint_message_id !== ch.checkpoint_message_id ||
            pending.message_ids.some((id: string, i: number) => BigInt(id) <= BigInt(i ? pending.message_ids[i - 1] : legacy ? ch.checkpoint_message_id || "0" : ch.scan_after ?? ch.watch_after))) {
            throw new Error("Invalid pending recovery identity");
        }
        if (legacy) pending.journal_start = beginning;
        if (ch.scan_until != null && BigInt(pending.new_checkpoint_message_id) > BigInt(ch.scan_until)) throw new Error("Pending page exceeds replay upper bound");
        if (compareBoundary(ch.checkpoint_journal_boundary, pending.journal_start) > 0) throw new Error("Pending start precedes recovery floor");
        validateBoundary(pending.journal_start);
        if (compareBoundary(pending.journal_start, getCurrentJournalBoundary()) > 0) throw new Error("Pending boundary beyond journal end");
        scanJournal(pending.journal_start, pending.journal_start, () => {});
    }
    // Validate the physical claims too: a syntactically valid coordinate can hide
    // overlap, and an ID can exist in the wrong channel. At most two witnesses
    // per channel plus one bounded pending page are required.
    const witnesses = new Map<string, Set<string>>();
    let needsScan = pending != null;
    for (const [channel, raw] of Object.entries(state.channels)) {
        const ch = raw as ChannelRecoveryCheckpoint;
        const ids = new Set<string>();
        if (ch.checkpoint_source === "rest") ids.add(ch.checkpoint_message_id);
        if (ch.scan_after != null && ch.scan_after !== ch.watch_after) ids.add(ch.scan_after);
        witnesses.set(channel, ids);
        needsScan ||= ids.size > 0 || compareBoundary(ch.checkpoint_journal_boundary!, beginning) > 0;
    }
    const pendingIDs = new Set<string>(pending?.message_ids || []);
    if (needsScan) scanJournal(beginning, getCurrentJournalBoundary(), (record, position) => {
        const ch = state.channels[record.channel_id];
        if (!ch) throw new Error("Journal channel lacks recovery state");
        if (compareBoundary(position, ch.checkpoint_journal_boundary) < 0 &&
            BigInt(record.message_id) > BigInt(ch.checkpoint_message_id || "0")) throw new Error("Recovery floor hides journal overlap");
        if (pendingIDs.has(record.message_id) && record.channel_id !== pending.channel_id) throw new Error("Pending message belongs to another journal channel");
        witnesses.get(record.channel_id)?.delete(record.message_id);
    });
    for (const ids of witnesses.values()) if (ids.size) throw new Error("Committed recovery ID missing from journal");
    state.version = 2;
    if (state.pending === undefined) state.pending = null;
}

export function saveRecoveryState(state: RecoveryStateRecord): void {
    validateRecoveryState(state);
    repairValidatedTail();
    persistRecoveryState(state);
}

function persistRecoveryState(state: RecoveryStateRecord): void {
    fs.mkdirSync(path.dirname(RECOVERY_STATE_PATH), { recursive: true, mode: 0o700 });
    safeReplaceJSON(RECOVERY_STATE_PATH, state, 0o600);
}

export function getAppendedMessageIdsSince(startSegment: number, startOffset: number): string[] {
    const ids: string[] = [];
    scanJournal({ segment: startSegment, offset: startOffset }, getCurrentJournalBoundary(), record => ids.push(record.message_id));
    return ids;
}

// Appends raw NDJSON lines to the active journal segment with durable fsync and automatic segment rotation
export function appendRawLinesToJournal(lines: string[], ids: string[]): void {
    if (lines.length === 0) return;
    if (!currentSegmentPath) initSegment();
    if (!journalValidated) throw new Error("Journal requires restart after failed initialization/write");

    for (let i = 0; i < lines.length; i++) {
        const line = lines[i].endsWith("\n") ? lines[i] : lines[i] + "\n";
        const buf = Buffer.from(line, "utf8");

        if (currentSegmentSize > 0 && (currentSegmentSize + buf.length) > MAX_SEGMENT_SIZE) {
            currentSegmentNumber++;
            currentSegmentPath = path.join(EVENTS_DIR, padSegmentNumber(currentSegmentNumber));
            currentSegmentSize = 0;
        }

        const created = !fs.existsSync(currentSegmentPath);
        const fd = fs.openSync(currentSegmentPath, "a");
        try {
            fs.writeFileSync(fd, buf);
            fs.fsyncSync(fd);
            if (created) syncDirectory(EVENTS_DIR);
        } catch (err) {
            journalValidated = false;
            throw err;
        } finally {
            fs.closeSync(fd);
        }

        currentSegmentSize += buf.length;
        lastEventTime = new Date().toISOString();

        journalRecordCount++;
        journalChannels.add(JSON.parse(lines[i]).channel_id);
        if (ids[i]) {
            if (BigInt(ids[i]) > journalMaxID) journalMaxID = BigInt(ids[i]);
            recentJournalMessageIds.set(ids[i], JSON.parse(lines[i]).channel_id);
            if (recentJournalMessageIds.size > MAX_DEDUPE_ENTRIES) {
                const oldest = recentJournalMessageIds.keys().next().value;
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

        const existing = state.channels[pending.channel_id];
        const floor = existing.checkpoint_journal_boundary!;
        const start = BigInt(pending.message_ids[0]) <= BigInt(existing.checkpoint_message_id || "0")
            ? { segment: 1, offset: 0 }
            : compareBoundary(floor, pending.journal_start) < 0 ? floor : pending.journal_start;
        const alreadyAppendedIds = getAppendedMessageIdsSince(start.segment, start.offset);

        const alreadySet = new Set(alreadyAppendedIds);
        const allPresent = pendingIds.length > 0 && pendingIds.every(id => alreadySet.has(id));

        if (allPresent) {
            // Case A: ALL of pending page was appended before crash
            const highWater = BigInt(pending.new_checkpoint_message_id) > BigInt(existing.checkpoint_message_id || "0")
                ? pending.new_checkpoint_message_id : existing.checkpoint_message_id;
            const newBoundary = deriveRecoveryFloor(pending.channel_id, highWater, start);
            state.channels[pending.channel_id] = {
                ...existing,
                checkpoint_message_id: highWater,
                scan_after: existing.scan_until ? pending.new_checkpoint_message_id : null,
                checkpoint_source: BigInt(pending.new_checkpoint_message_id) >= BigInt(existing.checkpoint_message_id || "0") ? "rest" : existing.checkpoint_source,
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

// Apply the deferred active-tail repair only after complete state preflight.
function repairValidatedTail(): void {
    if (!deferredTail) return;
    const fd = fs.openSync(deferredTail.file, "r+");
    try { fs.ftruncateSync(fd, deferredTail.size); fs.fsyncSync(fd); }
    finally { fs.closeSync(fd); }
    deferredTail = null;
}

// Crash-resistant safe replacement for JSON control/status files:
// writes to a temp file in the same directory, syncs to disk, closes, and renames over destination.
function syncDirectory(dir: string): void {
    // Windows Node cannot open directories for fsync. Linux is the appliance target.
    if (process.platform === "win32") return;
    const fd = fs.openSync(dir, "r");
    try { fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
}

export function safeReplaceJSON(destinationPath: string, data: any, mode: number = 0o644): void {
    const dir = path.dirname(destinationPath);
    if (!fs.existsSync(dir)) {
        fs.mkdirSync(dir, { recursive: true, mode: 0o755 });
    }
    const tmpPath = path.join(dir, `.${path.basename(destinationPath)}.${Date.now()}.${Math.random().toString(36).slice(2)}.tmp`);
    const payload = JSON.stringify(data, null, 2) + "\n";

    const fd = fs.openSync(tmpPath, "w", mode);
    try {
        fs.writeFileSync(fd, payload);
        fs.fsyncSync(fd);
    } finally {
        fs.closeSync(fd);
    }
    fs.renameSync(tmpPath, destinationPath);
    syncDirectory(dir);
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

// Safe update of collector-runtime-status.json (collector-private, consumed by supervisor)
export function writeStatus(state: "starting" | "running" | "setup_required" | "reauth_required" | "error" = "running", authenticated?: boolean | null): void {
    if (!recoveryReady) return;
    try {
        if (typeof authenticated !== "undefined") {
            discordAuthenticatedState = authenticated;
        }
        const wl = loadWatchlist();
        const runtimeRecord = {
            version: 1,
            updated_at: new Date().toISOString(),
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
        const runtimeDir = fs.existsSync(COLLECTOR_DATA_DIR) ? COLLECTOR_DATA_DIR : EXCHANGE_DIR;
        safeReplaceJSON(path.join(runtimeDir, "collector-runtime-status.json"), runtimeRecord);
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

        let record: any;
        try { record = JSON.parse(eventJson); } catch { throw new Error("Malformed live record JSON"); }
        if (!record || record.version !== 1 || record.event !== "message_create" ||
            !isID(record.message_id) || record.message_id === "0" || !isID(record.channel_id)) throw new Error("Invalid live record");
        const state = loadRecoveryState(); // Corrupt/lost state also blocks live writes.
        if (!state.channels[record.channel_id]) {
            await beginChannelInitialization(undefined, record.channel_id);
        }
        // A cache miss is never proof of absence. Delayed Gateway replays can be old.
        if (recentJournalMessageIds.has(record.message_id)) {
            if (recentJournalMessageIds.get(record.message_id) !== record.channel_id) throw new Error("Message belongs to another journal channel");
            return true;
        }
        if (!journalValidated) throw new Error("Journal initialization failed");
        let present = false;
        // A complete startup scan plus every append maintains an exact maximum.
        // IDs above it cannot be duplicates; out-of-order IDs need a durable scan.
        if (recentJournalMessageIds.size !== journalRecordCount && BigInt(record.message_id) <= journalMaxID) {
            scanJournal({ segment: 1, offset: 0 }, getCurrentJournalBoundary(), r => {
                if (r.message_id === record.message_id) {
                    if (r.channel_id !== record.channel_id) throw new Error("Message belongs to another journal channel");
                    present = true;
                }
            });
        }
        if (!present) appendRawLinesToJournal([eventJson], [record.message_id]);

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
                const trimmedName = ch.name.trim();
                if (trimmedName === "___hidden___" || trimmedName === "__hidden__") continue;
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

export function initCatalogState(): void {
    try {
        const catalogPath = path.join(EXCHANGE_DIR, "catalog.json");
        if (fs.existsSync(catalogPath)) {
            const raw = fs.readFileSync(catalogPath, "utf8");
            const parsed = JSON.parse(raw);
            if (parsed && parsed.version === 1 && Array.isArray(parsed.guilds)) {
                catalogState = "ready";
                catalogUpdatedAt = parsed.updated_at || null;
                lastCatalogContentHash = JSON.stringify(parsed.guilds);
            }
        }
    } catch {}
}

export async function getRecoveryState(_?: IpcMainInvokeEvent): Promise<RecoveryStateRecord> {
    return loadRecoveryState();
}

// Persist watch initialization before any REST await. A crash/retry must not choose
// a newer no-backfill baseline and exclude messages received since this watch began.
export async function beginChannelInitialization(_: IpcMainInvokeEvent | undefined, channelId: string): Promise<void> {
    if (!isID(channelId) || channelId === "0") throw new Error("Invalid channel ID");
    const state = loadRecoveryState();
    if (state.pending?.channel_id === channelId) throw new Error("Pending recovery must resolve before initialization");
    if (state.channels[channelId]?.checkpoint_message_id) return;
    state.channels[channelId] = { checkpoint_message_id: "", checkpoint_source: "baseline_pending",
        watch_after: "0", scan_after: null, scan_until: null,
        checkpoint_journal_boundary: { segment: 1, offset: 0 }, last_recovery_at: new Date().toISOString(),
        last_result: "success", last_error: null, recovered_count: 0 };
    saveRecoveryState(state);
}

export async function saveChannelCheckpoint(
    _: IpcMainInvokeEvent | undefined,
    channelId: string,
    checkpoint: ChannelRecoveryCheckpoint
): Promise<boolean> {
    try {
        if (!isID(channelId) || channelId === "0" || !checkpoint ||
            !["success", "error"].includes(checkpoint.last_result) ||
            typeof checkpoint.last_recovery_at !== "string" || !Number.isFinite(Date.parse(checkpoint.last_recovery_at)) ||
            !(checkpoint.last_error === null || typeof checkpoint.last_error === "string")) {
            return false;
        }
        const state = loadRecoveryState();
        const existing = state.channels[channelId];
        requireKeys(checkpoint, ["checkpoint_message_id", "checkpoint_source", "last_recovery_at", "last_result", "last_error", "recovered_count"]);
        if (state.pending) throw new Error("Pending recovery must resolve before checkpoint update");
        if (existing && (existing.checkpoint_message_id || checkpoint.last_result === "error")) {
            // Renderer error/status updates cannot change progress or physical authority.
            state.channels[channelId] = { ...existing, last_result: checkpoint.last_result,
                last_error: checkpoint.last_error, last_recovery_at: checkpoint.last_recovery_at };
        } else {
            if (checkpoint.last_result !== "success" || checkpoint.checkpoint_source !== "baseline_rest" ||
                !isID(checkpoint.checkpoint_message_id) || checkpoint.checkpoint_message_id === "0" ||
                !Number.isSafeInteger(checkpoint.recovered_count) || checkpoint.recovered_count < 0) throw new Error("Initialization requires an explicit exclusion baseline");
            // Use a Discord-derived boundary at the START of the anchor millisecond.
            // This includes the anchor millisecond, never a local clock estimate.
            const anchor = BigInt(checkpoint.checkpoint_message_id);
            const millisecondStart = (anchor >> 22n) << 22n;
            const lower = (millisecondStart > 0n ? millisecondStart - 1n : 0n).toString();
            state.channels[channelId] = { ...checkpoint, checkpoint_message_id: lower,
                watch_after: lower, scan_after: null, scan_until: null,
                checkpoint_journal_boundary: deriveRecoveryFloor(channelId, lower, { segment: 1, offset: 0 }) };
        }
        saveRecoveryState(state);
        return true;
    } catch (err: any) {
        console.error("[CordBrief] Failed saving channel checkpoint:", err);
        return false;
    }
}

// A finite sweep prevents continuous traffic from starving a later replay of older
// newly-visible IDs. The page cap bounds each run; these two IDs survive restart.
export async function beginRecoveryScan(_: IpcMainInvokeEvent | undefined, channelId: string, until: string): Promise<void> {
    const state = loadRecoveryState();
    const ch = state.channels[channelId];
    if (state.pending || !ch || !isID(until) || BigInt(until) <= BigInt(ch.watch_after!)) throw new Error("Cannot start recovery scan");
    if (ch.scan_until != null) return;
    ch.scan_after = ch.watch_after;
    ch.scan_until = until;
    saveRecoveryState(state);
}

export async function finishRecoveryScan(_: IpcMainInvokeEvent | undefined, channelId: string): Promise<void> {
    const state = loadRecoveryState();
    const ch = state.channels[channelId];
    if (state.pending || !ch) throw new Error("Cannot finish recovery scan");
    ch.scan_after = null;
    ch.scan_until = null;
    saveRecoveryState(state);
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
    channelId: string, messagesJson: string[], newCheckpoint: string
): Promise<AppendRecoveredResult> {
    try {
        if (!isID(channelId) || !isID(newCheckpoint) || !Array.isArray(messagesJson) || messagesJson.length > 100) throw new Error("Invalid recovery page");
        if (!currentSegmentPath) initSegment();
        let state = loadRecoveryState();
        const priorPending = state.pending;
        if (state.pending) {
            const reconciliation = reconcilePendingRecovery();
            if (reconciliation.caseResult === "error") throw new Error("Pending reconciliation failed");
            state = loadRecoveryState();
        }
        const prev = state.channels[channelId];
        if (!prev || (!prev.checkpoint_message_id && prev.checkpoint_source !== "baseline_pending")) throw new Error("Channel baseline is not initialized");
        const messageIds = messagesJson.map(raw => {
            let record: any;
            try { record = JSON.parse(raw); }
            catch { throw new Error("Malformed recovered record JSON"); }
            if (!record || record.version !== 1 || record.event !== "message_create" || record.channel_id !== channelId ||
                !isID(record.message_id)) throw new Error("Invalid recovered record identity");
            return record.message_id as string;
        });
        if (state.pending && (state.pending.channel_id !== channelId ||
            state.pending.new_checkpoint_message_id !== newCheckpoint ||
            JSON.stringify(state.pending.message_ids) !== JSON.stringify(messageIds))) {
            throw new Error("Outstanding pending page differs; refusing replacement");
        }
        if (priorPending && !state.pending && priorPending.channel_id === channelId &&
            priorPending.new_checkpoint_message_id === newCheckpoint &&
            JSON.stringify(priorPending.message_ids) === JSON.stringify(messageIds)) {
            return { success: true, appendedCount: 0, skippedDuplicates: messageIds.length };
        }
        const oldFloor = prev.checkpoint_journal_boundary!;
        if (!messageIds.length) {
            if (state.pending || newCheckpoint !== (prev.checkpoint_message_id || "0")) throw new Error("Empty response cannot advance checkpoint");
            prev.checkpoint_journal_boundary = deriveRecoveryFloor(channelId, newCheckpoint, oldFloor);
            saveRecoveryState(state);
            return { success: true, appendedCount: 0, skippedDuplicates: 0 };
        }
        if (messageIds.at(-1) !== newCheckpoint || messageIds.some((id, i) =>
            BigInt(id) <= BigInt(i ? messageIds[i - 1] : prev.scan_after ?? prev.watch_after!)) ||
            (prev.scan_until != null && BigInt(newCheckpoint) > BigInt(prev.scan_until))) {
            throw new Error("Recovery page must advance strictly in snowflake order");
        }
        const journalStart = state.pending?.journal_start || getCurrentJournalBoundary();
        // Historical replay intentionally revisits IDs <= K. Its dedupe evidence is
        // the retained journal, not the forward-only floor. No journal GC is safe yet.
        const scanStart = BigInt(messageIds[0]) <= BigInt(prev.checkpoint_message_id || "0")
            ? { segment: 1, offset: 0 }
            : compareBoundary(oldFloor, journalStart) < 0 ? oldFloor : journalStart;
        // Validate overlap before modifying intent; a read failure never manufactures progress.
        const already = new Set(getAppendedMessageIdsSince(scanStart.segment, scanStart.offset));
        if (!state.pending) {
            state.pending = { channel_id: channelId, old_checkpoint_message_id: prev.checkpoint_message_id,
                new_checkpoint_message_id: newCheckpoint, message_ids: messageIds, journal_start: journalStart };
            saveRecoveryState(state);
        }
        const missing = messagesJson.filter((_, i) => !already.has(messageIds[i]));
        const missingIds = messageIds.filter(id => !already.has(id));
        // The recent cache is never authority for recovery; the durable scan is complete.
        appendRawLinesToJournal(missing, missingIds);
        const highWater = BigInt(newCheckpoint) > BigInt(prev.checkpoint_message_id || "0") ? newCheckpoint : prev.checkpoint_message_id;
        const floor = deriveRecoveryFloor(channelId, highWater, scanStart);
        state.channels[channelId] = { ...prev, checkpoint_message_id: highWater,
            scan_after: prev.scan_until ? newCheckpoint : null,
            checkpoint_source: BigInt(newCheckpoint) >= BigInt(prev.checkpoint_message_id || "0") ? "rest" : prev.checkpoint_source, checkpoint_journal_boundary: floor,
            last_recovery_at: new Date().toISOString(), last_result: "success", last_error: null,
            recovered_count: (prev.recovered_count || 0) + missing.length };
        state.pending = null;
        saveRecoveryState(state);
        writeStatus("running", true);
        return { success: true, appendedCount: missing.length, skippedDuplicates: messageIds.length - missing.length };
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

// Maintenance validates without repair, migration, pending reconciliation, status,
// or background watchers. It must hold the external runtime/Core exclusion leases.
export function inspectRetentionState(): RecoveryStateRecord {
    initSegment();
    if (deferredTail) throw new Error("Active tail requires Collector repair");
    const state = JSON.parse(readRegular(RECOVERY_STATE_PATH).toString("utf8"));
    if (state.version !== 2) throw new Error("Retention requires recovery v2");
    validateRecoveryState(state);
    if (state.pending !== null) throw new Error("Retention blocked by pending recovery");
    return state;
}

// Initialize on load
if (process.env.CORDBRIEF_RETENTION_INSPECT !== "1") try {
    initSegment();
    loadRecoveryState(); // Entire state validates before repair/reconciliation/status writes.
    recoveryReady = true;
    reconcilePendingRecovery();
    initCatalogState();
    writeStatus("starting", true);
    startWatchlistMonitor();
} catch (err: any) {
    // Preserve the validation cause; every recovery/live write still fails closed.
    journalValidationError = String(err?.message || "Journal initialization/validation failed");
}
