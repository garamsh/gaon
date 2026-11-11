package liveness

import (
	"context"
	"log/slog"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/clock"
)

// Service keeps the node's Lease and mvmd's fencing deadline renewed.
type Service interface {
	// Renew runs one round: it renews the Lease, then the deadline when the
	// Lease leaves room for it. The error joins what failed.
	Renew(ctx context.Context) error
	// Run renews every RenewPeriod until ctx ends.
	Run(ctx context.Context)
}

type service struct {
	node     string
	params   Params
	leases   Leases
	deadline Deadline
	clock    clock.Clock
	log      *slog.Logger

	leaseVersion api.ResourceVersion
	// validUntil ends the validity of the latest acknowledged Lease renewal,
	// counted from the start of that call on the monotonic clock. It is zero
	// before the first acknowledgement.
	validUntil time.Time
	// renewal is the last renewal number sent or seen applied; synced says
	// whether it has been read from mvmd.
	renewal uint64
	synced  bool
}

// NewService returns a Service for the Lease named node.
func NewService(node string, params Params, leases Leases, deadline Deadline, clk clock.Clock, log *slog.Logger) Service {
	return &service{node: node, params: params, leases: leases, deadline: deadline, clock: clk, log: log}
}

func (s *service) Run(ctx context.Context) {
	for {
		if err := s.Renew(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("renewal round", "node", s.node, "error", err)
		}
		if err := s.clock.Sleep(ctx, s.params.RenewPeriod); err != nil {
			return
		}
	}
}
