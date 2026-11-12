package nodeagent

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/liveness"
	"github.com/garamsh/gaon/node-agent/internal/machines"
	"github.com/garamsh/gaon/node-agent/internal/machines/storage"
)

// Environment variables, each the fallback of the flag named beside it.
const (
	EnvAPIServer      = "GAON_API_SERVER"
	EnvNodeName       = "GAON_NODE_NAME"
	EnvMvmdSocket     = "GAON_MVMD_SOCKET"
	EnvDelta          = "GAON_DELTA"
	EnvLeaseDuration  = "GAON_LEASE_DURATION"
	EnvRenewPeriod    = "GAON_RENEW_PERIOD"
	EnvDeadlineLength = "GAON_DEADLINE_LENGTH"
	EnvRPCTimeout     = "GAON_RPC_TIMEOUT"
	EnvResyncPeriod   = "GAON_RESYNC_PERIOD"
	EnvTLSCA          = "GAON_TLS_CA"
	EnvTLSCert        = "GAON_TLS_CERT"
	EnvTLSKey         = "GAON_TLS_KEY"

	EnvBootstrapTokenFile = "GAON_BOOTSTRAP_TOKEN_FILE"
	EnvCertDir            = "GAON_CERT_DIR"

	EnvRestartBackoff    = "GAON_RESTART_BACKOFF"
	EnvRestartBackoffMax = "GAON_RESTART_BACKOFF_MAX"
	EnvRestartResetAfter = "GAON_RESTART_RESET_AFTER"

	EnvNetworkPlugin = "GAON_NETWORK_PLUGIN"
	EnvStoragePlugin = "GAON_STORAGE_PLUGIN"
	EnvEgressPrefix  = "GAON_EGRESS_PREFIX"
	EnvIngressPrefix = "GAON_INGRESS_PREFIX"
)

// Config is what the agent is started with.
type Config struct {
	// APIServer is the root of the resource API, the address up to the
	// group and version, for example http://127.0.0.1:8080/apis/compute.gaon.garam.sh/v1alpha1.
	APIServer string
	// HTTPClient reaches APIServer: mutual TLS when the three --tls flags
	// are given, plain HTTP when none is. It is nil when CertDir is set.
	HTTPClient *http.Client
	// CertDir, when set, is where the agent keeps the key and certificate it
	// reaches APIServer with, trusting only the CAs in TLSCA. BootstrapToken is
	// the token it asks for a first certificate with, empty when it has none.
	CertDir        string
	TLSCA          string
	BootstrapToken string
	// NodeName names the Node and the Lease.
	NodeName string
	// MvmdSocket is the path of mvmd's runtime API Unix socket.
	MvmdSocket string
	// Liveness holds δ and the Lease and deadline durations.
	Liveness liveness.Params
	// ResyncPeriod is the time between two reconciliations of the Machines
	// and two reports of the Node's status.
	ResyncPeriod time.Duration
	// RestartBackoff is the wait before a Machine's first restart, doubled
	// with each restart up to RestartBackoffMax.
	RestartBackoff    time.Duration
	RestartBackoffMax time.Duration
	// RestartResetAfter is how long a Machine runs before the wait starts
	// again from RestartBackoff.
	RestartResetAfter time.Duration
	// Network decides the network policy each Machine is created with. It
	// is the plugin --network-plugin names.
	Network machines.Network
	// StoragePlugin names the plugin that decides the disks of each Machine,
	// from --storage-plugin. Run builds it, as it needs the API client and
	// mvmd.
	StoragePlugin string
}

// Defaults of the durations. δ's is provisional: it is not measured.
const (
	defaultDelta          = time.Second
	defaultLeaseDuration  = 30 * time.Second
	defaultRenewPeriod    = 5 * time.Second
	defaultDeadlineLength = 15 * time.Second
	defaultRPCTimeout     = 2 * time.Second
	defaultResyncPeriod   = 10 * time.Second

	// The restart durations are those resource-api.md records, provisionally.
	defaultRestartBackoff    = 10 * time.Second
	defaultRestartBackoffMax = 5 * time.Minute
	defaultRestartResetAfter = 10 * time.Minute

	defaultNetworkPlugin = "default"
	defaultStoragePlugin = storage.LocalName
)

