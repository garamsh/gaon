package microvms

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Storage is the plugin that readies a MicroVM's volumes on the node. The
// agent calls Attach before CreateMicroVM and puts the disks in that request,
// and Release after it removes the MicroVM from mvmd.
type Storage interface {
	// Attach readies the volumes of m and returns the disks m is created
	// with. It returns a *Refusal for a MicroVM whose volumes it cannot
	// serve; any other error is retried, so Attach is repeatable for the same
	// MicroVM and epoch.
	Attach(ctx context.Context, m *api.MicroVM) ([]*v1alpha2.DiskAttachment, error)
	// Release gives back what Attach readied for the mvmd machine id. It is
	// repeatable, and called for an id Attach never saw.
	Release(ctx context.Context, id string) error
}
