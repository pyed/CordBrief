package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"cordbrief/internal/config"
	"cordbrief/internal/digest"
	"cordbrief/internal/journal"
)

type mockRunner struct {
	mu           sync.Mutex
	callCount    int
	lastTrigger  *digest.TriggerInfo
	resultToEmit *digest.TransactionResult
	errToEmit    error
}

func (m *mockRunner) RunDigest(ctx context.Context, trigger *digest.TriggerInfo) (*digest.TransactionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	m.lastTrigger = trigger
	return m.resultToEmit, m.errToEmit
}

func setupTestService(t *testing.T, initialTime time.Time, sch config.ScheduleConfig) (*Service, *mockRunner, *time.Time) {
	t.Helper()
	tmpDir := t.TempDir()
	store, err := config.NewStore(tmpDir, "")
	if err != nil {
		t.Fatal(err)
	}

	_ = store.SaveScheduleConfig(sch)

	currentTime := initialTime
	clockFunc := func() time.Time {
		return currentTime
	}

	runner := &mockRunner{
		resultToEmit: &digest.TransactionResult{
			Batch: &digest.Batch{BatchID: "batch-101"},
		},
	}

	svc, err := NewService(ServiceOptions{
		Store:         store,
		DataDir:       tmpDir,
		Runner:        runner,
		Clock:         clockFunc,
		CheckInterval: 1 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	return svc, runner, &currentTime
}

func TestScheduler_DisabledDoesNotRun(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	now := time.Date(2026, 9, 4, 10, 0, 0, 0, loc) // 10:00 > 08:00
	sch := config.ScheduleConfig{
		Enabled:  false,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}

	svc, runner, _ := setupTestService(t, now, sch)
	ran, err := svc.CheckAndRunSlot(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ran || runner.callCount != 0 {
		t.Fatalf("expected disabled scheduler not to run, ran=%v calls=%d", ran, runner.callCount)
	}
}

func TestScheduler_BeforeConfiguredTimeNotDue(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	now := time.Date(2026, 9, 4, 7, 59, 0, 0, loc) // 07:59 < 08:00
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}

	svc, runner, _ := setupTestService(t, now, sch)
	ran, err := svc.CheckAndRunSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ran || runner.callCount != 0 {
		t.Fatalf("expected before-time not to run, calls=%d", runner.callCount)
	}
}

func TestScheduler_ExactConfiguredTimeDue(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, loc) // 08:00 == 08:00
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}

	svc, runner, _ := setupTestService(t, now, sch)
	ran, err := svc.CheckAndRunSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ran || runner.callCount != 1 {
		t.Fatalf("expected exact-time to run, ran=%v calls=%d", ran, runner.callCount)
	}
	if runner.lastTrigger == nil || runner.lastTrigger.SlotID != "Asia/Riyadh/2026-09-04/08:00" {
		t.Fatalf("unexpected trigger info: %+v", runner.lastTrigger)
	}
}

func TestScheduler_AfterTimeSameDayDueIfIncomplete(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	now := time.Date(2026, 9, 4, 14, 30, 0, 0, loc) // 14:30 > 08:00, started later
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}

	svc, runner, _ := setupTestService(t, now, sch)
	ran, err := svc.CheckAndRunSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ran || runner.callCount != 1 {
		t.Fatalf("expected catch-up run when started after time, ran=%v calls=%d", ran, runner.callCount)
	}
}

func TestScheduler_CompletedSlotDoesNotRunAgain(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	now := time.Date(2026, 9, 4, 8, 5, 0, 0, loc)
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}

	svc, runner, curTime := setupTestService(t, now, sch)
	// Run 1: completes
	ran, _ := svc.CheckAndRunSlot(context.Background())
	if !ran || runner.callCount != 1 {
		t.Fatal("first run failed")
	}

	// Advance clock by 10 minutes same day
	*curTime = now.Add(10 * time.Minute)
	ran2, _ := svc.CheckAndRunSlot(context.Background())
	if ran2 || runner.callCount != 1 {
		t.Fatalf("completed slot must not re-run on same day, calls=%d", runner.callCount)
	}
}

