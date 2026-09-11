# Official Discord RPC probe

This is experimental evidence from a disposable Windows probe. It documents
what the official Discord desktop client and documented local RPC/OAuth flow
allowed during one successful account/application run. It is not a production
collector design and does not change CordBrief's current collector.

## Authentication and RPC path

- Windows Discord desktop client, documented IPC v1 over `discord-ipc-0`.
- IPC handshake completed with `READY`.
- A masked local prompt collected the current Client Secret in memory before
  `AUTHORIZE`; it was never printed or saved.
- `AUTHORIZE` requested `rpc`, `identify`, and `messages.read` with the exact
  registered loopback redirect URI `http://127.0.0.1:32145/callback`.
- The fresh callback code was exchanged immediately at Discord's documented
  [`/oauth2/token` endpoint](https://discord.com/api/oauth2/token). No
  authorization code or token value was saved.
- `AUTHENTICATE` succeeded and confirmed the requested scopes. Discord did not
  refuse the scopes or application access.

## Read measurements

The successful authenticated session observed:

| RPC command | Observation |
| --- | --- |
| `GET_GUILDS` | 4 guilds returned |
| `GET_CHANNELS` | 47 channels returned for one sampled guild |
| `GET_CHANNEL` | 30 messages returned for one sampled text channel |
| `SUBSCRIBE` for `MESSAGE_CREATE` | Accepted for watched channels |

The initial `GET_CHANNEL` result is one snapshot only. Its request has no
documented history limit, cursor, or pagination argument, so it does not prove
complete history or a universal message count.

## Live subscription and outage test

Two simultaneous RPC listeners watched two text channels whose identifiers are
intentionally omitted. The channels remained unopened while Discord was
minimized on the Friends screen, then one channel was selected in the
foreground. Events included message text in this finite sample.

| Phase | Watched channel A | Watched channel B | Duplicate IDs |
| --- | ---: | ---: | ---: |
| Minimized and unopened | 16 / 16 (collector / witness) | 9 / 9 | 0 |
| Collector restarted | 29 / 29 | 7 / 7 | 0 |
| Foreground, channel A selected | 18 / 18 | 3 / 3 | 0 |

Across overlapping live traffic, the listeners matched 63 IDs on channel A
and 19 on channel B. Every matching event had the same content fingerprint;
no duplicate IDs were observed.

For the outage, the collector process was stopped while the independent
witness stayed connected. After the collector reauthenticated and resubscribed,
it used `GET_CHANNEL` for recovery:

| Channel | Witness IDs during outage | Recovery snapshot | Witness IDs found | Missing | Duplicates |
| --- | ---: | ---: | ---: | ---: | ---: |
| A | 21 | 51 | 21 | 0 | 0 |
| B | 13 | 43 | 13 | 0 | 0 |

All 34 witness IDs were present in the corresponding recovery snapshots.
Nonempty content fingerprints matched for 19/21 on A and 13/13 on B. The
snapshots also contained 30 additional older or overlapping IDs per channel,
so this proves inclusion of the outage set, not snapshot completeness.

Restarting the Discord desktop process also worked operationally: both probe
listeners reauthenticated and resubscribed. No independent witness could run
while the entire desktop process was stopped, and no known natural messages
fell in that short gap. Losslessness across a full desktop restart is therefore
unproven.

## GET_CHANNEL behavior and limits

Observed snapshot depths were 98 and 25 before the collector outage, 51 and 43
during collector recovery, and 30/30 after the desktop restart. The variation
rules out a fixed 30-message limit in this test, but the result is still
client-cache/session dependent.

The documented RPC surface exposes no history cursor, pagination boundary, or
replay guarantee for `GET_CHANNEL`. A recovery sweep can find messages that
remain in the client-visible snapshot, but a snapshot cannot certify that no
older or temporarily unavailable messages exist. Agreement between two
listeners also does not prove that Discord delivered every event to both.

This was one finite run and did not test edits, attachment-only content, long
desktop downtime, or process crashes at every point in the reconnect sequence.

## Practical conclusion

The official path is promising as a live optimization with periodic bounded
recovery and exact ID deduplication. The probe does not establish it as a
durable replacement for CordBrief's existing recovery contract. Treat these
measurements as research evidence until a future implementation supplies and
verifies its own durability guarantees.

Official documentation:

- [Discord RPC](https://docs.discord.com/developers/topics/rpc)
- [Discord OAuth2](https://docs.discord.com/developers/topics/oauth2)
