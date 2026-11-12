package resourceapi

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// PutValidatingWebhookConfiguration creates c, or replaces the webhooks of the
// stored configuration c was read at. The write is not sent to any webhook, so
// that a broken webhook cannot lock out its own repair.
func (s *service) PutValidatingWebhookConfiguration(ctx context.Context, name string, c *api.ValidatingWebhookConfiguration) (*api.ValidatingWebhookConfiguration, error) {
	if err := checkName(name, c.Metadata.Name); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return validatingWebhookConfigurations.put(ctx, s.st, c, func() error { return nil }, func(*api.ValidatingWebhookConfiguration) error { return nil })
}

// DeleteValidatingWebhookConfiguration removes the configuration if rv is its
// current version.
func (s *service) DeleteValidatingWebhookConfiguration(ctx context.Context, name string, rv api.ResourceVersion) (*api.ValidatingWebhookConfiguration, error) {
	c, rev, err := validatingWebhookConfigurations.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(validatingWebhookConfigurations.plural, name, rv, c.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	return c, validatingWebhookConfigurations.remove(ctx, s.st, c, rev)
}

func (s *service) GetValidatingWebhookConfiguration(ctx context.Context, name string) (*api.ValidatingWebhookConfiguration, error) {
	c, _, err := validatingWebhookConfigurations.get(ctx, s.st, name)
	return c, err
}

func (s *service) ListValidatingWebhookConfigurations(ctx context.Context) (*api.ValidatingWebhookConfigurationList, error) {
	items, rv, err := validatingWebhookConfigurations.list(ctx, s.st)
	if err != nil {
		return nil, err
	}
	return &api.ValidatingWebhookConfigurationList{TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindValidatingWebhookConfigurationList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchValidatingWebhookConfigurations(ctx context.Context, rv api.ResourceVersion) (<-chan api.WatchEvent, error) {
	return validatingWebhookConfigurations.watch(ctx, s.st, rv)
}
