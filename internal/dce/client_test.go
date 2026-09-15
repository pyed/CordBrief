package dce

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyed/CordBrief/internal/state"
)

func TestCappedBufferConsumesEntireWrite(t *testing.T) {
	var buf cappedBuffer
	buf.limit = 10
	data := []byte(strings.Repeat("log line\n", 20))
	// bytes.Reader uses WriteTo, which rejects short writes with no error.
	if n, err := io.Copy(&buf, bytes.NewReader(data)); err != nil || n != int64(len(data)) {
		t.Fatalf("copy stopped at %d bytes: %v", n, err)
	}
	if n, err := buf.Write(data); n != len(data) || err != nil {
		t.Fatalf("full buffer rejected write: %d, %v", n, err)
	}
	if buf.String() != string(data[:buf.limit]) {
		t.Fatalf("unexpected captured log: %q", buf.String())
	}
}

func TestCommandEnvironmentExcludesUnrelatedCredentials(t *testing.T) {
	for _, key := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_OWNER_ID", "LLM_API_KEY", "DISCORD_TOKEN"} {
		t.Setenv(key, "credential-sentinel")
	}
	t.Setenv("CORDBRIEF_TEST_KEEP", "keep-this")
	env := strings.Join(commandEnv(), "\n")
	if strings.Contains(env, "credential-sentinel") || !strings.Contains(env, "CORDBRIEF_TEST_KEEP=keep-this") {
		t.Fatal("child environment filtering failed")
	}
}

// sampleValidJSON provides a realistic fixture matching observed DCE 2.48 output.
const sampleValidJSON = `{
  "guild": {
    "id": "1391911978150264944",
    "name": "LocalLLM"
  },
  "channel": {
    "id": "1391912303376728155",
    "type": "GuildTextChat",
    "category": "Text Channels",
    "name": "general"
  },
  "dateRange": {
    "after": "2026-09-14T12:00:00+00:00",
    "before": "2026-09-14T13:00:00+00:00"
  },
  "messages": [
    {
      "id": "1549028055563903046",
      "timestamp": "2026-09-14T12:04:27.996Z",
      "content": "Hello world from test",
      "author": {
        "id": "823901147123154975",
        "name": "tester1",
        "nickname": "TestUser",
        "isBot": false
      },
      "reference": {
        "messageId": "1549026953594413099",
        "channelId": "1391912303376728155",
        "guildId": "1391911978150264944"
      },
      "attachments": [
        {
          "id": "1549028055140270220",
          "url": "https://cdn.discord.example/test.png",
          "fileName": "test.png",
          "fileSizeBytes": 1024
        }
      ],
      "embeds": [
        {
          "title": "Example Link",
          "url": "https://example.com",
          "description": "An example website"
        }
      ],
      "unknownFutureDCEField": "should be ignored"
    }
  ],
  "messageCount": 1
}`

// 1. timestamp cursor produces correct verified --after argument
func TestArgv_TimestampCursor(t *testing.T) {
	var capturedArgs []string
	runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
		capturedArgs = args
		outPath := getArgValue(args, "-o")
		return os.WriteFile(outPath, []byte(`{"guild":{"id":"1"},"channel":{"id":"1"},"messages":[]}`), 0600)
	}

	client := NewMockClient("dce.exe", "fake-token", runner)
	req := ExportRequest{
		ChannelID: "1391912303376728155",
		After: state.Cursor{
			Kind:  state.CursorKindTimestamp,
			Value: "2026-09-14T12:00:00Z",
		},
	}

	_, err := client.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected export error: %v", err)
	}

	afterVal := getArgValue(capturedArgs, "--after")
	if afterVal != "2026-09-14T12:00:00Z" {
		t.Fatalf("expected --after 2026-09-14T12:00:00Z, got %q", afterVal)
	}
}

