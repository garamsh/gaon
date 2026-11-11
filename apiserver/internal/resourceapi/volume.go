package resourceapi

import (
	"context"
	"errors"

	"github.com/garamsh/gaon/lib/api"
)

// PutVolumeStatus replaces the status of the stored Volume v was read at. A
// new attachment must name the epoch of its Machine's current binding, and
// succeeds only where the Volume is unattached or attached at a lower epoch.
func (s *service) PutVolumeStatus(ctx context.Context, name string, next *api.Volume) (*api.Volume, error) {
	if err := checkName(name, next.Metadata.Name); err != nil {
		return nil, err
	}
	v, rev, err := volumes.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(volumes.plural, name, next.Metadata.ResourceVersion, v.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := next.ValidateStatus(); err != nil {
		return nil, err
	}
	if a := next.Status.Attachment; a != nil && !sameJSON(a, v.Status.Attachment) {
		if err := s.checkAttachment(ctx, v, a); err != nil {
			return nil, err
		}
	}
	v.Status = next.Status
	return v, volumes.update(ctx, s.st, v, rev)
}

func (s *service) checkAttachment(ctx context.Context, v *api.Volume, a *api.Attachment) error {
	m, _, err := machines.get(ctx, s.st, a.MachineName)
	if errors.Is(err, api.ErrNotFound) {
		return api.NewAPIError(api.ReasonInvalid, "attachment names machine %q, which does not exist", a.MachineName)
	}
	if err != nil {
		return err
	}
	if a.BindingEpoch != m.Spec.BindingEpoch {
		return api.NewAPIError(api.ReasonInvalid, "attachment epoch is %d, want the machine's current %d", a.BindingEpoch, m.Spec.BindingEpoch)
	}
	return v.Attach(a.MachineName, a.BindingEpoch)
}
