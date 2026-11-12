package nodeagent

import (
	"fmt"

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

// newStorage returns the storage plugin called name.
func newStorage(name string) (machines.Storage, error) {
	switch name {
	case storage.NoneName:
		return storage.None{}, nil
	default:
		return nil, fmt.Errorf("unknown storage plugin %q, want %q", name, storage.NoneName)
	}
}
