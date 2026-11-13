// Command gaon-gateway-proxy serves the listeners and the routes of one
// Gateway.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/garamsh/gaon/gateway/internal/proxy"
	"github.com/garamsh/gaon/lib/api"
)

// tokenPath is where the Node Agent writes the MicroVM's ServiceAccount token
// (ADR 0029).
const tokenPath = "/var/run/secrets/gaon.garam.sh/serviceaccount/token"

// retryInterval is the wait before the proxy lists again after losing the API
// server.
const retryInterval = 2 * time.Second

// config is what the proxy is started with.
type config struct {
	gateway   string
	server    string
	caFile    string
	tokenFile string
}

// parseFlags reads the flags in args. It returns flag.ErrHelp when usage was
// asked for, after writing it to out.
func parseFlags(args []string, out io.Writer) (config, error) {
	fs := flag.NewFlagSet("gaon-gateway-proxy", flag.ContinueOnError)
	fs.SetOutput(out)
	gateway := fs.String("gateway", "", "name of the Gateway to serve")
	server := fs.String("server", "", "base URL of the API server, such as https://127.0.0.1:8443")
	caFile := fs.String("tls-ca", "", "file of the certificate authority that signed the API server's certificate")
	tokenFile := fs.String("token-file", tokenPath, "file of the ServiceAccount token")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if *gateway == "" || *server == "" || *caFile == "" {
		return config{}, errors.New("flags -gateway, -server and -tls-ca are required")
	}
	return config{gateway: *gateway, server: *server, caFile: *caFile, tokenFile: *tokenFile}, nil
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

	if err := run(ctx, cfg, log); err != nil && ctx.Err() == nil {
		log.Error("stopped", "error", err)
		os.Exit(1)
	}
}

// run serves the Gateway until ctx ends.
func run(ctx context.Context, cfg config, log *slog.Logger) error {
	httpClient, err := api.NewServerTLSClient(cfg.caFile)
	if err != nil {
		return err
	}
	p := proxy.New(proxy.Config{Gateway: cfg.gateway}, log)
	defer p.Close()
	src := &proxy.Source{
		Server:    cfg.server,
		HTTP:      httpClient,
		TokenFile: cfg.tokenFile,
		Retry:     retryInterval,
		Log:       log,
		Proxy:     p,
	}
	return src.Run(ctx)
}
