# Architecture

CordBrief has two long-running services and one maintenance tool:

| Component | Responsibility |
|---|---|
| Collector | Runs prepared Discord + Vencord, captures watched messages, recovers gaps, writes journal records. |
| Core | Go service for the Web inbox, configuration, digests, scheduling, and Telegram delivery. Standard library only. |
| Setup | Temporary authentication screen, runtime builds/staging, and offline maintenance. |

```text
Discord → Collector → filesystem journal → Core → Web inbox / Telegram
                                           ↕
                                     chosen LLM endpoint
```

Core writes the watchlist and committed journal cursor. Collector writes messages,
catalog and status to the shared exchange volume. Each service also has private
state. Core cannot read the Discord profile, keyring, or Collector recovery state;
Collector cannot read Core's credentials. Neither receives a Docker socket.

Messages are appended and synced before recovery progress is committed. Core's
cursor advances on a successful commit, not simply when a message is read. Digest
artifacts contain durable source identities, so citations survive journal retirement.
Automatic Telegram delivery uses a durable outbox. An ambiguous send is marked
uncertain rather than silently retried as though it never happened.

Collector and setup share a runtime lock. Core and standalone journal commands
use a separate commit lock. Offline journal maintenance holds both and has an
explicit, opt-in mount of Core data. Diagnostic status files grant no deletion
authority.

[Recovery](RECOVERY_CONTRACT.md) defines the message guarantees and limitations.
[Retention](RETENTION.md) explains how old transcript bytes can be replaced by
exact identity evidence without duplicating replayed messages.
