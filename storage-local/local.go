// Package storagelocal is the storage plugin "local": it serves the Volumes of
// class local pinned to its Node as disks on mvmd. It is built on lib/plugin
// and lib/api, and on mvmd's runtime API client for the disks.
package storagelocal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"slices"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Name is the name the plugin is served under, and --storage-plugin selects it by.
const Name = "local"

// The reasons a MicroVM fails with when Local refuses it.
const (
	// ReasonVolumeNotFound is the reason of a MicroVM that names a Volume
	// that does not exist.
	ReasonVolumeNotFound = "VolumeNotFound"
	// ReasonUnsupportedStorageClass is the reason of a MicroVM that names a
	// Volume of a class other than local.
	ReasonUnsupportedStorageClass = "UnsupportedStorageClass"
	// ReasonVolumeNotUsable is the reason of a MicroVM that names a Volume
	// that is being deleted or is pinned to another Node.
	ReasonVolumeNotUsable = "VolumeNotUsable"
	// ReasonVolumeInUse is the reason of a MicroVM that names a Volume
	// attached to another MicroVM.
	ReasonVolumeInUse = "VolumeInUse"
	// ReasonVolumeFailed is the reason of a MicroVM that names a Volume
	// whose disk mvmd cannot make.
	ReasonVolumeFailed = "VolumeFailed"
)

// The reasons of the Resizing condition of a Volume whose growth is open.
const (
	// ReasonResizeInProgress is the reason while the growth is taken and not
	// finished.
	ReasonResizeInProgress = "InProgress"
	// ReasonResizeHolderNotResizable is the reason while mvmd answers that the
	// Machine holding the disk cannot have it grown.
	ReasonResizeHolderNotResizable = "HolderNotResizable"
	// ReasonResizeFailed is the reason while mvmd answers a growth with any
	// other error.
	ReasonResizeFailed = "Failed"
)

// statusAttempts bounds how often a status write that conflicts is retried
// from a fresh read within one call; the next call or round tries again.
const statusAttempts = 3

// Volumes is the part of the resource API Local uses.
type Volumes interface {
	ListVolumes(ctx context.Context) (*api.VolumeList, error)
	GetVolume(ctx context.Context, name string) (*api.Volume, error)
	PutVolume(ctx context.Context, v *api.Volume) (*api.Volume, error)
	PutVolumeStatus(ctx context.Context, v *api.Volume) (*api.Volume, error)
}

// Disks is the part of mvmd's runtime API Local uses.
type Disks interface {
	CreateDisk(ctx context.Context, in *v1alpha2.CreateDiskRequest, opts ...grpc.CallOption) (*v1alpha2.CreateDiskResponse, error)
	ListDisks(ctx context.Context, in *v1alpha2.ListDisksRequest, opts ...grpc.CallOption) (*v1alpha2.ListDisksResponse, error)
	DeleteDisk(ctx context.Context, in *v1alpha2.DeleteDiskRequest, opts ...grpc.CallOption) (*v1alpha2.DeleteDiskResponse, error)
	ResizeDisk(ctx context.Context, in *v1alpha2.ResizeDiskRequest, opts ...grpc.CallOption) (*v1alpha2.ResizeDiskResponse, error)
	SnapshotDisk(ctx context.Context, in *v1alpha2.SnapshotDiskRequest, opts ...grpc.CallOption) (*v1alpha2.SnapshotDiskResponse, error)
	ListDiskSnapshots(ctx context.Context, in *v1alpha2.ListDiskSnapshotsRequest, opts ...grpc.CallOption) (*v1alpha2.ListDiskSnapshotsResponse, error)
	DeleteDiskSnapshot(ctx context.Context, in *v1alpha2.DeleteDiskSnapshotRequest, opts ...grpc.CallOption) (*v1alpha2.DeleteDiskSnapshotResponse, error)
}

// Sleeper waits, for the loop's period.
type Sleeper interface {
	// Sleep waits for d or until ctx ends, and returns the context's error in
	// that case.
	Sleep(ctx context.Context, d time.Duration) error
}

// SystemSleeper waits on a timer.
type SystemSleeper struct{}

