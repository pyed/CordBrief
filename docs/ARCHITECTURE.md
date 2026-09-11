# Architecture

CordBrief consists of two core services communicating via filesystem exchange:

| Component | Responsibility |
|---|---|
| Collector | Runs official unmodified Discord desktop client + RPC daemon, captures watched messages, performs bounded snapshot recovery, writes journal records. |
| Core | Go service for the Web inbox, configuration, digests, scheduling, and Telegram delivery. Standard library only. |
| On-Demand Viewer | On-demand Xpra shadow viewer on loopback (127.0.0.1:28742) active only during interactive setup (Discord login, CordBrief authorization). |

```text
Discord (Desktop Client) ↔ RPC Daemon (Collector) → filesystem journal → Core → Web inbox / Telegram
                                                                           ↕
                                                                    chosen LLM endpoint
```

Core writes the watchlist, commands, and committed journal cursor. Collector writes messages,
catalog, and status to the shared exchange volume. Each service also has private
state. Core cannot read the Discord profile, keyring, or Collector recovery state;
Collector cannot read Core's credentials. Neither receives a Docker socket.

Messages are appended and synced before recovery progress is committed. Core's
cursor advances on a successful commit, not simply when a message is read. Digest
artifacts contain durable source identities, so citations survive journal retirement.
Automatic Telegram delivery uses a durable outbox. An ambiguous send is marked
uncertain rather than silently retried as though it never happened.

Collector and retention maintenance share a runtime lock. Core and standalone journal commands
use a separate commit lock. Offline journal maintenance holds both and has an
explicit, opt-in mount of Core data. Diagnostic status files grant no deletion
authority.

[Recovery](RECOVERY_CONTRACT.md) defines the message guarantees and limitations.
[Retention](RETENTION.md) explains how old transcript bytes can be replaced by
exact identity evidence without duplicating replayed messages.
