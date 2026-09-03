package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cordbrief/internal/catalog"
	"cordbrief/internal/config"
	"cordbrief/internal/digest"
	"cordbrief/internal/journal"
	"cordbrief/internal/llm"
)

// ServerOptions configures the Core Web Control Plane.
type ServerOptions struct {
	ExchangeDir string
	DataDir     string
	Store       *config.Store
}

// Server serves the Core Web Control Plane.
type Server struct {
	exchangeDir string
	dataDir     string
	store       *config.Store
	mux         *http.ServeMux
}

// NewServer initializes the Core web control plane handler.
func NewServer(opts ServerOptions) (*Server, error) {
	if opts.Store == nil {
		return nil, errors.New("config.Store is required")
	}
	if opts.ExchangeDir == "" {
		opts.ExchangeDir = "/var/cordbrief/exchange"
	}
	if opts.DataDir == "" {
		opts.DataDir = "/var/cordbrief/data"
	}

	s := &Server{
		exchangeDir: opts.ExchangeDir,
		dataDir:     opts.DataDir,
		store:       opts.Store,
		mux:         http.NewServeMux(),
	}

	s.routes()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline';")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/api/watchlist", s.handleWatchlist)
	s.mux.HandleFunc("/api/llm", s.handleLLMSettings)
	s.mux.HandleFunc("/api/llm/test", s.handleLLMTest)
	s.mux.HandleFunc("/api/digest/settings", s.handleDigestSettings)
	s.mux.HandleFunc("/api/digest/preview", s.handleDigestPreview)
}

// validateCSRF enforces Origin / Host check on state-changing requests.
func (s *Server) validateCSRF(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Origin is optional for same-origin form posts in some browsers; check Referer
		referer := r.Header.Get("Referer")
		if referer == "" {
			return true // Local CLI or script access allowed
		}
		u, err := url.Parse(referer)
		if err != nil {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

type indexViewModel struct {
	CollectorRunning     bool
	DiscordAuthenticated bool
	CatalogState         string
	CatalogGuildCount    int
	CatalogChannelCount  int
	WatchedGeneration    int
	WatchedChannelCount  int
	WatchedSet           map[string]bool
	CommittedCursor      journal.Cursor
	ActiveSegment        int
	JournalFinalOffset   int64
	Config               config.AppConfig
	GeminiConfigured     bool
	GeminiKeySource      config.SecretSource
	FocusJoined          string
	Catalog              *catalog.Catalog
	FlashMessage         string
	FlashError           string
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	vm := indexViewModel{
		WatchedSet: make(map[string]bool),
		Config:     s.store.GetAppConfig(),
	}

	// Flash messages from query params
	vm.FlashMessage = r.URL.Query().Get("flash")
	vm.FlashError = r.URL.Query().Get("error")

	// 1. Read collector-status.json
	statusPath := filepath.Join(s.exchangeDir, "collector-status.json")
	if statData, err := os.ReadFile(statusPath); err == nil {
		var st struct {
			CollectorState       string `json:"collector_state"`
			DiscordAuthenticated *bool  `json:"discord_authenticated"`
			CatalogState         string `json:"catalog_state"`
			WatchedGeneration    int    `json:"watched_generation"`
			WatchedChannelCount  int    `json:"watched_channel_count"`
			ActiveSegment        int    `json:"active_segment"`
		}
		if err := json.Unmarshal(statData, &st); err == nil {
			vm.CollectorRunning = (st.CollectorState == "running")
			vm.DiscordAuthenticated = (st.DiscordAuthenticated != nil && *st.DiscordAuthenticated)
			vm.CatalogState = st.CatalogState
			vm.WatchedGeneration = st.WatchedGeneration
			vm.WatchedChannelCount = st.WatchedChannelCount
			vm.ActiveSegment = st.ActiveSegment
		}
	}

	// 2. Read catalog.json
	if cat, err := catalog.Load(s.exchangeDir); err == nil {
		vm.Catalog = cat
		vm.CatalogGuildCount = len(cat.Guilds)
		for _, g := range cat.Guilds {
			vm.CatalogChannelCount += len(g.Channels)
		}
		if vm.CatalogState == "" {
			vm.CatalogState = "ready"
		}
	} else {
		if vm.CatalogState == "" {
			vm.CatalogState = "unavailable"
		}
	}

	// 3. Read watchlist.json to populate pre-checked set
	wlPath := filepath.Join(s.exchangeDir, "watchlist.json")
	if wlData, err := os.ReadFile(wlPath); err == nil {
		var wl struct {
			Generation int      `json:"generation"`
			ChannelIDs []string `json:"channel_ids"`
		}
		if err := json.Unmarshal(wlData, &wl); err == nil {
			for _, id := range wl.ChannelIDs {
				vm.WatchedSet[id] = true
			}
			if vm.WatchedGeneration == 0 {
				vm.WatchedGeneration = wl.Generation
				vm.WatchedChannelCount = len(wl.ChannelIDs)
			}
		}
	}

	// 4. Read core-ack.json committed cursor
	ackPath := filepath.Join(s.exchangeDir, "core-ack.json")
	if cur, err := journal.LoadCursor(ackPath); err == nil {
		vm.CommittedCursor = *cur
	}

	// 5. Active journal file size
	eventsDir := filepath.Join(s.exchangeDir, "events")
	seg1Path := filepath.Join(eventsDir, "0000000000000001.ndjson")
	if info, err := os.Stat(seg1Path); err == nil {
		vm.JournalFinalOffset = info.Size()
		if vm.ActiveSegment == 0 {
			vm.ActiveSegment = 1
		}
	}

	vm.GeminiKeySource = s.store.GetGeminiKeySource()
	vm.GeminiConfigured = (vm.GeminiKeySource != config.SecretSourceNone)
	vm.FocusJoined = strings.Join(vm.Config.Digest.Focus, ", ")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = IndexTemplate.Execute(w, vm)
}

func (s *Server) handleWatchlist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/x-www-form-urlencoded") && !strings.HasPrefix(ct, "multipart/form-data") {
		http.Error(w, "Unsupported Media Type", http.StatusUnsupportedMediaType)
		return
	}
	if !s.validateCSRF(r) {
		http.Error(w, "CSRF verification failed", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/?error=Failed+parsing+form", http.StatusSeeOther)
		return
	}

	selected := r.Form["channels"]
	var cleanIDs []string
	seen := make(map[string]bool)
	for _, id := range selected {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" && !seen[trimmed] {
			seen[trimmed] = true
			cleanIDs = append(cleanIDs, trimmed)
		}
	}

	// Load current watchlist generation
	var currentGen int64
	wlPath := filepath.Join(s.exchangeDir, "watchlist.json")
	if wlData, err := os.ReadFile(wlPath); err == nil {
		var curWL struct {
			Generation int64 `json:"generation"`
		}
		if err := json.Unmarshal(wlData, &curWL); err == nil {
			currentGen = curWL.Generation
		}
	}

	nextGen := currentGen + 1
	newWL := &journal.Watchlist{
		Version:    journal.CurrentSchemaVersion,
		Generation: nextGen,
		ChannelIDs: journal.NormalizeChannelIDs(cleanIDs),
	}
	if err := journal.WriteWatchlist(wlPath, newWL); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/?error=Failed+writing+watchlist:+%s", url.QueryEscape(err.Error())), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/?flash=Watchlist+updated+to+generation+%d+(%d+channels)", nextGen, len(cleanIDs)), http.StatusSeeOther)
}

