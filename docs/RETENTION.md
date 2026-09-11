# Journal retention

Retention is explicit, offline maintenance. It does not run on a schedule or
enforce a disk quota. Keep enough free space for unconsumed messages and evidence.

## What is safe to remove?

Recovery can revisit old Discord IDs below its high-water mark. Removing an old
transcript without keeping its identities would let replay append those messages
again. Forward recovery checkpoints alone are not a safe deletion boundary.

Before a closed prefix can be deleted, maintenance creates an exact sidecar for
every segment, then atomically publishes a retention manifest. Sidecars retain
message/channel IDs and original record offsets, never message text. Collector
uses this evidence alongside the remaining journal for dedupe and recovery
validation. Core refuses cursors into the retired prefix.

Only a contiguous prefix is removable. The highest segment is protected; Core
must have committed into a later segment; no recovery transaction may be pending;
and digest citations must be independent of transcript reconstruction. Missing or
invalid state, sidecars, hashes, offsets, or topology stop maintenance.

Sidecars grow with accepted history. This reduces transcript storage, **not** total
storage to a fixed bound, and does not reduce REST replay work.

## Maintenance

Use compatible Core, Collector runtime, and setup builds. Stop both services and
make a verified, matching backup of all volumes first. Restoring old code after
retirement also requires compatible state and transcript evidence; swapping an
image alone is not a rollback plan. See [setup](SETUP.md).

The following commands are for Linux containers, from the repository root.
`N` means the last closed segment in the prefix you intend to retire, not a number
to copy literally. Do not advance Core's cursor or force a rotation to make it fit.

```sh
docker compose -f docker/compose.yml stop cordbrief-core cordbrief-collector
docker compose -f docker/compose.yml -f docker/compose.retention.yml run --rm cordbrief-collector bash /home/cordbrief/collector/retention-publish.sh N
```

The default command publishes durable evidence only. It validates recovery state,
Core's cursor and artifacts while holding both kernel locks. **Publication itself
changes logical retention:** Core stops treating that prefix as readable transcript
history, even while its files still exist. This is not a dry run.

Physical deletion is a separate, explicit invocation for that exact certified prefix:

```sh
docker compose -f docker/compose.yml -f docker/compose.retention.yml run --rm cordbrief-collector bash /home/cordbrief/collector/retention-publish.sh N --delete-certified
docker compose -f docker/compose.yml up -d
```

Deletion revalidates the evidence and consumption boundary. It syncs the evidence,
unlinks the oldest remaining certified file, and syncs the journal directory
before touching its successor. A failure stops the run; retry validates again.
Completed collection is idempotent. Never clear an error by manually deleting
state, pending transactions, or sidecars.

## Format and verification

`retention-manifest.json` in exchange names a version, `retired_through`, and each
segment's size, source SHA-256 and sidecar SHA-256. The immutable files under
`retention/<segment>.ids.json` contain version, segment, size, source digest, and
ordered `{message_id, channel_id, offset, next_offset}` records. Coverage must be
complete; when source bytes exist, readers also compare them with the sidecar.

Logical history stays contiguous from 1. Physical transcript files form a suffix;
certified-but-not-yet-deleted files are valid crash leftovers. An unexplained hole
or missing evidence is corruption, not permission to skip history.

Disposable Linux tests cover actual unlink, process death, directory-sync errors,
lock contention, restart, replay and Core continuity. They do not establish
physical power-cut behavior. Production deletion has not been exercised.
