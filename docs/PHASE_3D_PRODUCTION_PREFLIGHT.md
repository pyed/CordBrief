# Phase 3D production preflight — correctly refused mutation

Phase 3D implementation is complete and checkpointed by annotated milestone
`m13-journal-retention`. This is an implementation/testing milestone, not a claim
of production deployment or production deletion. Production deletion remains
unexercised because no eligible closed segment existed.

Read-only observation on 2026-09-08, before deployment or maintenance:

| Evidence | Observed |
|---|---|
| Journal segments | Only `0000000000000001.ndjson` |
| Segment 1 bytes | 25,121 |
| Active segment | 1 |
| Core committed cursor | `(1, 9451)` |
| Bytes beyond Core cursor | 15,670 |
| Recovery schema / channels | v2 / 2 |
| Pending transaction / active sweeps | None / none |
| Collector state | Running, recovery ready, no recovery error |
| Retention manifest | Absent |
| Core / Collector containers | Both healthy |
| Digest artifact count | 3 |

There is no closed, Core-consumed prefix. Segment 1 fails both `S < H` and
`S < CoreAck.segment`. A snapshot of running state is sufficient to reject this
attempt, never to authorize deletion. No positive eligibility is claimed.

Production remains on the existing Phase 3C deployment. No image build, runtime
activation, service restart, manifest publication, journal rotation, cursor
advancement, deletion, or production backup mutation was performed in this
deployment attempt. The implementation was subsequently committed and tagged at
the user's request; production remains unchanged and no remote push was made.

Controlled production deletion must wait for a real closed segment and a Core
commit into a later segment. Do not advance the cursor merely to satisfy GC or
force transcript processing/delivery as a retention test. At that point repeat
locked eligibility validation, take and verify a stopped-source rollback backup,
deploy compatible readers, and perform the smallest certified deletion. Existing
disposable deletion proofs do not substitute for the requested production proof.
