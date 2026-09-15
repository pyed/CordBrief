package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/pyed/CordBrief/internal/state"
)

// TriggerFunc invokes a brief run. It returns true if started, false if skipped (e.g. collision).
type TriggerFunc func(ctx context.Context) bool

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

// NextRunForConfig calculates the next scheduled execution time from a Config and a reference time.
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

		nextTime, enabled, err := NextRunForConfig(cfg, s.now())
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

		delay := nextTime.Sub(s.now())
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
			if s.trigger != nil {
				if !s.trigger(ctx) {
					log.Println("[scheduler] skipping scheduled brief: job already running")
				}
			}
			// If timer fired microseconds early, wait until nextTime is strictly past
			if remaining := nextTime.Sub(s.now()); remaining > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(remaining + 10*time.Millisecond):
				}
			}
		}
	}
}
