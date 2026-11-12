package authorization

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	"github.com/garamsh/gaon/lib/api"
)

func (a *rbac) AuthorizeVolumeUpdate(ctx context.Context, c Caller, name string, next *api.Volume) error {
	held, err := a.rules(ctx, c)
	if err != nil {
		return err
	}
	if covers(held, api.Group, "volumes", string(api.VerbUpdate)) {
		return nil
	}
	old, err := a.src.GetVolume(ctx, name)
	if err != nil {
		return err
	}
	if !volumeFinalizerOnly(old, next) {
		return api.NewAPIError(api.ReasonForbidden, "%q may change only the %s finalizer of volume %q", c.User, NodeAgentFinalizer, name)
	}
	return nil
}

// volumeFinalizerOnly reports whether next differs from old at most in the
// NodeAgentFinalizer entry of metadata.finalizers. The fields the server
// sets itself, and the status, are not compared.
func volumeFinalizerOnly(old, next *api.Volume) bool {
	return bytes.Equal(comparableVolume(old), comparableVolume(next))
}

func comparableVolume(v *api.Volume) []byte {
	c := *v
	c.Status = api.VolumeStatus{}
	c.Metadata.ResourceVersion, c.Metadata.Generation, c.Metadata.DeletionTimestamp = "", 0, nil
	c.Metadata.Finalizers = slices.DeleteFunc(slices.Clone(v.Metadata.Finalizers), func(f string) bool { return f == NodeAgentFinalizer })
	b, _ := json.Marshal(c)
	return b
}
