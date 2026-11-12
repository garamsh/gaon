// Command gaon-storage-local serves the storage plugin "local" to the Node
// Agent on a Unix socket.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	storagelocal "github.com/garamsh/gaon/storage-local"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := storagelocal.ParseConfig(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Error("configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := storagelocal.Run(ctx, cfg, log); err != nil {
		log.Error("run", "error", err)
		os.Exit(1)
	}
}
