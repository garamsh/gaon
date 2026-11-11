package nodeagent

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

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
)

// Config is what the agent is started with.
type Config struct {
	// APIServer is the root of the resource API, the address up to the
	// group and version, for example http://127.0.0.1:8080/apis/compute.gaon.garam.sh/v1alpha1.
	APIServer string
	// NodeName names the Node and the Lease.
	NodeName string
	// MvmdSocket is the path of mvmd's runtime API Unix socket.
	MvmdSocket string
	// Liveness holds δ and the Lease and deadline durations.
	Liveness liveness.Params
	// ResyncPeriod is the time between two reconciliations of the Machines
	// and two reports of the Node's status.
	ResyncPeriod time.Duration
}

// Defaults of the durations. δ's is provisional: it is not measured.
const (
	defaultDelta          = time.Second
	defaultLeaseDuration  = 30 * time.Second
	defaultRenewPeriod    = 5 * time.Second
	defaultDeadlineLength = 15 * time.Second
	defaultRPCTimeout     = 2 * time.Second
	defaultResyncPeriod   = 10 * time.Second
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
	str(&c.NodeName, "node-name", EnvNodeName, "name of this node's Node and Lease")
	str(&c.MvmdSocket, "mvmd-socket", EnvMvmdSocket, "path of mvmd's runtime API Unix socket")
	dur(&c.Liveness.Delta, "delta", EnvDelta, defaultDelta, "δ, how long a Machine can run past mvmd's recorded deadline expiry")
	dur(&c.Liveness.LeaseDuration, "lease-duration", EnvLeaseDuration, defaultLeaseDuration, "how long the Lease is valid after a renewal, in whole seconds")
	dur(&c.Liveness.RenewPeriod, "renew-period", EnvRenewPeriod, defaultRenewPeriod, "time between two renewals of the Lease and the deadline")
	dur(&c.Liveness.DeadlineLength, "deadline-length", EnvDeadlineLength, defaultDeadlineLength, "length each deadline renewal asks mvmd for")
	dur(&c.Liveness.RPCTimeout, "rpc-timeout", EnvRPCTimeout, defaultRPCTimeout, "bound on each call of a renewal")
	dur(&c.ResyncPeriod, "resync-period", EnvResyncPeriod, defaultResyncPeriod, "time between two reconciliations of the Machines")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	if c.ResyncPeriod <= 0 {
		return Config{}, fmt.Errorf("resync period is %s, want a positive duration", c.ResyncPeriod)
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
	return c, nil
}
