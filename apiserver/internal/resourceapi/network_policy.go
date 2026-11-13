package resourceapi

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// PutNetworkPolicy creates o, or replaces the stored NetworkPolicy o was read at.
func (s *service) PutNetworkPolicy(ctx context.Context, name string, o *api.NetworkPolicy) (*api.NetworkPolicy, error) {
	if err := checkName(name, o.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, networkPolicies, "", operationOf(o.Metadata.ResourceVersion), o); err != nil {
		return nil, err
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return admittedPut(ctx, s, networkPolicies, o, func() error { return nil }, func(*api.NetworkPolicy) error { return nil })
}

// DeleteNetworkPolicy removes the NetworkPolicy if rv is its current version.
func (s *service) DeleteNetworkPolicy(ctx context.Context, name string, rv api.ResourceVersion) (*api.NetworkPolicy, error) {
	o, rev, err := networkPolicies.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(networkPolicies.plural, name, rv, o.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := admitDelete(ctx, s, networkPolicies, o); err != nil {
		return nil, err
	}
	return o, networkPolicies.remove(ctx, s.st, o, rev)
}

func (s *service) GetNetworkPolicy(ctx context.Context, name string) (*api.NetworkPolicy, error) {
	o, _, err := networkPolicies.get(ctx, s.st, name)
	return o, err
}

func (s *service) ListNetworkPolicies(ctx context.Context, sel api.Selector) (*api.NetworkPolicyList, error) {
	items, rv, err := networkPolicies.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.NetworkPolicyList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNetworkPolicyList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchNetworkPolicies(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return networkPolicies.watch(ctx, s.st, rv, sel)
}
