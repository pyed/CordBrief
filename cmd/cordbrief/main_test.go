package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cordbrief/internal/config"
	"cordbrief/internal/journal"
	"cordbrief/internal/llm"
	"time"
)

func TestRetentionReaderProcessExclusion(t *testing.T) {
	if dir := os.Getenv("CORDBRIEF_TEST_LOCK_CHILD"); dir != "" {
		lock, err := journal.AcquireCommitLock(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Release()
		fmt.Println("READY")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRetentionReaderProcessExclusion$")
	child.Env = append(os.Environ(), "CORDBRIEF_TEST_LOCK_CHILD="+dir)
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var childErrors bytes.Buffer
	child.Stderr = &childErrors
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		input.Close()
		if err := child.Wait(); err != nil {
			t.Errorf("lock child: %v %s", err, childErrors.String())
		}
	}()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "READY" {
		t.Fatalf("child not ready: %q %v", line, err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"exchange", "ingest", "--data-dir=" + dir, "--exchange-dir=" + dir}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "cannot acquire commit lock") {
		t.Fatalf("reader bypassed process exclusion: %d %s", code, stderr.String())
	}
}

func TestCLI_Version(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := Run([]string{"version"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0 for version, got %d", exitCode)
	}

	out := stdout.String()
	if !strings.Contains(out, "cordbrief v2.0.0") {
		t.Errorf("expected version output to contain version string, got: %s", out)
	}
}

func TestCLI_HelpAndUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer

	// Help should exit 0
	code := Run([]string{"help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0 for help, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Commands:") {
		t.Errorf("expected usage info in stdout, got: %s", stdout.String())
	}

	// No args should exit 1 with usage on stderr
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected code 1 for empty args, got %d", code)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("expected usage in stderr, got: %s", stderr.String())
	}

	// Unknown subcommand should exit 1
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"bogus"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected code 1 for unknown command, got %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown command \"bogus\"") {
		t.Errorf("expected unknown command error, got: %s", stderr.String())
	}
}

