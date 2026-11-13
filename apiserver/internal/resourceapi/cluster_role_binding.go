package resourceapi

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// PutClusterRoleBinding creates b, or changes the subjects of the stored
// ClusterRoleBinding b was read at. An update that changes roleRef is Invalid.
func (s *service) PutClusterRoleBinding(ctx context.Context, name string, b *api.ClusterRoleBinding) (*api.ClusterRoleBinding, error) {
	if err := checkName(name, b.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, clusterRoleBindings, "", operationOf(b.Metadata.ResourceVersion), b); err != nil {
		return nil, err
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return admittedPut(ctx, s, clusterRoleBindings, b, func() error { return nil }, func(old *api.ClusterRoleBinding) error {
		if b.RoleRef != old.RoleRef {
			return api.NewAPIError(api.ReasonInvalid, "roleRef of clusterrolebinding %q cannot change", name)
		}
		return nil
	})
}

// EnsureClusterRoleBinding creates b when no ClusterRoleBinding of its name
// exists, and leaves an existing one as it is. It calls no webhook.
func (s *service) EnsureClusterRoleBinding(ctx context.Context, b *api.ClusterRoleBinding) error {
	return ensure(ctx, s, clusterRoleBindings, b, b.Validate)
}

// DeleteClusterRoleBinding removes the ClusterRoleBinding if rv is its current
// version.
func (s *service) DeleteClusterRoleBinding(ctx context.Context, name string, rv api.ResourceVersion) (*api.ClusterRoleBinding, error) {
	b, rev, err := clusterRoleBindings.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(clusterRoleBindings.plural, name, rv, b.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := admitDelete(ctx, s, clusterRoleBindings, b); err != nil {
		return nil, err
	}
	return b, clusterRoleBindings.remove(ctx, s.st, b, rev)
}

func (s *service) GetClusterRoleBinding(ctx context.Context, name string) (*api.ClusterRoleBinding, error) {
	b, _, err := clusterRoleBindings.get(ctx, s.st, name)
	return b, err
}

func (s *service) ListClusterRoleBindings(ctx context.Context, sel api.Selector) (*api.ClusterRoleBindingList, error) {
	items, rv, err := clusterRoleBindings.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.ClusterRoleBindingList{TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRoleBindingList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchClusterRoleBindings(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return clusterRoleBindings.watch(ctx, s.st, rv, sel)
}
