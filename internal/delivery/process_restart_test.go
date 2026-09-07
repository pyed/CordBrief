package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cordbrief/internal/config"
	"cordbrief/internal/digest"
	"cordbrief/internal/journal"
)

const (
	testProcessEnvRole     = "CORDBRIEF_TEST_PROCESS_ROLE"
	testProcessEnvData     = "CORDBRIEF_TEST_DATA_DIR"
	testProcessEnvExchange = "CORDBRIEF_TEST_EXCHANGE_DIR"

	testBatchPreparedUncommitted = "1111111111111111111111111111111111111111111111111111111111111111"
	testBatchPreparedCommitted   = "2222222222222222222222222222222222222222222222222222222222222222"
	testBatchTelegramCrash       = "3333333333333333333333333333333333333333333333333333333333333333"
)

// TestRealProcessRestartHelper executes the sub-process roles across distinct OS processes.
func TestRealProcessRestartHelper(t *testing.T) {
	role := os.Getenv(testProcessEnvRole)
	if role == "" {
		return // Not running as a subprocess helper
	}

	dataDir := os.Getenv(testProcessEnvData)
	if dataDir == "" {
		fmt.Fprintln(os.Stderr, "missing CORDBRIEF_TEST_DATA_DIR")
		os.Exit(2)
	}
	exchangeDir := os.Getenv(testProcessEnvExchange)
	if exchangeDir == "" {
		fmt.Fprintln(os.Stderr, "missing CORDBRIEF_TEST_EXCHANGE_DIR")
		os.Exit(2)
	}

	switch role {
	// =========================================================================
	// Scenario 1: PREPARED-before-cursor must remain unsent after fresh process startup
	// =========================================================================
	case "PREPARED_UNCOMMITTED_A":
		// Process A creates artifact with CursorEnd (1, 500) and PREPARED delivery record,
		// but cursor file on disk is only at (1, 100) (uncommitted).
		digestsDir := filepath.Join(dataDir, "digests")
		_ = os.MkdirAll(digestsDir, 0755)
		art := &digest.Artifact{
			Version:   journal.CurrentSchemaVersion,
			BatchID:   testBatchPreparedUncommitted,
			CreatedAt: time.Now().UTC(),
			CursorEnd: journal.Cursor{Version: 1, Segment: 1, Offset: 500},
			DeliveryRequest: &digest.DeliveryRequest{
				Provider:  "telegram",
				ChatID:    "123456",
				ChatLabel: "Test Channel",
			},
			Digest: &digest.Digest{Title: "Uncommitted Digest"},
		}
		if err := digest.SaveArtifact(digestsDir, art); err != nil {
			fmt.Fprintf(os.Stderr, "SaveArtifact failed: %v\n", err)
			os.Exit(3)
		}

		store, _ := config.NewStore(dataDir, "")
		_ = store.SaveDeliveryConfig(config.DeliveryConfig{
			Telegram: config.TelegramConfig{Enabled: true, ChatID: "123456"},
		})
		_ = store.SaveTelegramBotToken("fake-token")

		svc, err := NewService(ServiceOptions{
			DataDir:     dataDir,
			ExchangeDir: exchangeDir,
			Store:       store,
			ClientGetter: func(token string) *TelegramClient {
				return nil
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "NewService failed: %v\n", err)
			os.Exit(4)
		}

		// Save PREPARED record targeting (1, 500)
		targetCur := journal.Cursor{Version: 1, Segment: 1, Offset: 500}
		if _, err := svc.PrepareDeliveryIntent(testBatchPreparedUncommitted, targetCur, art.DeliveryRequest); err != nil {
			fmt.Fprintf(os.Stderr, "PrepareDeliveryIntent failed: %v\n", err)
			os.Exit(5)
		}

		// Cursor on disk is only at offset 100 (has NOT reached 500)
		ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)
		if err := journal.SaveCursor(ackPath, &journal.Cursor{Version: 1, Segment: 1, Offset: 100}); err != nil {
			fmt.Fprintf(os.Stderr, "SaveCursor failed: %v\n", err)
			os.Exit(6)
		}

		os.Exit(0)

	case "PREPARED_UNCOMMITTED_B":
		// Process B (fresh OS process) runs ScanAndResume and verifies the record
		// remains StatePrepared and is NOT sent or enqueued.
		store, _ := config.NewStore(dataDir, "")
		var networkAttempted bool
		svc, err := NewService(ServiceOptions{
			DataDir:     dataDir,
			ExchangeDir: exchangeDir,
			Store:       store,
			ClientGetter: func(token string) *TelegramClient {
				networkAttempted = true
				return nil
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Process B NewService failed: %v\n", err)
			os.Exit(7)
		}

		svc.ScanAndResume(context.Background())

		rec, err := svc.GetDelivery(testBatchPreparedUncommitted)
		if err != nil || rec == nil {
			fmt.Fprintf(os.Stderr, "GetDelivery failed: %v\n", err)
			os.Exit(8)
		}
		if rec.State != StatePrepared {
			fmt.Fprintf(os.Stderr, "expected StatePrepared, got %s\n", rec.State)
			os.Exit(9)
		}

		select {
		case b := <-svc.queue:
			fmt.Fprintf(os.Stderr, "unexpected enqueued batch: %s\n", b)
			os.Exit(10)
		default:
			// Queue empty as required
		}

		// Unforced delivery attempt must be refused
		_, err = svc.DeliverBatch(context.Background(), testBatchPreparedUncommitted, false)
		if err == nil || !strings.Contains(err.Error(), "transaction is not yet committed") {
			fmt.Fprintf(os.Stderr, "expected uncommitted error, got: %v\n", err)
			os.Exit(11)
		}

		if networkAttempted {
			fmt.Fprintf(os.Stderr, "forbidden network attempt made\n")
			os.Exit(12)
		}
		os.Exit(0)

	// =========================================================================
	// Scenario 2: cursor-committed PREPARED must promote after fresh process startup
	// =========================================================================
	case "PREPARED_COMMITTED_A":
		// Process A creates artifact with CursorEnd (1, 500) and PREPARED delivery record,
		// and commits cursor on disk to (1, 500). Then exits before promotion to PENDING.
		digestsDir := filepath.Join(dataDir, "digests")
		_ = os.MkdirAll(digestsDir, 0755)
		art := &digest.Artifact{
			Version:   journal.CurrentSchemaVersion,
			BatchID:   testBatchPreparedCommitted,
			CreatedAt: time.Now().UTC(),
			CursorEnd: journal.Cursor{Version: 1, Segment: 1, Offset: 500},
			DeliveryRequest: &digest.DeliveryRequest{
				Provider:  "telegram",
				ChatID:    "123456",
				ChatLabel: "Test Channel",
			},
			Digest: &digest.Digest{Title: "Committed Digest"},
		}
		if err := digest.SaveArtifact(digestsDir, art); err != nil {
			fmt.Fprintf(os.Stderr, "SaveArtifact failed: %v\n", err)
			os.Exit(13)
		}

		store, _ := config.NewStore(dataDir, "")
		_ = store.SaveDeliveryConfig(config.DeliveryConfig{
			Telegram: config.TelegramConfig{Enabled: true, ChatID: "123456"},
		})
		_ = store.SaveTelegramBotToken("fake-token")

		svc, err := NewService(ServiceOptions{
			DataDir:     dataDir,
			ExchangeDir: exchangeDir,
			Store:       store,
			ClientGetter: func(token string) *TelegramClient {
				return nil
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "NewService failed: %v\n", err)
			os.Exit(14)
		}

		targetCur := journal.Cursor{Version: 1, Segment: 1, Offset: 500}
		if _, err := svc.PrepareDeliveryIntent(testBatchPreparedCommitted, targetCur, art.DeliveryRequest); err != nil {
			fmt.Fprintf(os.Stderr, "PrepareDeliveryIntent failed: %v\n", err)
			os.Exit(15)
		}

		// Cursor on disk commits to (1, 500)
		ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)
		if err := journal.SaveCursor(ackPath, &targetCur); err != nil {
			fmt.Fprintf(os.Stderr, "SaveCursor failed: %v\n", err)
			os.Exit(16)
		}

		// Process A exits before promoting to PENDING
		os.Exit(0)

	case "PREPARED_COMMITTED_B":
		// Process B (fresh OS process) runs ScanAndResume, verifies that the committed
		// PREPARED record is promoted to StatePending and pushed to the worker queue.
		store, _ := config.NewStore(dataDir, "")
		svc, err := NewService(ServiceOptions{
			DataDir:     dataDir,
			ExchangeDir: exchangeDir,
			Store:       store,
			ClientGetter: func(token string) *TelegramClient {
				return nil
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Process B NewService failed: %v\n", err)
			os.Exit(17)
		}

		svc.ScanAndResume(context.Background())

		rec, err := svc.GetDelivery(testBatchPreparedCommitted)
		if err != nil || rec == nil {
			fmt.Fprintf(os.Stderr, "GetDelivery failed: %v\n", err)
			os.Exit(18)
		}
		if rec.State != StatePending {
			fmt.Fprintf(os.Stderr, "expected StatePending, got %s\n", rec.State)
			os.Exit(19)
		}

		select {
		case b := <-svc.queue:
			if b != testBatchPreparedCommitted {
				fmt.Fprintf(os.Stderr, "unexpected enqueued batch: %s\n", b)
				os.Exit(20)
			}
		default:
			fmt.Fprintf(os.Stderr, "expected batch in worker queue\n")
			os.Exit(21)
		}

		os.Exit(0)

	// =========================================================================
	// Scenario 3: Telegram-success-before-local-persist must become uncertain after fresh process startup
	// =========================================================================
	case "TELEGRAM_SUCCESS_CRASH_A":
		// Process A starts fake Telegram server that accepts request and returns 200 OK.
		// It arms testCrashBeforeSuccessPersist to crash the process (exit 42) immediately
		// upon successful network receipt, BEFORE local disk persistence of the confirmed message ID.
		var telegramReceived atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			telegramReceived.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"message_id": 9999},
			})
		}))
		defer srv.Close()

		digestsDir := filepath.Join(dataDir, "digests")
		_ = os.MkdirAll(digestsDir, 0755)
		art := &digest.Artifact{
			Version:   journal.CurrentSchemaVersion,
			BatchID:   testBatchTelegramCrash,
			CreatedAt: time.Now().UTC(),
			DeliveryRequest: &digest.DeliveryRequest{
				Provider: "telegram",
				ChatID:   "123456",
			},
			Digest: &digest.Digest{Title: "Crash Test Digest"},
		}
		if err := digest.SaveArtifact(digestsDir, art); err != nil {
			fmt.Fprintf(os.Stderr, "SaveArtifact failed: %v\n", err)
			os.Exit(22)
		}

		store, _ := config.NewStore(dataDir, "")
		_ = store.SaveDeliveryConfig(config.DeliveryConfig{
			Telegram: config.TelegramConfig{Enabled: true, ChatID: "123456"},
		})
		_ = store.SaveTelegramBotToken("fake-token")

		svc, err := NewService(ServiceOptions{
			DataDir:     dataDir,
			ExchangeDir: exchangeDir,
			Store:       store,
			ClientGetter: func(token string) *TelegramClient {
				return NewTelegramClient(token, WithBaseURL(srv.URL))
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "NewService failed: %v\n", err)
			os.Exit(23)
		}

		// Arm crash hook right at the persistence boundary
		testHookBeforeSuccessPersist = func() {
			if !telegramReceived.Load() {
				fmt.Fprintln(os.Stderr, "Telegram server did not receive request!")
				os.Exit(24)
			}
			// Simulate process death / crash immediately after Telegram success
			os.Exit(42)
		}

		// DeliverBatch will write InFlightPart=0 to disk, send to Telegram (server returns 200),
		// and then trigger testHookBeforeSuccessPersist -> os.Exit(42) before writing message ID!
		_, _ = svc.DeliverBatch(context.Background(), testBatchTelegramCrash, false)
		os.Exit(25) // Should not reach here

	case "TELEGRAM_SUCCESS_CRASH_B":
		// Process B (fresh OS process) runs ScanAndResume.
		// It must detect an unresolved in-flight attempt, transition to UNCERTAIN,
		// and refuse to automatically call Telegram again.
		var networkCalls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			networkCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"message_id": 10000},
			})
		}))
		defer srv.Close()

		store, _ := config.NewStore(dataDir, "")
		svc, err := NewService(ServiceOptions{
			DataDir:     dataDir,
			ExchangeDir: exchangeDir,
			Store:       store,
			ClientGetter: func(token string) *TelegramClient {
				return NewTelegramClient(token, WithBaseURL(srv.URL))
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Process B NewService failed: %v\n", err)
			os.Exit(26)
		}

		svc.ScanAndResume(context.Background())

		rec, err := svc.GetDelivery(testBatchTelegramCrash)
		if err != nil || rec == nil {
			fmt.Fprintf(os.Stderr, "GetDelivery failed: %v\n", err)
			os.Exit(27)
		}

		if rec.State != StateUncertain {
			fmt.Fprintf(os.Stderr, "expected StateUncertain, got %s\n", rec.State)
			os.Exit(28)
		}
		if rec.InFlightPart != nil {
			fmt.Fprintf(os.Stderr, "expected InFlightPart to be cleared after marking uncertain, got %v\n", *rec.InFlightPart)
			os.Exit(29)
		}

		// Unforced DeliverBatch must be refused
		_, err = svc.DeliverBatch(context.Background(), testBatchTelegramCrash, false)
		if err == nil || !strings.Contains(err.Error(), "operator confirmation required") {
			fmt.Fprintf(os.Stderr, "expected operator confirmation error, got: %v\n", err)
			os.Exit(30)
		}

		// ZERO calls must be made to Telegram
		if networkCalls.Load() != 0 {
			fmt.Fprintf(os.Stderr, "expected 0 Telegram calls, got %d\n", networkCalls.Load())
			os.Exit(31)
		}

		os.Exit(0)

	default:
		fmt.Fprintf(os.Stderr, "unknown role: %s\n", role)
		os.Exit(99)
	}
}