// Sleep waits on a timer.
func (SystemSleeper) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Local serves the Volumes of class local pinned to its Node as disks on
// mvmd, named after the Volume. Attach makes the disk of each Volume a
// MicroVM names and writes the Volume's attachment; Run keeps each Volume's
// phase and capacity, grows the disk of a Volume whose storage is above its
// capacity, and deletes the disk of a Volume being deleted that no
// MicroVM holds and no unfinished VolumeSnapshot waits for. Run also takes
// and deletes the VolumeSnapshots of those Volumes (snapshot.go).
type Local struct {
	node      string
	resync    time.Duration
	volumes   Volumes
	snapshots Snapshots
	runtime   Disks
	clk       Sleeper
	log       *slog.Logger
}

// NewLocal returns a Local for the Node node, reconciling every resync.
func NewLocal(node string, resync time.Duration, volumes Volumes, snapshots Snapshots, runtime Disks, clk Sleeper, log *slog.Logger) *Local {
	return &Local{node: node, resync: resync, volumes: volumes, snapshots: snapshots, runtime: runtime, clk: clk, log: log}
}

func diskID(volume string) string { return "volume-" + volume }

// Attach checks each Volume m names, then makes its disk if it is missing,
// writes it Bound to m at its epoch, and returns the disks. It refuses m for a
// Volume that is missing, of another class, being deleted, pinned elsewhere,
// attached to another MicroVM or whose disk cannot be made. It writes nothing
// of the Volumes before every one has passed the checks.
func (l *Local) Attach(ctx context.Context, m *api.MicroVM) ([]plugin.DiskAttachment, error) {
	var names []string
	for _, mv := range m.Spec.Volumes {
		if !slices.Contains(names, mv.Name) {
			names = append(names, mv.Name)
		}
	}
	for _, name := range names {
		if _, err := l.inspect(ctx, m, name); err != nil {
			return nil, err
		}
	}
	disks := make([]plugin.DiskAttachment, 0, len(names))
	for _, name := range names {
		if err := l.bind(ctx, m, name); err != nil {
			return nil, err
		}
		disks = append(disks, plugin.DiskAttachment{DiskID: diskID(name)})
	}
	return disks, nil
}

// inspect reads the Volume name and returns it, or the refusal of m for it.
func (l *Local) inspect(ctx context.Context, m *api.MicroVM, name string) (*api.Volume, error) {
	v, err := l.volumes.GetVolume(ctx, name)
	if errors.Is(err, api.ErrNotFound) {
		return nil, &plugin.Refusal{Reason: ReasonVolumeNotFound, Message: fmt.Sprintf("volume %q does not exist", name)}
	}
	if err != nil {
		return nil, fmt.Errorf("read volume %s: %w", name, err)
	}
	switch {
	case v.Spec.StorageClass != api.StorageClassLocal:
		return nil, &plugin.Refusal{Reason: ReasonUnsupportedStorageClass, Message: fmt.Sprintf("volume %q is of class %q, which the local plugin does not serve", name, v.Spec.StorageClass)}
	case v.Metadata.DeletionTimestamp != nil:
		return nil, &plugin.Refusal{Reason: ReasonVolumeNotUsable, Message: fmt.Sprintf("volume %q is being deleted", name)}
	case v.Spec.NodeName != l.node:
		return nil, &plugin.Refusal{Reason: ReasonVolumeNotUsable, Message: fmt.Sprintf("volume %q is pinned to node %q, not %q", name, v.Spec.NodeName, l.node)}
	}
	if a := v.Status.Attachment; a != nil && *a != attachmentOf(m) {
		if err := (&api.Volume{Metadata: v.Metadata, Status: v.Status}).Attach(m.Metadata.Name, m.Spec.BindingEpoch); err != nil {
			return nil, &plugin.Refusal{Reason: ReasonVolumeInUse, Message: err.Error()}
		}
	}
	return v, nil
}

func attachmentOf(m *api.MicroVM) api.Attachment {
	return api.Attachment{MicroVMName: m.Metadata.Name, BindingEpoch: m.Spec.BindingEpoch}
}

