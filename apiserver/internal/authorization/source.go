package authorization

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// Source is what the RBAC authorizer reads to decide: the resource API, read
// as the server itself. A read that finds nothing returns an error matching
// api.ErrNotFound.
type Source interface {
	GetNode(ctx context.Context, name string) (*api.Node, error)
	GetMicroVM(ctx context.Context, name string) (*api.MicroVM, error)
	ListMicroVMs(ctx context.Context) (*api.MicroVMList, error)
	GetVolume(ctx context.Context, name string) (*api.Volume, error)
	GetCertificateSigningRequest(ctx context.Context, name string) (*api.CertificateSigningRequest, error)
	GetClusterRole(ctx context.Context, name string) (*api.ClusterRole, error)
	ListClusterRoleBindings(ctx context.Context) (*api.ClusterRoleBindingList, error)
}
