package web

import "html/template"

const appCSS = `
    :root {
      --bg: #0f1117;
      --card-bg: #1a1d27;
      --sidebar-bg: #13151f;
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
    }
    .app-layout {
      display: flex;
      min-height: 100vh;
    }
    .sidebar {
      width: 240px;
      background: var(--sidebar-bg);
      border-right: 1px solid var(--border);
      padding: 24px 16px;
      display: flex;
      flex-direction: column;
      flex-shrink: 0;
    }
    .sidebar-brand {
      font-size: 1.25rem;
      font-weight: 700;
      color: #fff;
      margin-bottom: 28px;
      display: flex;
      align-items: center;
      gap: 8px;
      padding-left: 6px;
    }
    .brand-badge {
      background: var(--primary);
      color: #fff;
      font-size: 0.85rem;
      padding: 2px 6px;
      border-radius: 4px;
    }
    .sidebar-nav {
      display: flex;
      flex-direction: column;
      gap: 4px;
      flex-grow: 1;
    }
    .nav-item {
      display: flex;
      align-items: center;
      gap: 10px;
      padding: 10px 14px;
      border-radius: 6px;
      color: var(--text-muted);
      text-decoration: none;
      font-size: 0.92rem;
      font-weight: 500;
      transition: all 0.15s ease;
    }
    .nav-item:hover {
      background: rgba(255, 255, 255, 0.04);
      color: var(--text);
    }
    .nav-item.active {
      background: var(--primary);
      color: #fff;
      font-weight: 600;
    }
    .nav-icon {
      font-size: 1.1rem;
      display: inline-block;
      width: 20px;
      text-align: center;
    }
    .sidebar-footer {
      padding-top: 16px;
      border-top: 1px solid var(--border);
      font-size: 0.75rem;
      color: var(--text-muted);
      padding-left: 6px;
    }
    .main-content {
      flex: 1;
      padding: 32px 40px;
      overflow-y: auto;
      max-width: 1040px;
    }
    @media (max-width: 820px) {
      .app-layout { flex-direction: column; }
      .sidebar {
        width: 100%;
        border-right: none;
        border-bottom: 1px solid var(--border);
        padding: 16px;
      }
      .sidebar-brand { margin-bottom: 12px; }
      .sidebar-nav { flex-direction: row; flex-wrap: wrap; gap: 6px; }
      .sidebar-footer { display: none; }
      .main-content { padding: 20px 16px; }
    }

    .page-header {
      margin-bottom: 24px;
      padding-bottom: 16px;
      border-bottom: 1px solid var(--border);
      display: flex;
      justify-content: space-between;
      align-items: center;
    }
    .page-title {
      font-size: 1.5rem;
      font-weight: 700;
      color: #fff;
    }
    .page-desc {
      font-size: 0.88rem;
      color: var(--text-muted);
      margin-top: 4px;
    }

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
      color: #fff;
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

    .overview-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
      gap: 16px;
      margin-bottom: 24px;
    }
    .overview-card {
      background: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 18px 20px;
      display: flex;
      flex-direction: column;
      justify-content: space-between;
      transition: border-color 0.15s ease;
    }
    .overview-card:hover {
      border-color: #3f4760;
    }
    .overview-card-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 12px;
    }
    .overview-card-title {
      font-size: 0.8rem;
      font-weight: 700;
      color: var(--text-muted);
      text-transform: uppercase;
      letter-spacing: 0.5px;
    }
    .overview-card-value {
      font-size: 1.15rem;
      font-weight: 600;
      color: #fff;
      margin-bottom: 6px;
    }
    .overview-card-sub {
      font-size: 0.85rem;
      color: var(--text-muted);
      line-height: 1.4;
    }
    .overview-card-link {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      margin-top: 14px;
      font-size: 0.85rem;
      color: var(--primary);
      text-decoration: none;
      font-weight: 500;
    }
    .overview-card-link:hover {
      text-decoration: underline;
    }

    .form-group { margin-bottom: 16px; }
    label { display: block; font-size: 0.85rem; font-weight: 600; margin-bottom: 6px; }
    input[type="text"], input[type="password"], input[type="time"], select {
      width: 100%;
      padding: 10px 12px;
      background: var(--bg);
      border: 1px solid var(--border);
      border-radius: 6px;
      color: var(--text);
      font-size: 0.9rem;
    }
    input[type="text"]:focus, input[type="password"]:focus, input[type="time"]:focus, select:focus {
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
    .tree-guild { margin-bottom: 16px; background: var(--bg); border: 1px solid var(--border); border-radius: 6px; padding: 14px; }
    .tree-guild-name { font-weight: 700; margin-bottom: 10px; color: #fff; font-size: 0.95rem; }
    .tree-channels { display: flex; flex-direction: column; gap: 8px; padding-left: 12px; }
    .channel-cb { display: flex; align-items: center; gap: 8px; font-size: 0.9rem; cursor: pointer; }
    .channel-cb input { cursor: pointer; width: 16px; height: 16px; }
    .channel-id { font-size: 0.8rem; color: var(--text-muted); }
    .alert {
      padding: 12px 16px;
      border-radius: 6px;
      margin-bottom: 20px;
      font-size: 0.9rem;
    }
    .alert-success { background: rgba(59, 165, 93, 0.15); border: 1px solid var(--success); color: #85e09f; }
    .alert-error { background: rgba(237, 66, 69, 0.15); border: 1px solid var(--danger); color: #ffa6a8; }
    .help-text { font-size: 0.8rem; color: var(--text-muted); margin-top: 4px; }

    /* Tables */
    .table { width: 100%; border-collapse: collapse; margin-top: 8px; }
    .table th, .table td { padding: 10px 12px; text-align: left; border-bottom: 1px solid var(--border); }
    .table th { font-size: 0.75rem; color: var(--text-muted); text-transform: uppercase; font-weight: 600; }
    .table td { font-size: 0.9rem; }

    /* Inbox */
    .inbox-list { display: flex; flex-direction: column; gap: 12px; }
    .inbox-card {
      background: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 18px 20px;
      text-decoration: none;
      color: inherit;
      display: block;
      transition: all 0.15s ease;
    }
    .inbox-card:hover { border-color: var(--primary); transform: translateY(-1px); }
    .inbox-top { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
    .inbox-meta { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
    .inbox-title { font-size: 1.15rem; font-weight: 600; margin-bottom: 6px; color: #fff; }
    .inbox-overview { font-size: 0.9rem; color: var(--text-muted); line-height: 1.4; }

    /* Digest Detail */
    .back-btn {
      display: inline-block;
      margin-bottom: 16px;
      color: var(--text-muted);
      text-decoration: none;
      font-size: 0.9rem;
      font-weight: 500;
    }
    .back-btn:hover { color: var(--text); }
    .detail-header { margin-bottom: 24px; padding-bottom: 16px; border-bottom: 1px solid var(--border); }
    .detail-title { font-size: 1.6rem; font-weight: 700; margin-top: 8px; color: #fff; }
    .detail-meta-bar { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-bottom: 8px; }
    .detail-overview-box {
      background: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 20px;
      margin-bottom: 24px;
      line-height: 1.6;
    }
    .overview-title { font-size: 1rem; font-weight: 600; color: #fff; margin-bottom: 8px; }
    .items-container { display: flex; flex-direction: column; gap: 16px; margin-bottom: 24px; }
    .item-card {
      background: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 16px 20px;
    }
    .item-top { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
    .channel-tag { font-size: 0.8rem; font-weight: 600; color: var(--text-muted); background: var(--bg); padding: 2px 6px; border-radius: 4px; border: 1px solid var(--border); }
    .item-text { font-size: 0.95rem; line-height: 1.5; margin-bottom: 12px; }
    .item-sources { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: 0.85rem; color: var(--text-muted); }
    .source-link {
      color: var(--primary);
      text-decoration: none;
      font-weight: 600;
      background: rgba(88, 101, 242, 0.1);
      padding: 2px 6px;
      border-radius: 4px;
      border: 1px solid rgba(88, 101, 242, 0.3);
      transition: background 0.15s ease;
    }
    .source-link:hover { background: rgba(88, 101, 242, 0.2); }
    .source-tag {
      background: var(--bg);
      padding: 2px 6px;
      border-radius: 4px;
      border: 1px solid var(--border);
      font-weight: 600;
    }
    .audit-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; }
    .audit-item { background: var(--bg); padding: 10px 12px; border-radius: 6px; border: 1px solid var(--border); }
    .audit-label { font-size: 0.75rem; color: var(--text-muted); text-transform: uppercase; margin-bottom: 2px; }
    .audit-val { font-size: 0.85rem; font-family: monospace; }
`

