package storagelocal

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
	"github.com/garamsh/mvmd/lib/runtimeapi"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Environment variables, each the fallback of the flag named beside it. They
// carry the names the Node Agent's variables of the same flags have, so one
// environment can configure both.
const (
	EnvPluginDir    = "GAON_PLUGIN_DIR"
	EnvAPIServer    = "GAON_API_SERVER"
	EnvNodeName     = "GAON_NODE_NAME"
	EnvMvmdSocket   = "GAON_MVMD_SOCKET"
	EnvResyncPeriod = "GAON_RESYNC_PERIOD"
	EnvTLSCA        = "GAON_TLS_CA"
	EnvTLSCert      = "GAON_TLS_CERT"
	EnvTLSKey       = "GAON_TLS_KEY"
	EnvCertDir      = "GAON_CERT_DIR"
)

// PairFile is the file in --cert-dir that holds the Node Agent's client
// certificate and then its key, which the plugin presents to the API server as
// the Node. The agent renews it by atomic rename, and the plugin reads it at
// each new connection.
const PairFile = "node-agent.pem"

const defaultResyncPeriod = 10 * time.Second

// Config is what the plugin is started with.
type Config struct {
	// PluginDir is the socket directory the plugin serves in.
	PluginDir string
	// APIServer is the root of the resource API, as the Node Agent's.
	APIServer string
	// NodeName is the Node whose local Volumes the plugin serves.
	NodeName string
	// MvmdSocket is the path of mvmd's runtime API Unix socket.
	MvmdSocket string
	// ResyncPeriod is the time between two reconciliations of the Volumes.
	ResyncPeriod time.Duration
	// CertDir, when set, is the Node Agent's --cert-dir: the plugin reaches
	// APIServer with the pair in its PairFile, trusting only the CAs in
	// TLSCA. Otherwise TLS is the three files of lib/api, or none.
	CertDir string
	TLS     api.TLSFiles
}

// ParseConfig reads the flags in args. A flag not given falls back to its
// environment variable, read through getenv, and then to its default. It
// returns flag.ErrHelp when usage was asked for, after writing it to out.
func ParseConfig(args []string, getenv func(string) string, out io.Writer) (Config, error) {
	var c Config
	var resync string
	fs := flag.NewFlagSet("gaon-storage-local", flag.ContinueOnError)
	fs.SetOutput(out)
	str := func(p *string, name, env, def, usage string) {
		d := getenv(env)
		if d == "" {
			d = def
		}
		fs.StringVar(p, name, d, usage+" (env "+env+")")
	}
	str(&c.PluginDir, "plugin-dir", EnvPluginDir, plugin.DefaultDir, "socket directory the plugin serves in")
	str(&c.APIServer, "api-server", EnvAPIServer, "", "root URL of the resource API, up to the group and version")
	str(&c.NodeName, "node-name", EnvNodeName, "", "name of this node's Node, whose local Volumes the plugin serves")
	str(&c.MvmdSocket, "mvmd-socket", EnvMvmdSocket, "", "path of mvmd's runtime API Unix socket")
	str(&resync, "resync-period", EnvResyncPeriod, defaultResyncPeriod.String(), "time between two reconciliations of the Volumes")
	str(&c.CertDir, "cert-dir", EnvCertDir, "", "the Node Agent's --cert-dir: present the certificate the agent keeps there")
	str(&c.TLS.CA, "tls-ca", EnvTLSCA, "", "file of the CA certificates the API server's certificate must chain to")
	str(&c.TLS.Cert, "tls-cert", EnvTLSCert, "", "file of the client certificate presented to the API server")
	str(&c.TLS.Key, "tls-key", EnvTLSKey, "", "file of the key of --tls-cert")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	d, err := time.ParseDuration(resync)
	if err != nil {
		return Config{}, fmt.Errorf("resync period: %w", err)
	}
	if d <= 0 {
		return Config{}, fmt.Errorf("resync period is %s, want a positive duration", d)
	}
	c.ResyncPeriod = d
	for _, f := range []struct{ name, env, value string }{
		{"api-server", EnvAPIServer, c.APIServer},
		{"node-name", EnvNodeName, c.NodeName},
		{"mvmd-socket", EnvMvmdSocket, c.MvmdSocket},
	} {
		if f.value == "" {
			return Config{}, fmt.Errorf("--%s or %s is required", f.name, f.env)
		}
	}
	if c.CertDir != "" {
		if c.TLS.Cert != "" || c.TLS.Key != "" {
			return Config{}, errors.New("--cert-dir and --tls-cert or --tls-key are mutually exclusive")
		}
		if c.TLS.CA == "" {
			return Config{}, errors.New("--cert-dir needs --tls-ca")
		}
	}
	return c, nil
}

// httpClient returns the client of the resource API the plugin acts through.
func (c Config) httpClient() (*http.Client, error) {
	if c.CertDir == "" {
		return c.TLS.Client()
	}
	return api.NewTLSClientFunc(c.TLS.CA, nodePair(filepath.Join(c.CertDir, PairFile)))
}

// nodePair returns a function that reads the Node's certificate and key from
// the file at each handshake, so a pair the agent renews is presented by the
// connections made after the renewal. A missing or unusable file fails the
// handshake, and the call that needed it is retried.
func nodePair(path string) func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read the node's certificate: %w", err)
		}
		pair, err := tls.X509KeyPair(data, data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		return &pair, nil
	}
}

// Run serves the plugin named Name in cfg.PluginDir, and reconciles the
// Volumes of its Node, until ctx ends. It returns an error when it cannot
// start.
func Run(ctx context.Context, cfg Config, log *slog.Logger) error {
	httpClient, err := cfg.httpClient()
	if err != nil {
		return err
	}
	conn, err := runtimeapi.Dial(ctx, cfg.MvmdSocket)
	if err != nil {
		return fmt.Errorf("dial mvmd at %s: %w", cfg.MvmdSocket, err)
	}
	defer func() { _ = conn.Close() }()
	client := api.NewClient(cfg.APIServer, httpClient)
	return serve(ctx, cfg, NewLocal(cfg.NodeName, cfg.ResyncPeriod, client, client, v1alpha2.NewRuntimeServiceClient(conn), SystemSleeper{}, log))
}

// serve runs the loop of l beside its server.
func serve(ctx context.Context, cfg Config, l *Local) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); l.Run(ctx) }()
	err := plugin.ServeStorage(ctx, cfg.PluginDir, Name, l)
	cancel()
	<-done
	return err
}
