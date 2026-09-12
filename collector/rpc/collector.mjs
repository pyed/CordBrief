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
        this.fsyncFn = options.fsyncFn || null;

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
        this.recoveryQueue = Promise.resolve();
        this.reconcilePromise = null;
        this.reconcileRequested = false;
        this.recoveryEpoch = 0;
    }

    /**
     * Enters terminal fail-stop state when journal write uncertainty occurs.
     * Halts further appends in-process and emits fatal_error for supervisor restart.
     * No I/O may precede the production listener's immediate process exit.
     * @param {string} reason
     */
    failStop(reason) {
        this.fatalError = reason;
        this.lastError = reason;
        this.collectorState = "error";
        this.stopping = true;
        this.recoveryEpoch++;
        this.emit("fatal_error", new Error(reason));
    }

    /**
     * Authoritative single transition for recovery-state failures.
     * Sets collector_state = error, recovery_state = error, records error details, and emits recovery_error.
     * @param {string|Error} reason
     */
    markRecoveryError(reason) {
        if (this.stopping || this.fatalError) return;
        this.recoveryEpoch++;
        const msg = String(reason?.message || reason || "Unknown recovery failure");
        this.collectorState = "error";
        this.recoveryStateStatus = "error";
        this.recoveryLastError = msg;
        this.lastError = msg;
        try {
            this.writeStatus("error");
        } catch {}
        this.emit("recovery_error", new Error(msg));
    }

    // All asynchronous recovery writers share this queue, including direct helper
    // callers. Live capture remains synchronous and is merged after each RPC wait.
    queueRecovery(operation) {
        const result = this.recoveryQueue.then(() => {
            if (this.stopping || this.fatalError) return 0;
            return operation();
        });
        this.recoveryQueue = result.catch(() => {});
        return result;
    }

    assertRecoveryCurrent(epoch) {
        if (this.stopping || this.fatalError || epoch !== this.recoveryEpoch) {
            throw new Error("Recovery interrupted by shutdown or a newer recovery error");
        }
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
                if (ch.checkpoint_source === "baseline_pending") {
                    throw new Error(`Corrupt recovery-state.json: existing journal channel ${chId} has invalid unanchored baseline_pending state`);
                }
                if (!ch.checkpoint_message_id || ch.watch_after === undefined || ch.watch_after === null || ch.watch_after === "") {
                    throw new Error(`Corrupt recovery-state.json: existing journal channel ${chId} missing checkpoint or watch_after`);
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
     * Initializes a newly watched channel's recovery checkpoint synchronously.
     * Refuses to re-initialize an existing channel that already has messages in the physical journal.
     * @param {string} channelId
     * @param {string} [explicitBaseline]
     */
    beginChannelInitialization(channelId, explicitBaseline) {
        if (this.hasPriorCollectionEvidence(channelId)) {
            throw new Error(`Cannot initialize existing journal channel ${channelId} as new first-watch channel`);
        }
        const state = this.loadRecoveryState();
        if (state.channels[channelId]?.checkpoint_message_id && state.channels[channelId]?.watch_after && state.channels[channelId]?.checkpoint_source === "rpc") return;

        if (explicitBaseline !== undefined && explicitBaseline !== null) {
            const baseline = String(explicitBaseline);
            state.channels[channelId] = {
                checkpoint_message_id: baseline,
                checkpoint_source: "rpc",
                watch_after: baseline,
                initial_baseline: baseline,
                scan_after: null,
                scan_until: null,
                checkpoint_journal_boundary: this.getCurrentJournalBoundary(),
                last_recovery_at: null,
                last_result: "pending",
                last_error: null,
                recovered_count: 0
            };
        } else {
            state.channels[channelId] = {
                checkpoint_message_id: "",
                checkpoint_source: "baseline_pending",
                watch_after: "",
                initial_baseline: null,
                scan_after: null,
                scan_until: null,
                checkpoint_journal_boundary: this.getCurrentJournalBoundary(),
                last_recovery_at: null,
                last_result: "pending",
                last_error: null,
                recovered_count: 0
            };
        }
        this.saveRecoveryState(state);
    }

    /**
     * Establishes an immutable pre-watch baseline for a newly watched channel.
     * Queries Discord for history prior to enabling live collection, derives the exclusion boundary,
     * and persists it durably. Once persisted, the baseline is immutable across restarts.
     * @param {string} channelId
     * @returns {Promise<object>}
     */
    establishFirstWatchBaseline(channelId) {
        return this.queueRecovery(() => this._establishFirstWatchBaseline(channelId, this.recoveryEpoch));
    }

    async _establishFirstWatchBaseline(channelId, epoch) {
        this.assertRecoveryCurrent(epoch);
        const state = this.loadRecoveryState();
        if (state.channels[channelId]?.checkpoint_source === "rpc" && state.channels[channelId]?.watch_after) {
            return state.channels[channelId];
        }
        if (this.hasPriorCollectionEvidence(channelId)) {
            throw new Error(`Cannot initialize existing journal channel ${channelId} as new first-watch channel`);
        }
        const ch = await this.client.getChannel(channelId);
        this.assertRecoveryCurrent(epoch);
        const messages = Array.isArray(ch?.messages) ? ch.messages : [];
        const highest = messages.reduce((max, m) => BigInt(m.id) > BigInt(max) ? String(m.id) : max, "0");
        // This synchronous commit reloads current state and preserves any baseline
        // already established while Discord was pending.
        this.beginChannelInitialization(channelId, deriveFirstWatchBoundary(highest));
        return this.loadRecoveryState().channels[channelId];
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
            let bytesWritten = false;
            try {
                fs.writeFileSync(fd, buf);
                bytesWritten = true;
                (this.fsyncFn || fs.fsyncSync)(fd);
                if (isNewFile) (this.syncDirectoryFn || syncDirectory)(this.eventsDir);
            } catch (ioErr) {
                if (bytesWritten) {
                    const fatalMsg = `Fatal journal error: sync failure after writing record ${evt.message_id}: ${ioErr.message}`;
                    this.failStop(fatalMsg);
                    throw new Error(fatalMsg);
                }
                throw ioErr;
            } finally {
                try {
                    fs.closeSync(fd);
                } catch (closeErr) {
                    if (bytesWritten && !this.fatalError) {
                        const fatalMsg = `Fatal journal error: close failure after writing record ${evt.message_id}: ${closeErr.message}`;
                        this.failStop(fatalMsg);
                        throw new Error(fatalMsg);
                    }
                }
            }

            try {
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
            } catch (postErr) {
                const fatalMsg = `Fatal journal error: bookkeeping failure after writing record ${evt.message_id}: ${postErr.message}`;
                this.failStop(fatalMsg);
                throw new Error(fatalMsg);
            }
        }

        if (appendedCount > 0) this.writeStatus();
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
     * Transactionally aggregates recovery across all watched channels.
     */
    reconcileWatchlist() {
        if (this.stopping || this.fatalError) return Promise.resolve();
        this.reconcileRequested = true;
        if (this.reconcilePromise) return this.reconcilePromise;
        this.reconcilePromise = this.queueRecovery(async () => {
            try {
                do {
                    this.reconcileRequested = false;
                    await this._reconcileWatchlist();
                } while (this.reconcileRequested && !this.stopping && !this.fatalError);
            } finally {
                this.reconcilePromise = null;
            }
        });
        return this.reconcilePromise;
    }

    async _reconcileWatchlist() {
        const epoch = this.recoveryEpoch;
        const wl = this.loadWatchlist();
        const targetIds = wl.valid ? new Set(wl.channel_ids) : new Set();
        const needsFullRecovery = this.recoveryStateStatus === "error" || this.collectorState === "error";
        this.watchedGeneration = wl.generation >= 0 ? wl.generation : 0;
        this.watchedChannelCount = targetIds.size;
        this.collectorState = needsFullRecovery ? "error" : "starting";
        this.recoveryStateStatus = "recovering";
        this.writeStatus();
        try {
            this.loadRecoveryState();
            for (const chId of Array.from(this.activeSubscriptions)) {
                if (!targetIds.has(chId)) {
                    try { await this.client.unsubscribeMessageCreate(chId); } catch {}
                    this.assertRecoveryCurrent(epoch);
                    this.activeSubscriptions.delete(chId);
                }
            }
            const failures = [];
            for (const chId of targetIds) {
                this.assertRecoveryCurrent(epoch);
                try {
                    let chState = this.loadRecoveryState().channels[chId];
                    if (!chState?.watch_after || chState.checkpoint_source === "baseline_pending") {
                        chState = await this._establishFirstWatchBaseline(chId, epoch);
                    }
                    const wasSubscribed = this.activeSubscriptions.has(chId);
                    if (!wasSubscribed) {
                        await this.client.subscribeMessageCreate(chId);
                        this.assertRecoveryCurrent(epoch);
                        this.activeSubscriptions.add(chId);
                    }
                    if (!wasSubscribed || chState.last_result !== "success" || needsFullRecovery) {
                        await this._recoverChannelSnapshot(chId, epoch);
                    }
                } catch (err) {
                    this.assertRecoveryCurrent(epoch); // A newer live error aborts this old pass.
                    this.recordChannelRecoveryError(chId, err);
                    failures.push(`${chId}: ${err.message}`);
                }
            }
            this.assertRecoveryCurrent(epoch);
            // Do not publish ready for an old watchlist, even if its watcher has not fired yet.
            if (JSON.stringify(this.loadWatchlist()) !== JSON.stringify(wl)) this.reconcileRequested = true;
            if (failures.length) {
                this.markRecoveryError(`Recovery failed for channel(s): ${failures.join("; ")}`);
            } else if (!this.reconcileRequested) {
                this.finishRecovery();
            }
        } catch (err) {
            if (!this.stopping && !this.fatalError && epoch === this.recoveryEpoch) {
                this.markRecoveryError(`Recovery state error: ${err.message}`);
            }
        }
    }

    finishRecovery() {
        this.recoveryStateStatus = "ready";
        this.recoveryLastError = null;
        this.recoveryLastAt = new Date().toISOString();
        if (this.lastError && (this.lastError.startsWith("Recovery") || this.lastError.startsWith("Subscribe failed"))) {
            this.lastError = null;
        }
        this.collectorState = this.lastError ? "error" : "running";
        this.writeStatus();
    }

    recordChannelRecoveryError(channelId, err) {
        try {
            const state = this.loadRecoveryState();
            if (state.channels[channelId]) {
                Object.assign(state.channels[channelId], {
                    last_result: "error", last_error: err.message, last_recovery_at: new Date().toISOString()
                });
                this.saveRecoveryState(state);
            }
        } catch {} // A failed annotation must never turn a failed RPC into success.
    }

    // Direct recovery callers use the same owner as the watchlist coordinator.
    recoverChannelSnapshot(channelId) {
        return this.queueRecovery(async () => {
            const epoch = this.recoveryEpoch;
            try {
                const appended = await this._recoverChannelSnapshot(channelId, epoch);
                this.assertRecoveryCurrent(epoch);
                const state = this.loadRecoveryState();
                if (Object.values(state.channels).every(ch => ch.last_result === "success")) this.finishRecovery();
                return appended;
            } catch (err) {
                if (!this.stopping && !this.fatalError && epoch === this.recoveryEpoch) {
                    this.recordChannelRecoveryError(channelId, err);
                    this.markRecoveryError(`Recovery failed for ${channelId}: ${err.message}`);
                }
                return 0;
            }
        });
    }

    async _recoverChannelSnapshot(channelId, epoch) {
        this.assertRecoveryCurrent(epoch);
        let state = this.loadRecoveryState();
        let chState = state.channels[channelId];
        if (!chState?.watch_after || chState.checkpoint_source === "baseline_pending") {
            chState = await this._establishFirstWatchBaseline(channelId, epoch);
        }
        const baseline = chState.watch_after;
        const ch = await this.client.getChannel(channelId);
        this.assertRecoveryCurrent(epoch);
        // Never commit a pre-await object: merge live checkpoints/other channels
        // from freshly validated durable state, and reject changed baselines.
        state = this.loadRecoveryState();
        chState = state.channels[channelId];
        if (!chState || chState.watch_after !== baseline || chState.checkpoint_source === "baseline_pending") {
            throw new Error(`Recovery baseline changed while awaiting snapshot for ${channelId}`);
        }
        const messages = Array.isArray(ch?.messages) ? ch.messages : [];
        messages.sort((a, b) => BigInt(a.id) < BigInt(b.id) ? -1 : 1);
        const eligible = messages.filter(m => BigInt(m.id) > BigInt(baseline));
        const guildId = this.channelGuildMap.get(channelId) || ch?.guild_id || "";
        const appended = this.appendEvents(eligible.map(m => normalizeDiscordMessage(m, guildId, channelId)));
        this.assertRecoveryCurrent(epoch);
        if (eligible.length && BigInt(eligible.at(-1).id) > BigInt(chState.checkpoint_message_id)) {
            chState.checkpoint_message_id = String(eligible.at(-1).id);
        }
        Object.assign(chState, {
            checkpoint_journal_boundary: this.getCurrentJournalBoundary(),
            last_recovery_at: new Date().toISOString(), last_result: "success", last_error: null,
            recovered_count: (chState.recovered_count || 0) + appended
        });
        this.saveRecoveryState(state);
        return appended;
    }

    /**
     * Handles live MESSAGE_CREATE dispatch from Discord IPC.
     * @param {object} dispatchData
     */
    handleMessageCreate(dispatchData) {
        if (!dispatchData || this.stopping) return;
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
            this.markRecoveryError(`Recovery state error: ${err.message}`);
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
        this.recoveryEpoch++;
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
