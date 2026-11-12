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
	GetMachine(ctx context.Context, name string) (*api.Machine, error)
	ListMachines(ctx context.Context) (*api.MachineList, error)
	GetCertificateSigningRequest(ctx context.Context, name string) (*api.CertificateSigningRequest, error)
	GetClusterRole(ctx context.Context, name string) (*api.ClusterRole, error)
	ListClusterRoleBindings(ctx context.Context) (*api.ClusterRoleBindingList, error)
}