const layoutShellTop = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>{{.PageTitle}} — CordBrief</title>
  <style>
` + appCSS + `
  </style>
</head>
<body>
  <div class="app-layout">
    <aside class="sidebar">
      <div class="sidebar-brand">
        <span class="brand-badge">⚡</span> CordBrief
      </div>
      <nav class="sidebar-nav">
        <a href="/" class="nav-item {{if eq .ActiveNav "overview"}}active{{end}}">
          <span class="nav-icon">📊</span> Overview
        </a>
        <a href="/inbox" class="nav-item {{if eq .ActiveNav "inbox"}}active{{end}}">
          <span class="nav-icon">📥</span> Inbox
        </a>
        <a href="/channels" class="nav-item {{if eq .ActiveNav "channels"}}active{{end}}">
          <span class="nav-icon">📺</span> Watched Channels
        </a>
        <a href="/schedule" class="nav-item {{if eq .ActiveNav "schedule"}}active{{end}}">
          <span class="nav-icon">⏰</span> Schedule
        </a>
        <a href="/provider" class="nav-item {{if eq .ActiveNav "provider"}}active{{end}}">
          <span class="nav-icon">🤖</span> AI Provider
        </a>
        <a href="/telegram" class="nav-item {{if eq .ActiveNav "telegram"}}active{{end}}">
          <span class="nav-icon">✈️</span> Telegram
        </a>
        <a href="/system" class="nav-item {{if eq .ActiveNav "system"}}active{{end}}">
          <span class="nav-icon">⚙️</span> System
        </a>
      </nav>
      <div class="sidebar-footer">
        <div class="footer-meta">Appliance v2.0.0</div>
      </div>
    </aside>
    <main class="main-content">
      <div class="page-header">
        <div>
          <h1 class="page-title">{{.PageTitle}}</h1>
          {{if .PageDescription}}<p class="page-desc">{{.PageDescription}}</p>{{end}}
        </div>
      </div>

      {{if .FlashMessage}}
        <div class="alert alert-success">{{.FlashMessage}}</div>
      {{end}}
      {{if .FlashError}}
        <div class="alert alert-error">{{.FlashError}}</div>
      {{end}}
`

const layoutShellBottom = `
    </main>
  </div>
