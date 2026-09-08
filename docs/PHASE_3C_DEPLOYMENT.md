# M13 Phase 3C production completion — 2026-09-08

Phase 3C recovery v2 is deployed. Milestone tag: `m13-recovery-v2`.
The previous annotated checkpoint `m13-durable-outbox` remains at
`27196c587202c66c7972b6ad04cf424927b7050f`. No remote push or journal GC occurred.

## What was deployed

The current collector plugin was compiled in the cached setup toolchain into
`cordbrief-setup:m13-phase3c` (also tagged locally as `docker-cordbrief-setup:latest`).
Image ID: `sha256:e9204f97c09b00778a65f1586779f8f41426513afbf9ea96813ad08026025bee`.
The collector and Core container images were unchanged. The production runtime
release changed from `20260906013759` to `20260908001305`.

The existing `createRuntimeRelease` and atomic activation functions staged the
new compiled Vencord/plugin using the previous prepared Discord 1.0.156 binaries.
Setup maintenance held the real FD 9 kernel flock. No runtime release was pruned.
The previous runtime still validates and remains present. The setup default image
tag now points to the new build so later maintenance will use the current plugin.

The runtime plugin source hashes matched the tested working files:

- native.ts: `0294f28a3a7f6dc21970f8042d5223f3becc73fd7e32044dbe244b5f40788a72`
- index.ts: `8cd61ace18c0b7c9068c2d02e73c8d7731c601006447b4aed0ba26d9c212ca57`

## Corrected rehearsal and production migration

The preceding preflight report overstated its journal validation. Its copy put
events under `root/events`, while its loader used `root/exchange/events`. Before
deployment, the rehearsal was repeated with the correct layout and actual native
loader. Both checkpoints were preserved and copied journal bytes stayed identical.
The existing harness uses a tiny rotation threshold; the subsequent production
loader used the normal threshold and confirmed boundary `(1,9451)`.

Core and collector were stopped for a consistent backup. After staging, the real
native loader ran against production state under the setup flock, with networking
disabled and no Discord/profile volume mounted. It migrated v1 to v2:

- Two channels; pending null before and after.
- Both checkpoint IDs preserved.
- Provenance `legacy`, `watch_after=0`, null sweep bounds, physical floors `(1,0)`.
- Second load byte-identical; journal unchanged; no tail repair required.

| Evidence | SHA-256 |
|---|---|
| Original v1 state | `8a42c3157af6d909f5675e2e3c7990210843386e949bec6d850dbcef5633d0bf` |
| Migrated v2 state before replay | `54619e1aa0586efc60b7b8133ea0990f75943178c21621c8bc5401dbb00c3051` |
| Original journal, 9,451 bytes | `1bd804634380d3e923129ca65847ac701af48be1e6c976f2c156405a8d09f881` |
| Journal after replay, 25,121 bytes | `6c6f3ad43ed7c29a8506e841a6eea1f84df045ff6e4fee1ea880f5140e42d240` |

## Production replay and restart evidence

Collector started at 00:13:40 UTC and performed real authenticated REST replay.
The original 21 journal records became 62 records with 62 unique message IDs.
All original bytes remained an exact prefix. The 41 new messages were historical
replay below the preserved checkpoints; neither high-water advanced. Per-channel
recovered counters changed from 2/2 to 23/22. Both sweeps completed successfully,
with pending null and no active sweep.

Core was then resumed. Collector was restarted once more. Fresh real sweeps after
restart completed with the same 62 records and byte-identical journal hash. Both
containers were healthy; collector reported normal/running/authenticated/ready,
zero pending channels, and no recovery error. PID 1 retained the runtime FD 9
`FLOCK ADVISORY WRITE` record. Collector publishes no ports; Core remains on
`127.0.0.1:28741`.

Core's actual `exchange ingest` dry run read all 41 new records, from `(1,9451)` to
`(1,25121)`, without committing a cursor. Core advances its durable cursor on digest
commit, not merely because collector appended data. The 41 records therefore
remain legitimate backlog for the next digest. No digest/Telegram/LLM action was
forced for this proof. Inbox and all three existing digest pages returned HTTP
200. All three digest artifacts, both delivery records, and scheduler state parsed
and were byte-identical to the predeployment backup.

