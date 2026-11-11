package machines

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// API is the part of the resource API the Machines use.
type API interface {
	ListMachines(ctx context.Context) (*api.MachineList, error)
	WatchMachines(ctx context.Context, rv api.ResourceVersion) (*api.Watch[api.Machine], error)
	GetMachine(ctx context.Context, name string) (*api.Machine, error)
	PutMachine(ctx context.Context, m *api.Machine) (*api.Machine, error)
	PutMachineStatus(ctx context.Context, m *api.Machine) (*api.Machine, error)
}
