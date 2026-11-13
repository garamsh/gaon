package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/garamsh/gaon/dns/internal/zone"
	"github.com/garamsh/gaon/lib/api"
)

// source reads Services and Endpoints from the API server under the
// ServiceAccount token in a file.
type source struct {
	root      string
	http      *http.Client
	tokenFile string
	retry     time.Duration
	log       *slog.Logger
}

// client returns a Client that sends the token the file holds now. The Node
// Agent replaces the file before the token expires (ADR 0029), so each list
// and each watch reads it again.
func (s *source) client() (*api.Client, error) {
	raw, err := os.ReadFile(s.tokenFile)
	if err != nil {
		return nil, fmt.Errorf("token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return nil, fmt.Errorf("token file %s is empty", s.tokenFile)
	}
	return api.NewClient(s.root, s.http, api.WithBearerToken(token)), nil
}

// run keeps z current with the Services and the Endpoints until ctx ends.
func (s *source) run(ctx context.Context, z *zone.Zone) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		follow(ctx, s, "services",
			func(ctx context.Context) ([]api.Service, api.ResourceVersion, error) {
				c, err := s.client()
				if err != nil {
					return nil, "", err
				}
				l, err := c.ListServices(ctx)
				if err != nil {
					return nil, "", err
				}
				return l.Items, l.Metadata.ResourceVersion, nil
			},
			func(ctx context.Context, rv api.ResourceVersion) (*api.Watch[api.Service], error) {
				c, err := s.client()
				if err != nil {
					return nil, err
				}
				return c.WatchServices(ctx, rv)
			},
			z.ReplaceServices, z.ApplyService)
	}()
	go func() {
		defer wg.Done()
		follow(ctx, s, "endpoints",
			func(ctx context.Context) ([]api.Endpoints, api.ResourceVersion, error) {
				c, err := s.client()
				if err != nil {
					return nil, "", err
				}
				l, err := c.ListEndpoints(ctx)
				if err != nil {
					return nil, "", err
				}
				return l.Items, l.Metadata.ResourceVersion, nil
			},
			func(ctx context.Context, rv api.ResourceVersion) (*api.Watch[api.Endpoints], error) {
				c, err := s.client()
				if err != nil {
					return nil, err
				}
				return c.WatchEndpoints(ctx, rv)
			},
			z.ReplaceEndpoints, z.ApplyEndpoints)
	}()
	wg.Wait()
}

// follow lists one kind, hands the list to replace, then applies the events of
// a watch from the list's version. When the list, the watch or the stream
// fails, it waits s.retry and lists again, leaving what it last handed over in
// place. It returns when ctx ends.
func follow[T any](
	ctx context.Context,
	s *source,
	kind string,
	list func(context.Context) ([]T, api.ResourceVersion, error),
	watch func(context.Context, api.ResourceVersion) (*api.Watch[T], error),
	replace func([]T),
	apply func(api.EventType, T),
) {
	for ctx.Err() == nil {
		err := followOnce(ctx, list, watch, replace, apply)
		if ctx.Err() != nil {
			return
		}
		s.log.Warn("lost the API server", "kind", kind, "error", err, "retry", s.retry)
		select {
		case <-ctx.Done():
		case <-time.After(s.retry):
		}
	}
}

func followOnce[T any](
	ctx context.Context,
	list func(context.Context) ([]T, api.ResourceVersion, error),
	watch func(context.Context, api.ResourceVersion) (*api.Watch[T], error),
	replace func([]T),
	apply func(api.EventType, T),
) error {
	items, rv, err := list(ctx)
	if err != nil {
		return err
	}
	replace(items)
	w, err := watch(ctx, rv)
	if err != nil {
		return err
	}
	defer w.Close()
	for {
		ev, err := w.Next()
		if err != nil {
			return errors.Join(errors.New("watch ended"), err)
		}
		apply(ev.Type, ev.Object)
	}
}
