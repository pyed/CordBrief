package scheduler

import (
	"fmt"
	"strings"
	"time"

	"cordbrief/internal/config"
)

// ComputeSlotID returns the deterministic identifier for a daily slot.
func ComputeSlotID(tz, dateStr, wallTime string) string {
	return fmt.Sprintf("%s/%s/%s", tz, dateStr, wallTime)
}

// SlotStatus holds evaluation results for a scheduling check.
type SlotStatus struct {
	Due         bool
	SlotID      string
	StatusText  string
	NextRunTime *time.Time
}

// EvaluateSlot evaluates whether the scheduler should execute for the given time and config.
func EvaluateSlot(cfg config.ScheduleConfig, state *State, now time.Time) SlotStatus {
	if !cfg.Enabled {
		return SlotStatus{
			Due:        false,
			StatusText: "Automatic daily digest is disabled",
		}
	}

	tz := strings.TrimSpace(cfg.Timezone)
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return SlotStatus{
			Due:        false,
			StatusText: fmt.Sprintf("Invalid timezone: %s", tz),
		}
	}

	wallTime := strings.TrimSpace(cfg.Time)
	parsedTime, err := time.Parse("15:04", wallTime)
	if err != nil {
		return SlotStatus{
			Due:        false,
			StatusText: fmt.Sprintf("Invalid time: %s", wallTime),
		}
	}
	targetHour := parsedTime.Hour()
	targetMinute := parsedTime.Minute()

	localNow := now.In(loc)
	localDate := localNow.Format("2006-01-02")
	todaySlotID := ComputeSlotID(tz, localDate, wallTime)

	// Target time today in local timezone
	targetToday := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), targetHour, targetMinute, 0, 0, loc)

	// Case 1: Current local time is before today's scheduled time
	if localNow.Before(targetToday) {
		return SlotStatus{
			Due:         false,
			SlotID:      todaySlotID,
			StatusText:  fmt.Sprintf("Today at %s (%s)", wallTime, tz),
			NextRunTime: &targetToday,
		}
	}

	// Case 2: Today's slot has already been completed successfully
	if state != nil && state.LastCompletedSlot == todaySlotID {
		// Tomorrow's run
		tomorrow := localNow.AddDate(0, 0, 1)
		targetTomorrow := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), targetHour, targetMinute, 0, 0, loc)
		return SlotStatus{
			Due:         false,
			SlotID:      todaySlotID,
			StatusText:  fmt.Sprintf("Tomorrow at %s (%s)", wallTime, tz),
			NextRunTime: &targetTomorrow,
		}
	}

	// Case 3: In retry backoff window from a recent failure
	if state != nil && state.NextRetryAt != nil && now.Before(*state.NextRetryAt) {
		retryLocal := state.NextRetryAt.In(loc)
		return SlotStatus{
			Due:         false,
			SlotID:      todaySlotID,
			StatusText:  fmt.Sprintf("Retry scheduled at %s (%s)", retryLocal.Format("15:04"), tz),
			NextRunTime: state.NextRetryAt,
		}
	}

	// Case 4: Wall clock >= configured time and today's slot is incomplete -> DUE!
	return SlotStatus{
		Due:        true,
		SlotID:     todaySlotID,
		StatusText: "Digest is due now",
	}
}