func TestCLI_Exchange(t *testing.T) {
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 1. Write watchlist via CLI
	var stdout, stderr bytes.Buffer
	code := Run([]string{"exchange", "write-watchlist", "-exchange-dir=" + tmpDir, "-generation=1", "-channels=ch-a,ch-b"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("write-watchlist failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "[PASS] Watchlist written: generation 1, 2 channels") {
		t.Fatalf("unexpected write-watchlist output: %s", stdout.String())
	}

	// 2. Read watchlist via CLI
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"exchange", "read-watchlist", "-exchange-dir=" + tmpDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("read-watchlist failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Watchlist Generation: 1") || !strings.Contains(stdout.String(), "ch-a") {
		t.Fatalf("unexpected read-watchlist output: %s", stdout.String())
	}

	// 3. Populate a test segment file
	seg1 := filepath.Join(eventsDir, "0000000000000001.ndjson")
	eventLine := []byte(`{"version":1,"event":"message_create","message_id":"msg-101","guild_id":"g1","channel_id":"ch-a","timestamp":"2026-09-03T12:00:00Z","captured_at":"2026-09-03T12:00:00Z","author":{"id":"u1","name":"alice","display_name":"Alice","bot":false},"content":"hello"}` + "\n")
	if err := os.WriteFile(seg1, eventLine, 0644); err != nil {
		t.Fatal(err)
	}

	// 4. Ingest without commit (dry-run)
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"exchange", "ingest", "-exchange-dir=" + tmpDir, "-data-dir=" + tmpDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("ingest dry-run failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Events Read: 1") || !strings.Contains(stdout.String(), "Dry-run read: cursor not committed") {
		t.Fatalf("unexpected dry-run output: %s", stdout.String())
	}

	// Cursor should still be default because it was not committed
	ackFile := filepath.Join(tmpDir, "core-ack.json")
	if _, err := os.Stat(ackFile); !os.IsNotExist(err) {
		t.Fatal("expected core-ack.json to NOT exist after dry run")
	}

	// 5. Ingest with commit
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"exchange", "ingest", "-exchange-dir=" + tmpDir, "-data-dir=" + tmpDir, "-commit"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("ingest with commit failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Events Read: 1") || !strings.Contains(stdout.String(), "[PASS] Committed new cursor to core-ack.json") {
		t.Fatalf("unexpected committed ingest output: %s", stdout.String())
	}

	// 6. Ingest again immediately (should return 0 events)
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"exchange", "ingest", "-exchange-dir=" + tmpDir, "-data-dir=" + tmpDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("second ingest failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Events Read: 0") {
		t.Fatalf("expected 0 events on second ingest, got: %s", stdout.String())
	}
}

func TestCLI_Digest(t *testing.T) {
	fakeServer := llm.NewFakeLLMServer()
	defer fakeServer.Close()

	fakeServer.ResponseContent = `{"title":"Test Digest Title","overview":"Test digest overview paragraph","items":[{"kind":"finding","text":"Observed drawdown difference","source_ids":["S000001"]}]}`

	t.Setenv("CORDBRIEF_LLM_BASE_URL", fakeServer.URL)
	t.Setenv("CORDBRIEF_LLM_MODEL", "mock-model")
	t.Setenv("GEMINI_API_KEY", "test-api-key")

	tmpDir := t.TempDir()
	exchangeDir := filepath.Join(tmpDir, "exchange")
	eventsDir := filepath.Join(exchangeDir, "events")
	dataDir := filepath.Join(tmpDir, "data")
	_ = os.MkdirAll(eventsDir, 0755)
	_ = os.MkdirAll(dataDir, 0755)

	// Write 1 message to segment
	seg1 := filepath.Join(eventsDir, "0000000000000001.ndjson")
	eventLine := []byte(`{"version":1,"event":"message_create","message_id":"15451001","guild_id":"g1","channel_id":"ch-a","timestamp":"2026-09-03T12:00:00Z","captured_at":"2026-09-03T12:00:00Z","author":{"id":"u1","name":"alice","display_name":"Alice","bot":false},"content":"Test grinder drawdown"}` + "\n")
	if err := os.WriteFile(seg1, eventLine, 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Preview
	var stdout, stderr bytes.Buffer
	t.Chdir(t.TempDir())
	code := Run([]string{"digest", "preview", "-exchange-dir=" + exchangeDir, "-data-dir=" + dataDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("digest preview failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "[PREVIEW] Cursor untouched") || !strings.Contains(stdout.String(), "# CordBrief — Test Digest Title") {
		t.Fatalf("unexpected preview output: %s", stdout.String())
	}

	// Verify cursor was NOT advanced
	ackFile := filepath.Join(exchangeDir, "core-ack.json")
	if _, err := os.Stat(ackFile); !os.IsNotExist(err) {
		t.Fatal("expected core-ack.json to NOT exist after preview")
	}

	// 2. Run (with commit)
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"digest", "run", "-config=", "-exchange-dir=" + exchangeDir, "-data-dir=" + dataDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("digest run failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Committed Cursor:") || !strings.Contains(stdout.String(), "Artifact:") {
		t.Fatalf("unexpected run output: %s", stdout.String())
	}

	// Verify core-ack.json was created
	if _, err := os.Stat(ackFile); err != nil {
		t.Fatalf("expected core-ack.json to exist after run, got: %v", err)
	}

	// 3. Run again immediately (should report no new events)
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"digest", "run", "-config=", "-exchange-dir=" + exchangeDir, "-data-dir=" + dataDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("second digest run failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "[INFO] No new journal events to process.") {
		t.Fatalf("expected no new events output, got: %s", stdout.String())
	}
}

func TestCLI_ServeUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("help failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "serve       Run the Web inbox and scheduler") {
		t.Errorf("usage missing serve command: %s", stdout.String())
	}
}

func TestServeDataDirectoryDoesNotExposeUI(t *testing.T) {
	t.Setenv("CORDBRIEF_DATA_DIR", t.TempDir())
	t.Setenv("CORDBRIEF_EXCHANGE_DIR", t.TempDir())
	t.Setenv("CORDBRIEF_WEB_ADDR", "")
	t.Setenv("CORDBRIEF_HTTP_ADDR", "")
	var stdout, stderr bytes.Buffer
	Run([]string{"serve", "-h"}, &stdout, &stderr)
	if !strings.Contains(stderr.String(), `HTTP listen address (default "127.0.0.1")`) {
		t.Fatalf("custom directories changed listen address: %s", stderr.String())
	}
}

func TestDigestRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("config.json", []byte(`{"broken":`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"digest", "preview"}, {"digest", "preview", "--config=missing.json"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "error loading config") {
			t.Fatalf("invalid config was ignored: %d %s", code, stderr.String())
		}
	}
}

func TestRemovedBotCommands(t *testing.T) {
	for _, command := range []string{"channels", "doctor", "run"} {
		var stdout, stderr bytes.Buffer
		if code := Run([]string{command}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "unknown command") {
			t.Fatalf("obsolete command %q remains available: %d %s", command, code, stderr.String())
		}
	}
}

func TestPortHardeningDefaults(t *testing.T) {
	t.Setenv("CORDBRIEF_WEB_PORT", "")
	t.Setenv("CORDBRIEF_HTTP_PORT", "")
	t.Setenv("CORDBRIEF_WEB_ADDR", "")
	t.Setenv("CORDBRIEF_HTTP_ADDR", "")
	t.Setenv("CORDBRIEF_DATA_DIR", "")
	t.Setenv("CORDBRIEF_EXCHANGE_DIR", "")

	if config.DefaultCorePort != 28741 {
		t.Fatalf("expected DefaultCorePort 28741, got %d", config.DefaultCorePort)
	}
	if config.DefaultSetupPort != 28742 {
		t.Fatalf("expected DefaultSetupPort 28742, got %d", config.DefaultSetupPort)
	}
}

func TestDigestDeliveryTriggerPolicy(t *testing.T) {
	// Mock Telegram API Server
	var sendCount int
	mockTG := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "sendMessage") {
			sendCount++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"ok": true, "result": {"message_id": %d}}`, 700+sendCount)))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer mockTG.Close()

	t.Setenv("CORDBRIEF_TELEGRAM_API_BASE", mockTG.URL)
	t.Setenv("TELEGRAM_BOT_TOKEN", "cli-test-token")

	fakeServer := llm.NewFakeLLMServer()
	defer fakeServer.Close()
	fakeServer.ResponseContent = `{"title":"CLI Delivery Test","overview":"CLI delivery overview","items":[{"kind":"finding","text":"Tested delivery CLI","source_ids":["S000001"]}]}`

	t.Setenv("CORDBRIEF_LLM_BASE_URL", fakeServer.URL)
	t.Setenv("CORDBRIEF_LLM_MODEL", "mock-model")
	t.Setenv("GEMINI_API_KEY", "test-api-key")

	tmpDir := t.TempDir()
	exchangeDir := filepath.Join(tmpDir, "exchange")
	eventsDir := filepath.Join(exchangeDir, "events")
	dataDir := filepath.Join(tmpDir, "data")
	_ = os.MkdirAll(eventsDir, 0755)
	_ = os.MkdirAll(dataDir, 0755)

	store, err := config.NewStore(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	delCfg := store.GetDeliveryConfig()
	delCfg.Telegram.Enabled = true
	delCfg.Telegram.ChatID = "-100123456789"
	_ = store.SaveDeliveryConfig(delCfg)

	// Write 1 message to segment
	seg1 := filepath.Join(eventsDir, "0000000000000001.ndjson")
	eventLine := []byte(`{"version":1,"event":"message_create","message_id":"15451001","guild_id":"g1","channel_id":"ch-a","timestamp":"2026-09-03T12:00:00Z","captured_at":"2026-09-03T12:00:00Z","author":{"id":"u1","name":"alice","display_name":"Alice","bot":false},"content":"Testing CLI delivery policy"}` + "\n")
	if err := os.WriteFile(seg1, eventLine, 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Preview: must NOT deliver even if Telegram configured
	var stdout, stderr bytes.Buffer
	code := Run([]string{"digest", "preview", "-config=", "-exchange-dir=" + exchangeDir, "-data-dir=" + dataDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("preview failed: %s", stderr.String())
	}
	if sendCount != 0 {
		t.Fatalf("SECURITY VIOLATION: digest preview triggered delivery (sendCount=%d)", sendCount)
	}

	// 2. Run WITHOUT --deliver flag: must NOT deliver
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"digest", "run", "-config=", "-exchange-dir=" + exchangeDir, "-data-dir=" + dataDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run failed: %s", stderr.String())
	}
	if sendCount != 0 {
		t.Fatalf("POLICY VIOLATION: digest run without --deliver triggered delivery (sendCount=%d)", sendCount)
	}

	// Verify cursor was committed
	ackFile := filepath.Join(exchangeDir, "core-ack.json")
	if _, err := os.Stat(ackFile); err != nil {
		t.Fatalf("expected core-ack.json to exist after run: %v", err)
	}

	// Write second event for deliver run
	eventLine2 := []byte(`{"version":1,"event":"message_create","message_id":"15451002","guild_id":"g1","channel_id":"ch-a","timestamp":"2026-09-03T12:01:00Z","captured_at":"2026-09-03T12:01:00Z","author":{"id":"u2","name":"bob","display_name":"Bob","bot":false},"content":"Testing CLI with deliver flag"}` + "\n")
	f, err := os.OpenFile(seg1, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(eventLine2)
	_ = f.Close()

	// 3. Run WITH --deliver flag: MUST deliver post-commit
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"digest", "run", "--deliver", "-config=", "-exchange-dir=" + exchangeDir, "-data-dir=" + dataDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run with --deliver failed: %s", stderr.String())
	}
	if sendCount != 1 {
		t.Fatalf("expected exactly 1 Telegram delivery when --deliver passed, got %d", sendCount)
	}
	if !strings.Contains(stdout.String(), "[PASS] Delivered to Telegram") {
		t.Errorf("expected '[PASS] Delivered to Telegram' in stdout, got: %s", stdout.String())
	}
}

func TestWebRequestLimitsAndTimeouts(t *testing.T) {
	srv := NewHTTPServer("127.0.0.1:28741", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("expected ReadHeaderTimeout 10s, got %v", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 30*time.Second {
		t.Errorf("expected ReadTimeout 30s, got %v", srv.ReadTimeout)
	}
	if srv.WriteTimeout < 240*time.Second {
		t.Errorf("expected WriteTimeout >= 240s, got %v", srv.WriteTimeout)
	}
	if srv.WriteTimeout <= MaxProviderTimeout {
		t.Errorf("expected WriteTimeout (%v) to exceed MaxProviderTimeout (%v)", srv.WriteTimeout, MaxProviderTimeout)
	}
	if srv.WriteTimeout != DefaultHTTPServerWriteTimeout {
		t.Errorf("expected WriteTimeout %v, got %v", DefaultHTTPServerWriteTimeout, srv.WriteTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Errorf("expected IdleTimeout 120s, got %v", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 1<<20 {
		t.Errorf("expected MaxHeaderBytes 1MB, got %d", srv.MaxHeaderBytes)
	}
}

func TestCommitLock(t *testing.T) {
	tmpDir := t.TempDir()
	exchangeDir := filepath.Join(tmpDir, "exchange")
	dataDir := filepath.Join(tmpDir, "data")
	eventsDir := filepath.Join(exchangeDir, "events")
	_ = os.MkdirAll(eventsDir, 0755)
	_ = os.MkdirAll(dataDir, 0755)

	// 1. Basic acquire & conflict on dataDir
	l1, err := journal.AcquireCommitLock(dataDir)
	if err != nil {
		t.Fatalf("failed acquiring commit lock: %v", err)
	}

	_, err2 := journal.AcquireCommitLock(dataDir)
	if err2 == nil {
		t.Fatal("expected ErrCommitLockActive on second acquire, got nil")
	}

	defer l1.Release()
	// Journal readers must exclude offline retention maintenance too.
	fake := llm.NewFakeLLMServer()
	defer fake.Close()
	fake.ResponseContent = `{"title":"Test Digest","summary":"Summary","items":[]}`

	t.Setenv(config.EnvGeminiKey, "mock-key")
	t.Setenv("CORDBRIEF_LLM_PROVIDER", "gemini")
	t.Setenv("CORDBRIEF_LLM_BASE_URL", fake.URL)
	t.Setenv("CORDBRIEF_LLM_MODEL", "gemini-3.7-flash")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"digest", "preview", "-config=", "-exchange-dir=" + exchangeDir, "-data-dir=" + dataDir}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "cannot acquire commit lock") {
		t.Fatalf("expected preview to refuse while lock is held, got code %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"exchange", "ingest", "-exchange-dir=" + exchangeDir, "-data-dir=" + dataDir}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "cannot acquire commit lock") {
		t.Fatalf("unlocked dry-run ingest: %d %s", code, stderr.String())
	}

	// 3. While lock is held, digest run fails with lock error
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"digest", "run", "-config=", "-exchange-dir=" + exchangeDir, "-data-dir=" + dataDir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected run to fail when commit lock held, got %d", code)
	}
	if !strings.Contains(stderr.String(), "cannot run digest: cannot acquire commit lock") {
		t.Errorf("expected commit lock held error, got: %s", stderr.String())
	}

	// 4. Release lock -> subsequent acquire succeeds
	l1.Release()

	l3, err3 := journal.AcquireCommitLock(dataDir)
	if err3 != nil {
		t.Fatalf("expected acquire after release to succeed, got %v", err3)
	}
	l3.Release()
}

func TestMigrateCommitLock(t *testing.T) {
	tmpDir := t.TempDir()
	exchangeDir := filepath.Join(tmpDir, "exchange")
	eventsDir := filepath.Join(exchangeDir, "events")
	dataDir := filepath.Join(tmpDir, "data")
	digestsDir := filepath.Join(dataDir, "digests")
	_ = os.MkdirAll(eventsDir, 0755)
	_ = os.MkdirAll(digestsDir, 0755)

	// Create test journal event
	segPath := filepath.Join(eventsDir, "0000000000000001.ndjson")
	eventLine := `{"version":1,"event":"message_create","message_id":"1545224975760494701","channel_id":"1545115236619518014","guild_id":"1545114461868658862","timestamp":"2026-09-06T10:00:00Z","author":{"id":"u1","name":"alice"},"content":"test content"}` + "\n"
	if err := os.WriteFile(segPath, []byte(eventLine), 0644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(segPath)

	// Create unmigrated test artifact
	batchID := "4444444444444444444444444444444444444444444444444444444444444444"
	artPath := filepath.Join(digestsDir, batchID+".json")
	artJSON := fmt.Sprintf(`{
		"version": 1,
		"batch_id": "%s",
		"created_at": "2026-09-06T10:00:00Z",
		"cursor_start": {"version": 1, "segment": 1, "offset": 0},
		"cursor_end": {"version": 1, "segment": 1, "offset": %d},
		"input_message_count": 1,
		"included_message_count": 1,
		"provider": "gemini",
		"model": "gemini-3.7-flash",
		"digest": {
			"title": "Test Title",
			"items": [{"kind": "finding", "text": "Test Item", "source_ids": ["S000001"]}]
		}
	}`, batchID, fi.Size())
	if err := os.WriteFile(artPath, []byte(artJSON), 0644); err != nil {
		t.Fatal(err)
	}

	initialHash := func() [32]byte {
		b, err := os.ReadFile(artPath)
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(b)
	}
	baselineHash := initialHash()

	// A & B: Acquire commit lock (simulating running cordbrief serve)
	holderLock, err := journal.AcquireCommitLock(dataDir)
	if err != nil {
		t.Fatalf("failed acquiring commit lock: %v", err)
	}

	defer holderLock.Release()
	// Test A: dry-run migration reads transcripts and must be excluded too.
	var stdoutA, stderrA bytes.Buffer
	codeA := Run([]string{"migrate", "--dry-run", "-exchange-dir=" + exchangeDir, "-data-dir=" + dataDir}, &stdoutA, &stderrA)
	if codeA != 1 || !strings.Contains(stderrA.String(), "acquisition refused") {
		t.Fatalf("Test A failed: expected dry-run exclusion, got %d. stderr: %s", codeA, stderrA.String())
	}
	if initialHash() != baselineHash {
		t.Fatal("Test A: dry-run modified the artifact file!")
	}

	// Test B: serve/holder owns commit lock -> mutating migrate is REFUSED immediately with zero file modifications
	var stdoutB, stderrB bytes.Buffer
	codeB := Run([]string{"migrate", "-exchange-dir=" + exchangeDir, "-data-dir=" + dataDir}, &stdoutB, &stderrB)
	if codeB != 1 {
		t.Fatalf("Test B failed: expected mutating migrate to be refused with exit code 1, got %d", codeB)
	}
	if !strings.Contains(stderrB.String(), "migrate: acquisition refused:") {
		t.Fatalf("Test B: expected acquisition refused error, got: %s", stderrB.String())
	}
	if initialHash() != baselineHash {
		t.Fatal("Test B: refused migrate unexpectedly modified the artifact file!")
	}

	// Test C: lock free -> mutating migrate SUCCEEDS
	holderLock.Release()

	var stdoutC, stderrC bytes.Buffer
	codeC := Run([]string{"migrate", "-exchange-dir=" + exchangeDir, "-data-dir=" + dataDir}, &stdoutC, &stderrC)
	if codeC != 0 {
		t.Fatalf("Test C failed: expected migrate to succeed when lock free, got %d. stderr: %s", codeC, stderrC.String())
	}
	if !strings.Contains(stdoutC.String(), "=== LIVE MIGRATION COMPLETE") {
		t.Fatalf("Test C unexpected stdout: %s", stdoutC.String())
	}
	// Verify artifact was mutated with durable source_refs
	migratedBytes, err := os.ReadFile(artPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(migratedBytes), `"source_refs"`) {
		t.Fatal("Test C: expected migrated artifact to contain source_refs")
	}

	// Test D: kernel lock release verification (lock can be re-acquired cleanly after release)
	checkLock, err := journal.AcquireCommitLock(dataDir)
	if err != nil {
		t.Fatalf("Test D failed: lock was not released after migration: %v", err)
	}
	checkLock.Release()
}
