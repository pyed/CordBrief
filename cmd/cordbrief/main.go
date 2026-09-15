package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata"

	"github.com/pyed/CordBrief/internal/bot"
	"github.com/pyed/CordBrief/internal/state"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	setup := flag.Bool("setup", false, "configure saved credentials and exit (interactive terminal required)")
	dceVersion := flag.String("dce-version", "", "pin an official DCE release tag, or latest to resume automatic updates")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("unexpected arguments; use --help (credentials are entered only through --setup or environment variables)")
	}

	env, err := bot.LoadEnv()
	if *setup || (errors.Is(err, bot.ErrCredentialsMissing) && bot.Interactive()) {
		if err := bot.Configure(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Credentials saved. Environment variables still override saved values.")
		if *setup {
			return
		}
		env, err = bot.LoadEnv()
	}
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	if *dceVersion != "" {
		env.DCEVersion = *dceVersion
	}
	log.Println("CordBrief starting; preparing DiscordChatExporter if needed...")

	store := state.NewStore(env.DataDir)

	// Verify or initialize configuration and state stores fail-closed
	if _, err := store.LoadConfig(); err != nil {
		log.Fatalf("failed to initialize config store: %v", err)
	}
	if _, err := store.LoadState(); err != nil {
		log.Fatalf("failed to initialize state store: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	appBot, err := bot.New(ctx, env, store)
	if err != nil {
		log.Fatalf("failed to initialize telegram bot: %v", err)
	}

	log.Printf("Telegram bot initialized (data directory: %s)", env.DataDir)
	if appBot.DCEManager() != nil {
		log.Printf("Discord exporter: %s", appBot.DCEManager().Status())
	} else {
		log.Println("Discord exporter not configured (DISCORD_TOKEN missing; run --setup)")
	}
	if env.LLMAPIKey != "" {
		log.Println("LLM provider configured")
	} else {
		log.Println("LLM API key unset (only unauthenticated endpoints will work)")
	}

	log.Println("Starting Telegram long polling...")
	appBot.Start()

	log.Println("Shutting down CordBrief...")
	if r := appBot.Runner(); r != nil {
		log.Println("Waiting for active brief to complete...")
		r.Wait()
	}
	log.Println("CordBrief shutdown complete.")
}
