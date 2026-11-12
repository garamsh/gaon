// Command gaon-workloads registers gaon's workload kinds and runs them through
// the resource API.
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
	"strings"
	"syscall"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/workloads/internal/job"
	"github.com/garamsh/gaon/workloads/internal/job/systemclock"
)

// config is what the unit is started with.
type config struct {
	server     string
	httpClient *http.Client
	jobs       job.Config
}

// parseFlags reads the flags in args and builds the HTTP client the --tls
// flags describe. It returns flag.ErrHelp when usage was asked for, after
// writing it to out.
func parseFlags(args []string, out io.Writer) (config, error) {
	fs := flag.NewFlagSet("gaon-workloads", flag.ContinueOnError)
	fs.SetOutput(out)
	server := fs.String("server", "", "base URL of the API server, such as https://127.0.0.1:8443")
	interval := fs.Duration("interval", 2*time.Second, "time between the end of one sweep over the Jobs and the start of the next")
	files := api.RegisterTLSFlags(fs)
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if *server == "" || *interval <= 0 {
		return config{}, errors.New("flag -server is required, and -interval must be above zero")
	}
	httpClient, err := files.Client()
	if err != nil {
		return config{}, err
	}
	return config{server: strings.TrimSuffix(*server, "/"), httpClient: httpClient, jobs: job.Config{Interval: *interval}}, nil
}

func main() {
	log := slog.Default()
	cfg, err := parseFlags(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Error("start", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	jobs, err := api.NewCustomResourceClient(cfg.server, cfg.httpClient, job.Type)
	if err != nil {
		log.Error("start", "error", err)
		os.Exit(2)
	}
	definitions := api.NewClient(cfg.server+"/apis/"+api.APIExtensionsGroupVersion, cfg.httpClient)
	managed := api.Selector{{Key: job.ManagedByLabel, Operator: api.SelectorIn, Values: []string{job.Manager}}}
	microvms := api.NewClient(cfg.server+"/apis/"+api.GroupVersion, cfg.httpClient, api.WithLabelSelector(managed))
	svc := job.NewService(definitions, jobs, microvms, systemclock.Clock{}, cfg.jobs, log)
	if err := svc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("stopped", "error", err)
		os.Exit(1)
	}
}