// ParseConfig reads the flags in args. A flag not given falls back to its
// environment variable, read through getenv, and then to its default. It
// returns flag.ErrHelp when usage was asked for, after writing it to out.
func ParseConfig(args []string, getenv func(string) string, out io.Writer) (Config, error) {
	var c Config
	var errs []error
	seen := map[string]bool{}
	fs := flag.NewFlagSet("gaon-node-agent", flag.ContinueOnError)
	fs.SetOutput(out)
	str := func(p *string, name, env, usage string) {
		fs.StringVar(p, name, getenv(env), usage+" (env "+env+")")
	}
	dur := func(p *time.Duration, name, env string, def time.Duration, usage string) {
		d := def
		if v := getenv(env); v != "" {
			parsed, err := time.ParseDuration(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", env, err))
			}
			d = parsed
		}
		fs.DurationVar(p, name, d, usage+" (env "+env+")")
	}
	str(&c.APIServer, "api-server", EnvAPIServer, "root URL of the resource API, up to the group and version")
	var tlsFiles api.TLSFiles
	str(&tlsFiles.CA, "tls-ca", EnvTLSCA, "file of the CA certificates the API server's certificate must chain to")
	str(&tlsFiles.Cert, "tls-cert", EnvTLSCert, "file of the client certificate presented to the API server")
	str(&tlsFiles.Key, "tls-key", EnvTLSKey, "file of the key of --tls-cert")
	var tokenFile string
	str(&tokenFile, "bootstrap-token-file", EnvBootstrapTokenFile, "file of the bootstrap token the agent asks for its client certificate with")
	str(&c.CertDir, "cert-dir", EnvCertDir, "directory where the agent keeps its key and client certificate")
	str(&c.NodeName, "node-name", EnvNodeName, "name of this node's Node and Lease")
	str(&c.MvmdSocket, "mvmd-socket", EnvMvmdSocket, "path of mvmd's runtime API Unix socket")
	dur(&c.Liveness.Delta, "delta", EnvDelta, defaultDelta, "δ, how long a Machine can run past mvmd's recorded deadline expiry")
	dur(&c.Liveness.LeaseDuration, "lease-duration", EnvLeaseDuration, defaultLeaseDuration, "how long the Lease is valid after a renewal, in whole seconds")
	dur(&c.Liveness.RenewPeriod, "renew-period", EnvRenewPeriod, defaultRenewPeriod, "time between two renewals of the Lease and the deadline")
	dur(&c.Liveness.DeadlineLength, "deadline-length", EnvDeadlineLength, defaultDeadlineLength, "length each deadline renewal asks mvmd for")
	dur(&c.Liveness.RPCTimeout, "rpc-timeout", EnvRPCTimeout, defaultRPCTimeout, "bound on each call of a renewal")
	dur(&c.ResyncPeriod, "resync-period", EnvResyncPeriod, defaultResyncPeriod, "time between two reconciliations of the Machines")
	dur(&c.RestartBackoff, "restart-backoff", EnvRestartBackoff, defaultRestartBackoff, "wait before a Machine's first restart, doubled with each restart")
	dur(&c.RestartBackoffMax, "restart-backoff-max", EnvRestartBackoffMax, defaultRestartBackoffMax, "longest wait before a restart")
	dur(&c.RestartResetAfter, "restart-reset-after", EnvRestartResetAfter, defaultRestartResetAfter, "how long a Machine runs before the restart wait starts again from the first")
	var networkPlugin, storagePlugin string
	var egress, ingress []string
	fs.StringVar(&networkPlugin, "network-plugin", orDefault(getenv(EnvNetworkPlugin), defaultNetworkPlugin), "network plugin, which decides each Machine's network policy (env "+EnvNetworkPlugin+")")
	fs.StringVar(&storagePlugin, "storage-plugin", orDefault(getenv(EnvStoragePlugin), defaultStoragePlugin), "storage plugin, which decides each Machine's disks (env "+EnvStoragePlugin+")")
	prefixes := func(p *[]string, name, env, usage string) {
		if v := getenv(env); v != "" {
			*p = strings.Split(v, ",")
		}
		fs.Func(name, usage+", repeatable (env "+env+", comma separated)", func(v string) error {
			if len(*p) > 0 && !seen[name] {
				*p = nil
			}
			seen[name] = true
			*p = append(*p, v)
			return nil
		})
	}
	prefixes(&egress, "egress-prefix", EnvEgressPrefix, "CIDR a Machine may send to over any protocol, for the default network plugin")
	prefixes(&ingress, "ingress-prefix", EnvIngressPrefix, "CIDR a Machine's ports accept TCP from, for the default network plugin")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	if c.ResyncPeriod <= 0 {
		return Config{}, fmt.Errorf("resync period is %s, want a positive duration", c.ResyncPeriod)
	}
	if c.RestartBackoff <= 0 || c.RestartResetAfter <= 0 || c.RestartBackoffMax < c.RestartBackoff {
		return Config{}, fmt.Errorf("restart backoff is %s, its maximum %s and its reset time %s, want positive durations with the maximum not below the backoff",
			c.RestartBackoff, c.RestartBackoffMax, c.RestartResetAfter)
	}
	for _, f := range []struct{ name, env, value string }{
		{"api-server", EnvAPIServer, c.APIServer},
		{"node-name", EnvNodeName, c.NodeName},
		{"mvmd-socket", EnvMvmdSocket, c.MvmdSocket},
	} {
		if f.value == "" {
			return Config{}, fmt.Errorf("--%s or %s is required", f.name, f.env)
		}
	}
	if err := c.Liveness.Validate(); err != nil {
		return Config{}, err
	}
	var err error
	if c.Network, err = newNetwork(networkPlugin, egress, ingress); err != nil {
		return Config{}, err
	}
	if err = checkStoragePlugin(storagePlugin); err != nil {
		return Config{}, err
	}
	c.StoragePlugin = storagePlugin
	if tokenFile != "" || c.CertDir != "" {
		token, err := bootstrapToken(tokenFile, c.CertDir, tlsFiles)
		if err != nil {
			return Config{}, err
		}
		c.TLSCA, c.BootstrapToken = tlsFiles.CA, token
		return c, nil
	}
	if c.HTTPClient, err = tlsFiles.Client(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
