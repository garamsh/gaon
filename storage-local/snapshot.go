package storagelocal

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"google.golang.org/grpc/status"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// The reasons of the Snapshotting condition of a VolumeSnapshot not yet ready.
const (
	// ReasonSnapshotInProgress is the reason while the snapshot is taken and
	// not finished.
	ReasonSnapshotInProgress = "InProgress"
	// ReasonSnapshotSourceNotReady is the reason while the source Volume is
	// pinned to this Node and has no capacity, because its disk does not exist.
	ReasonSnapshotSourceNotReady = "SourceNotReady"
	// ReasonSnapshotHolderNotSnapshottable is the reason while mvmd answers
	// that the Machine holding the disk cannot be snapshotted.
	ReasonSnapshotHolderNotSnapshottable = "HolderNotSnapshottable"
	// ReasonSnapshotFailed is the reason while mvmd answers a snapshot with any
	// other error.
	ReasonSnapshotFailed = "Failed"
)

// Snapshots is the part of the resource API Local uses for VolumeSnapshots.
type Snapshots interface {
	GetVolumeSnapshot(ctx context.Context, name string) (*api.VolumeSnapshot, error)
	ListVolumeSnapshots(ctx context.Context) (*api.VolumeSnapshotList, error)
	PutVolumeSnapshot(ctx context.Context, s *api.VolumeSnapshot) (*api.VolumeSnapshot, error)
	PutVolumeSnapshotStatus(ctx context.Context, s *api.VolumeSnapshot) (*api.VolumeSnapshot, error)
}

// snapshotID is the identifier of the mvmd snapshot of s: "snapshot-" and the
// snapshot's metadata.uid, so a snapshot deleted and made again under the same
// name is another mvmd snapshot. It is also the stem of the request ID, which is
// the same at every attempt.
func snapshotID(s *api.VolumeSnapshot) (string, error) {
	if s.Metadata.UID == "" {
		return "", fmt.Errorf("volume snapshot %s has no uid", s.Metadata.Name)
	}
	return "snapshot-" + string(s.Metadata.UID), nil
}

// snapshotWaiting reports whether s is not ready and not being deleted, which
// holds the deletion of its source Volume's disk.
func snapshotWaiting(s *api.VolumeSnapshot) bool {
	return !s.Status.ReadyToUse && s.Metadata.DeletionTimestamp == nil
}

// unplaced reports whether s carries the agent's finalizer and has no
// status.nodeName, so only its source Volume's pin lets a Node write it.
func unplaced(s *api.VolumeSnapshot) bool {
	return s.Status.NodeName == "" && slices.Contains(s.Metadata.Finalizers, api.NodeAgentFinalizer)
}

// reconcileSnapshots takes each snapshot of a local Volume pinned to this Node
// that is not ready and not being deleted, and deletes each snapshot of this Node
// that is being deleted. A snapshot's failure does not stop the others.
func (l *Local) reconcileSnapshots(ctx context.Context, volumes []api.Volume, snapshots []api.VolumeSnapshot) []error {
	var errs []error
	for i := range snapshots {
		s := &snapshots[i]
		var source *api.Volume
		if j := slices.IndexFunc(volumes, func(v api.Volume) bool { return v.Metadata.Name == s.Spec.VolumeName }); j >= 0 {
			source = &volumes[j]
		}
		var err error
		switch {
		case s.Metadata.DeletionTimestamp != nil && l.holds(s, source):
			err = l.deleteSnapshot(ctx, s, volumes)
		case snapshotWaiting(s) && l.pinned(source):
			err = l.takeSnapshot(ctx, s, source)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("volume snapshot %s: %w", s.Metadata.Name, err))
		}
	}
	return errs
}

// pinned reports whether source is a local Volume pinned to this Node.
func (l *Local) pinned(source *api.Volume) bool {
	return source != nil && source.Spec.StorageClass == api.StorageClassLocal && source.Spec.NodeName == l.node
}

// holds reports whether s is this Node's to delete: its status names this Node,
// or names none and its source Volume is a local one pinned here.
func (l *Local) holds(s *api.VolumeSnapshot, source *api.Volume) bool {
	if s.Status.NodeName != "" {
		return s.Status.NodeName == l.node
	}
	return l.pinned(source)
}

