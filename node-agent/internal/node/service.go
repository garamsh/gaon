package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Service registers the node and reports its status.
type Service interface {
	// Sync creates the Node when the API has none, then writes its status:
	// state from spec.admitted, capacity from mvmd's runtime info, and the
	// Ready condition.
	Sync(ctx context.Context) error
	// Run calls Sync every period until ctx ends, whether or not a call
	// succeeded.
	Run(ctx context.Context, period time.Duration) error
}

type service struct {
	name    string
	nodes   Nodes
	runtime Runtime
	clock   clock.Clock
	log     *slog.Logger
}

// NewService returns a Service for the Node named name.
func NewService(name string, nodes Nodes, runtime Runtime, clk clock.Clock, log *slog.Logger) Service {
	return &service{name: name, nodes: nodes, runtime: runtime, clock: clk, log: log}
}

func (s *service) Run(ctx context.Context, period time.Duration) error {
	for {
		if err := s.Sync(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("node sync", "node", s.name, "error", err)
		}
		if err := s.clock.Sleep(ctx, period); err != nil {
			return nil
		}
	}
}

func (s *service) Sync(ctx context.Context) error {
	info, err := s.runtime.GetRuntimeInfo(ctx, &v1alpha2.GetRuntimeInfoRequest{})
	if err != nil {
		return fmt.Errorf("get runtime info: %w", err)
	}
	capacity, err := capacity(info.GetCapacity())
	if err != nil {
		return err
	}
	current, err := s.register(ctx)
	if err != nil {
		return err
	}
	next := *current
	next.Status.Capacity = capacity
	next.Status.State = api.NodeStateDiscovered
	if current.Spec.Admitted {
		next.Status.State = api.NodeStateAdmitted
	}
	next.Status.Conditions = withReady(current.Status.Conditions, s.clock.Now())
	if _, err := s.nodes.PutNodeStatus(ctx, &next); err != nil {
		return fmt.Errorf("put node status: %w", err)
	}
	return nil
}

// register returns the stored Node, creating it with an empty spec, by a PUT
// without a version, when there is none.
func (s *service) register(ctx context.Context) (*api.Node, error) {
	current, err := s.nodes.GetNode(ctx, s.name)
	if err == nil {
		return current, nil
	}
	if !errors.Is(err, api.ErrNotFound) {
		return nil, fmt.Errorf("get node: %w", err)
	}
	created, err := s.nodes.PutNode(ctx, &api.Node{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNode},
		Metadata: api.ObjectMeta{Name: s.name},
	})
	if err != nil {
		return nil, fmt.Errorf("create node: %w", err)
	}
	return created, nil
}
