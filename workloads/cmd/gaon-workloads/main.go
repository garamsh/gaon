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
	leader     *api.LeaderElectionFlags
	clock      job.Clock
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
	leader := api.RegisterLeaderElectionFlags(fs, "gaon-workloads")
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
	return config{server: strings.TrimSuffix(*server, "/"), httpClient: httpClient, jobs: job.Config{Interval: *interval}, leader: leader, clock: systemclock.Clock{}}, nil
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

	if err := run(ctx, cfg, log); err != nil {
		log.Error("stopped", "error", err)
		os.Exit(1)
	}
}

// run runs the Jobs while this process holds the coordination Lease, or at once
// when leader election is off. Each term builds a service of its own, so that a
// process that lost the Lease and took it again registers and sweeps from the
// Store's state.
func run(ctx context.Context, cfg config, log *slog.Logger) error {
	computeRoot := cfg.server + "/apis/" + api.GroupVersion
	elector, err := cfg.leader.Elector(computeRoot, cfg.httpClient)
	if err != nil {
		return err
	}
	jobs, err := api.NewCustomResourceClient(cfg.server, cfg.httpClient, job.Type)
	if err != nil {
		return err
	}
	cronjobs, err := api.NewCustomResourceClient(cfg.server, cfg.httpClient, job.CronJobType)
	if err != nil {
		return err
	}
	deployments, err := api.NewCustomResourceClient(cfg.server, cfg.httpClient, job.DeploymentType)
	if err != nil {
		return err
	}
	definitions := api.NewClient(cfg.server+"/apis/"+api.APIExtensionsGroupVersion, cfg.httpClient)
	managed := api.Selector{{Key: job.ManagedByLabel, Operator: api.SelectorIn, Values: []string{job.Manager}}}
	microvms := api.NewClient(computeRoot, cfg.httpClient, api.WithLabelSelector(managed))
	return api.RunLeading(ctx, elector, func(ctx context.Context) error {
		return job.NewService(definitions, jobs, cronjobs, deployments, microvms, cfg.clock, cfg.jobs, log).Run(ctx)
	})
}
