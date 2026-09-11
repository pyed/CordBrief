# Gates: M15 Productize Official Discord RPC Collector Setup & Runtime

OWNS: collector/rpc/**, collector/test/rpc_lifecycle_test.mjs, collector/entrypoint-rpc.sh, internal/journal/**, internal/web/**, GATES.md

Scope: Productize the official Discord RPC collector as CordBrief's primary setup and runtime experience, replacing the legacy Vencord lifecycle while preserving journal and Core correctness.

- [x] G1: Explicit 7-State Lifecycle Machine: Daemon models and transitions through all 7 states (discord_starting, discord_login_required, discord_authenticated, cordbrief_authorization_required, oauth_exchange, catalog_watchlist_ready, running) and publishes strictly valid collector-status.json.
  CHECK: node collector/test/rpc_lifecycle_test.mjs --test-states
  EXPECT: rpc_lifecycle_states_verified
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=c3a5dd50534cdf9232eefec1f0c72296f5c4c28e62de6a1b424a87679ab846ae; output-bytes=233

- [x] G2: OAuth Refresh-Token Lifecycle: Daemon handles proactive and reactive token refresh before expiry, saves credentials with 0600 mode, and falls back to authorization if refresh token is rejected.
  CHECK: node collector/test/rpc_lifecycle_test.mjs --test-token-refresh
  EXPECT: rpc_token_refresh_verified
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=48ea2ae975494a62ea954fa1bc7a0d1917f47fea0d7f6709d78ebba06d5fdf19; output-bytes=438

- [x] G3: Anti-Spam Authorization Prompting: Missed, cancelled, or expired authorization prompts do not spam Discord or crash the collector; operator notification is surfaced and retry cooldown is enforced.
  CHECK: node collector/test/rpc_lifecycle_test.mjs --test-anti-spam
  EXPECT: rpc_anti_spam_prompt_verified
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=12d84ad211f0fc07ab159f57295e7ff8deb96a1ab5d8de0d1207ead93de26b6a; output-bytes=564

- [x] G4: On-Demand Xpra Lifecycle: Xpra shadow viewer is started only during interactive states (discord_login_required, cordbrief_authorization_required, enter_reauth) and stopped during normal operation; unattended restarts with valid state bypass Xpra entirely.
  CHECK: node collector/test/rpc_lifecycle_test.mjs --test-xpra-lifecycle
  EXPECT: rpc_xpra_lifecycle_verified
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=4b0dfc0878ed5c8608c9e342c364f2ce77136f2d1541ee6adf8486c48206f457; output-bytes=211

- [x] G5: Exchange Command Control: Core-to-Collector commands (enter_reauth, return_normal, status) via collector-command.json are processed, state transitioned, and acknowledged in collector-command-ack.json.
  CHECK: node collector/test/rpc_lifecycle_test.mjs --test-commands
  EXPECT: rpc_commands_verified
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=8f99656f8f6424c0f81cb1eb3a9b1c5840e468de5ad799049ef162c06c196dc6; output-bytes=335

- [x] G6: Go Core Interop & Web UI Integration: Go Core strictly parses collector-status.json, renders operator setup/reauth guidance, and passes all web and journal unit tests.
  CHECK: node collector/test/rpc_lifecycle_test.mjs --test-core-interop
  EXPECT: core_interop_tests_passed
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=c496f6ea21264e43902ca6090a7816039aea8c98a53916aa2aad02f49fc38d7b; output-bytes=215

- [x] G7: Complete Regression Suite & Legacy Safe-to-Delete Inventory: All RPC unit/integration tests pass and legacy Vencord components safe to delete post-verification are cataloged without breaking rollback.
  CHECK: node collector/test/rpc_lifecycle_test.mjs --test-all
  EXPECT: rpc_all_lifecycle_checks_passed
  EVIDENCE: exit=0; shell=C:\WINDOWS\system32\cmd.exe; cwd=C:\Users\Sheriff\Desktop\src\CordBrief; path=26d9d1520155/28 entries; EXPECT=matched; output-sha256=ace12b4fd63c712030b3afca6ab597a0248f3403d908693572ea2244fd2e5dc1; output-bytes=3432



