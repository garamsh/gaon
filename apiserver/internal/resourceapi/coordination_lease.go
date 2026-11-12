package resourceapi

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// PutCoordinationLease creates l, or replaces the stored Lease l was read at.
// Of two writers at the same version only one succeeds, which is the
// compare-and-swap a lock is taken by. Unlike a Node's Lease it needs no Node.
func (s *service) PutCoordinationLease(ctx context.Context, name string, l *api.CoordinationLease) (*api.CoordinationLease, error) {
	if err := checkName(name, l.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, coordinationLeases, "", operationOf(l.Metadata.ResourceVersion), l); err != nil {
		return nil, err
	}
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return admittedPut(ctx, s, coordinationLeases, l, func() error { return nil }, func(*api.CoordinationLease) error { return nil })
}

// DeleteCoordinationLease removes the Lease if rv is its current version.
func (s *service) DeleteCoordinationLease(ctx context.Context, name string, rv api.ResourceVersion) (*api.CoordinationLease, error) {
	l, rev, err := coordinationLeases.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(coordinationLeases.plural, name, rv, l.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := admitDelete(ctx, s, coordinationLeases, l); err != nil {
		return nil, err
	}
	return l, coordinationLeases.remove(ctx, s.st, l, rev)
}

func (s *service) GetCoordinationLease(ctx context.Context, name string) (*api.CoordinationLease, error) {
	l, _, err := coordinationLeases.get(ctx, s.st, name)
	return l, err
}

func (s *service) ListCoordinationLeases(ctx context.Context, sel api.Selector) (*api.CoordinationLeaseList, error) {
	items, rv, err := coordinationLeases.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.CoordinationLeaseList{TypeMeta: api.TypeMeta{APIVersion: api.CoordinationGroupVersion, Kind: api.KindCoordinationLeaseList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchCoordinationLeases(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return coordinationLeases.watch(ctx, s.st, rv, sel)
}
