package resourceapi

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// PutClusterEvent creates e, or replaces the stored Event e was read at, which
// may raise its count and move its last timestamp and change nothing else of
// its spec.
func (s *service) PutClusterEvent(ctx context.Context, name string, e *api.ClusterEvent) (*api.ClusterEvent, error) {
	if err := checkName(name, e.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, clusterEvents, "", operationOf(e.Metadata.ResourceVersion), e); err != nil {
		return nil, err
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return admittedPut(ctx, s, clusterEvents, e, func() error { return nil }, e.ValidateUpdate)
}

// DeleteClusterEvent removes the Event if rv is its current version.
func (s *service) DeleteClusterEvent(ctx context.Context, name string, rv api.ResourceVersion) (*api.ClusterEvent, error) {
	e, rev, err := clusterEvents.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(clusterEvents.plural, name, rv, e.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := admitDelete(ctx, s, clusterEvents, e); err != nil {
		return nil, err
	}
	return e, clusterEvents.remove(ctx, s.st, e, rev)
}

func (s *service) GetClusterEvent(ctx context.Context, name string) (*api.ClusterEvent, error) {
	e, _, err := clusterEvents.get(ctx, s.st, name)
	return e, err
}

func (s *service) ListClusterEvents(ctx context.Context, sel api.Selector) (*api.ClusterEventList, error) {
	items, rv, err := clusterEvents.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.ClusterEventList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEventList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchClusterEvents(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return clusterEvents.watch(ctx, s.st, rv, sel)
}