</body>
</html>`

func makePageTemplate(name, contentHTML string) *template.Template {
	return template.Must(template.New(name).Parse(layoutShellTop + contentHTML + layoutShellBottom))
}

// -------------------------------------------------------------
// 1. OVERVIEW TEMPLATE
// -------------------------------------------------------------
const overviewTemplateHTML = `
  {{if .ActionRequired}}
    <div style="background: rgba(245, 158, 11, 0.15); border: 1px solid rgba(245, 158, 11, 0.4); border-left: 4px solid #f59e0b; padding: 14px 18px; border-radius: 6px; margin-bottom: 24px;">
      <div style="font-weight: 600; color: #f59e0b; margin-bottom: 4px; font-size: 0.95rem;">Operator Action Required</div>
      <div style="color: #d1d5db; font-size: 0.9rem;">{{.ActionRequired}}</div>
    </div>
  {{end}}
  <div class="overview-grid">
    <!-- 1. Collector Status -->
    <div class="overview-card">
      <div>
        <div class="overview-card-header">
          <span class="overview-card-title">Collector Daemon</span>
          {{if .CollectorStale}}
            <span class="badge badge-err">Collector Offline</span>
          {{else if .CollectorRunning}}
            <span class="badge badge-ok">Running</span>
          {{else if .SetupRequired}}
            <span class="badge badge-warn">Setup Required</span>
          {{else if .ReauthRequired}}
            <span class="badge badge-warn">Reauth Required</span>
          {{else}}
            <span class="badge badge-err">Collector Offline</span>
          {{end}}
        </div>
        <div class="overview-card-value">
          {{if .CollectorStale}}
            Collector Offline
          {{else if .CollectorRunning}}
            Active ({{.CollectorMode}})
          {{else}}
            {{.CollectorStateStr}}
          {{end}}
        </div>
        <div class="overview-card-sub">
          {{if and (not .CollectorStale) .RecoveryStatusStr}}
            Continuity: {{.RecoveryStatusStr}}
          {{else if .CollectorStale}}
            Heartbeat expired; ingestion daemon unreachable.
          {{else}}
            Headless ingestion daemon with 0 published host ports.
          {{end}}
        </div>
      </div>
      <a href="/system" class="overview-card-link">View System Status →</a>
    </div>

    <!-- 2. Discord Session -->
    <div class="overview-card">
      <div>
        <div class="overview-card-header">
          <span class="overview-card-title">Discord Session</span>
          {{if .DiscordAuth}}
            <span class="badge badge-ok">Authenticated</span>
          {{else if .ReauthRequired}}
            <span class="badge badge-warn">Reauth Required</span>
          {{else}}
            <span class="badge badge-err">Disconnected</span>
          {{end}}
        </div>
        <div class="overview-card-value">
          {{if .DiscordAuth}}Session Active{{else}}Action Needed{{end}}
        </div>
        <div class="overview-card-sub">
          {{if .DiscordAuth}}
            Gateway connection healthy and consuming events.
          {{else}}
            Authentication required in Setup Viewer (:28742).
          {{end}}
        </div>
      </div>
      <a href="/system" class="overview-card-link">Manage Authentication →</a>
    </div>

    <!-- 3. Watched Channels -->
    <div class="overview-card">
      <div>
        <div class="overview-card-header">
          <span class="overview-card-title">Watched Channels</span>
          <span class="badge badge-info">Gen {{.WatchedGeneration}}</span>
        </div>
        <div class="overview-card-value">
          {{.WatchedChannelCount}} Channels
        </div>
        <div class="overview-card-sub">
          Selected across {{.CatalogGuildCount}} Discord server(s).
        </div>
      </div>
      <a href="/channels" class="overview-card-link">Configure Channels →</a>
    </div>

    <!-- 4. Last Digest -->
    <div class="overview-card">
      <div>
        <div class="overview-card-header">
          <span class="overview-card-title">Latest Digest</span>
          {{if .HasLastDigest}}
            <span class="badge badge-ok">Available</span>
          {{else}}
            <span class="badge badge-manual">None</span>
          {{end}}
        </div>
        <div class="overview-card-value" style="font-size: 1rem;">
          {{if .HasLastDigest}}{{.LastDigestTitle}}{{else}}No digests synthesized yet{{end}}
        </div>
        <div class="overview-card-sub">
          {{if .HasLastDigest}}{{.LastDigestTime}}{{else}}Awaiting scheduled or manual run{{end}}
        </div>
      </div>
      <a href="/inbox" class="overview-card-link">Open Inbox →</a>
    </div>

    <!-- 5. Daily Schedule -->
    <div class="overview-card">
      <div>
        <div class="overview-card-header">
          <span class="overview-card-title">Daily Schedule</span>
          {{if .ScheduleEnabled}}
            <span class="badge badge-scheduled">Active</span>
          {{else}}
            <span class="badge badge-manual">Paused</span>
          {{end}}
        </div>
        <div class="overview-card-value">
          {{if .ScheduleEnabled}}Daily at {{.ScheduleTime}}{{else}}Disabled{{end}}
        </div>
        <div class="overview-card-sub">
          {{if .ScheduleEnabled}}
            Next due: {{.ScheduleNextDue}} ({{.ScheduleTimezone}})
          {{else}}
            Automated synthesis is currently paused.
          {{end}}
        </div>
      </div>
      <a href="/schedule" class="overview-card-link">Edit Schedule →</a>
    </div>

    <!-- 6. Telegram Delivery -->
    <div class="overview-card">
      <div>
        <div class="overview-card-header">
          <span class="overview-card-title">Telegram Delivery</span>
          {{if .TelegramEnabled}}
            <span class="badge badge-ok">Enabled</span>
          {{else}}
            <span class="badge badge-manual">Disabled</span>
          {{end}}
        </div>
        <div class="overview-card-value">
          {{if .TelegramConfigured}}Configured{{else}}Not Configured{{end}}
        </div>
        <div class="overview-card-sub">
          {{if .TelegramDestination}}
            Destination: {{.TelegramDestination}}
          {{else}}
            Direct digest delivery to Telegram chat/DM.
          {{end}}
        </div>
      </div>
      <a href="/telegram" class="overview-card-link">Configure Telegram →</a>
    </div>

    <!-- 7. AI Provider -->
    <div class="overview-card">
      <div>
        <div class="overview-card-header">
          <span class="overview-card-title">AI Provider</span>
          <span class="badge badge-info">{{.AIProvider}}</span>
        </div>
        <div class="overview-card-value" style="font-size: 1rem;">
          {{.AIModel}}
        </div>
        <div class="overview-card-sub">
          Key source: {{.AIKeySource}}
        </div>
      </div>
      <a href="/provider" class="overview-card-link">Provider Settings →</a>
    </div>

    <!-- 8. Core Appliance -->
    <div class="overview-card">
      <div>
        <div class="overview-card-header">
          <span class="overview-card-title">Core Appliance</span>
          <span class="badge badge-ok">Healthy</span>
        </div>
        <div class="overview-card-value">
          Port {{.CorePort}}
        </div>
        <div class="overview-card-sub">
          Unconsumed backlog: {{.BacklogCount}} bytes.
        </div>
      </div>
      <a href="/system" class="overview-card-link">System Telemetry →</a>
    </div>
  </div>
