package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"cordbrief/internal/config"
	"cordbrief/internal/discord"
)

const Version = "v0.1.0-dev"

// defaultAPIBase allows tests to inject an httptest.Server URL.
var defaultAPIBase = discord.DefaultBaseURL

func getAPIBase() string {
	if env := os.Getenv("CORDBRIEF_DISCORD_API_BASE"); env != "" {
		return env
	}
	return defaultAPIBase
}

func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}

// Run executes the CLI with the provided arguments, writing output to stdout and errors to stderr.
// It returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 1
	}

	subcommand := args[0]
	subArgs := args[1:]

	switch subcommand {
	case "version", "-version", "--version", "-v":
		fmt.Fprintf(stdout, "cordbrief %s (go: %s, os: %s, arch: %s)\n", Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0

	case "channels":
		return runChannels(subArgs, stdout, stderr)

	case "doctor":
		return runDoctor(subArgs, stdout, stderr)

	case "run":
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		fs.SetOutput(stderr)
		_ = fs.String("config", "config.json", "path to configuration file")
		_ = fs.Bool("dry-run", false, "render digest without posting or updating state")
		if err := fs.Parse(subArgs); err != nil {
			return 2
		}
		fmt.Fprintln(stderr, "error: 'run' is not implemented in Milestone 2")
		return 1

	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		fs.SetOutput(stderr)
		_ = fs.String("config", "config.json", "path to configuration file")
		if err := fs.Parse(subArgs); err != nil {
			return 2
		}
		fmt.Fprintln(stderr, "error: 'serve' is not implemented in Milestone 2")
		return 1

	case "help", "-help", "--help", "-h":
		printUsage(stdout)
		return 0

	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", subcommand)
		printUsage(stderr)
		return 1
	}
}

