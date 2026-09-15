package bot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyed/CordBrief/internal/dce"
	"github.com/pyed/CordBrief/internal/state"
)

func TestDCEStatus(t *testing.T) {
	t.Run("not configured reports not configured", func(t *testing.T) {
		b, sender, _, _ := setupTestBot(t)
		b.handleStatus(context.Background(), 12345)

		last := sender.lastSent()
		if last == nil {
			t.Fatal("expected message sent, got nil")
		}
		if !strings.Contains(last.Text, "Discord exporter: not configured") {
			t.Fatalf("expected 'Discord exporter: not configured' in status, got:\n%s", last.Text)
		}
	})

	t.Run("status reports active version without assuming it is latest", func(t *testing.T) {
		dir := t.TempDir()
		store := state.NewStore(dir)
		sender := &fakeSender{}

		mgr, err := dce.NewManager(dir, "/path/to/dce", "fake-token",
			dce.WithBootstrapVersion("2.48.0"),
		)
		if err != nil {
			t.Fatal(err)
		}

		b, err := New(context.Background(), &EnvConfig{
			BotToken: "test-token",
			OwnerID:  12345,
			DataDir:  dir,
		}, store, WithSender(sender), WithDCEManager(mgr))
		if err != nil {
			t.Fatal(err)
		}

		b.handleStatus(context.Background(), 12345)
		last := sender.lastSent()
		if last == nil {
			t.Fatal("expected message sent, got nil")
		}
		if !strings.Contains(last.Text, "Discord exporter: 2.48.0 · active") {
			t.Fatalf("expected 'Discord exporter: 2.48.0 · active' in status, got:\n%s", last.Text)
		}
	})

	t.Run("candidate pending reports candidate pending", func(t *testing.T) {
		dir := t.TempDir()
		store := state.NewStore(dir)
		sender := &fakeSender{}

		mgr, err := dce.NewManager(dir, "/path/to/dce", "fake-token",
			dce.WithBootstrapVersion("2.48.0"),
		)
		if err != nil {
			t.Fatal(err)
		}

		// Stage candidate in updater state
		st := mgr.State()
		st.CandidateVersion = "2.49.0"
		st.CandidatePath = "/path/to/cand/dce"
		statePath := filepath.Join(dir, "dce", "updater.json")
		if err := dce.SaveUpdaterState(statePath, &st); err != nil {
			t.Fatal(err)
		}

		// Reload manager to pick up state
		mgr, _ = dce.NewManager(dir, "/path/to/dce", "fake-token",
			dce.WithBootstrapVersion("2.48.0"),
		)

		b, err := New(context.Background(), &EnvConfig{
			BotToken: "test-token",
			OwnerID:  12345,
			DataDir:  dir,
		}, store, WithSender(sender), WithDCEManager(mgr))
		if err != nil {
			t.Fatal(err)
		}

		b.handleStatus(context.Background(), 12345)
		last := sender.lastSent()
		if last == nil {
			t.Fatal("expected message sent, got nil")
		}
		if !strings.Contains(last.Text, "Discord exporter: 2.48.0 · candidate 2.49.0 pending") {
			t.Fatalf("expected candidate pending status, got:\n%s", last.Text)
		}
	})

	t.Run("rejected version reports rejected", func(t *testing.T) {
		dir := t.TempDir()
		store := state.NewStore(dir)
		sender := &fakeSender{}

		mgr, err := dce.NewManager(dir, "/path/to/dce", "fake-token",
			dce.WithBootstrapVersion("2.48.0"),
		)
		if err != nil {
			t.Fatal(err)
		}

		// Stage rejected in updater state
		st := mgr.State()
		st.RejectedVersion = "2.49.0"
		statePath := filepath.Join(dir, "dce", "updater.json")
		if err := dce.SaveUpdaterState(statePath, &st); err != nil {
			t.Fatal(err)
		}

		mgr, _ = dce.NewManager(dir, "/path/to/dce", "fake-token",
			dce.WithBootstrapVersion("2.48.0"),
		)

		b, err := New(context.Background(), &EnvConfig{
			BotToken: "test-token",
			OwnerID:  12345,
			DataDir:  dir,
		}, store, WithSender(sender), WithDCEManager(mgr))
		if err != nil {
			t.Fatal(err)
		}

		b.handleStatus(context.Background(), 12345)
		last := sender.lastSent()
		if last == nil {
			t.Fatal("expected message sent, got nil")
		}
		if !strings.Contains(last.Text, "Discord exporter: 2.48.0 · 2.49.0 rejected") {
			t.Fatalf("expected rejected status, got:\n%s", last.Text)
		}
	})

	t.Run("last check persistence across restart", func(t *testing.T) {
		dir := t.TempDir()
		statePath := filepath.Join(dir, "dce", "updater.json")
		checkTime := time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)

		st := &dce.UpdaterState{
			ActiveVersion: "2.48.0",
			ActivePath:    "/path/to/dce",
			LastCheck:     checkTime,
		}
		if err := dce.SaveUpdaterState(statePath, st); err != nil {
			t.Fatal(err)
		}

		// Create manager from same dir
		mgr, err := dce.NewManager(dir, "/path/to/dce", "fake-token")
		if err != nil {
			t.Fatal(err)
		}

		loadedSt := mgr.State()
		if !loadedSt.LastCheck.Equal(checkTime) {
			t.Fatalf("expected LastCheck %v, got %v", checkTime, loadedSt.LastCheck)
		}
	})

	t.Run("new initializes dce manager from env", func(t *testing.T) {
		dir := t.TempDir()
		store := state.NewStore(dir)
		sender := &fakeSender{}

		fakeExe := filepath.Join(dir, "fake-dce")
		if err := os.WriteFile(fakeExe, []byte("dce"), 0755); err != nil {
			t.Fatal(err)
		}

		b, err := New(context.Background(), &EnvConfig{
			BotToken:     "test-token",
			OwnerID:      12345,
			DataDir:      dir,
			DCEPath:      fakeExe,
			DiscordToken: "fake-discord-token",
		}, store, WithSender(sender))
		if err != nil {
			t.Fatal(err)
		}

		if b.DCEManager() == nil {
			t.Fatal("expected DCEManager to be initialized from env config")
		}
		if !b.DCEManager().IsConfigured() {
			t.Fatal("expected DCEManager to be configured")
		}
	})
}
