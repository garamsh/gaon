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
)

// Config is what the plugin is started with.
type Config struct {
	// PluginDir is the socket directory the plugin serves in.
	PluginDir string
	// Egress and Ingress are the CIDRs of --egress-prefix and
	// --ingress-prefix.
	Egress, Ingress []string
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
	prefixes(&c.Egress, "egress-prefix", EnvEgressPrefix, "CIDR a MicroVM may send to over any protocol")
	prefixes(&c.Ingress, "ingress-prefix", EnvIngressPrefix, "CIDR a MicroVM's ports accept TCP from")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	// A prefix that is not a CIDR stops the start.
	if _, err := NewDefault(c.Egress, c.Ingress); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Run serves the plugin named Name in cfg.PluginDir until ctx ends.
func Run(ctx context.Context, cfg Config) error {
	d, err := NewDefault(cfg.Egress, cfg.Ingress)
	if err != nil {
		return err
	}
	return plugin.ServeNetwork(ctx, cfg.PluginDir, Name, d)
}
