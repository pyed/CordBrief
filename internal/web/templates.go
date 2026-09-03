package web

import "html/template"

const indexTemplateHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>CordBrief Setup Control Plane</title>
  <style>
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
      margin-bottom: 24px;
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
    }
    .badge-ok { background: rgba(59, 165, 93, 0.2); color: var(--success); border: 1px solid var(--success); }
    .badge-warn { background: rgba(250, 168, 26, 0.2); color: var(--warning); border: 1px solid var(--warning); }
    .badge-err { background: rgba(237, 66, 69, 0.2); color: var(--danger); border: 1px solid var(--danger); }
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
      grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
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
          <div class="status-label">Discord Authentication</div>
          <div class="status-value">
            {{if .DiscordAuthenticated}}
              <span class="badge badge-ok">Connected</span>
            {{else}}
              <span class="badge badge-warn">Reauth Required</span>
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
      </div>
    </div>

    <!-- 2. WATCHLIST / CHANNELS SELECTION -->
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

    <!-- 3. LLM SETTINGS -->
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

    <!-- 4. DIGEST SETTINGS -->
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

    <!-- 5. MANUAL DIGEST PREVIEW -->
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

var IndexTemplate = template.Must(template.New("index").Parse(indexTemplateHTML))
