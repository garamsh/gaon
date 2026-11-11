// Command gaon-apiserver serves the v1alpha1 resource API over etcd.
package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/garamsh/gaon/apiserver"
)

func main() {
	log := slog.Default()
	if err := run(log); err != nil {
		log.Error("gaon-apiserver stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := apiserver.ParseConfig(os.Args[1:])
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("serving the resource API", "address", ln.Addr().String(), "etcd", cfg.Endpoints)
	return apiserver.Run(ctx, cfg, ln, log)
}