func TestScheduler_RestartAfterScheduledTimeCatchesUp(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := config.NewStore(tmpDir, "")
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}
	_ = store.SaveScheduleConfig(sch)

	loc, _ := time.LoadLocation("Asia/Riyadh")
	bootTime := time.Date(2026, 9, 4, 11, 0, 0, 0, loc) // rebooted at 11:00

	runner := &mockRunner{
		resultToEmit: &digest.TransactionResult{Batch: &digest.Batch{BatchID: "b-reboot"}},
	}

	svc, err := NewService(ServiceOptions{
		Store:   store,
		DataDir: tmpDir,
		Runner:  runner,
		Clock:   func() time.Time { return bootTime },
	})
	if err != nil {
		t.Fatal(err)
	}

	ran, err := svc.CheckAndRunSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ran || runner.callCount != 1 {
		t.Fatalf("expected restart catch-up run, ran=%v calls=%d", ran, runner.callCount)
	}

	// Verify state saved to disk
	st, err := LoadState(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastCompletedSlot != "Asia/Riyadh/2026-09-04/08:00" {
		t.Errorf("unexpected completed slot: %s", st.LastCompletedSlot)
	}
}

func TestScheduler_NextLocalDateCreatesNewSlot(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	day1 := time.Date(2026, 9, 4, 8, 1, 0, 0, loc)
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}

	svc, runner, curTime := setupTestService(t, day1, sch)
	// Day 1 run
	ran, _ := svc.CheckAndRunSlot(context.Background())
	if !ran || runner.callCount != 1 {
		t.Fatal("day 1 run failed")
	}

	// Advance to Day 2 at 08:01
	day2 := time.Date(2026, 9, 5, 8, 1, 0, 0, loc)
	*curTime = day2

	ranDay2, _ := svc.CheckAndRunSlot(context.Background())
	if !ranDay2 || runner.callCount != 2 {
		t.Fatalf("expected day 2 to execute new slot, calls=%d", runner.callCount)
	}
	if runner.lastTrigger.SlotID != "Asia/Riyadh/2026-09-05/08:00" {
		t.Errorf("unexpected slot id on day 2: %s", runner.lastTrigger.SlotID)
	}
}

func TestScheduler_FailureRetryInterval(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, loc)
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}

	svc, runner, curTime := setupTestService(t, now, sch)
	runner.errToEmit = errors.New("simulated LLM provider timeout")

	// 1. First execution fails
	ran, err := svc.CheckAndRunSlot(context.Background())
	if err == nil || !ran || runner.callCount != 1 {
		t.Fatalf("expected failure, got ran=%v err=%v", ran, err)
	}

	// Verify failure state
	if svc.state.LastResult != ResultError || svc.state.NextRetryAt == nil {
		t.Fatalf("expected failure state recorded, got: %+v", svc.state)
	}

	// 2. Retry before 15 minutes (at 8:10) -> suppressed by backoff
	*curTime = now.Add(10 * time.Minute)
	ranRetryEarly, errRetryEarly := svc.CheckAndRunSlot(context.Background())
	if ranRetryEarly || errRetryEarly != nil || runner.callCount != 1 {
		t.Fatalf("early retry must not call runner, ran=%v calls=%d", ranRetryEarly, runner.callCount)
	}

	// 3. Retry at 15 minutes (at 8:15) -> executes again!
	*curTime = now.Add(15 * time.Minute)
	runner.errToEmit = nil // Provider succeeds now
	runner.resultToEmit = &digest.TransactionResult{Batch: &digest.Batch{BatchID: "b-retry-ok"}}

	ranRetryDue, errRetryDue := svc.CheckAndRunSlot(context.Background())
	if errRetryDue != nil || !ranRetryDue || runner.callCount != 2 {
		t.Fatalf("expected retry after 15m to succeed, ran=%v calls=%d err=%v", ranRetryDue, runner.callCount, errRetryDue)
	}
	if svc.state.LastCompletedSlot != "Asia/Riyadh/2026-09-04/08:00" {
		t.Errorf("slot not completed after retry: %s", svc.state.LastCompletedSlot)
	}
	if svc.state.NextRetryAt != nil {
		t.Errorf("expected NextRetryAt cleared after success")
	}
}

