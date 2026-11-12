package resourceapi

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// PutConfigMap creates o, or replaces the stored ConfigMap o was read at.
func (s *service) PutConfigMap(ctx context.Context, name string, o *api.ConfigMap) (*api.ConfigMap, error) {
	if err := checkName(name, o.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, configMaps, "", operationOf(o.Metadata.ResourceVersion), o); err != nil {
		return nil, err
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return admittedPut(ctx, s, configMaps, o, func() error { return nil }, func(*api.ConfigMap) error { return nil })
}

// DeleteConfigMap removes the ConfigMap if rv is its current version.
func (s *service) DeleteConfigMap(ctx context.Context, name string, rv api.ResourceVersion) (*api.ConfigMap, error) {
	o, rev, err := configMaps.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(configMaps.plural, name, rv, o.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := admitDelete(ctx, s, configMaps, o); err != nil {
		return nil, err
	}
	return o, configMaps.remove(ctx, s.st, o, rev)
}

func (s *service) GetConfigMap(ctx context.Context, name string) (*api.ConfigMap, error) {
	o, _, err := configMaps.get(ctx, s.st, name)
	return o, err
}

func (s *service) ListConfigMaps(ctx context.Context, sel api.Selector) (*api.ConfigMapList, error) {
	items, rv, err := configMaps.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.ConfigMapList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindConfigMapList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchConfigMaps(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return configMaps.watch(ctx, s.st, rv, sel)
}
