# Acceptance Gates: Layered Discord Collector Diagnostic & Proof

## Phase A: Clean Official Discord Only (No Vencord, No Plugins)

- [x] Gate A1: Untouched official Discord launched with completely clean profile/runtime volume in Xpra desktop mode + Openbox.
- [x] Gate A2: Discord updater_bootstrap lifecycle and process behavior analyzed and verified non-looping.
- [x] Gate A3: Clean official Discord reaches stable login/QR screen on display :100 and remains running stably.
- [x] Gate A4: Clean official Discord authenticated via QR code, reaches `/channels` UI, and authenticated session persists across complete container restart.

## Phase B: Official Vencord Injection (No CordBrief Plugin)

- [x] Gate B1: Vencord built from source and injected into Discord using Vencord's official installation mechanism without manual ASAR surgery.
- [x] Gate B2: Discord + official Vencord boots directly into the authenticated `/channels` UI, `#app-mount` is fully populated, and client remains running stably for >60 seconds.

## Phase C: CordBrief Collector Userplugin

- [x] Gate C1: CordBriefCollector userplugin integrated cleanly (`index.ts` -> Flux MESSAGE_CREATE -> `native.ts` IPC bridge) and built with Vencord.
- [x] Gate C2: Discord + Vencord + CordBriefCollector boots cleanly, authenticated UI remains responsive, CordBriefCollector is active (`started: true`) with native helpers bound.

## Phase D: Real Background Message Delivery

- [x] Gate D1: Controlled Background Test — 10/10 messages captured on watched background Channel A (`#test-a`), 10/10 captured on watched background Channel B (`#test-b`), 0 persisted on unwatched foreground Channel C (`#general`).
- [x] Gate D2: Browser Detached Test — 5/5 on Channel A and 5/5 on Channel B captured headlessly with 0 connected Xpra/browser clients.
- [x] Gate D3: Collector Restart Test — Clean container restart preserves authenticated session, Vencord, and CordBriefCollector watchlist; subsequent 5/5 on Channel A and 5/5 on Channel B captured without missing records.
- [x] Gate D4: NDJSON Integrity — 40 total records, 0 malformed lines, 0 duplicate records, 0 allowlist violations.
