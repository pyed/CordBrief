import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import assert from "assert";

console.log("=== Running Enhanced Collector Native & Watchlist Tests ===");

const testDir = fs.mkdtempSync(path.join(os.tmpdir(), "collector-test-"));
const exchangeDir = path.join(testDir, "exchange");
const eventsDir = path.join(exchangeDir, "events");

fs.mkdirSync(eventsDir, { recursive: true });

// 1. Test Dynamic Watchlist Reload & Fail-Closed Transitions
console.log("[Test 1] Dynamic Watchlist Reload & Fail-Closed Transition...");

let cachedWl = { valid: false, generation: -1, channel_ids: [] };
let lastMtime = 0;

function reloadWatchlist(filePath) {
    if (!fs.existsSync(filePath)) {
        cachedWl = { valid: false, generation: -1, channel_ids: [] };
        return cachedWl;
    }
    const stat = fs.statSync(filePath);
    if (cachedWl.valid && stat.mtimeMs === lastMtime) {
        return cachedWl;
    }
    try {
        const raw = fs.readFileSync(filePath, "utf8");
        const parsed = JSON.parse(raw);
        if (
            typeof parsed !== "object" || parsed === null ||
            parsed.version !== 1 ||
            typeof parsed.generation !== "number" || parsed.generation < 0 ||
            !Array.isArray(parsed.channel_ids)
        ) {
            cachedWl = { valid: false, generation: -1, channel_ids: [] };
            lastMtime = stat.mtimeMs;
            return cachedWl;
        }
        const validIds = new Set();
        for (const item of parsed.channel_ids) {
            if (typeof item === "string" && item.trim().length > 0) {
                validIds.add(item.trim());
            } else {
                cachedWl = { valid: false, generation: -1, channel_ids: [] };
                lastMtime = stat.mtimeMs;
                return cachedWl;
            }
        }
        if (validIds.size === 0) {
            cachedWl = { valid: false, generation: parsed.generation, channel_ids: [] };
            lastMtime = stat.mtimeMs;
            return cachedWl;
        }
        lastMtime = stat.mtimeMs;
        cachedWl = {
            valid: true,
            generation: parsed.generation,
            channel_ids: Array.from(validIds).sort()
        };
        return cachedWl;
    } catch {
        cachedWl = { valid: false, generation: -1, channel_ids: [] };
        return cachedWl;
    }
}

const wlFile = path.join(exchangeDir, "watchlist.json");

// A. Initial write: Generation 1 with channels A, B
fs.writeFileSync(wlFile, JSON.stringify({ version: 1, generation: 1, channel_ids: ["chan_b", "chan_a"] }));
let wl = reloadWatchlist(wlFile);
assert.strictEqual(wl.valid, true);
assert.strictEqual(wl.generation, 1);
assert.deepStrictEqual(wl.channel_ids, ["chan_a", "chan_b"]);

// B. Dynamic update: Generation 2 with channels B, C
// Ensure mtime changes
fs.utimesSync(wlFile, new Date(Date.now() + 1000), new Date(Date.now() + 1000));
fs.writeFileSync(wlFile, JSON.stringify({ version: 1, generation: 2, channel_ids: ["chan_c", "chan_b"] }));
fs.utimesSync(wlFile, new Date(Date.now() + 2000), new Date(Date.now() + 2000));
wl = reloadWatchlist(wlFile);
assert.strictEqual(wl.valid, true);
assert.strictEqual(wl.generation, 2);
assert.deepStrictEqual(wl.channel_ids, ["chan_b", "chan_c"]);

// C. Critical Security Test: Watchlist becomes corrupt/invalid -> FAILS CLOSED IMMEDIATELY
// Must NOT continue using previous generation 2 channels!
fs.writeFileSync(wlFile, "{ malformed json");
fs.utimesSync(wlFile, new Date(Date.now() + 3000), new Date(Date.now() + 3000));
wl = reloadWatchlist(wlFile);
assert.strictEqual(wl.valid, false, "Corrupt watchlist must transition to valid=false");
assert.strictEqual(wl.channel_ids.length, 0, "Corrupt watchlist must yield zero allowed channels");

// D. Recovery: Watchlist restored with Generation 3
fs.writeFileSync(wlFile, JSON.stringify({ version: 1, generation: 3, channel_ids: ["chan_x"] }));
fs.utimesSync(wlFile, new Date(Date.now() + 4000), new Date(Date.now() + 4000));
wl = reloadWatchlist(wlFile);
assert.strictEqual(wl.valid, true);
assert.strictEqual(wl.generation, 3);
assert.deepStrictEqual(wl.channel_ids, ["chan_x"]);