func runChannels(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("channels", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "config.json", "path to configuration file")
	guildIDFlag := fs.String("guild", "", "Discord guild ID (optional override)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	token := strings.TrimSpace(os.Getenv(config.EnvDiscordToken))
	if token == "" {
		fmt.Fprintf(stderr, "error: environment variable %s is not set or empty\n", config.EnvDiscordToken)
		return 1
	}

	guildID := strings.TrimSpace(*guildIDFlag)
	if guildID == "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			fmt.Fprintf(stderr, "error: failed to load config from %s: %v\n(Hint: pass -guild <id> to query channels before creating full config)\n", *configPath, err)
			return 1
		}
		guildID = cfg.GuildID
	}

	if guildID == "" {
		fmt.Fprintln(stderr, "error: guild ID is required (set 'guild_id' in config.json or specify with -guild <id>)")
		return 1
	}

	client := discord.NewClient(token, discord.WithBaseURL(getAPIBase()))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ident, err := client.GetBotIdentity(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "error: Discord authentication failed: %v\n", err)
		return 1
	}

	channels, err := client.ListGuildChannels(ctx, guildID)
	if err != nil {
		fmt.Fprintf(stderr, "error: failed listing guild channels: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Supported channels in guild %s (bot: @%s):\n\n", guildID, ident.Username)
	if len(channels) == 0 {
		fmt.Fprintln(stdout, "No supported text or announcement channels found in this guild.")
		return 0
	}

	fmt.Fprintf(stdout, "%-30s %-22s %s\n", "NAME", "CHANNEL ID", "TYPE")
	fmt.Fprintf(stdout, "%-30s %-22s %s\n", strings.Repeat("-", 30), strings.Repeat("-", 22), strings.Repeat("-", 18))
	for _, ch := range channels {
		fmt.Fprintf(stdout, "%-30s %-22s %s\n", ch.Name, ch.ID, ch.TypeName())
	}
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Note: Run 'cordbrief doctor' to verify bot permissions for configured channels.")
	return 0
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "config.json", "path to configuration file")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	fmt.Fprintln(stdout, "Running CordBrief diagnostics...")
	fmt.Fprintln(stdout, "")

	// 1. Config loading & validation
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stdout, "[FAIL] Config: Failed to load from %s: %v\n", *configPath, err)
		return 1
	}
	fmt.Fprintf(stdout, "[PASS] Config: Loaded and validated successfully from %s\n", *configPath)

	// 2. Token presence
	token, err := cfg.DiscordToken()
	if err != nil {
		fmt.Fprintf(stdout, "[FAIL] Discord Token: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "[PASS] Discord Token: Present in %s\n", config.EnvDiscordToken)

	// 3. Client & Authentication
	client := discord.NewClient(token, discord.WithBaseURL(getAPIBase()))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ident, err := client.GetBotIdentity(ctx)
	if err != nil {
		fmt.Fprintf(stdout, "[FAIL] Discord Auth: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "[PASS] Discord Auth: Authenticated as bot @%s (ID: %s)\n", ident.Username, ident.ID)

	// 4. Guild Channels discovery
	guildChannels, err := client.ListGuildChannels(ctx, cfg.GuildID)
	if err != nil {
		fmt.Fprintf(stdout, "[FAIL] Guild: Failed to retrieve channels for guild %s: %v\n", cfg.GuildID, err)
		return 1
	}
	fmt.Fprintf(stdout, "[PASS] Guild: Guild %s channels retrieved (%d supported channels found)\n", cfg.GuildID, len(guildChannels))

	channelMap := make(map[string]discord.Channel)
	for _, ch := range guildChannels {
		channelMap[ch.ID] = ch
	}

	hasFailure := false

	// 5. Check each source channel
	for _, srcID := range cfg.SourceChannelIDs {
		ch, found := channelMap[srcID]
		if !found {
			fmt.Fprintf(stdout, "[FAIL] Source Channel ID %s: Not found in guild or not a supported channel type\n", srcID)
			hasFailure = true
			continue
		}

		// History probe (limit=1)
		probe, err := client.ProbeChannelHistory(ctx, srcID)
		if err != nil || !probe.Accessible {
			probeErr := err
			if probe != nil && probe.Error != nil {
				probeErr = probe.Error
			}
			fmt.Fprintf(stdout, "[FAIL] Source Channel '%s' (ID: %s, %s): History endpoint inaccessible (%v)\n", ch.Name, ch.ID, ch.TypeName(), probeErr)
			hasFailure = true
			continue
		}

		if probe.MessageCount > 0 {
			if probe.HasContent {
				fmt.Fprintf(stdout, "[PASS] Source Channel '%s' (ID: %s, %s): History accessible; content observed (Note: privileged Message Content intent cannot be conclusively determined from probe alone)\n", ch.Name, ch.ID, ch.TypeName())
			} else {
				fmt.Fprintf(stdout, "[WARN] Source Channel '%s' (ID: %s, %s): History accessible (sampled message has empty content; Message Content intent is inconclusive)\n", ch.Name, ch.ID, ch.TypeName())
			}
		} else {
			fmt.Fprintf(stdout, "[WARN] Source Channel '%s' (ID: %s, %s): History accessible (0 messages returned; channel is empty, cannot conclusively verify Read Message History or Message Content intent)\n", ch.Name, ch.ID, ch.TypeName())
		}
	}

	// 6. Check destination channel
	destCh, found := channelMap[cfg.DigestChannelID]
	if !found {
		fmt.Fprintf(stdout, "[FAIL] Digest Channel ID %s: Not found in guild or not a supported channel type\n", cfg.DigestChannelID)
		hasFailure = true
	} else {
		fmt.Fprintf(stdout, "[PASS] Digest Channel '%s' (ID: %s, %s): Channel exists in guild (Note: 'Send Messages' permission is tested upon posting in later milestones)\n", destCh.Name, destCh.ID, destCh.TypeName())
	}

	// 7. LLM Connectivity deferred
	fmt.Fprintln(stdout, "[DEFERRED] LLM Connectivity: Deferred to Milestone 3 (not checked)")

	if hasFailure {
		fmt.Fprintln(stdout, "\nResult: Diagnostic checks failed.")
		return 1
	}

	fmt.Fprintln(stdout, "\nResult: All Discord read diagnostic checks passed!")
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "CordBrief: A tiny, self-hosted Discord daily-digest application")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  cordbrief <command> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  doctor      Validate config, Discord permissions, and LLM connectivity")
	fmt.Fprintln(w, "  channels    List guild channels to assist with configuration")
	fmt.Fprintln(w, "  run         Execute a digest cycle (supports --dry-run)")
	fmt.Fprintln(w, "  serve       Run the scheduled digest service")
	fmt.Fprintln(w, "  version     Print version information")
}
