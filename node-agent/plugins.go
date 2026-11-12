package nodeagent

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/garamsh/gaon/node-agent/internal/clock"

	"github.com/garamsh/gaon/node-agent/internal/machines"
	"github.com/garamsh/gaon/node-agent/internal/machines/network"
	"github.com/garamsh/gaon/node-agent/internal/machines/storage"
)

// newNetwork returns the network plugin called name, built with the prefixes
// of --egress-prefix and --ingress-prefix. The plugins are compiled in.
func newNetwork(name string, egress, ingress []string) (machines.Network, error) {
	switch name {
	case network.Name:
		return network.NewDefault(egress, ingress)
	default:
		return nil, fmt.Errorf("unknown network plugin %q, want %q", name, network.Name)
	}
}

// checkStoragePlugin returns an error unless name is a storage plugin.
func checkStoragePlugin(name string) error {
	switch name {
	case storage.LocalName, storage.NoneName:
		return nil
	default:
		return fmt.Errorf("unknown storage plugin %q, want %q or %q", name, storage.LocalName, storage.NoneName)
	}
}

// newStorage returns the storage plugin called name, built with the
// dependencies it needs. A plugin with a Run method is returned with it.
func newStorage(name string, node string, resync time.Duration, client storage.Volumes, runtime machines.Runtime, clk clock.Clock, log *slog.Logger) (machines.Storage, error) {
	switch name {
	case storage.LocalName:
		return storage.NewLocal(node, resync, client, runtime, clk, log), nil
	case storage.NoneName:
		return storage.None{}, nil
	default:
		return nil, fmt.Errorf("unknown storage plugin %q", name)
	}
}
