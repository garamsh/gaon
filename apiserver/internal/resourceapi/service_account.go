package resourceapi

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// PutServiceAccount creates o, or replaces the stored ServiceAccount o was read at.
func (s *service) PutServiceAccount(ctx context.Context, name string, o *api.ServiceAccount) (*api.ServiceAccount, error) {
	if err := checkName(name, o.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, serviceAccounts, "", operationOf(o.Metadata.ResourceVersion), o); err != nil {
		return nil, err
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return admittedPut(ctx, s, serviceAccounts, o, func() error { return nil }, func(*api.ServiceAccount) error { return nil })
}

// DeleteServiceAccount removes the ServiceAccount if rv is its current version.
func (s *service) DeleteServiceAccount(ctx context.Context, name string, rv api.ResourceVersion) (*api.ServiceAccount, error) {
	o, rev, err := serviceAccounts.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(serviceAccounts.plural, name, rv, o.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := admitDelete(ctx, s, serviceAccounts, o); err != nil {
		return nil, err
	}
	return o, serviceAccounts.remove(ctx, s.st, o, rev)
}

func (s *service) GetServiceAccount(ctx context.Context, name string) (*api.ServiceAccount, error) {
	o, _, err := serviceAccounts.get(ctx, s.st, name)
	return o, err
}

func (s *service) ListServiceAccounts(ctx context.Context, sel api.Selector) (*api.ServiceAccountList, error) {
	items, rv, err := serviceAccounts.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.ServiceAccountList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindServiceAccountList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchServiceAccounts(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return serviceAccounts.watch(ctx, s.st, rv, sel)
}
