package endpoints

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// watchResult is one thing a watch delivered: a change to apply to the
// controller's view, or the error that ended the watch.
type watchResult struct {
	apply func()
	err   error
}

func (s *service) Run(ctx context.Context) error {
	for {
		err := s.watchOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.log.Warn("watch ended", "operation", "watch services and microvms", "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.clock.After(RetryDelay):
		}
	}
}

// watchOnce lists the Services and the MicroVMs, then watches both from their
// lists' versions until a watch fails or ctx ends.
func (s *service) watchOnce(ctx context.Context) error {
	services, err := s.client.ListServices(ctx)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	microvms, err := s.client.ListMicroVMs(ctx)
	if err != nil {
		return fmt.Errorf("list microvms: %w", err)
	}
	s.replace(services.Items, microvms.Items)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan watchResult)
	sw, err := s.client.WatchServices(ctx, services.Metadata.ResourceVersion)
	if err != nil {
		return fmt.Errorf("watch services: %w", err)
	}
	defer sw.Close()
	go pump(ctx, sw, results, s.applyService)
	mw, err := s.client.WatchMicroVMs(ctx, microvms.Metadata.ResourceVersion)
	if err != nil {
		return fmt.Errorf("watch microvms: %w", err)
	}
	defer mw.Close()
	go pump(ctx, mw, results, s.applyMicroVM)

	for {
		s.reconcile(ctx)
		var retry <-chan time.Time
		if len(s.pending) > 0 {
			retry = s.clock.After(RetryDelay)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-results:
			if r.err != nil {
				return fmt.Errorf("read watch event: %w", r.err)
			}
			r.apply()
		case <-retry:
		}
	}
}

// pump sends each event of w to out until w fails or ctx ends.
func pump[T any](ctx context.Context, w *api.Watch[T], out chan<- watchResult, apply func(api.Event[T])) {
	for {
		ev, err := w.Next()
		r := watchResult{apply: func() { apply(ev) }, err: err}
		select {
		case out <- r:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

// replace takes services and microvms as the full sets and marks every Service
// with a selector to be written.
func (s *service) replace(services []api.Service, microvms []api.MicroVM) {
	s.services = make(map[string]*api.Service, len(services))
	s.microvms = make(map[string]*api.MicroVM, len(microvms))
	s.pending = map[string]bool{}
	for i := range microvms {
		s.microvms[microvms[i].Metadata.Name] = &microvms[i]
	}
	for i := range services {
		s.observeService(&services[i])
	}
}

func (s *service) applyService(ev api.Event[api.Service]) {
	if ev.Type == api.EventDeleted {
		delete(s.services, ev.Object.Metadata.Name)
		delete(s.pending, ev.Object.Metadata.Name)
		return
	}
	s.observeService(&ev.Object)
}

// observeService stores svc and marks it to be written when it has a selector.
func (s *service) observeService(svc *api.Service) {
	name := svc.Metadata.Name
	s.services[name] = svc
	if len(svc.Spec.Selector) > 0 {
		s.pending[name] = true
	} else {
		delete(s.pending, name)
	}
}

// applyMicroVM stores the change and marks each Service whose selector selects
// the MicroVM before or after it.
func (s *service) applyMicroVM(ev api.Event[api.MicroVM]) {
	name := ev.Object.Metadata.Name
	old := s.microvms[name]
	if ev.Type == api.EventDeleted {
		delete(s.microvms, name)
	} else {
		s.microvms[name] = &ev.Object
	}
	for svcName, svc := range s.services {
		sel := svc.Spec.Selector
		if (old != nil && selects(sel, old.Metadata.Labels)) || (ev.Type != api.EventDeleted && selects(sel, ev.Object.Metadata.Labels)) {
			s.pending[svcName] = true
		}
	}
}

// reconcile writes the Endpoints of each pending Service. A Service whose write
// fails stays pending, except when the API refuses the Endpoints as invalid,
// which a retry would not change: it waits for the next change to the Service
// or to a MicroVM it selects.
func (s *service) reconcile(ctx context.Context) {
	for name := range s.pending {
		err := s.write(ctx, s.services[name])
		switch {
		case err == nil:
			delete(s.pending, name)
		case errors.Is(err, api.ErrInvalid):
			delete(s.pending, name)
			s.log.Error("endpoints refused", "operation", "write endpoints", "service", name, "error", err)
		case ctx.Err() == nil:
			s.log.Warn("endpoints not written", "operation", "write endpoints", "service", name, "error", err)
		}
	}
}