console.log("  ✔ Dynamic reload & fail-closed transition verified.");

// 2. Test Segment Resume Across Restarts & Rotation
console.log("[Test 2] Segment Resume Across Restarts & Rotation...");

const MAX_SEG_SIZE = 220;
let curSeg = 1;
let curPath = "";
let curSize = 0;

function padSeg(n) { return String(n).padStart(16, "0") + ".ndjson"; }

function initWriter() {
    const files = fs.readdirSync(eventsDir).filter(f => /^\d{16}\.ndjson$/.test(f)).sort();
    if (files.length === 0) {
        curSeg = 1;
    } else {
        const last = files[files.length - 1];
        curSeg = parseInt(last.replace(".ndjson", ""), 10);
    }
    curPath = path.join(eventsDir, padSeg(curSeg));
    if (fs.existsSync(curPath)) {
        repairTrailing(curPath);
        const stat = fs.statSync(curPath);
        curSize = stat.size;
        if (curSize >= MAX_SEG_SIZE) {
            curSeg++;
            curPath = path.join(eventsDir, padSeg(curSeg));
            curSize = 0;
        }
    } else {
        curSize = 0;
    }
}

function repairTrailing(filePath) {
    const stat = fs.statSync(filePath);
    if (stat.size === 0) return;
    const fd = fs.openSync(filePath, "r+");
    try {
        const buf = Buffer.alloc(stat.size);
        fs.readSync(fd, buf, 0, stat.size, 0);
        if (buf[stat.size - 1] === 0x0A) return; // Clean newline
        let lastNl = -1;
        for (let i = stat.size - 2; i >= 0; i--) {
            if (buf[i] === 0x0A) { lastNl = i; break; }
        }
        const cleanLen = lastNl >= 0 ? lastNl + 1 : 0;
        fs.ftruncateSync(fd, cleanLen);
    } finally {
        fs.closeSync(fd);
    }
}

function writeRecord(msgId, content) {
    const line = JSON.stringify({ version: 1, event: "message_create", message_id: msgId, content }) + "\n";
    const buf = Buffer.from(line, "utf8");
    if (curSize > 0 && (curSize + buf.length) > MAX_SEG_SIZE) {
        curSeg++;
        curPath = path.join(eventsDir, padSeg(curSeg));
        curSize = 0;
    }
    fs.appendFileSync(curPath, buf);
    curSize += buf.length;
}

// Write 2 records in segment 1 (83 + 84 = 167 bytes <= 220)
initWriter();
writeRecord("m1", "First record");
writeRecord("m2", "Second record");
assert.strictEqual(curSeg, 1, "m1 and m2 must be in segment 1");

// Simulate Restart 1: Writer discovers segment 1 and resumes appending
initWriter();
assert.strictEqual(curSeg, 1, "Writer must resume on segment 1");
writeRecord("m3", "Third record - triggers rotation to seg 2");
assert.strictEqual(curSeg, 2, "Writing m3 should have rotated to segment 2");
writeRecord("m4", "Fourth record");
assert.strictEqual(curSeg, 2, "Writing m4 should stay in segment 2");

// Simulate Restart 2: Writer discovers segment 2 and resumes
initWriter();
assert.strictEqual(curSeg, 2, "Writer must resume on segment 2");

const allSegFiles = fs.readdirSync(eventsDir).filter(f => f.endsWith(".ndjson")).sort();
assert.deepStrictEqual(allSegFiles, ["0000000000000001.ndjson", "0000000000000002.ndjson"]);
console.log("  ✔ Segment resume across restarts and rotation verified.");

// 3. Test Crash Recovery on Torn Trailing Record
console.log("[Test 3] Crash Recovery on Torn Trailing Record...");

// Intentionally append half a record without newline to segment 2
const tornBytes = Buffer.from('{"version":1,"event":"message_create","message_id":"torn_msg"', "utf8");
fs.appendFileSync(curPath, tornBytes);

const sizeWithTorn = fs.statSync(curPath).size;
assert(sizeWithTorn > curSize, "Torn record bytes present");

// Simulate Restart after crash: initWriter must detect torn record and truncate back to clean boundary
initWriter();

const sizeAfterRepair = fs.statSync(curPath).size;
assert(sizeAfterRepair < sizeWithTorn, "Torn record bytes must be truncated");

// Append new record after recovery
writeRecord("m6", "Sixth record after recovery");