func (s *Server) handleLLMSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/x-www-form-urlencoded") && !strings.HasPrefix(ct, "multipart/form-data") {
		http.Error(w, "Unsupported Media Type", http.StatusUnsupportedMediaType)
		return
	}
	if !s.validateCSRF(r) {
		http.Error(w, "CSRF verification failed", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/?error=Failed+parsing+form", http.StatusSeeOther)
		return
	}

	cfg := s.store.GetAppConfig()
	provider := strings.ToLower(strings.TrimSpace(r.FormValue("provider")))

	if provider == config.ProviderGemini {
		cfg.LLM.Provider = config.ProviderGemini
		model := strings.TrimSpace(r.FormValue("gemini_model"))
		if model == "" {
			model = config.DefaultGeminiModel
		}
		cfg.LLM.Model = model

		// Optional secret key entered in UI
		if key := strings.TrimSpace(r.FormValue("gemini_api_key")); key != "" {
			if s.store.GetGeminiKeySource() == config.SecretSourceEnvironment {
				http.Redirect(w, r, "/?error=GEMINI_API_KEY+is+managed+by+environment;+UI+override+is+disabled", http.StatusSeeOther)
				return
			}
			if err := s.store.SaveGeminiKey(key); err != nil {
				http.Redirect(w, r, fmt.Sprintf("/?error=Failed+saving+secret:+%s", url.QueryEscape(err.Error())), http.StatusSeeOther)
				return
			}
		}
	} else if provider == config.ProviderLocal {
		cfg.LLM.Provider = config.ProviderLocal
		baseURL := strings.TrimSpace(r.FormValue("local_base_url"))
		model := strings.TrimSpace(r.FormValue("local_model"))
		if baseURL == "" || model == "" {
			http.Redirect(w, r, "/?error=Local+provider+requires+both+Base+URL+and+Model", http.StatusSeeOther)
			return
		}
		cfg.LLM.BaseURL = baseURL
		cfg.LLM.Model = model
	} else {
		http.Redirect(w, r, "/?error=Unsupported+provider", http.StatusSeeOther)
		return
	}

	if err := s.store.SaveAppConfig(cfg); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/?error=Failed+saving+config:+%s", url.QueryEscape(err.Error())), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/?flash=LLM+settings+saved+successfully", http.StatusSeeOther)
}

