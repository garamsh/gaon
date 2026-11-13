package resourceapi

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// PutEndpoints creates o, or replaces the stored Endpoints o was read at.
func (s *service) PutEndpoints(ctx context.Context, name string, o *api.Endpoints) (*api.Endpoints, error) {
	if err := checkName(name, o.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, endpointsKind, "", operationOf(o.Metadata.ResourceVersion), o); err != nil {
		return nil, err
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return admittedPut(ctx, s, endpointsKind, o, func() error { return nil }, func(*api.Endpoints) error { return nil })
}

// DeleteEndpoints removes the Endpoints if rv is its current version.
func (s *service) DeleteEndpoints(ctx context.Context, name string, rv api.ResourceVersion) (*api.Endpoints, error) {
	o, rev, err := endpointsKind.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(endpointsKind.plural, name, rv, o.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := admitDelete(ctx, s, endpointsKind, o); err != nil {
		return nil, err
	}
	return o, endpointsKind.remove(ctx, s.st, o, rev)
}

func (s *service) GetEndpoints(ctx context.Context, name string) (*api.Endpoints, error) {
	o, _, err := endpointsKind.get(ctx, s.st, name)
	return o, err
}

func (s *service) ListEndpoints(ctx context.Context, sel api.Selector) (*api.EndpointsList, error) {
	items, rv, err := endpointsKind.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.EndpointsList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEndpointsList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchEndpoints(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return endpointsKind.watch(ctx, s.st, rv, sel)
}