// 2. message_id cursor produces exact decimal --after argument
func TestArgv_MessageIDCursor(t *testing.T) {
	var capturedArgs []string
	runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
		capturedArgs = args
		outPath := getArgValue(args, "-o")
		return os.WriteFile(outPath, []byte(`{"guild":{"id":"1"},"channel":{"id":"1"},"messages":[]}`), 0600)
	}

	client := NewMockClient("dce.exe", "fake-token", runner)
	req := ExportRequest{
		ChannelID: "1391912303376728155",
		After: state.Cursor{
			Kind:  state.CursorKindMessageID,
			Value: "1549028055563903046",
		},
	}

	_, err := client.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected export error: %v", err)
	}

	afterVal := getArgValue(capturedArgs, "--after")
	if afterVal != "1549028055563903046" {
		t.Fatalf("expected --after 1549028055563903046, got %q", afterVal)
	}
}

// 3. fixed cutoff produces correct --before
func TestArgv_BeforeCutoff(t *testing.T) {
	var capturedArgs []string
	runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
		capturedArgs = args
		outPath := getArgValue(args, "-o")
		return os.WriteFile(outPath, []byte(`{"guild":{"id":"1"},"channel":{"id":"1"},"messages":[]}`), 0600)
	}

	client := NewMockClient("dce.exe", "fake-token", runner)
	cutoff := time.Date(2026, 9, 14, 13, 0, 0, 0, time.UTC)
	req := ExportRequest{
		ChannelID: "1391912303376728155",
		Before:    cutoff,
	}

	_, err := client.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected export error: %v", err)
	}

	beforeVal := getArgValue(capturedArgs, "--before")
	if beforeVal != "2026-09-14T13:00:00Z" {
		t.Fatalf("expected --before 2026-09-14T13:00:00Z, got %q", beforeVal)
	}
}

// 4. JSON format is explicitly requested (-f Json)
func TestArgv_FormatJson(t *testing.T) {
	var capturedArgs []string
	runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
		capturedArgs = args
		outPath := getArgValue(args, "-o")
		return os.WriteFile(outPath, []byte(`{"guild":{"id":"1"},"channel":{"id":"1"},"messages":[]}`), 0600)
	}

	client := NewMockClient("dce.exe", "fake-token", runner)
	req := ExportRequest{
		ChannelID: "1391912303376728155",
	}

	_, err := client.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected export error: %v", err)
	}

	formatVal := getArgValue(capturedArgs, "-f")
	if formatVal != "Json" {
		t.Fatalf("expected -f Json, got %q", formatVal)
	}
}

// 5. channel ID passed exactly
func TestArgv_ChannelIDExact(t *testing.T) {
	var capturedArgs []string
	runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
		capturedArgs = args
		outPath := getArgValue(args, "-o")
		return os.WriteFile(outPath, []byte(`{"guild":{"id":"1"},"channel":{"id":"1"},"messages":[]}`), 0600)
	}

	client := NewMockClient("dce.exe", "fake-token", runner)
	req := ExportRequest{
		ChannelID: "987654321098765432",
	}

	_, err := client.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected export error: %v", err)
	}

	chanVal := getArgValue(capturedArgs, "-c")
	if chanVal != "987654321098765432" {
		t.Fatalf("expected -c 987654321098765432, got %q", chanVal)
	}
}

// 6. output path passed exactly
func TestArgv_OutputPathExact(t *testing.T) {
	var capturedArgs []string
	runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
		capturedArgs = args
		outPath := getArgValue(args, "-o")
		return os.WriteFile(outPath, []byte(`{"guild":{"id":"1"},"channel":{"id":"1"},"messages":[]}`), 0600)
	}

	client := NewMockClient("dce.exe", "fake-token", runner)
	customDir := t.TempDir()
	req := ExportRequest{
		ChannelID: "1391912303376728155",
		OutputDir: customDir,
	}

	_, err := client.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected export error: %v", err)
	}

	outPath := getArgValue(capturedArgs, "-o")
	if !strings.HasPrefix(outPath, customDir) {
		t.Fatalf("expected -o path in %s, got %s", customDir, outPath)
	}
}