func TestScheduler_EmptyAndExcludedCompleteSlot(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, loc)
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}

	// A. Empty batch
	svc, runner, _ := setupTestService(t, now, sch)
	runner.resultToEmit = &digest.TransactionResult{Empty: true}

	ran, err := svc.CheckAndRunSlot(context.Background())
	if err != nil || !ran {
		t.Fatal(err)
	}
	if svc.state.LastCompletedSlot != "Asia/Riyadh/2026-09-04/08:00" || svc.state.LastResult != ResultEmpty {
		t.Fatalf("expected empty result to complete slot: %+v", svc.state)
	}

	// B. All excluded batch
	now2 := time.Date(2026, 9, 5, 8, 0, 0, 0, loc)
	svc2, runner2, _ := setupTestService(t, now2, sch)
	runner2.resultToEmit = &digest.TransactionResult{AllExcluded: true}

	ran2, err2 := svc2.CheckAndRunSlot(context.Background())
	if err2 != nil || !ran2 {
		t.Fatal(err2)
	}
	if svc2.state.LastCompletedSlot != "Asia/Riyadh/2026-09-05/08:00" || svc2.state.LastResult != ResultEmpty {
		t.Fatalf("expected all-excluded result to complete slot: %+v", svc2.state)
	}
}

func TestScheduler_DSTFallBackExecutesOnce(t *testing.T) {
	// London fall-back: last Sunday in October (e.g. 2026-10-25).
	// Clocks go back 1 hour from 02:00 BST to 01:00 GMT.
	// Slot scheduled at 01:30: occurs in wall clock before and after fall-back.
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}

	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "01:30",
		Timezone: "Europe/London",
	}

	// First time clock hits 01:30 BST
	first130 := time.Date(2026, 10, 25, 1, 30, 0, 0, loc)
	svc, runner, curTime := setupTestService(t, first130, sch)

	ran1, _ := svc.CheckAndRunSlot(context.Background())
	if !ran1 || runner.callCount != 1 {
		t.Fatalf("first 01:30 must execute, ran=%v calls=%d", ran1, runner.callCount)
	}

	// 1 hour later (clocks went back, second 01:30 GMT occurs)
	second130 := first130.Add(1 * time.Hour)
	*curTime = second130

	ran2, _ := svc.CheckAndRunSlot(context.Background())
	if ran2 || runner.callCount != 1 {
		t.Fatalf("DST fall-back duplicate must NOT execute second time, calls=%d", runner.callCount)
	}
}

func TestScheduler_SpringForwardExecutesWhenPassed(t *testing.T) {
	// New York spring-forward: second Sunday in March (e.g. 2026-03-08).
	// Clocks jump from 02:00 EST to 03:00 EDT (02:30 does not physically exist).
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}

	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "02:30",
		Timezone: "America/New_York",
	}

	// At 03:05 EDT (clock has jumped past 02:30)
	time305 := time.Date(2026, 3, 8, 3, 5, 0, 0, loc)
	svc, runner, _ := setupTestService(t, time305, sch)

	ran, err := svc.CheckAndRunSlot(context.Background())
	if err != nil || !ran || runner.callCount != 1 {
		t.Fatalf("spring forward time must become due once clock passes it, ran=%v calls=%d", ran, runner.callCount)
	}
}

