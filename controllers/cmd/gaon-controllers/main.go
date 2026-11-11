// Command gaon-controllers runs gaon's controllers against the resource API.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/garamsh/gaon/controllers/internal/nodelifecycle"
	"github.com/garamsh/gaon/controllers/internal/nodelifecycle/systemclock"
	"github.com/garamsh/gaon/lib/api"
)

func main() {
	root := flag.String("api", "", "root URL of the compute.gaon.garam.sh/v1alpha1 resource API")
	margin := flag.Duration("lease-margin", 0, "time added to a Lease's duration before it counts as expired")
	flag.Parse()

	log := slog.Default()
	if *root == "" || *margin <= 0 {
		log.Error("flags -api and -lease-margin are required, the margin above zero")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc := nodelifecycle.NewService(api.NewClient(*root, nil), systemclock.Clock{}, nodelifecycle.Config{Margin: *margin}, log)
	if err := svc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("node lifecycle controller stopped", "error", err)
		os.Exit(1)
	}
}
