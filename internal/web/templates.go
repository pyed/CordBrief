package web

import "html/template"

const navCSS = `
    .nav-bar {
      display: flex;
      gap: 12px;
      margin-bottom: 20px;
      border-bottom: 1px solid var(--border);
      padding-bottom: 12px;
    }
    .nav-link {
      padding: 8px 16px;
      border-radius: 6px;
      text-decoration: none;
      font-weight: 600;
      font-size: 0.95rem;
      color: var(--text-muted);
      transition: all 0.15s ease;
    }
    .nav-link:hover {
      background: rgba(255, 255, 255, 0.05);
      color: var(--text);
    }
    .nav-link.active {
      background: var(--primary);
      color: #fff;
    }
`

const baseCSS = `
    :root {
      --bg: #0f1117;
      --card-bg: #1a1d27;
      --border: #2e3446;
      --text: #e1e4ed;
      --text-muted: #8b92a5;
      --primary: #5865f2;
      --primary-hover: #4752c4;
      --success: #3ba55d;
      --warning: #faa81a;
      --danger: #ed4245;
      --code-bg: #12141c;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
      background: var(--bg);
      color: var(--text);
      line-height: 1.5;
      padding: 24px 16px;
    }
    .container {
      max-width: 860px;
      margin: 0 auto;
    }
    header {
      margin-bottom: 16px;
      padding-bottom: 16px;
      border-bottom: 1px solid var(--border);
      display: flex;
      justify-content: space-between;
      align-items: center;
    }
    h1 { font-size: 1.5rem; font-weight: 700; }
    .badge {
      font-size: 0.75rem;
      font-weight: 600;
      padding: 4px 8px;
      border-radius: 4px;
      text-transform: uppercase;
      letter-spacing: 0.5px;
      display: inline-block;
    }
    .badge-ok { background: rgba(59, 165, 93, 0.2); color: var(--success); border: 1px solid var(--success); }
    .badge-warn { background: rgba(250, 168, 26, 0.2); color: var(--warning); border: 1px solid var(--warning); }
    .badge-err { background: rgba(237, 66, 69, 0.2); color: var(--danger); border: 1px solid var(--danger); }
    .badge-info { background: rgba(88, 101, 242, 0.2); color: #8ea1e1; border: 1px solid var(--primary); }
    .badge-scheduled { background: rgba(155, 89, 182, 0.2); color: #d7bde2; border: 1px solid #9b59b6; }
    .badge-manual { background: rgba(120, 144, 156, 0.2); color: #b0bec5; border: 1px solid #78909c; }

    .badge-kind-important { background: rgba(237, 66, 69, 0.2); color: #ff8b8d; border: 1px solid #ed4245; }
    .badge-kind-finding { background: rgba(88, 101, 242, 0.2); color: #9bb1ff; border: 1px solid #5865f2; }
    .badge-kind-experiment { background: rgba(155, 89, 182, 0.2); color: #d7bde2; border: 1px solid #9b59b6; }
    .badge-kind-disagreement { background: rgba(230, 126, 34, 0.2); color: #f8c471; border: 1px solid #e67e22; }
    .badge-kind-question { background: rgba(241, 196, 15, 0.2); color: #f9e79f; border: 1px solid #f1c40f; }
    .badge-kind-resource { background: rgba(46, 204, 113, 0.2); color: #a3e4d7; border: 1px solid #2ecc71; }

    .card {
      background: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 20px;
      margin-bottom: 24px;
    }
    .card-title {
      font-size: 1.1rem;
      font-weight: 600;
      margin-bottom: 16px;
      display: flex;
      justify-content: space-between;
      align-items: center;
    }
    .status-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
      gap: 12px;
      margin-bottom: 8px;
    }
    .status-item {
      background: var(--bg);
      padding: 12px;
      border-radius: 6px;
      border: 1px solid var(--border);
    }
    .status-label { font-size: 0.75rem; color: var(--text-muted); text-transform: uppercase; margin-bottom: 4px; }
    .status-value { font-weight: 600; font-size: 0.95rem; display: flex; align-items: center; gap: 8px; }
    .form-group { margin-bottom: 16px; }
    label { display: block; font-size: 0.85rem; font-weight: 600; margin-bottom: 6px; }
    input[type="text"], input[type="password"], select {
      width: 100%;
      padding: 10px 12px;
      background: var(--bg);
      border: 1px solid var(--border);
      border-radius: 6px;
      color: var(--text);
      font-size: 0.9rem;
    }
    input[type="text"]:focus, input[type="password"]:focus, select:focus {
      outline: none;
      border-color: var(--primary);
    }
    .btn {
      display: inline-block;
      padding: 10px 18px;
      font-size: 0.9rem;
      font-weight: 600;
      border-radius: 6px;
      border: none;
      cursor: pointer;
      text-decoration: none;
      transition: background 0.15s ease;
    }
    .btn-primary { background: var(--primary); color: white; }
    .btn-primary:hover { background: var(--primary-hover); }
    .btn-secondary { background: #2c3144; color: var(--text); }
    .btn-secondary:hover { background: #383f58; }
    .btn-group { display: flex; gap: 10px; align-items: center; margin-top: 16px; }
    .tree-guild { margin-bottom: 16px; background: var(--bg); border: 1px solid var(--border); border-radius: 6px; padding: 12px; }
    .tree-guild-name { font-weight: 700; margin-bottom: 8px; color: #fff; }
    .tree-channels { display: flex; flex-direction: column; gap: 6px; padding-left: 12px; }
    .channel-cb { display: flex; align-items: center; gap: 8px; font-size: 0.9rem; cursor: pointer; }
    .channel-cb input { cursor: pointer; width: 16px; height: 16px; }
    .alert {
      padding: 12px 16px;
      border-radius: 6px;
      margin-bottom: 20px;
      font-size: 0.9rem;
    }
    .alert-success { background: rgba(59, 165, 93, 0.15); border: 1px solid var(--success); color: #85e09f; }
    .alert-error { background: rgba(237, 66, 69, 0.15); border: 1px solid var(--danger); color: #ffa6a8; }
    .preview-box {
      background: var(--code-bg);
      border: 1px solid var(--border);
      border-radius: 6px;
      padding: 16px;
      margin-top: 16px;
      font-family: inherit;
      white-space: pre-wrap;
      overflow-x: auto;
      line-height: 1.6;
    }
    .help-text { font-size: 0.8rem; color: var(--text-muted); margin-top: 4px; }
`

const indexTemplateHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>CordBrief Setup Control Plane</title>
  <style>
` + baseCSS + navCSS + `
  </style>
