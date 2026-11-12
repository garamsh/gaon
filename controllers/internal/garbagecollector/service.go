package garbagecollector

import (
	"context"
	"log/slog"

	"github.com/garamsh/gaon/lib/api"
)

// Service is the garbage collector.
type Service interface {
	// Run sweeps every Config.Interval: it deletes each object whose owners
	// are all gone, for every built-in kind and every Established registered
	// kind. It returns when ctx ends.
	Run(ctx context.Context) error
}

// ClientFor returns the client of one kind's objects.
type ClientFor func(t api.CustomResourceType) (*api.CustomResourceClient, error)

type service struct {
	definitions *api.Client
	clientFor   ClientFor
	clock       Clock
	cfg         Config
	log         *slog.Logger
}

// NewService returns a Service that finds registered kinds through
// definitions, a client rooted at the apiextensions group, and reaches the
// objects of every kind through the client clientFor returns.
func NewService(definitions *api.Client, clientFor ClientFor, clock Clock, cfg Config, log *slog.Logger) Service {
	return &service{definitions: definitions, clientFor: clientFor, clock: clock, cfg: cfg, log: log}
}

func (s *service) Run(ctx context.Context) error {
	for {
		s.sweep(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.clock.After(s.cfg.Interval):
		}
	}
}
