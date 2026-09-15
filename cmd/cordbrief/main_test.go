package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestNoninteractiveStartup(t *testing.T) {
	for _, mode := range []string{"normal", "setup"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStartupHelper$")
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				switch key {
				case "TELEGRAM_BOT_TOKEN", "TELEGRAM_OWNER_ID", "DISCORD_TOKEN", "LLM_API_KEY", "CORDBRIEF_DATA_DIR", "CORDBRIEF_STARTUP_TEST":
					continue
				}
				cmd.Env = append(cmd.Env, entry)
			}
			cmd.Env = append(cmd.Env, "CORDBRIEF_DATA_DIR="+t.TempDir(), "CORDBRIEF_STARTUP_TEST="+mode)
			out, err := cmd.CombinedOutput()
			if err == nil || ctx.Err() != nil || !strings.Contains(string(out), "--setup") {
				t.Fatalf("startup did not fail promptly with instructions: %s (%v)", out, err)
			}
		})
	}
}

func TestStartupHelper(t *testing.T) {
	mode := os.Getenv("CORDBRIEF_STARTUP_TEST")
	if mode == "" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("cordbrief", flag.ExitOnError)
	os.Args = []string{"cordbrief"}
	if mode == "setup" {
		os.Args = append(os.Args, "--setup")
	}
	main()
}
