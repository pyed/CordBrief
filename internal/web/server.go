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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"cordbrief/internal/catalog"
	"cordbrief/internal/config"
	"cordbrief/internal/delivery"
	"cordbrief/internal/digest"
	"cordbrief/internal/inbox"
	"cordbrief/internal/journal"
	"cordbrief/internal/llm"
	"cordbrief/internal/scheduler"
)

// DefaultMaxRequestBodyBytes bounds HTTP mutation request bodies (128 KB) to prevent unbounded memory usage.
const DefaultMaxRequestBodyBytes = 128 << 10

// ServerOptions configures the Core Web Control Plane.
type ServerOptions struct {
	ExchangeDir     string
	DataDir         string
	Store           *config.Store
	Scheduler       *scheduler.Service
	DeliveryService *delivery.Service
	Lock            *sync.Mutex
}

// Server serves the Core Web Control Plane.
type Server struct {
	exchangeDir     string
	dataDir         string
	store           *config.Store
	scheduler       *scheduler.Service
	deliveryService *delivery.Service
	lock            *sync.Mutex
	mux             *http.ServeMux
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
	if opts.Lock == nil {
		opts.Lock = &sync.Mutex{}
	}

	s := &Server{
		exchangeDir:     opts.ExchangeDir,
		dataDir:         opts.DataDir,
		store:           opts.Store,
		scheduler:       opts.Scheduler,
		deliveryService: opts.DeliveryService,
		lock:            opts.Lock,
		mux:             http.NewServeMux(),
	}

	s.routes()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline';")

	// Limit request body size for mutation methods to prevent unauthenticated/unbounded memory exhaustion
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
		r.Body = http.MaxBytesReader(w, r.Body, DefaultMaxRequestBodyBytes)
	}

	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleOverview)
	s.mux.HandleFunc("/overview", s.handleOverview)
	s.mux.HandleFunc("/inbox", s.handleInbox)
	s.mux.HandleFunc("/channels", s.handleChannels)
	s.mux.HandleFunc("/schedule", s.handleSchedulePage)
	s.mux.HandleFunc("/provider", s.handleProviderPage)
	s.mux.HandleFunc("/telegram", s.handleTelegramPage)
	s.mux.HandleFunc("/system", s.handleSystemPage)
	s.mux.HandleFunc("/digests/", s.handleDigestDetail)

	// API / Mutation routes (PRG)
	s.mux.HandleFunc("/api/schedule", s.handleSchedule)
	s.mux.HandleFunc("/api/watchlist", s.handleWatchlist)
	s.mux.HandleFunc("/api/llm", s.handleLLMSettings)
	s.mux.HandleFunc("/api/llm/test", s.handleLLMTest)
	s.mux.HandleFunc("/api/digest/settings", s.handleDigestSettings)
	s.mux.HandleFunc("/api/digest/preview", s.handleDigestPreview)
	s.mux.HandleFunc("/api/collector/command", s.handleCollectorCommand)
	s.mux.HandleFunc("/api/telegram/settings", s.handleTelegramSettings)
	s.mux.HandleFunc("/api/telegram/test", s.handleTelegramTest)
	s.mux.HandleFunc("/api/telegram/chats", s.handleTelegramChats)
	s.mux.HandleFunc("/api/telegram/send-test", s.handleTelegramSendTest)
	s.mux.HandleFunc("/api/digests/", s.handleDigestDeliveryAction)
}

// validateCSRF enforces Origin / Referer / Fetch metadata checks on state-changing requests.
func (s *Server) validateCSRF(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}

	// 1. Check Sec-Fetch-Site (modern browsers send this on cross-origin requests)
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}

	origin := strings.TrimSpace(r.Header.Get("Origin"))
	// Reject explicit null origin (sandboxed iframes, file://, or privacy proxies)
	if origin == "null" {
		return false
	}

	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	}

	referer := strings.TrimSpace(r.Header.Get("Referer"))
	if referer != "" {
		u, err := url.Parse(referer)
		if err != nil || u.Host == "" {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	}

	// Absent Origin and Referer: allowed for local CLI / script access
	return true
}

func (s *Server) getCorePort() int {
	if portStr := os.Getenv("CORDBRIEF_WEB_PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			return p
		}
	}
	return config.DefaultCorePort
}

type overviewViewModel struct {
	PageTitle           string
	PageDescription     string
	ActiveNav           string
	FlashMessage        string
	FlashError          string
	CorePort            int
	CollectorRunning    bool
	CollectorStale      bool
	CollectorMode       string
	CollectorStateStr   string
	DiscordAuth         bool
	SetupRequired       bool
	ReauthRequired      bool
	CatalogState        string
	CatalogGuildCount   int
	CatalogChannelCount int
	WatchedGeneration   int
	WatchedChannelCount int
	HasLastDigest       bool
	LastDigestTitle     string
	LastDigestID        string
	LastDigestTime      string
	ScheduleEnabled     bool
	ScheduleTime        string
	ScheduleTimezone    string
	ScheduleNextDue     string
	TelegramEnabled     bool
	TelegramConfigured  bool
	TelegramDestination string
	AIProvider          string
	AIModel             string
	AIKeySource         config.SecretSource
	BacklogCount        int64
	RecoveryStatusStr   string
}

