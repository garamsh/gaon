package proxy

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

	"github.com/garamsh/gaon/gateway/internal/gateway"
	"github.com/garamsh/gaon/lib/api"
)

// Source keeps a Proxy current with the objects the API server holds, under the
// ServiceAccount token in a file.
type Source struct {
	// Server is the base URL of the API server.
	Server string
	// HTTP is the client of the API server; it sets no Timeout, because a watch is one long response.
	HTTP *http.Client
	// TokenFile is the file of the ServiceAccount token.
	TokenFile string
	// Retry is the wait before the Source lists again after losing the API server.
	Retry time.Duration
	Log   *slog.Logger
	Proxy *Proxy

	mu        sync.Mutex
	gateways  keyed[api.CustomObject]
	httpRoute keyed[api.CustomObject]
	tlsRoute  keyed[api.CustomObject]
	services  keyed[api.Service]
	endpoints keyed[api.Endpoints]
	secrets   keyed[api.Secret]
}

// keyed is one kind's objects by name.
type keyed[T any] struct {
	items map[string]T
	name  func(T) string
}

func (k *keyed[T]) replace(list []T) {
	k.items = make(map[string]T, len(list))
	for _, o := range list {
		k.items[k.name(o)] = o
	}
}

func (k *keyed[T]) event(t api.EventType, o T) {
	if t == api.EventDeleted {
		delete(k.items, k.name(o))
		return
	}
	k.items[k.name(o)] = o
}

func (k *keyed[T]) list() []T {
	out := make([]T, 0, len(k.items))
	for _, o := range k.items {
		out = append(out, o)
	}
	return out
}

// token returns the token the file holds now. The Node Agent replaces the file
// before the token expires (ADR 0029), so each list and each watch reads it
// again.
func (s *Source) token() (string, error) {
	raw, err := os.ReadFile(s.TokenFile)
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("token file %s is empty", s.TokenFile)
	}
	return token, nil
}

func (s *Source) root() string {
	return strings.TrimSuffix(s.Server, "/") + "/apis/" + api.GroupVersion
}

// compute returns a client of the compute kinds, selecting by sel when given.
func (s *Source) compute(sel api.Selector) (*api.Client, error) {
	token, err := s.token()
	if err != nil {
		return nil, err
	}
	opts := []api.ClientOption{api.WithBearerToken(token)}
	if len(sel) > 0 {
		opts = append(opts, api.WithLabelSelector(sel))
	}
	return api.NewClient(s.root(), s.HTTP, opts...), nil
}

func (s *Source) custom(t api.CustomResourceType) (*api.CustomResourceClient, error) {
	token, err := s.token()
	if err != nil {
		return nil, err
	}
	return api.NewCustomResourceClient(s.Server, s.HTTP, t, api.WithBearerToken(token))
}

// push hands the proxy a snapshot. It runs under s.mu, so snapshots reach
// Apply in the order they were taken.
func (s *Source) push() {
	s.Proxy.Apply(Objects{
		Gateways:   s.gateways.list(),
		HTTPRoutes: s.httpRoute.list(),
		TLSRoutes:  s.tlsRoute.list(),
		Services:   s.services.list(),
		Endpoints:  s.endpoints.list(),
		Secrets:    s.secrets.list(),
	})
}

