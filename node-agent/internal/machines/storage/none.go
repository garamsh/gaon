// Package storage holds the built-in storage plugins of the Node Agent.
package storage

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// NoneName is the name that selects the None plugin.
const NoneName = "none"

// None attaches no disk and refuses no Machine.
type None struct{}

// Attach returns no disks.
func (None) Attach(context.Context, *api.Machine) ([]*v1alpha2.DiskAttachment, error) {
	return nil, nil
}

// Release has nothing to give back.
func (None) Release(context.Context, string) error { return nil }