type channelsViewModel struct {
	PageTitle           string
	PageDescription     string
	ActiveNav           string
	FlashMessage        string
	FlashError          string
	Catalog             *catalog.Catalog
	CatalogState        string
	CatalogGuildCount   int
	CatalogChannelCount int
	WatchedCount        int
	WatchedGeneration   int
	WatchedSet          map[string]bool
}

type scheduleViewModel struct {
	PageTitle        string
	PageDescription  string
	ActiveNav        string
	FlashMessage     string
	FlashError       string
	Config           config.ScheduleConfig
	State            scheduler.State
	NextDueFormatted string
	NextDueDuration  string
}

type providerViewModel struct {
	PageTitle        string
	PageDescription  string
	ActiveNav        string
	FlashMessage     string
	FlashError       string
	LLMConfig        config.LLMConfig
	DigestConfig     config.DigestConfig
	GeminiKeySource  config.SecretSource
	GeminiConfigured bool
	FocusJoined      string
}

type telegramViewModel struct {
	PageTitle       string
	PageDescription string
	ActiveNav       string
	FlashMessage    string
	FlashError      string
	Enabled         bool
	TokenSource     config.SecretSource
	Configured      bool
	ChatID          string
	ChatLabel       string
}

type systemViewModel struct {
	PageTitle               string
	PageDescription         string
	ActiveNav               string
	FlashMessage            string
	FlashError              string
	CorePort                int
	DataDir                 string
	ExchangeDir             string
	CollectorRunning        bool
	CollectorStale          bool
	CollectorMode           string
	CollectorStateStr       string
	DiscordAuth             bool
	CatalogState            string
	CatalogUpdatedFormatted string
	RecoveryState           string
	RecoveryPendingChannels int
	WatchedGeneration       int
	WatchedChannelCount     int
	ActiveSegment           int
	JournalSizeBytes        int64
	CommittedCursor         journal.Cursor
	UnconsumedBytes         int64
	SchedulerState          scheduler.State
	SchedulerNextDue        string
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.handleOverview(w, r)
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/overview" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	appConfig := s.store.GetAppConfig()
	vm := overviewViewModel{
		PageTitle:        "Overview",
		PageDescription:  "Live operational status of CordBrief appliance subsystems.",
		ActiveNav:        "overview",
		FlashMessage:     r.URL.Query().Get("flash"),
		FlashError:       r.URL.Query().Get("error"),
		CorePort:         s.getCorePort(),
		ScheduleEnabled:  appConfig.Schedule.Enabled,
		ScheduleTime:     appConfig.Schedule.Time,
		ScheduleTimezone: appConfig.Schedule.Timezone,
		AIProvider:       appConfig.LLM.Provider,
		AIModel:          appConfig.LLM.Model,
		AIKeySource:      s.store.GetGeminiKeySource(),
	}

	// 1. Collector status with freshness check (30s threshold)
	statPath := filepath.Join(s.exchangeDir, "collector-status.json")
	if stat, err := journal.ReadCollectorStatus(statPath); err == nil {
		isFresh := stat.IsFresh(time.Now().UTC(), 30*time.Second)
		vm.CollectorStale = !isFresh
		vm.CollectorMode = stat.Mode
		vm.CollectorStateStr = stat.CollectorState

		if isFresh {
			vm.CollectorRunning = (stat.CollectorState == "running")
			vm.DiscordAuth = (stat.DiscordAuthenticated != nil && *stat.DiscordAuthenticated)
			vm.SetupRequired = (stat.CollectorState == "setup_required" || stat.Mode == "setup")
			vm.ReauthRequired = (stat.CollectorState == "reauth_required" || stat.Mode == "reauth")
			vm.CatalogState = stat.CatalogState
			vm.WatchedGeneration = int(stat.WatchedGeneration)
			vm.WatchedChannelCount = stat.WatchedChannelCount

			switch stat.RecoveryState {
			case "ready":
				vm.RecoveryStatusStr = "✓ Up to date"
			case "recovering":
				vm.RecoveryStatusStr = fmt.Sprintf("Recovering (%d remaining)", stat.RecoveryPendingChannels)
			case "error":
				vm.RecoveryStatusStr = "Recovery Warning"
			default:
				if stat.RecoveryState != "" {
					vm.RecoveryStatusStr = stat.RecoveryState
				}
			}
		} else {
			vm.CollectorRunning = false
			vm.DiscordAuth = false
			vm.CollectorStateStr = "Collector Offline"
		}
	} else {
		vm.CollectorRunning = false
		vm.CollectorStale = true
		vm.CollectorStateStr = "Collector Offline"
	}

	// 2. Catalog
	if cat, err := catalog.Load(s.exchangeDir); err == nil {
		vm.CatalogGuildCount = len(cat.Guilds)
		for _, g := range cat.Guilds {
			for _, ch := range g.Channels {
				if !catalog.IsHiddenChannelSentinel(ch.Name) {
					vm.CatalogChannelCount++
				}
			}
		}
		if vm.CatalogState == "" {
			vm.CatalogState = "ready"
		}
	} else {
		if vm.CatalogState == "" {
			vm.CatalogState = "unavailable"
		}
	}

	// 3. Watchlist fallback if collector unread
	if vm.WatchedChannelCount == 0 {
		wlPath := filepath.Join(s.exchangeDir, "watchlist.json")
		if wlData, err := os.ReadFile(wlPath); err == nil {
			var wl struct {
				Generation int      `json:"generation"`
				ChannelIDs []string `json:"channel_ids"`
			}
			if err := json.Unmarshal(wlData, &wl); err == nil {
				vm.WatchedGeneration = wl.Generation
				vm.WatchedChannelCount = len(wl.ChannelIDs)
			}
		}
	}

	// 4. Cursor and backlog
	ackPath := filepath.Join(s.exchangeDir, "core-ack.json")
	cur, _ := journal.LoadCursor(ackPath)
	eventsDir := filepath.Join(s.exchangeDir, "events")
	wm, _ := journal.CaptureWatermark(eventsDir)
	unconsumed, _ := journal.CalculateBacklog(cur, wm)
	vm.BacklogCount = unconsumed

	// 5. Last Digest
	digestsDir := filepath.Join(s.dataDir, "digests")
	if summaries, _, err := inbox.ListDigests(digestsDir); err == nil && len(summaries) > 0 {
		vm.HasLastDigest = true
		vm.LastDigestTitle = summaries[0].Title
		vm.LastDigestID = summaries[0].BatchID
		vm.LastDigestTime = FormatDisplayTime(summaries[0].CreatedAt, s.getDisplayTimezone())
	}

	// 6. Schedule Next Due
	var nextDue time.Time
	if s.scheduler != nil {
		status, _ := s.scheduler.GetStatus()
		if status.NextRunTime != nil {
			nextDue = *status.NextRunTime
		}
	} else {
		var st scheduler.State
		if sLoad, err := scheduler.LoadState(s.dataDir); err == nil {
			st = *sLoad
		}
		status := scheduler.EvaluateSlot(appConfig.Schedule, &st, time.Now())
		if status.NextRunTime != nil {
			nextDue = *status.NextRunTime
		}
	}
	if !nextDue.IsZero() {
		vm.ScheduleNextDue = FormatDisplayTime(nextDue, s.getDisplayTimezone())
	}

	// 7. Telegram
	delCfg := s.store.GetDeliveryConfig()
	vm.TelegramEnabled = delCfg.Telegram.Enabled
	vm.TelegramConfigured = s.store.IsTelegramConfigured()
	if delCfg.Telegram.ChatLabel != "" {
		vm.TelegramDestination = delCfg.Telegram.ChatLabel
	} else if delCfg.Telegram.ChatID != "" {
		vm.TelegramDestination = delCfg.Telegram.ChatID
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = OverviewTemplate.Execute(w, vm)
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	vm := channelsViewModel{
		PageTitle:       "Watched Channels",
		PageDescription: "Select Discord channels to monitor for daily digest synthesis.",
		ActiveNav:       "channels",
		FlashMessage:    r.URL.Query().Get("flash"),
		FlashError:      r.URL.Query().Get("error"),
		WatchedSet:      make(map[string]bool),
	}

	// 1. Read catalog.json, strictly filtering hidden channel sentinels
	if cat, err := catalog.Load(s.exchangeDir); err == nil {
		sanitizedCat := &catalog.Catalog{
			Version:   cat.Version,
			UpdatedAt: cat.UpdatedAt,
		}
		for _, g := range cat.Guilds {
			var safeChannels []catalog.Channel
			for _, ch := range g.Channels {
				if !catalog.IsHiddenChannelSentinel(ch.Name) {
					safeChannels = append(safeChannels, ch)
				}
			}
			if len(safeChannels) > 0 {
				sanitizedCat.Guilds = append(sanitizedCat.Guilds, catalog.Guild{
					ID:       g.ID,
					Name:     g.Name,
					Channels: safeChannels,
				})
			}
		}
		vm.Catalog = sanitizedCat
		vm.CatalogGuildCount = len(sanitizedCat.Guilds)
		for _, g := range sanitizedCat.Guilds {
			vm.CatalogChannelCount += len(g.Channels)
		}
		vm.CatalogState = "ready"
	} else {
		vm.CatalogState = "unavailable"
	}

	// 2. Read watchlist.json to populate checked set
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
			vm.WatchedGeneration = wl.Generation
			vm.WatchedCount = len(wl.ChannelIDs)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = ChannelsTemplate.Execute(w, vm)
}

func (s *Server) handleSchedulePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	appConfig := s.store.GetAppConfig()
	vm := scheduleViewModel{
		PageTitle:       "Schedule",
		PageDescription: "Configure autonomous daily digest generation timing and timezone.",
		ActiveNav:       "schedule",
		FlashMessage:    r.URL.Query().Get("flash"),
		FlashError:      r.URL.Query().Get("error"),
		Config:          appConfig.Schedule,
	}

	var nextDue time.Time
	if s.scheduler != nil {
		status, st := s.scheduler.GetStatus()
		vm.State = st
		if status.NextRunTime != nil {
			nextDue = *status.NextRunTime
		}
	} else {
		if st, err := scheduler.LoadState(s.dataDir); err == nil {
			vm.State = *st
		}
		status := scheduler.EvaluateSlot(vm.Config, &vm.State, time.Now())
		if status.NextRunTime != nil {
			nextDue = *status.NextRunTime
		}
	}

	if !nextDue.IsZero() {
		vm.NextDueFormatted = FormatDisplayTime(nextDue, s.getDisplayTimezone())
		dur := time.Until(nextDue).Round(time.Minute)
		if dur > 0 {
			vm.NextDueDuration = fmt.Sprintf("in %s", dur)
		} else {
			vm.NextDueDuration = "due now"
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = ScheduleTemplate.Execute(w, vm)
}

func (s *Server) handleProviderPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	appConfig := s.store.GetAppConfig()
	keySource := s.store.GetGeminiKeySource()
	vm := providerViewModel{
		PageTitle:        "AI Provider",
		PageDescription:  "Configure LLM synthesis provider parameters and digest prompt focus.",
		ActiveNav:        "provider",
		FlashMessage:     r.URL.Query().Get("flash"),
		FlashError:       r.URL.Query().Get("error"),
		LLMConfig:        appConfig.LLM,
		DigestConfig:     appConfig.Digest,
		GeminiKeySource:  keySource,
		GeminiConfigured: (keySource != config.SecretSourceNone),
		FocusJoined:      strings.Join(appConfig.Digest.Focus, ", "),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = ProviderTemplate.Execute(w, vm)
}

func (s *Server) handleTelegramPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	delCfg := s.store.GetDeliveryConfig()
	vm := telegramViewModel{
		PageTitle:       "Telegram Delivery",
		PageDescription: "Configure first-class daily digest delivery directly to a Telegram chat or DM.",
		ActiveNav:       "telegram",
		FlashMessage:    r.URL.Query().Get("flash"),
		FlashError:      r.URL.Query().Get("error"),
		Enabled:         delCfg.Telegram.Enabled,
		TokenSource:     s.store.GetTelegramTokenSource(),
		Configured:      s.store.IsTelegramConfigured(),
		ChatID:          delCfg.Telegram.ChatID,
		ChatLabel:       delCfg.Telegram.ChatLabel,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = TelegramTemplate.Execute(w, vm)
}

func (s *Server) handleSystemPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	vm := systemViewModel{
		PageTitle:       "System Status",
		PageDescription: "Operational telemetry, storage offsets, and appliance control plane.",
		ActiveNav:       "system",
		FlashMessage:    r.URL.Query().Get("flash"),
		FlashError:      r.URL.Query().Get("error"),
		CorePort:        s.getCorePort(),
		DataDir:         s.dataDir,
		ExchangeDir:     s.exchangeDir,
	}

	// 1. Collector status
	statPath := filepath.Join(s.exchangeDir, "collector-status.json")
	if stat, err := journal.ReadCollectorStatus(statPath); err == nil {
		isFresh := stat.IsFresh(time.Now().UTC(), 30*time.Second)
		vm.CollectorStale = !isFresh
		vm.CollectorMode = stat.Mode
		vm.CollectorStateStr = stat.CollectorState
		if isFresh {
			vm.CollectorRunning = (stat.CollectorState == "running")
			vm.DiscordAuth = (stat.DiscordAuthenticated != nil && *stat.DiscordAuthenticated)
			vm.WatchedGeneration = int(stat.WatchedGeneration)
			vm.WatchedChannelCount = stat.WatchedChannelCount
			vm.ActiveSegment = int(stat.ActiveSegment)
			vm.RecoveryState = stat.RecoveryState
			vm.RecoveryPendingChannels = stat.RecoveryPendingChannels
		} else {
			vm.CollectorRunning = false
			vm.RecoveryState = "stale"
		}
	} else {
		vm.CollectorRunning = false
		vm.CollectorStale = true
		vm.RecoveryState = "unavailable"
	}

	// 2. Catalog freshness
	if cat, err := catalog.Load(s.exchangeDir); err == nil {
		vm.CatalogState = "ready"
		vm.CatalogUpdatedFormatted = FormatDisplayTime(cat.UpdatedAt, s.getDisplayTimezone())
	} else {
		vm.CatalogState = "unavailable"
	}

	// 3. Cursor & offsets
	ackPath := filepath.Join(s.exchangeDir, "core-ack.json")
	if cur, err := journal.LoadCursor(ackPath); err == nil {
		vm.CommittedCursor = *cur
	}
	eventsDir := filepath.Join(s.exchangeDir, "events")
	wm, _ := journal.CaptureWatermark(eventsDir)
	unconsumed, total := journal.CalculateBacklog(&vm.CommittedCursor, wm)
	vm.JournalSizeBytes = total
	vm.UnconsumedBytes = unconsumed
	if wm != nil && wm.MaxSegment > 0 {
		vm.ActiveSegment = int(wm.MaxSegment)
	} else if vm.ActiveSegment == 0 {
		vm.ActiveSegment = 1
	}

	// 4. Scheduler state
	if s.scheduler != nil {
		status, st := s.scheduler.GetStatus()
		vm.SchedulerState = st
		if status.NextRunTime != nil {
			vm.SchedulerNextDue = FormatDisplayTime(*status.NextRunTime, s.getDisplayTimezone())
		}
	} else {
		if st, err := scheduler.LoadState(s.dataDir); err == nil {
			vm.SchedulerState = *st
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = SystemTemplate.Execute(w, vm)
}

func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
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

	if err := parseForm(r); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Redirect(w, r, "/schedule?error=Failed+parsing+form", http.StatusSeeOther)
		return
	}

	enabled := (r.FormValue("enabled") == "true" || r.FormValue("enabled") == "on")
	wallTime := strings.TrimSpace(r.FormValue("time"))
	timezone := strings.TrimSpace(r.FormValue("timezone"))

	newSched := config.ScheduleConfig{
		Enabled:  enabled,
		Time:     wallTime,
		Timezone: timezone,
	}

	if err := newSched.Validate(); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/schedule?error=Invalid+schedule+settings:+%s", url.QueryEscape(err.Error())), http.StatusSeeOther)
		return
	}

	if err := s.store.SaveScheduleConfig(newSched); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/schedule?error=Failed+saving+schedule:+%s", url.QueryEscape(err.Error())), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/schedule?flash=Schedule+settings+saved+successfully", http.StatusSeeOther)
}

