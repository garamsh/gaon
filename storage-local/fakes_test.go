package storagelocal_test

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// fakeVolumes is the Volumes of the resource API held in memory. Like the API
// server it refuses a write made over another version with api.ErrConflict,
// and removes a Volume being deleted when its last finalizer goes.
type fakeVolumes struct {
	mu      sync.Mutex
	rev     int
	volumes map[string]api.Volume

	statuses []api.VolumeStatus

	snapshots        map[string]api.VolumeSnapshot
	snapshotStatuses []api.VolumeSnapshotStatus
}

func newFakeVolumes() *fakeVolumes {
	return &fakeVolumes{volumes: map[string]api.Volume{}, snapshots: map[string]api.VolumeSnapshot{}}
}

// SetVolume stores v under a new version, as a user or a binding does.
func (f *fakeVolumes) SetVolume(v api.Volume) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rev++
	v.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.rev))
	f.volumes[v.Metadata.Name] = v
}

// SetMicroVM is a no-op: the plugin reads no MicroVM from the API.
func (f *fakeVolumes) SetMicroVM(api.MicroVM) {}

// Volume returns the stored Volume name, and whether it exists.
func (f *fakeVolumes) Volume(name string) (api.Volume, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.volumes[name]
	return v, ok
}

// MarkVolumeDeleting sets deletionTimestamp on the Volume name.
func (f *fakeVolumes) MarkVolumeDeleting(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.volumes[name]
	now := time.Now().UTC()
	v.Metadata.DeletionTimestamp = &now
	f.rev++
	v.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.rev))
	f.volumes[name] = v
}

func (f *fakeVolumes) ListVolumes(context.Context) (*api.VolumeList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := &api.VolumeList{Items: []api.Volume{}}
	for _, v := range f.volumes {
		list.Items = append(list.Items, v)
	}
	slices.SortFunc(list.Items, func(a, b api.Volume) int { return strings.Compare(a.Metadata.Name, b.Metadata.Name) })
	return list, nil
}

func (f *fakeVolumes) GetVolume(_ context.Context, name string) (*api.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.volumes[name]
	if !ok {
		return nil, api.ErrNotFound
	}
	return &v, nil
}

// PutVolume changes the finalizers of a Volume and nothing else.
func (f *fakeVolumes) PutVolume(_ context.Context, next *api.Volume) (*api.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.volumes[next.Metadata.Name]
	if !ok {
		return nil, api.ErrNotFound
	}
	if v.Metadata.ResourceVersion != next.Metadata.ResourceVersion {
		return nil, api.ErrConflict
	}
	f.rev++
	v.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.rev))
	v.Metadata.Finalizers = next.Metadata.Finalizers
	if v.Metadata.DeletionTimestamp != nil && len(v.Metadata.Finalizers) == 0 {
		delete(f.volumes, v.Metadata.Name)
		return &v, nil
	}
	f.volumes[v.Metadata.Name] = v
	return &v, nil
}

// PutVolumeStatus replaces the status of a Volume and nothing else.
func (f *fakeVolumes) PutVolumeStatus(_ context.Context, next *api.Volume) (*api.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.volumes[next.Metadata.Name]
	if !ok {
		return nil, api.ErrNotFound
	}
	if v.Metadata.ResourceVersion != next.Metadata.ResourceVersion {
		return nil, api.ErrConflict
	}
	f.rev++
	v.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.rev))
	v.Status = next.Status
	f.volumes[v.Metadata.Name] = v
	f.statuses = append(f.statuses, v.Status)
	return &v, nil
}

// Statuses returns every status written through PutVolumeStatus, in order.
func (f *fakeVolumes) Statuses() []api.VolumeStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.statuses)
}

// SetSnapshot stores s under a new version, as a user or a provisioner does.
func (f *fakeVolumes) SetSnapshot(s api.VolumeSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rev++
	s.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.rev))
	f.snapshots[s.Metadata.Name] = s
}

// Snapshot returns the stored VolumeSnapshot name, and whether it exists.
func (f *fakeVolumes) Snapshot(name string) (api.VolumeSnapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.snapshots[name]
	return s, ok
}

