package csrsigner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// conflictRetries bounds the re-reads of a request after a write conflicts.
const conflictRetries = 3

func (s *service) Run(ctx context.Context) error {
	for {
		err := s.watchOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.log.Warn("watch ended", "operation", "watch certificatesigningrequests", "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(RetryDelay):
		}
	}
}

// watchOnce decides every undecided request in the list, then those the watch
// delivers, until a list, watch or write fails or ctx ends.
func (s *service) watchOnce(ctx context.Context) error {
	list, err := s.client.ListCertificateSigningRequests(ctx)
	if err != nil {
		return fmt.Errorf("list certificatesigningrequests: %w", err)
	}
	for i := range list.Items {
		if err := s.decide(ctx, &list.Items[i]); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w, err := s.client.WatchCertificateSigningRequests(ctx, list.Metadata.ResourceVersion)
	if err != nil {
		return fmt.Errorf("watch certificatesigningrequests: %w", err)
	}
	defer w.Close()
	for {
		ev, err := w.Next()
		if err != nil {
			return fmt.Errorf("read watch event: %w", err)
		}
		if ev.Type == api.EventDeleted {
			continue
		}
		if err := s.decide(ctx, &ev.Object); err != nil {
			return err
		}
	}
}

// decide approves and signs r, or denies it, unless it is already decided. On
// a conflict it reads r again and writes only while r is still undecided.
func (s *service) decide(ctx context.Context, r *api.CertificateSigningRequest) error {
	if decided(r) {
		return nil
	}
	name := r.Metadata.Name
	cond, certificate, err := s.outcome(r)
	if err != nil {
		return fmt.Errorf("decide %s: %w", name, err)
	}
	for range conflictRetries {
		r.Status.Conditions = append(r.Status.Conditions, cond)
		r.Status.Certificate = certificate
		_, err := s.client.PutCertificateSigningRequestStatus(ctx, r)
		if err == nil {
			s.log.Info("request decided", "request", name, "condition", cond.Type, "reason", cond.Reason)
			return nil
		}
		if !errors.Is(err, api.ErrConflict) {
			return fmt.Errorf("write status of %s: %w", name, err)
		}
		if r, err = s.client.GetCertificateSigningRequest(ctx, name); err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if decided(r) {
			return nil
		}
	}
	return fmt.Errorf("request %s changed on every attempt: %w", name, api.ErrConflict)
}

// outcome returns the condition to record for r and, when it is Approved, the
// signed certificate.
func (s *service) outcome(r *api.CertificateSigningRequest) (api.Condition, string, error) {
	at := s.cfg.Now()
	csr, d := approve(r)
	if d != nil {
		return api.Condition{Type: api.CSRDenied, Status: api.ConditionTrue, Reason: d.reason, Message: d.message, LastTransitionTime: &at}, "", nil
	}
	certificate, err := s.sign(csr)
	if err != nil {
		return api.Condition{}, "", err
	}
	return api.Condition{Type: api.CSRApproved, Status: api.ConditionTrue, Reason: reasonApproved, LastTransitionTime: &at}, certificate, nil
}