type inboxItemViewModel struct {
	inbox.DigestSummary
	CreatedAtFormatted string
	ShortBatchID       string
	Delivery           *delivery.DeliveryRecord
}

type inboxViewModel struct {
	PageTitle       string
	PageDescription string
	ActiveNav       string
	FlashMessage    string
	FlashError      string
	Digests         []inboxItemViewModel
	CorruptCount    int
}

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	digestsDir := filepath.Join(s.dataDir, "digests")
	summaries, corrupt, err := inbox.ListDigests(digestsDir)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed reading inbox: %v", err), http.StatusInternalServerError)
		return
	}

	tzName := s.getDisplayTimezone()
	var items []inboxItemViewModel
	for _, sum := range summaries {
		shortID := sum.BatchID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}
		var delRec *delivery.DeliveryRecord
		if s.deliveryService != nil {
			delRec, _ = s.deliveryService.GetDelivery(sum.BatchID)
		}
		items = append(items, inboxItemViewModel{
			DigestSummary:      sum,
			CreatedAtFormatted: FormatDisplayTime(sum.CreatedAt, tzName),
			ShortBatchID:       shortID,
			Delivery:           delRec,
		})
	}

	vm := inboxViewModel{
		PageTitle:       "Inbox",
		PageDescription: "Generated executive digests synthesized from watched Discord channels.",
		ActiveNav:       "inbox",
		FlashMessage:    r.URL.Query().Get("flash"),
		FlashError:      r.URL.Query().Get("error"),
		Digests:         items,
		CorruptCount:    corrupt,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = InboxTemplate.Execute(w, vm)
}

