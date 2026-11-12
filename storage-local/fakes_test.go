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
}

func newFakeVolumes() *fakeVolumes { return &fakeVolumes{volumes: map[string]api.Volume{}} }

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
	return &v, nil
}

// fakeDisks is the disk calls of mvmd's runtime API held in memory: a disk is
// held by at most one machine, and one a machine holds cannot be deleted.
type fakeDisks struct {
	mu       sync.Mutex
	disks    map[string]*v1alpha2.Disk
	calls    []string
	failure  v1alpha2.OutcomeKind
	machines map[string][]string
}

func newFakeDisks() *fakeDisks {
	return &fakeDisks{disks: map[string]*v1alpha2.Disk{}, machines: map[string][]string{}}
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
