package microvms

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// API is the part of the resource API the MicroVMs use.
type API interface {
	ListMicroVMs(ctx context.Context) (*api.MicroVMList, error)
	WatchMicroVMs(ctx context.Context, rv api.ResourceVersion) (*api.Watch[api.MicroVM], error)
	GetMicroVM(ctx context.Context, name string) (*api.MicroVM, error)
	PutMicroVM(ctx context.Context, m *api.MicroVM) (*api.MicroVM, error)
	GetSecret(ctx context.Context, name string) (*api.Secret, error)
	GetConfigMap(ctx context.Context, name string) (*api.ConfigMap, error)
	PutMicroVMStatus(ctx context.Context, m *api.MicroVM) (*api.MicroVM, error)
}
