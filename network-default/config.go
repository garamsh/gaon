package networkdefault

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/garamsh/gaon/lib/plugin"
)

// Environment variables, each the fallback of the flag named beside it.
const (
	EnvPluginDir     = "GAON_PLUGIN_DIR"
	EnvEgressPrefix  = "GAON_EGRESS_PREFIX"
	EnvIngressPrefix = "GAON_INGRESS_PREFIX"
	EnvLANInterface  = "GAON_LAN_INTERFACE"
	EnvLANPool       = "GAON_LAN_POOL"
)

// Config is what the plugin is started with.
type Config struct {
	// PluginDir is the socket directory the plugin serves in.
	PluginDir string
	// Egress and Ingress are the CIDRs of --egress-prefix and
	// --ingress-prefix.
	Egress, Ingress []string
	// LANInterface is --lan-interface, the interface the node answers ARP and
	// NDP on for a LoadBalancer Service's LAN address, and LANPool the CIDRs
	// of --lan-pool, the range of LAN addresses the plugin owns on it. Both
	// are empty when the node answers for none.
	LANInterface string
	LANPool      []string
}

// ParseConfig reads the flags in args. A flag not given falls back to its
// environment variable, read through getenv, and then to its default. A flag
// given replaces its variable. It returns flag.ErrHelp when usage was asked
// for, after writing it to out.
func ParseConfig(args []string, getenv func(string) string, out io.Writer) (Config, error) {
	var c Config
	seen := map[string]bool{}
	fs := flag.NewFlagSet("gaon-network-default", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := getenv(EnvPluginDir)
	if dir == "" {
		dir = plugin.DefaultDir
	}
	fs.StringVar(&c.PluginDir, "plugin-dir", dir, "socket directory the plugin serves in (env "+EnvPluginDir+")")
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
	fs.StringVar(&c.LANInterface, "lan-interface", getenv(EnvLANInterface), "interface the node answers ARP and NDP on for LAN addresses (env "+EnvLANInterface+")")
	prefixes(&c.LANPool, "lan-pool", EnvLANPool, "CIDR of the LAN addresses the plugin owns on the LAN interface")
	prefixes(&c.Egress, "egress-prefix", EnvEgressPrefix, "CIDR a MicroVM may send to over any protocol")
	prefixes(&c.Ingress, "ingress-prefix", EnvIngressPrefix, "CIDR a MicroVM's ports accept TCP from")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	// A prefix that is not a CIDR stops the start.
	if _, err := newDefault(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Run serves the plugin named Name in cfg.PluginDir until ctx ends.
func Run(ctx context.Context, cfg Config) error {
	d, err := newDefault(cfg)
	if err != nil {
		return err
	}
	return plugin.ServeNetwork(ctx, cfg.PluginDir, Name, d)
}

func newDefault(c Config) (*Default, error) {
	d, err := NewDefault(c.Egress, c.Ingress)
	if err != nil {
		return nil, err
	}
	return d.WithLAN(c.LANInterface, c.LANPool)
}
