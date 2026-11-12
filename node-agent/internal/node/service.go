package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Service registers the node and reports its status.
type Service interface {
	// Sync creates the Node when the API has none, then writes its status:
	// state from spec.admitted, capacity and MicroVM prefixes from mvmd's
	// runtime info, and the Ready condition. A prefix that is not a CIDR is
	// logged and left out. When mvmd does not answer, Ready is written false
	// with the error as its message, the stored capacity and prefixes are
	// kept, and Sync returns the error.
	Sync(ctx context.Context) error
	// Register returns once the API has the Node, creating it when it has
	// none and trying again every period when that fails. It returns the
	// context's error when ctx ends first.
	Register(ctx context.Context, period time.Duration) error
	// Run calls Sync every period until ctx ends, whether or not a call
	// succeeded.
	Run(ctx context.Context, period time.Duration)
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

func (s *service) Register(ctx context.Context, period time.Duration) error {
	for {
		_, err := s.register(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() == nil {
			s.log.Warn("node registration", "node", s.name, "error", err)
		}
		if err := s.clock.Sleep(ctx, period); err != nil {
			return err
		}
	}
}

func (s *service) Run(ctx context.Context, period time.Duration) {
	for {
		if err := s.Sync(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("node sync", "node", s.name, "error", err)
		}
		if err := s.clock.Sleep(ctx, period); err != nil {
			return
		}
	}
}

func (s *service) Sync(ctx context.Context) error {
	info, runtimeErr := s.runtime.GetRuntimeInfo(ctx, &v1alpha2.GetRuntimeInfoRequest{})
	var stored api.NodeCapacity
	var prefixes []string
	if runtimeErr == nil {
		var err error
		if stored, err = capacity(info.GetCapacity()); err != nil {
			return err
		}
		prefixes = s.microVMPrefixes(info.GetMachinePrefixes())
	}
	current, err := s.register(ctx)
	if err != nil {
		return err
	}
	next := *current
	next.Status.State = api.NodeStateDiscovered
	if current.Spec.Admitted {
		next.Status.State = api.NodeStateAdmitted
	}
	if runtimeErr == nil {
		next.Status.Capacity = stored
		next.Status.MicroVMPrefixes = prefixes
		next.Status.Conditions = withReady(current.Status.Conditions, api.ConditionTrue, ReasonRuntimeReachable, "", s.clock.Now())
	} else {
		// The stored capacity and prefixes stay: they are what the Node last had.
		next.Status.Conditions = withReady(current.Status.Conditions, api.ConditionFalse, ReasonRuntimeUnreachable, runtimeErr.Error(), s.clock.Now())
	}
	if _, err := s.nodes.PutNodeStatus(ctx, &next); err != nil {
		return fmt.Errorf("put node status: %w", err)
	}
	if runtimeErr != nil {
		return fmt.Errorf("get runtime info: %w", runtimeErr)
	}
	return nil
}

// microVMPrefixes returns the prefixes mvmd reports that are CIDRs, and logs
// each that is not, naming it.
func (s *service) microVMPrefixes(reported []string) []string {
	var prefixes []string
	for _, r := range reported {
		if _, err := netip.ParsePrefix(r); err != nil {
			s.log.Warn("mvmd reports a MicroVM prefix that is not a CIDR; not written to the Node's status", "node", s.name, "prefix", r, "error", err)
			continue
		}
		prefixes = append(prefixes, r)
	}
	return prefixes
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
