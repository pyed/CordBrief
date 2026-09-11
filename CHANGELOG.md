# Changelog
All notable changes to CordBrief will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [2.0.0] - 2026-09-11

### Added
- **Official Discord RPC Architecture**: Canonical collection engine communicating with the official, unmodified Discord Linux desktop client via local Unix domain socket IPC (`discord-ipc-0`).
- **Official OAuth2 Authorization**: Standard Discord OAuth flow requesting strictly `rpc`, `identify`, and `messages.read` scopes with unattended token refresh.
- **On-Demand Interactive Setup**: Ephemeral Xpra shadow viewer on loopback (`127.0.0.1:28742`) that activates exclusively when interactive Discord login or OAuth authorization is required, shutting down completely during normal headless streaming.
- **Unattended Lifecycle & Session Recovery**: Reliable, headless auto-initialization and session restoration across container and host restarts without synthetic clicks, focus hacks, or window activation automation.
- **Secure In-Volume Credential Seeding**: Non-echoing credential setup scripts (`scripts/update_secret.sh` and `scripts/update_secret.ps1`) that write Discord OAuth secrets directly to private container volume storage (`mode 0600`, `1000:1000`) without storing secrets in plaintext `.env`.
- **Public Developer Setup Guide**: Step-by-step documentation for creating and configuring a Discord Developer Application.

### Changed
- Promoted `docker/compose.yml` and `collector/Dockerfile` as the sole canonical production stack.
- Updated recovery contract and documentation to reflect local RPC event subscriptions and bounded client-cache snapshot recovery (`GET_CHANNEL`).
- Hardened retention maintenance and runtime locks (`docker/compose.retention.yml`) to enforce mutual exclusion against active collectors.

### Removed
- Removed legacy Vencord client modification tooling, patched `app.asar`, CDP remote debugging flags, and internal REST scraping paths.
- Decommissioned legacy staging compose files (`docker/compose.rpc.yml`).
- Scrubbed all development fixtures and personal identifiers from tracked repository files.