</head>
<body>
  <div class="container">
    <header>
      <div>
        <h1>CordBrief Setup Control Plane</h1>
        <p class="help-text">Architecture v2 Appliance Management</p>
      </div>
      <div>
        {{if .CollectorRunning}}
          <span class="badge badge-ok">Collector Running</span>
        {{else}}
          <span class="badge badge-err">Collector Problem</span>
        {{end}}
      </div>
    </header>

    <nav class="nav-bar">
      <a href="/" class="nav-link active">⚙ Setup</a>
      <a href="/inbox" class="nav-link">📬 Inbox</a>
    </nav>

    {{if .FlashMessage}}
      <div class="alert alert-success">{{.FlashMessage}}</div>
    {{end}}
    {{if .FlashError}}
      <div class="alert alert-error">{{.FlashError}}</div>
    {{end}}

    <!-- 1. STATUS SUMMARY -->
    <div class="card">
      <div class="card-title">System Readiness</div>
      <div class="status-grid">
        <div class="status-item">
          <div class="status-label">Discord Session</div>
          <div class="status-value">
            {{if .CollectorStale}}
              <span class="badge badge-err">Collector Offline</span>
            {{else if .SetupRequired}}
              <span class="badge badge-warn">Setup Required</span>
            {{else if .ReauthRequired}}
              <span class="badge badge-err">Reauth Required</span>
            {{else if .DiscordAuthenticated}}
              <span class="badge badge-ok">Connected</span>
            {{else if eq .CollectorStateStr "starting"}}
              <span class="badge badge-info">Starting...</span>
            {{else}}
              <span class="badge badge-warn">Disconnected</span>
            {{end}}
          </div>
        </div>
        <div class="status-item">
          <div class="status-label">Channel Catalog</div>
          <div class="status-value">
            {{if eq .CatalogState "ready"}}
              <span class="badge badge-ok">Ready ({{.CatalogGuildCount}} Servers, {{.CatalogChannelCount}} Channels)</span>
            {{else if eq .CatalogState "error"}}
              <span class="badge badge-err">Catalog Error</span>
            {{else}}
              <span class="badge badge-warn">Unavailable</span>
            {{end}}
          </div>
        </div>
        <div class="status-item">
          <div class="status-label">Watched Channels</div>
          <div class="status-value">
            <span class="badge badge-ok">Gen {{.WatchedGeneration}} ({{.WatchedChannelCount}} Channels)</span>
          </div>
        </div>
        <div class="status-item">
          <div class="status-label">LLM Provider</div>
          <div class="status-value">
            {{if eq .Config.LLM.Provider "gemini"}}
              <span class="badge badge-ok">Gemini ({{.Config.LLM.Model}})</span>
            {{else}}
              <span class="badge badge-ok">Local ({{.Config.LLM.Model}})</span>
            {{end}}
          </div>
        </div>
        <div class="status-item">
          <div class="status-label">Committed Cursor</div>
          <div class="status-value">
            Seg {{.CommittedCursor.Segment}}, Off {{.CommittedCursor.Offset}}
          </div>
        </div>
        <div class="status-item">
          <div class="status-label">Active Journal</div>
          <div class="status-value">
            Seg {{.ActiveSegment}}, Off {{.JournalFinalOffset}}
          </div>
        </div>
        <div class="status-item">
          <div class="status-label">Discord Continuity</div>
          <div class="status-value">
            {{if .CollectorStale}}
              <span class="badge badge-err">Unavailable</span>
            {{else if eq .RecoveryState "recovering"}}
              <span class="badge badge-warn">Recovering ({{.RecoveryPendingChannels}} remaining)</span>
            {{else if eq .RecoveryState "error"}}
              <span class="badge badge-err">Recovery Warning</span>
            {{else if eq .RecoveryState "ready"}}
              <span class="badge badge-ok">✓ Up to date</span>
            {{else}}
              <span class="badge badge-warn">Unavailable</span>
            {{end}}
          </div>
        </div>
      </div>
    </div>

    <!-- DISCORD SESSION CONTROL -->
    {{if or .SetupRequired .ReauthRequired}}
      <div class="card" style="border-left: 4px solid var(--warning);">
        <div class="card-title">
          <span>Discord Authentication Required</span>
          {{if .SetupRequired}}
            <span class="badge badge-warn">First-Run Setup</span>
          {{else}}
            <span class="badge badge-err">Reauthentication Needed</span>
          {{end}}
        </div>
        <p style="margin-bottom: 12px; font-size: 0.95rem;">
          {{if .SetupRequired}}
            CordBrief requires an official Discord login to discover your channels and monitor updates.
          {{else}}
            Your Discord session has expired or requires reauthentication.
          {{end}}
          To authenticate, run the ephemeral setup tool:
        </p>
        <div style="background: var(--code-bg); padding: 10px 12px; border-radius: 6px; font-family: monospace; font-size: 13px; margin-bottom: 12px; line-height: 1.6;">
          # 1. Stop collector and launch setup viewer:<br>
          <strong>docker compose stop cordbrief-collector && docker compose --profile setup up cordbrief-setup</strong><br><br>
          # 2. Open viewer in browser and sign in:<br>
          <a href="http://127.0.0.1:28742/" target="_blank" style="color: var(--primary); font-weight: bold;">http://127.0.0.1:28742/</a><br><br>
          # 3. Once signed in, restart collector:<br>
          <strong>docker compose start cordbrief-collector</strong>
        </div>
        <div class="btn-group">
          <a href="http://127.0.0.1:28742/" target="_blank" class="btn btn-primary">
            🖥 Open Setup Viewer (:28742)
          </a>
          <form method="POST" action="/api/collector/command" style="display: inline;">
            <input type="hidden" name="command" value="return_normal">
            <button type="submit" class="btn btn-secondary">Check Status</button>
          </form>
        </div>
        <div class="help-text" style="margin-top: 10px; line-height: 1.5;">
          <strong>Security Note:</strong> Port <code>28742</code> is bound to <code>127.0.0.1</code> (localhost) only to protect your Discord desktop session.<br>
          If managing CordBrief remotely on a NAS, forward ports over SSH:<br>
          <code style="background: var(--code-bg); padding: 2px 6px; border-radius: 4px;">ssh -L 28741:127.0.0.1:28741 -L 28742:127.0.0.1:28742 user@nas</code>
        </div>
      </div>
    {{else if .DiscordAuthenticated}}
      <div class="card">
        <div class="card-title">
          <span>Discord Desktop Session</span>
          <span class="badge badge-ok">Active Session</span>
        </div>
        <p class="help-text" style="margin-bottom: 12px;">
          Discord Desktop is running and capturing messages in background. If you need to switch accounts or refresh session tokens:
        </p>
        <div class="btn-group" style="margin-top: 0; margin-bottom: 10px;">
          <form method="POST" action="/api/collector/command" onsubmit="return confirm('This will pause collection and request re-authentication. Proceed?');" style="display: inline-block;">
            <input type="hidden" name="command" value="enter_reauth">
            <button type="submit" class="btn btn-secondary">Reauthenticate Discord</button>
          </form>
        </div>
        <div class="help-text">
          Reauthentication runs via: <code>docker compose stop cordbrief-collector && docker compose --profile setup up cordbrief-setup</code>
        </div>
      </div>
    {{end}}

    <!-- 2. DAILY DIGEST SCHEDULER -->
    <div class="card">
      <div class="card-title">
        <span>Daily Digest Schedule</span>
        <span class="help-text">Autonomous daily summarization</span>
      </div>
      <form method="POST" action="/api/schedule">
        <div class="form-group">
          <label class="channel-cb">
            <input type="checkbox" name="enabled" value="true" {{if .Config.Schedule.Enabled}}checked{{end}}>
            <span><strong>Enable Daily Autonomous Digest</strong></span>
          </label>
          <div class="help-text">When enabled, CordBrief automatically summarizes unconsumed events once per local day and delivers the result directly to your Inbox.</div>
        </div>

        <div class="status-grid" style="margin-bottom: 16px;">
          <div class="status-item">
            <div class="status-label">Daily Time (24-Hour HH:MM)</div>
            <input type="text" name="time" value="{{.Config.Schedule.Time}}" placeholder="08:00" style="margin-top: 4px;">
            <div class="help-text">Format: 00:00 to 23:59</div>
          </div>
          <div class="status-item">
            <div class="status-label">Timezone (IANA Name)</div>
            <input type="text" name="timezone" value="{{.Config.Schedule.Timezone}}" list="tz-list" placeholder="UTC" style="margin-top: 4px;">
            <datalist id="tz-list">
              <option value="UTC">
              <option value="Asia/Riyadh">
              <option value="America/New_York">
              <option value="America/Los_Angeles">
              <option value="America/Chicago">
              <option value="Europe/London">
              <option value="Europe/Paris">
              <option value="Europe/Berlin">
              <option value="Asia/Tokyo">
              <option value="Asia/Dubai">
            </datalist>
            <div class="help-text">Embedded timezone database (e.g. Asia/Riyadh, UTC)</div>
          </div>
        </div>

        <div class="status-grid" style="margin-bottom: 16px;">
          <div class="status-item">
            <div class="status-label">Schedule Status</div>
            <div class="status-value">
              {{if .Config.Schedule.Enabled}}
                <span class="badge badge-ok">Enabled</span>
              {{else}}
                <span class="badge badge-warn">Disabled</span>
              {{end}}
            </div>
          </div>
          <div class="status-item">
            <div class="status-label">Last Completed Slot</div>
            <div class="status-value" style="font-size: 0.85rem; word-break: break-all;">
              {{if .ScheduleState.LastCompletedSlot}}
                {{.ScheduleState.LastCompletedSlot}}
              {{else}}
                <span class="help-text">None yet</span>
              {{end}}
            </div>
          </div>
          <div class="status-item">
            <div class="status-label">Last Execution Result</div>
            <div class="status-value">
              {{if eq .ScheduleState.LastResult "success"}}
                <span class="badge badge-ok">Success</span>
              {{else if eq .ScheduleState.LastResult "empty"}}
                <span class="badge badge-ok">Up to date (Empty)</span>
              {{else if eq .ScheduleState.LastResult "error"}}
                <span class="badge badge-err">Failed: {{.ScheduleState.LastError}}</span>
              {{else}}
                <span class="help-text">None yet</span>
              {{end}}
            </div>
          </div>
          <div class="status-item">
            <div class="status-label">Next Expected Run</div>
            <div class="status-value" style="font-size: 0.85rem;">
              {{if and .Config.Schedule.Enabled (not .ScheduleNextDue.IsZero)}}
                {{.ScheduleNextDueFormatted}}
              {{else if .Config.Schedule.Enabled}}
                Pending evaluation
              {{else}}
                <span class="help-text">Schedule Disabled</span>
              {{end}}
            </div>
          </div>
        </div>

        <button type="submit" class="btn btn-primary">Save Schedule Settings</button>
      </form>
    </div>

    <!-- 3. WATCHLIST / CHANNELS SELECTION -->
    <div class="card">
      <div class="card-title">
        <span>Watched Channels Selection</span>
        <span class="help-text">Select channels to ingest into exchange journal</span>
      </div>
      <form method="POST" action="/api/watchlist">
        {{if .Catalog}}
          {{range .Catalog.Guilds}}
            <div class="tree-guild">
              <div class="tree-guild-name">🏰 {{.Name}}</div>
              <div class="tree-channels">
                {{range .Channels}}
                  <label class="channel-cb">
                    <input type="checkbox" name="channels" value="{{.ID}}" {{if index $.WatchedSet .ID}}checked{{end}}>
                    <span>#{{.Name}}</span>
                    <span class="help-text" style="font-size: 0.75rem;">(ID: {{.ID}})</span>
                  </label>
                {{end}}
              </div>
            </div>
          {{end}}
          <button type="submit" class="btn btn-primary">Save Watchlist</button>
        {{else}}
          <p class="help-text">Catalog is not yet published by the Discord collector. Ensure Discord is logged in.</p>
        {{end}}
      </form>
    </div>

    <!-- 4. LLM SETTINGS -->
    <div class="card">
      <div class="card-title">Language Model Configuration</div>
      <form method="POST" action="/api/llm" id="llm-form">
        <div class="form-group">
          <label>Provider</label>
          <select name="provider" id="llm-provider" onchange="toggleProviderFields()">
            <option value="gemini" {{if eq .Config.LLM.Provider "gemini"}}selected{{end}}>Google Gemini (Cloud)</option>
            <option value="local" {{if eq .Config.LLM.Provider "local"}}selected{{end}}>Local LLM (OpenAI-compatible)</option>
          </select>
        </div>

        <div id="gemini-fields">
          <div class="form-group">
            <label>Model</label>
            <input type="text" name="gemini_model" value="{{if eq .Config.LLM.Provider "gemini"}}{{.Config.LLM.Model}}{{else}}gemini-3.7-flash{{end}}">
            <div class="help-text">Default: gemini-3.7-flash. Official Google OpenAI-compatible endpoint.</div>
          </div>
          <div class="form-group">
            <label>API Key Status</label>
            <div style="margin-bottom: 8px;">
              {{if eq .GeminiKeySource "environment"}}
                <span class="badge badge-ok">Configured (via environment)</span>
              {{else if eq .GeminiKeySource "stored"}}
                <span class="badge badge-ok">Configured (stored in secrets.json)</span>
              {{else}}
                <span class="badge badge-warn">Not Configured</span>
              {{end}}
            </div>
            <label>Enter / Update Gemini API Key</label>
            {{if eq .GeminiKeySource "environment"}}
              <input type="password" name="gemini_api_key" placeholder="Managed by environment variable (GEMINI_API_KEY)" disabled>
              <div class="help-text"><strong>Precedence Note:</strong> Environment variable (<code>GEMINI_API_KEY</code>) takes precedence over stored secrets. Update <code>.env</code> to change this key.</div>
            {{else if eq .GeminiKeySource "stored"}}
              <input type="password" name="gemini_api_key" placeholder="Configured — leave blank to keep unchanged">
              <div class="help-text">Stored privately in <code>data/secrets.json</code> (0600). Enter a new key to replace it.</div>
            {{else}}
              <input type="password" name="gemini_api_key" placeholder="Enter GEMINI_API_KEY">
              <div class="help-text">Obtained from Google AI Studio. Stored privately in <code>data/secrets.json</code> (0600); never displayed.</div>
            {{end}}
          </div>
        </div>

        <div id="local-fields" style="display: none;">
          <div class="form-group">
            <label>Base URL</label>
            <input type="text" name="local_base_url" value="{{if eq .Config.LLM.Provider "local"}}{{.Config.LLM.BaseURL}}{{else}}http://host.docker.internal:8081/v1{{end}}">
            <div class="help-text">Inside Docker, use host.docker.internal to reach services running on the host.</div>
          </div>
          <div class="form-group">
            <label>Model Name</label>
            <input type="text" name="local_model" value="{{if eq .Config.LLM.Provider "local"}}{{.Config.LLM.Model}}{{end}}" placeholder="e.g. Qwen, llama3">
          </div>
        </div>

        <div class="btn-group">
          <button type="submit" class="btn btn-primary">Save LLM Settings</button>
          <button type="button" class="btn btn-secondary" onclick="testConnection()">Test Connection</button>
          <span id="test-result" style="font-size: 0.9rem; font-weight: 600;"></span>
        </div>
      </form>
    </div>

    <!-- 5. DIGEST SETTINGS -->
    <div class="card">
      <div class="card-title">Digest Generation Settings</div>
      <form method="POST" action="/api/digest/settings">
        <div class="form-group">
          <label>Output Language</label>
          <input type="text" name="output_language" value="{{.Config.Digest.OutputLanguage}}">
          <div class="help-text">e.g. 'en', 'es', 'de', 'fr'</div>
        </div>
        <div class="form-group">
          <label>Focus Keywords (comma-separated)</label>
          <input type="text" name="focus" value="{{.FocusJoined}}" placeholder="announcements, releases, decisions">
        </div>
        <div class="form-group">
          <label class="channel-cb">
            <input type="checkbox" name="ignore_bots" value="true" {{if .Config.Digest.IgnoreBots}}checked{{end}}>
            <span>Ignore bot messages</span>
          </label>
        </div>
        <button type="submit" class="btn btn-primary">Save Digest Settings</button>
      </form>
    </div>

    <!-- 6. TELEGRAM DELIVERY SETTINGS -->
    <div class="card">
      <div class="card-title">
        <span>Telegram Delivery Configuration</span>
        <span class="help-text">Direct delivery of completed digests over outbound HTTPS</span>
      </div>
      <form method="POST" action="/api/telegram/settings" id="telegram-form">
        <div class="form-group">
          <label class="channel-cb">
            <input type="checkbox" name="enabled" value="true" {{if .TelegramEnabled}}checked{{end}}>
            <span><strong>Enable Telegram Delivery</strong></span>
          </label>
        </div>

        <div class="form-group">
          <label>Bot Token Status</label>
          <div id="tg-token-status" style="margin-bottom: 8px;">
            {{if eq .TelegramTokenSource "environment"}}
              <span class="badge badge-ok">Configured (via environment: TELEGRAM_BOT_TOKEN)</span>
            {{else if eq .TelegramTokenSource "stored"}}
              <span class="badge badge-ok">Configured (stored in secrets.json)</span>
            {{else}}
              <span class="badge badge-warn">Not Configured</span>
            {{end}}
          </div>
          <label>Enter / Update Bot Token</label>
          {{if eq .TelegramTokenSource "environment"}}
            <input type="password" name="token" id="tg-token" placeholder="Managed by environment variable (TELEGRAM_BOT_TOKEN)" disabled>
            <div class="help-text"><strong>Precedence Note:</strong> Environment variable (<code>TELEGRAM_BOT_TOKEN</code>) takes precedence over stored secrets.</div>
          {{else if eq .TelegramTokenSource "stored"}}
            <input type="password" name="token" id="tg-token" placeholder="Configured — leave blank to keep unchanged">
            <div class="help-text">Stored privately in <code>data/secrets.json</code> (0600). Enter a new token to replace it.</div>
          {{else}}
            <input type="password" name="token" id="tg-token" placeholder="Enter bot token from @BotFather (e.g. 123456789:ABCDef...)">
            <div class="help-text">Obtained from Telegram @BotFather. Stored privately in <code>data/secrets.json</code> (0600); never displayed.</div>
          {{end}}
          <div style="margin-top: 8px;">
            <button type="button" class="btn btn-secondary" onclick="testTelegramBot()">Test Bot Token</button>
            <span id="tg-bot-test-result" style="margin-left: 8px; font-size: 0.9rem; font-weight: 600;"></span>
          </div>
        </div>

        <div class="form-group">
          <label>Destination Chat ID</label>
          <div style="display: flex; gap: 8px; margin-bottom: 6px;">
            <input type="text" name="chat_id" id="tg-chat-id" value="{{.TelegramChatID}}" placeholder="e.g. -100123456789 or 987654321" style="flex: 1;">
            <button type="button" class="btn btn-secondary" onclick="discoverTelegramChats()">Discover Chats</button>
          </div>
          <div id="tg-discovered-chats" style="display: none; margin-bottom: 8px;">
            <label style="font-size: 0.85rem; color: var(--text-muted);">Discovered Recent Chats (click to select):</label>
            <div id="tg-chats-list" style="display: flex; flex-direction: column; gap: 6px; max-height: 180px; overflow-y: auto; background: var(--code-bg); padding: 8px; border-radius: 4px; border: 1px solid var(--border);"></div>
            <div id="tg-selected-chat-note" style="display: none; margin-top: 6px; font-size: 0.85rem; color: var(--accent); font-weight: 600;"></div>
          </div>
          <div class="help-text">Send a message to your bot first (e.g. <code>/start</code>), then click 'Discover Chats' to auto-populate.</div>
        </div>

        <div class="form-group">
          <label>Destination Chat Label (optional)</label>
          <input type="text" name="chat_label" id="tg-chat-label" value="{{.TelegramChatLabel}}" placeholder="e.g. CordBrief Daily Updates">
        </div>

        <div class="btn-group">
          <button type="submit" class="btn btn-primary">Save Telegram Settings</button>
          <button type="button" class="btn btn-secondary" onclick="sendTestTelegramMessage()">Send Test Ping</button>
          <span id="tg-ping-result" style="font-size: 0.9rem; font-weight: 600;"></span>
        </div>
      </form>
    </div>

    <!-- 7. MANUAL DIGEST PREVIEW -->
    <div class="card">
      <div class="card-title">
        <span>Manual Digest Preview</span>
        <span class="help-text">Runs proven pipeline without committing cursor or persisting artifact</span>
      </div>
      <p class="help-text" style="margin-bottom: 12px;">Generates a live preview using unconsumed events in the exchange journal.</p>
      <button type="button" class="btn btn-primary" onclick="runPreview()">Preview Current Digest</button>
      <div id="preview-container" style="display: none;">
        <div id="preview-meta" style="margin-top: 16px; font-weight: 600; font-size: 0.85rem; color: var(--text-muted);"></div>
        <div class="preview-box" id="preview-output"></div>
      </div>
    </div>
  </div>

  <script>
    function toggleProviderFields() {
      const p = document.getElementById('llm-provider').value;
      document.getElementById('gemini-fields').style.display = (p === 'gemini') ? 'block' : 'none';
      document.getElementById('local-fields').style.display = (p === 'local') ? 'block' : 'none';
    }
    toggleProviderFields();

    async function testConnection() {
      const resEl = document.getElementById('test-result');
      resEl.textContent = 'Testing connection...';
      resEl.style.color = 'var(--text-muted)';
      try {
        const form = document.getElementById('llm-form');
        const formData = new FormData(form);
        const resp = await fetch('/api/llm/test', {
          method: 'POST',
          body: formData
        });
        const data = await resp.json();
        if (data.ok) {
          resEl.textContent = '✓ ' + data.message;
          resEl.style.color = 'var(--success)';
        } else {
          resEl.textContent = '✗ ' + (data.error || 'Connection failed');
          resEl.style.color = 'var(--danger)';
        }
      } catch (err) {
        resEl.textContent = '✗ Network error testing connection';
        resEl.style.color = 'var(--danger)';
      }
    }

    async function testTelegramBot() {
      const resEl = document.getElementById('tg-bot-test-result');
      resEl.textContent = 'Testing bot...';
      resEl.style.color = 'var(--text-muted)';
      try {
        const tokenInput = document.getElementById('tg-token');
        const params = new URLSearchParams();
        if (tokenInput && tokenInput.value.trim()) {
          params.append('token', tokenInput.value.trim());
        }
        const resp = await fetch('/api/telegram/test', {
          method: 'POST',
          headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
          body: params.toString()
        });
        const data = await resp.json();
        if (data.ok) {
          resEl.textContent = '✓ Bot verified: @' + (data.username || data.first_name) + ' (ID: ' + data.id + ') — Saved to secrets.json';
          resEl.style.color = 'var(--success)';

          const statusEl = document.getElementById('tg-token-status');
          if (statusEl) {
            const badgeText = (data.source === 'environment')
              ? 'Configured (via environment: TELEGRAM_BOT_TOKEN)'
              : 'Configured (stored in secrets.json)';
            statusEl.innerHTML = '<span class="badge badge-ok">' + badgeText + '</span>';
          }
          if (tokenInput && data.source !== 'environment') {
            tokenInput.placeholder = 'Configured — leave blank to keep unchanged';
            tokenInput.value = '';
          }
        } else {
          resEl.textContent = '✗ ' + (data.error || 'Test failed');
          resEl.style.color = 'var(--danger)';
        }
      } catch (err) {
        resEl.textContent = '✗ Network error testing bot';
        resEl.style.color = 'var(--danger)';
      }
    }

    function selectDiscoveredChat(id, label, rowEl) {
      const chatIdInput = document.getElementById('tg-chat-id');
      const chatLabelInput = document.getElementById('tg-chat-label');
      if (chatIdInput) chatIdInput.value = id;
      if (chatLabelInput && label) chatLabelInput.value = label;

      const allRows = document.querySelectorAll('.tg-discovered-item');
      allRows.forEach(r => {
        r.style.borderColor = 'var(--border)';
        r.style.background = 'var(--bg-card)';
        const badge = r.querySelector('.tg-item-badge');
        if (badge) badge.style.display = 'none';
      });

      if (rowEl) {
        rowEl.style.borderColor = 'var(--accent)';
        rowEl.style.background = 'rgba(88, 101, 242, 0.12)';
        const badge = rowEl.querySelector('.tg-item-badge');
        if (badge) badge.style.display = 'inline-block';
      }

      const selNote = document.getElementById('tg-selected-chat-note');
      if (selNote) {
        selNote.textContent = 'Selected: ' + (label || id) + ' (' + id + ')';
        selNote.style.display = 'block';
      }
    }

    async function discoverTelegramChats() {
      const container = document.getElementById('tg-discovered-chats');
      const listEl = document.getElementById('tg-chats-list');
      const selNote = document.getElementById('tg-selected-chat-note');
      if (selNote) selNote.style.display = 'none';
      listEl.innerHTML = '<span style="color: var(--text-muted); font-size: 0.85rem;">Querying updates...</span>';
      container.style.display = 'block';
      try {
        const tokenInput = document.getElementById('tg-token');
        const params = new URLSearchParams();
        if (tokenInput && tokenInput.value.trim()) {
          params.append('token', tokenInput.value.trim());
        }
        const resp = await fetch('/api/telegram/chats', {
          method: 'POST',
          headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
          body: params.toString()
        });
        const data = await resp.json();
        if (data.ok && data.chats && data.chats.length > 0) {
          listEl.innerHTML = '';
          const currentChatId = (document.getElementById('tg-chat-id').value || '').trim();
          let autoSelectTarget = null;
          let autoSelectRow = null;

          // Auto-select ONLY if exactly 1 eligible private chat exists and no destination is already configured
          const privateChats = data.chats.filter(c => c.type === 'private');
          const shouldAutoSelect = (data.chats.length === 1 && privateChats.length === 1 && currentChatId === '');

          data.chats.forEach(c => {
            const isSelected = (currentChatId === c.id);
            const isGroup = (c.type === 'group' || c.type === 'supergroup');
            const icon = isGroup ? '👥' : '👤';
            const typeLabel = (c.type || 'chat').toUpperCase();

            const row = document.createElement('div');
            row.className = 'tg-discovered-item';
            row.style.display = 'flex';
            row.style.justifyContent = 'space-between';
            row.style.alignItems = 'center';
            row.style.padding = '8px 12px';
            row.style.borderRadius = '6px';
            row.style.border = '1px solid ' + (isSelected ? 'var(--accent)' : 'var(--border)');
            row.style.background = isSelected ? 'rgba(88, 101, 242, 0.12)' : 'var(--bg-card)';
            row.style.cursor = 'pointer';
            row.style.transition = 'all 0.15s ease';

            row.innerHTML =
              '<div style="display: flex; align-items: center; gap: 8px;">' +
                '<span style="font-size: 1.1rem;">' + icon + '</span>' +
                '<span style="font-weight: 600; font-size: 0.9rem;">' + c.label + '</span>' +
                '<span style="font-size: 0.8rem; color: var(--text-muted);">(' + c.id + ')</span>' +
              '</div>' +
              '<div style="display: flex; align-items: center; gap: 8px;">' +
                '<span class="badge" style="font-size: 0.75rem; letter-spacing: 0.5px;">' + typeLabel + '</span>' +
                '<span class="badge badge-ok tg-item-badge" style="font-size: 0.75rem; display: ' + (isSelected ? 'inline-block' : 'none') + ';">✓ SELECTED</span>' +
              '</div>';

            row.onclick = () => selectDiscoveredChat(c.id, c.label, row);
            listEl.appendChild(row);

            if (shouldAutoSelect) {
              autoSelectTarget = c;
              autoSelectRow = row;
            }
          });

          if (shouldAutoSelect && autoSelectTarget && autoSelectRow) {
            selectDiscoveredChat(autoSelectTarget.id, autoSelectTarget.label, autoSelectRow);
          }
        } else if (data.ok) {
          listEl.innerHTML = '<span style="color: var(--text-muted); font-size: 0.85rem;">No recent chat interactions found. Send a message (e.g. <code>/start</code>) to the bot on Telegram first, then try again.</span>';
        } else {
          listEl.innerHTML = '<span style="color: var(--danger); font-size: 0.85rem;">Error: ' + (data.error || 'Failed discovering chats') + '</span>';
        }
      } catch (err) {
        listEl.innerHTML = '<span style="color: var(--danger); font-size: 0.85rem;">Network error discovering chats</span>';
      }
    }

    async function sendTestTelegramMessage() {
      const resEl = document.getElementById('tg-ping-result');
      resEl.textContent = 'Sending test ping...';
      resEl.style.color = 'var(--text-muted)';
      try {
        const chatId = document.getElementById('tg-chat-id').value.trim();
        const params = new URLSearchParams();
        if (chatId) params.append('chat_id', chatId);
        const resp = await fetch('/api/telegram/send-test', {
          method: 'POST',
          headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
          body: params.toString()
        });
        const data = await resp.json();
        if (data.ok) {
          resEl.textContent = '✓ Test message sent successfully (ID: ' + data.message_id + ')';
          resEl.style.color = 'var(--success)';
        } else {
          resEl.textContent = '✗ ' + (data.error || 'Send failed');
          resEl.style.color = 'var(--danger)';
        }
      } catch (err) {
        resEl.textContent = '✗ Network error sending ping';
        resEl.style.color = 'var(--danger)';
      }
    }

    async function runPreview() {
      const container = document.getElementById('preview-container');
      const meta = document.getElementById('preview-meta');
      const output = document.getElementById('preview-output');
      container.style.display = 'block';
      meta.textContent = 'Running live digest preview...';
      output.textContent = 'Waiting for LLM synthesis...';

      try {
        const resp = await fetch('/api/digest/preview', { method: 'POST' });
        const data = await resp.json();
        if (data.ok) {
          if (data.empty) {
            meta.textContent = 'Result: No unconsumed journal events to summarize.';
            output.textContent = 'Cursor is up-to-date. Send new Discord messages in watched channels to preview a digest.';
          } else {
            meta.textContent = 'Batch: ' + data.batch_id + ' | Records: ' + data.total_records + ' | Included: ' + data.included_records + ' | Provider: ' + data.provider + ' (' + data.model + ')';
            output.textContent = data.rendered_markdown;
          }
        } else {
          meta.textContent = 'Preview Failed';
          output.textContent = 'Error: ' + data.error;
        }
      } catch (err) {
        meta.textContent = 'Preview Request Error';
        output.textContent = 'Network error: ' + err;
      }
    }
  </script>
