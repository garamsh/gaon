package liveness

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// Leases is the part of the resource API the renewal uses.
type Leases interface {
	GetLease(ctx context.Context, name string) (*api.Lease, error)
	PutLease(ctx context.Context, l *api.Lease) (*api.Lease, error)
}
