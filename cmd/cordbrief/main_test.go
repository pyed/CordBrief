package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cordbrief/internal/config"
	"cordbrief/internal/discord"
)

func TestCLI_Version(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := Run([]string{"version"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0 for version, got %d", exitCode)
	}

	out := stdout.String()
	if !strings.Contains(out, "cordbrief v0.1.0-dev") {
		t.Errorf("expected version output to contain version string, got: %s", out)
	}
}

func TestCLI_UnimplementedCommands(t *testing.T) {
	commands := []string{"run", "serve"}

	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exitCode := Run([]string{cmd}, &stdout, &stderr)
			if exitCode != 1 {
				t.Fatalf("expected exit code 1 for %s in Milestone 2, got %d", cmd, exitCode)
			}

			errOut := stderr.String()
			expectedMsg := "'" + cmd + "' is not implemented in Milestone 2"
			if !strings.Contains(errOut, expectedMsg) {
				t.Errorf("expected error containing %q, got: %s", expectedMsg, errOut)
			}
		})
	}
}

func TestCLI_Channels(t *testing.T) {
	server := discord.NewFakeDiscordServer()
	defer server.Close()
	server.ExpectedToken = "test-token"

	t.Setenv(config.EnvDiscordToken, "test-token")
	t.Setenv("CORDBRIEF_DISCORD_API_BASE", server.URL)

	server.GuildChannels["guild-123"] = []discord.Channel{
		{ID: "ch-ann", Name: "announcements", Type: discord.ChannelTypeGuildAnnouncement, Position: 1},
		{ID: "ch-gen", Name: "general", Type: discord.ChannelTypeGuildText, Position: 2},
		{ID: "ch-voice", Name: "voice-chat", Type: 2, Position: 3}, // Unsupported
	}

	t.Run("channels success with -guild flag", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"channels", "-guild", "guild-123"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("expected code 0, got %d. stderr: %s", code, stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "announcements") || !strings.Contains(out, "ch-ann") {
			t.Errorf("missing announcements channel in output: %s", out)
		}
		if !strings.Contains(out, "general") || !strings.Contains(out, "ch-gen") {
			t.Errorf("missing general channel in output: %s", out)
		}
		if strings.Contains(out, "voice-chat") {
			t.Errorf("unsupported voice channel should not appear: %s", out)
		}
	})

	t.Run("channels auth failure", func(t *testing.T) {
		t.Setenv(config.EnvDiscordToken, "wrong-token")
		var stdout, stderr bytes.Buffer
		code := Run([]string{"channels", "-guild", "guild-123"}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("expected code 1 on auth failure, got %d", code)
		}
		if !strings.Contains(stderr.String(), "authentication failed") {
			t.Errorf("expected auth error on stderr, got: %s", stderr.String())
		}
		t.Setenv(config.EnvDiscordToken, "test-token")
	})
}