</body>
</html>`

const inboxTemplateHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>CordBrief Inbox</title>
  <style>
` + baseCSS + navCSS + `
    .digest-list {
      display: flex;
      flex-direction: column;
      gap: 16px;
    }
    .digest-item {
      background: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 18px 20px;
      text-decoration: none;
      color: inherit;
      display: block;
      transition: border-color 0.15s ease, background 0.15s ease;
    }
    .digest-item:hover {
      border-color: var(--primary);
      background: #1f2330;
    }
    .digest-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 8px;
      gap: 8px;
      flex-wrap: wrap;
    }
    .digest-title {
      font-size: 1.15rem;
      font-weight: 700;
      color: #fff;
    }
    .digest-overview {
      font-size: 0.9rem;
      color: var(--text-muted);
      margin-bottom: 12px;
      line-height: 1.5;
      display: -webkit-box;
      -webkit-line-clamp: 2;
      -webkit-box-orient: vertical;
      overflow: hidden;
    }
    .digest-footer {
      display: flex;
      align-items: center;
      gap: 12px;
      font-size: 0.8rem;
      color: var(--text-muted);
      flex-wrap: wrap;
    }
    .digest-footer span {
      display: flex;
      align-items: center;
      gap: 4px;
    }
    .empty-inbox {
      text-align: center;
      padding: 48px 20px;
      background: var(--card-bg);
      border: 1px dashed var(--border);
      border-radius: 8px;
      color: var(--text-muted);
    }
    .empty-inbox h3 {
      font-size: 1.1rem;
      color: var(--text);
      margin-bottom: 8px;
    }
  </style>
</head>
<body>
  <div class="container">
    <header>
      <div>
        <h1>CordBrief Inbox</h1>
        <p class="help-text">Native Architecture v2 Digest Archive</p>
      </div>
      <div>
        <span class="badge badge-info">{{len .Digests}} {{if eq (len .Digests) 1}}Digest{{else}}Digests{{end}}</span>
      </div>
    </header>

    <nav class="nav-bar">
      <a href="/" class="nav-link">⚙ Setup</a>
      <a href="/inbox" class="nav-link active">📬 Inbox</a>
    </nav>

    {{if .CorruptCount}}
      <div class="alert alert-error">
        ⚠️ {{.CorruptCount}} corrupted digest artifact(s) were isolated and skipped safely.
      </div>
    {{end}}

    <div class="digest-list">
      {{if .Digests}}
        {{range .Digests}}
          <a href="/digests/{{.BatchID}}" class="digest-item">
            <div class="digest-header">
              <div class="digest-title">{{.Title}}</div>
              <div>
                {{if .Trigger}}
                  {{if eq .Trigger.Type "scheduled"}}
                    <span class="badge badge-scheduled">🕒 Scheduled</span>
                  {{else}}
                    <span class="badge badge-manual">👤 Manual</span>
                  {{end}}
                {{else}}
                  <span class="badge badge-manual">👤 Manual</span>
                {{end}}
                {{if .Delivery}}
                  {{if eq .Delivery.State "sent"}}
                    <span class="badge badge-ok">✈ Telegram: Sent</span>
                  {{else if eq .Delivery.State "sending"}}
                    <span class="badge badge-warn">✈ Telegram: Sending ({{.Delivery.NextPart}}/{{.Delivery.TotalParts}})</span>
                  {{else if eq .Delivery.State "pending"}}
                    <span class="badge badge-info">✈ Telegram: Pending</span>
                  {{else if eq .Delivery.State "uncertain"}}
                    <span class="badge badge-err">✈ Telegram: Uncertain</span>
                  {{else if eq .Delivery.State "failed"}}
                    <span class="badge badge-err">✈ Telegram: Failed</span>
                  {{end}}
                {{else}}
                  <span class="badge" style="background: #374151; color: #9ca3af;">✈ Telegram: Not Delivered</span>
                {{end}}
              </div>
            </div>
            <div class="digest-overview">{{.Overview}}</div>
            <div class="digest-footer">
              <span>📅 {{.CreatedAtFormatted}}</span>
              <span>•</span>
              <span>💬 {{.IncludedMessageCount}} messages</span>
              <span>•</span>
              <span>🤖 {{.Provider}} ({{.Model}})</span>
              <span>•</span>
              <span style="font-family: monospace;">ID: {{.ShortBatchID}}</span>
            </div>
          </a>
        {{end}}
      {{else}}
        <div class="empty-inbox">
          <h3>Inbox is empty</h3>
          <p>No digests have been generated yet. Enable the Daily Scheduler or trigger a digest to populate your inbox.</p>
        </div>
      {{end}}
    </div>
  </div>
</body>
</html>`

const digestDetailTemplateHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>{{.Artifact.Digest.Title}} — CordBrief</title>
  <style>
` + baseCSS + navCSS + `
    .back-btn {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      margin-bottom: 16px;
      color: var(--text-muted);
      text-decoration: none;
      font-size: 0.9rem;
      font-weight: 600;
    }
    .back-btn:hover {
      color: var(--text);
    }
    .detail-header {
      margin-bottom: 24px;
    }
    .detail-title {
      font-size: 1.6rem;
      font-weight: 700;
      color: #fff;
      margin-bottom: 12px;
      line-height: 1.3;
    }
    .detail-meta-bar {
      display: flex;
      gap: 10px;
      align-items: center;
      flex-wrap: wrap;
      margin-bottom: 16px;
    }
    .detail-overview-box {
      background: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 20px;
      margin-bottom: 24px;
      line-height: 1.6;
      font-size: 1rem;
      color: #d1d5db;
    }
    .overview-title {
      font-size: 0.85rem;
      text-transform: uppercase;
      font-weight: 700;
      color: var(--text-muted);
      margin-bottom: 8px;
      letter-spacing: 0.5px;
    }
    .section-title {
      font-size: 1.15rem;
      font-weight: 700;
      margin-bottom: 16px;
      color: #fff;
      display: flex;
      align-items: center;
      gap: 8px;
    }
    .items-container {
      display: flex;
      flex-direction: column;
      gap: 12px;
      margin-bottom: 24px;
    }
    .item-card {
      background: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 6px;
      padding: 16px;
    }
    .item-top {
      display: flex;
      align-items: center;
      gap: 8px;
      margin-bottom: 8px;
      flex-wrap: wrap;
    }
    .item-text {
      font-size: 0.95rem;
      line-height: 1.6;
      color: var(--text);
      margin-bottom: 10px;
    }
    .item-sources {
      font-size: 0.8rem;
      color: var(--text-muted);
      display: flex;
      align-items: center;
      gap: 8px;
      flex-wrap: wrap;
      border-top: 1px solid rgba(255, 255, 255, 0.05);
      padding-top: 8px;
    }
    .source-tag {
      background: var(--bg);
      border: 1px solid var(--border);
      padding: 2px 6px;
      border-radius: 4px;
      font-family: monospace;
      font-size: 0.75rem;
      color: var(--text-muted);
    }
    .source-link {
      background: rgba(88, 101, 242, 0.15);
      border: 1px solid var(--primary);
      padding: 2px 6px;
      border-radius: 4px;
      font-family: monospace;
      font-size: 0.75rem;
      color: #8ea1e1;
      text-decoration: none;
      transition: background 0.15s ease;
    }
    .source-link:hover {
      background: rgba(88, 101, 242, 0.3);
      color: #fff;
    }
    .channel-tag {
      font-size: 0.8rem;
      color: var(--text-muted);
      font-weight: 600;
    }
    .audit-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
      gap: 12px;
      font-size: 0.85rem;
    }
    .audit-item {
      background: var(--bg);
      border: 1px solid var(--border);
      padding: 10px 12px;
      border-radius: 6px;
    }
    .audit-label {
      font-size: 0.7rem;
      color: var(--text-muted);
      text-transform: uppercase;
      margin-bottom: 2px;
    }
    .audit-val {
      font-family: monospace;
      font-weight: 600;
      word-break: break-all;
    }
  </style>
