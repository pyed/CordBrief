// Offline evidence publication; physical deletion requires --delete-certified
// and an already committed manifest. Both operations hold runtime + Core leases.
import fs from "node:fs";
import path from "node:path";
import crypto from "node:crypto";
import { stripTypeScriptTypes } from "node:module";

const fail = message => { throw new Error(message); };
const env = name => process.env[name] || fail(`Required: ${name}`);
const hash = bytes => crypto.createHash("sha256").update(bytes).digest("hex");
const number = n => Number.isSafeInteger(n) && n >= 0;
const name = n => String(n).padStart(16, "0");
function regular(file) {
    if (!fs.lstatSync(file).isFile()) fail("Expected regular file");
    return fs.readFileSync(file);
}
function syncDir(dir) { const fd = fs.openSync(dir, "r"); try { fs.fsyncSync(fd); } finally { fs.closeSync(fd); } }
function locked(fd, file) {
    const held = fs.fstatSync(fd), expected = fs.lstatSync(file);
    if (!expected.isFile() || held.ino !== expected.ino || held.dev !== expected.dev ||
        !/lock:\s+\d+:\s+FLOCK\s+ADVISORY\s+WRITE/.test(fs.readFileSync(`/proc/self/fdinfo/${fd}`, "utf8"))) fail("Required exclusive lock is not owned");
}
function write(file, bytes) {
    const temporary = `${file}.tmp-${process.pid}-${crypto.randomUUID()}`;
    const fd = fs.openSync(temporary, "wx", 0o600);
    try { fs.writeFileSync(fd, bytes); fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
    if (!regular(temporary).equals(bytes)) fail("Evidence readback mismatch");
    fs.renameSync(temporary, file);
    syncDir(path.dirname(file));
}
function artifacts(core) {
    const dir = path.join(core, "digests");
    // Missing artifact directory is unknown, not proof of an empty inventory.
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
        if (!entry.isFile() || !/^[a-f0-9]{64}\.json$/.test(entry.name)) fail("Unknown artifact inventory entry");
        const artifact = JSON.parse(regular(path.join(dir, entry.name)));
        if (artifact.version !== 1 || artifact.batch_id !== entry.name.slice(0, -5) || !artifact.digest || typeof artifact.digest !== "object" || Array.isArray(artifact.digest) || (artifact.digest.items != null && !Array.isArray(artifact.digest.items))) fail("Invalid artifact");
        for (const item of artifact.digest.items ?? []) {
            if (!item || typeof item !== "object" || (item.source_ids != null && !Array.isArray(item.source_ids))) fail("Invalid artifact citations");
            for (const id of item.source_ids ?? []) {
                if (typeof id !== "string") fail("Invalid citation ID");
                if (!id.trim()) continue;
                const ref = artifact.source_refs?.[id.trim()];
                if (!ref || ![ref.guild_id, ref.channel_id, ref.message_id].every(value => typeof value === "string" && /^[0-9]{1,32}$/.test(value.trim()))) fail("Artifact still needs journal citations");
            }
        }
    }
}