// Run follows the kinds until ctx ends.
func (s *Source) Run(ctx context.Context) error {
	sel, err := api.ParseSelector(gateway.CertificateLabel + "=" + gateway.CertificateLabelValue)
	if err != nil {
		return err
	}
	customName := func(o api.CustomObject) string { return o.Metadata.Name }
	s.gateways = keyed[api.CustomObject]{name: customName}
	s.httpRoute = keyed[api.CustomObject]{name: customName}
	s.tlsRoute = keyed[api.CustomObject]{name: customName}
	s.services = keyed[api.Service]{name: func(o api.Service) string { return o.Metadata.Name }}
	s.endpoints = keyed[api.Endpoints]{name: func(o api.Endpoints) string { return o.Metadata.Name }}
	s.secrets = keyed[api.Secret]{name: func(o api.Secret) string { return o.Metadata.Name }}
	s.gateways.replace(nil)
	s.httpRoute.replace(nil)
	s.tlsRoute.replace(nil)
	s.services.replace(nil)
	s.endpoints.replace(nil)
	s.secrets.replace(nil)

	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	for _, c := range []struct {
		kind string
		t    api.CustomResourceType
		k    *keyed[api.CustomObject]
	}{
		{"gateways", gateway.GatewayType, &s.gateways},
		{"httproutes", gateway.HTTPRouteType, &s.httpRoute},
		{"tlsroutes", gateway.TLSRouteType, &s.tlsRoute},
	} {
		run(func() {
			follow(ctx, s, c.kind,
				func(ctx context.Context) ([]api.CustomObject, api.ResourceVersion, error) {
					cl, err := s.custom(c.t)
					if err != nil {
						return nil, "", err
					}
					l, err := cl.List(ctx)
					if err != nil {
						return nil, "", err
					}
					return l.Items, l.Metadata.ResourceVersion, nil
				},
				func(ctx context.Context, rv api.ResourceVersion) (*api.Watch[api.CustomObject], error) {
					cl, err := s.custom(c.t)
					if err != nil {
						return nil, err
					}
					return cl.Watch(ctx, rv)
				}, c.k)
		})
	}
	run(func() {
		follow(ctx, s, "services",
			func(ctx context.Context) ([]api.Service, api.ResourceVersion, error) {
				c, err := s.compute(nil)
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
				c, err := s.compute(nil)
				if err != nil {
					return nil, err
				}
				return c.WatchServices(ctx, rv)
			}, &s.services)
	})
	run(func() {
		follow(ctx, s, "endpoints",
			func(ctx context.Context) ([]api.Endpoints, api.ResourceVersion, error) {
				c, err := s.compute(nil)
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
				c, err := s.compute(nil)
				if err != nil {
					return nil, err
				}
				return c.WatchEndpoints(ctx, rv)
			}, &s.endpoints)
	})
	run(func() {
		follow(ctx, s, "secrets",
			func(ctx context.Context) ([]api.Secret, api.ResourceVersion, error) {
				c, err := s.compute(sel)
				if err != nil {
					return nil, "", err
				}
				l, err := c.ListSecrets(ctx)
				if err != nil {
					return nil, "", err
				}
				return l.Items, l.Metadata.ResourceVersion, nil
			},
			func(ctx context.Context, rv api.ResourceVersion) (*api.Watch[api.Secret], error) {
				c, err := s.compute(sel)
				if err != nil {
					return nil, err
				}
				return c.WatchSecrets(ctx, rv)
			}, &s.secrets)
	})
	wg.Wait()
	return ctx.Err()
}

// follow lists one kind, replaces the kind's objects with the list, then
// applies the events of a watch from the list's version. When the list, the
// watch or the stream fails, it waits s.Retry and lists again, leaving the
// objects as they were. It returns when ctx ends.
func follow[T any](
	ctx context.Context,
	s *Source,
	kind string,
	list func(context.Context) ([]T, api.ResourceVersion, error),
	watch func(context.Context, api.ResourceVersion) (*api.Watch[T], error),
	k *keyed[T],
) {
	for ctx.Err() == nil {
		err := followOnce(ctx, s, list, watch, k)
		if ctx.Err() != nil {
			return
		}
		s.Log.Warn("lost the API server", "kind", kind, "error", err, "retry", s.Retry)
		select {
		case <-ctx.Done():
		case <-time.After(s.Retry):
		}
	}
}

func followOnce[T any](
	ctx context.Context,
	s *Source,
	list func(context.Context) ([]T, api.ResourceVersion, error),
	watch func(context.Context, api.ResourceVersion) (*api.Watch[T], error),
	k *keyed[T],
) error {
	items, rv, err := list(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	k.replace(items)
	s.push()
	s.mu.Unlock()
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
		s.mu.Lock()
		k.event(ev.Type, ev.Object)
		s.push()
		s.mu.Unlock()
	}
}