`

// -------------------------------------------------------------
// 2. CHANNELS TEMPLATE
// -------------------------------------------------------------
const channelsTemplateHTML = `
  <form method="POST" action="/api/watchlist">
    <div class="card">
      <div class="card-title">
        <div>Channel Watchlist</div>
        <div style="display: flex; gap: 12px; align-items: center;">
          <span class="badge badge-info">Catalog: {{.CatalogState}}</span>
          <span class="help-text"><strong id="selectedCount">{{.WatchedCount}}</strong> channels selected (Gen {{.WatchedGeneration}})</span>
        </div>
      </div>

      <div class="form-group">
        <input type="text" id="channelSearch" placeholder="Filter channels by name..." oninput="filterChannels(this.value)">
      </div>

      {{if .Catalog}}
        {{range .Catalog.Guilds}}
          <div class="tree-guild" data-guild-name="{{.Name}}">
            <div class="tree-guild-name">📁 {{.Name}}</div>
            <div class="tree-channels">
              {{range .Channels}}
                <label class="channel-cb" data-channel-name="{{.Name}}">
                  <input type="checkbox" name="channels" value="{{.ID}}" {{if index $.WatchedSet .ID}}checked{{end}} onchange="updateCount()">
                  <span class="channel-name">#{{.Name}}</span>
                  <span class="channel-id">({{.ID}})</span>
                </label>
              {{end}}
            </div>
          </div>
        {{end}}
      {{else}}
        <div class="alert" style="background: rgba(250, 168, 26, 0.15); border: 1px solid var(--warning); color: #fde68a;">
          Channel catalog is currently unavailable. Ensure the collector is running and authenticated with Discord.
        </div>
      {{end}}

      <div class="btn-group">
        <button type="submit" class="btn btn-primary">Save Watchlist</button>
      </div>
    </div>
  </form>

  <script>
    function filterChannels(query) {
      const q = query.toLowerCase().trim();
      document.querySelectorAll('.tree-guild').forEach(guild => {
        let visibleInGuild = 0;
        guild.querySelectorAll('.channel-cb').forEach(label => {
          const name = label.getAttribute('data-channel-name') || '';
          const match = !q || name.toLowerCase().includes(q);
          label.style.display = match ? 'flex' : 'none';
          if (match) visibleInGuild++;
        });
        guild.style.display = visibleInGuild > 0 ? 'block' : 'none';
      });
    }

    function updateCount() {
      const count = document.querySelectorAll('input[name="channels"]:checked').length;
      const el = document.getElementById('selectedCount');
      if (el) el.textContent = count;
    }
  </script>
`

// -------------------------------------------------------------
// 3. SCHEDULE TEMPLATE
// -------------------------------------------------------------
const scheduleTemplateHTML = `
  <div class="status-grid" style="margin-bottom: 24px;">
    <div class="status-item">
      <div class="status-label">Schedule Status</div>
      <div class="status-value">
        {{if .Config.Enabled}}
          <span class="badge badge-ok">Active</span>
        {{else}}
          <span class="badge badge-manual">Paused</span>
        {{end}}
      </div>
    </div>
    <div class="status-item">
      <div class="status-label">Next Due Run</div>
      <div class="status-value" style="font-size: 0.9rem;">
        {{if .Config.Enabled}}
          {{.NextDueFormatted}} ({{.NextDueDuration}})
        {{else}}
          Not scheduled
        {{end}}
      </div>
    </div>
    <div class="status-item">
      <div class="status-label">Last Completed Slot</div>
      <div class="status-value" style="font-size: 0.85rem; font-family: monospace;">
        {{if .State.LastCompletedSlot}}{{.State.LastCompletedSlot}}{{else}}None{{end}}
      </div>
    </div>
    <div class="status-item">
      <div class="status-label">Last Run Result</div>
      <div class="status-value">
        {{if eq .State.LastResult "success"}}
          <span class="badge badge-ok">Success</span>
        {{else if eq .State.LastResult "empty"}}
          <span class="badge badge-info">Empty Backlog</span>
        {{else if eq .State.LastResult "error"}}
          <span class="badge badge-err">Error</span>
        {{else}}
          <span class="badge badge-manual">{{if .State.LastResult}}{{.State.LastResult}}{{else}}None{{end}}</span>
        {{end}}
      </div>
    </div>
  </div>

  <form method="POST" action="/api/schedule">
    <div class="card">
      <div class="card-title">Daily Schedule Configuration</div>
      <div class="form-group">
        <label class="channel-cb">
          <input type="checkbox" name="enabled" value="true" {{if .Config.Enabled}}checked{{end}}>
          <span>Enable Autonomous Daily Digest Generation</span>
        </label>
        <p class="help-text">When enabled, CordBrief checks at the configured time every day and synthesizes unread messages.</p>
      </div>

      <div class="form-group">
        <label for="schedTime">Daily Execution Time</label>
        <input type="time" id="schedTime" name="time" value="{{.Config.Time}}" required style="max-width: 200px;">
        <p class="help-text">24-hour local time format (HH:MM).</p>
      </div>

      <div class="form-group">
        <label for="schedTz">Execution Timezone</label>
        <input type="text" id="schedTz" name="timezone" list="tzOptions" value="{{.Config.Timezone}}" required style="max-width: 360px;">
        <datalist id="tzOptions">
          <option value="Asia/Riyadh">
          <option value="UTC">
          <option value="America/New_York">
          <option value="America/Los_Angeles">
          <option value="America/Chicago">
          <option value="Europe/London">
          <option value="Europe/Paris">
          <option value="Asia/Tokyo">
          <option value="Asia/Dubai">
        </datalist>
        <p class="help-text">IANA timezone identifier (e.g. Asia/Riyadh, UTC).</p>
      </div>

      <div class="btn-group">
        <button type="submit" class="btn btn-primary">Save Schedule</button>
      </div>
    </div>
  </form>
