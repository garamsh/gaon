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
	CreateServiceAccountToken(ctx context.Context, name string, req *api.TokenRequest) (*api.TokenRequest, error)
	ListNetworkPolicies(ctx context.Context) (*api.NetworkPolicyList, error)
	ListServices(ctx context.Context) (*api.ServiceList, error)
	ListEndpoints(ctx context.Context) (*api.EndpointsList, error)
}
