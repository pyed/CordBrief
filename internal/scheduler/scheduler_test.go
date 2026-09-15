package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyed/CordBrief/internal/state"
)

func setupTestStore(t *testing.T) *state.Store {
	t.Helper()
	tmpDir := t.TempDir()
	return state.NewStore(tmpDir)
}

func TestNextRun_Calculations(t *testing.T) {
	locRiyadh, err := time.LoadLocation("Asia/Riyadh")
	if err != nil {
		t.Fatalf("failed to load Asia/Riyadh: %v", err)
	}

	// 1. Reference time before target today: should fire today
	// Now: 2026-09-15 06:30:00 AST (+03:00)
	// Target: 08:00
	nowBefore := time.Date(2026, 9, 15, 6, 30, 0, 0, locRiyadh)
	nextBefore := NextRun(nowBefore, locRiyadh, 8, 0)
	expectedBefore := time.Date(2026, 9, 15, 8, 0, 0, 0, locRiyadh)
	if !nextBefore.Equal(expectedBefore) {
		t.Errorf("expected %v, got %v", expectedBefore, nextBefore)
	}

	// 2. Reference time after target today: should fire tomorrow
	// Now: 2026-09-15 09:15:00 AST (+03:00)
	// Target: 08:00
	nowAfter := time.Date(2026, 9, 15, 9, 15, 0, 0, locRiyadh)
	nextAfter := NextRun(nowAfter, locRiyadh, 8, 0)
	expectedAfter := time.Date(2026, 9, 16, 8, 0, 0, 0, locRiyadh)
	if !nextAfter.Equal(expectedAfter) {
		t.Errorf("expected %v, got %v", expectedAfter, nextAfter)
	}

	// 3. Reference time exactly at target: should fire tomorrow
	nowExact := time.Date(2026, 9, 15, 8, 0, 0, 0, locRiyadh)
	nextExact := NextRun(nowExact, locRiyadh, 8, 0)
	expectedExact := time.Date(2026, 9, 16, 8, 0, 0, 0, locRiyadh)
	if !nextExact.Equal(expectedExact) {
		t.Errorf("expected %v, got %v", expectedExact, nextExact)
	}
}

func TestNextRunForConfig(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	now := time.Date(2026, 9, 15, 7, 0, 0, 0, loc)

	// Enabled config
	cfg := state.DefaultConfig()
	cfg.Schedule.Enabled = true
	cfg.Schedule.Time = "08:00"
	cfg.Timezone = "UTC"

	next, enabled, err := NextRunForConfig(cfg, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !enabled {
		t.Fatal("expected enabled to be true")
	}
	expected := time.Date(2026, 9, 15, 8, 0, 0, 0, loc)
	if !next.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, next)
	}

	// Disabled config
	cfg.Schedule.Enabled = false
	_, enabled, err = NextRunForConfig(cfg, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enabled {
		t.Fatal("expected enabled to be false")
	}

	// Invalid timezone
	cfg.Schedule.Enabled = true
	cfg.Timezone = "Invalid/Zone"
	_, _, err = NextRunForConfig(cfg, now)
	if err == nil {
		t.Fatal("expected error for invalid timezone")
	}

	// Invalid time format
	cfg.Timezone = "UTC"
	cfg.Schedule.Time = "99:99"
	_, _, err = NextRunForConfig(cfg, now)
	if err == nil {
		t.Fatal("expected error for invalid time")
	}
}

func TestScheduler_TriggerAndCollision(t *testing.T) {
	store := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Schedule.Enabled = true
	cfg.Schedule.Time = "08:00"
	cfg.Timezone = "UTC"
	_ = store.SaveConfig(cfg)

	loc, _ := time.LoadLocation("UTC")
	// Set mock clock to 07:59:59.980 (20ms before 08:00)
	var currentLock sync.Mutex
	currentTime := time.Date(2026, 9, 15, 7, 59, 59, 980000000, loc)
	mockNow := func() time.Time {
		currentLock.Lock()
		defer currentLock.Unlock()
		return currentTime
	}

	var triggerCalls int32
	var shouldCollide int32

	trigger := func(ctx context.Context) bool {
		atomic.AddInt32(&triggerCalls, 1)
		if atomic.LoadInt32(&shouldCollide) == 1 {
			// Simulate runner collision
			return false
		}
		return true
	}

	s := New(store, trigger, WithNow(mockNow))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(runDone)
	}()

	// Wait for timer to fire
	deadline := time.After(500 * time.Millisecond)
	for {
		if atomic.LoadInt32(&triggerCalls) >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for scheduled trigger")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Verify collision skip behavior
	atomic.StoreInt32(&shouldCollide, 1)
	// Advance clock to next day just before 08:00
	currentLock.Lock()
	currentTime = time.Date(2026, 9, 16, 7, 59, 59, 980000000, loc)
	currentLock.Unlock()
	s.Wake()

	deadline = time.After(500 * time.Millisecond)
	for {
		if atomic.LoadInt32(&triggerCalls) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for second trigger with collision")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(1 * time.Second):
		t.Fatal("scheduler did not stop cleanly on context cancellation")
	}
}

func TestScheduler_WakeRecomputesConfig(t *testing.T) {
	store := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Schedule.Enabled = false // initially disabled
	_ = store.SaveConfig(cfg)

	loc, _ := time.LoadLocation("UTC")
	var currentLock sync.Mutex
	currentTime := time.Date(2026, 9, 15, 10, 0, 0, 0, loc)
	mockNow := func() time.Time {
		currentLock.Lock()
		defer currentLock.Unlock()
		return currentTime
	}

	var triggerCalls int32
	trigger := func(ctx context.Context) bool {
		atomic.AddInt32(&triggerCalls, 1)
		return true
	}

	s := New(store, trigger, WithNow(mockNow))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(runDone)
	}()

	// Since schedule is disabled, trigger should not fire
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&triggerCalls) != 0 {
		t.Fatal("trigger fired while schedule was disabled")
	}

	// Now enable schedule for 10:00:00.020 (20ms in future)
	cfg.Schedule.Enabled = true
	cfg.Schedule.Time = "10:01"
	_ = store.SaveConfig(cfg)

	currentLock.Lock()
	currentTime = time.Date(2026, 9, 15, 10, 0, 59, 980000000, loc)
	currentLock.Unlock()

	// Wake scheduler
	s.Wake()

	// Scheduler should wake, reload config, set new timer, and fire
	deadline := time.After(500 * time.Millisecond)
	for {
		if atomic.LoadInt32(&triggerCalls) >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for trigger after wake")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-runDone
}