`

// -------------------------------------------------------------
// 4. PROVIDER TEMPLATE
// -------------------------------------------------------------
const providerTemplateHTML = `
  <form method="POST" action="/api/llm" id="llmForm">
    <div class="card">
      <div class="card-title">LLM Synthesis Provider</div>

      <div class="form-group">
        <label>Provider Type</label>
        <div style="display: flex; gap: 16px; margin-top: 6px;">
          <label class="channel-cb">
            <input type="radio" name="provider" value="gemini" {{if eq .LLMConfig.Provider "gemini"}}checked{{end}} onchange="toggleProviderUI()">
            <span>Google Gemini Cloud (Recommended)</span>
          </label>
          <label class="channel-cb">
            <input type="radio" name="provider" value="local" {{if eq .LLMConfig.Provider "local"}}checked{{end}} onchange="toggleProviderUI()">
            <span>Local / OpenAI-Compatible (Ollama, vLLM, LM Studio)</span>
          </label>
        </div>
      </div>

      <!-- GEMINI SECTION -->
      <div id="geminiSection" style="{{if eq .LLMConfig.Provider "local"}}display:none;{{end}}">
        <div class="form-group">
          <label for="geminiModel">Gemini Model</label>
          <select id="geminiModel" name="gemini_model">
            <option value="gemini-3.7-flash" {{if eq .LLMConfig.Model "gemini-3.7-flash"}}selected{{end}}>gemini-3.7-flash (Default / Recommended)</option>
            <option value="gemini-2.5-flash" {{if eq .LLMConfig.Model "gemini-2.5-flash"}}selected{{end}}>gemini-2.5-flash</option>
            <option value="gemini-2.5-pro" {{if eq .LLMConfig.Model "gemini-2.5-pro"}}selected{{end}}>gemini-2.5-pro</option>
          </select>
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
            <input type="password" name="gemini_api_key" placeholder="Managed by environment variable (GEMINI_API_KEY)" disabled style="max-width: 450px;">
            <div class="help-text"><strong>Precedence Note:</strong> Environment variable (<code>GEMINI_API_KEY</code>) takes precedence over stored secrets. Update <code>.env</code> to change this key.</div>
          {{else if eq .GeminiKeySource "stored"}}
            <input type="password" name="gemini_api_key" placeholder="Configured — leave blank to keep unchanged" style="max-width: 450px;">
            <div class="help-text">Stored privately in <code>data/secrets.json</code> (0600). Enter a new key to replace it.</div>
          {{else}}
            <input type="password" name="gemini_api_key" placeholder="Enter Gemini API key (AIzaSy...)" style="max-width: 450px;">
            <div class="help-text">Obtained from Google AI Studio. Stored privately in <code>data/secrets.json</code> (0600); never displayed.</div>
          {{end}}
        </div>
      </div>

      <!-- LOCAL SECTION -->
      <div id="localSection" style="{{if ne .LLMConfig.Provider "local"}}display:none;{{end}}">
        <div class="form-group">
          <label for="localBaseURL">OpenAI-Compatible Base URL</label>
          <input type="text" id="localBaseURL" name="local_base_url" value="{{.LLMConfig.BaseURL}}" placeholder="http://host.docker.internal:11434/v1">
        </div>
        <div class="form-group">
          <label for="localModel">Model Name</label>
          <input type="text" id="localModel" name="local_model" value="{{.LLMConfig.Model}}" placeholder="llama3:latest">
        </div>
      </div>

      <div class="btn-group">
        <button type="submit" class="btn btn-primary">Save Provider Settings</button>
        <button type="button" class="btn btn-secondary" onclick="testConnection()">Test Connection</button>
      </div>

      <div id="connectionTestResult"></div>
    </div>
  </form>

  <form method="POST" action="/api/digest/settings">
    <div class="card">
      <div class="card-title">Digest Generation & Synthesis Focus</div>

      <div class="form-group">
        <label for="outputLang">Output Language</label>
        <input type="text" id="outputLang" name="output_language" value="{{.DigestConfig.OutputLanguage}}" placeholder="en, ar, etc." style="max-width: 200px;">
        <p class="help-text">Target natural language for digest title, executive overview, and key insights.</p>
      </div>

      <div class="form-group">
        <label for="focusAreas">Synthesis Focus Topics</label>
        <input type="text" id="focusAreas" name="focus" value="{{.FocusJoined}}" placeholder="security, announcements, releases, critical">
        <p class="help-text">Comma-separated topics given priority weighting during synthesis.</p>
      </div>

      <div class="form-group">
        <label class="channel-cb">
          <input type="checkbox" name="ignore_bots" value="true" {{if .DigestConfig.IgnoreBots}}checked{{end}}>
          <span>Ignore Bot Messages During Synthesis</span>
        </label>
      </div>

      <div class="btn-group">
        <button type="submit" class="btn btn-primary">Save Generation Settings</button>
      </div>
    </div>
  </form>

  <script>
    function toggleProviderUI() {
      const isLocal = document.querySelector('input[name="provider"]:checked').value === 'local';
      document.getElementById('geminiSection').style.display = isLocal ? 'none' : 'block';
      document.getElementById('localSection').style.display = isLocal ? 'block' : 'none';
    }

    function testConnection() {
      const resEl = document.getElementById('connectionTestResult');
      resEl.innerHTML = '<div style="margin-top: 12px; color: var(--text-muted);">Testing connection to provider...</div>';
      const form = document.getElementById('llmForm');
      const data = new FormData(form);

      fetch('/api/llm/test', {
        method: 'POST',
        body: new URLSearchParams(data)
      })
      .then(r => r.json())
      .then(res => {
        if (res.ok) {
          resEl.innerHTML = '<div class="alert alert-success" style="margin-top: 12px;">✓ ' + res.message + '</div>';
        } else {
          resEl.innerHTML = '<div class="alert alert-error" style="margin-top: 12px;">✗ ' + (res.error || 'Connection failed') + '</div>';
        }
      })
      .catch(err => {
        resEl.innerHTML = '<div class="alert alert-error" style="margin-top: 12px;">✗ Network error: ' + err.message + '</div>';
      });
    }
  </script>
