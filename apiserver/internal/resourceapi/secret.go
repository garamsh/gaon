package resourceapi

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// PutSecret creates o, or replaces the stored Secret o was read at.
func (s *service) PutSecret(ctx context.Context, name string, o *api.Secret) (*api.Secret, error) {
	if err := checkName(name, o.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, secrets, "", operationOf(o.Metadata.ResourceVersion), o); err != nil {
		return nil, err
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return admittedPut(ctx, s, secrets, o, func() error { return nil }, func(*api.Secret) error { return nil })
}

// DeleteSecret removes the Secret if rv is its current version.
func (s *service) DeleteSecret(ctx context.Context, name string, rv api.ResourceVersion) (*api.Secret, error) {
	o, rev, err := secrets.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(secrets.plural, name, rv, o.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := admitDelete(ctx, s, secrets, o); err != nil {
		return nil, err
	}
	return o, secrets.remove(ctx, s.st, o, rev)
}

func (s *service) GetSecret(ctx context.Context, name string) (*api.Secret, error) {
	o, _, err := secrets.get(ctx, s.st, name)
	return o, err
}

func (s *service) ListSecrets(ctx context.Context, sel api.Selector) (*api.SecretList, error) {
	items, rv, err := secrets.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.SecretList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindSecretList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchSecrets(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return secrets.watch(ctx, s.st, rv, sel)
}
