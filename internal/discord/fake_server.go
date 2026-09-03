package discord

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

// FakeDiscordServer provides an httptest.Server simulating Discord REST API v10 endpoints.
type FakeDiscordServer struct {
	Server   *httptest.Server
	URL      string
	mu       sync.Mutex
	Requests []*http.Request

	// Auth & Overrides
	ExpectedToken string
	BotUser       BotIdentity
	BotUserStatus int

	// Rate limit configuration
	RateLimitNext     bool
	RateLimitSecs     float64
	RateLimitInBody   bool
	RateLimitInHeader bool
	RemainingZero     bool
	ResetAfterSecs    float64

	// Status overrides per channel or route
	ChannelStatus map[string]int // channelID -> status code (e.g. 403, 404)
	FailStatus    int

	// Mock data
	GuildChannels    map[string][]Channel        // guildID -> channels
	Channels         map[string]Channel          // channelID -> channel
	Messages         map[string][]map[string]any // channelID -> messages (newest to oldest)
	RepeatStaticPage bool                        // if true, handleChannelMessages always returns StaticPage
	StaticPage       []map[string]any
}

// NewFakeDiscordServer starts a new in-memory fake Discord server.
func NewFakeDiscordServer() *FakeDiscordServer {
	fds := &FakeDiscordServer{
		RateLimitSecs:     0.05,
		RateLimitInHeader: true,
		RateLimitInBody:   true,
		ResetAfterSecs:    0.05,
		BotUser: BotIdentity{
			ID:       "1234567890",
			Username: "CordBriefBot",
			IsBot:    true,
		},
		ChannelStatus: make(map[string]int),
		GuildChannels: make(map[string][]Channel),
		Channels:      make(map[string]Channel),
		Messages:      make(map[string][]map[string]any),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v10/users/@me", fds.handleUsersMe)
	mux.HandleFunc("/users/@me", fds.handleUsersMe)

	mux.HandleFunc("/api/v10/guilds/", fds.handleGuilds)
	mux.HandleFunc("/guilds/", fds.handleGuilds)

	mux.HandleFunc("/api/v10/channels/", fds.handleChannels)
	mux.HandleFunc("/channels/", fds.handleChannels)

	fds.Server = httptest.NewServer(mux)
	fds.URL = fds.Server.URL + "/api/v10"
	return fds
}

// Close shuts down the underlying test server.
func (fds *FakeDiscordServer) Close() {
	fds.Server.Close()
}

func (fds *FakeDiscordServer) checkCommon(w http.ResponseWriter, r *http.Request) bool {
	fds.Requests = append(fds.Requests, r)

	if fds.ExpectedToken != "" {
		expectedAuth := "Bot " + fds.ExpectedToken
		if r.Header.Get("Authorization") != expectedAuth {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": "401: Unauthorized",
				"code":    0,
			})
			return false
		}
	}

	// Rate limit injection
	if fds.RateLimitNext {
		fds.RateLimitNext = false
		w.Header().Set("Content-Type", "application/json")
		if fds.RateLimitInHeader {
			w.Header().Set("Retry-After", fmt.Sprintf("%.2f", fds.RateLimitSecs))
		}
		w.WriteHeader(http.StatusTooManyRequests)
		body := map[string]any{
			"message": "You are being rate limited.",
			"global":  false,
		}
		if fds.RateLimitInBody {
			body["retry_after"] = fds.RateLimitSecs
		}
		_ = json.NewEncoder(w).Encode(body)
		return false
	}

	// Global failure injection
	if fds.FailStatus > 0 {
		status := fds.FailStatus
		fds.FailStatus = 0
		w.WriteHeader(status)
		return false
	}

	return true
}

func (fds *FakeDiscordServer) handleUsersMe(w http.ResponseWriter, r *http.Request) {
	fds.mu.Lock()
	defer fds.mu.Unlock()

	if !fds.checkCommon(w, r) {
		return
	}

	if fds.BotUserStatus > 0 {
		w.WriteHeader(fds.BotUserStatus)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(fds.BotUser)
}

func (fds *FakeDiscordServer) handleGuilds(w http.ResponseWriter, r *http.Request) {
	fds.mu.Lock()
	defer fds.mu.Unlock()

	if !fds.checkCommon(w, r) {
		return
	}

	p := r.URL.Path
	p = strings.TrimPrefix(p, "/api/v10")
	parts := strings.Split(strings.TrimPrefix(p, "/guilds/"), "/")
	if len(parts) >= 2 && parts[1] == "channels" {
		guildID := parts[0]
		channels, exists := fds.GuildChannels[guildID]
		if !exists {
			channels = []Channel{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(channels)
		return
	}

	w.WriteHeader(http.StatusNotFound)
}

func (fds *FakeDiscordServer) handleChannels(w http.ResponseWriter, r *http.Request) {
	fds.mu.Lock()
	defer fds.mu.Unlock()

	if !fds.checkCommon(w, r) {
		return
	}

	p := r.URL.Path
	p = strings.TrimPrefix(p, "/api/v10")
	tail := strings.TrimPrefix(p, "/channels/")
	parts := strings.Split(tail, "/")
	channelID := parts[0]

	// Check channel specific status override (e.g. 403, 404)
	if status, ok := fds.ChannelStatus[channelID]; ok && status > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": fmt.Sprintf("Error %d on channel", status),
			"code":    status * 100,
		})
		return
	}

	if len(parts) >= 2 && parts[1] == "messages" {
		fds.handleChannelMessages(w, r, channelID)
		return
	}

	if ch, ok := fds.Channels[channelID]; ok {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ch)
		return
	}

	// Default channel detail
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": channelID, "status": "ok"})
}

func (fds *FakeDiscordServer) handleChannelMessages(w http.ResponseWriter, r *http.Request, channelID string) {
	if fds.RepeatStaticPage {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(fds.StaticPage)
		return
	}

	q := r.URL.Query()
	limit := 50
	if lStr := q.Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			limit = l
		}
	}
	beforeID := q.Get("before")

	allMsgs, exists := fds.Messages[channelID]
	if !exists {
		allMsgs = []map[string]any{}
	}

	startIndex := 0
	if beforeID != "" {
		startIndex = len(allMsgs)
		for i, m := range allMsgs {
			if idStr, ok := m["id"].(string); ok && idStr == beforeID {
				startIndex = i + 1
				break
			}
		}
	}

	endIndex := startIndex + limit
	if endIndex > len(allMsgs) {
		endIndex = len(allMsgs)
	}

	var page []map[string]any
	if startIndex < len(allMsgs) {
		page = allMsgs[startIndex:endIndex]
	} else {
		page = []map[string]any{}
	}

	if fds.RemainingZero {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset-After", fmt.Sprintf("%.2f", fds.ResetAfterSecs))
		fds.RemainingZero = false
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(page)
}
