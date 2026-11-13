package nodeagent

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
	"github.com/garamsh/gaon/node-agent/internal/liveness"
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

	EnvAgentAddresses = "GAON_AGENT_ADDRESSES"
	EnvAgentPort      = "GAON_AGENT_PORT"

	EnvBootstrapTokenFile = "GAON_BOOTSTRAP_TOKEN_FILE"
	EnvCertDir            = "GAON_CERT_DIR"

	EnvRestartBackoff    = "GAON_RESTART_BACKOFF"
	EnvRestartBackoffMax = "GAON_RESTART_BACKOFF_MAX"
	EnvRestartResetAfter = "GAON_RESTART_RESET_AFTER"

	EnvPluginDir     = "GAON_PLUGIN_DIR"
	EnvNetworkPlugin = "GAON_NETWORK_PLUGIN"
	EnvStoragePlugin = "GAON_STORAGE_PLUGIN"

	EnvServiceAccountAudience = "GAON_SERVICE_ACCOUNT_AUDIENCE"
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
	BootstrapToken string
	// TLSCA is the file of the CA certificates the API server's certificate
	// must chain to, and a client certificate of the listener must chain to.
	// TLSCert and TLSKey are the files of the certificate an operator supplies
	// instead of CertDir.
	TLSCA, TLSCert, TLSKey string
	// AgentAddresses and AgentPort are where the agent serves a MicroVM's log
	// and exec, and what the Node's status states. Both are empty when it
	// serves none.
	AgentAddresses []netip.Addr
	AgentPort      int
	// NodeName names the Node and the Lease.
	NodeName string
	// MvmdSocket is the path of mvmd's runtime API Unix socket.
	MvmdSocket string
	// Liveness holds δ and the Lease and deadline durations.
	Liveness liveness.Params
	// PluginTimeout bounds each call to a plugin; zero is 10 seconds.
	PluginTimeout time.Duration
	// ResyncPeriod is the time between two reconciliations of the MicroVMs
	// and two reports of the Node's status.
	ResyncPeriod time.Duration
	// RestartBackoff is the wait before a MicroVM's first restart, doubled
	// with each restart up to RestartBackoffMax.
	RestartBackoff    time.Duration
	RestartBackoffMax time.Duration
	// RestartResetAfter is how long a MicroVM runs before the wait starts
	// again from RestartBackoff.
	RestartResetAfter time.Duration
	// PluginDir is the directory the plugins serve their sockets in.
	PluginDir string
	// NetworkPlugin and StoragePlugin name the plugins that decide each
	// MicroVM's network policy and disks, each a process serving the socket
	// <PluginDir>/<kind>-<name>.sock. An empty name is no plugin: the policy
	// is empty and no disk is attached.
	NetworkPlugin string
	StoragePlugin string
	// ServiceAccountAudience is the audience the agent requests a MicroVM's
	// ServiceAccount token with. The API server accepts a token whose
	// audience is one of its --api-audiences, by default its issuer URL, so
	// this is that value. A MicroVM that names a ServiceAccount is not
	// started while it is empty.
	ServiceAccountAudience string
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
)

