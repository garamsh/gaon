package nodeagent

import (
	"context"
	"fmt"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
	"github.com/garamsh/gaon/node-agent/internal/microvms"
)

// defaultPluginTimeout bounds each call to a plugin when Config.PluginTimeout
// is not set, so one that hangs delays a MicroVM's round and no more.
const defaultPluginTimeout = 10 * time.Second

// pluginTimeout is the bound on each call to a plugin.
func (c Config) pluginTimeout() time.Duration {
	if c.PluginTimeout > 0 {
		return c.PluginTimeout
	}
	return defaultPluginTimeout
}

// pluginClients are the plugins the agent calls and what releases them.
type pluginClients struct {
	network microvms.Network
	storage microvms.Storage
	closers []func() error
}

func (p *pluginClients) close() {
	for _, c := range p.closers {
		_ = c()
	}
}

// dialPlugins returns the clients of the plugins cfg names. It does not
// connect: a plugin that is not running fails the call that needs it, which is
// retried. A name that is empty is no plugin.
func dialPlugins(cfg Config) (*pluginClients, error) {
	p := &pluginClients{network: noNetwork{}, storage: noStorage{}}
	if cfg.NetworkPlugin != "" {
		c, err := plugin.DialNetwork(cfg.PluginDir, cfg.NetworkPlugin)
		if err != nil {
			return nil, err
		}
		p.network = timedNetwork{c, cfg.pluginTimeout()}
		p.closers = append(p.closers, c.Close)
	}
	if cfg.StoragePlugin != "" {
		c, err := plugin.DialStorage(cfg.PluginDir, cfg.StoragePlugin)
		if err != nil {
			p.close()
			return nil, err
		}
		p.storage = timedStorage{c, cfg.pluginTimeout()}
		p.closers = append(p.closers, c.Close)
	}
	return p, nil
}

type timedNetwork struct {
	plugin.Network
	timeout time.Duration
}

func (n timedNetwork) Policy(ctx context.Context, m *api.MicroVM) (*plugin.NetworkPolicy, error) {
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	return n.Network.Policy(ctx, m)
}

type timedStorage struct {
	plugin.Storage
	timeout time.Duration
}

func (s timedStorage) Attach(ctx context.Context, m *api.MicroVM) ([]plugin.DiskAttachment, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.Storage.Attach(ctx, m)
}

func (s timedStorage) Release(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.Storage.Release(ctx, id)
}

// noNetwork stands in when no network plugin is named: every MicroVM is
// created with the empty policy, which blocks all traffic.
type noNetwork struct{}

func (noNetwork) Policy(context.Context, *api.MicroVM) (*plugin.NetworkPolicy, error) {
	return &plugin.NetworkPolicy{}, nil
}

// reasonNoStoragePlugin is the reason a MicroVM that names Volumes fails when
// the agent has no storage plugin.
const reasonNoStoragePlugin = "NoStoragePlugin"

// noStorage stands in when no storage plugin is named: a MicroVM with no
// Volumes is served, one that names a Volume is refused.
type noStorage struct{}

func (noStorage) Attach(_ context.Context, m *api.MicroVM) ([]plugin.DiskAttachment, error) {
	if len(m.Spec.Volumes) > 0 {
		return nil, &plugin.Refusal{Reason: reasonNoStoragePlugin, Message: fmt.Sprintf("the MicroVM names %d Volumes and the node agent has no storage plugin", len(m.Spec.Volumes))}
	}
	return nil, nil
}

func (noStorage) Release(context.Context, string) error { return nil }
