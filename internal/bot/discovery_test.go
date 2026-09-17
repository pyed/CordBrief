package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/pyed/CordBrief/internal/dce"
	"github.com/pyed/CordBrief/internal/job"
	"github.com/pyed/CordBrief/internal/state"
)

func TestDiscoveryBrowserCacheHideAndFollow(t *testing.T) {
	b, sender, store, now := setupTestBot(t)
	ctx := context.Background()
	calls := 0
	fail := false
	mgr, err := dce.NewManager(store.DataDir(), "active", "token", dce.WithBootstrapVersion("2.48"), dce.WithCommandRunner(func(_ context.Context, _ string, args, env []string, stdout, stderr io.Writer) error {
		calls++
		if fail {
			return errors.New("metadata unavailable")
		}
		if args[0] == "guilds" {
			for i := 1; i <= 12; i++ {
				fmt.Fprintf(stdout, "%d | server-%d\n", i, i)
			}
		} else if args[0] == "channels" {
			// Historical failure mode: DCE catalog is larger than the Discord UI.
			for i := 100; i < 600; i++ {
				fmt.Fprintf(stdout, "%d | Category / channel-%d\n", i, i)
			}
		} else {
			t.Fatal("unexpected DCE command", args)
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	b.dceManager = mgr
	message := func(text string) { b.HandleUpdate(ctx, nil, makeMsg(12345, "private", text)) }
	callback := func(data string) { b.HandleUpdate(ctx, nil, makeCallback(12345, "private", data)) }
	message("/discover")
	if calls != 1 || !strings.Contains(sender.lastSent().Text, "12 shown") {
		t.Fatal("servers not discovered")
	}
	callback("d:page::1")
	callback("d:page::0")
	message("/discover")
	if calls != 1 {
		t.Fatal("server navigation re-fetched")
	}
	callback("d:open:1")
	if calls != 2 || !strings.Contains(sender.lastEdited().Text, "500 shown") {
		t.Fatal("large catalog missing")
	}
	callback("d:page:1:62")
	callback("d:page::0")
	callback("d:open:1")
	if calls != 2 {
		t.Fatal("channel navigation re-fetched")
	}
	callback("d:pick:1:100")
	callback("d:hide:1:100")
	if !strings.Contains(sender.lastEdited().Text, "499 shown") {
		t.Fatal("hidden not filtered")
	}
	callback("d:follow:1:101")
	if len(b.pendingFollows) != 1 || !strings.Contains(sender.lastSent().Text, "Select start mode") {
		t.Fatal("discovery bypassed follow flow")
	}
	cfg, _ := store.LoadConfig()
	st, _ := store.LoadState()
	if len(cfg.Channels) != 0 || len(st.Channels) != 0 {
		t.Fatal("follow persisted before selection")
	}
	callback("f:24h:f1")
	cfg, _ = store.LoadConfig()
	st, _ = store.LoadState()
	if len(cfg.Channels) != 1 || cfg.Channels[0].ID != "101" || st.Channels["101"].Cursor.Value != now().UTC().Add(-24*time.Hour).Format(time.RFC3339) {
		t.Fatal("existing follow transaction failed")
	}
	callback("d:page:1:0")
	if !strings.Contains(sender.lastEdited().Text, "498 shown") {
		t.Fatal("followed and hidden not filtered")
	}
	callback("d:refresh:1")
	if calls != 3 || !strings.Contains(sender.lastEdited().Text, "498 shown") {
		t.Fatal("refresh lost filtering")
	}
	fail = true
	callback("d:refresh:1")
	callback("d:page:1:0")
	if calls != 4 || !strings.Contains(sender.lastEdited().Text, "498 shown") {
		t.Fatal("failed refresh destroyed cache")
	}
	// A restart loses only the catalog; hiding and cursor settings survive.
	restarted, err := New(ctx, &EnvConfig{OwnerID: 12345}, store, WithSender(sender))
	if err != nil {
		t.Fatal(err)
	}
	restarted.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/hidden"))
	rows := getInlineKeyboard(sender.lastSent())
	if len(rows) == 0 || rows[0][0].CallbackData != "d:unhide:100" {
		t.Fatal("hidden choices did not survive restart")
	}
	restarted.HandleUpdate(ctx, nil, makeCallback(12345, "private", "d:unhide:100"))
	cfg, _ = store.LoadConfig()
	if len(cfg.HiddenChannels) != 0 {
		t.Fatal("unhide not durable")
	}
	callback("d:page:1:0")
	if calls != 4 || !strings.Contains(sender.lastEdited().Text, "499 shown") {
		t.Fatal("unhide did not restore browser entry")
	}
	message("/follow 999 direct-name")
	callback("f:now:f2")
	cfg, _ = store.LoadConfig()
	if !containsChannel(cfg.Channels, "999") {
		t.Fatal("direct follow broken")
	}
	// Stale/unauthorized callbacks cannot choose arbitrary undiscovered IDs.
	before := calls
	for _, data := range []string{"d:open:999", "d:pick:1:999", "d:page:1:-500", "d:page:1:9999999999999999999999", "d:hide", "d:page"} {
		callback(data)
	}
	b.HandleUpdate(ctx, nil, makeCallback(777, "private", "d:refresh:"))
	b.HandleUpdate(ctx, nil, makeMsg(12345, "group", "/discover"))
	if calls != before {
		t.Fatal("invalid navigation invoked DCE")
	}
}

func TestDiscoveryRunningBriefExclusion(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "1", Name: "one"}}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	st := state.NewEmptyState()
	st.Channels["1"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindMessageID, Value: "1"}}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	exporter := &discoveryBlockingExporter{entered: entered}
	r, _ := job.NewRunner(store, job.WithDCEClient(exporter), job.WithDeliverer(b))
	b.runner = r
	calls := 0
	b.dceManager, _ = dce.NewManager(store.DataDir(), "active", "token", dce.WithBootstrapVersion("2.48"), dce.WithCommandRunner(func(context.Context, string, []string, []string, io.Writer, io.Writer) error { calls++; return nil }))
	b.discoveryCache[""] = []state.ChannelConfig{{ID: "42", Name: "server"}}
	b.discoveryCache["42"] = []state.ChannelConfig{{ID: "2", Name: "channel"}}
	if !r.Start(ctx, "") {
		t.Fatal("brief not started")
	}
	<-entered
	callback := func(data string) { b.HandleUpdate(context.Background(), nil, makeCallback(12345, "private", data)) }
	callback("d:page::0")
	callback("d:open:42")
	if !strings.Contains(sender.lastEdited().Text, "1 shown") {
		t.Fatal("cached browsing unavailable during brief")
	}
	callback("d:refresh:42")
	if calls != 0 || !strings.Contains(sender.lastSent().Text, "brief already running") {
		t.Fatal("refresh ran during brief")
	}
	// Fallback settings share the same atomic mutation gate.
	b.handleFallback(context.Background(), 100, []string{"on"})
	if !strings.Contains(sender.lastSent().Text, "brief already running") {
		t.Fatal("fallback mutation escaped job lock")
	}
	callback("d:hide:42:2")
	callback("d:follow:42:2")
	callback("f:now:f1")
	after, _ := store.LoadConfig()
	if len(after.HiddenChannels) != 0 || len(after.Channels) != 1 {
		t.Fatal("mutation escaped brief lock")
	}
	delete(b.discoveryCache, "42")
	callback("d:open:42")
	if calls != 0 {
		t.Fatal("cache miss invoked DCE during brief")
	}
	cancel()
	r.Wait()
	callback("f:now:f1")
	after, _ = store.LoadConfig()
	if !containsChannel(after.Channels, "2") {
		t.Fatal("pending follow lost on busy error")
	}
}

type discoveryBlockingExporter struct{ entered chan struct{} }

func (*discoveryBlockingExporter) IsConfigured() bool { return true }
func (d *discoveryBlockingExporter) Export(ctx context.Context, _ dce.ExportRequest) (*dce.ExportResult, error) {
	close(d.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDiscoveryDuplicatePendingCannotResetCursor(t *testing.T) {
	b, _, store, _ := setupTestBot(t)
	ctx := context.Background()
	b.handleFollow(ctx, 100, []string{"1", "one"})
	b.handleFollow(ctx, 100, []string{"1", "one"})
	b.handleFollowCallback(ctx, 100, 1, "f:now:f1")
	st, _ := store.LoadState()
	st.Channels["1"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindMessageID, Value: "999"}}
	store.SaveState(st)
	b.handleFollowCallback(ctx, 100, 1, "f:24h:f2")
	after, _ := store.LoadState()
	if after.Channels["1"].Cursor.Value != "999" {
		t.Fatal("stale follow reset cursor")
	}
}

func TestDiscoveryCallbackSizesAndEmptyCache(t *testing.T) {
	b, sender, _, _ := setupTestBot(t)
	id := strings.Repeat("9", 20)
	b.discoveryCache[""] = []state.ChannelConfig{{ID: id, Name: strings.Repeat("界", 100)}}
	b.discoveryCache[id] = []state.ChannelConfig{{ID: id, Name: strings.Repeat("界", 100)}}
	b.handleDiscoveryCallback(context.Background(), 100, 1, "d:pick:"+id+":"+id)
	for _, row := range getEditedInlineKeyboard(sender.lastEdited()) {
		for _, btn := range row {
			if len(btn.CallbackData) > 64 {
				t.Fatal("Telegram callback too large")
			}
		}
	}
	b.discoveryCache[id] = []state.ChannelConfig{}
	b.showDiscovery(context.Background(), 100, 1, id, 100, true, false)
	if !strings.Contains(sender.lastEdited().Text, "0 shown") {
		t.Fatal("empty catalog not cached")
	}
	// Keep the Telegram markup type exercised as part of the browser contract.
	if _, ok := sender.lastEdited().ReplyMarkup.(*models.InlineKeyboardMarkup); !ok {
		t.Fatal("browser lacks buttons")
	}
}