// TestRealProcessRestartProof exercises all three required scenarios across distinct OS process boundaries.
func TestRealProcessRestartProof(t *testing.T) {
	t.Run("PREPARED_Before_Cursor", func(t *testing.T) {
		tmpDir := t.TempDir()
		dataDir := filepath.Join(tmpDir, "data")
		exchangeDir := filepath.Join(tmpDir, "exchange")
		_ = os.MkdirAll(dataDir, 0755)
		_ = os.MkdirAll(exchangeDir, 0755)

		// 1. Run Process A
		cmdA := exec.Command(os.Args[0], "-test.run=^TestRealProcessRestartHelper$")
		cmdA.Env = append(os.Environ(),
			testProcessEnvRole+"=PREPARED_UNCOMMITTED_A",
			testProcessEnvData+"="+dataDir,
			testProcessEnvExchange+"="+exchangeDir,
		)
		outA, errA := cmdA.CombinedOutput()
		if errA != nil {
			t.Fatalf("Process A failed: %v\nOutput: %s", errA, string(outA))
		}

		// 2. Run Process B (fresh process)
		cmdB := exec.Command(os.Args[0], "-test.run=^TestRealProcessRestartHelper$")
		cmdB.Env = append(os.Environ(),
			testProcessEnvRole+"=PREPARED_UNCOMMITTED_B",
			testProcessEnvData+"="+dataDir,
			testProcessEnvExchange+"="+exchangeDir,
		)
		outB, errB := cmdB.CombinedOutput()
		if errB != nil {
			t.Fatalf("Process B failed: %v\nOutput: %s", errB, string(outB))
		}
	})

	t.Run("PREPARED_Cursor_Committed", func(t *testing.T) {
		tmpDir := t.TempDir()
		dataDir := filepath.Join(tmpDir, "data")
		exchangeDir := filepath.Join(tmpDir, "exchange")
		_ = os.MkdirAll(dataDir, 0755)
		_ = os.MkdirAll(exchangeDir, 0755)

		// 1. Run Process A
		cmdA := exec.Command(os.Args[0], "-test.run=^TestRealProcessRestartHelper$")
		cmdA.Env = append(os.Environ(),
			testProcessEnvRole+"=PREPARED_COMMITTED_A",
			testProcessEnvData+"="+dataDir,
			testProcessEnvExchange+"="+exchangeDir,
		)
		outA, errA := cmdA.CombinedOutput()
		if errA != nil {
			t.Fatalf("Process A failed: %v\nOutput: %s", errA, string(outA))
		}

		// 2. Run Process B (fresh process)
		cmdB := exec.Command(os.Args[0], "-test.run=^TestRealProcessRestartHelper$")
		cmdB.Env = append(os.Environ(),
			testProcessEnvRole+"=PREPARED_COMMITTED_B",
			testProcessEnvData+"="+dataDir,
			testProcessEnvExchange+"="+exchangeDir,
		)
		outB, errB := cmdB.CombinedOutput()
		if errB != nil {
			t.Fatalf("Process B failed: %v\nOutput: %s", errB, string(outB))
		}
	})

	t.Run("Telegram_Success_Crash_Before_Persist", func(t *testing.T) {
		tmpDir := t.TempDir()
		dataDir := filepath.Join(tmpDir, "data")
		exchangeDir := filepath.Join(tmpDir, "exchange")
		_ = os.MkdirAll(dataDir, 0755)
		_ = os.MkdirAll(exchangeDir, 0755)

		// 1. Run Process A (must terminate with exit code 42 at the crash hook)
		cmdA := exec.Command(os.Args[0], "-test.run=^TestRealProcessRestartHelper$")
		cmdA.Env = append(os.Environ(),
			testProcessEnvRole+"=TELEGRAM_SUCCESS_CRASH_A",
			testProcessEnvData+"="+dataDir,
			testProcessEnvExchange+"="+exchangeDir,
		)
		outA, errA := cmdA.CombinedOutput()
		if errA == nil {
			t.Fatalf("expected Process A to crash with exit code 42, but succeeded! Output: %s", string(outA))
		}
		exitErr, ok := errA.(*exec.ExitError)
		if !ok || exitErr.ExitCode() != 42 {
			t.Fatalf("expected exit code 42 from Process A, got %v\nOutput: %s", errA, string(outA))
		}

		// Verify on disk that in-flight state is written
		recOnDisk, err := LoadDeliveryRecord(dataDir, testBatchTelegramCrash)
		if err != nil {
			t.Fatalf("failed reading delivery record after Process A crash: %v", err)
		}
		if recOnDisk.State != StateSending || recOnDisk.InFlightPart == nil || *recOnDisk.InFlightPart != 0 {
			t.Fatalf("expected record on disk to have StateSending and InFlightPart: 0, got: %+v", recOnDisk)
		}

		// 2. Run Process B (fresh process)
		cmdB := exec.Command(os.Args[0], "-test.run=^TestRealProcessRestartHelper$")
		cmdB.Env = append(os.Environ(),
			testProcessEnvRole+"=TELEGRAM_SUCCESS_CRASH_B",
			testProcessEnvData+"="+dataDir,
			testProcessEnvExchange+"="+exchangeDir,
		)
		outB, errB := cmdB.CombinedOutput()
		if errB != nil {
			t.Fatalf("Process B failed: %v\nOutput: %s", errB, string(outB))
		}
	})
}
