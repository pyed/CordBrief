/*
 * CordBrief Journal Retention & Topology Manager
 * Validates retention-manifest.json, verifies retired sidecars, validates contiguous journal topology,
 * and enables seamless unified scanning across retired sidecars and raw NDJSON segments.
 */

import * as fs from "fs";
import * as path from "path";
import * as crypto from "crypto";

export function padSegmentNumber(num) {
    return String(num).padStart(16, "0") + ".ndjson";
}

export function sha256(buffer) {
    return crypto.createHash("sha256").update(buffer).digest("hex");
}

export function isSnowflake(value) {
    return typeof value === "string" && (/^(0|[1-9][0-9]{0,19})$/.test(value) && BigInt(value) <= 18446744073709551615n);
}

/**
 * Parses JSON while strictly rejecting duplicate object keys.
 * Ensures Collector and Core give identical meaning to retention contracts.
 */
export function parseStrictJSON(bytes) {
    const text = typeof bytes === "string" ? bytes : bytes.toString("utf8");
    const parsed = JSON.parse(text);

    const stack = [];
    for (const [token] of text.matchAll(/"(?:\\.|[^"\\])*"|[{}\[\],:]/g)) {
        if (token === "{") {
            stack.push({ keys: new Set(), key: true });
        } else if (token === "[") {
            stack.push(null);
        } else if (token === "}" || token === "]") {
            stack.pop();
        } else {
            const object = stack.at(-1);
            if (token === "," && object) {
                object.key = true;
            } else if (token.startsWith('"') && object?.key) {
                const key = JSON.parse(token);
                if (object.keys.has(key)) {
                    throw new Error(`Duplicate JSON key ${key} in retention document`);
                }
                object.keys.add(key);
                object.key = false;
            }
        }
    }
    return parsed;
}

function readRegular(file) {
    const stat = fs.lstatSync(file);
    if (!stat.isFile()) throw new Error(`Non-regular file encountered: ${file}`);
    return fs.readFileSync(file);
}

/**
 * Reads and verifies retention-manifest.json and associated sidecars.
 * Returns a Map of segment number -> sidecar record.
 * @param {string} exchangeDir
 * @returns {Map<number, object>}
 */
export function readRetiredEvidence(exchangeDir) {
    const result = new Map();
    const manifestPath = path.join(exchangeDir, "retention-manifest.json");

    let manifestBytes;
    try {
        manifestBytes = readRegular(manifestPath);
    } catch (err) {
        if (err.code === "ENOENT") return result;
        throw err;
    }

    const manifest = parseStrictJSON(manifestBytes);
    if (
        !manifest || typeof manifest !== "object" ||
        manifest.version !== 1 ||
        !Number.isSafeInteger(manifest.retired_through) || manifest.retired_through < 1 ||
        !Array.isArray(manifest.segments) || manifest.segments.length !== manifest.retired_through
    ) {
        throw new Error("Invalid retention manifest format or coverage");
    }

    const retentionDir = path.join(exchangeDir, "retention");
    const stat = fs.lstatSync(retentionDir);
    if (!stat.isDirectory()) throw new Error("Invalid retention directory");

    const eventsDir = path.join(exchangeDir, "events");

    for (let i = 0; i < manifest.segments.length; i++) {
        const entry = manifest.segments[i];
        if (
            entry.segment !== i + 1 ||
            !Number.isSafeInteger(entry.size) || entry.size < 0 ||
            !/^[a-f0-9]{64}$/.test(entry.sha256) ||
            !/^[a-f0-9]{64}$/.test(entry.sidecar_sha256)
        ) {
            throw new Error(`Invalid retention manifest entry for segment ${i + 1}`);
        }

        const sidecarPath = path.join(retentionDir, String(entry.segment).padStart(16, "0") + ".ids.json");
        const sidecarBytes = readRegular(sidecarPath);
        if (sha256(sidecarBytes) !== entry.sidecar_sha256) {
            throw new Error(`Retention sidecar checksum mismatch for segment ${entry.segment}`);
        }

        const sidecar = parseStrictJSON(sidecarBytes);
        if (
            sidecar.version !== 1 ||
            sidecar.segment !== entry.segment ||
            sidecar.size !== entry.size ||
            sidecar.sha256 !== entry.sha256 ||
            !Array.isArray(sidecar.records)
        ) {
            throw new Error(`Invalid retention sidecar metadata for segment ${entry.segment}`);
        }

        let currentOffset = 0;
        for (const record of sidecar.records) {
            if (
                !isSnowflake(record.message_id) || record.message_id === "0" ||
                !isSnowflake(record.channel_id) || record.channel_id === "0" ||
                record.offset !== currentOffset ||
                !Number.isSafeInteger(record.next_offset) ||
                record.next_offset <= currentOffset ||
                record.next_offset > sidecar.size
            ) {
                throw new Error(`Invalid identity/position in retention sidecar ${entry.segment}`);
            }
            currentOffset = record.next_offset;
        }

        if (currentOffset !== sidecar.size) {
            throw new Error(`Incomplete byte coverage in retention sidecar ${entry.segment}`);
        }

        // If raw file still exists (before --delete-certified), verify byte-for-byte fidelity
        const rawPath = path.join(eventsDir, padSegmentNumber(entry.segment));
        if (fs.existsSync(rawPath)) {
            const rawBytes = readRegular(rawPath);
            if (sha256(rawBytes) !== entry.sha256 || rawBytes.length !== entry.size) {
                throw new Error(`Retired raw segment differs from retention manifest in segment ${entry.segment}`);
            }
            for (const record of sidecar.records) {
                if (rawBytes[record.next_offset - 1] !== 10) {
                    throw new Error(`Missing newline delimiter in raw segment ${entry.segment}`);
                }
                const rawLine = JSON.parse(rawBytes.subarray(record.offset, record.next_offset - 1).toString("utf8"));
                if (
                    rawLine.version !== 1 ||
                    rawLine.event !== "message_create" ||
                    rawLine.message_id !== record.message_id ||
                    rawLine.channel_id !== record.channel_id
                ) {
                    throw new Error(`Retired sidecar identity differs from raw record in segment ${entry.segment}`);
                }
            }
        }

        result.set(entry.segment, sidecar);
    }

    return result;
}

