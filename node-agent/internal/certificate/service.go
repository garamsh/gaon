package certificate

import (
	"context"
	"crypto/x509"
	"fmt"
	"log/slog"
	"time"

	"github.com/garamsh/gaon/node-agent/internal/clock"
)

// Service keeps the agent's client certificate.
type Service interface {
	// Start returns once the Holder presents a valid pair. It uses the pair
	// in the directory when that is valid, and otherwise asks for one with
	// the bootstrap requests, which a Service built without them cannot do.
	// A request that is denied, or any failed call, is an error.
	Start(ctx context.Context) error
	// Run renews the pair at two thirds of its validity until ctx ends. A
	// renewal that fails is logged and tried again after a minute, and the
	// pair in use stays.
	Run(ctx context.Context)
}

type service struct {
	params    Params
	holder    *Holder
	bootstrap Requests
	renew     Requests
	clock     clock.Clock
	log       *slog.Logger
}

// NewService returns a Service that presents its pair through holder. Start
// asks for a first certificate through bootstrap, which is nil when the agent
// holds no token, and Run renews through renew, the requests of a client that
// presents the pair.
func NewService(p Params, holder *Holder, bootstrap, renew Requests, clk clock.Clock, log *slog.Logger) Service {
	return &service{params: p, holder: holder, bootstrap: bootstrap, renew: renew, clock: clk, log: log}
}

func (s *service) Start(ctx context.Context) error {
	if err := ensureDir(s.params.Dir); err != nil {
		return err
	}
	pair, loadErr := loadPair(s.params.Dir, s.params.NodeName, s.clock.Now())
	if loadErr == nil {
		s.holder.set(pair)
		return nil
	}
	if s.bootstrap == nil {
		return fmt.Errorf("no valid certificate to use and no bootstrap token to ask for one: %w", loadErr)
	}
	s.log.Info("bootstrapping the client certificate", "node", s.params.NodeName, "reason", loadErr.Error())
	return s.replace(ctx, s.bootstrap)
}

func (s *service) Run(ctx context.Context) {
	for {
		if wait := s.due().Sub(s.clock.Now()); wait > 0 {
			if s.clock.Sleep(ctx, wait) != nil {
				return
			}
		}
		if err := s.replace(ctx, s.renew); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.log.Warn("renew the client certificate", "node", s.params.NodeName, "retry_after", retryAfter, "error", err)
			if s.clock.Sleep(ctx, retryAfter) != nil {
				return
			}
		}
	}
}

// due is the time the current pair is renewed: two thirds into its validity.
func (s *service) due() time.Time {
	leaf := s.holder.get().Leaf
	return leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) * 2 / 3)
}

// replace asks reqs for a certificate with a new key, and when it has one,
// writes the pair and presents it.
func (s *service) replace(ctx context.Context, reqs Requests) error {
	keyPEM, certPEM, err := s.obtain(ctx, reqs)
	if err != nil {
		return err
	}
	pair, err := parsePair(joinPair(certPEM, keyPEM), s.params.NodeName, s.clock.Now())
	if err != nil {
		return fmt.Errorf("issued certificate: %w", err)
	}
	if err := writePair(s.params.Dir, keyPEM, certPEM); err != nil {
		return err
	}
	s.holder.set(pair)
	return nil
}

// checkLeaf reports why leaf is not a certificate for node at now.
func checkLeaf(leaf *x509.Certificate, node string, now time.Time) error {
	if leaf.Subject.CommonName != node {
		return fmt.Errorf("certificate is for %q, want %q", leaf.Subject.CommonName, node)
	}
	if !now.Before(leaf.NotAfter) {
		return fmt.Errorf("certificate expired at %s", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	return nil
}