// MarkSnapshotDeleting sets deletionTimestamp on the VolumeSnapshot name.
func (f *fakeVolumes) MarkSnapshotDeleting(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snapshots[name]
	now := time.Now().UTC()
	s.Metadata.DeletionTimestamp = &now
	f.rev++
	s.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.rev))
	f.snapshots[name] = s
}

// SnapshotStatuses returns every status written through
// PutVolumeSnapshotStatus, in order.
func (f *fakeVolumes) SnapshotStatuses() []api.VolumeSnapshotStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.snapshotStatuses)
}

func (f *fakeVolumes) ListVolumeSnapshots(context.Context) (*api.VolumeSnapshotList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := &api.VolumeSnapshotList{Items: []api.VolumeSnapshot{}}
	for _, s := range f.snapshots {
		list.Items = append(list.Items, s)
	}
	slices.SortFunc(list.Items, func(a, b api.VolumeSnapshot) int { return strings.Compare(a.Metadata.Name, b.Metadata.Name) })
	return list, nil
}

// PutVolumeSnapshot changes the finalizers of a VolumeSnapshot and nothing
// else, and removes one being deleted when its last finalizer goes.
func (f *fakeVolumes) PutVolumeSnapshot(_ context.Context, next *api.VolumeSnapshot) (*api.VolumeSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.snapshots[next.Metadata.Name]
	if !ok {
		return nil, api.ErrNotFound
	}
	if s.Metadata.ResourceVersion != next.Metadata.ResourceVersion {
		return nil, api.ErrConflict
	}
	f.rev++
	s.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.rev))
	s.Metadata.Finalizers = next.Metadata.Finalizers
	if s.Metadata.DeletionTimestamp != nil && len(s.Metadata.Finalizers) == 0 {
		delete(f.snapshots, s.Metadata.Name)
		return &s, nil
	}
	f.snapshots[s.Metadata.Name] = s
	return &s, nil
}

// PutVolumeSnapshotStatus replaces the status of a VolumeSnapshot and nothing
// else.
func (f *fakeVolumes) PutVolumeSnapshotStatus(_ context.Context, next *api.VolumeSnapshot) (*api.VolumeSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.snapshots[next.Metadata.Name]
	if !ok {
		return nil, api.ErrNotFound
	}
	if s.Metadata.ResourceVersion != next.Metadata.ResourceVersion {
		return nil, api.ErrConflict
	}
	f.rev++
	s.Metadata.ResourceVersion = api.ResourceVersion(strconv.Itoa(f.rev))
	s.Status = next.Status
	f.snapshots[s.Metadata.Name] = s
	f.snapshotStatuses = append(f.snapshotStatuses, s.Status)
	return &s, nil
}

// fakeDisks is the disk calls of mvmd's runtime API held in memory: a disk is
// held by at most one machine, and one a machine holds cannot be deleted.
type fakeDisks struct {
	mu            sync.Mutex
	disks         map[string]*v1alpha2.Disk
	calls         []string
	failure       v1alpha2.OutcomeKind
	resizeFailure v1alpha2.OutcomeKind
	onResize      func()
	machines      map[string][]string

	snapshots       map[string]*v1alpha2.DiskSnapshot
	snapshotFailure v1alpha2.OutcomeKind
	onSnapshot      func()
	deleteFailure   v1alpha2.OutcomeKind
}

func newFakeDisks() *fakeDisks {
	return &fakeDisks{disks: map[string]*v1alpha2.Disk{}, machines: map[string][]string{}, snapshots: map[string]*v1alpha2.DiskSnapshot{}}
}

// FailDiskSnapshotsWith makes every SnapshotDisk fail with an error of kind,
// until it is called with OUTCOME_KIND_UNSPECIFIED.
func (f *fakeDisks) FailDiskSnapshotsWith(kind v1alpha2.OutcomeKind) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshotFailure = kind
}

// FailSnapshotDeletesWith makes every DeleteDiskSnapshot fail with an error of
// kind, until it is called with OUTCOME_KIND_UNSPECIFIED.
func (f *fakeDisks) FailSnapshotDeletesWith(kind v1alpha2.OutcomeKind) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteFailure = kind
}