func TestCLI_Doctor(t *testing.T) {
	server := discord.NewFakeDiscordServer()
	defer server.Close()
	server.ExpectedToken = "doc-token"

	t.Setenv(config.EnvDiscordToken, "doc-token")
	t.Setenv("CORDBRIEF_DISCORD_API_BASE", server.URL)

	guildID := "guild-doc"
	server.GuildChannels[guildID] = []discord.Channel{
		{ID: "src-1", Name: "announcements", Type: discord.ChannelTypeGuildAnnouncement, Position: 1},
		{ID: "src-2", Name: "discussions", Type: discord.ChannelTypeGuildText, Position: 2},
		{ID: "dest-1", Name: "digest-feed", Type: discord.ChannelTypeGuildText, Position: 3},
	}

	secretSampleMessage := "CONFIDENTIAL_PAYROLL_DATA_DO_NOT_LEAK"
	server.Messages["src-1"] = []map[string]any{
		{"id": "msg-1", "content": secretSampleMessage, "timestamp": "2026-09-03T12:00:00Z"},
	}
	server.Messages["src-2"] = []map[string]any{} // empty channel

	// Helper to write config
	writeDoctorConfig := func(t *testing.T, srcIDs []string, destID string) string {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.json")
		content := fmt.Sprintf(`{
			"guild_id": "%s",
			"source_channel_ids": ["%s"],
			"digest_channel_id": "%s",
			"schedule": {"time": "08:00", "timezone": "UTC"},
			"llm": {"base_url": "http://localhost:8080/v1", "model": "test"},
			"digest": {}
		}`, guildID, strings.Join(srcIDs, `","`), destID)

		if err := os.WriteFile(cfgPath, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return cfgPath
	}

	t.Run("doctor healthy diagnostics with positive content and inconclusive empty channel", func(t *testing.T) {
		cfgPath := writeDoctorConfig(t, []string{"src-1", "src-2"}, "dest-1")
		var stdout, stderr bytes.Buffer
		code := Run([]string{"doctor", "-config", cfgPath}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("expected code 0 for healthy doctor, got %d. Out: %s", code, stdout.String())
		}
		out := stdout.String()

		// Verify expected milestones
		if !strings.Contains(out, "[PASS] Config") {
			t.Error("expected [PASS] Config")
		}
		if !strings.Contains(out, "[PASS] Discord Auth") {
			t.Error("expected [PASS] Discord Auth")
		}
		if !strings.Contains(out, "[PASS] Guild") {
			t.Error("expected [PASS] Guild")
		}
		if !strings.Contains(out, "[PASS] Source Channel 'announcements'") || !strings.Contains(out, "content observed") {
			t.Errorf("expected content observed verification on src-1: %s", out)
		}
		if !strings.Contains(out, "cannot be conclusively determined") {
			t.Errorf("expected inconclusive notice on privileged intent: %s", out)
		}
		if !strings.Contains(out, "[WARN] Source Channel 'discussions'") || !strings.Contains(out, "Inconclusive") && !strings.Contains(out, "empty") {
			t.Errorf("expected inconclusive warning on empty src-2: %s", out)
		}
		if !strings.Contains(out, "[PASS] Digest Channel 'digest-feed'") {
			t.Errorf("expected pass on digest channel: %s", out)
		}
		if !strings.Contains(out, "[DEFERRED] LLM Connectivity") {
			t.Error("expected [DEFERRED] LLM Connectivity")
		}

		// Security check: NEVER leak raw message text
		if strings.Contains(out, secretSampleMessage) {
			t.Fatalf("CRITICAL SECURITY VIOLATION: doctor printed secret message text: %s", out)
		}
	})

	t.Run("doctor fails on inaccessible 403 source channel", func(t *testing.T) {
		server.ChannelStatus["src-forbidden"] = http.StatusForbidden
		server.GuildChannels[guildID] = append(server.GuildChannels[guildID], discord.Channel{
			ID: "src-forbidden", Name: "secret-room", Type: discord.ChannelTypeGuildText,
		})

		cfgPath := writeDoctorConfig(t, []string{"src-forbidden"}, "dest-1")
		var stdout, stderr bytes.Buffer
		code := Run([]string{"doctor", "-config", cfgPath}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("expected code 1 on forbidden source, got %d. Out: %s", code, stdout.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "[FAIL] Source Channel 'secret-room'") {
			t.Errorf("expected FAIL for forbidden channel, got: %s", out)
		}
	})

	t.Run("doctor fails on unsupported channel type", func(t *testing.T) {
		cfgPath := writeDoctorConfig(t, []string{"ch-nonexistent"}, "dest-1")
		var stdout, stderr bytes.Buffer
		code := Run([]string{"doctor", "-config", cfgPath}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("expected code 1 on missing source channel, got %d", code)
		}
		out := stdout.String()
		if !strings.Contains(out, "[FAIL] Source Channel ID ch-nonexistent") {
			t.Errorf("expected FAIL on nonexistent source: %s", out)
		}
	})
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
