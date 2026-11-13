// Command gaon-gateway-controller registers gaon's gateway kinds and serves
// them through the resource API.
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

	"github.com/garamsh/gaon/gateway/internal/gateway"
	"github.com/garamsh/gaon/lib/api"
)

// config is what the unit is started with.
type config struct {
	server     string
	httpClient *http.Client
	gateway    gateway.Config
	leader     *api.LeaderElectionFlags
}

// parseFlags reads the flags in args and builds the HTTP client the --tls
// flags describe. It returns flag.ErrHelp when usage was asked for, after
// writing it to out.
func parseFlags(args []string, out io.Writer) (config, error) {
	fs := flag.NewFlagSet("gaon-gateway-controller", flag.ContinueOnError)
	fs.SetOutput(out)
	server := fs.String("server", "", "base URL of the API server, such as https://127.0.0.1:8443")
	interval := fs.Duration("interval", 2*time.Second, "time between the end of one sweep and the start of the next")
	image := fs.String("proxy-image", "", "image of the proxy each Gateway's Deployment runs")
	proxyServer := fs.String("proxy-server", "", "API server address the proxies are given; default is -server")
	proxyCA := fs.String("proxy-ca-configmap", "", "ConfigMap whose key ca.crt is the CA the proxies trust")
	proxyAccount := fs.String("proxy-service-account", "gaon-gateway-proxy", "ServiceAccount the proxies run as")
	files := api.RegisterTLSFlags(fs)
	leader := api.RegisterLeaderElectionFlags(fs, gateway.Manager)
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if *server == "" || *interval <= 0 || *image == "" || *proxyCA == "" {
		return config{}, errors.New("flags -server, -proxy-image and -proxy-ca-configmap are required, and -interval must be above zero")
	}
	httpClient, err := files.Client()
	if err != nil {
		return config{}, err
	}
	root := strings.TrimSuffix(*server, "/")
	if *proxyServer == "" {
		*proxyServer = root
	}
	proxy := gateway.ProxyConfig{Image: *image, Server: *proxyServer, CAConfigMap: *proxyCA, ServiceAccount: *proxyAccount}
	return config{server: root, httpClient: httpClient, gateway: gateway.Config{Interval: *interval, Proxy: proxy}, leader: leader}, nil
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

// run serves the gateway kinds while this process holds the coordination Lease,
// or at once when leader election is off. Each term builds a service of its own,
// so that a process that lost the Lease and took it again registers and sweeps
// from the stored state.
func run(ctx context.Context, cfg config, log *slog.Logger) error {
	computeRoot := cfg.server + "/apis/" + api.GroupVersion
	elector, err := cfg.leader.Elector(computeRoot, cfg.httpClient)
	if err != nil {
		return err
	}
	var clients [4]*api.CustomResourceClient
	for i, t := range []api.CustomResourceType{gateway.GatewayType, gateway.HTTPRouteType, gateway.TLSRouteType, gateway.DeploymentType} {
		if clients[i], err = api.NewCustomResourceClient(cfg.server, cfg.httpClient, t); err != nil {
			return err
		}
	}
	definitions := api.NewClient(cfg.server+"/apis/"+api.APIExtensionsGroupVersion, cfg.httpClient)
	services := api.NewClient(computeRoot, cfg.httpClient)
	return api.RunLeading(ctx, elector, func(ctx context.Context) error {
		return gateway.NewService(definitions, clients[0], clients[1], clients[2], clients[3], services, cfg.gateway, log).Run(ctx)
	})
}
