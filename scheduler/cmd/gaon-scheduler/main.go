// Command gaon-scheduler binds unbound Machines to Nodes through the resource API.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/scheduler/internal/scheduler"
)

func main() {
	root := flag.String("api", "", "group and version root of the resource API, e.g. http://127.0.0.1:8080/apis/compute.gaon.garam.sh/v1alpha1")
	interval := flag.Duration("interval", 5*time.Second, "time between scheduling passes")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *root == "" || *interval <= 0 {
		log.Error("start", "err", "-api is required and -interval must be positive")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := api.NewClient(*root, &http.Client{Timeout: 30 * time.Second})
	scheduler.NewService(client, log).Run(ctx, *interval)
}