// ParseConfig reads the flags in args. A flag not given falls back to its
// environment variable, read through getenv, and then to its default. It
// returns flag.ErrHelp when usage was asked for, after writing it to out.
func ParseConfig(args []string, getenv func(string) string, out io.Writer) (Config, error) {
	var c Config
	var errs []error
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
	var agentAddresses string
	str(&agentAddresses, "agent-addresses", EnvAgentAddresses, "comma-separated IP addresses the agent serves a MicroVM's log and exec on, and the Node's status.addresses; none when empty")
	port := 0
	if v := getenv(EnvAgentPort); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", EnvAgentPort, err))
		}
		port = p
	}
	fs.IntVar(&c.AgentPort, "agent-port", port, "port the agent serves a MicroVM's log and exec on at --agent-addresses; 0 serves none (env "+EnvAgentPort+")")
	var tokenFile string
	str(&tokenFile, "bootstrap-token-file", EnvBootstrapTokenFile, "file of the bootstrap token the agent asks for its client certificate with")
	str(&c.CertDir, "cert-dir", EnvCertDir, "directory where the agent keeps its key and client certificate")
	str(&c.NodeName, "node-name", EnvNodeName, "name of this node's Node and Lease")
	str(&c.MvmdSocket, "mvmd-socket", EnvMvmdSocket, "path of mvmd's runtime API Unix socket")
	dur(&c.Liveness.Delta, "delta", EnvDelta, defaultDelta, "δ, how long a MicroVM can run past mvmd's recorded deadline expiry")
	dur(&c.Liveness.LeaseDuration, "lease-duration", EnvLeaseDuration, defaultLeaseDuration, "how long the Lease is valid after a renewal, in whole seconds")
	dur(&c.Liveness.RenewPeriod, "renew-period", EnvRenewPeriod, defaultRenewPeriod, "time between two renewals of the Lease and the deadline")
	dur(&c.Liveness.DeadlineLength, "deadline-length", EnvDeadlineLength, defaultDeadlineLength, "length each deadline renewal asks mvmd for")
	dur(&c.Liveness.RPCTimeout, "rpc-timeout", EnvRPCTimeout, defaultRPCTimeout, "bound on each call of a renewal")
	dur(&c.ResyncPeriod, "resync-period", EnvResyncPeriod, defaultResyncPeriod, "time between two reconciliations of the MicroVMs")
	dur(&c.RestartBackoff, "restart-backoff", EnvRestartBackoff, defaultRestartBackoff, "wait before a MicroVM's first restart, doubled with each restart")
	dur(&c.RestartBackoffMax, "restart-backoff-max", EnvRestartBackoffMax, defaultRestartBackoffMax, "longest wait before a restart")
	dur(&c.RestartResetAfter, "restart-reset-after", EnvRestartResetAfter, defaultRestartResetAfter, "how long a MicroVM runs before the restart wait starts again from the first")
	str(&c.PluginDir, "plugin-dir", EnvPluginDir, "directory the network and storage plugins serve their sockets in, default "+plugin.DefaultDir)
	str(&c.NetworkPlugin, "network-plugin", EnvNetworkPlugin, "name of the network plugin, which decides each MicroVM's network policy; none when empty")
	str(&c.StoragePlugin, "storage-plugin", EnvStoragePlugin, "name of the storage plugin, which decides each MicroVM's disks; none when empty")
	str(&c.ServiceAccountAudience, "service-account-audience", EnvServiceAccountAudience, "audience of a MicroVM's ServiceAccount token: one of the API server's --api-audiences, by default its issuer URL; a MicroVM that names a ServiceAccount is not started while empty")
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
	if c.PluginDir == "" {
		c.PluginDir = plugin.DefaultDir
	}
	for _, p := range []struct {
		kind plugin.Kind
		name string
	}{{plugin.KindNetwork, c.NetworkPlugin}, {plugin.KindStorage, c.StoragePlugin}} {
		if p.name == "" {
			continue
		}
		if _, err := plugin.SocketPath(c.PluginDir, p.kind, p.name); err != nil {
			return Config{}, err
		}
	}
	var err error
	if c.AgentAddresses, err = agentListen(agentAddresses, c.AgentPort); err != nil {
		return Config{}, err
	}
	c.TLSCA, c.TLSCert, c.TLSKey = tlsFiles.CA, tlsFiles.Cert, tlsFiles.Key
	if len(c.AgentAddresses) > 0 && (tlsFiles.CA == "" || (c.CertDir == "" && (tlsFiles.Cert == "" || tlsFiles.Key == ""))) {
		return Config{}, errors.New("--agent-addresses needs --tls-ca and a certificate to serve with: --cert-dir, or --tls-cert and --tls-key")
	}
	if tokenFile != "" || c.CertDir != "" {
		token, err := bootstrapToken(tokenFile, c.CertDir, tlsFiles)
		if err != nil {
			return Config{}, err
		}
		c.BootstrapToken = token
		return c, nil
	}
	if c.HTTPClient, err = tlsFiles.Client(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// agentListen returns the addresses the list names, each an IP literal given
// once, and checks that they and the port are given together and the port is
// one a process can listen on.
func agentListen(list string, port int) ([]netip.Addr, error) {
	if list == "" && port == 0 {
		return nil, nil
	}
	if list == "" || port == 0 {
		return nil, errors.New("--agent-addresses and --agent-port are given together or not at all")
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("--agent-port is %d, want 1 to 65535", port)
	}
	var addrs []netip.Addr
	for _, part := range strings.Split(list, ",") {
		a, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("--agent-addresses: %w", err)
		}
		if a.IsUnspecified() || a.Zone() != "" {
			return nil, fmt.Errorf("--agent-addresses: %s is not an address of the Node", a)
		}
		if slices.Contains(addrs, a) {
			return nil, fmt.Errorf("--agent-addresses: %s is given twice", a)
		}
		addrs = append(addrs, a)
	}
	return addrs, nil
}
