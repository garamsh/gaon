package endpoints

import (
	"context"
	"log/slog"

	"github.com/garamsh/gaon/lib/api"
)

// Service is the endpoints controller.
type Service interface {
	// Run watches Services and MicroVMs and keeps the Endpoints of each
	// Service that has a selector equal to its Ready MicroVMs. It returns when
	// ctx ends.
	Run(ctx context.Context) error
}

type service struct {
	client *api.Client
	clock  Clock
	log    *slog.Logger

	services map[string]*api.Service
	microvms map[string]*api.MicroVM
	// pending holds the Services whose Endpoints is to be written.
	pending map[string]bool
}

// NewService returns a Service that reads and writes through client, which is
// rooted at the compute group, and paces its retries on clock.
func NewService(client *api.Client, clock Clock, log *slog.Logger) Service {
	return &service{client: client, clock: clock, log: log}
}
