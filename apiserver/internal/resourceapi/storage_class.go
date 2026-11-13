package resourceapi

import (
	"context"
	"errors"

	"github.com/garamsh/gaon/lib/api"
)

// PutStorageClass creates c, or changes the stored StorageClass c was read at.
// An update that changes the provisioner is Invalid.
func (s *service) PutStorageClass(ctx context.Context, name string, c *api.StorageClass) (*api.StorageClass, error) {
	if err := checkName(name, c.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, storageClasses, "", operationOf(c.Metadata.ResourceVersion), c); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return admittedPut(ctx, s, storageClasses, c, func() error { return nil }, c.ValidateUpdate)
}

// EnsureStorageClass creates c when no StorageClass of its name exists, and
// leaves an existing one as it is. It calls no webhook.
func (s *service) EnsureStorageClass(ctx context.Context, c *api.StorageClass) error {
	return ensure(ctx, s, storageClasses, c, c.Validate)
}

// DeleteStorageClass removes the StorageClass if rv is its current version.
func (s *service) DeleteStorageClass(ctx context.Context, name string, rv api.ResourceVersion) (*api.StorageClass, error) {
	c, rev, err := storageClasses.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(storageClasses.plural, name, rv, c.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := admitDelete(ctx, s, storageClasses, c); err != nil {
		return nil, err
	}
	return c, storageClasses.remove(ctx, s.st, c, rev)
}

func (s *service) GetStorageClass(ctx context.Context, name string) (*api.StorageClass, error) {
	c, _, err := storageClasses.get(ctx, s.st, name)
	return c, err
}

func (s *service) ListStorageClasses(ctx context.Context, sel api.Selector) (*api.StorageClassList, error) {
	items, rv, err := storageClasses.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.StorageClassList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindStorageClassList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchStorageClasses(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return storageClasses.watch(ctx, s.st, rv, sel)
}

// storageClassOf returns the StorageClass named name, or nil where there is
// none.
func (s *service) storageClassOf(ctx context.Context, name string) (*api.StorageClass, error) {
	c, _, err := storageClasses.get(ctx, s.st, name)
	if errors.Is(err, api.ErrNotFound) {
		return nil, nil
	}
	return c, err
}

// EnsureLocalStorageClass creates the StorageClass StorageClassLocal, which
// provisions with "local" and allows volume expansion, when svc holds none. It
// leaves an existing one as it is, so an administrator's change to it survives
// a restart, and calls no webhook.
func EnsureLocalStorageClass(ctx context.Context, svc Service) error {
	class := &api.StorageClass{
		TypeMeta:             api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindStorageClass},
		Metadata:             api.ObjectMeta{Name: api.StorageClassLocal},
		Provisioner:          api.StorageClassLocal,
		AllowVolumeExpansion: true,
	}
	return svc.EnsureStorageClass(ctx, class)
}
