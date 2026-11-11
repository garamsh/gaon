package nodelifecycle

import (
	"context"
	"log/slog"

	"github.com/garamsh/gaon/lib/api"
)

// Service is the node lifecycle controller.
type Service interface {
	// Run watches Leases and keeps each Node's LeaseExpired condition true
	// exactly while its Lease has expired. It returns when ctx ends.
	Run(ctx context.Context) error
}

type service struct {
	client *api.Client
	clock  Clock
	cfg    Config
	log    *slog.Logger

	leases map[string]*observation
}

// NewService returns a Service that reads and writes through client and
// measures time on clock.
func NewService(client *api.Client, clock Clock, cfg Config, log *slog.Logger) Service {
	return &service{client: client, clock: clock, cfg: cfg, log: log, leases: map[string]*observation{}}
}