// FormatDisplayTime renders a UTC time into the user's configured display timezone.
// If tzName is empty or invalid, it falls back to UTC.
// Output format: "Jan 02, 2006 15:04 — <Timezone>" (e.g. "Sep 04, 2026 03:17 — Asia/Riyadh").
func FormatDisplayTime(t time.Time, tzName string) string {
	loc := time.UTC
	displayTZ := "UTC"
	if tz := strings.TrimSpace(tzName); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
			displayTZ = tz
		}
	}
	return fmt.Sprintf("%s — %s", t.In(loc).Format("Jan 02, 2006 15:04"), displayTZ)
}

func (s *Server) getDisplayTimezone() string {
	if s.store != nil {
		appCfg := s.store.GetAppConfig()
		if tz := strings.TrimSpace(appCfg.Schedule.Timezone); tz != "" {
			return tz
		}
	}
	return "UTC"
}

type SourceLinkView struct {
	Label string
	ID    string
	URL   string
}

type DetailItemView struct {
	Kind           string
	Text           string
	ChannelContext string
	Sources        []SourceLinkView
}

type detailViewModel struct {
	PageTitle          string
	PageDescription    string
	ActiveNav          string
	FlashMessage       string
	FlashError         string
	Artifact           *digest.Artifact
	CreatedAtFormatted string
	Items              []DetailItemView
	Delivery           *delivery.DeliveryRecord
}

