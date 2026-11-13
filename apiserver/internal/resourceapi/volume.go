package resourceapi

import (
	"context"
	"errors"
	"slices"

	"github.com/garamsh/gaon/lib/api"
)

// PutVolumeStatus replaces the status of the stored Volume v was read at. A
// new attachment must name the epoch of its MicroVM's current binding, and
// succeeds only where the Volume is unattached or attached at a lower epoch.
func (s *service) PutVolumeStatus(ctx context.Context, name string, next *api.Volume) (*api.Volume, error) {
	if err := checkName(name, next.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, volumes, "status", api.OperationUpdate, next); err != nil {
		return nil, err
	}
	v, rev, err := volumes.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(volumes.plural, name, next.Metadata.ResourceVersion, v.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := next.ValidateStatus(); err != nil {
		return nil, err
	}
	if a := next.Status.Attachment; a != nil && !sameJSON(a, v.Status.Attachment) {
		if err := s.checkAttachment(ctx, v, a); err != nil {
			return nil, err
		}
	}
	old := *v
	v.Status = next.Status
	if err := validate(ctx, s, volumes, "status", api.OperationUpdate, v, &old); err != nil {
		return nil, err
	}
	return v, volumes.update(ctx, s.st, v, rev)
}

func (s *service) checkAttachment(ctx context.Context, v *api.Volume, a *api.Attachment) error {
	m, _, err := microvms.get(ctx, s.st, a.MicroVMName)
	if errors.Is(err, api.ErrNotFound) {
		return api.NewAPIError(api.ReasonInvalid, "attachment names microvm %q, which does not exist", a.MicroVMName)
	}
	if err != nil {
		return err
	}
	if a.BindingEpoch != m.Spec.BindingEpoch {
		return api.NewAPIError(api.ReasonInvalid, "attachment epoch is %d, want the microvm's current %d", a.BindingEpoch, m.Spec.BindingEpoch)
	}
	return v.Attach(a.MicroVMName, a.BindingEpoch)
}

// PutVolume creates v with phase Pending, pinned to the Node of its snapshot
// when it names one of class local, or changes the finalizers and
// spec.storage of the stored Volume v was read at. Neither takes the status or
// the node from v: only the binding of a MicroVM pins a Volume, and storage
// changes as Volume.ValidateResize allows. An update that clears the last finalizer of a Volume being deleted removes it.
func (s *service) PutVolume(ctx context.Context, name string, v *api.Volume) (*api.Volume, error) {
	if err := checkName(name, v.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, volumes, "", operationOf(v.Metadata.ResourceVersion), v); err != nil {
		return nil, err
	}
	if v.Metadata.ResourceVersion != "" {
		return s.updateVolume(ctx, v)
	}
	if err := v.ValidateCreate(); err != nil {
		return nil, err
	}
	if err := v.Metadata.ValidateCreate(); err != nil {
		return nil, err
	}
	v.Metadata.Generation, v.Metadata.DeletionTimestamp = 1, nil
	v.Status = api.VolumeStatus{Phase: api.VolumePending}
	if err := s.createFromSnapshot(ctx, v); err != nil {
		return nil, err
	}
	if err := validate(ctx, s, volumes, "", api.OperationCreate, v, nil); err != nil {
		return nil, err
	}
	return v, volumes.create(ctx, s.st, v)
}

// createFromSnapshot applies Volume.CreateFromSnapshot to v with the stored
// VolumeSnapshot v.spec.dataSource names, read just before the write and not
// in its transaction.
func (s *service) createFromSnapshot(ctx context.Context, v *api.Volume) error {
	if v.Spec.DataSource == "" {
		return nil
	}
	snap, _, err := volumeSnapshots.get(ctx, s.st, v.Spec.DataSource)
	if errors.Is(err, api.ErrNotFound) {
		return v.CreateFromSnapshot(nil)
	}
	if err != nil {
		return err
	}
	return v.CreateFromSnapshot(snap)
}

func (s *service) updateVolume(ctx context.Context, v *api.Volume) (*api.Volume, error) {
	old, rev, err := volumes.get(ctx, s.st, v.Metadata.Name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(volumes.plural, v.Metadata.Name, v.Metadata.ResourceVersion, old.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := v.Metadata.ValidateUpdate(old.Metadata); err != nil {
		return nil, err
	}
	v.Metadata.UID = old.Metadata.UID
	class, err := s.storageClassOf(ctx, v.Spec.StorageClass)
	if err != nil {
		return nil, err
	}
	if err := v.ValidateResize(old, class); err != nil {
		return nil, err
	}
	deleting := old.Metadata.DeletionTimestamp != nil
	if deleting && slices.ContainsFunc(v.Metadata.Finalizers, func(f string) bool { return !slices.Contains(old.Metadata.Finalizers, f) }) {
		return nil, api.NewAPIError(api.ReasonInvalid, "a finalizer cannot be added to a volume being deleted")
	}
	if deleting && len(v.Metadata.Finalizers) == 0 {
		if err := validate(ctx, s, volumes, "", api.OperationUpdate, v, old); err != nil {
			return nil, err
		}
		old.Metadata.Finalizers = nil
		return old, volumes.remove(ctx, s.st, old, rev)
	}
	v.Status = old.Status
	v.Metadata.DeletionTimestamp = old.Metadata.DeletionTimestamp
	v.Metadata.Generation = nextGeneration(old.Metadata.Generation, old.Spec, sameSize(old.Spec, v.Spec))
	if err := validate(ctx, s, volumes, "", api.OperationUpdate, v, old); err != nil {
		return nil, err
	}
	return v, volumes.update(ctx, s.st, v, rev)
}

// sameSize returns next with the storage of old where both are the same size,
// so a size written in another unit is no change of spec.
func sameSize(old, next api.VolumeSpec) api.VolumeSpec {
	a, errA := old.Storage.Value()
	b, errB := next.Storage.Value()
	if errA == nil && errB == nil && a.Cmp(b) == 0 {
		next.Storage = old.Storage
	}
	return next
}

// DeleteVolume sets the deletion time of the Volume if rv is its current
// version, and removes it when it holds no finalizer. A Volume already being
// deleted is returned as it is.
func (s *service) DeleteVolume(ctx context.Context, name string, rv api.ResourceVersion) (*api.Volume, error) {
	v, rev, err := volumes.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(volumes.plural, name, rv, v.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if v.Metadata.DeletionTimestamp != nil {
		return v, nil
	}
	if err := admitDelete(ctx, s, volumes, v); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	v.Metadata.DeletionTimestamp = &now
	if len(v.Metadata.Finalizers) > 0 {
		return v, volumes.update(ctx, s.st, v, rev)
	}
	return v, volumes.remove(ctx, s.st, v, rev)
}

func (s *service) GetVolume(ctx context.Context, name string) (*api.Volume, error) {
	v, _, err := volumes.get(ctx, s.st, name)
	return v, err
}

func (s *service) ListVolumes(ctx context.Context, sel api.Selector) (*api.VolumeList, error) {
	items, rv, err := volumes.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.VolumeList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolumeList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchVolumes(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return volumes.watch(ctx, s.st, rv, sel)
}