func (s *Server) handleLLMTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validateCSRF(r) {
		http.Error(w, "CSRF verification failed", http.StatusForbidden)
		return
	}

	_ = r.ParseMultipartForm(32 << 20)
	_ = r.ParseForm()

	providerType := strings.ToLower(strings.TrimSpace(r.FormValue("provider")))
	if providerType == "" {
		providerType = s.store.GetAppConfig().LLM.Provider
	}

	var baseURL, model, apiKey string
	if providerType == config.ProviderGemini {
		baseURL = config.DefaultGeminiBaseURL
		model = strings.TrimSpace(r.FormValue("gemini_model"))
		if model == "" {
			model = config.DefaultGeminiModel
		}
		// Use submitted key or active store key
		if key := strings.TrimSpace(r.FormValue("gemini_api_key")); key != "" {
			apiKey = key
		} else {
			apiKey = s.store.GetGeminiKey()
		}
		if apiKey == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "GEMINI_API_KEY is not configured"})
			return
		}
	} else if providerType == config.ProviderLocal {
		baseURL = strings.TrimSpace(r.FormValue("local_base_url"))
		model = strings.TrimSpace(r.FormValue("local_model"))
		if baseURL == "" || model == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Local provider requires both Base URL and Model"})
			return
		}
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Unsupported provider"})
		return
	}

	p := llm.NewOpenAICompatibleProvider(baseURL, model, apiKey, 100, 15*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	resp, err := p.GenerateText(ctx, "You are a test responder.", "Respond with the single word PONG")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": fmt.Sprintf("Connected to %s (%s): %s", providerType, model, strings.TrimSpace(resp)),
	})
}

func (s *Server) handleDigestSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/x-www-form-urlencoded") && !strings.HasPrefix(ct, "multipart/form-data") {
		http.Error(w, "Unsupported Media Type", http.StatusUnsupportedMediaType)
		return
	}
	if !s.validateCSRF(r) {
		http.Error(w, "CSRF verification failed", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/?error=Failed+parsing+form", http.StatusSeeOther)
		return
	}

	cfg := s.store.GetAppConfig()
	cfg.Digest.OutputLanguage = strings.TrimSpace(r.FormValue("output_language"))
	if cfg.Digest.OutputLanguage == "" {
		cfg.Digest.OutputLanguage = config.DefaultLanguage
	}

	focusRaw := r.FormValue("focus")
	var focus []string
	for _, part := range strings.Split(focusRaw, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			focus = append(focus, trimmed)
		}
	}
	cfg.Digest.Focus = focus
	cfg.Digest.IgnoreBots = (r.FormValue("ignore_bots") == "true")

	if err := s.store.SaveAppConfig(cfg); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/?error=Failed+saving+digest+settings:+%s", url.QueryEscape(err.Error())), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/?flash=Digest+settings+saved+successfully", http.StatusSeeOther)
}

func (s *Server) handleDigestPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validateCSRF(r) {
		http.Error(w, "CSRF verification failed", http.StatusForbidden)
		return
	}

	appCfg := s.store.GetAppConfig()
	var apiKey string
	if appCfg.LLM.Provider == config.ProviderGemini {
		apiKey = s.store.GetGeminiKey()
		if apiKey == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "GEMINI_API_KEY is not configured"})
			return
		}
	}

	provider := llm.NewOpenAICompatibleProvider(
		appCfg.LLM.BaseURL,
		appCfg.LLM.Model,
		apiKey,
		appCfg.LLM.MaxOutputTokens,
		time.Duration(appCfg.LLM.TimeoutSeconds)*time.Second,
	)

	pipe := llm.NewPipeline(provider, appCfg.LLM.MaxInputChars, llm.PromptConfig{
		Language: appCfg.Digest.OutputLanguage,
		Focus:    appCfg.Digest.Focus,
	})

	opts := digest.TransactionOptions{
		ExchangeDir:  s.exchangeDir,
		DataDir:      s.dataDir,
		IgnoreBots:   appCfg.Digest.IgnoreBots,
		BatchLimit:   1000,
		ProviderName: appCfg.LLM.Provider,
		ModelName:    appCfg.LLM.Model,
		Commit:       false, // Preview strictly preserves cursor and creates no artifact
	}

	res, err := digest.RunTransaction(context.Background(), pipe, opts)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	if res.Empty {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "empty": true})
		return
	}

	renderedMD := digest.RenderMarkdown(res.Digest, res.Batch)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"empty":             false,
		"batch_id":          res.Batch.BatchID,
		"total_records":     res.Batch.TotalJournalRecords,
		"included_records":  len(res.Batch.IncludedMessages),
		"provider":          appCfg.LLM.Provider,
		"model":             appCfg.LLM.Model,
		"rendered_markdown": renderedMD,
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