try {
    if (process.platform !== "linux") fail("Retention publication requires Linux kernel locks");
    const exchange = env("CORDBRIEF_EXCHANGE_DIR"), core = env("CORDBRIEF_CORE_DATA_DIR");
    locked(9, path.join(env("CORDBRIEF_RUNTIME_DIR"), "runtime.lock"));
    locked(8, path.join(core, "commit.lock"));
    const through = Number(process.argv[2]);
    if (!number(through) || through < 1) fail("Expected positive retired-through segment");
    const deleting = process.argv[3] === "--delete-certified";
    if (process.argv.length > 4 || (process.argv[3] && !deleting)) fail("Usage: <through> [--delete-certified]");
    process.env.CORDBRIEF_RETENTION_INSPECT = "1";
    process.env.CORDBRIEF_RECOVERY_STATE_PATH = path.join(env("CORDBRIEF_COLLECTOR_DATA_DIR"), "recovery-state.json");
    const source = fs.readFileSync(new URL("./plugin/native.ts", import.meta.url), "utf8").replace('import { IpcMainInvokeEvent } from "electron";', "");
    const native = await import(`data:text/javascript;base64,${Buffer.from(stripTypeScriptTypes(source)).toString("base64")}`);
    native.inspectRetentionState();
    const manifestPath = path.join(exchange, "retention-manifest.json");
    const previous = fs.existsSync(manifestPath) ? JSON.parse(regular(manifestPath)) : { version: 1, retired_through: 0, segments: [] };
    if (through < previous.retired_through) fail("Cannot rewind retention coverage");
    if (deleting && through !== previous.retired_through) fail("Deletion requires exactly the committed certified prefix");
    const ack = JSON.parse(regular(path.join(exchange, "core-ack.json")));
    if (!ack || Object.keys(ack).sort().join(",") !== "offset,segment,version" || ack.version !== 1 || !number(ack.segment) || ack.segment <= through || !number(ack.offset)) fail("Core has not consumed candidate prefix");
    const ackBytes = regular(path.join(exchange, "events", `${name(ack.segment)}.ndjson`));
    if (ack.offset > ackBytes.length || (ack.offset > 0 && ackBytes[ack.offset - 1] !== 10)) fail("Core cursor is not a record boundary");
    artifacts(core);
    const evidenceDir = path.join(exchange, "retention");
    if (fs.existsSync(evidenceDir) && !fs.lstatSync(evidenceDir).isDirectory()) fail("Invalid retention directory");
    if (deleting) {
        const events = path.join(exchange, "events");
        if (!fs.lstatSync(events).isDirectory()) fail("Invalid events directory");
        // A previous publisher may have died after rename but before directory
        // fsync. Establish durable authority again before the first unlink.
        for (const descriptor of previous.segments) syncDir(path.join(evidenceDir, `${name(descriptor.segment)}.ids.json`));
        syncDir(evidenceDir);
        syncDir(manifestPath);
        syncDir(exchange);
        // Also finish durability of a previous interrupted unlink before moving on.
        syncDir(events);
        const physical = fs.readdirSync(events).filter(f => /^\d{16}\.ndjson$/.test(f)).sort();
        const highest = Number(physical.at(-1)?.slice(0, 16));
        if (!number(highest) || through >= highest) fail("Active segment cannot be deleted");
        let deleted = 0;
        for (const filename of physical) {
            const segment = Number(filename.slice(0, 16));
            if (segment > through) break;
            const target = path.join(events, filename);
            const descriptor = previous.segments[segment - 1];
            if (!descriptor || hash(regular(target)) !== descriptor.sha256) fail("Certified transcript changed before unlink");
            // Full preflight proved a contiguous suffix. Ascending unlink plus a
            // directory fsync before EACH successor keeps that true after crashes.
            fs.unlinkSync(target);
            syncDir(events);
            deleted++;
        }
        console.log(JSON.stringify({ retired_through: through, deleted }));
    } else {
        function project(segment) {
            const bytes = regular(path.join(exchange, "events", `${name(segment)}.ndjson`));
            let offset = 0;
            const records = [];
            while (offset < bytes.length) {
                const newline = bytes.indexOf(10, offset);
                if (newline < 0) fail("Closed journal has a torn tail");
                const event = JSON.parse(bytes.subarray(offset, newline).toString("utf8"));
                if (event.version !== 1 || event.event !== "message_create" || ![event.message_id, event.channel_id].every(id => typeof id === "string" && /^[1-9][0-9]*$/.test(id))) fail("Unknown journal record");
                records.push({ message_id: event.message_id, channel_id: event.channel_id, offset, next_offset: newline + 1 });
                offset = newline + 1;
            }
            const sidecar = { version: 1, segment, size: bytes.length, sha256: hash(bytes), records };
            const encoded = Buffer.from(JSON.stringify(sidecar) + "\n");
            return { encoded, descriptor: { segment, size: bytes.length, sha256: sidecar.sha256, sidecar_sha256: hash(encoded) } };
        }
        const descriptors = [...previous.segments];
        // Two passes keep transcript/identity working memory bounded to one segment.
        for (let segment = previous.retired_through + 1; segment <= through; segment++) {
            const { encoded, descriptor } = project(segment);
            const target = path.join(evidenceDir, `${name(segment)}.ids.json`);
            if (fs.existsSync(target) && !regular(target).equals(encoded)) fail("Existing sidecar disagrees with exact journal projection");
            descriptors.push(descriptor);
        }
        // All validation precedes publication. Orphan sidecars from interrupted attempts are harmless.
        fs.mkdirSync(evidenceDir, { recursive: true, mode: 0o700 });
        syncDir(exchange);
        for (let segment = previous.retired_through + 1; segment <= through; segment++) {
            const { encoded } = project(segment);
            if (hash(encoded) !== descriptors[segment - 1].sidecar_sha256) fail("Journal changed despite exclusive ownership");
            const target = path.join(evidenceDir, `${name(segment)}.ids.json`);
            if (fs.existsSync(target)) {
                if (!regular(target).equals(encoded)) fail("Existing sidecar disagrees with exact journal projection");
                syncDir(target); // Reused orphan bytes also need durable storage before authority commits.
            } else write(target, encoded);
        }
        syncDir(evidenceDir); // Includes a rename whose preceding process died before directory fsync.
        write(manifestPath, Buffer.from(JSON.stringify({ version: 1, retired_through: through, segments: descriptors }) + "\n"));
        console.log(JSON.stringify({ published_through: through, sidecars: descriptors.length, deleted: 0 }));
    }
} catch (error) {
    console.error(`Retention publication refused: ${error.message}`);
    process.exitCode = 1;
}