No newly posted Gateway message was induced or independently observed in this
window. Live Gateway races remain actual-code process-tested; the production
evidence here is authenticated REST replay, restart deduplication and continuity.

## Backup and rollback path

Private Docker volume `cordbrief_m13_3c_rollback_20260908` contains mode-0600 tar
archives under a mode-0700 root. Source volumes were mounted read-only while both
services were stopped. Every archive was compared to its source with `tar -d`.
All six archives were also extracted into the isolated
`cordbrief_m13_3c_restorecheck_20260908` volume and compared again successfully.
The restore-check volume was removed after verification; the rollback volume is
retained, including all original archives and the post-replay evidence below.

| Archive / original volume | SHA-256 |
|---|---|
| core.tar / cordbrief_core_data | `e0bbbe835a19a8174bbd9546687de2b5606bf6e380db00e44c27bf947ef3978e` |
| collector.tar / cordbrief_collector_data | `0e94a363e8137aa6911c60440f05cfdf04d9ba00bc19aad601e6b466dcc40a02` |
| exchange.tar / cordbrief_exchange | `c9e2a3089ca508d517095dce206fa014773425e431aef70b5bc598ee1fd107c5` |
| runtime.tar / cordbrief_discord_runtime | `37d8a7ded349ce5171636affa9ecded6b2763b6bb01489dd94e5fdf68da1cb80` |
| profile.tar / cordbrief_discord_profile | `6a0495305234bae836685a9702fda380e0be10f5312a822871bc592cf019a0dd` |
| keyring.tar / cordbrief_discord_keyring | `21411d3a3a37ef0fab1646af2dd0c2c53c4931847eb36ac766b68c4a33180782` |

Rollback image tags are retained locally:
`cordbrief-setup:m13-durable-outbox-rollback`,
`cordbrief-collector:m13-durable-outbox-rollback`, and
`cordbrief-core:m13-durable-outbox-rollback`.

The rollback volume also contains `after-replay-journal.ndjson` (all 62 records)
and `after-replay-recovery-v2.json`, preserving the newly recovered evidence.

A rollback must stop both services and preserve a fresh copy of all current state
first. Restore the six matched archives into fresh replacement volumes, verify
their contents and previous runtime, and attach the old images to those volumes.
Do not simply run old recovery code over v2 state or overwrite current data with
old archives. The snapshot restores the previous point in time; later records and
external delivery effects must be reconciled separately before resuming processing.
The preserved post-replay journal protects this deployment's newly recovered data.
Filesystem restoration was tested; an authenticated old-runtime rollback was not
performed because production is healthy.

Cleanup note: automatic approval review rejected recursive removal of the local
rehearsal directory without a more specific reason. That disposable copy remains
at `C:\Users\Sheriff\AppData\Local\Temp\cordbrief-deploy-f75ef7bf1b86470d849c260b11f47b19`.
It is outside the repository and contains copied journal/recovery data; no
alternate deletion mechanism was used to bypass the rejection.

## Final validation and limitations

The full Windows and Linux development bars passed before deployment on the same
production source hashes. After deployment, the new setup image passed Linux
pending-bounds, 20 cross-field invariant cases, first-watch acceptance,
migration/replay, `gc_readiness_test.mjs --require-safe` (101/250/1050 IDs with real
rotation), all 19 Linux retention cases, and adversarial locking/maintenance.
Go tests passed in all 14 packages, vet passed, and `go list -m all` was exactly
`cordbrief`. No new dependencies or production lock changes were introduced.

Recovery's accepted limits remain: unavailable undurable messages cannot be
reconstructed; conservative migration/uncertain initialization may import old
history; replay costs grow with retained history; permanently changed pending
pages can require intervention; no power-cut durability test was performed.
Historical replay still consumes journal history, so this milestone is recovery
completion, not permission to implement or run destructive GC.