func TestScheduler_CursorCommitBeforeStateCrashRecovery(t *testing.T) {
	// Boundary B: cursor committed, but crash before scheduler-state recorded completion.
	tmpDir := t.TempDir()
	store, _ := config.NewStore(tmpDir, "")
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "UTC",
	}
	_ = store.SaveScheduleConfig(sch)

	now := time.Date(2026, 9, 4, 8, 30, 0, 0, time.UTC)

	// State on disk is empty (not marked completed)
	runner := &mockRunner{
		// Since cursor committed, subsequent run returns Empty
		resultToEmit: &digest.TransactionResult{
			Empty:           true,
			CommittedCursor: &journal.Cursor{Segment: 1, Offset: 1000},
		},
	}

	svc, err := NewService(ServiceOptions{
		Store:   store,
		DataDir: tmpDir,
		Runner:  runner,
		Clock:   func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	ran, err := svc.CheckAndRunSlot(context.Background())
	if err != nil || !ran || runner.callCount != 1 {
		t.Fatalf("expected recovery check to execute, ran=%v calls=%d", ran, runner.callCount)
	}

	// Slot is now safely marked complete, zero duplicate digest
	if svc.state.LastCompletedSlot != "UTC/2026-09-04/08:00" {
		t.Errorf("slot not marked completed: %s", svc.state.LastCompletedSlot)
	}
	if svc.state.LastResult != ResultEmpty {
		t.Errorf("expected empty result, got %s", svc.state.LastResult)
	}
}

func TestScheduler_PersistedRetrySurvivesServiceRestart(t *testing.T) {
	// Sequence:
	// 08:00 provider fails -> NextRetryAt = 08:15 persisted
	// Recreate scheduler Service from disk -> 08:10 CheckAndRunSlot -> ZERO calls -> 08:15 retry allowed
	tmpDir := t.TempDir()
	store, _ := config.NewStore(tmpDir, "")
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}
	_ = store.SaveScheduleConfig(sch)

	loc, _ := time.LoadLocation("Asia/Riyadh")
	t0800 := time.Date(2026, 9, 4, 8, 0, 0, 0, loc)

	currentClock := t0800
	runner1 := &mockRunner{
		errToEmit: errors.New("gemini provider temporarily down (503)"),
	}

	svc1, err := NewService(ServiceOptions{
		Store:   store,
		DataDir: tmpDir,
		Runner:  runner1,
		Clock:   func() time.Time { return currentClock },
	})
	if err != nil {
		t.Fatal(err)
	}

	// 08:00: execution fails
	ran, err := svc1.CheckAndRunSlot(context.Background())
	if err == nil || !ran || runner1.callCount != 1 {
		t.Fatalf("expected initial run to execute and fail, ran=%v calls=%d err=%v", ran, runner1.callCount, err)
	}

	// Verify scheduler-state.json was persisted on disk with NextRetryAt = 08:15
	stOnDisk, err := LoadState(tmpDir)
	if err != nil {
		t.Fatalf("failed loading persisted state: %v", err)
	}
	if stOnDisk.LastResult != ResultError {
		t.Fatalf("expected ResultError on disk, got %s", stOnDisk.LastResult)
	}
	expectedRetry := t0800.Add(RetryInterval)
	if stOnDisk.NextRetryAt == nil || !stOnDisk.NextRetryAt.Equal(expectedRetry) {
		t.Fatalf("expected persisted NextRetryAt %v, got %v", expectedRetry, stOnDisk.NextRetryAt)
	}

	// Restart Service: create fresh Service reading same tmpDir on disk
	runner2 := &mockRunner{
		resultToEmit: &digest.TransactionResult{
			Artifact: &digest.Artifact{BatchID: "batch-recovered-0815"},
		},
	}
	svc2, err := NewService(ServiceOptions{
		Store:   store,
		DataDir: tmpDir,
		Runner:  runner2,
		Clock:   func() time.Time { return currentClock },
	})
	if err != nil {
		t.Fatal(err)
	}

	// At 08:10 (before 08:15 retry window): must suppress execution with 0 provider calls
	currentClock = time.Date(2026, 9, 4, 8, 10, 0, 0, loc)
	ran2, err2 := svc2.CheckAndRunSlot(context.Background())
	if err2 != nil {
		t.Fatalf("unexpected error at 08:10: %v", err2)
	}
	if ran2 || runner2.callCount != 0 {
		t.Fatalf("expected ZERO provider calls during backoff across service restart, ran=%v calls=%d", ran2, runner2.callCount)
	}

	// At 08:15: retry window reached -> retry allowed and succeeds
	currentClock = time.Date(2026, 9, 4, 8, 15, 0, 0, loc)
	ran3, err3 := svc2.CheckAndRunSlot(context.Background())
	if err3 != nil {
		t.Fatalf("expected success at 08:15, got err: %v", err3)
	}
	if !ran3 || runner2.callCount != 1 {
		t.Fatalf("expected retry execution at 08:15, ran=%v calls=%d", ran3, runner2.callCount)
	}
	if svc2.state.LastCompletedSlot != "Asia/Riyadh/2026-09-04/08:00" {
		t.Errorf("slot not completed after retry: %s", svc2.state.LastCompletedSlot)
	}
	if svc2.state.LastBatchID != "batch-recovered-0815" {
		t.Errorf("expected LastBatchID batch-recovered-0815, got: %s", svc2.state.LastBatchID)
	}
}

