// Command gaon-scheduler binds unbound MicroVMs to Nodes through the resource API.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/scheduler/internal/scheduler"
)

// config is what the scheduler is started with.
type config struct {
	root       string
	interval   time.Duration
	httpClient *http.Client
	scheduler  scheduler.Config
}

// parseFlags reads the flags in args and builds the HTTP client the --tls
// flags describe. It returns flag.ErrHelp when usage was asked for, after
// writing it to out.
func parseFlags(args []string, out io.Writer) (config, error) {
	fs := flag.NewFlagSet("gaon-scheduler", flag.ContinueOnError)
	fs.SetOutput(out)
	root := fs.String("api", "", "group and version root of the resource API, e.g. http://127.0.0.1:8080/apis/compute.gaon.garam.sh/v1alpha1")
	interval := fs.Duration("interval", 5*time.Second, "time between scheduling passes")
	configFile := fs.String("config", "", "scheduler configuration file; with none, no score plugin and no extender is enabled")
	files := api.RegisterTLSFlags(fs)
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if *root == "" || *interval <= 0 {
		return config{}, errors.New("-api is required and -interval must be positive")
	}
	httpClient, err := files.Client()
	if err != nil {
		return config{}, err
	}
	httpClient.Timeout = 30 * time.Second
	var sc scheduler.Config
	if *configFile != "" {
		if sc, err = scheduler.LoadConfig(*configFile); err != nil {
			return config{}, err
		}
	}
	return config{root: *root, interval: *interval, httpClient: httpClient, scheduler: sc}, nil
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := parseFlags(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Error("start", "err", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := api.NewClient(cfg.root, cfg.httpClient)
	scheduler.NewService(client, log, scheduler.WithConfig(cfg.scheduler)).Run(ctx, cfg.interval)
}