// bind makes the disk of the Volume name and writes the Volume Bound to m. A
// write that conflicts is retried from a fresh read, which is checked again.
func (l *Local) bind(ctx context.Context, m *api.MicroVM, name string) error {
	for attempt := 0; attempt < statusAttempts; attempt++ {
		v, err := l.inspect(ctx, m, name)
		if err != nil {
			return err
		}
		disk, err := l.ensureDisk(ctx, v, nil)
		if err != nil {
			return err
		}
		want := attachmentOf(m)
		if a := v.Status.Attachment; a != nil && *a == want && v.Status.Phase == api.VolumeBound {
			return nil
		}
		next := *v
		next.Status = api.VolumeStatus{Phase: api.VolumeBound, Attachment: &want, Capacity: capacity(disk)}
		_, err = l.volumes.PutVolumeStatus(ctx, &next)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, api.ErrConflict):
			// A changed Volume: read it again and decide again.
		default:
			return fmt.Errorf("write status of volume %s: %w", name, err)
		}
	}
	return fmt.Errorf("write status of volume %s: still conflicting after %d attempts", name, statusAttempts)
}

// ensureDisk returns the disk of v, making it if mvmd has none. disks is the
// listing already read, or nil to read it. A disk mvmd refuses to make is
// written as Failed and refused.
func (l *Local) ensureDisk(ctx context.Context, v *api.Volume, disks map[string]*v1alpha2.Disk) (*v1alpha2.Disk, error) {
	id := diskID(v.Metadata.Name)
	if disks == nil {
		listed, err := l.listDisks(ctx)
		if err != nil {
			return nil, err
		}
		disks = listed
	}
	if d, ok := disks[id]; ok {
		return d, nil
	}
	size, err := sizeBytes(v.Spec.Storage)
	if err != nil {
		return nil, l.fail(ctx, v, err)
	}
	resp, err := l.runtime.CreateDisk(ctx, &v1alpha2.CreateDiskRequest{DiskId: id, RequestId: "create-" + id, SizeBytes: size})
	switch kind := v1alpha2.KindOf(err); {
	case err == nil:
		return resp.GetDisk(), nil
	case kind == v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED, kind == v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED, kind == v1alpha2.OutcomeKind_OUTCOME_KIND_FAILED:
		return nil, l.fail(ctx, v, err)
	default:
		return nil, fmt.Errorf("create disk %s: %w", id, err)
	}
}

// fail writes v Failed, unless it is already, and returns the refusal that
// carries cause. A write that does not land is logged: the refusal stands.
func (l *Local) fail(ctx context.Context, v *api.Volume, cause error) error {
	if v.Status.Phase != api.VolumeFailed {
		next := *v
		next.Status.Phase = api.VolumeFailed
		if _, err := l.volumes.PutVolumeStatus(ctx, &next); err != nil {
			l.log.Warn("write volume status Failed", "volume", v.Metadata.Name, "err", err)
		}
	}
	return &plugin.Refusal{Reason: ReasonVolumeFailed, Message: fmt.Sprintf("the disk of volume %q cannot be made: %v", v.Metadata.Name, cause)}
}

func (l *Local) listDisks(ctx context.Context) (map[string]*v1alpha2.Disk, error) {
	resp, err := l.runtime.ListDisks(ctx, &v1alpha2.ListDisksRequest{})
	if err != nil {
		return nil, fmt.Errorf("list disks: %w", err)
	}
	disks := make(map[string]*v1alpha2.Disk, len(resp.GetDisks()))
	for _, d := range resp.GetDisks() {
		disks[d.GetId()] = d
	}
	return disks, nil
}

// Release has nothing to give back: the disk lives until its Volume is
// deleted, and Run frees the Volume once no MicroVM holds the disk.
func (l *Local) Release(context.Context, string) error { return nil }

// Run reconciles the Volumes every resync until ctx ends.
func (l *Local) Run(ctx context.Context) {
	for {
		if err := l.reconcile(ctx); err != nil && ctx.Err() == nil {
			l.log.Warn("reconcile volumes", "err", err)
		}
		if l.clk.Sleep(ctx, l.resync) != nil {
			return
		}
	}
}

