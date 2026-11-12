package microvms

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/garamsh/gaon/lib/api"
)

// Finalizer is the entry the agent keeps in metadata.finalizers of every
// MicroVM it runs, from before it first starts the MicroVM until the MicroVM
// is released on mvmd.
const Finalizer = "compute.gaon.garam.sh/node-agent"

// finalizerAttempts bounds how often a write that conflicts is retried from
// a fresh read within one round; the next round tries again.
const finalizerAttempts = 3

// setFinalizer adds the agent's finalizer to the MicroVM name when add is
// true, and removes it when false. It changes nothing else, and no other
// entry of metadata.finalizers. A conflict is retried from a fresh read.
func (s *service) setFinalizer(ctx context.Context, name string, add bool) error {
	for attempt := 0; attempt < finalizerAttempts; attempt++ {
		m, ok := s.cache.microvms[name]
		if !ok || slices.Contains(m.Metadata.Finalizers, Finalizer) == add {
			return nil
		}
		if add {
			m.Metadata.Finalizers = append(slices.Clone(m.Metadata.Finalizers), Finalizer)
		} else {
			m.Metadata.Finalizers = slices.DeleteFunc(slices.Clone(m.Metadata.Finalizers), func(f string) bool { return f == Finalizer })
		}
		written, err := s.api.PutMicroVM(ctx, &m)
		switch {
		case err == nil:
			s.cache.observe(*written)
			return nil
		case errors.Is(err, api.ErrConflict):
			fresh, getErr := s.api.GetMicroVM(ctx, name)
			if getErr != nil {
				return fmt.Errorf("read microvm %s: %w", name, getErr)
			}
			s.observe(api.EventModified, *fresh)
		case errors.Is(err, api.ErrNotFound):
			// The MicroVM is gone; the watch tells the cache.
			return nil
		default:
			return fmt.Errorf("put finalizers of %s: %w", name, err)
		}
	}
	return fmt.Errorf("put finalizers of %s: still conflicting after %d attempts", name, finalizerAttempts)
}

// release removes the agent's finalizer from every MicroVM being deleted that
// carries it, is bound to this node and is no longer held by mvmd under any
// epoch. held names the MicroVMs mvmd still holds.
func (s *service) release(ctx context.Context, held map[string]bool) error {
	var errs []error
	for _, name := range s.cache.names() {
		m := s.cache.microvms[name]
		if m.Spec.NodeName != s.node || m.Metadata.DeletionTimestamp == nil || held[name] || !slices.Contains(m.Metadata.Finalizers, Finalizer) {
			continue
		}
		errs = append(errs, s.setFinalizer(ctx, name, false))
	}
	return errors.Join(errs...)
}