`

// -------------------------------------------------------------
// 5. TELEGRAM TEMPLATE
// -------------------------------------------------------------
const telegramTemplateHTML = `
  <form method="POST" action="/api/telegram/settings" id="tgForm">
    <div class="card">
      <div class="card-title">
        <div>Telegram Delivery Configuration</div>
        {{if .Enabled}}
          <span class="badge badge-ok">Enabled</span>
        {{else}}
          <span class="badge badge-manual">Disabled</span>
        {{end}}
      </div>

      <div class="form-group">
        <label class="channel-cb">
          <input type="checkbox" name="enabled" value="true" {{if .Enabled}}checked{{end}}>
          <span>Enable Automatic Telegram Digest Delivery</span>
        </label>
        <p class="help-text">Automatically deliver completed daily digests to the configured Telegram destination.</p>
      </div>

      <!-- BOT TOKEN STATUS -->
      <div class="form-group" style="margin-top: 20px;">
        <label>Bot Token Status</label>
        <div style="margin-bottom: 8px;">
          {{if eq .TokenSource "environment"}}
            <span class="badge badge-ok">CONFIGURED (FROM ENVIRONMENT)</span>
          {{else if eq .TokenSource "stored"}}
            <span class="badge badge-ok">CONFIGURED (STORED IN SECRETS.JSON)</span>
          {{else}}
            <span class="badge badge-err">NOT CONFIGURED</span>
          {{end}}
        </div>

        {{if ne .TokenSource "environment"}}
          <label for="tgToken" style="margin-top: 12px;">Enter / Update Bot Token</label>
          <div style="display: flex; gap: 10px; max-width: 600px;">
            <input type="password" id="tgToken" name="token" placeholder="Enter BotFather bot token (e.g. 123456789:ABCDef...)">
            <button type="button" class="btn btn-secondary" style="white-space: nowrap;" onclick="testTelegramBot()">Test Bot Token</button>
          </div>
          <p class="help-text">Tokens are stored securely in <code>data/secrets.json</code> (0600) and never logged or exposed.</p>
          <div id="tgBotStatus" style="margin-top: 8px;"></div>
        {{end}}
      </div>

      <!-- DESTINATION DISCOVERY -->
      <div class="form-group" style="margin-top: 24px;">
        <label>Destination Chat Discovery</label>
        <p class="help-text" style="margin-bottom: 12px;">Send <code>/start</code> or any message to your bot in Telegram, then click Discover Chats.</p>
        <button type="button" class="btn btn-secondary" onclick="discoverTelegramChats()">Discover Chats</button>
        <div id="tgDiscoveredChats"></div>
      </div>

      <!-- SELECTED DESTINATION -->
      <div class="form-group" style="margin-top: 20px;">
        <label>Selected Destination</label>
        <div style="background: var(--bg); border: 1px solid var(--border); border-radius: 6px; padding: 12px; margin-top: 6px; max-width: 500px;" id="destDisplay">
          {{if .ChatID}}
            <strong>{{if .ChatLabel}}{{.ChatLabel}}{{else}}Chat Destination{{end}}</strong>
            <span style="color: var(--text-muted);">({{.ChatID}})</span>
          {{else}}
            <span style="color: var(--text-muted);">No destination selected yet. Use Discover Chats above.</span>
          {{end}}
        </div>
        <input type="hidden" id="selectedChatID" name="chat_id" value="{{.ChatID}}">
        <input type="hidden" id="selectedChatLabel" name="chat_label" value="{{.ChatLabel}}">
      </div>

      <div class="btn-group" style="margin-top: 24px;">
        <button type="submit" class="btn btn-primary">Save Telegram Settings</button>
        <button type="button" class="btn btn-secondary" onclick="sendTelegramTestPing()">Send Test Ping</button>
      </div>

      <div id="tgPingResult"></div>
    </div>
  </form>

  <script>
    function testTelegramBot() {
      const tokenInput = document.getElementById('tgToken');
      const token = tokenInput ? tokenInput.value.trim() : '';
      const statusEl = document.getElementById('tgBotStatus');
      statusEl.innerHTML = '<span style="color: var(--text-muted);">Testing bot token...</span>';

      const data = new URLSearchParams();
      if (token) data.append('token', token);

      fetch('/api/telegram/test', {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: data
      })
      .then(r => r.json())
      .then(res => {
        if (res.ok) {
          statusEl.innerHTML = '<span class="badge badge-ok">✓ Connected: @' + res.username + ' (' + res.first_name + ')</span>';
        } else {
          statusEl.innerHTML = '<span class="badge badge-err">✗ ' + (res.error || 'Test failed') + '</span>';
        }
      })
      .catch(err => {
        statusEl.innerHTML = '<span class="badge badge-err">✗ Network error: ' + err.message + '</span>';
      });
    }

    function discoverTelegramChats() {
      const tokenInput = document.getElementById('tgToken');
      const token = tokenInput ? tokenInput.value.trim() : '';
      const chatsEl = document.getElementById('tgDiscoveredChats');
      chatsEl.innerHTML = '<div style="margin-top: 10px; color: var(--text-muted);">Discovering chats from recent updates...</div>';

      const data = new URLSearchParams();
      if (token) data.append('token', token);

      fetch('/api/telegram/chats', {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: data
      })
      .then(r => r.json())
      .then(res => {
        if (!res.ok) {
          chatsEl.innerHTML = '<div class="alert alert-error" style="margin-top:10px;">' + (res.error || 'Failed discovering chats') + '</div>';
          return;
        }
        if (!res.chats || res.chats.length === 0) {
          chatsEl.innerHTML = '<div class="alert" style="background: rgba(250, 168, 26, 0.15); border: 1px solid var(--warning); color: #fde68a; margin-top:10px;">No chats found yet. Send /start or a message to your bot in Telegram first, then try again.</div>';
          return;
        }

        let html = '<table class="table" style="margin-top:12px;"><thead><tr><th style="width:40px;">Pick</th><th>Destination</th><th>Type</th><th>Chat ID</th></tr></thead><tbody>';
        res.chats.forEach(c => {
          html += '<tr>' +
            '<td><input type="radio" name="chat_pick" value="' + c.id + '" data-label="' + c.label + '" onchange="selectChat(this)"></td>' +
            '<td><strong>' + c.label + '</strong></td>' +
            '<td><span class="badge badge-info">' + c.type + '</span></td>' +
            '<td><code>' + c.id + '</code></td>' +
            '</tr>';
        });
        html += '</tbody></table>';
        chatsEl.innerHTML = html;
      })
      .catch(err => {
        chatsEl.innerHTML = '<div class="alert alert-error" style="margin-top:10px;">Network error: ' + err.message + '</div>';
      });
    }

    function selectChat(radio) {
      const id = radio.value;
      const label = radio.getAttribute('data-label');
      document.getElementById('selectedChatID').value = id;
      document.getElementById('selectedChatLabel').value = label;
      document.getElementById('destDisplay').innerHTML = '<strong>' + label + '</strong> <span style="color:var(--text-muted);">(' + id + ')</span>';
    }

    function sendTelegramTestPing() {
      const chatId = document.getElementById('selectedChatID').value.trim();
      const resEl = document.getElementById('tgPingResult');
      if (!chatId) {
        resEl.innerHTML = '<div class="alert alert-error" style="margin-top:12px;">Select a chat destination first before sending a test ping.</div>';
        return;
      }
      resEl.innerHTML = '<div style="margin-top: 12px; color: var(--text-muted);">Sending test ping...</div>';
      const data = new URLSearchParams();
      data.append('chat_id', chatId);

      fetch('/api/telegram/send-test', {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: data
      })
      .then(r => r.json())
      .then(res => {
        if (res.ok) {
          resEl.innerHTML = '<div class="alert alert-success" style="margin-top:12px;">✓ Test ping delivered successfully! Check your Telegram client.</div>';
        } else {
          resEl.innerHTML = '<div class="alert alert-error" style="margin-top:12px;">✗ ' + (res.error || 'Failed sending test ping') + '</div>';
        }
      })
      .catch(err => {
        resEl.innerHTML = '<div class="alert alert-error" style="margin-top:12px;">✗ Network error: ' + err.message + '</div>';
      });
    }
  </script>
