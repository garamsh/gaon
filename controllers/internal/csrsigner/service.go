package csrsigner

import (
	"context"
	"log/slog"

	"github.com/garamsh/gaon/lib/api"
)

// Service is the CertificateSigningRequest signer.
type Service interface {
	// Run lists and watches CertificateSigningRequests and decides each one
	// that has no Approved or Denied condition. It returns when ctx ends.
	Run(ctx context.Context) error
}

type service struct {
	client *api.Client
	cfg    Config
	log    *slog.Logger
}

// NewService returns a Service that reads and writes through client.
func NewService(client *api.Client, cfg Config, log *slog.Logger) Service {
	return &service{client: client, cfg: cfg, log: log}
}
