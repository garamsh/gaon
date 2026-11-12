package job

import (
	"context"
	"log/slog"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// Config configures the Service.
type Config struct {
	// Interval is the time between the end of one sweep and the start of the next.
	Interval time.Duration
}

// Service runs Jobs.
type Service interface {
	// Run registers the Job kind, then sweeps every Config.Interval: for each
	// Job that has not finished it counts the MicroVMs the Job owns, creates
	// the next one when none is running and more are needed, and writes the
	// counts and the completion condition to the status. It returns when ctx
	// ends, or at once when the kind cannot be registered.
	Run(ctx context.Context) error
}

type service struct {
	definitions *api.Client
	jobs        *api.CustomResourceClient
	microvms    *api.Client
	clock       Clock
	cfg         Config
	log         *slog.Logger
}

// NewService returns a Service. definitions is a client rooted at the
// apiextensions group, jobs the client of the Job kind, and microvms a client
// rooted at the compute group that lists only the MicroVMs the unit wrote.
func NewService(definitions *api.Client, jobs *api.CustomResourceClient, microvms *api.Client, clock Clock, cfg Config, log *slog.Logger) Service {
	return &service{definitions: definitions, jobs: jobs, microvms: microvms, clock: clock, cfg: cfg, log: log}
}

func (s *service) Run(ctx context.Context) error {
	if err := s.register(ctx); err != nil {
		return err
	}
	for {
		s.sweep(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.clock.After(s.cfg.Interval):
		}
	}
}