`

// -------------------------------------------------------------
// 6. SYSTEM TEMPLATE
// -------------------------------------------------------------
const systemTemplateHTML = `
  <div class="card">
    <div class="card-title">Core Runtime Service</div>
    <div class="status-grid">
      <div class="status-item">
        <div class="status-label">Service Health</div>
        <div class="status-value"><span class="badge badge-ok">Healthy</span></div>
      </div>
      <div class="status-item">
        <div class="status-label">HTTP Address</div>
        <div class="status-value" style="font-size: 0.9rem; font-family: monospace;">127.0.0.1:{{.CorePort}}</div>
      </div>
      <div class="status-item">
        <div class="status-label">Data Directory</div>
        <div class="status-value" style="font-size: 0.85rem; font-family: monospace;">{{.DataDir}}</div>
      </div>
      <div class="status-item">
        <div class="status-label">Exchange Directory</div>
        <div class="status-value" style="font-size: 0.85rem; font-family: monospace;">{{.ExchangeDir}}</div>
      </div>
    </div>
  </div>

  <div class="card">
    <div class="card-title">
      <div>Collector Daemon</div>
      {{if .CollectorRunning}}
        <span class="badge badge-ok">Running</span>
      {{else}}
        <span class="badge badge-err">{{.CollectorStateStr}}</span>
      {{end}}
    </div>
    <div class="status-grid">
      <div class="status-item">
        <div class="status-label">Collector State</div>
        <div class="status-value">{{.CollectorStateStr}}</div>
      </div>
      <div class="status-item">
        <div class="status-label">Operational Mode</div>
        <div class="status-value"><span class="badge badge-info">{{.CollectorMode}}</span></div>
      </div>
      <div class="status-item">
        <div class="status-label">Discord Session</div>
        <div class="status-value">
          {{if .DiscordAuth}}
            <span class="badge badge-ok">Authenticated</span>
          {{else}}
            <span class="badge badge-warn">Reauth Needed</span>
          {{end}}
        </div>
      </div>
      <div class="status-item">
        <div class="status-label">Published Ports</div>
        <div class="status-value"><span class="badge badge-ok">0 (Hardened)</span></div>
      </div>
    </div>

    {{if .ActionRequired}}
      <div style="background: rgba(245, 158, 11, 0.15); border: 1px solid rgba(245, 158, 11, 0.4); border-left: 4px solid #f59e0b; padding: 12px 16px; border-radius: 6px; margin-top: 16px;">
        <div style="font-weight: 600; color: #f59e0b; font-size: 0.9rem; margin-bottom: 2px;">Operator Action Required</div>
        <div style="color: #d1d5db; font-size: 0.85rem;">{{.ActionRequired}}</div>
      </div>
    {{end}}

    <div class="btn-group" style="margin-top: 16px;">
      {{if eq .CollectorMode "normal"}}
        <form method="POST" action="/api/collector/command">
          <input type="hidden" name="command" value="enter_reauth">
          <button type="submit" class="btn btn-secondary">Request Reauth Mode</button>
        </form>
      {{else if eq .CollectorMode "reauth"}}
        <form method="POST" action="/api/collector/command">
          <input type="hidden" name="command" value="return_normal">
          <button type="submit" class="btn btn-primary">Return to Normal Mode</button>
        </form>
      {{end}}
    </div>
  </div>

  <div class="card">
    <div class="card-title">Journal & Storage Telemetry</div>
    <div class="status-grid">
      <div class="status-item">
        <div class="status-label">Active Segment</div>
        <div class="status-value">Segment {{.ActiveSegment}}</div>
      </div>
      <div class="status-item">
        <div class="status-label">Physical Journal Size</div>
        <div class="status-value">{{.JournalSizeBytes}} bytes</div>
      </div>
      <div class="status-item">
        <div class="status-label">Committed Cursor</div>
        <div class="status-value" style="font-size: 0.85rem; font-family: monospace;">Seg {{.CommittedCursor.Segment}}, Offset {{.CommittedCursor.Offset}}</div>
      </div>
      <div class="status-item">
        <div class="status-label">Unconsumed Backlog</div>
        <div class="status-value">{{.UnconsumedBytes}} bytes</div>
      </div>
    </div>
  </div>

  <div class="card">
    <div class="card-title">Catalog & Continuity Telemetry</div>
    <div class="status-grid">
      <div class="status-item">
        <div class="status-label">Catalog State</div>
        <div class="status-value"><span class="badge badge-ok">{{.CatalogState}}</span></div>
      </div>
      <div class="status-item">
        <div class="status-label">Catalog Updated</div>
        <div class="status-value" style="font-size: 0.85rem;">{{.CatalogUpdatedFormatted}}</div>
      </div>
      <div class="status-item">
        <div class="status-label">Recovery State</div>
        <div class="status-value"><span class="badge badge-info">{{.RecoveryState}}</span></div>
      </div>
      <div class="status-item">
        <div class="status-label">Pending Recovery Channels</div>
        <div class="status-value">{{.RecoveryPendingChannels}}</div>
      </div>
    </div>
  </div>

  <div class="card">
    <div class="card-title">Security & Secret Hygiene</div>
    <p class="help-text">
      Zero secret keys, Discord tokens, session cookies, or raw message payloads are ever logged, printed, or rendered in this interface.
      All mutations require valid origin/CSRF headers.
    </p>
  </div>