func TestScheduler_PreAttemptSaveStateFailureBlocksRunner(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, loc)
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}

	tmpDir := t.TempDir()
	store, _ := config.NewStore(tmpDir, "")
	_ = store.SaveScheduleConfig(sch)

	runner := &mockRunner{
		resultToEmit: &digest.TransactionResult{Batch: &digest.Batch{BatchID: "batch-xyz"}},
	}

	// Inject a failing SaveStateFn for the pre-attempt write
	injectedErr := errors.New("simulated disk I/O error on pre-attempt save")
	svc, err := NewService(ServiceOptions{
		Store:   store,
		DataDir: tmpDir,
		Runner:  runner,
		Clock:   func() time.Time { return now },
		SaveStateFn: func(dataDir string, s *State) error {
			return injectedErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ran, runErr := svc.CheckAndRunSlot(context.Background())
	if runErr == nil {
		t.Fatal("expected error when pre-attempt state persistence fails")
	}
	if ran {
		t.Errorf("expected ran=false on pre-attempt failure, got ran=true")
	}
	// Verify runner / LLM was NEVER invoked
	if runner.callCount != 0 {
		t.Fatalf("RUNNER MUST NOT BE INVOKED when pre-attempt state persistence fails, calls=%d", runner.callCount)
	}
}

func TestScheduler_CompletionSaveStateFailureDoesNotUndoTransaction(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, loc)
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}

	tmpDir := t.TempDir()
	store, _ := config.NewStore(tmpDir, "")
	_ = store.SaveScheduleConfig(sch)

	runner := &mockRunner{
		resultToEmit: &digest.TransactionResult{
			Artifact: &digest.Artifact{BatchID: "batch-committed-123"},
		},
	}

	// Allow pre-attempt save (call 1) to succeed, but fail on completion save (call 2)
	saveCallCount := 0
	svc, err := NewService(ServiceOptions{
		Store:   store,
		DataDir: tmpDir,
		Runner:  runner,
		Clock:   func() time.Time { return now },
		SaveStateFn: func(dataDir string, s *State) error {
			saveCallCount++
			if saveCallCount == 1 {
				return SaveState(dataDir, s) // pre-attempt succeeds
			}
			return errors.New("simulated disk full error on completion save")
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ran, runErr := svc.CheckAndRunSlot(context.Background())
	// Transaction executed (ran=true) but caller receives error regarding completion save failure
	if !ran {
		t.Errorf("expected ran=true because transaction succeeded")
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "saving scheduler state failed") {
		t.Fatalf("expected save error reported to caller, got: %v", runErr)
	}
	if runner.callCount != 1 {
		t.Fatalf("expected transaction to have executed once, calls=%d", runner.callCount)
	}
}

func TestScheduler_PreventStaleLastBatchID(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	day1Time := time.Date(2026, 9, 4, 8, 0, 0, 0, loc)
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}

	svc, runner, curTime := setupTestService(t, day1Time, sch)

	// Day 1: produces a real digest artifact with batch ID
	runner.resultToEmit = &digest.TransactionResult{
		Artifact: &digest.Artifact{BatchID: "batch-day-1-abc"},
	}
	ran, err := svc.CheckAndRunSlot(context.Background())
	if err != nil || !ran {
		t.Fatalf("day 1 run failed: %v", err)
	}
	if svc.state.LastResult != ResultSuccess || svc.state.LastBatchID != "batch-day-1-abc" {
		t.Fatalf("expected success with batch-day-1-abc, got result=%s batch=%s", svc.state.LastResult, svc.state.LastBatchID)
	}

	// Day 2: 08:00 next day, but journal has 0 new records -> Empty result
	day2Time := time.Date(2026, 9, 5, 8, 0, 0, 0, loc)
	*curTime = day2Time
	runner.resultToEmit = &digest.TransactionResult{
		Empty: true,
	}

	ran2, err2 := svc.CheckAndRunSlot(context.Background())
	if err2 != nil || !ran2 {
		t.Fatalf("day 2 run failed: %v", err2)
	}
	if svc.state.LastResult != ResultEmpty {
		t.Fatalf("expected ResultEmpty for day 2, got: %s", svc.state.LastResult)
	}
	// LastBatchID MUST NOT retain Day 1's batch ID
	if svc.state.LastBatchID != "" {
		t.Fatalf("STALE BATCH ID DETECTED: LastBatchID should be empty for empty slot, got: %q", svc.state.LastBatchID)
	}

	// Day 3: provider fails -> Error result
	day3Time := time.Date(2026, 9, 6, 8, 0, 0, 0, loc)
	*curTime = day3Time
	runner.resultToEmit = nil
	runner.errToEmit = errors.New("provider failure")

	ran3, err3 := svc.CheckAndRunSlot(context.Background())
	if err3 == nil || !ran3 {
		t.Fatalf("day 3 run expected error: ran=%v err=%v", ran3, err3)
	}
	if svc.state.LastResult != ResultError {
		t.Fatalf("expected ResultError for day 3, got: %s", svc.state.LastResult)
	}
	if svc.state.LastBatchID != "" {
		t.Fatalf("LastBatchID should be empty on error, got: %q", svc.state.LastBatchID)
	}
}

func TestScheduler_ConcurrentDueChecksSingleFlightAndNoStaleExecution(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, loc)
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}

	tmpDir := t.TempDir()
	store, _ := config.NewStore(tmpDir, "")
	_ = store.SaveScheduleConfig(sch)

	// Make runner slow to simulate in-flight execution, and fail with error
	runner := &mockRunner{
		errToEmit: errors.New("provider timeout"),
	}

	sharedLock := &sync.Mutex{}
	currentClock := now

	svc, err := NewService(ServiceOptions{
		Store:   store,
		DataDir: tmpDir,
		Runner:  runner,
		Clock:   func() time.Time { return currentClock },
		Lock:    sharedLock,
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	rans := make([]bool, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			r, e := svc.CheckAndRunSlot(context.Background())
			rans[idx] = r
			errs[idx] = e
		}()
	}

	wg.Wait()

	// Exactly ONE goroutine should execute the runner;
	// the second goroutine waiting on s.lock must re-evaluate, see NextRetryAt active, and NOT execute
	if runner.callCount != 1 {
		t.Fatalf("expected exactly 1 runner call under concurrency, got: %d", runner.callCount)
	}

	// One should have ran (and failed), the other should have returned ran=false, err=nil (or both handled safely)
	ranCount := 0
	for _, r := range rans {
		if r {
			ranCount++
		}
	}
	if ranCount != 1 {
		t.Fatalf("expected exactly 1 caller to execute slot, got ranCount=%d", ranCount)
	}
}

func TestScheduler_SafeLastErrorSanitization(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, loc)
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}

	svc, runner, _ := setupTestService(t, now, sch)

	// Craft an error containing sensitive tokens, authorization headers, and long body > 300 chars
	secretBearer := "Bearer ya29.a0AfH6SMBsecretToken1234567890"
	secretGeminiKey := "AIzaSyD1234567890123456789012345678901"
	hugePayload := strings.Repeat("Extremely verbose provider response body fragment. ", 15)
	adversarialErr := errors.New("post https://generativelanguage.googleapis.com/: 401 Unauthorized with header " + secretBearer + " and key=" + secretGeminiKey + " payload: " + hugePayload)

	runner.errToEmit = adversarialErr

	ran, err := svc.CheckAndRunSlot(context.Background())
	if err == nil || !ran {
		t.Fatalf("expected error, got ran=%v err=%v", ran, err)
	}

	lastErr := svc.state.LastError
	if len(lastErr) > 256 {
		t.Fatalf("persisted LastError length (%d) exceeds 256 char bound", len(lastErr))
	}
	if strings.Contains(lastErr, secretBearer) {
		t.Fatalf("SECURITY VIOLATION: Bearer token leaked in LastError: %s", lastErr)
	}
	if strings.Contains(lastErr, secretGeminiKey) {
		t.Fatalf("SECURITY VIOLATION: Gemini key leaked in LastError: %s", lastErr)
	}
	if strings.Contains(lastErr, "\n") || strings.Contains(lastErr, "\r") {
		t.Fatalf("persisted LastError contains newline characters: %q", lastErr)
	}
	if !strings.Contains(lastErr, "[REDACTED]") && !strings.Contains(lastErr, "[REDACTED_GEMINI_KEY]") {
		t.Fatalf("expected redacted token in LastError, got: %s", lastErr)
	}
}