func (s *Server) handleDigestDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	batchID := strings.TrimPrefix(r.URL.Path, "/digests/")
	if !digest.IsValidBatchID(batchID) {
		http.NotFound(w, r)
		return
	}

	digestsDir := filepath.Join(s.dataDir, "digests")
	art, err := inbox.GetDigest(digestsDir, batchID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, inbox.ErrInvalidBatchID) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, fmt.Sprintf("Failed reading digest: %v", err), http.StatusInternalServerError)
		return
	}

	// Build source map for jump links
	sourceMap := make(map[string]digest.SourceMessage)
	eventsDir := filepath.Join(s.exchangeDir, "events")
	startCur := art.CursorStart
	endCur := art.CursorEnd
	if startCur.Version == 0 {
		startCur.Version = journal.CurrentSchemaVersion
	}
	if endCur.Version == 0 {
		endCur.Version = journal.CurrentSchemaVersion
	}
	if wm, err := journal.CaptureWatermark(eventsDir); err == nil && wm != nil {
		reader := journal.NewReader(eventsDir, wm)
		records, _, err := reader.ReadBatch(startCur, art.InputMessageCount+100)
		if err == nil && len(records) > 0 {
			cat, _ := catalog.Load(s.exchangeDir)
			batch, err := digest.BuildBatch(records, startCur, endCur, wm, false, cat)
			if err == nil && batch != nil {
				sourceMap = batch.SourceMap
			}
		}
	}

	displayMap := delivery.BuildSourceDisplayMap(art.Digest)
	var items []DetailItemView
	if art.Digest != nil {
		for _, item := range art.Digest.Items {
			type sourceItem struct {
				num  int
				view SourceLinkView
			}
			var itemSources []sourceItem
			for _, sID := range item.SourceIDs {
				sIDTrim := strings.TrimSpace(sID)
				if sIDTrim == "" {
					continue
				}
				var jumpURL string
				if sm, ok := sourceMap[sIDTrim]; ok {
					jumpURL = digest.JumpLink(sm)
				}
				label := sIDTrim
				num := 0
				if mapped, ok := displayMap[sIDTrim]; ok {
					label = mapped
					if n, err := strconv.Atoi(mapped); err == nil {
						num = n
					}
				}
				itemSources = append(itemSources, sourceItem{
					num: num,
					view: SourceLinkView{
						Label: label,
						ID:    sIDTrim,
						URL:   jumpURL,
					},
				})
			}
			sort.SliceStable(itemSources, func(i, j int) bool {
				return itemSources[i].num < itemSources[j].num
			})
			var sources []SourceLinkView
			for _, s := range itemSources {
				sources = append(sources, s.view)
			}
			items = append(items, DetailItemView{
				Kind:           item.Kind,
				Text:           item.Text,
				ChannelContext: item.ChannelContext,
				Sources:        sources,
			})
		}
	}

	var delRec *delivery.DeliveryRecord
	if s.deliveryService != nil {
		delRec, _ = s.deliveryService.GetDelivery(batchID)
	}

	tzName := s.getDisplayTimezone()
	vm := detailViewModel{
		PageTitle:          art.Digest.Title,
		PageDescription:    fmt.Sprintf("Synthesized on %s via %s (%s)", FormatDisplayTime(art.CreatedAt, tzName), art.Provider, art.Model),
		ActiveNav:          "inbox",
		Artifact:           art,
		CreatedAtFormatted: FormatDisplayTime(art.CreatedAt, tzName),
		Items:              items,
		Delivery:           delRec,
		FlashMessage:       r.URL.Query().Get("flash"),
		FlashError:         r.URL.Query().Get("error"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = DigestDetailTemplate.Execute(w, vm)
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

	if err := parseForm(r); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Redirect(w, r, "/channels?error=Failed+parsing+form", http.StatusSeeOther)
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
		http.Redirect(w, r, fmt.Sprintf("/channels?error=Failed+writing+watchlist:+%s", url.QueryEscape(err.Error())), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/channels?flash=Watchlist+updated+to+generation+%d+(%d+channels)", nextGen, len(cleanIDs)), http.StatusSeeOther)
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

	if err := parseForm(r); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Redirect(w, r, "/provider?error=Failed+parsing+form", http.StatusSeeOther)
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
				http.Redirect(w, r, "/provider?error=GEMINI_API_KEY+is+managed+by+environment;+UI+override+is+disabled", http.StatusSeeOther)
				return
			}
			if err := s.store.SaveGeminiKey(key); err != nil {
				http.Redirect(w, r, fmt.Sprintf("/provider?error=Failed+saving+secret:+%s", url.QueryEscape(err.Error())), http.StatusSeeOther)
				return
			}
		}
	} else if provider == config.ProviderLocal {
		cfg.LLM.Provider = config.ProviderLocal
		baseURL := strings.TrimSpace(r.FormValue("local_base_url"))
		model := strings.TrimSpace(r.FormValue("local_model"))
		if baseURL == "" || model == "" {
			http.Redirect(w, r, "/provider?error=Local+provider+requires+both+Base+URL+and+Model", http.StatusSeeOther)
			return
		}
		cfg.LLM.BaseURL = baseURL
		cfg.LLM.Model = model
	} else {
		http.Redirect(w, r, "/provider?error=Unsupported+provider", http.StatusSeeOther)
		return
	}

	if err := s.store.SaveAppConfig(cfg); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/provider?error=Failed+saving+config:+%s", url.QueryEscape(err.Error())), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/provider?flash=LLM+settings+saved+successfully", http.StatusSeeOther)
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

	if err := parseForm(r); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
	}

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
		// If user entered key in input, use it; otherwise use stored/env key
		apiKey = strings.TrimSpace(r.FormValue("gemini_api_key"))
		if apiKey == "" {
			apiKey = s.store.GetGeminiKey()
		}
		if apiKey == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "GEMINI_API_KEY is not configured"})
			return
		}
	} else if providerType == config.ProviderLocal {
		baseURL = strings.TrimSpace(r.FormValue("local_base_url"))
		model = strings.TrimSpace(r.FormValue("local_model"))
		apiKey = strings.TrimSpace(r.FormValue("local_api_key"))
		if baseURL == "" || model == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Base URL and Model are required for local provider test"})
			return
		}
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Unknown provider: " + providerType})
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

	if err := parseForm(r); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Redirect(w, r, "/provider?error=Failed+parsing+form", http.StatusSeeOther)
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
		http.Redirect(w, r, fmt.Sprintf("/provider?error=Failed+saving+digest+settings:+%s", url.QueryEscape(err.Error())), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/provider?flash=Digest+settings+saved+successfully", http.StatusSeeOther)
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

	// Single-flight lock: coordinate with scheduler service
	s.lock.Lock()
	defer s.lock.Unlock()

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

func (s *Server) handleCollectorCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validateCSRF(r) {
		http.Error(w, "Forbidden: CSRF validation failed", http.StatusForbidden)
		return
	}

	if err := parseForm(r); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Failed parsing form", http.StatusBadRequest)
		return
	}

	command := strings.TrimSpace(r.FormValue("command"))
	validCommands := map[string]bool{
		"enter_reauth":  true,
		"return_normal": true,
		"status":        true,
	}
	if !validCommands[command] {
		http.Error(w, "Invalid command", http.StatusBadRequest)
		return
	}

	cmd := journal.CollectorCommand{
		Version:     1,
		Command:     command,
		RequestID:   fmt.Sprintf("req-%d", time.Now().UnixNano()),
		RequestedAt: time.Now().UTC(),
	}

	if err := journal.WriteCollectorCommand(s.exchangeDir, cmd); err != nil {
		if errors.Is(err, journal.ErrCommandPending) {
			http.Error(w, "Command already pending: a previous collector command has not been processed yet", http.StatusConflict)
			return
		}
		http.Error(w, fmt.Sprintf("Failed to write command: %v", err), http.StatusInternalServerError)
		return
	}

	msg := "Reauthentication mode requested. Open Setup Viewer (:28742) to sign in."
	if command == "return_normal" {
		msg = "Normal collection mode requested."
	}
	http.Redirect(w, r, "/system?flash="+url.QueryEscape(msg), http.StatusSeeOther)
}

