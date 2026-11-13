package authorization

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	"github.com/garamsh/gaon/lib/api"
)

func (a *rbac) AuthorizeVolumeSnapshotUpdate(ctx context.Context, c Caller, name string, next *api.VolumeSnapshot) error {
	held, err := a.rules(ctx, c)
	if err != nil {
		return err
	}
	granted, err := a.allowed(ctx, held, Request{Group: api.Group, Resource: "volumesnapshots", Verb: api.VerbUpdate, Name: name, Labels: next.Metadata.Labels})
	if err != nil {
		return err
	}
	if granted {
		return nil
	}
	old, err := a.src.GetVolumeSnapshot(ctx, name)
	if err != nil {
		return err
	}
	if !volumeSnapshotFinalizerOnly(old, next) {
		return api.NewAPIError(api.ReasonForbidden, "%q may change only the %s finalizer of volume snapshot %q", c.User, NodeAgentFinalizer, name)
	}
	return nil
}

// volumeSnapshotFinalizerOnly reports whether next differs from old at most in
// the NodeAgentFinalizer entry of metadata.finalizers. The fields the server
// sets itself, and the status, are not compared.
func volumeSnapshotFinalizerOnly(old, next *api.VolumeSnapshot) bool {
	return bytes.Equal(comparableVolumeSnapshot(old), comparableVolumeSnapshot(next))
}

func comparableVolumeSnapshot(s *api.VolumeSnapshot) []byte {
	c := *s
	c.Status = api.VolumeSnapshotStatus{}
	c.Metadata.ResourceVersion, c.Metadata.Generation, c.Metadata.DeletionTimestamp, c.Metadata.UID = "", 0, nil, ""
	c.Metadata.Finalizers = slices.DeleteFunc(slices.Clone(s.Metadata.Finalizers), func(f string) bool { return f == NodeAgentFinalizer })
	b, _ := json.Marshal(c)
	return b
}
