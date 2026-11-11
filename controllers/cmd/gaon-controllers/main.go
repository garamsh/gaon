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
	"time"

	"github.com/garamsh/gaon/controllers/internal/nodelifecycle"
	"github.com/garamsh/gaon/controllers/internal/nodelifecycle/systemclock"
	"github.com/garamsh/gaon/lib/api"
)

func main() {
	root := flag.String("api", "", "root URL of the compute.gaon.garam.sh/v1alpha1 resource API")
	margin := flag.Duration("lease-margin", 5*time.Second, "time added to a Lease's duration before it counts as expired")
	duration := flag.Duration("default-lease-duration", 30*time.Second, "validity, before the margin, of a Node whose Lease has not been seen")
	flag.Parse()

	log := slog.Default()
	if *root == "" || *margin <= 0 || *duration <= 0 {
		log.Error("flag -api is required, and -lease-margin and -default-lease-duration must be above zero")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc := nodelifecycle.NewService(api.NewClient(*root, nil), systemclock.Clock{}, nodelifecycle.Config{Margin: *margin, DefaultLeaseDuration: *duration}, log)
	if err := svc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("node lifecycle controller stopped", "error", err)
		os.Exit(1)
	}
}