// 7. DISCORD_TOKEN not present in argv
func TestSecurity_TokenNotInArgv(t *testing.T) {
	const secretToken = "super-secret-discord-token-12345"
	var capturedArgs []string
	runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
		capturedArgs = args
		outPath := getArgValue(args, "-o")
		return os.WriteFile(outPath, []byte(`{"guild":{"id":"1"},"channel":{"id":"1"},"messages":[]}`), 0600)
	}

	client := NewMockClient("dce.exe", secretToken, runner)
	req := ExportRequest{ChannelID: "1391912303376728155"}

	_, err := client.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected export error: %v", err)
	}

	for _, arg := range capturedArgs {
		if strings.Contains(arg, secretToken) {
			t.Fatalf("security violation: secret token leaked in argv element: %q", arg)
		}
		if arg == "-t" || arg == "--token" {
			t.Fatalf("security violation: token flag passed in argv")
		}
	}
}

// 8. child receives DISCORD_TOKEN through environment
func TestSecurity_ChildReceivesTokenInEnv(t *testing.T) {
	const secretToken = "super-secret-discord-token-98765"
	var capturedEnv []string
	runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
		capturedEnv = env
		outPath := getArgValue(args, "-o")
		return os.WriteFile(outPath, []byte(`{"guild":{"id":"1"},"channel":{"id":"1"},"messages":[]}`), 0600)
	}

	client := NewMockClient("dce.exe", secretToken, runner)
	req := ExportRequest{ChannelID: "1391912303376728155"}

	_, err := client.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected export error: %v", err)
	}

	found := false
	for _, e := range capturedEnv {
		if e == "DISCORD_TOKEN="+secretToken {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected DISCORD_TOKEN=%s in child environment", secretToken)
	}
}

// 9. missing token returns safe error
func TestError_MissingToken(t *testing.T) {
	client := NewMockClient("dce.exe", "", nil)
	req := ExportRequest{ChannelID: "1391912303376728155"}

	_, err := client.Export(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for missing token, got nil")
	}
	if !strings.Contains(err.Error(), "DISCORD_TOKEN is required") {
		t.Fatalf("expected missing token error message, got: %v", err)
	}
}

// 10. missing DCE executable returns useful safe error
func TestError_MissingDCEExecutable(t *testing.T) {
	nonExistentPath := filepath.Join(t.TempDir(), "non-existent-dce.exe")
	_, err := NewClient(nonExistentPath, "valid-token")
	if err == nil {
		t.Fatal("expected error for non-existent executable, got nil")
	}
	if !strings.Contains(err.Error(), "DCE executable not found") {
		t.Fatalf("expected 'DCE executable not found', got: %v", err)
	}
}

// 11. nonzero DCE exit becomes error
func TestError_NonzeroExitCode(t *testing.T) {
	runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
		_, _ = io.WriteString(stderr, "Request to 'channels/123' failed: not found")
		return errors.New("exit status 1")
	}

	client := NewMockClient("dce.exe", "fake-token", runner)
	req := ExportRequest{ChannelID: "1391912303376728155"}

	_, err := client.Export(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nonzero exit code, got nil")
	}
	if !strings.Contains(err.Error(), "dce export failed") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected wrapped dce error with diagnostic, got: %v", err)
	}
}

// 12. context cancellation terminates/waits for child correctly
func TestCancellation_ContextCancelled(t *testing.T) {
	runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}

	client := NewMockClient("dce.exe", "fake-token", runner)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	req := ExportRequest{ChannelID: "1391912303376728155"}
	_, err := client.Export(ctx, req)
	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("expected context canceled error, got: %v", err)
	}
}

// 13. valid real-shaped fixture JSON parses correctly
func TestParser_ValidFixture(t *testing.T) {
	res, err := parseDCEExport([]byte(sampleValidJSON))
	if err != nil {
		t.Fatalf("failed to parse valid fixture: %v", err)
	}

	if res.Guild.ID != "1391911978150264944" || res.Guild.Name != "LocalLLM" {
		t.Fatalf("unexpected guild: %+v", res.Guild)
	}
	if res.Channel.ID != "1391912303376728155" || res.Channel.Name != "general" {
		t.Fatalf("unexpected channel: %+v", res.Channel)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(res.Messages))
	}
}