</head>
<body>
  <div class="container">
    <a href="/inbox" class="back-btn">← Back to Inbox</a>

    {{if .FlashMessage}}
      <div class="alert alert-success">{{.FlashMessage}}</div>
    {{end}}
    {{if .FlashError}}
      <div class="alert alert-error">{{.FlashError}}</div>
    {{end}}

    <div class="detail-header">
      <div class="detail-meta-bar">
        {{if .Artifact.Trigger}}
          {{if eq .Artifact.Trigger.Type "scheduled"}}
            <span class="badge badge-scheduled">🕒 Scheduled ({{.Artifact.Trigger.SlotID}})</span>
          {{else}}
            <span class="badge badge-manual">👤 Manual Run</span>
          {{end}}
        {{else}}
          <span class="badge badge-manual">👤 Manual Run</span>
        {{end}}
        <span class="badge badge-info">{{.Artifact.Provider}} ({{.Artifact.Model}})</span>
        {{if .Delivery}}
          {{if eq .Delivery.State "sent"}}
            <span class="badge badge-ok">✈ Telegram: Sent</span>
          {{else if eq .Delivery.State "sending"}}
            <span class="badge badge-warn">✈ Telegram: Sending ({{.Delivery.NextPart}}/{{.Delivery.TotalParts}})</span>
          {{else if eq .Delivery.State "pending"}}
            <span class="badge badge-info">✈ Telegram: Pending</span>
          {{else if eq .Delivery.State "uncertain"}}
            <span class="badge badge-err">✈ Telegram: Uncertain</span>
          {{else if eq .Delivery.State "failed"}}
            <span class="badge badge-err">✈ Telegram: Failed</span>
          {{end}}
        {{else}}
          <span class="badge" style="background: #374151; color: #9ca3af;">✈ Telegram: Not Delivered</span>
        {{end}}
        <span class="help-text">Generated on {{.CreatedAtFormatted}}</span>
      </div>
      <h1 class="detail-title">{{.Artifact.Digest.Title}}</h1>
    </div>

    <!-- TELEGRAM DELIVERY CARD -->
    <div class="card" style="margin-bottom: 24px;">
      <div class="card-title">
        <span>Telegram Delivery</span>
        {{if .Delivery}}
          <span class="help-text">Destination: {{if .Delivery.DestinationLabel}}{{.Delivery.DestinationLabel}}{{else}}{{.Delivery.DestinationID}}{{end}}</span>
        {{end}}
      </div>
      {{if .Delivery}}
        {{if .Delivery.LastSafeError}}
          <div style="background: rgba(237, 66, 69, 0.15); border: 1px solid var(--danger); border-radius: 6px; padding: 12px; margin-bottom: 12px; color: #ffa6a8; font-size: 0.9rem;">
            <strong>Delivery Issue:</strong> {{.Delivery.LastSafeError}}
          </div>
        {{end}}
        {{if eq .Delivery.State "uncertain"}}
          <div style="background: rgba(250, 168, 26, 0.15); border: 1px solid var(--warning); border-radius: 6px; padding: 12px; margin-bottom: 12px; color: #fde68a; font-size: 0.9rem;">
            <strong>Ambiguous Outcome:</strong> Transport failed ambiguously after submission (timeout or server error). Messages may have reached Telegram. Confirmation required before retrying to avoid duplicate messages.
          </div>
          <form method="POST" action="/api/digests/{{$.Artifact.BatchID}}/deliver" style="display: inline;">
            <input type="hidden" name="force" value="true">
            <button type="submit" class="btn btn-primary" onclick="return confirm('Delivery outcome was uncertain. Send again anyway?')">Confirm &amp; Resend</button>
          </form>
        {{else if eq .Delivery.State "sent"}}
          <div style="color: var(--success); font-weight: 600; margin-bottom: 12px;">✓ All parts delivered to Telegram ({{len .Delivery.TelegramMessageIDs}} message(s)).</div>
          <form method="POST" action="/api/digests/{{$.Artifact.BatchID}}/deliver" style="display: inline;">
            <input type="hidden" name="force" value="true">
            <button type="submit" class="btn btn-secondary" onclick="return confirm('This digest has already been sent. Send again anyway?')">Send Again Anyway</button>
          </form>
        {{else if eq .Delivery.State "failed"}}
          <form method="POST" action="/api/digests/{{$.Artifact.BatchID}}/deliver" style="display: inline;">
            <input type="hidden" name="force" value="true">
            <button type="submit" class="btn btn-primary">Retry Delivery</button>
          </form>
        {{else}}
          <span class="help-text">Delivery is in progress...</span>
        {{end}}
      {{else}}
        <p class="help-text" style="margin-bottom: 12px;">This digest has not been delivered to Telegram yet.</p>
        <form method="POST" action="/api/digests/{{$.Artifact.BatchID}}/deliver" style="display: inline;">
          <button type="submit" class="btn btn-primary">Send to Telegram</button>
        </form>
      {{end}}
    </div>

    <div class="detail-overview-box">
      <div class="overview-title">Executive Overview</div>
      <p>{{.Artifact.Digest.Overview}}</p>
    </div>

    {{if .Items}}
      <div class="section-title">Key Insights & Attributions</div>
      <div class="items-container">
        {{range .Items}}
          <div class="item-card">
            <div class="item-top">
              <span class="badge badge-kind-{{.Kind}}">{{.Kind}}</span>
              {{if .ChannelContext}}
                <span class="channel-tag">#{{.ChannelContext}}</span>
              {{end}}
            </div>
            <p class="item-text">{{.Text}}</p>
            {{if .Sources}}
              <div class="item-sources">
                <span>Sources:</span>
                {{range .Sources}}
                  {{if .URL}}
                    <a href="{{.URL}}" target="_blank" rel="noopener noreferrer" class="source-link" title="Jump to Discord message (Internal ID: {{.ID}})">{{.Label}} ↗</a>
                  {{else}}
                    <span class="source-tag" title="Internal ID: {{.ID}}">{{.Label}}</span>
                  {{end}}
                {{end}}
              </div>
            {{end}}
          </div>
        {{end}}
      </div>
    {{end}}

    <!-- EXECUTION METADATA / AUDIT TRAIL -->
    <div class="card" style="margin-top: 32px;">
      <div class="card-title" style="font-size: 0.95rem; color: var(--text-muted); text-transform: uppercase;">
        Durable Artifact Metadata
      </div>
      <div class="audit-grid">
        <div class="audit-item">
          <div class="audit-label">Batch ID</div>
          <div class="audit-val">{{.Artifact.BatchID}}</div>
        </div>
        <div class="audit-item">
          <div class="audit-label">Cursor Range</div>
          <div class="audit-val">Seg {{.Artifact.CursorStart.Segment}}:{{.Artifact.CursorStart.Offset}} → Seg {{.Artifact.CursorEnd.Segment}}:{{.Artifact.CursorEnd.Offset}}</div>
        </div>
        <div class="audit-item">
          <div class="audit-label">Messages</div>
          <div class="audit-val">{{.Artifact.IncludedMessageCount}} included / {{.Artifact.InputMessageCount}} total</div>
        </div>
        <div class="audit-item">
          <div class="audit-label">Schema Version</div>
          <div class="audit-val">v{{.Artifact.Version}}</div>
        </div>
      </div>
    </div>
  </div>
</body>
</html>`

var (
	IndexTemplate        = template.Must(template.New("index").Parse(indexTemplateHTML))
	InboxTemplate        = template.Must(template.New("inbox").Parse(inboxTemplateHTML))
	DigestDetailTemplate = template.Must(template.New("detail").Parse(digestDetailTemplateHTML))
)
