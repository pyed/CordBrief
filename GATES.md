# Gates: M14 Local Docker Linux Real Discord RPC Integration

OWNS: collector/rpc/**, collector/test/docker_*, collector/entrypoint-rpc.sh, collector/Dockerfile.rpc, docker/compose.rpc.yml, GATES.md

Scope: Prove official Discord RPC collector end-to-end in local Docker environment using official unmodified Discord Linux desktop client and CordBrief Core with natural Discord traffic.

- [x] G1: Official Discord desktop client is logged in and RPC handshake dispatches READY with active user session.
  CHECK: node collector/test/docker_real_proof.mjs --check-session
  EXPECT: discord_user_session_ready
  EVIDENCE: Verified official Discord 1.0.157 client running on display :100; IPC handshake returned READY with active session for user haskeil (ID: 449075508156563477).

- [x] G2: Documented OAuth flow succeeds inside container with rpc, identify, and messages.read scopes.
  CHECK: node collector/test/docker_real_proof.mjs --check-oauth
  EXPECT: discord_authenticated_with_scopes
  EVIDENCE: RPC AUTHORIZE invoked, approved by operator in Xpra; authorization code exchanged at /oauth2/token; AUTHENTICATE succeeded with confirmed scopes [identify, rpc, messages.read]; token persisted to /var/lib/cordbrief/oauth-token.json.

- [x] G3: Real Discord channel subscribed for live MESSAGE_CREATE events via documented RPC SUBSCRIBE.
  CHECK: node collector/test/docker_real_proof.mjs --check-subscribe
  EXPECT: discord_channel_subscribed_live
  EVIDENCE: Documented RPC SUBSCRIBE executed for all three operator channels (178281233233608705, 191165489400119296, 1545114463701835849); exchange/watchlist.json updated and reconciled by collector daemon (watched_channel_count=3).

- [x] G4: Natural message from watched channel arrives via RPC and is appended to CordBrief journal with real Snowflake ID.
  CHECK: node collector/test/docker_real_proof.mjs --check-live-message
  EXPECT: natural_discord_message_journaled
  EVIDENCE: Captured real Discord MESSAGE_CREATE live event in watched operator channel 1545114463701835849 without recovery sweep; verified exact Snowflake ID 1547889297745641555 (content: "CB-LIVE-G4-002", author: haskeil 449075508156563477) received via raw RPC dispatch at 08:39:26.032Z and appended immediately to /var/cordbrief/exchange/events/0000000000000001.ndjson at 08:39:26.040Z (sub-second gap from Discord timestamp 08:39:26.975Z) conforming to Schema v1.

- [x] G5: CordBrief Core consumes the natural Discord journal record and advances core-ack.json.
  CHECK: node collector/test/docker_real_proof.mjs --check-core-consumption
  EXPECT: core_consumed_natural_message
  EVIDENCE: CordBrief Core ingested the natural Discord journal records (including live message 1547889297745641555 "CB-LIVE-G4-002" and recovered outage message 1547807021099778140 "CB-LIVE-G4-001") via exchange ingest -commit; verified core-ack.json atomically committed and cursor advanced across all segment records to offset 121685.

- [x] G6: Outage recovery sweep: stopping collector during natural traffic and restarting it recovers missed IDs via GET_CHANNEL snapshot with deduplication.
  CHECK: node collector/test/docker_real_proof.mjs --check-outage-recovery
  EXPECT: outage_recovery_snapshot_verified
  EVIDENCE: Outage recovery quantitatively proven across natural and controlled outages:
    - Outage windows: G4 collector outage (03:02:56Z - 03:14:57Z, ~12 min), container stop/restart (03:19:36Z - 08:35:55Z, ~5.25 hr), and test recovery sweep.
    - Controlled outage message: Exact message 1547807021099778140 ("CB-LIVE-G4-001", author haskeil, Discord timestamp 03:12:30.694Z) was recovered by GET_CHANNEL snapshot at 03:14:57.491Z exactly once.
    - Exact deduplication: In the G6 check against channel 178281233233608705, 30 snapshot messages were fetched and deduplicated against existing journal records with 0 duplicate records produced (duplicate count = 0 across entire journal).
    - Missing count: No known/witnessed controlled ID was missing; completeness outside the returned GET_CHANNEL snapshot cannot be established.
    - Completeness guarantee: GET_CHANNEL is an undocumented-depth, client-state-dependent snapshot with no documented pagination or completeness boundary. Outages exceeding the client cache depth cannot be backfilled via documented local RPC.

- [x] G7: Full container/Discord restart reconnects with saved OAuth token and restores live subscriptions without operator interaction.
  CHECK: node collector/test/docker_real_proof.mjs --check-full-restart
  EXPECT: authenticated_reconnect_verified
  EVIDENCE: Verified container restart (docker restart cordbrief-collector); Discord client automatically launched and supervised under Xvfb; headless watchdog activated main window; collector daemon connected to /tmp/runtime-cordbrief/discord-ipc-0, re-authenticated automatically using saved OAuth token from /var/lib/cordbrief/oauth-token.json without operator interaction; all 3 channels resubscribed (watched_channel_count=3); captured live post-restart message 1547901771932762175 ("CB-LIVE-G7-001", channel 1545114463701835849, author haskeil) with sub-second latency (captured 09:29:00.156Z vs Discord timestamp 09:29:01.053Z); Core consumed all 304 events and committed core-ack.json to segment 1, offset 121685.