// 14. unknown extra DCE JSON fields do not break parsing
func TestParser_UnknownFieldsForwardCompatibility(t *testing.T) {
	jsonWithExtra := `{
		"guild": {"id": "1", "name": "G", "newField": 123},
		"channel": {"id": "2", "name": "C", "extraObj": {"foo": "bar"}},
		"unknownTop": true,
		"messages": [
			{
				"id": "100",
				"timestamp": "2026-09-14T12:00:00Z",
				"content": "msg",
				"author": {"id": "5", "name": "u", "unknownAuthorProp": "yes"},
				"unknownNested": [1, 2, 3]
			}
		]
	}`

	res, err := parseDCEExport([]byte(jsonWithExtra))
	if err != nil {
		t.Fatalf("expected forward compatibility for unknown fields, got error: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].ID != "100" {
		t.Fatalf("unexpected message parsed: %+v", res.Messages)
	}
}

// 15. malformed JSON rejected
func TestParser_MalformedJSON(t *testing.T) {
	_, err := parseDCEExport([]byte(`{not valid json`))
	if err == nil {
		t.Fatal("expected malformed JSON error, got nil")
	}
}

// 16. empty message array succeeds
func TestParser_EmptyMessageArray(t *testing.T) {
	emptyJSON := `{
		"guild": {"id": "1", "name": "G"},
		"channel": {"id": "2", "name": "C"},
		"messages": [],
		"messageCount": 0
	}`

	res, err := parseDCEExport([]byte(emptyJSON))
	if err != nil {
		t.Fatalf("unexpected error for empty messages array: %v", err)
	}
	if len(res.Messages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(res.Messages))
	}
	if res.MaxMessageID != "" {
		t.Fatalf("expected empty MaxMessageID for empty export, got %q", res.MaxMessageID)
	}
}

// 17. message IDs > signed 64-bit survive unchanged
func TestParser_LargeSnowflakePreservation(t *testing.T) {
	// A snowflake number larger than signed int64 max (9223372036854775807)
	largeID := "9999999999999999999"
	jsonText := fmt.Sprintf(`{
		"guild": {"id": "1"},
		"channel": {"id": "2"},
		"messages": [{"id": %q, "timestamp": "2026-09-14T12:00:00Z", "content": "test"}]
	}`, largeID)

	res, err := parseDCEExport([]byte(jsonText))
	if err != nil {
		t.Fatalf("failed to parse large snowflake: %v", err)
	}
	if res.Messages[0].ID != largeID {
		t.Fatalf("expected %s, got %s", largeID, res.Messages[0].ID)
	}
	if res.MaxMessageID != largeID {
		t.Fatalf("expected MaxMessageID %s, got %s", largeID, res.MaxMessageID)
	}
}

// 18. chronological output behavior matches chosen contract (oldest -> newest)
func TestParser_ChronologicalOrdering(t *testing.T) {
	unorderedJSON := `{
		"guild": {"id": "1"},
		"channel": {"id": "2"},
		"messages": [
			{"id": "300", "timestamp": "2026-09-14T12:05:00Z", "content": "later"},
			{"id": "100", "timestamp": "2026-09-14T12:00:00Z", "content": "earliest"},
			{"id": "200", "timestamp": "2026-09-14T12:02:00Z", "content": "middle"}
		]
	}`

	res, err := parseDCEExport([]byte(unorderedJSON))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(res.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(res.Messages))
	}
	if res.Messages[0].ID != "100" || res.Messages[1].ID != "200" || res.Messages[2].ID != "300" {
		t.Fatalf("messages not sorted chronologically: got IDs [%s, %s, %s]",
			res.Messages[0].ID, res.Messages[1].ID, res.Messages[2].ID)
	}
}

// 19. max/latest message ID behavior correct
func TestParser_MaxMessageID(t *testing.T) {
	jsonText := `{
		"guild": {"id": "1"},
		"channel": {"id": "2"},
		"messages": [
			{"id": "100", "timestamp": "2026-09-14T12:00:00Z"},
			{"id": "500", "timestamp": "2026-09-14T12:01:00Z"},
			{"id": "200", "timestamp": "2026-09-14T12:02:00Z"}
		]
	}`

	res, err := parseDCEExport([]byte(jsonText))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.MaxMessageID != "500" {
		t.Fatalf("expected MaxMessageID 500, got %s", res.MaxMessageID)
	}
}

