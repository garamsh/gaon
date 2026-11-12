package resourceapi

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// PutMutatingWebhookConfiguration creates c, or replaces the webhooks of the
// stored configuration c was read at. The write is not sent to any webhook, so
// that a broken webhook cannot lock out its own repair.
func (s *service) PutMutatingWebhookConfiguration(ctx context.Context, name string, c *api.MutatingWebhookConfiguration) (*api.MutatingWebhookConfiguration, error) {
	if err := checkName(name, c.Metadata.Name); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return mutatingWebhookConfigurations.put(ctx, s.st, c, func() error { return nil }, func(*api.MutatingWebhookConfiguration) error { return nil })
}

// DeleteMutatingWebhookConfiguration removes the configuration if rv is its
// current version.
func (s *service) DeleteMutatingWebhookConfiguration(ctx context.Context, name string, rv api.ResourceVersion) (*api.MutatingWebhookConfiguration, error) {
	c, rev, err := mutatingWebhookConfigurations.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(mutatingWebhookConfigurations.plural, name, rv, c.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	return c, mutatingWebhookConfigurations.remove(ctx, s.st, c, rev)
}

func (s *service) GetMutatingWebhookConfiguration(ctx context.Context, name string) (*api.MutatingWebhookConfiguration, error) {
	c, _, err := mutatingWebhookConfigurations.get(ctx, s.st, name)
	return c, err
}

func (s *service) ListMutatingWebhookConfigurations(ctx context.Context, sel api.Selector) (*api.MutatingWebhookConfigurationList, error) {
	items, rv, err := mutatingWebhookConfigurations.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.MutatingWebhookConfigurationList{TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindMutatingWebhookConfigurationList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchMutatingWebhookConfigurations(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return mutatingWebhookConfigurations.watch(ctx, s.st, rv, sel)
}
