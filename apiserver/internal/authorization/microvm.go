package authorization

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	"github.com/garamsh/gaon/lib/api"
)

// NodeAgentFinalizer is the one finalizer a Node may add to and remove from a
// MicroVM bound to it (resource-api.md, §Common rules).
const NodeAgentFinalizer = "compute.gaon.garam.sh/node-agent"

func (a *rbac) AuthorizeMicroVMUpdate(ctx context.Context, c Caller, name string, next *api.MicroVM) error {
	held, err := a.rules(ctx, c)
	if err != nil {
		return err
	}
	if covers(held, api.Group, "microvms", string(api.VerbUpdate)) {
		return nil
	}
	old, err := a.src.GetMicroVM(ctx, name)
	if err != nil {
		return err
	}
	if !finalizerOnly(old, next) {
		return api.NewAPIError(api.ReasonForbidden, "%q may change only the %s finalizer of microvm %q", c.User, NodeAgentFinalizer, name)
	}
	return nil
}

// finalizerOnly reports whether next differs from old at most in the
// NodeAgentFinalizer entry of metadata.finalizers. The fields the server
// sets itself, and the status, are not compared.
func finalizerOnly(old, next *api.MicroVM) bool {
	return bytes.Equal(comparable(old), comparable(next))
}

func comparable(m *api.MicroVM) []byte {
	c := *m
	c.Status = api.MicroVMStatus{}
	c.Metadata.ResourceVersion, c.Metadata.Generation, c.Metadata.DeletionTimestamp = "", 0, nil
	c.Metadata.Finalizers = slices.DeleteFunc(slices.Clone(m.Metadata.Finalizers), func(f string) bool { return f == NodeAgentFinalizer })
	b, _ := json.Marshal(c)
	return b
}