// reconcile takes and deletes the VolumeSnapshots of this Node's Volumes, then
// writes the status of each local Volume pinned to this Node and deletes the
// disk and drops the finalizer of each such Volume being deleted that no
// MicroVM holds and no unfinished snapshot waits for. One object's failure does
// not stop the others.
func (l *Local) reconcile(ctx context.Context) error {
	list, err := l.volumes.ListVolumes(ctx)
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	snapshots, err := l.snapshots.ListVolumeSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("list volume snapshots: %w", err)
	}
	disks, err := l.listDisks(ctx)
	if err != nil {
		return err
	}
	errs := l.reconcileSnapshots(ctx, list.Items, snapshots.Items)
	for i := range list.Items {
		v := &list.Items[i]
		if v.Spec.StorageClass != api.StorageClassLocal || v.Spec.NodeName != l.node {
			continue
		}
		var err error
		if v.Metadata.DeletionTimestamp != nil {
			err = l.finish(ctx, v, disks, snapshots.Items)
		} else {
			err = l.sync(ctx, v, disks)
		}
		if err != nil {
			var refusal *plugin.Refusal
			if !errors.As(err, &refusal) {
				errs = append(errs, fmt.Errorf("volume %s: %w", v.Metadata.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// sync writes v's phase, capacity and Resizing condition from its disk: Bound
// while a MicroVM holds the disk, Released once none does but an attachment
// remains, and Available before any. A disk held with no attachment yet keeps
// the phase. A conflicting write is left to the next round.
func (l *Local) sync(ctx context.Context, v *api.Volume, disks map[string]*v1alpha2.Disk) error {
	disk, err := l.ensureDisk(ctx, v, disks)
	if err != nil {
		return err
	}
	v, size, resizing, err := l.grow(ctx, v, disk)
	if err != nil || v == nil {
		return err
	}
	phase := api.VolumeAvailable
	switch {
	case disk.GetHolderMachineId() != "" && v.Status.Attachment != nil:
		phase = api.VolumeBound
	case disk.GetHolderMachineId() != "":
		phase = v.Status.Phase
	case v.Status.Attachment != nil:
		phase = api.VolumeReleased
	}
	conditions := withResizing(v.Status.Conditions, resizing)
	if v.Status.Phase == phase && v.Status.Capacity == size && slices.Equal(v.Status.Conditions, conditions) {
		return nil
	}
	next := *v
	next.Status.Phase, next.Status.Capacity, next.Status.Conditions = phase, size, conditions
	if _, err := l.volumes.PutVolumeStatus(ctx, &next); err != nil && !errors.Is(err, api.ErrConflict) && !errors.Is(err, api.ErrNotFound) {
		return fmt.Errorf("write status: %w", err)
	}
	return nil
}

// grow asks mvmd to grow the disk of v while v's storage is above its
// capacity, which is the disk's size when v has none. It returns the Volume as
// last written (nil when a write conflicted and the round ends), the capacity
// to report and the Resizing condition to keep, nil when no growth is open. A
// growth with no condition yet is written InProgress first, so the Volume shows
// it while mvmd works.
func (l *Local) grow(ctx context.Context, v *api.Volume, disk *v1alpha2.Disk) (*api.Volume, api.Quantity, *api.Condition, error) {
	size := capacity(disk)
	have := v.Status.Capacity
	if have == "" {
		have = size
	}
	want, err := sizeBytes(v.Spec.Storage)
	if err != nil {
		return v, size, resizingCondition(ReasonResizeFailed, err.Error()), nil
	}
	if cmp, err := compareQuantity(have, v.Spec.Storage); err != nil || cmp >= 0 {
		return v, size, nil, nil
	}
	if findResizing(v.Status.Conditions) == nil {
		next := *v
		next.Status.Conditions = withResizing(v.Status.Conditions, resizingCondition(ReasonResizeInProgress, ""))
		written, err := l.volumes.PutVolumeStatus(ctx, &next)
		switch {
		case err == nil:
			v = written
		case errors.Is(err, api.ErrConflict), errors.Is(err, api.ErrNotFound):
			return nil, "", nil, nil
		default:
			return nil, "", nil, fmt.Errorf("write status: %w", err)
		}
	}
	_, err = l.runtime.ResizeDisk(ctx, &v1alpha2.ResizeDiskRequest{DiskId: diskID(v.Metadata.Name), SizeBytes: want})
	switch {
	case err == nil:
		return v, api.Quantity(strconv.FormatUint(want, 10)), nil, nil
	case ctx.Err() != nil:
		return nil, "", nil, fmt.Errorf("resize disk %s: %w", disk.GetId(), err)
	case v1alpha2.KindOf(err) == v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED:
		return v, size, resizingCondition(ReasonResizeHolderNotResizable, status.Convert(err).Message()), nil
	default:
		return v, size, resizingCondition(ReasonResizeFailed, status.Convert(err).Message()), nil
	}
}

func resizingCondition(reason, message string) *api.Condition {
	return &api.Condition{Type: api.VolumeConditionResizing, Status: api.ConditionTrue, Reason: reason, Message: message}
}

func findResizing(conditions []api.Condition) *api.Condition {
	for i := range conditions {
		if conditions[i].Type == api.VolumeConditionResizing {
			return &conditions[i]
		}
	}
	return nil
}

// withResizing returns conditions with the Resizing one replaced by c, or
// removed when c is nil, and every other condition kept.
func withResizing(conditions []api.Condition, c *api.Condition) []api.Condition {
	out := slices.DeleteFunc(slices.Clone(conditions), func(x api.Condition) bool { return x.Type == api.VolumeConditionResizing })
	if c != nil {
		out = append(out, *c)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// compareQuantity returns -1, 0 or 1 as a is below, equal to or above b.
func compareQuantity(a, b api.Quantity) (int, error) {
	x, err := a.Value()
	if err != nil {
		return 0, err
	}
	y, err := b.Value()
	if err != nil {
		return 0, err
	}
	return x.Cmp(y), nil
}

// finish deletes the disk of the deleting v and removes the agent's finalizer,
// changing no other entry, once mvmd holds the disk for no MicroVM, no
// VolumeSnapshot of v is waiting to be taken, and none carries the agent's
// finalizer without a status.nodeName: no other Node may delete that one, and
// v's pin is how this Node still does. A disk still held keeps both.
func (l *Local) finish(ctx context.Context, v *api.Volume, disks map[string]*v1alpha2.Disk, snapshots []api.VolumeSnapshot) error {
	if !slices.Contains(v.Metadata.Finalizers, api.NodeAgentFinalizer) {
		return nil
	}
	if slices.ContainsFunc(snapshots, func(s api.VolumeSnapshot) bool {
		return s.Spec.VolumeName == v.Metadata.Name && (snapshotWaiting(&s) || unplaced(&s))
	}) {
		return nil
	}
	id := diskID(v.Metadata.Name)
	if d, ok := disks[id]; ok {
		if d.GetHolderMachineId() != "" {
			return nil
		}
		if _, err := l.runtime.DeleteDisk(ctx, &v1alpha2.DeleteDiskRequest{DiskId: id}); err != nil && v1alpha2.KindOf(err) != v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND {
			return fmt.Errorf("delete disk %s: %w", id, err)
		}
	}
	next := *v
	next.Metadata.Finalizers = slices.DeleteFunc(slices.Clone(v.Metadata.Finalizers), func(f string) bool { return f == api.NodeAgentFinalizer })
	if _, err := l.volumes.PutVolume(ctx, &next); err != nil && !errors.Is(err, api.ErrConflict) && !errors.Is(err, api.ErrNotFound) {
		return fmt.Errorf("remove finalizer: %w", err)
	}
	return nil
}

func capacity(d *v1alpha2.Disk) api.Quantity {
	return api.Quantity(strconv.FormatUint(d.GetSizeBytes(), 10))
}

// sizeBytes returns q as a whole number of bytes, rounded up.
func sizeBytes(q api.Quantity) (uint64, error) {
	v, err := q.Value()
	if err != nil {
		return 0, err
	}
	whole := new(big.Int).Div(new(big.Int).Add(v.Num(), new(big.Int).Sub(v.Denom(), big.NewInt(1))), v.Denom())
	if !whole.IsUint64() || whole.Sign() <= 0 {
		return 0, fmt.Errorf("size %q is out of range", q)
	}
	return whole.Uint64(), nil
}