// 20. parser preserves timestamp, author, content, reply/attachment fields
func TestParser_PreservesCoreFields(t *testing.T) {
	res, err := parseDCEExport([]byte(sampleValidJSON))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	m := res.Messages[0]
	if m.ID != "1549028055563903046" {
		t.Fatalf("message ID mismatch: %s", m.ID)
	}
	if m.Content != "Hello world from test" {
		t.Fatalf("content mismatch: %s", m.Content)
	}
	if m.Author.DisplayName() != "TestUser" {
		t.Fatalf("display name mismatch: %s", m.Author.DisplayName())
	}
	if m.Author.Name != "tester1" {
		t.Fatalf("author name mismatch: %s", m.Author.Name)
	}
	if m.ReplyTo == nil || m.ReplyTo.MessageID != "1549026953594413099" {
		t.Fatalf("reply ref mismatch: %+v", m.ReplyTo)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].FileName != "test.png" || m.Attachments[0].Bytes != 1024 {
		t.Fatalf("attachment mismatch: %+v", m.Attachments)
	}
	if len(m.Embeds) != 1 || m.Embeds[0].Title != "Example Link" {
		t.Fatalf("embed mismatch: %+v", m.Embeds)
	}
}

// 21. raw export temp file is cleaned when owned by the adapter
func TestCleanup_TempFileRemoved(t *testing.T) {
	var writtenFilePath string
	runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
		writtenFilePath = getArgValue(args, "-o")
		return os.WriteFile(writtenFilePath, []byte(sampleValidJSON), 0600)
	}

	customDir := t.TempDir()
	client := NewMockClient("dce.exe", "fake-token", runner)
	req := ExportRequest{
		ChannelID: "1391912303376728155",
		OutputDir: customDir,
	}

	_, err := client.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	if writtenFilePath == "" {
		t.Fatal("no output path was written")
	}

	// Verify the file was cleaned up by defer
	if _, err := os.Stat(writtenFilePath); !os.IsNotExist(err) {
		t.Fatalf("expected temp export file %s to be removed, but it still exists", writtenFilePath)
	}
}

// 22. returned errors never include a known fake token string
func TestSecurity_TokenRedactedFromErrors(t *testing.T) {
	const secretToken = "SECRET_TOKEN_DO_NOT_LEAK_xyz123"
	runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
		// Simulate child process emitting token in stderr
		_, _ = io.WriteString(stderr, fmt.Sprintf("Authentication failed with token: %s", secretToken))
		return errors.New("command failed")
	}

	client := NewMockClient("dce.exe", secretToken, runner)
	req := ExportRequest{ChannelID: "1391912303376728155"}

	_, err := client.Export(context.Background(), req)
	if err == nil {
		t.Fatal("expected export error, got nil")
	}

	errStr := err.Error()
	if strings.Contains(errStr, secretToken) {
		t.Fatalf("CRITICAL SECURITY ERROR: token leaked in error message: %s", errStr)
	}
	if !strings.Contains(errStr, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] in sanitized error, got: %s", errStr)
	}
}

// Snowflake comparison helper unit tests
func TestCompareSnowflake(t *testing.T) {
	tests := []struct {
		a, b     string
		expected int
	}{
		{"100", "100", 0},
		{"99", "100", -1},
		{"100", "99", 1},
		{"1000", "999", 1},
		{"999", "1000", -1},
		{"1549026953594413099", "1549028055563903046", -1},
		{"1549028055563903046", "1549026953594413099", 1},
		{"99999999999999999999", "100000000000000000000", -1},
	}

	for _, tc := range tests {
		got := CompareSnowflake(tc.a, tc.b)
		if got != tc.expected {
			t.Errorf("CompareSnowflake(%q, %q) = %d, expected %d", tc.a, tc.b, got, tc.expected)
		}
	}
}

// helper to extract value for a flag from args list
func getArgValue(args []string, flag string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(args[i], flag+"=") {
			return strings.TrimPrefix(args[i], flag+"=")
		}
	}
	return ""
}
