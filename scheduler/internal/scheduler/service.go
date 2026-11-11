package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// The condition the Scheduler writes on a Machine it cannot place.
const (
	ConditionScheduled  = "Scheduled"
	ReasonUnschedulable = "Unschedulable"
)

// Service binds Machines to Nodes.
type Service interface {
	// Reconcile makes one pass: it binds each unbound Machine, and rebinds
	// each Machine whose Node has LeaseExpired true, to a Node that fits it.
	// A Machine changed by someone else meanwhile is left for the next pass.
	Reconcile(ctx context.Context) error
	// Run reconciles at once and then every interval until ctx ends. It logs
	// a failed pass and carries on.
	Run(ctx context.Context, interval time.Duration)
}

type service struct {
	client API
	log    *slog.Logger
}

// NewService returns a Service that reads and writes through client.
func NewService(client API, log *slog.Logger) Service {
	return &service{client: client, log: log}
}

func (s *service) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("reconcile", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *service) Reconcile(ctx context.Context) error {
	nodes, err := s.client.ListNodes(ctx)
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	machines, err := s.client.ListMachines(ctx)
	if err != nil {
		return fmt.Errorf("list machines: %w", err)
	}
	book, err := newLedger(nodes.Items, machines.Items)
	if err != nil {
		return err
	}
	expired := map[string]bool{}
	for i := range nodes.Items {
		expired[nodes.Items[i].Metadata.Name] = leaseExpired(&nodes.Items[i])
	}

	items := slices.SortedFunc(slices.Values(machines.Items), func(a, b api.Machine) int {
		return strings.Compare(a.Metadata.Name, b.Metadata.Name)
	})
	var errs []error
	for i := range items {
		m := &items[i]
		if m.Metadata.DeletionTimestamp != nil {
			continue
		}
		rebinding := m.Spec.NodeName != ""
		if rebinding && !expired[m.Spec.NodeName] {
			continue
		}
		if err := s.place(ctx, book, m, rebinding); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// place binds m to the first Node that fits it. An unbound m that fits none
// gets a Scheduled condition saying why; a rebound m keeps the status of its
// dead Node's epoch, which nobody rewrites.
func (s *service) place(ctx context.Context, book *ledger, m *api.Machine, rebinding bool) error {
	r, err := requests(m)
	if err != nil {
		return err
	}
	node, ok := book.choose(r)
	if !ok {
		if rebinding {
			return nil
		}
		return s.reportUnschedulable(ctx, book, m)
	}
	_, err = s.client.BindMachine(ctx, m.Metadata.Name, api.Binding{
		ResourceVersion: m.Metadata.ResourceVersion,
		NodeName:        node,
		BindingEpoch:    m.Spec.BindingEpoch + 1,
	})
	if lostRace(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("bind machine %s: %w", m.Metadata.Name, err)
	}
	book.take(node, r)
	s.log.Info("bound", "machine", m.Metadata.Name, "node", node, "epoch", m.Spec.BindingEpoch+1)
	return nil
}

func (s *service) reportUnschedulable(ctx context.Context, book *ledger, m *api.Machine) error {
	want := api.Condition{
		Type:   ConditionScheduled,
		Status: api.ConditionFalse,
		Reason: ReasonUnschedulable,
		Message: fmt.Sprintf("no Node fits: %d of %d Nodes are Admitted, Ready and not LeaseExpired, and none of them has free cpu and memory for the requests (cpu %q, memory %q)",
			len(book.names), book.nodes, m.Spec.Resources.Requests.CPU, m.Spec.Resources.Requests.Memory),
	}
	next := *m
	next.Status.Conditions = slices.Clone(m.Status.Conditions)
	i := slices.IndexFunc(next.Status.Conditions, func(c api.Condition) bool { return c.Type == ConditionScheduled })
	switch {
	case i < 0:
		next.Status.Conditions = append(next.Status.Conditions, want)
	case sameCondition(next.Status.Conditions[i], want):
		return nil
	default:
		next.Status.Conditions[i] = want
	}
	if next.Status.Phase == "" {
		next.Status.Phase = api.MachinePending
	}
	_, err := s.client.PutMachineStatus(ctx, &next)
	if lostRace(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("report machine %s unschedulable: %w", m.Metadata.Name, err)
	}
	return nil
}

func sameCondition(a, b api.Condition) bool {
	return a.Status == b.Status && a.Reason == b.Reason && a.Message == b.Message
}

// lostRace reports whether err says the Machine changed or went away since
// it was read, which the next pass sees for itself.
func lostRace(err error) bool {
	return errors.Is(err, api.ErrConflict) || errors.Is(err, api.ErrNotFound)
}
