// Command gaon-controllers runs gaon's controllers against the resource API.
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

	"github.com/garamsh/gaon/controllers/internal/nodelifecycle"
	"github.com/garamsh/gaon/controllers/internal/nodelifecycle/systemclock"
	"github.com/garamsh/gaon/lib/api"
)

// config is what the controllers are started with.
type config struct {
	root       string
	httpClient *http.Client
	lifecycle  nodelifecycle.Config
}

// parseFlags reads the flags in args and builds the HTTP client the --tls
// flags describe. It returns flag.ErrHelp when usage was asked for, after
// writing it to out.
func parseFlags(args []string, out io.Writer) (config, error) {
	fs := flag.NewFlagSet("gaon-controllers", flag.ContinueOnError)
	fs.SetOutput(out)
	root := fs.String("api", "", "root URL of the compute.gaon.garam.sh/v1alpha1 resource API")
	margin := fs.Duration("lease-margin", 5*time.Second, "time added to a Lease's duration before it counts as expired")
	duration := fs.Duration("default-lease-duration", 30*time.Second, "validity, before the margin, of a Node whose Lease has not been seen")
	files := api.RegisterTLSFlags(fs)
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if *root == "" || *margin <= 0 || *duration <= 0 {
		return config{}, errors.New("flag -api is required, and -lease-margin and -default-lease-duration must be above zero")
	}
	httpClient, err := files.Client()
	if err != nil {
		return config{}, err
	}
	return config{
		root:       *root,
		httpClient: httpClient,
		lifecycle:  nodelifecycle.Config{Margin: *margin, DefaultLeaseDuration: *duration},
	}, nil
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

	svc := nodelifecycle.NewService(api.NewClient(cfg.root, cfg.httpClient), systemclock.Clock{}, cfg.lifecycle, log)
	if err := svc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("node lifecycle controller stopped", "error", err)
		os.Exit(1)
	}
}
