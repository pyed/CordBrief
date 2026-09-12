/*
 * CordBrief Official Discord RPC Collector Engine
 * Connects to unmodified Discord client via local RPC v1 + OAuth,
 * discovers catalog, reconciles watchlist subscriptions, captures live events,
 * performs bounded snapshot recovery, and writes to segmented NDJSON journal.
 * Fully integrates Phase 3D certified retention and exact deduplication across restarts.
 */

import * as fs from "fs";
import * as path from "path";
import { EventEmitter } from "events";
import { RpcTransport } from "./transport.mjs";
import { DiscordRpcClient } from "./protocol.mjs";
import {
    readRetiredEvidence,
    validateJournalTopology,
    scanJournalRecords,
    deriveFirstWatchBoundary,
    padSegmentNumber,
    isSnowflake
} from "./retention.mjs";
import { acquireRuntimeLock } from "../runtime.mjs";

const DEFAULT_MAX_SEGMENT_SIZE = 33554432; // 32 MiB

function syncDirectory(dir) {
    if (process.platform === "win32") return;
    try {
        const fd = fs.openSync(dir, "r");
        try { fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
    } catch (err) {
        const unsupportedCodes = new Set(["EINVAL", "ENOTSUP", "EISDIR", "EBADF"]);
        if (err && unsupportedCodes.has(err.code)) {
            return;
        }
        throw err;
    }
}

export function safeReplaceJSON(destinationPath, data, mode = 0o644) {
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

export function normalizeDiscordMessage(message, explicitGuildId = "", explicitChannelId = "") {
    const guildId = explicitGuildId || String(message.guild_id || "");
    const channelId = explicitChannelId || String(message.channel_id || "");
    const attachments = (message.attachments || []).map(a => ({
        id: String(a.id),
        filename: String(a.filename || "unnamed"),
        content_type: String(a.content_type || "application/octet-stream"),
        size: Number(a.size) || 0
    }));

    let replyTo = null;
    if (message.message_reference?.message_id) {
        replyTo = String(message.message_reference.message_id);
    } else if (message.referenced_message?.id) {
        replyTo = String(message.referenced_message.id);
    }

    return {
        version: 1,
        event: "message_create",
        message_id: String(message.id),
        guild_id: guildId,
        channel_id: channelId,
        timestamp: String(message.timestamp || new Date().toISOString()),
        captured_at: new Date().toISOString(),
        author: {
            id: String(message.author?.id || ""),
            name: String(message.author?.username || "unknown"),
            display_name: String(message.author?.global_name || message.author?.username || "unknown"),
            bot: Boolean(message.author?.bot)
        },
        content: String(message.content || ""),
        reply_to_message_id: replyTo,
        attachments
    };
}

export class DiscordRpcCollector extends EventEmitter {
    constructor(options = {}) {
        super();
        this.exchangeDir = options.exchangeDir || process.env.CORDBRIEF_EXCHANGE_DIR || "/var/cordbrief/exchange";
        this.collectorDataDir = options.collectorDataDir || process.env.CORDBRIEF_COLLECTOR_DATA_DIR || path.join(this.exchangeDir, "private");
        this.recoveryStatePath = options.recoveryStatePath || process.env.CORDBRIEF_RECOVERY_STATE_PATH || path.join(this.collectorDataDir, "recovery-state.json");
        this.runtimeDir = options.runtimeDir || process.env.CORDBRIEF_RUNTIME_DIR || path.join(this.exchangeDir, "runtime");
        this.runtimeLockFile = options.runtimeLockFile || path.join(this.runtimeDir, "runtime.lock");
        this.enableLock = options.enableLock !== undefined ? options.enableLock : true;
        this.lockHandle = null;

        this.eventsDir = path.join(this.exchangeDir, "events");
        this.maxSegmentSize = options.maxSegmentSize || parseInt(process.env.CORDBRIEF_MAX_SEGMENT_SIZE || String(DEFAULT_MAX_SEGMENT_SIZE), 10);
        this.syncDirectoryFn = options.syncDirectoryFn || syncDirectory;

        this.transport = options.transport || new RpcTransport({ socketPath: options.socketPath });
        this.client = options.client || new DiscordRpcClient(this.transport);

        this.currentSegmentNumber = 1;
        this.currentSegmentPath = "";
        this.currentSegmentSize = 0;

        this.collectorState = "starting";
        this.fatalError = null;
        this.lastEventAt = null;
        this.lastError = null;
        this.authenticated = false;
        this.catalogState = "unavailable";
        this.catalogUpdatedAt = null;

        this.watchedGeneration = 0;
        this.watchedChannelCount = 0;
        this.recoveryStateStatus = "idle";
        this.recoveryLastAt = null;
        this.recoveryLastError = null;

        this.activeSubscriptions = new Set();
        this.channelGuildMap = new Map(); // channel_id -> guild_id

        // Retention evidence map: segment -> sidecar
        this.retiredEvidence = new Map();

        // Authoritative deduplication state across physical segments and retired sidecars
        this.recentMessageIds = new Map(); // message_id -> channel_id
        this.journalMaxID = 0n;
        this.journalRecordCount = 0;
        this.journalChannels = new Set();
        this.maxDedupeEntries = 10000;
        this.journalValidated = false;

        this.lastWatchlistMtime = 0;
        this.cachedWatchlist = { valid: false, generation: -1, channel_ids: [] };
        this.heartbeatTimer = null;
        this.stopping = false;
    }

    /**
     * Enters terminal fail-stop state when journal write uncertainty occurs.
     * Halts further appends in-process and emits fatal_error for supervisor restart.
     * @param {string} reason
     */
    failStop(reason) {
        this.fatalError = reason;
        this.lastError = reason;
        this.collectorState = "error";
        this.stopping = true;
        this.writeStatus("error");
        this.emit("fatal_error", new Error(reason));
    }

    /**
     * Checks whether durable journal segments or retired sidecars contain prior collection evidence for this channel.
     * @param {string} channelId
     * @returns {boolean}
     */
    hasPriorCollectionEvidence(channelId) {
        if (!this.journalValidated && fs.existsSync(this.eventsDir)) {
            this.initJournal();
        }
        return Boolean(this.journalChannels && this.journalChannels.has(channelId));
    }

    /**
     * Initializes journal topology, validates retention evidence, and builds authoritative dedupe ledger.
     */
    initJournal() {
        if (!fs.existsSync(this.exchangeDir)) {
            fs.mkdirSync(this.exchangeDir, { recursive: true, mode: 0o755 });
        }
        if (!fs.existsSync(this.eventsDir)) {
            fs.mkdirSync(this.eventsDir, { recursive: true, mode: 0o755 });
        }

        // 1. Read certified retention evidence
        this.retiredEvidence = readRetiredEvidence(this.exchangeDir);

        // 2. Validate physical journal topology against retention manifest
        const files = validateJournalTopology(this.eventsDir, this.retiredEvidence);

        if (files.length === 0) {
            const retiredThrough = this.retiredEvidence.size;
            this.currentSegmentNumber = retiredThrough > 0 ? retiredThrough + 1 : 1;
            this.currentSegmentPath = path.join(this.eventsDir, padSegmentNumber(this.currentSegmentNumber));
            this.currentSegmentSize = 0;
        } else {
            const last = files[files.length - 1];
            const num = parseInt(last.replace(".ndjson", ""), 10);
            this.currentSegmentNumber = isNaN(num) || num < 1 ? 1 : num;
            this.currentSegmentPath = path.join(this.eventsDir, last);

            const stat = fs.statSync(this.currentSegmentPath);
            this.currentSegmentSize = stat.size;

            // Active-tail repair: If current segment has torn trailing bytes from an interrupted write (no trailing \n),
            // truncate back to the last complete record boundary.
            if (this.currentSegmentSize > 0) {
                const fd = fs.openSync(this.currentSegmentPath, "r+");
                try {
                    const lastByte = Buffer.alloc(1);
                    fs.readSync(fd, lastByte, 0, 1, this.currentSegmentSize - 1);
                    if (lastByte[0] !== 10) {
                        const fullBuf = fs.readFileSync(this.currentSegmentPath);
                        const lastNewline = fullBuf.lastIndexOf(10);
                        const validSize = lastNewline >= 0 ? lastNewline + 1 : 0;
                        fs.ftruncateSync(fd, validSize);
                        fs.fsyncSync(fd);
                        this.currentSegmentSize = validSize;
                    }
                } finally {
                    fs.closeSync(fd);
                }
                syncDirectory(this.eventsDir);
            }

            if (this.currentSegmentSize >= this.maxSegmentSize) {
                this.currentSegmentNumber++;
                this.currentSegmentPath = path.join(this.eventsDir, padSegmentNumber(this.currentSegmentNumber));
                this.currentSegmentSize = 0;
            }
        }

        // 3. Build authoritative startup deduplication ledger
        this.initDedupeLedger();
        this.journalValidated = true;
    }

    /**
     * Returns current physical journal boundary coordinate.
     * @returns {{segment: number, offset: number}}
     */
    getCurrentJournalBoundary() {
        if (!this.currentSegmentPath) this.initJournal();
        return {
            segment: this.currentSegmentNumber,
            offset: this.currentSegmentSize
        };
    }

    /**
     * Traverses certified retired sidecars and raw NDJSON segments to establish exact startup deduplication ledger.
     */
    initDedupeLedger() {
        this.recentMessageIds.clear();
        this.journalMaxID = 0n;
        this.journalRecordCount = 0;
        this.journalChannels.clear();

        const start = { segment: 1, offset: 0 };
        const end = { segment: this.currentSegmentNumber, offset: this.currentSegmentSize };

        scanJournalRecords(this.exchangeDir, start, end, this.retiredEvidence, (record, position) => {
            this.journalRecordCount++;
            this.journalChannels.add(record.channel_id);

            const n = BigInt(record.message_id);
            if (n > this.journalMaxID) this.journalMaxID = n;

            this.recentMessageIds.set(record.message_id, record.channel_id);
            if (this.recentMessageIds.size > this.maxDedupeEntries) {
                const oldest = this.recentMessageIds.keys().next().value;
                if (oldest) this.recentMessageIds.delete(oldest);
            }
        });
    }

    /**
     * Loads and validates recovery-state.json (Schema v2). Fail-closed on corruption.
     * @returns {object}
     */
    loadRecoveryState() {
        if (!this.journalValidated && fs.existsSync(this.eventsDir)) {
            this.initJournal();
        }
        if (!fs.existsSync(this.recoveryStatePath)) {
            if (this.journalChannels && this.journalChannels.size > 0) {
                throw new Error("Recovery state missing for existing journal channels");
            }
            return { version: 2, channels: {}, pending: null };
        }
        let raw;
        try {
            raw = fs.readFileSync(this.recoveryStatePath, "utf8");
        } catch (readErr) {
            throw new Error(`Failed to read recovery-state.json: ${readErr.message}`);
        }
        let parsed;
        try {
            parsed = JSON.parse(raw);
        } catch (jsonErr) {
            throw new Error(`Corrupt recovery-state.json: malformed JSON (${jsonErr.message})`);
        }
        if (!parsed || parsed.version !== 2 || typeof parsed.channels !== "object" || parsed.channels === null || Array.isArray(parsed.channels)) {
            throw new Error("Corrupt recovery-state.json: invalid schema (version must be 2 and channels must be an object)");
        }
        if (parsed.pending === undefined) parsed.pending = null;

        if (this.journalChannels) {
            for (const chId of this.journalChannels) {
                if (!parsed.channels[chId]) {
                    throw new Error(`Corrupt recovery-state.json: existing journal channel ${chId} missing from recovery state`);
                }
                const ch = parsed.channels[chId];
                if (ch.checkpoint_source === "baseline_pending" || !ch.checkpoint_message_id) {
                    throw new Error(`Corrupt recovery-state.json: existing journal channel ${chId} has invalid unanchored baseline_pending state`);
                }
            }
        }
        return parsed;
    }

    /**
     * Persists recovery-state.json (Schema v2) safely.
     * @param {object} state
     */
    saveRecoveryState(state) {
        state.version = 2;
        if (state.pending === undefined) state.pending = null;
        safeReplaceJSON(this.recoveryStatePath, state, 0o600);
    }

    /**
     * Initializes a newly watched channel's recovery checkpoint.
     * Refuses to re-initialize an existing channel that already has messages in the physical journal.
     * @param {string} channelId
     */
    beginChannelInitialization(channelId) {
        if (this.hasPriorCollectionEvidence(channelId)) {
            throw new Error(`Cannot initialize existing journal channel ${channelId} as new first-watch channel`);
        }
        const state = this.loadRecoveryState();
        if (state.channels[channelId]?.checkpoint_message_id) return;

        state.channels[channelId] = {
            checkpoint_message_id: "",
            checkpoint_source: "baseline_pending",
            watch_after: "0",
            scan_after: null,
            scan_until: null,
            checkpoint_journal_boundary: { segment: 1, offset: 0 },
            last_recovery_at: new Date().toISOString(),
            last_result: "success",
            last_error: null,
            recovered_count: 0
        };
        this.saveRecoveryState(state);
    }

    /**
     * Appends an array of normalized Schema v1 events to the segmented journal with fsync.
     * Enforces authoritative deduplication across physical segments and retired sidecars.
     * @param {Array<object>} events
     * @returns {number} Count of newly appended messages
     */
    appendEvents(events) {
        if (!events || events.length === 0) return 0;
        if (this.fatalError) {
            throw new Error(`Collector is in fail-stop state: ${this.fatalError}`);
        }
        if (!this.journalValidated) this.initJournal();

        let appendedCount = 0;
        for (const evt of events) {
            if (this.fatalError) {
                throw new Error(`Collector is in fail-stop state: ${this.fatalError}`);
            }
            if (!isSnowflake(evt.message_id) || !isSnowflake(evt.channel_id)) continue;

            // 1. Cache hit check
            if (this.recentMessageIds.has(evt.message_id)) {
                if (this.recentMessageIds.get(evt.message_id) !== evt.channel_id) {
                    throw new Error(`Message ID ${evt.message_id} belongs to another journal channel`);
                }
                continue;
            }

            // 2. If ID <= journalMaxID and missed in cache, run authoritative durable scan
            if (this.recentMessageIds.size !== this.journalRecordCount && BigInt(evt.message_id) <= this.journalMaxID) {
                let duplicateFound = false;
                const start = { segment: 1, offset: 0 };
                const end = { segment: this.currentSegmentNumber, offset: this.currentSegmentSize };

                scanJournalRecords(this.exchangeDir, start, end, this.retiredEvidence, record => {
                    if (record.message_id === evt.message_id) {
                        if (record.channel_id !== evt.channel_id) {
                            throw new Error(`Message ID ${evt.message_id} belongs to another journal channel`);
                        }
                        duplicateFound = true;
                    }
                });

                if (duplicateFound) {
                    this.recentMessageIds.set(evt.message_id, evt.channel_id);
                    continue;
                }
            }

            // 3. New message: write to active segment
            const line = JSON.stringify(evt) + "\n";
            const buf = Buffer.from(line, "utf8");

            if (this.currentSegmentSize > 0 && (this.currentSegmentSize + buf.length) > this.maxSegmentSize) {
                this.currentSegmentNumber++;
                this.currentSegmentPath = path.join(this.eventsDir, padSegmentNumber(this.currentSegmentNumber));
                this.currentSegmentSize = 0;
            }

            const isNewFile = !fs.existsSync(this.currentSegmentPath);
            const fd = fs.openSync(this.currentSegmentPath, "a");
            let fileWritten = false;
            try {
                fs.writeFileSync(fd, buf);
                fs.fsyncSync(fd);
                fileWritten = true;
                if (isNewFile) (this.syncDirectoryFn || syncDirectory)(this.eventsDir);
            } catch (ioErr) {
                if (fileWritten) {
                    const fatalMsg = `Fatal journal error: directory sync failed after writing record ${evt.message_id}: ${ioErr.message}`;
                    this.failStop(fatalMsg);
                    throw new Error(fatalMsg);
                }
                throw ioErr;
            } finally {
                fs.closeSync(fd);
            }

            this.currentSegmentSize += buf.length;
            this.lastEventAt = new Date().toISOString();
            appendedCount++;
            this.journalRecordCount++;
            this.journalChannels.add(evt.channel_id);

            const n = BigInt(evt.message_id);
            if (n > this.journalMaxID) this.journalMaxID = n;

            this.recentMessageIds.set(evt.message_id, evt.channel_id);
            if (this.recentMessageIds.size > this.maxDedupeEntries) {
                const oldest = this.recentMessageIds.keys().next().value;
                if (oldest) this.recentMessageIds.delete(oldest);
            }
        }

        if (appendedCount > 0) {
            if (this.collectorState !== "error") {
                this.writeStatus("running");
            }
        }
        return appendedCount;
    }

    /**
     * Discovers all visible guilds and channels, publishes catalog.json, and maps channels to guilds.
     */
    async refreshCatalog() {
        try {
            const rawGuilds = await this.client.getGuilds();
            const catalogGuilds = [];

            this.channelGuildMap.clear();

            for (const g of rawGuilds) {
                if (!g.id || !g.name) continue;
                const channels = await this.client.getChannels(g.id);
                const validChannels = [];

                for (const ch of channels) {
                    if (!ch.id || !ch.name) continue;
                    validChannels.push({
                        id: String(ch.id),
                        name: String(ch.name),
                        type: typeof ch.type === "number" ? ch.type : 0
                    });
                    this.channelGuildMap.set(String(ch.id), String(g.id));
                }

                validChannels.sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id));
                catalogGuilds.push({
                    id: String(g.id),
                    name: String(g.name),
                    channels: validChannels
                });
            }

            catalogGuilds.sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id));

            const now = new Date().toISOString();
            const record = {
                version: 1,
                updated_at: now,
                guilds: catalogGuilds
            };

            const catalogPath = path.join(this.exchangeDir, "catalog.json");
            safeReplaceJSON(catalogPath, record);

            this.catalogState = "ready";
            this.catalogUpdatedAt = now;
            this.writeStatus("running");
            return true;
        } catch (err) {
            this.catalogState = "error";
            this.lastError = `Catalog refresh failed: ${err?.message || String(err)}`;
            this.writeStatus("error");
            return false;
        }
    }

    /**
     * Loads watchlist.json with strict fail-closed validation.
     */
    loadWatchlist() {
        const watchlistFile = path.join(this.exchangeDir, "watchlist.json");
        try {
            if (!fs.existsSync(watchlistFile)) {
                this.cachedWatchlist = { valid: false, generation: -1, channel_ids: [] };
                return this.cachedWatchlist;
            }

            const stat = fs.statSync(watchlistFile);
            if (this.cachedWatchlist.valid && stat.mtimeMs === this.lastWatchlistMtime) {
                return this.cachedWatchlist;
            }

            const raw = fs.readFileSync(watchlistFile, "utf8");
            const parsed = JSON.parse(raw);

            if (
                typeof parsed !== "object" || parsed === null ||
                parsed.version !== 1 ||
                typeof parsed.generation !== "number" || parsed.generation < 0 ||
                !Array.isArray(parsed.channel_ids)
            ) {
                this.cachedWatchlist = { valid: false, generation: -1, channel_ids: [] };
                this.lastWatchlistMtime = stat.mtimeMs;
                return this.cachedWatchlist;
            }

            const validIds = new Set();
            for (const item of parsed.channel_ids) {
                if (typeof item === "string" && item.trim().length > 0) {
                    validIds.add(item.trim());
                } else {
                    this.cachedWatchlist = { valid: false, generation: -1, channel_ids: [] };
                    this.lastWatchlistMtime = stat.mtimeMs;
                    return this.cachedWatchlist;
                }
            }

            if (validIds.size === 0) {
                this.cachedWatchlist = { valid: false, generation: parsed.generation, channel_ids: [] };
                this.lastWatchlistMtime = stat.mtimeMs;
                return this.cachedWatchlist;
            }

            this.lastWatchlistMtime = stat.mtimeMs;
            this.cachedWatchlist = {
                valid: true,
                generation: parsed.generation,
                channel_ids: Array.from(validIds).sort()
            };
            return this.cachedWatchlist;
        } catch {
            this.cachedWatchlist = { valid: false, generation: -1, channel_ids: [] };
            return this.cachedWatchlist;
        }
    }

    /**
     * Reconciles active Discord RPC channel subscriptions with the latest watchlist.
     */
    async reconcileWatchlist() {
        const wl = this.loadWatchlist();
        this.watchedGeneration = wl.generation >= 0 ? wl.generation : 0;
        const targetIds = wl.valid ? new Set(wl.channel_ids) : new Set();
        this.watchedChannelCount = targetIds.size;

        // Channels to subscribe or retry recovery
        for (const chId of targetIds) {
            if (!this.activeSubscriptions.has(chId)) {
                try {
                    await this.client.subscribeMessageCreate(chId);
                    this.activeSubscriptions.add(chId);
                    await this.recoverChannelSnapshot(chId);
                } catch (err) {
                    this.lastError = `Subscribe failed for ${chId}: ${err?.message || String(err)}`;
                }
            } else if (this.recoveryStateStatus === "error") {
                await this.recoverChannelSnapshot(chId);
            }
        }

        // Channels to unsubscribe
        for (const chId of Array.from(this.activeSubscriptions)) {
            if (!targetIds.has(chId)) {
                try {
                    await this.client.unsubscribeMessageCreate(chId);
                } catch {}
                this.activeSubscriptions.delete(chId);
            }
        }

        if (this.recoveryStateStatus === "ready" && this.lastError && this.lastError.startsWith("Recovery failed for")) {
            this.lastError = null;
        }

        if (this.recoveryStateStatus === "error" || this.lastError) {
            this.collectorState = "error";
            this.writeStatus("error");
        } else {
            this.collectorState = "running";
            this.writeStatus("running");
        }
    }

    /**
     * Bounded recent message recovery via GET_CHANNEL snapshot.
     * Enforces watch_after lower bounds, deduplication, and persists Recovery v2 checkpoint.
     * @param {string} channelId
     */
    async recoverChannelSnapshot(channelId) {
        try {
            this.recoveryStateStatus = "recovering";
            let state = this.loadRecoveryState();
            let chState = state.channels[channelId];

            if (!chState) {
                this.beginChannelInitialization(channelId);
                state = this.loadRecoveryState();
                chState = state.channels[channelId];
            }

            const ch = await this.client.getChannel(channelId);
            const messages = Array.isArray(ch?.messages) ? ch.messages : [];

            if (messages.length === 0) {
                if (chState.checkpoint_source === "baseline_pending") {
                    if (this.hasPriorCollectionEvidence(channelId)) {
                        throw new Error(`Cannot re-anchor channel ${channelId}: channel has existing durable collection evidence but recovery state is baseline_pending`);
                    }
                    chState.watch_after = "0";
                    chState.checkpoint_message_id = "0";
                    chState.checkpoint_source = "rpc";
                }
                chState.last_recovery_at = new Date().toISOString();
                chState.last_result = "success";
                chState.last_error = null;
                this.saveRecoveryState(state);
                this.recoveryStateStatus = "ready";
                this.recoveryLastAt = new Date().toISOString();
                return 0;
            }

            // Sort raw snapshot ascending by snowflake
            messages.sort((a, b) => BigInt(a.id) < BigInt(b.id) ? -1 : 1);

            // First-watch anchor policy: establish watch_after from the latest visible message H
            if (chState.checkpoint_source === "baseline_pending") {
                if (this.hasPriorCollectionEvidence(channelId)) {
                    throw new Error(`Cannot re-anchor channel ${channelId}: channel has existing durable collection evidence but recovery state is baseline_pending`);
                }
                const latestMsg = messages[messages.length - 1];
                const watchAfter = deriveFirstWatchBoundary(latestMsg.id);
                chState.watch_after = watchAfter;
                chState.checkpoint_message_id = watchAfter;
                chState.checkpoint_source = "rpc";
            }

            // Exclude pre-watch history (Snowflake <= watch_after)
            const watchAfterBig = BigInt(chState.watch_after || "0");
            const eligibleMessages = messages.filter(m => BigInt(m.id) > watchAfterBig);

            const guildId = this.channelGuildMap.get(channelId) || ch.guild_id || "";
            const normalized = eligibleMessages.map(m => normalizeDiscordMessage(m, guildId, channelId));

            const appended = this.appendEvents(normalized);

            // Update checkpoint high-water mark if newer messages accepted
            if (eligibleMessages.length > 0) {
                const highestMsgId = eligibleMessages[eligibleMessages.length - 1].id;
                if (BigInt(highestMsgId) > BigInt(chState.checkpoint_message_id || "0")) {
                    chState.checkpoint_message_id = highestMsgId;
                }
            }

            chState.checkpoint_journal_boundary = this.getCurrentJournalBoundary();
            chState.last_recovery_at = new Date().toISOString();
            chState.last_result = "success";
            chState.last_error = null;
            chState.recovered_count = (chState.recovered_count || 0) + appended;

            this.saveRecoveryState(state);

            this.recoveryStateStatus = "ready";
            this.recoveryLastAt = new Date().toISOString();
            this.recoveryLastError = null;
            if (this.lastError && this.lastError.startsWith("Recovery failed for")) {
                this.lastError = null;
            }
            if (this.collectorState === "error" && !this.fatalError) {
                this.collectorState = "running";
                this.writeStatus("running");
            }
            return appended;
        } catch (err) {
            this.recoveryStateStatus = "error";
            this.recoveryLastError = `Recovery failed for ${channelId}: ${err?.message || String(err)}`;
            this.lastError = this.recoveryLastError;
            this.collectorState = "error";
            try {
                const state = this.loadRecoveryState();
                if (state.channels[channelId]) {
                    state.channels[channelId].last_result = "error";
                    state.channels[channelId].last_error = this.recoveryLastError;
                    state.channels[channelId].last_recovery_at = new Date().toISOString();
                    this.saveRecoveryState(state);
                }
            } catch {}
            this.writeStatus("error");
            return 0;
        }
    }

    /**
     * Handles live MESSAGE_CREATE dispatch from Discord IPC.
     * @param {object} dispatchData
     */
    handleMessageCreate(dispatchData) {
        if (!dispatchData) return;
        if (this.fatalError) {
            console.error(`[RPC Collector] Refusing live message append: collector is in fail-stop state (${this.fatalError})`);
            return;
        }
        const msg = dispatchData.message || dispatchData;
        if (!msg || !msg.id) return;
        const chId = String(dispatchData.channel_id || msg.channel_id || "");

        console.log(`[RPC Collector] handleMessageCreate: msgId=${msg.id} chId=${chId} activeSub=${this.activeSubscriptions.has(chId)}`);

        // Fail-closed: only record if channel is currently in active subscriptions
        if (!this.activeSubscriptions.has(chId)) {
            return;
        }

        if (this.recoveryStateStatus === "error") {
            console.error(`[RPC Collector] Refusing live message append due to recovery state error: ${this.recoveryLastError}`);
            this.lastError = `Recovery state error: ${this.recoveryLastError}`;
            return;
        }

        let state;
        try {
            state = this.loadRecoveryState();
        } catch (err) {
            console.error(`[RPC Collector] Refusing live message append due to recovery state error: ${err.message}`);
            this.lastError = `Recovery state error: ${err.message}`;
            return;
        }
        const chState = state.channels[chId];
        if (chState?.watch_after && BigInt(msg.id) <= BigInt(chState.watch_after)) {
            console.log(`[RPC Collector] Dropping pre-watch message: ${msg.id} <= ${chState.watch_after}`);
            return;
        }

        const guildId = this.channelGuildMap.get(chId) || msg.guild_id || "";
        const evt = normalizeDiscordMessage(msg, guildId, chId);
        const appended = this.appendEvents([evt]);
        console.log(`[RPC Collector] Appended ${appended} live event(s) to journal (message ID: ${msg.id})`);

        if (appended > 0 && chState) {
            if (BigInt(msg.id) > BigInt(chState.checkpoint_message_id || "0")) {
                chState.checkpoint_message_id = String(msg.id);
                chState.checkpoint_journal_boundary = this.getCurrentJournalBoundary();
                this.saveRecoveryState(state);
            }
        }
    }

    /**
     * Authoritative telemetry snapshot for collector-status.json conforming to Schema v1.
     */
    getStatusRecord() {
        return {
            version: 1,
            updated_at: new Date().toISOString(),
            mode: "normal",
            collector_state: this.collectorState,
            discord_authenticated: this.authenticated,
            catalog_state: this.catalogState,
            catalog_updated_at: this.catalogUpdatedAt,
            watched_generation: this.watchedGeneration,
            watched_channel_count: this.watchedChannelCount,
            active_segment: this.currentSegmentNumber,
            last_event_at: this.lastEventAt,
            last_error: this.lastError,
            prompt_state: null,
            action_required: null,
            recovery_state: this.recoveryStateStatus,
            recovery_last_at: this.recoveryLastAt,
            recovery_pending_channels: 0,
            recovery_last_error: this.recoveryLastError
        };
    }

    /**
     * Writes operational telemetry to collector-status.json conforming to Schema v1.
     */
    writeStatus(state = null) {
        if (state) this.collectorState = state;
        try {
            const statusRecord = this.getStatusRecord();
            const statusPath = path.join(this.exchangeDir, "collector-status.json");
            safeReplaceJSON(statusPath, statusRecord);
        } catch (err) {
            this.lastError = `writeStatus failed: ${err?.message || String(err)}`;
        }
    }

    /**
     * Starts the collector: initializes journal, connects to Discord, authenticates,
     * refreshes catalog, starts watchlist monitoring, and listens for live messages.
     * @param {object} config
     * @param {string} config.clientId
     * @param {string} config.accessToken
     */
    async start(config) {
        if (this.enableLock) {
            const lock = acquireRuntimeLock(this.runtimeLockFile, "collector");
            if (!lock.acquired) {
                this.lastError = `Runtime lock held by ${lock.existing?.holder || "unknown"}`;
                this.writeStatus("error");
                throw new Error(`Failed to acquire runtime lock: ${this.lastError}`);
            }
            this.lockHandle = lock;
        }

        try {
            this.initJournal();
            this.writeStatus("starting");

            // 1. Connect and Handshake
            await this.transport.connect(config.clientId);

            // 2. Authenticate
            if (!this.authenticated && config.accessToken) {
                await this.client.authenticate(config.accessToken);
                this.authenticated = true;
            }

            // 3. Attach live event dispatch listener
            this.transport.on("dispatch", event => {
                console.log(`[RPC Collector] Dispatch event received: evt=${event.evt}`);
                if (event.evt === "MESSAGE_CREATE") {
                    this.handleMessageCreate(event.data);
                }
            });

            // 4. Refresh Catalog
            await this.refreshCatalog();

            // 5. Initial Watchlist Reconciliation
            await this.reconcileWatchlist();

            // 6. Monitor watchlist.json for changes
            const watchlistFile = path.join(this.exchangeDir, "watchlist.json");
            try {
                fs.watchFile(watchlistFile, { interval: 1000 }, (curr, prev) => {
                    if (curr.mtimeMs !== prev.mtimeMs) {
                        this.reconcileWatchlist().catch(err => {
                            this.lastError = `Watchlist reload error: ${err?.message || String(err)}`;
                        });
                    }
                });
            } catch {}

            if (this.recoveryStateStatus === "error" || this.lastError) {
                this.collectorState = "error";
                this.writeStatus("error");
            } else {
                this.collectorState = "running";
                this.writeStatus("running");
            }
        } catch (err) {
            if (this.lockHandle) {
                this.lockHandle.release();
                this.lockHandle = null;
            }
            throw err;
        }
    }

    /**
     * Releases exclusive runtime single-ownership lease.
     */
    releaseRuntimeLock() {
        if (this.lockHandle) {
            this.lockHandle.release();
            this.lockHandle = null;
        }
    }

    /**
     * Stops the collector cleanly.
     */
    async stop() {
        this.stopping = true;
        if (this.heartbeatTimer) {
            clearInterval(this.heartbeatTimer);
            this.heartbeatTimer = null;
        }

        const watchlistFile = path.join(this.exchangeDir, "watchlist.json");
        try {
            fs.unwatchFile(watchlistFile);
        } catch {}

        for (const chId of Array.from(this.activeSubscriptions)) {
            try {
                await this.client.unsubscribeMessageCreate(chId);
            } catch {}
        }
        this.activeSubscriptions.clear();

        this.transport.close();
        if (this.lockHandle) {
            this.lockHandle.release();
            this.lockHandle = null;
        }
        this.writeStatus("stopped");
    }
}
