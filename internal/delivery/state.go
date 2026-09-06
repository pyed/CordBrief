package delivery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cordbrief/internal/digest"
	"cordbrief/internal/durable"
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

// DeliveryRecord captures durable state for a Telegram digest delivery.
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
// Validates that batchID strictly matches 64 lowercase hexadecimal characters.
func DeliveryRecordPath(dataDir, batchID string) (string, error) {
	trimmed := strings.TrimSpace(batchID)
	if err := digest.ValidateBatchID(trimmed); err != nil {
		return "", fmt.Errorf("invalid delivery record batch_id: %w", err)
	}
	return filepath.Join(dataDir, "deliveries", trimmed, "telegram.json"), nil
}

// LoadDeliveryRecord reads the durable delivery state for a batch ID from disk.
func LoadDeliveryRecord(dataDir, batchID string) (*DeliveryRecord, error) {
	p, err := DeliveryRecordPath(dataDir, batchID)
	if err != nil {
		return nil, err
	}

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

// SaveDeliveryRecord writes the delivery record atomically with file sync and rename
// via durable.AtomicWriteJSON.
func SaveDeliveryRecord(dataDir string, rec *DeliveryRecord) error {
	if rec == nil {
		return errors.New("delivery record cannot be nil")
	}

	targetPath, err := DeliveryRecordPath(dataDir, rec.DigestBatchID)
	if err != nil {
		return err
	}

	rec.UpdatedAt = time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = rec.UpdatedAt
	}
	if rec.Version == 0 {
		rec.Version = CurrentDeliveryRecordVersion
	}

	return durable.AtomicWriteJSON(targetPath, rec, 0644)
}
