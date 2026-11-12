package microvms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc/status"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/clock"
)

// Params are the periods the MicroVM loop keeps.
type Params struct {
	// Resync is the time between two reconciliations of every MicroVM, which
	// is also how soon a start mvmd refused or a failed call is tried again.
	Resync time.Duration
	// Retry is the wait before listing again after a list or a watch failed.
	Retry time.Duration
	// RestartBackoff is the wait before the first restart of a MicroVM, which
	// doubles with each restart up to RestartBackoffMax.
	RestartBackoff    time.Duration
	RestartBackoffMax time.Duration
	// RestartResetAfter is how long a MicroVM runs before the wait starts
	// again from RestartBackoff.
	RestartResetAfter time.Duration
	// ReadTimeout bounds the read of an entrypoint's record.
	ReadTimeout time.Duration
}

// Service runs the MicroVMs bound to the node.
type Service interface {
	// Run lists the MicroVMs, watches them and reconciles on every change and
	// every Params.Resync, until ctx ends.
	Run(ctx context.Context)
}

type service struct {
	node    string
	params  Params
	api     API
	runtime Runtime
	network Network
	storage Storage
	clock   clock.Clock
	log     *slog.Logger
	cache   *cache
	runs    map[string]*run
}

// NewService returns a Service for the MicroVMs bound to the node named node,
// which creates each with the policy network gives and the disks storage
// readies.
func NewService(node string, params Params, api API, runtime Runtime, network Network, storage Storage, clk clock.Clock, log *slog.Logger) Service {
	return &service{node: node, params: params, api: api, runtime: runtime, network: network, storage: storage, clock: clk, log: log, cache: newCache(node), runs: map[string]*run{}}
}

func (s *service) Run(ctx context.Context) {
	for {
		if err := s.session(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("microvm session ended", "node", s.node, "error", err)
		}
		if err := s.clock.Sleep(ctx, s.params.Retry); err != nil {
			return
		}
	}
}

type event struct {
	ev  api.Event[api.MicroVM]
	err error
}

// session lists the MicroVMs, reconciles them all, and then follows the
// watch from the version of the list, until the watch or a list fails.
func (s *service) session(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	list, err := s.api.ListMicroVMs(ctx)
	if err != nil {
		return fmt.Errorf("list microvms: %w", err)
	}
	s.cache.replace(list.Items)
	s.logErr(s.reconcileAll(ctx))
	w, err := s.api.WatchMicroVMs(ctx, list.Metadata.ResourceVersion)
	if err != nil {
		return fmt.Errorf("watch microvms: %w", err)
	}
	defer func() { _ = w.Close() }()
	events := make(chan event)
	go func() {
		for {
			ev, err := w.Next()
			select {
			case events <- event{ev, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	ticks := make(chan struct{}, 1)
	go func() {
		for s.clock.Sleep(ctx, s.params.Resync) == nil {
			select {
			case ticks <- struct{}{}:
			default:
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
			s.logErr(s.reconcileAll(ctx))
		case e := <-events:
			if e.err != nil {
				if errors.Is(e.err, api.ErrExpired) {
					return fmt.Errorf("watch expired: %w", e.err)
				}
				return fmt.Errorf("watch: %w", e.err)
			}
			if s.observe(e.ev.Type, e.ev.Object) {
				s.logErr(s.reconcileAll(ctx))
			}
		}
	}
}

// observe applies one change to the cache and reports whether it was taken.
func (s *service) observe(t api.EventType, m api.MicroVM) bool {
	if t == api.EventDeleted {
		return s.cache.forget(m)
	}
	return s.cache.observe(m)
}

func (s *service) logErr(err error) {
	if err != nil {
		s.log.Warn("reconcile", "node", s.node, "error", err)
	}
}

func reasonOf(err error) string {
	return status.Convert(err).Message()
}
