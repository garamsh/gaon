// Command gaon-network-default serves the network plugin "default" to the
// Node Agent on a Unix socket.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	networkdefault "github.com/garamsh/gaon/network-default"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := networkdefault.ParseConfig(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Error("configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := networkdefault.Run(ctx, cfg); err != nil {
		log.Error("run", "error", err)
		os.Exit(1)
	}
}
