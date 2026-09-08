package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"cordbrief/internal/config"
	"cordbrief/internal/delivery"
	"cordbrief/internal/digest"
	"cordbrief/internal/discord"
	"cordbrief/internal/journal"
	"cordbrief/internal/llm"
	"cordbrief/internal/scheduler"
	"cordbrief/internal/web"
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

	case "exchange":
		return runExchange(subArgs, stdout, stderr)

	case "digest":
		return runDigest(subArgs, stdout, stderr)

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
		return runServe(subArgs, stdout, stderr)

	case "lock-probe":
		return runLockProbe(subArgs, stdout, stderr)

	case "migrate":
		return runMigrate(subArgs, stdout, stderr)

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

func getExchangeDir(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("CORDBRIEF_EXCHANGE_DIR"); env != "" {
		return env
	}
	return "/var/cordbrief/exchange"
}

func runExchange(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "error: missing exchange subcommand (write-watchlist, read-watchlist, status, ingest)")
		return 1
	}

	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "write-watchlist":
		fs := flag.NewFlagSet("exchange write-watchlist", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dirFlag := fs.String("exchange-dir", "", "path to exchange directory")
		genFlag := fs.Int64("generation", 1, "watchlist generation number")
		channelsFlag := fs.String("channels", "", "comma-separated channel IDs")
		if err := fs.Parse(subArgs); err != nil {
			return 2
		}
		if *channelsFlag == "" {
			fmt.Fprintln(stderr, "error: --channels is required (comma-separated IDs)")
			return 1
		}
		rawIDs := strings.Split(*channelsFlag, ",")
		w := &journal.Watchlist{
			Version:    journal.CurrentSchemaVersion,
			Generation: *genFlag,
			ChannelIDs: rawIDs,
		}
		targetPath := filepath.Join(getExchangeDir(*dirFlag), journal.DefaultWatchlistFilename)
		if err := journal.WriteWatchlist(targetPath, w); err != nil {
			fmt.Fprintf(stderr, "error writing watchlist: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "[PASS] Watchlist written: generation %d, %d channels -> %s\n", w.Generation, len(w.ChannelIDs), targetPath)
		return 0

	case "read-watchlist":
		fs := flag.NewFlagSet("exchange read-watchlist", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dirFlag := fs.String("exchange-dir", "", "path to exchange directory")
		if err := fs.Parse(subArgs); err != nil {
			return 2
		}
		targetPath := filepath.Join(getExchangeDir(*dirFlag), journal.DefaultWatchlistFilename)
		w, err := journal.ReadWatchlist(targetPath)
		if err != nil {
			fmt.Fprintf(stderr, "error reading watchlist: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Watchlist Generation: %d\n", w.Generation)
		fmt.Fprintf(stdout, "Channel Count: %d\n", len(w.ChannelIDs))
		for i, ch := range w.ChannelIDs {
			fmt.Fprintf(stdout, "  [%d] %s\n", i+1, ch)
		}
		return 0

	case "status":
		fs := flag.NewFlagSet("exchange status", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dirFlag := fs.String("exchange-dir", "", "path to exchange directory")
		if err := fs.Parse(subArgs); err != nil {
			return 2
		}
		targetPath := filepath.Join(getExchangeDir(*dirFlag), journal.DefaultStatusFilename)
		st, err := journal.ReadCollectorStatus(targetPath)
		if err != nil {
			fmt.Fprintf(stderr, "error reading collector status: %v\n", err)
			return 1
		}
		authStr := "unknown"
		if st.DiscordAuthenticated != nil {
			authStr = fmt.Sprintf("%t", *st.DiscordAuthenticated)
		}
		fmt.Fprintf(stdout, "Collector State:       %s\n", st.CollectorState)
		fmt.Fprintf(stdout, "Discord Authenticated: %s\n", authStr)
		if st.CatalogState != "" {
			fmt.Fprintf(stdout, "Catalog State:         %s\n", st.CatalogState)
		}
		if st.CatalogUpdatedAt != nil {
			fmt.Fprintf(stdout, "Catalog Updated At:    %s\n", st.CatalogUpdatedAt.Format(time.RFC3339))
		}
		fmt.Fprintf(stdout, "Watched Generation:    %d\n", st.WatchedGeneration)
		fmt.Fprintf(stdout, "Watched Channel Count: %d\n", st.WatchedChannelCount)
		fmt.Fprintf(stdout, "Active Segment:        %d\n", st.ActiveSegment)
		fmt.Fprintf(stdout, "Updated At:            %s\n", st.UpdatedAt.Format(time.RFC3339))
		if st.LastEventAt != nil {
			fmt.Fprintf(stdout, "Last Event At:         %s\n", st.LastEventAt.Format(time.RFC3339))
		}
		return 0

	case "ingest":
		fs := flag.NewFlagSet("exchange ingest", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dirFlag := fs.String("exchange-dir", "", "path to exchange directory")
		dataDirFlag := fs.String("data-dir", "", "path to data directory")
		limitFlag := fs.Int("limit", 100, "maximum events to read in batch")
		commitFlag := fs.Bool("commit", false, "commit advanced cursor to core-ack.json after reading")
		if err := fs.Parse(subArgs); err != nil {
			return 2
		}

		exchangeDir := getExchangeDir(*dirFlag)
		actualDataDir := getDataDir(*dataDirFlag)
		commitLock, lockErr := journal.AcquireCommitLock(actualDataDir)
		if lockErr != nil {
			fmt.Fprintf(stderr, "cannot read journal: %v\n", lockErr)
			return 1
		}
		defer commitLock.Release()
		ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)
		eventsDir := filepath.Join(exchangeDir, "events")

		cur, err := journal.LoadCursor(ackPath)
		if err != nil {
			fmt.Fprintf(stderr, "error loading cursor: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Cursor Before: segment=%d, offset=%d\n", cur.Segment, cur.Offset)

		wm, err := journal.CaptureWatermark(eventsDir)
		if err != nil {
			fmt.Fprintf(stderr, "error capturing watermark: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Watermark: max_segment=%d, total_segments=%d\n", wm.MaxSegment, len(wm.Segments))

		reader := journal.NewReader(eventsDir, wm)
		records, nextCur, err := reader.ReadBatch(*cur, *limitFlag)
		if err != nil {
			fmt.Fprintf(stderr, "error reading batch: %v\n", err)
			return 1
		}

		fmt.Fprintf(stdout, "Events Read: %d\n", len(records))
		for i, rec := range records {
			fmt.Fprintf(stdout, "  [%d] msg_id=%s ch_id=%s author=%s (seg=%d, off=%d, next_off=%d)\n",
				i+1, rec.Event.MessageID, rec.Event.ChannelID, rec.Event.Author.Name, rec.Segment, rec.Offset, rec.NextOffset)
		}
		fmt.Fprintf(stdout, "Cursor After:  segment=%d, offset=%d\n", nextCur.Segment, nextCur.Offset)

		if *commitFlag {
			if err := journal.SaveCursor(ackPath, &nextCur); err != nil {
				fmt.Fprintf(stderr, "error committing cursor: %v\n", err)
				return 1
			}
			fmt.Fprintln(stdout, "[PASS] Committed new cursor to core-ack.json")
		} else {
			fmt.Fprintln(stdout, "[NOTE] Dry-run read: cursor not committed")
		}
		return 0

	default:
		fmt.Fprintf(stderr, "error: unknown exchange subcommand %q\n", sub)
		return 1
	}
}

func getDataDir(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("CORDBRIEF_DATA_DIR"); env != "" {
		return env
	}
	return "/var/cordbrief/data"
}

func runDigest(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "error: missing digest subcommand (preview, run)")
		return 1
	}

	sub := args[0]
	subArgs := args[1:]

	if sub != "preview" && sub != "run" && sub != "test-llm" {
		fmt.Fprintf(stderr, "error: unknown digest subcommand %q (expected 'preview', 'run', or 'test-llm')\n", sub)
		return 1
	}

	fs := flag.NewFlagSet("digest "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "config.json", "path to configuration file")
	exchangeDir := fs.String("exchange-dir", "", "path to exchange directory")
	dataDir := fs.String("data-dir", "", "path to data directory")
	limit := fs.Int("limit", 1000, "maximum events to read in batch")
	deliverFlag := fs.Bool("deliver", false, "deliver digest via configured channels (e.g. Telegram)")

	if err := fs.Parse(subArgs); err != nil {
		return 2
	}

	// 1. Resolve configuration
	providerType := config.ProviderGemini
	llmBaseURL := config.DefaultGeminiBaseURL
	llmModel := config.DefaultGeminiModel
	apiKeyEnv := config.DefaultGeminiKeyEnv
	maxInputChars := config.DefaultGeminiMaxInput
	maxOutputTokens := config.DefaultGeminiMaxOutput
	timeoutSecs := config.DefaultGeminiTimeout
	lang := config.DefaultLanguage
	var focus []string
	var ignoreBots bool

	if *cfgPath != "" {
		if cfg, err := config.Load(*cfgPath); err == nil {
			providerType = cfg.LLM.Provider
			llmBaseURL = cfg.LLM.BaseURL
			llmModel = cfg.LLM.Model
			apiKeyEnv = cfg.LLM.APIKeyEnv
			maxInputChars = cfg.LLM.MaxInputChars
			maxOutputTokens = cfg.LLM.MaxOutputTokens
			timeoutSecs = cfg.LLM.TimeoutSeconds
			if cfg.Digest.OutputLanguage != "" {
				lang = cfg.Digest.OutputLanguage
			}
			focus = cfg.Digest.Focus
			ignoreBots = cfg.Digest.IgnoreBots
		} else if !os.IsNotExist(err) && *cfgPath != "config.json" {
			fmt.Fprintf(stderr, "error loading config: %v\n", err)
			return 1
		}
	}

	// Environment overrides
	if env := os.Getenv("CORDBRIEF_LLM_PROVIDER"); env != "" {
		providerType = strings.ToLower(strings.TrimSpace(env))
	}
	if env := os.Getenv("CORDBRIEF_LLM_BASE_URL"); env != "" {
		llmBaseURL = env
	}
	if env := os.Getenv("CORDBRIEF_LLM_MODEL"); env != "" {
		llmModel = env
	}

	var llmAPIKey string
	if apiKeyEnv != "" {
		llmAPIKey = os.Getenv(apiKeyEnv)
	}
	if envKey := os.Getenv("GEMINI_API_KEY"); envKey != "" && providerType == config.ProviderGemini && llmAPIKey == "" {
		llmAPIKey = envKey
	}
	if envKey := os.Getenv("CORDBRIEF_LLM_API_KEY"); envKey != "" && llmAPIKey == "" {
		llmAPIKey = envKey
	}

	// Validation
	switch providerType {
	case config.ProviderGemini:
		if llmBaseURL == "" {
			llmBaseURL = config.DefaultGeminiBaseURL
		}
		if llmModel == "" {
			llmModel = config.DefaultGeminiModel
		}
		if llmAPIKey == "" {
			fmt.Fprintln(stderr, "error: GEMINI_API_KEY is not set. Get an API key from Google AI Studio and set the GEMINI_API_KEY environment variable.")
			return 1
		}
	case config.ProviderLocal:
		if llmBaseURL == "" {
			fmt.Fprintln(stderr, "error: llm.base_url is required for provider 'local' (e.g. 'http://host.docker.internal:8081/v1')")
			return 1
		}
		if llmModel == "" {
			fmt.Fprintln(stderr, "error: llm.model is required for provider 'local'")
			return 1
		}
	default:
		fmt.Fprintf(stderr, "error: unsupported llm.provider %q (must be 'gemini' or 'local')\n", providerType)
		return 1
	}

	providerDisplayName := "Gemini"
	if providerType == config.ProviderLocal {
		providerDisplayName = "Local"
	}

	// 2. Initialize provider & pipeline
	provider := llm.NewOpenAICompatibleProvider(llmBaseURL, llmModel, llmAPIKey, maxOutputTokens, time.Duration(timeoutSecs)*time.Second)

	if sub == "test-llm" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSecs)*time.Second)
		defer cancel()
		resp, err := provider.GenerateText(ctx, "You are a test responder.", "Respond with the single word PONG")
		if err != nil {
			fmt.Fprintf(stderr, "error connecting to %s (%s): %v\n", providerDisplayName, llmModel, err)
			return 1
		}
		fmt.Fprintf(stdout, "[PASS] LLM Connectivity Verified: %s (%s)\nResponse: %s\n", providerDisplayName, llmModel, strings.TrimSpace(resp))
		return 0
	}

	pipe := llm.NewPipeline(provider, maxInputChars, llm.PromptConfig{
		Language: lang,
		Focus:    focus,
	})

	// 3. Execute transaction
	commit := (sub == "run")
	{
		commitLock, lockErr := journal.AcquireCommitLock(getDataDir(*dataDir))
		if lockErr != nil {
			fmt.Fprintf(stderr, "cannot run digest: %v\n", lockErr)
			return 1
		}
		defer commitLock.Release()
	}

	var delSvc *delivery.Service
	var delReq *digest.DeliveryRequest
	if commit && *deliverFlag {
		actualDataDir := getDataDir(*dataDir)
		actualExchangeDir := getExchangeDir(*exchangeDir)
		store, storeErr := config.NewStore(actualDataDir, *cfgPath)
		if storeErr != nil {
			fmt.Fprintf(stderr, "error initializing config store: %v\n", storeErr)
			return 1
		}
		delCfg := store.GetDeliveryConfig()
		if !delCfg.Telegram.Enabled || !store.IsTelegramConfigured() || delCfg.Telegram.ChatID == "" {
			fmt.Fprintln(stderr, "error: --deliver specified but Telegram is not enabled or configured")
			return 1
		}
		delReq = &digest.DeliveryRequest{
			Provider:  "telegram",
			ChatID:    delCfg.Telegram.ChatID,
			ChatLabel: delCfg.Telegram.ChatLabel,
		}
		var svcErr error
		delSvc, svcErr = delivery.NewService(delivery.ServiceOptions{
			DataDir:     actualDataDir,
			ExchangeDir: actualExchangeDir,
			Store:       store,
		})
		if svcErr != nil {
			fmt.Fprintf(stderr, "error initializing delivery service: %v\n", svcErr)
			return 1
		}
	}

	opts := digest.TransactionOptions{
		ExchangeDir:     getExchangeDir(*exchangeDir),
		DataDir:         getDataDir(*dataDir),
		IgnoreBots:      ignoreBots,
		BatchLimit:      *limit,
		ProviderName:    providerType,
		ModelName:       llmModel,
		Commit:          commit,
		DeliveryRequest: delReq,
	}

	if delSvc != nil {
		opts.PrepareDeliveryIntentFn = func(batchID string, targetCur journal.Cursor, req *digest.DeliveryRequest) error {
			_, err := delSvc.PrepareDeliveryIntent(batchID, targetCur, req)
			return err
		}
		opts.PromoteDeliveryIntentFn = func(batchID string) error {
			_, err := delSvc.PromoteDeliveryIntent(batchID)
			return err
		}
	}

	res, err := digest.RunTransaction(context.Background(), pipe, opts)
	if err != nil {
		fmt.Fprintf(stderr, "digest error: %v\n", err)
		return 1
	}

	if res.Empty {
		fmt.Fprintln(stdout, "[INFO] No new journal events to process.")
		return 0
	}

	if res.AllExcluded {
		fmt.Fprintln(stdout, "[INFO] Journal events contained only excluded messages (e.g. bots). Cursor advanced past range.")
		return 0
	}

	if res.WasIdempotentHit {
		fmt.Fprintf(stdout, "[IDEMPOTENT HIT] Found existing artifact on disk for batch %s\n", res.Batch.BatchID)
		fmt.Fprintf(stdout, "Artifact: %s\n", res.ArtifactPath)
		fmt.Fprintf(stdout, "Committed Cursor: segment=%d, offset=%d\n\n", res.CommittedCursor.Segment, res.CommittedCursor.Offset)
		fmt.Fprint(stdout, digest.RenderMarkdown(res.Digest, res.Batch))
		return 0
	}

	fmt.Fprintf(stdout, "Batch ID:          %s\n", res.Batch.BatchID)
	fmt.Fprintf(stdout, "Total Messages:    %d\n", res.Batch.TotalJournalRecords)
	fmt.Fprintf(stdout, "Included Messages: %d\n", len(res.Batch.IncludedMessages))
	fmt.Fprintf(stdout, "Provider:          %s\n", providerDisplayName)
	fmt.Fprintf(stdout, "Model:             %s\n", llmModel)
	if commit {
		fmt.Fprintf(stdout, "Artifact:          %s\n", res.ArtifactPath)
		fmt.Fprintf(stdout, "Committed Cursor:  segment=%d, offset=%d\n", res.CommittedCursor.Segment, res.CommittedCursor.Offset)
	} else {
		fmt.Fprintf(stdout, "[PREVIEW] Cursor untouched: segment=%d, offset=%d\n", res.CommittedCursor.Segment, res.CommittedCursor.Offset)
	}

	fmt.Fprintln(stdout, "\n--- Rendered Digest ---")
	fmt.Fprint(stdout, digest.RenderMarkdown(res.Digest, res.Batch))

	if delSvc != nil && res != nil && res.Artifact != nil {
		delRec, delErr := delSvc.DeliverBatch(context.Background(), res.Artifact.BatchID, false)
		if delErr != nil {
			fmt.Fprintf(stderr, "\n[WARN] Telegram delivery failed: %v\n", delErr)
		} else if delRec != nil {
			fmt.Fprintf(stdout, "\n[PASS] Delivered to Telegram (batch: %s, parts: %d)\n", res.Artifact.BatchID, delRec.TotalParts)
		}
	}

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
	fmt.Fprintln(w, "  exchange    Manage exchange watchlist, inspect status, and ingest events")
	fmt.Fprintln(w, "  digest      Generate structured digest from exchange journal (preview, run, test-llm)")
	fmt.Fprintln(w, "  run         Execute a digest cycle (supports --dry-run)")
	fmt.Fprintln(w, "  serve       Run the web setup control plane")
	fmt.Fprintln(w, "  version     Print version information")
}

func runServe(args []string, stdout, stderr io.Writer) int {
	defaultAddr := "127.0.0.1"
	if env := os.Getenv("CORDBRIEF_WEB_ADDR"); env != "" {
		defaultAddr = env
	} else if env := os.Getenv("CORDBRIEF_HTTP_ADDR"); env != "" {
		defaultAddr = env
	} else if os.Getenv("CORDBRIEF_DATA_DIR") != "" || os.Getenv("CORDBRIEF_EXCHANGE_DIR") != "" {
		// Inside Docker container, bind to 0.0.0.0 so host port mapping (127.0.0.1:28741:28741) can forward traffic
		defaultAddr = "0.0.0.0"
	}

	defaultPort := config.DefaultCorePort
	if env := os.Getenv("CORDBRIEF_WEB_PORT"); env != "" {
		if p, err := strconv.Atoi(env); err == nil && p > 0 {
			defaultPort = p
		}
	} else if env := os.Getenv("CORDBRIEF_HTTP_PORT"); env != "" {
		if p, err := strconv.Atoi(env); err == nil && p > 0 {
			defaultPort = p
		}
	}

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addrFlag := fs.String("addr", defaultAddr, "HTTP listen address")
	portFlag := fs.Int("port", defaultPort, "HTTP listen port")
	cfgPath := fs.String("config", "config.json", "path to configuration file")
	exchangeDir := fs.String("exchange-dir", "", "path to exchange directory")
	dataDir := fs.String("data-dir", "", "path to data directory")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	actualDataDir := getDataDir(*dataDir)
	actualExchangeDir := getExchangeDir(*exchangeDir)

	// Acquire cross-process commit lock so standalone CLI commands cannot race with serve daemon.
	// Acquired before initializing store, workers, or scheduler to guarantee fail-closed behavior on lock contention.
	commitLock, lockErr := journal.AcquireCommitLock(actualDataDir)
	if lockErr != nil {
		fmt.Fprintf(stderr, "cannot start serve: %v\n", lockErr)
		return 1
	}
	defer commitLock.Release()

	store, err := config.NewStore(actualDataDir, *cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "error initializing config store: %v\n", err)
		return 1
	}

	deliveryService, err := delivery.NewService(delivery.ServiceOptions{
		DataDir:     actualDataDir,
		ExchangeDir: actualExchangeDir,
		Store:       store,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error initializing delivery service: %v\n", err)
		return 1
	}
	defer deliveryService.Close()

	digestLock := &sync.Mutex{}
	runner := &scheduler.CoreDigestRunner{
		ExchangeDir: actualExchangeDir,
		DataDir:     actualDataDir,
		Store:       store,
		DeliveryPreparer: func(batchID string, targetCur journal.Cursor, req *digest.DeliveryRequest) error {
			_, err := deliveryService.PrepareDeliveryIntent(batchID, targetCur, req)
			return err
		},
		DeliveryPromoter: func(batchID string) error {
			_, err := deliveryService.PromoteDeliveryIntent(batchID)
			return err
		},
	}

	sched, err := scheduler.NewService(scheduler.ServiceOptions{
		Store:            store,
		DataDir:          actualDataDir,
		Runner:           runner,
		DeliveryEnqueuer: deliveryService,
		Clock:            time.Now,
		CheckInterval:    15 * time.Second,
		Lock:             digestLock,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error initializing scheduler service: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Start(ctx)
	go deliveryService.StartWorker(ctx)

	server, err := web.NewServer(web.ServerOptions{
		ExchangeDir:     actualExchangeDir,
		DataDir:         actualDataDir,
		Store:           store,
		Scheduler:       sched,
		DeliveryService: deliveryService,
		Lock:            digestLock,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error initializing web server: %v\n", err)
		return 1
	}

	listenAddr := fmt.Sprintf("%s:%d", *addrFlag, *portFlag)
	fmt.Fprintf(stdout, "Starting CordBrief Setup Control Plane & Daily Scheduler on http://%s\n", listenAddr)
	fmt.Fprintf(stdout, "Exchange directory: %s\n", actualExchangeDir)
	fmt.Fprintf(stdout, "Data directory:     %s\n", actualDataDir)

	httpServer := NewHTTPServer(listenAddr, server)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Fprintln(stdout, "\nShutting down CordBrief server and scheduler...")
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "server error: %v\n", err)
		return 1
	}
	return 0
}

// MaxProviderTimeout is the maximum legitimate provider execution timeout in CordBrief (180s for local LLM).
const MaxProviderTimeout = time.Duration(config.DefaultLocalTimeout) * time.Second

// HTTPServerWriteMargin is the safety margin added to MaxProviderTimeout to ensure
// synchronous web requests handling LLM triggers are not severed prematurely.
const HTTPServerWriteMargin = 60 * time.Second

// DefaultHTTPServerWriteTimeout is the derived WriteTimeout: 180s + 60s = 240s.
const DefaultHTTPServerWriteTimeout = MaxProviderTimeout + HTTPServerWriteMargin

func init() {
	if DefaultHTTPServerWriteTimeout < MaxProviderTimeout+HTTPServerWriteMargin || DefaultHTTPServerWriteTimeout < 240*time.Second {
		panic("DefaultHTTPServerWriteTimeout must be at least 240s and exceed MaxProviderTimeout")
	}
}

// NewHTTPServer constructs an http.Server configured with bounded timeouts and header limits.
// WriteTimeout is explicitly derived from MaxProviderTimeout + HTTPServerWriteMargin (240s)
// to prevent premature HTTP connection drops during legitimate synchronous LLM operations.
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      DefaultHTTPServerWriteTimeout,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB
	}
}

