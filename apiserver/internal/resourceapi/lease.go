package resourceapi

import (
	"context"
	"errors"

	"github.com/garamsh/gaon/lib/api"
)

// PutLease creates l, or renews the stored Lease l was read at, if a Node of
// the same name exists.
func (s *service) PutLease(ctx context.Context, name string, l *api.Lease) (*api.Lease, error) {
	if err := checkName(name, l.Metadata.Name); err != nil {
		return nil, err
	}
	if err := l.Validate(); err != nil {
		return nil, err
	}
	if _, _, err := nodes.get(ctx, s.st, name); err != nil {
		if errors.Is(err, api.ErrNotFound) {
			return nil, api.NewAPIError(api.ReasonInvalid, "lease %q is named as no existing node", name)
		}
		return nil, err
	}
	return leases.put(ctx, s.st, l, func() error { return nil }, func(*api.Lease) error { return nil })
}
