# Gates: Milestone 12 — Telegram Delivery + Port Hardening

OWNS: cmd/** internal/** collector/** docker/** GATES.md README.md

Scope: Implement first-class stdlib-only Telegram delivery for CordBrief digests and migrate default ports to 28741 (Core) and 28742 (Setup) with zero published ports on Collector.

- [x] G1: Port Hardening Configuration & Defaults
  CHECK: go test -v -run TestPortHardeningDefaults ./internal/config ./cmd/cordbrief
  EXPECT: ok  	cordbrief/internal/config
  EVIDENCE: verified (Core port defaults to 28741, Setup port defaults to 28742, env overrides respected)

- [x] G2: Stdlib Telegram API Client
  CHECK: go test -v -run TestTelegramClient ./internal/delivery
  EXPECT: ok  	cordbrief/internal/delivery
  EVIDENCE: verified (stdlib net/http, getMe, sendMessage, 429 backoff, 1MB bounded reader, 15s timeout)

- [x] G3: Secret Hygiene & Token Redaction
  CHECK: go test -v -run TestSecretHygiene ./internal/delivery ./internal/config
  EXPECT: ok  	cordbrief/internal/delivery
  EVIDENCE: verified (token redacted in URLs, error messages, and description strings)

- [x] G4: Interactive Chat Discovery & Safe Metadata
  CHECK: go test -v -run TestChatDiscovery ./internal/delivery
  EXPECT: ok  	cordbrief/internal/delivery
  EVIDENCE: verified (getUpdates extracts safe metadata, discards all message content, dedupes chats)

- [x] G5: Digest HTML Rendering, Escaping & Safe Chunking
  CHECK: go test -v -run TestTelegramRenderingAndChunking ./internal/delivery
  EXPECT: ok  	cordbrief/internal/delivery
  EVIDENCE: verified (HTML escaped, jump links preserved, no localhost links, rune-safe chunking with ~3800 char ceiling)

- [x] G6: Durable Delivery State & Multipart Resume
  CHECK: go test -v -run TestDurableDeliveryState ./internal/delivery
  EXPECT: ok  	cordbrief/internal/delivery
  EVIDENCE: verified (durable JSON per batch, atomic writes, confirmed message_ids preserved, next-part resume)

- [x] G7: Ambiguous Transport Failure & Uncertain State
  CHECK: go test -v -run TestUncertainDeliverySemantics ./internal/delivery
  EXPECT: ok  	cordbrief/internal/delivery
  EVIDENCE: verified (timeout/5xx sets state to uncertain, auto-retry forbidden, force=true allows operator retry)

- [x] G8: Core In-Process Delivery Worker & Startup Recovery
  CHECK: go test -v -run TestDeliveryWorker ./internal/delivery
  EXPECT: ok  	cordbrief/internal/delivery
  EVIDENCE: verified (startup scan resumes pending deliveries, does not auto-resend uncertain/sent)

- [x] G9: Web Control Plane & Inbox Delivery Integration with CSRF
  CHECK: go test -v -run TestWebDeliveryHandlers ./internal/web
  EXPECT: ok  	cordbrief/internal/web
  EVIDENCE: verified (CSRF checks, settings, test, chats discovery, ping, manual deliver, UI status badges)

- [x] G10: Canonical Digest Invariant & CLI Trigger Policy
  CHECK: go test -v -run TestDigestDeliveryTriggerPolicy ./cmd/cordbrief ./internal/scheduler
  EXPECT: ok  	cordbrief/cmd/cordbrief
  EVIDENCE: verified (preview never sends, CLI run requires --deliver, scheduler enqueues post-commit without cursor rollback on failure)

- [x] G11: Full Automated Regression Suite (Node + Go stdlib only)
  CHECK: node -e "const cp=require('child_process'); cp.execSync('node collector/test/writer_test.mjs', {stdio:'inherit'}); cp.execSync('node collector/test/recovery_test.mjs', {stdio:'inherit'}); cp.execSync('node collector/test/operational_test.mjs', {stdio:'inherit'}); cp.execSync('go test -count=1 -timeout=60s ./...', {stdio:'inherit'}); cp.execSync('go vet ./...', {stdio:'inherit'}); const mods=cp.execSync('go list -m all', {encoding:'utf8'}).trim(); if(mods!=='cordbrief') process.exit(1); console.log('regression bar passed');"
  EXPECT: regression bar passed
  EVIDENCE: verified (all collector suites passed, all Go unit tests passed, vet clean, go list -m all strictly 'cordbrief')

- [x] G12: Live Gate A — Port Hardening Live Verification
  EVIDENCE: verified (Core running on 127.0.0.1:28741, port 8080 closed, collector zero published ports, setup on 28742 and closed on exit, collector healthy, continuity intact)

- [x] G13: Live Gate B — Telegram Hold Boundary
  EVIDENCE: verified (Hold boundary established. Ready for local operator Web UI configuration without Antigravity token exposure)

- [x] G14: Live Gate C — Real Telegram Digest Delivery
  EVIDENCE: verified (real BotFather token accepted, Test Bot / getMe passed, /start + Discover Chats passed, real Send Test Ping received on Telegram, real existing digest 515958192c753f14ab18410039d579c45e6f6c8c9062e4ebad31874a0ef0c9bb delivered to Telegram successfully with clickable sources and verified client layout)

- [x] G15: Live Gate D — Autonomous Scheduled Telegram Delivery Gate
  EVIDENCE: verified (autonomous scheduler pipeline proven end-to-end with real Telegram delivery):
  1. Scheduled slot Asia/Riyadh/2026-09-06/04:50 evaluated.
  2. First attempt failed due to missing GEMINI_API_KEY inside Core container.
  3. Corrected docker/compose.yml with env_file forwarding (path: ../.env, required: false).
  4. Overdue 04:50 slot then succeeded autonomously using Gemini Cloud (gemini-3.7-flash).
  5. Digest artifact d1e39e65d0f1eff9b1e3648134d6c91b2c15a32dd87f768f8b9fc0f6776cbfac created with trigger.slot_id = "Asia/Riyadh/2026-09-06/04:50" and title "Discord Collector Offline Recovery Testing".
  6. Journal cursor advanced from offset 7498 to 9451 (unconsumed tail count = 0).
  7. Telegram delivery dispatched automatically (Message ID: 5).
  8. User visually confirmed Telegram receipt: clean styling, "Sources: 1, 2, 3, 4" clickable jump links, zero internal S000001 IDs leaked.
  9. Subsequent 04:55 slot evaluated with zero new backlog and completed empty (last_result: "empty").
  10. Schedule restored to operator baseline (enabled: false, time: "03:17"). Core container restart verified durable state and zero resends.

