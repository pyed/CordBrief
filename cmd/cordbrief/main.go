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

	appBot, err := bot.New(env, store)
	if err != nil {
		log.Fatalf("failed to initialize telegram bot: %v", err)
	}

	log.Printf("Telegram bot initialized for owner ID %d (data directory: %s)", env.OwnerID, env.DataDir)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Println("Starting Telegram long polling...")
	appBot.Start(ctx)

	log.Println("Shutting down CordBrief...")
}