`

// -------------------------------------------------------------
// 7. INBOX TEMPLATE
// -------------------------------------------------------------
const inboxTemplateHTML = `
  {{if gt .CorruptCount 0}}
    <div class="alert alert-error">
      <strong>Notice:</strong> {{.CorruptCount}} corrupted digest artifact(s) were isolated and excluded from display.
    </div>
  {{end}}

  <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px;">
    <div style="color: var(--text-muted); font-size: 0.9rem;">
      <span class="badge badge-info">{{len .Digests}} Digests</span>
    </div>
  </div>

  {{if .Digests}}
    <div class="inbox-list">
      {{range .Digests}}
        <a href="/digests/{{.BatchID}}" class="inbox-card">
          <div class="inbox-top">
            <div class="inbox-meta">
              {{if .Trigger}}
                {{if eq .Trigger.Type "scheduled"}}
                  <span class="badge badge-scheduled">🕒 Scheduled</span>
                {{else}}
                  <span class="badge badge-manual">👤 Manual</span>
                {{end}}
              {{else}}
                <span class="badge badge-manual">👤 Manual</span>
              {{end}}
              <span class="badge badge-info">{{.Provider}} ({{.Model}})</span>
              {{if .Delivery}}
                {{if eq .Delivery.State "sent"}}
                  <span class="badge badge-ok">✈ Telegram: Sent</span>
                {{else if eq .Delivery.State "sending"}}
                  <span class="badge badge-warn">✈ Telegram: Sending</span>
                {{else if eq .Delivery.State "pending"}}
                  <span class="badge badge-info">✈ Telegram: Pending</span>
                {{else if eq .Delivery.State "uncertain"}}
                  <span class="badge badge-err">✈ Telegram: Uncertain</span>
                {{else if eq .Delivery.State "failed"}}
                  <span class="badge badge-err">✈ Telegram: Failed</span>
                {{end}}
              {{end}}
            </div>
            <span class="help-text">{{.CreatedAtFormatted}}</span>
          </div>
          <div class="inbox-title">{{.Title}}</div>
          <div class="inbox-overview">{{.Overview}}</div>
        </a>
      {{end}}
    </div>
  {{else}}
    <div class="card" style="text-align: center; padding: 48px 20px;">
      <div style="font-size: 2.5rem; margin-bottom: 12px;">📥</div>
      <h2 style="font-size: 1.25rem; font-weight: 600; margin-bottom: 8px;">Inbox is empty</h2>
      <p class="help-text" style="max-width: 480px; margin: 0 auto 20px auto;">
        <span class="badge badge-info">0 Digests</span>
        <br><br>
        CordBrief synthesizes daily digests from your watched channels autonomously based on your schedule.
      </p>
      <a href="/schedule" class="btn btn-primary">Check Schedule Settings</a>
    </div>
  {{end}}
`

// -------------------------------------------------------------
// 8. DIGEST DETAIL TEMPLATE
// -------------------------------------------------------------
const digestDetailTemplateHTML = `
  <a href="/inbox" class="back-btn">← Back to Inbox</a>

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
          <strong>Ambiguous Outcome:</strong> Transport failed ambiguously after submission. Confirmation required before retrying to avoid duplicates.
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
    <div class="card-title" style="margin-top: 24px; margin-bottom: 16px;">Key Insights & Attributions</div>
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
`

var (
	OverviewTemplate     = makePageTemplate("overview", overviewTemplateHTML)
	IndexTemplate        = OverviewTemplate // backward compatibility alias
	InboxTemplate        = makePageTemplate("inbox", inboxTemplateHTML)
	DigestDetailTemplate = makePageTemplate("detail", digestDetailTemplateHTML)
	ChannelsTemplate     = makePageTemplate("channels", channelsTemplateHTML)
	ScheduleTemplate     = makePageTemplate("schedule", scheduleTemplateHTML)
	ProviderTemplate     = makePageTemplate("provider", providerTemplateHTML)
	TelegramTemplate     = makePageTemplate("telegram", telegramTemplateHTML)
	SystemTemplate       = makePageTemplate("system", systemTemplateHTML)
)