func parseForm(r *http.Request) error {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return r.ParseMultipartForm(DefaultMaxRequestBodyBytes)
	}
	return r.ParseForm()
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) handleTelegramSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validateCSRF(r) {
		http.Error(w, "CSRF verification failed", http.StatusForbidden)
		return
	}

	if err := parseForm(r); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Redirect(w, r, "/telegram?error=Failed+parsing+form", http.StatusSeeOther)
		return
	}

	enabled := (r.FormValue("enabled") == "true" || r.FormValue("enabled") == "on")
	token := strings.TrimSpace(r.FormValue("token"))
	chatID := strings.TrimSpace(r.FormValue("chat_id"))
	chatLabel := strings.TrimSpace(r.FormValue("chat_label"))

	// Save token to secrets.json if provided and not managed by environment
	if token != "" {
		if s.store.GetTelegramTokenSource() != config.SecretSourceEnvironment {
			if err := s.store.SaveTelegramBotToken(token); err != nil {
				http.Redirect(w, r, fmt.Sprintf("/telegram?error=Failed+saving+token:+%s", url.QueryEscape(err.Error())), http.StatusSeeOther)
				return
			}
		}
	}

	delCfg := s.store.GetDeliveryConfig()
	delCfg.Telegram.Enabled = enabled
	delCfg.Telegram.ChatID = chatID
	delCfg.Telegram.ChatLabel = chatLabel

	// Validation: a persisted configuration with telegram.enabled = true must have configured token and destination
	if enabled {
		tokenSource := s.store.GetTelegramTokenSource()
		if tokenSource == config.SecretSourceNone && token == "" {
			errMsg := "Configure and verify a Telegram bot token before enabling delivery."
			if strings.Contains(r.Header.Get("Accept"), "application/json") {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": errMsg})
				return
			}
			http.Redirect(w, r, "/telegram?error="+url.QueryEscape(errMsg), http.StatusSeeOther)
			return
		}
		if chatID == "" {
			errMsg := "Select a Telegram destination before enabling daily delivery."
			if strings.Contains(r.Header.Get("Accept"), "application/json") {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": errMsg})
				return
			}
			http.Redirect(w, r, "/telegram?error="+url.QueryEscape(errMsg), http.StatusSeeOther)
			return
		}
	}

	if err := s.store.SaveDeliveryConfig(delCfg); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/telegram?error=Failed+saving+delivery+settings:+%s", url.QueryEscape(err.Error())), http.StatusSeeOther)
		return
	}

	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	http.Redirect(w, r, "/telegram?flash=Telegram+delivery+settings+saved", http.StatusSeeOther)
}