// Verify that all lines in segment 2 are 100% valid JSON records
const seg2Lines = fs.readFileSync(curPath, "utf8").trim().split("\n");
for (const line of seg2Lines) {
    const parsed = JSON.parse(line);
    assert.strictEqual(parsed.version, 1);
    assert(parsed.message_id !== "torn_msg", "Torn message was pruned cleanly");
}
assert(seg2Lines.some(l => JSON.parse(l).message_id === "m6"), "Record m6 was appended cleanly");

console.log("  ✔ Crash recovery on torn record verified (zero record merging).");

// 4. Test Catalog Validation, Deduplication & Atomic Publication
console.log("[Test 4] Catalog Validation, Deduplication & Atomic Publication...");

const catalogFile = path.join(exchangeDir, "catalog.json");
let lastCatalogContentHash = "";

function safeReplaceJSON(destinationPath, data) {
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

function publishCatalog(guilds) {
    if (!Array.isArray(guilds)) {
        return false;
    }
    const validatedGuilds = [];
    for (const g of guilds) {
        if (!g || typeof g.id !== "string" || !g.id || typeof g.name !== "string") continue;
        const validChannels = [];
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
        validChannels.sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id));
        validatedGuilds.push({
            id: g.id,
            name: g.name,
            channels: validChannels
        });
    }
    validatedGuilds.sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id));

    const canonicalPayload = JSON.stringify(validatedGuilds);
    if (canonicalPayload === lastCatalogContentHash && fs.existsSync(catalogFile)) {
        return true; // deduplicated, no write
    }

    const record = {
        version: 1,
        updated_at: new Date().toISOString(),
        guilds: validatedGuilds
    };
    safeReplaceJSON(catalogFile, record);
    lastCatalogContentHash = canonicalPayload;
    return true;
}

// A. Publish valid catalog
const sampleGuilds = [
    {
        id: "g2",
        name: "Beta Guild",
        channels: [
            { id: "c2", name: "general", type: 0 },
            { id: "c1", name: "announcements", type: 5 },
            { id: "c_hid1", name: "___hidden___", type: 0 },
            { id: "c_hid2", name: "__hidden__", type: 0 },
            { id: "c_legit", name: "📡  Hidden Signal  📡", type: 0 }
        ]
    },
    {
        id: "g1",
        name: "Alpha Guild",
        channels: [
            { id: "c3", name: "chat", type: 0 }
        ]
    }
];

const ok = publishCatalog(sampleGuilds);
assert.strictEqual(ok, true, "Publish must succeed");
assert(fs.existsSync(catalogFile), "catalog.json must exist");

const parsedCat = JSON.parse(fs.readFileSync(catalogFile, "utf8"));
assert.strictEqual(parsedCat.version, 1);
assert.strictEqual(parsedCat.guilds.length, 2);
// Verified deterministic sorting: Alpha Guild before Beta Guild
assert.strictEqual(parsedCat.guilds[0].name, "Alpha Guild");
assert.strictEqual(parsedCat.guilds[1].name, "Beta Guild");
// Verified channel sorting: 📡  Hidden Signal  📡, announcements, general (and ___hidden___ / __hidden__ are filtered out!)
assert.strictEqual(parsedCat.guilds[1].channels.length, 3, "Sentinels filtered, legitimate channels kept");
assert.strictEqual(parsedCat.guilds[1].channels[0].name, "📡  Hidden Signal  📡");
assert.strictEqual(parsedCat.guilds[1].channels[1].name, "announcements");
assert.strictEqual(parsedCat.guilds[1].channels[2].name, "general");

// B. Deduplication: second publish of identical content does NOT rewrite
const statBefore = fs.statSync(catalogFile);
const ok2 = publishCatalog(sampleGuilds);
assert.strictEqual(ok2, true);
const statAfter = fs.statSync(catalogFile);
assert.strictEqual(statBefore.mtimeMs, statAfter.mtimeMs, "Deduplication must avoid rewrite when unchanged");

// C. Malformed payload rejected without corrupting existing catalog
const okFail = publishCatalog("not an array");
assert.strictEqual(okFail, false, "Malformed payload must fail");
const catIntact = JSON.parse(fs.readFileSync(catalogFile, "utf8"));
assert.strictEqual(catIntact.version, 1, "Catalog remains valid after bad payload");
console.log("  ✔ Catalog validation, deduplication & atomic publication verified.");

// Cleanup
fs.rmSync(testDir, { recursive: true, force: true });
console.log("=== All Enhanced Collector Tests: PASSED ===\n");