// takeSnapshot adds the agent's finalizer to s, asks mvmd to snapshot the disk
// of s's source and writes s ready with the snapshot's size, removing the
// Snapshotting condition in the same write. Until then s carries the condition
// with the reason that fits the last answer. A write that conflicts ends the
// attempt; the next round tries again.
func (l *Local) takeSnapshot(ctx context.Context, s *api.VolumeSnapshot, source *api.Volume) error {
	if source.Status.Capacity == "" {
		_, err := l.setSnapshotting(ctx, s, ReasonSnapshotSourceNotReady, fmt.Sprintf("the disk of volume %q does not exist yet", source.Metadata.Name))
		return err
	}
	id, err := snapshotID(s)
	if err != nil {
		return err
	}
	if !slices.Contains(s.Metadata.Finalizers, api.NodeAgentFinalizer) {
		next := *s
		next.Metadata.Finalizers = append(slices.Clone(s.Metadata.Finalizers), api.NodeAgentFinalizer)
		written, err := l.snapshots.PutVolumeSnapshot(ctx, &next)
		if err != nil {
			return ignoreChanged(err, "add finalizer")
		}
		s = written
	}
	if findCondition(s.Status.Conditions, api.VolumeSnapshotConditionSnapshotting) == nil {
		written, err := l.setSnapshotting(ctx, s, ReasonSnapshotInProgress, "")
		if err != nil || written == nil {
			return err
		}
		s = written
	}
	_, err = l.runtime.SnapshotDisk(ctx, &v1alpha2.SnapshotDiskRequest{DiskId: diskID(source.Metadata.Name), SnapshotId: id, RequestId: "take-" + id})
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return fmt.Errorf("snapshot disk %s: %w", diskID(source.Metadata.Name), err)
	case v1alpha2.KindOf(err) == v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED:
		_, err = l.setSnapshotting(ctx, s, ReasonSnapshotHolderNotSnapshottable, status.Convert(err).Message())
		return err
	default:
		_, err = l.setSnapshotting(ctx, s, ReasonSnapshotFailed, status.Convert(err).Message())
		return err
	}
	listed, err := l.runtime.ListDiskSnapshots(ctx, &v1alpha2.ListDiskSnapshotsRequest{})
	if err != nil {
		return fmt.Errorf("list disk snapshots: %w", err)
	}
	i := slices.IndexFunc(listed.GetDiskSnapshots(), func(d *v1alpha2.DiskSnapshot) bool { return d.GetId() == id })
	if i < 0 {
		return fmt.Errorf("disk snapshot %s is not listed after it was taken", id)
	}
	next := *s
	next.Status = api.VolumeSnapshotStatus{
		ReadyToUse:   true,
		RestoreSize:  api.Quantity(strconv.FormatUint(listed.GetDiskSnapshots()[i].GetSizeBytes(), 10)),
		StorageClass: api.StorageClassLocal,
		NodeName:     l.node,
		Conditions:   withCondition(s.Status.Conditions, api.VolumeSnapshotConditionSnapshotting, nil),
	}
	_, err = l.snapshots.PutVolumeSnapshotStatus(ctx, &next)
	return ignoreChanged(err, "write status")
}

// setSnapshotting writes s with a Snapshotting condition of reason and message,
// unless it carries that one already. It returns s as written, or nil when the
// write conflicted and the round ends.
func (l *Local) setSnapshotting(ctx context.Context, s *api.VolumeSnapshot, reason, message string) (*api.VolumeSnapshot, error) {
	c := &api.Condition{Type: api.VolumeSnapshotConditionSnapshotting, Status: api.ConditionTrue, Reason: reason, Message: message}
	if have := findCondition(s.Status.Conditions, c.Type); have != nil && have.Reason == reason && have.Message == message {
		return s, nil
	}
	next := *s
	next.Status.Conditions = withCondition(s.Status.Conditions, c.Type, c)
	written, err := l.snapshots.PutVolumeSnapshotStatus(ctx, &next)
	if err != nil {
		return nil, ignoreChanged(err, "write status")
	}
	return written, nil
}

// deleteSnapshot deletes the mvmd snapshot of the deleting s and removes the
// agent's finalizer, changing no other entry, unless a Volume that names s has
// no capacity and is not being deleted: that Volume's disk is not made yet and
// needs the snapshot.
func (l *Local) deleteSnapshot(ctx context.Context, s *api.VolumeSnapshot, volumes []api.Volume) error {
	if !slices.Contains(s.Metadata.Finalizers, api.NodeAgentFinalizer) {
		return nil
	}
	if slices.ContainsFunc(volumes, func(v api.Volume) bool {
		return v.Spec.DataSource == s.Metadata.Name && v.Status.Capacity == "" && v.Metadata.DeletionTimestamp == nil
	}) {
		return nil
	}
	id, err := snapshotID(s)
	if err != nil {
		return err
	}
	if _, err := l.runtime.DeleteDiskSnapshot(ctx, &v1alpha2.DeleteDiskSnapshotRequest{SnapshotId: id}); err != nil && v1alpha2.KindOf(err) != v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND {
		return fmt.Errorf("delete disk snapshot %s: %w", id, err)
	}
	next := *s
	next.Metadata.Finalizers = slices.DeleteFunc(slices.Clone(s.Metadata.Finalizers), func(f string) bool { return f == api.NodeAgentFinalizer })
	_, err = l.snapshots.PutVolumeSnapshot(ctx, &next)
	return ignoreChanged(err, "remove finalizer")
}

// ignoreChanged returns nil for a write that conflicted or found its object
// gone, which the next round reads again, and err wrapped with what for any
// other.
func ignoreChanged(err error, what string) error {
	if err == nil || errors.Is(err, api.ErrConflict) || errors.Is(err, api.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("%s: %w", what, err)
}

func findCondition(conditions []api.Condition, typ string) *api.Condition {
	i := slices.IndexFunc(conditions, func(c api.Condition) bool { return c.Type == typ })
	if i < 0 {
		return nil
	}
	return &conditions[i]
}

// withCondition returns conditions with the one of type typ replaced by c, or
// removed when c is nil, and every other kept.
func withCondition(conditions []api.Condition, typ string, c *api.Condition) []api.Condition {
	out := slices.DeleteFunc(slices.Clone(conditions), func(x api.Condition) bool { return x.Type == typ })
	if c != nil {
		out = append(out, *c)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