func (s *Server) handleTelegramTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validateCSRF(r) {
		http.Error(w, "CSRF verification failed", http.StatusForbidden)
		return
	}

	if err := parseForm(r); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
	}
	token := strings.TrimSpace(r.FormValue("token"))

	if s.deliveryService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":    false,
			"error": "Delivery service not initialized",
		})
		return
	}

	if token == "" && s.store.GetTelegramTokenSource() == config.SecretSourceNone {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "telegram bot token is not configured",
		})
		return
	}

	user, err := s.deliveryService.TestBot(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	// Persist verified token into data/secrets.json (0600) so Discover Chats and delivery work immediately
	if token != "" && s.store.GetTelegramTokenSource() != config.SecretSourceEnvironment {
		if err := s.store.SaveTelegramBotToken(token); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": fmt.Sprintf("saving bot token: %v", err),
			})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"id":         user.ID,
		"username":   user.Username,
		"first_name": user.FirstName,
		"configured": true,
		"source":     s.store.GetTelegramTokenSource(),
	})
}

func (s *Server) handleTelegramChats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validateCSRF(r) {
		http.Error(w, "CSRF verification failed", http.StatusForbidden)
		return
	}

	if err := parseForm(r); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
	}
	token := strings.TrimSpace(r.FormValue("token"))

	if s.deliveryService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":    false,
			"error": "Delivery service not initialized",
		})
		return
	}

	chats, err := s.deliveryService.DiscoverChats(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	type safeChatView struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Type  string `json:"type"`
	}

	var results []safeChatView
	for _, c := range chats {
		results = append(results, safeChatView{
			ID:    c.ID,
			Label: c.Label(),
			Type:  c.Type,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"chats": results,
	})
}

