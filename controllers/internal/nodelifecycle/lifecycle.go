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

// watchResult is one thing a watch delivered: a change to apply to the
// controller's observations, or the error that ended the watch.
type watchResult struct {
	apply func()
	err   error
}

func (s *service) Run(ctx context.Context) error {
	for {
		err := s.watchOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.log.Warn("watch ended", "operation", "watch leases and nodes", "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.clock.After(RetryDelay):
		}
	}
}

// watchOnce lists the Nodes and the Leases, then watches both from their
// lists' versions until a watch fails or ctx ends.
func (s *service) watchOnce(ctx context.Context) error {
	nodes, err := s.client.ListNodes(ctx)
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	leases, err := s.client.ListLeases(ctx)
	if err != nil {
		return fmt.Errorf("list leases: %w", err)
	}
	s.replace(nodes.Items, leases.Items)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan watchResult)
	nw, err := s.client.WatchNodes(ctx, nodes.Metadata.ResourceVersion)
	if err != nil {
		return fmt.Errorf("watch nodes: %w", err)
	}
	defer nw.Close()
	go pump(ctx, nw, results, s.applyNode)
	lw, err := s.client.WatchLeases(ctx, leases.Metadata.ResourceVersion)
	if err != nil {
		return fmt.Errorf("watch leases: %w", err)
	}
	defer lw.Close()
	go pump(ctx, lw, results, s.applyLease)

	for {
		s.reconcile(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-results:
			if r.err != nil {
				return fmt.Errorf("read watch event: %w", r.err)
			}
			r.apply()
		case <-s.wake():
		}
	}
}

// pump sends each event of w to out until w fails or ctx ends.
func pump[T any](ctx context.Context, w *api.Watch[T], out chan<- watchResult, apply func(api.Event[T])) {
	for {
		ev, err := w.Next()
		r := watchResult{apply: func() { apply(ev) }, err: err}
		select {
		case out <- r:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

// replace takes nodes and leases as the full sets: it observes each and
// forgets every Node and Lease not listed.
func (s *service) replace(nodes []api.Node, leases []api.Lease) {
	listed := make(map[string]bool, len(nodes)+len(leases))
	for i := range leases {
		listed[leases[i].Metadata.Name] = true
		s.observeLease(&leases[i])
	}
	for i := range nodes {
		listed[nodes[i].Metadata.Name] = true
		s.observeNode(&nodes[i])
	}
	for name := range s.obs {
		if !listed[name] {
			delete(s.obs, name)
		}
	}
}

func (s *service) applyLease(ev api.Event[api.Lease]) {
	if ev.Type == api.EventDeleted {
		delete(s.obs, ev.Object.Metadata.Name)
		return
	}
	s.observeLease(&ev.Object)
}

func (s *service) applyNode(ev api.Event[api.Node]) {
	if ev.Type != api.EventDeleted {
		s.observeNode(&ev.Object)
		return
	}
	if o := s.obs[ev.Object.Metadata.Name]; o != nil && o.missing {
		delete(s.obs, ev.Object.Metadata.Name)
	}
}

// observeLease starts the validity clock of l when its version is one the
// controller has not seen. A version seen before keeps its first sighting.
func (s *service) observeLease(l *api.Lease) {
	name := l.Metadata.Name
	prev, ok := s.obs[name]
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
	s.obs[name] = o
}

// observeNode starts the validity clock of a Node with no Lease at the first
// sighting of the Node. A Node already observed, with or without a Lease,
// keeps its observation.
func (s *service) observeNode(n *api.Node) {
	name := n.Metadata.Name
	if _, ok := s.obs[name]; ok {
		return
	}
	s.obs[name] = &observation{
		missing:  true,
		seen:     s.clock.Now(),
		validity: s.cfg.DefaultLeaseDuration + s.cfg.Margin,
	}
}

// reconcile brings each Node's condition to what its Lease's validity says.
func (s *service) reconcile(ctx context.Context) {
	now := s.clock.Now()
	for name, o := range s.obs {
		if o.settled(now) || now.Before(o.retryAt) {
			continue
		}
		want := o.want(now)
		if err := s.ensure(ctx, name, o, want); err != nil {
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
	for _, o := range s.obs {
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
func (s *service) ensure(ctx context.Context, name string, o *observation, want api.ConditionStatus) error {
	reason, message := o.reason(want)
	for range conflictRetries {
		n, err := s.client.GetNode(ctx, name)
		if errors.Is(err, api.ErrNotFound) && want == api.ConditionFalse {
			return nil
		}
		if err != nil {
			return err
		}
		if !setCondition(n, want, reason, message, s.clock.Now()) {
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
func setCondition(n *api.Node, want api.ConditionStatus, reason, message string, at time.Time) bool {
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
