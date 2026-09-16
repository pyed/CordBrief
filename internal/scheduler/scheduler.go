package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/pyed/CordBrief/internal/state"
)

// TriggerFunc invokes a brief run. It returns true if started, false if skipped (e.g. collision).
type TriggerFunc func(ctx context.Context, deliveryAt time.Time) bool

// Option configures Scheduler instances.
type Option func(*Scheduler)

// WithNow configures a custom time source for deterministic testing.
func WithNow(now func() time.Time) Option {
	return func(s *Scheduler) {
		s.now = now
	}
}

// Scheduler handles automatic daily execution of the brief runner.
type Scheduler struct {
	store   *state.Store
	trigger TriggerFunc
	now     func() time.Time
	wake    chan struct{}
}

// New constructs a new Scheduler.
func New(store *state.Store, trigger TriggerFunc, opts ...Option) *Scheduler {
	s := &Scheduler{
		store:   store,
		trigger: trigger,
		now:     time.Now,
		wake:    make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Wake notifies the scheduler loop that configuration has changed and next run should be recomputed.
func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// NextRun computes the next future daily occurrence strictly after now for a given location, hour, and minute.
func NextRun(now time.Time, loc *time.Location, hour, minute int) time.Time {
	nowInLoc := now.In(loc)
	candidate := time.Date(nowInLoc.Year(), nowInLoc.Month(), nowInLoc.Day(), hour, minute, 0, 0, loc)
	if candidate.After(nowInLoc) {
		return candidate
	}
	return candidate.AddDate(0, 0, 1)
}

// NextRunForConfig calculates the next scheduled delivery time.
// It returns (nextTime, enabled, error).
func NextRunForConfig(cfg *state.Config, now time.Time) (time.Time, bool, error) {
	if cfg == nil || !cfg.Schedule.Enabled {
		return time.Time{}, false, nil
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("load timezone %q: %w", cfg.Timezone, err)
	}
	t, err := time.Parse("15:04", cfg.Schedule.Time)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse schedule time %q: %w", cfg.Schedule.Time, err)
	}
	return NextRun(now, loc, t.Hour(), t.Minute()), true, nil
}

// Run executes the scheduler loop until ctx is canceled.
func (s *Scheduler) Run(ctx context.Context) {
	var lastTarget time.Time
	for {
		cfg, err := s.store.LoadConfig()
		if err != nil {
			log.Printf("[scheduler] failed to load config: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			case <-time.After(10 * time.Second):
				continue
			}
		}

		reference := s.now()
		if lastTarget.After(reference) {
			reference = lastTarget
		}
		nextTime, enabled, err := NextRunForConfig(cfg, reference)
		if err != nil {
			log.Printf("[scheduler] invalid schedule config: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			}
		}

		if !enabled {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			}
		}

		delay := PreparationTime(cfg, nextTime).Sub(s.now())
		if delay < 0 {
			delay = 0
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
			continue
		case <-timer.C:
			lastTarget = nextTime
			if s.trigger != nil {
				if !s.trigger(ctx, nextTime) {
					log.Println("[scheduler] skipping scheduled brief: job already running")
				}
			}
		}
	}
}

// PreparationTime budgets one slot per channel, leaving the final slot for
// summarization. Even with spacing disabled, allow a minute per channel.
func PreparationTime(cfg *state.Config, deliveryAt time.Time) time.Time {
	lead := time.Duration(len(cfg.Channels)) * max(cfg.DCECooldown(), time.Minute)
	// ponytail: at most one day's lookahead; jobs that cannot fit still deliver late.
	lead = min(lead, 24*time.Hour-time.Minute)
	return deliveryAt.Add(-lead)
}