func (s *Server) handleTelegramSendTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validateCSRF(r) {
		http.Error(w, "CSRF verification failed", http.StatusForbidden)
		return
	}

	if err := parseForm(r); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
	}
	chatID := strings.TrimSpace(r.FormValue("chat_id"))
	if chatID == "" {
		chatID = s.store.GetDeliveryConfig().Telegram.ChatID
	}
	if chatID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "chat_id is required",
		})
		return
	}

	if s.deliveryService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":    false,
			"error": "Delivery service not initialized",
		})
		return
	}

	msgID, err := s.deliveryService.SendTestMessage(r.Context(), chatID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"message_id": msgID,
		"message":    "Test message sent successfully.",
	})
}

func (s *Server) handleDigestDeliveryAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validateCSRF(r) {
		http.Error(w, "CSRF verification failed", http.StatusForbidden)
		return
	}

	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 || parts[1] != "api" || parts[2] != "digests" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	batchID := parts[3]
	if !digest.IsValidBatchID(batchID) {
		http.Error(w, "Invalid batch ID", http.StatusBadRequest)
		return
	}

	if err := parseForm(r); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
	}
	force := (r.FormValue("force") == "true" || r.FormValue("force") == "1")

	if s.deliveryService == nil {
		http.Error(w, "Delivery service not initialized", http.StatusServiceUnavailable)
		return
	}

	rec, err := s.deliveryService.DeliverBatch(r.Context(), batchID, force)

	isJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	if isJSON {
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":     false,
				"error":  err.Error(),
				"record": rec,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"record": rec,
		})
		return
	}

	if err != nil {
		http.Redirect(w, r, fmt.Sprintf("/digests/%s?error=%s", batchID, url.QueryEscape(err.Error())), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/digests/%s?flash=Delivered+to+Telegram+successfully", batchID), http.StatusSeeOther)
}
