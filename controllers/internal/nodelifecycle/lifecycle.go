package nodelifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// conflictRetries bounds the re-reads of a Node after a write conflicts.
const conflictRetries = 3

type watchResult struct {
	event api.Event[api.Lease]
	err   error
}

func (s *service) Run(ctx context.Context) error {
	for {
		err := s.watchOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.log.Warn("lease watch ended", "operation", "watch leases", "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.clock.After(RetryDelay):
		}
	}
}

// watchOnce lists the Leases, then watches from the list's version until the
// watch fails or ctx ends.
func (s *service) watchOnce(ctx context.Context) error {
	list, err := s.client.ListLeases(ctx)
	if err != nil {
		return fmt.Errorf("list leases: %w", err)
	}
	s.replace(list.Items)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w, err := s.client.WatchLeases(ctx, list.Metadata.ResourceVersion)
	if err != nil {
		return fmt.Errorf("watch leases: %w", err)
	}
	defer w.Close()
	results := make(chan watchResult)
	go func() {
		for {
			ev, err := w.Next()
			select {
			case results <- watchResult{ev, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		s.reconcile(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-results:
			if r.err != nil {
				return fmt.Errorf("read lease event: %w", r.err)
			}
			s.apply(r.event)
		case <-s.wake():
		}
	}
}

// replace takes leases as the full set: it observes each and forgets the rest.
func (s *service) replace(leases []api.Lease) {
	listed := make(map[string]bool, len(leases))
	for i := range leases {
		listed[leases[i].Metadata.Name] = true
		s.observe(&leases[i])
	}
	for name := range s.leases {
		if !listed[name] {
			delete(s.leases, name)
		}
	}
}

func (s *service) apply(ev api.Event[api.Lease]) {
	if ev.Type == api.EventDeleted {
		delete(s.leases, ev.Object.Metadata.Name)
		return
	}
	s.observe(&ev.Object)
}

// observe starts the validity clock of l when its version is one the
// controller has not seen. A version seen before keeps its first sighting.
func (s *service) observe(l *api.Lease) {
	name := l.Metadata.Name
	prev, ok := s.leases[name]
	if ok && prev.version == l.Metadata.ResourceVersion {
		return
	}
	o := &observation{
		version:  l.Metadata.ResourceVersion,
		seen:     s.clock.Now(),
		validity: time.Duration(l.Spec.DurationSeconds)*time.Second + s.cfg.Margin,
	}
	if ok {
		o.known, o.marked = prev.known, prev.marked
	}
	s.leases[name] = o
}

// reconcile brings each Node's condition to what its Lease's validity says.
func (s *service) reconcile(ctx context.Context) {
	now := s.clock.Now()
	for name, o := range s.leases {
		if o.settled(now) || now.Before(o.retryAt) {
			continue
		}
		want := o.want(now)
		if err := s.ensure(ctx, name, want); err != nil {
			o.retryAt = now.Add(RetryDelay)
			s.log.Warn("write condition failed", "operation", "put node status", "node", name, "condition", api.NodeLeaseExpired, "status", want, "error", err)
			continue
		}
		o.known, o.marked = true, want == api.ConditionTrue
		s.log.Info("condition written", "node", name, "condition", api.NodeLeaseExpired, "status", want)
	}
}

// wake returns a channel that fires when reconcile next has work, or nil when
// it has none until an event arrives.
func (s *service) wake() <-chan time.Time {
	now := s.clock.Now()
	var next time.Time
	for _, o := range s.leases {
		var at time.Time
		switch {
		case !o.settled(now):
			at = o.retryAt
		case !o.marked:
			at = o.expiresAt()
		default:
			continue
		}
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	if next.IsZero() {
		return nil
	}
	return s.clock.After(max(next.Sub(now), 0))
}

// ensure makes the Node's LeaseExpired condition carry want, writing the
// status only when it differs. A Node that does not exist has no condition
// to clear.
func (s *service) ensure(ctx context.Context, name string, want api.ConditionStatus) error {
	for range conflictRetries {
		n, err := s.client.GetNode(ctx, name)
		if errors.Is(err, api.ErrNotFound) && want == api.ConditionFalse {
			return nil
		}
		if err != nil {
			return err
		}
		if !setCondition(n, want, s.clock.Now()) {
			return nil
		}
		_, err = s.client.PutNodeStatus(ctx, n)
		if errors.Is(err, api.ErrConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("node %s changed on every attempt: %w", name, api.ErrConflict)
}

// setCondition sets n's LeaseExpired condition to want and reports whether n
// changed. A missing condition counts as False.
func setCondition(n *api.Node, want api.ConditionStatus, at time.Time) bool {
	reason, message := reasonLeaseRenewed, "the Lease is valid"
	if want == api.ConditionTrue {
		reason, message = reasonLeaseExpired, "the Lease validity has passed"
	}
	for i := range n.Status.Conditions {
		c := &n.Status.Conditions[i]
		if c.Type != api.NodeLeaseExpired {
			continue
		}
		if c.Status == want {
			return false
		}
		c.Status, c.Reason, c.Message, c.LastTransitionTime = want, reason, message, &at
		return true
	}
	if want == api.ConditionFalse {
		return false
	}
	n.Status.Conditions = append(n.Status.Conditions, api.Condition{
		Type: api.NodeLeaseExpired, Status: want, Reason: reason, Message: message, LastTransitionTime: &at,
	})
	return true
}
