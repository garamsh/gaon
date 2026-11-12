package scheduler

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// API is the part of the resource API the Scheduler calls; *api.Client
// satisfies it.
type API interface {
	ListNodes(ctx context.Context) (*api.NodeList, error)
	ListMachines(ctx context.Context) (*api.MachineList, error)
	ListVolumes(ctx context.Context) (*api.VolumeList, error)
	BindMachine(ctx context.Context, name string, b api.Binding) (*api.Machine, error)
	PutMachineStatus(ctx context.Context, m *api.Machine) (*api.Machine, error)
}
