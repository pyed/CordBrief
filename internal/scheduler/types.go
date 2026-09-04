package scheduler

import (
	"time"
)

const (
	// DefaultStateFilename is the scheduler state file in data directory.
	DefaultStateFilename = "scheduler-state.json"

	// RetryInterval is the fixed backoff interval after a failed execution.
	RetryInterval = 15 * time.Minute

	// ResultSuccess indicates a digest was generated and persisted.
	ResultSuccess = "success"
	// ResultEmpty indicates no unconsumed events were available or all were excluded.
	ResultEmpty = "empty"
	// ResultError indicates a transaction failure.
	ResultError = "error"
)

// State tracks execution history, slot completion, and retry schedules.
type State struct {
	Version           int        `json:"version"`
	LastCompletedSlot string     `json:"last_completed_slot,omitempty"`
	LastAttemptedSlot string     `json:"last_attempted_slot,omitempty"`
	LastAttemptAt     *time.Time `json:"last_attempt_at,omitempty"`
	NextRetryAt       *time.Time `json:"next_retry_at,omitempty"`
	LastResult        string     `json:"last_result,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	LastBatchID       string     `json:"last_batch_id,omitempty"`
}

// Clock abstracts time querying for deterministic testing.
type Clock func() time.Time