func runLockProbe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lock-probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "path to data directory")
	holdSeconds := fs.Int("hold", 0, "seconds to hold lock before releasing (0 = probe and release)")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	actualDataDir := getDataDir(*dataDir)
	lock, err := journal.AcquireCommitLock(actualDataDir)
	if err != nil {
		fmt.Fprintf(stderr, "lock-probe: acquisition refused: %v\n", err)
		return 1
	}
	defer lock.Release()

	if *holdSeconds > 0 {
		fmt.Fprintf(stdout, "lock-probe: acquired, holding for %ds\n", *holdSeconds)
		time.Sleep(time.Duration(*holdSeconds) * time.Second)
		fmt.Fprintln(stdout, "lock-probe: released")
	} else {
		fmt.Fprintln(stdout, "lock-probe: acquired successfully")
	}
	return 0
}

func runMigrate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "dry run without modifying artifacts")
	exchangeDir := fs.String("exchange-dir", "", "path to exchange directory")
	dataDir := fs.String("data-dir", "", "path to data directory")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	actualExchangeDir := getExchangeDir(*exchangeDir)
	actualDataDir := getDataDir(*dataDir)

	{
		commitLock, err := journal.AcquireCommitLock(actualDataDir)
		if err != nil {
			fmt.Fprintf(stderr, "migrate: acquisition refused: %v\n", err)
			return 1
		}
		defer commitLock.Release()
	}

	reports, err := digest.MigrateArtifacts(actualExchangeDir, actualDataDir, *dryRun)
	if err != nil {
		fmt.Fprintf(stderr, "migration error: %v\n", err)
		return 1
	}

	modeLabel := "LIVE MIGRATION"
	if *dryRun {
		modeLabel = "DRY-RUN MIGRATION"
	}
	fmt.Fprintf(stdout, "=== Artifact SourceRef %s (Count: %d) ===\n", modeLabel, len(reports))
	allEligible := true
	for _, r := range reports {
		fmt.Fprintf(stdout, "Batch ID:          %s\n", r.BatchID)
		fmt.Fprintf(stdout, "  Cited Sources:   %d\n", r.SourceCount)
		fmt.Fprintf(stdout, "  Resolved:        %d\n", r.ResolvedCount)
		fmt.Fprintf(stdout, "  Eligible:        %t\n", r.Eligible)
		fmt.Fprintf(stdout, "  AlreadyMigrated: %t\n", r.AlreadyMigrated)
		if !r.Eligible {
			allEligible = false
		}
	}
	if !allEligible {
		fmt.Fprintln(stderr, "error: one or more artifacts could not be fully resolved")
		return 1
	}
	fmt.Fprintf(stdout, "=== %s COMPLETE (All Eligible: true) ===\n", modeLabel)
	return 0
}