// OnSnapshot sets fn to run at the start of every SnapshotDisk, before it
// answers.
func (f *fakeDisks) OnSnapshot(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSnapshot = fn
}

// AddSnapshot makes a disk snapshot, without recording a call.
func (f *fakeDisks) AddSnapshot(id, diskID string, sizeBytes uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots[id] = &v1alpha2.DiskSnapshot{Id: id, DiskId: diskID, SizeBytes: sizeBytes}
}

// DiskSnapshot returns a copy of the disk snapshot id, and whether mvmd holds
// it.
func (f *fakeDisks) DiskSnapshot(id string) (*v1alpha2.DiskSnapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.snapshots[id]
	if !ok {
		return nil, false
	}
	return proto.Clone(d).(*v1alpha2.DiskSnapshot), true
}

// SnapshotDisk copies the disk as it is, once per snapshot identifier: a repeat
// makes nothing again.
func (f *fakeDisks) SnapshotDisk(_ context.Context, r *v1alpha2.SnapshotDiskRequest, _ ...grpc.CallOption) (*v1alpha2.SnapshotDiskResponse, error) {
	f.mu.Lock()
	fn := f.onSnapshot
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "SnapshotDisk "+r.GetDiskId()+" "+r.GetSnapshotId()+" "+r.GetRequestId())
	if f.snapshotFailure != v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED {
		return nil, v1alpha2.NewError(f.snapshotFailure, "mvmd refuses the snapshot")
	}
	d, ok := f.disks[r.GetDiskId()]
	if !ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such disk")
	}
	if _, ok := f.snapshots[r.GetSnapshotId()]; !ok {
		f.snapshots[r.GetSnapshotId()] = &v1alpha2.DiskSnapshot{Id: r.GetSnapshotId(), DiskId: d.Id, SizeBytes: d.SizeBytes}
	}
	return &v1alpha2.SnapshotDiskResponse{DiskSnapshot: proto.Clone(f.snapshots[r.GetSnapshotId()]).(*v1alpha2.DiskSnapshot)}, nil
}

func (f *fakeDisks) ListDiskSnapshots(context.Context, *v1alpha2.ListDiskSnapshotsRequest, ...grpc.CallOption) (*v1alpha2.ListDiskSnapshotsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &v1alpha2.ListDiskSnapshotsResponse{}
	for _, d := range f.snapshots {
		out.DiskSnapshots = append(out.DiskSnapshots, proto.Clone(d).(*v1alpha2.DiskSnapshot))
	}
	slices.SortFunc(out.DiskSnapshots, func(a, b *v1alpha2.DiskSnapshot) int { return strings.Compare(a.Id, b.Id) })
	return out, nil
}

// DeleteDiskSnapshot removes the snapshot; an absent one succeeds.
func (f *fakeDisks) DeleteDiskSnapshot(_ context.Context, r *v1alpha2.DeleteDiskSnapshotRequest, _ ...grpc.CallOption) (*v1alpha2.DeleteDiskSnapshotResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "DeleteDiskSnapshot "+r.GetSnapshotId())
	if f.deleteFailure != v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED {
		return nil, v1alpha2.NewError(f.deleteFailure, "a call is using the snapshot")
	}
	delete(f.snapshots, r.GetSnapshotId())
	return &v1alpha2.DeleteDiskSnapshotResponse{}, nil
}

// Calls returns the disk calls made, as "<Call> <disk id>".
func (f *fakeDisks) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// FailDiskCreates makes every CreateDisk fail with a refusal while on is true.
func (f *fakeDisks) FailDiskCreates(on bool) {
	kind := v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED
	if on {
		kind = v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED
	}
	f.FailDiskCreatesWith(kind)
}

// FailDiskCreatesWith makes every CreateDisk fail with an error of kind, until
// it is called with OUTCOME_KIND_UNSPECIFIED.
func (f *fakeDisks) FailDiskCreatesWith(kind v1alpha2.OutcomeKind) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failure = kind
}

