package resourceapi

import (
	"context"
	"slices"

	"github.com/garamsh/gaon/lib/api"
)

// PutVolumeSnapshot creates s, or changes the finalizers of the stored
// VolumeSnapshot s was read at. A create takes no status, and an update takes
// neither the status nor a change of spec. An update that clears the last
// finalizer of a VolumeSnapshot being deleted removes it.
func (s *service) PutVolumeSnapshot(ctx context.Context, name string, snap *api.VolumeSnapshot) (*api.VolumeSnapshot, error) {
	if err := checkName(name, snap.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, volumeSnapshots, "", operationOf(snap.Metadata.ResourceVersion), snap); err != nil {
		return nil, err
	}
	if snap.Metadata.ResourceVersion != "" {
		return s.updateVolumeSnapshot(ctx, snap)
	}
	if err := snap.Validate(); err != nil {
		return nil, err
	}
	if err := snap.Metadata.ValidateCreate(); err != nil {
		return nil, err
	}
	snap.Metadata.Generation, snap.Metadata.DeletionTimestamp = 1, nil
	snap.Status = api.VolumeSnapshotStatus{}
	if err := validate(ctx, s, volumeSnapshots, "", api.OperationCreate, snap, nil); err != nil {
		return nil, err
	}
	return snap, volumeSnapshots.create(ctx, s.st, snap)
}

func (s *service) updateVolumeSnapshot(ctx context.Context, snap *api.VolumeSnapshot) (*api.VolumeSnapshot, error) {
	old, rev, err := volumeSnapshots.get(ctx, s.st, snap.Metadata.Name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(volumeSnapshots.plural, snap.Metadata.Name, snap.Metadata.ResourceVersion, old.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := snap.Metadata.ValidateUpdate(old.Metadata); err != nil {
		return nil, err
	}
	snap.Metadata.UID = old.Metadata.UID
	if err := snap.ValidateUpdate(old); err != nil {
		return nil, err
	}
	deleting := old.Metadata.DeletionTimestamp != nil
	if deleting && slices.ContainsFunc(snap.Metadata.Finalizers, func(f string) bool { return !slices.Contains(old.Metadata.Finalizers, f) }) {
		return nil, api.NewAPIError(api.ReasonInvalid, "a finalizer cannot be added to a volume snapshot being deleted")
	}
	if deleting && len(snap.Metadata.Finalizers) == 0 {
		if err := validate(ctx, s, volumeSnapshots, "", api.OperationUpdate, snap, old); err != nil {
			return nil, err
		}
		old.Metadata.Finalizers = nil
		return old, volumeSnapshots.remove(ctx, s.st, old, rev)
	}
	snap.Status = old.Status
	snap.Metadata.DeletionTimestamp = old.Metadata.DeletionTimestamp
	snap.Metadata.Generation = old.Metadata.Generation
	if err := validate(ctx, s, volumeSnapshots, "", api.OperationUpdate, snap, old); err != nil {
		return nil, err
	}
	return snap, volumeSnapshots.update(ctx, s.st, snap, rev)
}

// PutVolumeSnapshotStatus replaces the status of the stored VolumeSnapshot
// next was read at. Once the snapshot is ready to use the status write may not
// undo that or change what a Volume was pinned by.
func (s *service) PutVolumeSnapshotStatus(ctx context.Context, name string, next *api.VolumeSnapshot) (*api.VolumeSnapshot, error) {
	if err := checkName(name, next.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, volumeSnapshots, "status", api.OperationUpdate, next); err != nil {
		return nil, err
	}
	snap, rev, err := volumeSnapshots.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(volumeSnapshots.plural, name, next.Metadata.ResourceVersion, snap.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	old := *snap
	snap.Status = next.Status
	if err := snap.ValidateStatusUpdate(&old); err != nil {
		return nil, err
	}
	if err := validate(ctx, s, volumeSnapshots, "status", api.OperationUpdate, snap, &old); err != nil {
		return nil, err
	}
	return snap, volumeSnapshots.update(ctx, s.st, snap, rev)
}

// DeleteVolumeSnapshot sets the deletion time of the VolumeSnapshot if rv is
// its current version, and removes it when it holds no finalizer. One already
// being deleted is returned as it is.
func (s *service) DeleteVolumeSnapshot(ctx context.Context, name string, rv api.ResourceVersion) (*api.VolumeSnapshot, error) {
	snap, rev, err := volumeSnapshots.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(volumeSnapshots.plural, name, rv, snap.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if snap.Metadata.DeletionTimestamp != nil {
		return snap, nil
	}
	if err := admitDelete(ctx, s, volumeSnapshots, snap); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	snap.Metadata.DeletionTimestamp = &now
	if len(snap.Metadata.Finalizers) > 0 {
		return snap, volumeSnapshots.update(ctx, s.st, snap, rev)
	}
	return snap, volumeSnapshots.remove(ctx, s.st, snap, rev)
}

func (s *service) GetVolumeSnapshot(ctx context.Context, name string) (*api.VolumeSnapshot, error) {
	snap, _, err := volumeSnapshots.get(ctx, s.st, name)
	return snap, err
}

func (s *service) ListVolumeSnapshots(ctx context.Context, sel api.Selector) (*api.VolumeSnapshotList, error) {
	items, rv, err := volumeSnapshots.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.VolumeSnapshotList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolumeSnapshotList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchVolumeSnapshots(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return volumeSnapshots.watch(ctx, s.st, rv, sel)
}
