package scheduler

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// API is the part of the resource API the Scheduler calls; *api.Client
// satisfies it.
type API interface {
	ListNodes(ctx context.Context) (*api.NodeList, error)
	ListMicroVMs(ctx context.Context) (*api.MicroVMList, error)
	ListVolumes(ctx context.Context) (*api.VolumeList, error)
	BindMicroVM(ctx context.Context, name string, b api.Binding) (*api.MicroVM, error)
	PutMicroVMStatus(ctx context.Context, m *api.MicroVM) (*api.MicroVM, error)
}
