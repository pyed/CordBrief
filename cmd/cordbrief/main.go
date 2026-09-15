package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/pyed/CordBrief/internal/bot"
	"github.com/pyed/CordBrief/internal/state"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.Println("CordBrief v3 starting...")

	env, err := bot.LoadEnv()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

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
	if env.DCEPath != "" && env.DiscordToken != "" {
		log.Printf("Discord exporter configured (DCE binary: %s)", env.DCEPath)
	} else {
		log.Println("Discord exporter not configured (DISCORD_TOKEN or CORDBRIEF_DCE_PATH missing)")
	}
	if env.LLMAPIKey != "" {
		log.Println("LLM provider configured")
	} else {
		log.Println("LLM provider not configured (LLM_API_KEY missing)")
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
