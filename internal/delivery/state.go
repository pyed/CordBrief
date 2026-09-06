package delivery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DeliveryState represents the lifecycle status of a Telegram digest dispatch.
type DeliveryState string

const (
	StatePending   DeliveryState = "pending"
	StateSending   DeliveryState = "sending"
	StateSent      DeliveryState = "sent"
	StateFailed    DeliveryState = "failed"
	StateUncertain DeliveryState = "uncertain"

	CurrentDeliveryRecordVersion = 1
)

// DeliveryRecord captures durable state for a Telegram delivery attempt.
// It stores only safe metadata and never contains bot tokens or raw digest content.
type DeliveryRecord struct {
	Version            int           `json:"version"`
	DigestBatchID      string        `json:"digest_batch_id"`
	DestinationID      string        `json:"destination_id"`
	DestinationLabel   string        `json:"destination_label,omitempty"`
	State              DeliveryState `json:"state"`
	CreatedAt          time.Time     `json:"created_at"`
	UpdatedAt          time.Time     `json:"updated_at"`
	AttemptCount       int           `json:"attempt_count"`
	NextPart           int           `json:"next_part"`
	TotalParts         int           `json:"total_parts"`
	TelegramMessageIDs []int64       `json:"telegram_message_ids"`
	LastSafeError      string        `json:"last_safe_error,omitempty"`
}

// DeliveryRecordPath returns the canonical path to a delivery state file.
func DeliveryRecordPath(dataDir, batchID string) string {
	return filepath.Join(dataDir, "deliveries", batchID, "telegram.json")
}

// LoadDeliveryRecord reads the durable delivery state for a batch ID from disk.
func LoadDeliveryRecord(dataDir, batchID string) (*DeliveryRecord, error) {
	if strings.TrimSpace(batchID) == "" {
		return nil, errors.New("batch_id cannot be empty")
	}

	p := DeliveryRecordPath(dataDir, batchID)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}

	var rec DeliveryRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("unmarshal delivery record: %w", err)
	}
	return &rec, nil
}

// SaveDeliveryRecord writes the delivery record atomically with file sync and rename.
func SaveDeliveryRecord(dataDir string, rec *DeliveryRecord) error {
	if rec == nil {
		return errors.New("delivery record cannot be nil")
	}
	if strings.TrimSpace(rec.DigestBatchID) == "" {
		return errors.New("delivery record digest_batch_id cannot be empty")
	}

	targetPath := DeliveryRecordPath(dataDir, rec.DigestBatchID)
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create delivery directory: %w", err)
	}

	rec.UpdatedAt = time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = rec.UpdatedAt
	}
	if rec.Version == 0 {
		rec.Version = CurrentDeliveryRecordVersion
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal delivery record: %w", err)
	}
	data = append(data, '\n')

	tmpPath := fmt.Sprintf("%s.%d.tmp", targetPath, time.Now().UnixNano())
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("create tmp delivery record: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write tmp delivery record: %w", err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync tmp delivery record: %w", err)
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close tmp delivery record: %w", err)
	}

	return os.Rename(tmpPath, targetPath)
}