/**
 * Validates the physical journal topology against retention manifest.
 * Enforces contiguous sequence and suffix invariants.
 * @param {string} eventsDir
 * @param {Map<number, object>} retiredEvidence
 * @returns {string[]} Sorted physical segment file names
 */
export function validateJournalTopology(eventsDir, retiredEvidence) {
    if (!fs.existsSync(eventsDir)) {
        if (retiredEvidence.size > 0) throw new Error("Missing journal events directory with retired evidence");
        return [];
    }

    const files = fs.readdirSync(eventsDir)
        .filter(f => /^\d{16}\.ndjson$/.test(f))
        .sort();

    const retiredCount = retiredEvidence.size;
    const first = files.length ? Number(files[0].slice(0, 16)) : 1;

    if (retiredCount > 0) {
        if (first > retiredCount + 1) {
            throw new Error(`Uncertified journal prefix gap: files start at ${first}, retired through ${retiredCount}`);
        }
        if (!files.length || Number(files.at(-1).slice(0, 16)) <= retiredCount) {
            throw new Error(`Active segment must be strictly beyond retired prefix (${retiredCount})`);
        }
    }

    for (let i = 0; i < files.length; i++) {
        const expectedName = padSegmentNumber(first + i);
        if (files[i] !== expectedName || !fs.lstatSync(path.join(eventsDir, files[i])).isFile()) {
            throw new Error(`Invalid journal segment sequence or non-regular file: expected ${expectedName}, got ${files[i]}`);
        }
    }

    return files;
}

/**
 * Unified journal record scanner across certified retired sidecars and raw NDJSON segments.
 * @param {string} exchangeDir
 * @param {{segment: number, offset: number}} start
 * @param {{segment: number, offset: number}} end
 * @param {Map<number, object>} retiredEvidence
 * @param {(record: object, position: {segment: number, offset: number}) => void} visitFn
 */
export function scanJournalRecords(exchangeDir, start, end, retiredEvidence, visitFn) {
    if (start.segment > end.segment || (start.segment === end.segment && start.offset > end.offset)) {
        throw new Error("Journal scan start boundary is beyond end boundary");
    }

    const eventsDir = path.join(exchangeDir, "events");
    const seen = new Set();

    for (let segment = start.segment; segment <= end.segment; segment++) {
        const offset = segment === start.segment ? start.offset : 0;
        const sidecar = retiredEvidence.get(segment);

        if (sidecar) {
            const limit = segment === end.segment ? end.offset : sidecar.size;
            if (offset > limit) throw new Error(`Scan offset ${offset} exceeds sidecar limit ${limit} in segment ${segment}`);

            for (const record of sidecar.records) {
                if (record.offset < offset || record.offset >= limit) continue;
                if (seen.has(record.message_id)) throw new Error(`Duplicate message ID ${record.message_id} in journal history`);
                seen.add(record.message_id);
                visitFn({ version: 1, event: "message_create", ...record }, { segment, offset: record.offset });
            }
            continue;
        }

        const filePath = path.join(eventsDir, padSegmentNumber(segment));
        if (segment === end.segment && end.offset === 0 && offset === 0 && !fs.existsSync(filePath)) {
            // Uncreated empty segment boundary is allowed
            continue;
        }

        const rawBytes = readRegular(filePath);
        const limit = segment === end.segment ? end.offset : rawBytes.length;
        if (limit > rawBytes.length || offset > limit || (offset > 0 && rawBytes[offset - 1] !== 10)) {
            throw new Error(`Invalid journal record boundary at segment ${segment}, offset ${offset}`);
        }

        let cursor = offset;
        while (cursor < limit) {
            const newline = rawBytes.indexOf(10, cursor);
            if (newline < 0 || newline >= limit) {
                throw new Error(`Incomplete journal line in segment ${segment} at offset ${cursor}`);
            }

            let record;
            try {
                record = JSON.parse(rawBytes.subarray(cursor, newline).toString("utf8"));
            } catch {
                throw new Error(`Malformed journal JSON in segment ${segment} at offset ${cursor}`);
            }

            if (
                !record || record.version !== 1 || record.event !== "message_create" ||
                !isSnowflake(record.message_id) || record.message_id === "0" ||
                !isSnowflake(record.channel_id) || record.channel_id === "0"
            ) {
                throw new Error(`Invalid journal event schema in segment ${segment}`);
            }

            if (seen.has(record.message_id)) {
                throw new Error(`Duplicate message ID ${record.message_id} in journal segment ${segment}`);
            }
            seen.add(record.message_id);

            visitFn(record, { segment, offset: cursor });
            cursor = newline + 1;
        }
    }
}

/**
 * Calculates the first-watch lower exclusion bound from the latest visible message anchor H.
 * Includes H's entire Discord timestamp millisecond; excludes all prior milliseconds.
 * @param {string} anchorMessageId
 * @returns {string}
 */
export function deriveFirstWatchBoundary(anchorMessageId) {
    if (!anchorMessageId || anchorMessageId === "0") return "0";
    const anchor = BigInt(anchorMessageId);
    const millisecondStart = (anchor >> 22n) << 22n;
    return (millisecondStart > 0n ? millisecondStart - 1n : 0n).toString();
}