// AddDisk makes a disk no machine holds, without recording a call.
func (f *fakeDisks) AddDisk(id string, sizeBytes uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disks[id] = &v1alpha2.Disk{Id: id, SizeBytes: sizeBytes}
}

// Disk returns a copy of the disk id, and whether mvmd holds it.
func (f *fakeDisks) Disk(id string) (*v1alpha2.Disk, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.disks[id]
	if !ok {
		return nil, false
	}
	return proto.Clone(d).(*v1alpha2.Disk), true
}

// Hold makes the machine hold its disks, as creating it does.
func (f *fakeDisks) Hold(machine string, diskIDs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range diskIDs {
		f.disks[id].HolderMachineId = machine
	}
	f.machines[machine] = diskIDs
}

// Drop frees the disks the machine holds, as deleting it does.
func (f *fakeDisks) Drop(machine string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.machines[machine] {
		f.disks[id].HolderMachineId = ""
	}
	delete(f.machines, machine)
}

func (f *fakeDisks) CreateDisk(_ context.Context, r *v1alpha2.CreateDiskRequest, _ ...grpc.CallOption) (*v1alpha2.CreateDiskResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "CreateDisk "+r.GetDiskId())
	if f.failure != v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED {
		return nil, v1alpha2.NewError(f.failure, "the node cannot make the disk")
	}
	if _, ok := f.disks[r.GetDiskId()]; ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_CONFLICT, "disk exists")
	}
	d := &v1alpha2.Disk{Id: r.GetDiskId(), SizeBytes: r.GetSizeBytes()}
	f.disks[d.Id] = d
	return &v1alpha2.CreateDiskResponse{Disk: proto.Clone(d).(*v1alpha2.Disk)}, nil
}

func (f *fakeDisks) ListDisks(context.Context, *v1alpha2.ListDisksRequest, ...grpc.CallOption) (*v1alpha2.ListDisksResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &v1alpha2.ListDisksResponse{}
	for _, d := range f.disks {
		out.Disks = append(out.Disks, proto.Clone(d).(*v1alpha2.Disk))
	}
	slices.SortFunc(out.Disks, func(a, b *v1alpha2.Disk) int { return strings.Compare(a.Id, b.Id) })
	return out, nil
}

// FailDiskResizesWith makes every ResizeDisk fail with an error of kind, until
// it is called with OUTCOME_KIND_UNSPECIFIED.
func (f *fakeDisks) FailDiskResizesWith(kind v1alpha2.OutcomeKind) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizeFailure = kind
}

// OnResize sets fn to run at the start of every ResizeDisk, before it answers.
func (f *fakeDisks) OnResize(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onResize = fn
}

func (f *fakeDisks) ResizeDisk(_ context.Context, r *v1alpha2.ResizeDiskRequest, _ ...grpc.CallOption) (*v1alpha2.ResizeDiskResponse, error) {
	f.mu.Lock()
	fn := f.onResize
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "ResizeDisk "+r.GetDiskId()+" "+strconv.FormatUint(r.GetSizeBytes(), 10))
	if f.resizeFailure != v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED {
		return nil, v1alpha2.NewError(f.resizeFailure, "mvmd refuses the growth")
	}
	d, ok := f.disks[r.GetDiskId()]
	switch {
	case !ok:
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such disk")
	case r.GetSizeBytes() < d.SizeBytes:
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED, "size is below the disk's")
	}
	d.SizeBytes = r.GetSizeBytes()
	return &v1alpha2.ResizeDiskResponse{}, nil
}

func (f *fakeDisks) DeleteDisk(_ context.Context, r *v1alpha2.DeleteDiskRequest, _ ...grpc.CallOption) (*v1alpha2.DeleteDiskResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "DeleteDisk "+r.GetDiskId())
	d, ok := f.disks[r.GetDiskId()]
	switch {
	case !ok:
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such disk")
	case d.HolderMachineId != "":
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED, "disk is held by "+d.HolderMachineId)
	}
	delete(f.disks, r.GetDiskId())
	return &v1alpha2.DeleteDiskResponse{}, nil
}
