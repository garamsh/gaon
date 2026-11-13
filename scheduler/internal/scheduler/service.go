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

// The condition the Scheduler writes on a MicroVM it cannot place.
const (
	ConditionScheduled  = "Scheduled"
	ReasonUnschedulable = "Unschedulable"
	ReasonExtenderError = "ExtenderError"
)

// Service binds MicroVMs to Nodes.
type Service interface {
	// Reconcile makes one pass: it binds each unbound MicroVM, and rebinds
	// each MicroVM whose Node has LeaseExpired true, to a Node that fits it.
	// A MicroVM changed by someone else meanwhile is left for the next pass.
	Reconcile(ctx context.Context) error
	// Run reconciles at once and then every interval until ctx ends. It logs
	// a failed pass and carries on.
	Run(ctx context.Context, interval time.Duration)
}

type service struct {
	client    API
	log       *slog.Logger
	scores    []weightedScore
	extenders []*extender
}

// Option configures a Service.
type Option func(*service)

// WithConfig enables the score plugins and extenders cfg holds.
func WithConfig(cfg Config) Option {
	return func(s *service) { s.scores, s.extenders = cfg.scores, cfg.extenders }
}

// NewService returns a Service that reads and writes through client. With no
// option it enables no score plugin and no extender.
func NewService(client API, log *slog.Logger, opts ...Option) Service {
	s := &service{client: client, log: log}
	for _, o := range opts {
		o(s)
	}
	return s
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
	microvms, err := s.client.ListMicroVMs(ctx)
	if err != nil {
		return fmt.Errorf("list microvms: %w", err)
	}
	var errs []error
	volumes, err := s.listVolumes(ctx)
	if err != nil {
		if !errors.Is(err, api.ErrForbidden) {
			return err
		}
		errs = append(errs, err)
	}
	book, err := newLedger(nodes.Items, microvms.Items)
	if err != nil {
		return err
	}
	expired := map[string]bool{}
	for i := range nodes.Items {
		expired[nodes.Items[i].Metadata.Name] = leaseExpired(&nodes.Items[i])
	}

	items := slices.SortedFunc(slices.Values(microvms.Items), func(a, b api.MicroVM) int {
		return strings.Compare(a.Metadata.Name, b.Metadata.Name)
	})
	for i := range items {
		m := &items[i]
		if m.Metadata.DeletionTimestamp != nil {
			continue
		}
		if volumes == nil && len(m.Spec.Volumes) > 0 {
			continue
		}
		rebinding := m.Spec.NodeName != ""
		if rebinding && !expired[m.Spec.NodeName] {
			continue
		}
		if err := s.place(ctx, book, m, needOf(m, volumes), rebinding); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// listVolumes returns the Volumes by name. After a failed list the map is nil,
// and MicroVMs that name a Volume wait for a pass where it succeeds.
func (s *service) listVolumes(ctx context.Context) (map[string]*api.Volume, error) {
	list, err := s.client.ListVolumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	byName := make(map[string]*api.Volume, len(list.Items))
	for i := range list.Items {
		byName[list.Items[i].Metadata.Name] = &list.Items[i]
	}
	return byName, nil
}

// place binds m to the Node choose picks. An unbound m that fits none, or whose
// placement failed on an extender, gets a Scheduled condition saying why; a
// rebound m keeps the status of its dead Node's epoch, which nobody rewrites.
func (s *service) place(ctx context.Context, book *ledger, m *api.MicroVM, vols volumeNeed, rebinding bool) error {
	r, err := requests(m)
	if err != nil {
		return err
	}
	node, refused, err := s.choose(ctx, book, m, demand{req: r, volumes: vols, selector: m.Spec.NodeSelector})
	if err != nil {
		s.log.Warn("not placed", "microvm", m.Metadata.Name, "err", err)
		if rebinding {
			return nil
		}
		return s.report(ctx, m, ReasonExtenderError, err.Error())
	}
	if node == "" {
		if rebinding {
			return nil
		}
		return s.report(ctx, m, ReasonUnschedulable, fmt.Sprintf(
			"no Node fits: %d of %d Nodes are Admitted, Ready and not LeaseExpired; requests cpu %q, memory %q; %s",
			book.eligible, len(book.names), m.Spec.Resources.Requests.CPU, m.Spec.Resources.Requests.Memory, refused))
	}
	_, err = s.client.BindMicroVM(ctx, m.Metadata.Name, api.Binding{
		ResourceVersion: m.Metadata.ResourceVersion,
		NodeName:        node,
		BindingEpoch:    m.Spec.BindingEpoch + 1,
	})
	if lostRace(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("bind microvm %s: %w", m.Metadata.Name, err)
	}
	book.take(node, r)
	s.log.Info("bound", "microvm", m.Metadata.Name, "node", node, "epoch", m.Spec.BindingEpoch+1)
	return nil
}

// report writes m's Scheduled condition as false with the reason and message
// given, unless it already says that.
func (s *service) report(ctx context.Context, m *api.MicroVM, reason, message string) error {
	want := api.Condition{Type: ConditionScheduled, Status: api.ConditionFalse, Reason: reason, Message: message}
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
		next.Status.Phase = api.MicroVMPending
	}
	_, err := s.client.PutMicroVMStatus(ctx, &next)
	if lostRace(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("report microvm %s as %s: %w", m.Metadata.Name, reason, err)
	}
	return nil
}

func sameCondition(a, b api.Condition) bool {
	return a.Status == b.Status && a.Reason == b.Reason && a.Message == b.Message
}

// lostRace reports whether err says the MicroVM changed or went away since
// it was read, which the next pass sees for itself.
func lostRace(err error) bool {
	return errors.Is(err, api.ErrConflict) || errors.Is(err, api.ErrNotFound)
}
