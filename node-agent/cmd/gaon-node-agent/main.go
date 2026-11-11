// Command gaon-node-agent runs the Machines bound to its node on mvmd.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	nodeagent "github.com/garamsh/gaon/node-agent"
	"github.com/garamsh/gaon/node-agent/internal/clock"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := nodeagent.ParseConfig(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Error("configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := nodeagent.Run(ctx, cfg, clock.System{}, log); err != nil {
		log.Error("run", "error", err)
		os.Exit(1)
	}
}