type mockDeliveryEnqueuer struct {
	mu           sync.Mutex
	enqueuedList []string
	errToEmit    error
}

func (m *mockDeliveryEnqueuer) Enqueue(batchID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enqueuedList = append(m.enqueuedList, batchID)
	return m.errToEmit
}

func TestDigestDeliveryTriggerPolicy(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	now := time.Date(2026, 9, 4, 10, 0, 0, 0, loc)
	sch := config.ScheduleConfig{
		Enabled:  true,
		Time:     "08:00",
		Timezone: "Asia/Riyadh",
	}

	tmpDir := t.TempDir()
	store, err := config.NewStore(tmpDir, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = store.SaveScheduleConfig(sch)

	// Case 1: Telegram NOT enabled in config -> Enqueue is NOT called
	runner := &mockRunner{
		resultToEmit: &digest.TransactionResult{
			Batch:    &digest.Batch{BatchID: strings.Repeat("1", 64)},
			Artifact: &digest.Artifact{BatchID: strings.Repeat("1", 64)},
		},
	}
	enq := &mockDeliveryEnqueuer{}

	svc, err := NewService(ServiceOptions{
		Store:            store,
		DataDir:          tmpDir,
		Runner:           runner,
		DeliveryEnqueuer: enq,
		Clock:            func() time.Time { return now },
		CheckInterval:    1 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	ran, err := svc.CheckAndRunSlot(context.Background())
	if err != nil || !ran {
		t.Fatalf("expected slot to run, ran=%v err=%v", ran, err)
	}
	if len(enq.enqueuedList) != 0 {
		t.Errorf("expected 0 enqueued batches when telegram disabled, got %d", len(enq.enqueuedList))
	}

	// Case 2: Telegram configured and enabled -> Enqueue is called post-commit
	now2 := now.Add(24 * time.Hour)
	delCfg := store.GetDeliveryConfig()
	delCfg.Telegram.Enabled = true
	delCfg.Telegram.ChatID = "-100123456789"
	_ = store.SaveDeliveryConfig(delCfg)
	_ = store.SaveTelegramBotToken("fake-token")

	runner.resultToEmit = &digest.TransactionResult{
		Batch:    &digest.Batch{BatchID: strings.Repeat("2", 64)},
		Artifact: &digest.Artifact{BatchID: strings.Repeat("2", 64)},
	}

	svc.clock = func() time.Time { return now2 }
	ran, err = svc.CheckAndRunSlot(context.Background())
	if err != nil || !ran {
		t.Fatalf("expected slot to run, ran=%v err=%v", ran, err)
	}
	if len(enq.enqueuedList) != 1 || enq.enqueuedList[0] != strings.Repeat("2", 64) {
		t.Errorf("expected batch %s to be enqueued, got %v", strings.Repeat("2", 64), enq.enqueuedList)
	}

	// Case 3: Enqueue error does NOT cause CheckAndRunSlot to fail or rollback cursor
	now3 := now2.Add(24 * time.Hour)
	runner.resultToEmit = &digest.TransactionResult{
		Batch:    &digest.Batch{BatchID: strings.Repeat("3", 64)},
		Artifact: &digest.Artifact{BatchID: strings.Repeat("3", 64)},
	}
	enq.errToEmit = errors.New("queue storage full")

	svc.clock = func() time.Time { return now3 }
	ran, err = svc.CheckAndRunSlot(context.Background())
	if err != nil || !ran {
		t.Fatalf("expected slot to succeed even if delivery enqueue errors, got ran=%v err=%v", ran, err)
	}
}
