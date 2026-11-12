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
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/garamsh/gaon/controllers/internal/csrsigner"
	"github.com/garamsh/gaon/controllers/internal/garbagecollector"
	"github.com/garamsh/gaon/controllers/internal/nodelifecycle"
	"github.com/garamsh/gaon/controllers/internal/nodelifecycle/systemclock"
	"github.com/garamsh/gaon/lib/api"
)

// config is what the controllers are started with.
type config struct {
	root       string
	httpClient *http.Client
	lifecycle  nodelifecycle.Config
	// server is the address of the API server, taken from root. It is empty
	// when root does not end in the compute group's path: the garbage
	// collector, which needs the other groups' roots, does not run.
	server    string
	collector garbagecollector.Config
	// signer is nil when no signing files were given: the signer does not run.
	signer *csrsigner.Config
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
	signingCert := fs.String("signing-cert", "", "PEM file of the cluster CA certificate that signs CertificateSigningRequests")
	signingKey := fs.String("signing-key", "", "PEM file of the cluster CA key; given with -signing-cert or not at all")
	signingDuration := fs.Duration("signing-duration", 8760*time.Hour, "validity of a certificate the signer issues")
	gcInterval := fs.Duration("gc-interval", 30*time.Second, "time between the end of one garbage collection sweep and the start of the next")
	files := api.RegisterTLSFlags(fs)
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if *root == "" || *margin <= 0 || *duration <= 0 {
		return config{}, errors.New("flag -api is required, and -lease-margin and -default-lease-duration must be above zero")
	}
	if *gcInterval <= 0 {
		return config{}, errors.New("flag -gc-interval must be above zero")
	}
	if *signingDuration <= 0 {
		return config{}, errors.New("flag -signing-duration must be above zero")
	}
	if (*signingCert == "") != (*signingKey == "") {
		return config{}, errors.New("flags -signing-cert and -signing-key are given together or not at all")
	}
	httpClient, err := files.Client()
	if err != nil {
		return config{}, err
	}
	cfg := config{
		root:       *root,
		httpClient: httpClient,
		lifecycle:  nodelifecycle.Config{Margin: *margin, DefaultLeaseDuration: *duration},
		collector:  garbagecollector.Config{Interval: *gcInterval},
	}
	if base, ok := strings.CutSuffix(strings.TrimSuffix(*root, "/"), "/apis/"+api.GroupVersion); ok {
		cfg.server = base
	}
	if *signingCert != "" {
		ca, err := csrsigner.LoadCA(*signingCert, *signingKey)
		if err != nil {
			return config{}, err
		}
		cfg.signer = &csrsigner.Config{CA: ca, Duration: *signingDuration, Now: time.Now}
	}
	return cfg, nil
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

	client := api.NewClient(cfg.root, cfg.httpClient)
	runners := map[string]func(context.Context) error{
		"node lifecycle controller": nodelifecycle.NewService(client, systemclock.Clock{}, cfg.lifecycle, log).Run,
	}
	if cfg.signer != nil {
		runners["csr signer"] = csrsigner.NewService(client, *cfg.signer, log).Run
	} else {
		log.Info("csr signer does not run: no -signing-cert and -signing-key")
	}
	if cfg.server != "" {
		runners["garbage collector"] = newCollector(cfg, log).Run
	} else {
		log.Info("garbage collector does not run: -api does not end in /apis/" + api.GroupVersion)
	}
	if !runAll(ctx, log, runners) {
		os.Exit(1)
	}
}

// newCollector builds the garbage collector for the API server at cfg.server.
func newCollector(cfg config, log *slog.Logger) garbagecollector.Service {
	definitions := api.NewClient(cfg.server+"/apis/"+api.APIExtensionsGroupVersion, cfg.httpClient)
	clientFor := func(t api.CustomResourceType) (*api.CustomResourceClient, error) {
		return api.NewCustomResourceClient(cfg.server, cfg.httpClient, t)
	}
	return garbagecollector.NewService(definitions, clientFor, systemclock.Clock{}, cfg.collector, log)
}

// runAll runs each runner until ctx ends or one fails, which ends the others.
// It reports whether none failed.
func runAll(ctx context.Context, log *slog.Logger, runners map[string]func(context.Context) error) bool {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg     sync.WaitGroup
		failed atomic.Bool
	)
	for name, run := range runners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("stopped", "controller", name, "error", err)
				failed.Store(true)
				cancel()
			}
		}()
	}
	wg.Wait()
	return !failed.Load()
}
