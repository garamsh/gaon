package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"slices"
	"strconv"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/machines"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// LocalName is the name that selects the Local plugin.
const LocalName = "local"

// The reasons a Machine fails with when Local refuses it.
const (
	// ReasonVolumeNotFound is the reason of a Machine that names a Volume
	// that does not exist.
	ReasonVolumeNotFound = "VolumeNotFound"
	// ReasonUnsupportedStorageClass is the reason of a Machine that names a
	// Volume of a class other than local.
	ReasonUnsupportedStorageClass = "UnsupportedStorageClass"
	// ReasonVolumeNotUsable is the reason of a Machine that names a Volume
	// that is being deleted or is pinned to another Node.
	ReasonVolumeNotUsable = "VolumeNotUsable"
	// ReasonVolumeInUse is the reason of a Machine that names a Volume
	// attached to another Machine.
	ReasonVolumeInUse = "VolumeInUse"
	// ReasonVolumeFailed is the reason of a Machine that names a Volume
	// whose disk mvmd cannot make.
	ReasonVolumeFailed = "VolumeFailed"
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

// Local serves the Volumes of class local pinned to its Node as disks on
// mvmd, named after the Volume. Attach makes the disk of each Volume a
// Machine names and writes the Volume's attachment; Run keeps each Volume's
// phase and capacity, and deletes the disk of a Volume being deleted that no
// Machine holds.
type Local struct {
	node    string
	resync  time.Duration
	volumes Volumes
	runtime machines.Runtime
	clk     clock.Clock
	log     *slog.Logger
}

// NewLocal returns a Local for the Node node, reconciling every resync.
func NewLocal(node string, resync time.Duration, volumes Volumes, runtime machines.Runtime, clk clock.Clock, log *slog.Logger) *Local {
	return &Local{node: node, resync: resync, volumes: volumes, runtime: runtime, clk: clk, log: log}
}

func diskID(volume string) string { return "volume-" + volume }

// Attach checks each Volume m names, then makes its disk if it is missing,
// writes it Bound to m at its epoch, and returns the disks. It refuses m for a
// Volume that is missing, of another class, being deleted, pinned elsewhere,
// attached to another Machine or whose disk cannot be made. It writes nothing
// of the Volumes before every one has passed the checks.
func (l *Local) Attach(ctx context.Context, m *api.Machine) ([]*v1alpha2.DiskAttachment, error) {
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
	disks := make([]*v1alpha2.DiskAttachment, 0, len(names))
	for _, name := range names {
		if err := l.bind(ctx, m, name); err != nil {
			return nil, err
		}
		disks = append(disks, &v1alpha2.DiskAttachment{DiskId: diskID(name)})
	}
	return disks, nil
}

// inspect reads the Volume name and returns it, or the refusal of m for it.
func (l *Local) inspect(ctx context.Context, m *api.Machine, name string) (*api.Volume, error) {
	v, err := l.volumes.GetVolume(ctx, name)
	if errors.Is(err, api.ErrNotFound) {
		return nil, &machines.Refusal{Reason: ReasonVolumeNotFound, Message: fmt.Sprintf("volume %q does not exist", name)}
	}
	if err != nil {
		return nil, fmt.Errorf("read volume %s: %w", name, err)
	}
	switch {
	case v.Spec.StorageClass != api.StorageClassLocal:
		return nil, &machines.Refusal{Reason: ReasonUnsupportedStorageClass, Message: fmt.Sprintf("volume %q is of class %q, which the local plugin does not serve", name, v.Spec.StorageClass)}
	case v.Metadata.DeletionTimestamp != nil:
		return nil, &machines.Refusal{Reason: ReasonVolumeNotUsable, Message: fmt.Sprintf("volume %q is being deleted", name)}
	case v.Spec.NodeName != l.node:
		return nil, &machines.Refusal{Reason: ReasonVolumeNotUsable, Message: fmt.Sprintf("volume %q is pinned to node %q, not %q", name, v.Spec.NodeName, l.node)}
	}
	if a := v.Status.Attachment; a != nil && *a != attachmentOf(m) {
		if err := (&api.Volume{Metadata: v.Metadata, Status: v.Status}).Attach(m.Metadata.Name, m.Spec.BindingEpoch); err != nil {
			return nil, &machines.Refusal{Reason: ReasonVolumeInUse, Message: err.Error()}
		}
	}
	return v, nil
}

func attachmentOf(m *api.Machine) api.Attachment {
	return api.Attachment{MachineName: m.Metadata.Name, BindingEpoch: m.Spec.BindingEpoch}
}

// bind makes the disk of the Volume name and writes the Volume Bound to m. A
// write that conflicts is retried from a fresh read, which is checked again.
func (l *Local) bind(ctx context.Context, m *api.Machine, name string) error {
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
	return &machines.Refusal{Reason: ReasonVolumeFailed, Message: fmt.Sprintf("the disk of volume %q cannot be made: %v", v.Metadata.Name, cause)}
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
// deleted, and Run frees the Volume once no Machine holds the disk.
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

// reconcile writes the status of each local Volume pinned to this Node and
// deletes the disk and drops the finalizer of each such Volume being deleted
// that no Machine holds. One Volume's failure does not stop the others.
func (l *Local) reconcile(ctx context.Context) error {
	list, err := l.volumes.ListVolumes(ctx)
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	disks, err := l.listDisks(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for i := range list.Items {
		v := &list.Items[i]
		if v.Spec.StorageClass != api.StorageClassLocal || v.Spec.NodeName != l.node {
			continue
		}
		var err error
		if v.Metadata.DeletionTimestamp != nil {
			err = l.finish(ctx, v, disks)
		} else {
			err = l.sync(ctx, v, disks)
		}
		if err != nil {
			var refusal *machines.Refusal
			if !errors.As(err, &refusal) {
				errs = append(errs, fmt.Errorf("volume %s: %w", v.Metadata.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// sync writes v's phase and capacity from its disk: Bound while a Machine
// holds the disk, Released once none does but an attachment remains, and
// Available before any. A conflicting write is left to the next round.
func (l *Local) sync(ctx context.Context, v *api.Volume, disks map[string]*v1alpha2.Disk) error {
	disk, err := l.ensureDisk(ctx, v, disks)
	if err != nil {
		return err
	}
	phase := api.VolumeAvailable
	switch {
	case disk.GetHolderMachineId() != "" && v.Status.Attachment != nil:
		phase = api.VolumeBound
	case disk.GetHolderMachineId() != "":
		return nil
	case v.Status.Attachment != nil:
		phase = api.VolumeReleased
	}
	if v.Status.Phase == phase && v.Status.Capacity == capacity(disk) {
		return nil
	}
	next := *v
	next.Status.Phase, next.Status.Capacity = phase, capacity(disk)
	if _, err := l.volumes.PutVolumeStatus(ctx, &next); err != nil && !errors.Is(err, api.ErrConflict) && !errors.Is(err, api.ErrNotFound) {
		return fmt.Errorf("write status: %w", err)
	}
	return nil
}

// finish deletes the disk of the deleting v and removes the agent's finalizer,
// changing no other entry, once mvmd holds the disk for no Machine. A disk
// still held keeps both.
func (l *Local) finish(ctx context.Context, v *api.Volume, disks map[string]*v1alpha2.Disk) error {
	if !slices.Contains(v.Metadata.Finalizers, api.NodeAgentFinalizer) {
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
