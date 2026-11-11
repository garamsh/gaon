package liveness

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

func (s *service) Renew(ctx context.Context) error {
	leaseErr := s.renewLease(ctx)
	deadlineErr := s.renewDeadline(ctx)
	return errors.Join(leaseErr, deadlineErr)
}

// renewLease renews the Lease by compare-and-swap, creating it first when
// the API has none. Any failure forgets the version, so the next round reads
// the Lease again.
func (s *service) renewLease(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.params.RPCTimeout)
	defer cancel()
	if s.leaseVersion == "" {
		current, err := s.leases.GetLease(ctx, s.node)
		switch {
		case err == nil:
			s.leaseVersion = current.Metadata.ResourceVersion
		case !errors.Is(err, api.ErrNotFound):
			return fmt.Errorf("read lease: %w", err)
		}
	}
	start := s.clock.Now()
	renewed, err := s.leases.PutLease(ctx, &api.Lease{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindLease},
		Metadata: api.ObjectMeta{Name: s.node, ResourceVersion: s.leaseVersion},
		Spec: api.LeaseSpec{
			Holder:          s.node,
			RenewTime:       start.UTC(),
			DurationSeconds: int32(s.params.LeaseDuration / time.Second),
		},
	})
	if err != nil {
		s.leaseVersion = ""
		return fmt.Errorf("renew lease: %w", err)
	}
	s.leaseVersion = renewed.Metadata.ResourceVersion
	s.validUntil = start.Add(s.params.LeaseDuration)
	return nil
}

// renewDeadline renews mvmd's deadline for the configured length, only when
// a renewal received as late as the call's timeout still expires, with δ,
// within the Lease.
func (s *service) renewDeadline(ctx context.Context) error {
	if !s.synced {
		inspected, err := s.deadline.InspectDeadline(ctx, &v1alpha2.InspectDeadlineRequest{})
		if err != nil {
			return fmt.Errorf("inspect deadline: %w", err)
		}
		s.renewal = inspected.GetStatus().GetLastRenewalNumber()
		s.synced = true
	}
	latest := s.clock.Now().Add(s.params.RPCTimeout + s.params.DeadlineLength + s.params.Delta)
	if latest.After(s.validUntil) {
		return errNoValidity
	}
	ctx, cancel := context.WithTimeout(ctx, s.params.RPCTimeout)
	defer cancel()
	s.renewal++
	renewed, err := s.deadline.RenewDeadline(ctx, &v1alpha2.RenewDeadlineRequest{
		Length:        durationpb.New(s.params.DeadlineLength),
		RenewalNumber: s.renewal,
	})
	if err != nil {
		return fmt.Errorf("renew deadline: %w", err)
	}
	s.renewal = max(s.renewal, renewed.GetStatus().GetLastRenewalNumber())
	return nil
}
