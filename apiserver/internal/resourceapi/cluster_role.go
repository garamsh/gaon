package resourceapi

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// PutClusterRole creates r, or changes the rules of the stored ClusterRole r
// was read at.
func (s *service) PutClusterRole(ctx context.Context, name string, r *api.ClusterRole) (*api.ClusterRole, error) {
	if err := checkName(name, r.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, clusterRoles, "", operationOf(r.Metadata.ResourceVersion), r); err != nil {
		return nil, err
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return admittedPut(ctx, s, clusterRoles, r, func() error { return nil }, func(*api.ClusterRole) error { return nil })
}

// EnsureClusterRole creates r when no ClusterRole of its name exists, and
// leaves an existing one as it is. It calls no webhook.
func (s *service) EnsureClusterRole(ctx context.Context, r *api.ClusterRole) error {
	return ensure(ctx, s, clusterRoles, r, r.Validate)
}

// DeleteClusterRole removes the ClusterRole if rv is its current version.
func (s *service) DeleteClusterRole(ctx context.Context, name string, rv api.ResourceVersion) (*api.ClusterRole, error) {
	r, rev, err := clusterRoles.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(clusterRoles.plural, name, rv, r.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := admitDelete(ctx, s, clusterRoles, r); err != nil {
		return nil, err
	}
	return r, clusterRoles.remove(ctx, s.st, r, rev)
}

func (s *service) GetClusterRole(ctx context.Context, name string) (*api.ClusterRole, error) {
	r, _, err := clusterRoles.get(ctx, s.st, name)
	return r, err
}

func (s *service) ListClusterRoles(ctx context.Context, sel api.Selector) (*api.ClusterRoleList, error) {
	items, rv, err := clusterRoles.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.ClusterRoleList{TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRoleList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchClusterRoles(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return clusterRoles.watch(ctx, s.st, rv, sel)
}
