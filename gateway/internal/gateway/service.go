package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// Config configures the Service.
type Config struct {
	// Interval is the time between the end of one sweep and the start of the next.
	Interval time.Duration
	// Proxy is what the proxies' Deployments are made from.
	Proxy ProxyConfig
}

// Service serves the gateway kinds.
type Service interface {
	// Run registers the Gateway, HTTPRoute and TLSRoute kinds, then sweeps every
	// Config.Interval: it validates each Gateway and route, decides which routes
	// attach to which listeners, makes each Gateway's Service and Deployment, and
	// writes the status of every Gateway and route that changed. It returns when
	// ctx ends, or at once when a kind cannot be registered.
	Run(ctx context.Context) error
}

type service struct {
	definitions *api.Client
	gateways    *api.CustomResourceClient
	httproutes  *api.CustomResourceClient
	tlsroutes   *api.CustomResourceClient
	deployments *api.CustomResourceClient
	services    *api.Client
	cfg         Config
	log         *slog.Logger
	// refused holds the message last logged for a Gateway whose address is refused.
	refused map[string]string
}

// NewService returns a Service. definitions is a client rooted at the
// apiextensions group; gateways, httproutes and tlsroutes are the clients of the
// three kinds and deployments the client of the workloads Deployment kind;
// services is a client rooted at the compute group.
func NewService(definitions *api.Client, gateways, httproutes, tlsroutes, deployments *api.CustomResourceClient, services *api.Client, cfg Config, log *slog.Logger) Service {
	return &service{
		definitions: definitions, gateways: gateways, httproutes: httproutes, tlsroutes: tlsroutes,
		deployments: deployments, services: services, cfg: cfg, log: log, refused: map[string]string{},
	}
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
		case <-time.After(s.cfg.Interval):
		}
	}
}

// route is a route read and decided.
type route struct {
	obj      api.CustomObject
	client   *api.CustomResourceClient
	kind     string
	spec     RouteSpec
	problem  string
	parents  []attachment
	resolved bool
	backends string
}

func byName(a, b api.CustomObject) int { return strings.Compare(a.Metadata.Name, b.Metadata.Name) }

// sweep reads every Gateway, route, Service and Deployment once, decides, and
// writes. A failed read ends the sweep, because a decision on part of the state
// would unattach what it did not see. A failure of one object is logged and does
// not stop the others.
func (s *service) sweep(ctx context.Context) {
	gws, err := s.gateways.List(ctx)
	if err != nil {
		s.log.Warn("list gateways", "error", err)
		return
	}
	https, err := s.httproutes.List(ctx)
	if err != nil {
		s.log.Warn("list httproutes", "error", err)
		return
	}
	tlss, err := s.tlsroutes.List(ctx)
	if err != nil {
		s.log.Warn("list tlsroutes", "error", err)
		return
	}
	svcList, err := s.services.ListServices(ctx)
	if err != nil {
		s.log.Warn("list services", "error", err)
		return
	}
	depList, err := s.deployments.List(ctx)
	if err != nil {
		s.log.Warn("list deployments", "error", err)
		return
	}
	services := map[string]api.Service{}
	for _, svc := range svcList.Items {
		services[svc.Metadata.Name] = svc
	}
	deployments := map[string]api.CustomObject{}
	for _, d := range depList.Items {
		deployments[d.Metadata.Name] = d
	}

	slices.SortFunc(gws.Items, byName)
	specs := map[string]GatewaySpec{}
	checks := map[string]gatewayCheck{}
	for _, g := range gws.Items {
		var spec GatewaySpec
		if err := json.Unmarshal(g.Spec, &spec); err != nil {
			s.log.Warn("decode gateway", "gateway", g.Metadata.Name, "error", err)
			continue
		}
		specs[g.Metadata.Name] = spec
		checks[g.Metadata.Name] = checkGateway(g.Metadata.Name, spec)
	}

	routes := s.decideRoutes(https.Items, s.httproutes, KindHTTPRoute, checks, services)
	routes = append(routes, s.decideRoutes(tlss.Items, s.tlsroutes, KindTLSRoute, checks, services)...)
	attached := map[string]map[string]int32{}
	for _, r := range routes {
		counted := map[[2]string]bool{}
		for _, a := range r.parents {
			for _, l := range a.listeners {
				key := [2]string{a.ref.Name, l}
				if counted[key] {
					continue
				}
				counted[key] = true
				if attached[a.ref.Name] == nil {
					attached[a.ref.Name] = map[string]int32{}
				}
				attached[a.ref.Name][l]++
			}
		}
	}

	for i := range gws.Items {
		g := &gws.Items[i]
		if _, ok := specs[g.Metadata.Name]; ok {
			s.reconcileGateway(ctx, g, specs[g.Metadata.Name], checks[g.Metadata.Name], attached[g.Metadata.Name], services, deployments)
		}
	}
	for _, r := range routes {
		var old RouteStatus
		if len(r.obj.Status) > 0 {
			_ = json.Unmarshal(r.obj.Status, &old)
		}
		next := routeStatus(r.problem, r.parents, r.resolved, r.backends, old, time.Now().UTC().Truncate(time.Second))
		if err := putStatus(ctx, r.client, &r.obj, old, next); err != nil {
			s.log.Warn("write status", "kind", r.kind, "name", r.obj.Metadata.Name, "error", err)
		}
	}
}

// decideRoutes decodes the routes of one kind and decides their parent
// references and backends.
func (s *service) decideRoutes(items []api.CustomObject, client *api.CustomResourceClient, kind string, checks map[string]gatewayCheck, services map[string]api.Service) []route {
	slices.SortFunc(items, byName)
	var out []route
	for _, o := range items {
		r := route{obj: o, client: client, kind: kind}
		if err := json.Unmarshal(o.Spec, &r.spec); err != nil {
			s.log.Warn("decode route", "kind", kind, "name", o.Metadata.Name, "error", err)
			continue
		}
		r.problem = checkRoute(kind, r.spec)
		r.parents = attach(kind, r.spec, checks)
		if r.problem != "" {
			for i := range r.parents {
				r.parents[i].accepted, r.parents[i].listeners = false, nil
			}
		}
		r.resolved, r.backends = resolveBackends(r.spec, services)
		out = append(out, r)
	}
	return out
}

// reconcileGateway makes what the Gateway needs and writes its status.
func (s *service) reconcileGateway(ctx context.Context, g *api.CustomObject, spec GatewaySpec, check gatewayCheck, attached map[string]int32, services map[string]api.Service, deployments map[string]api.CustomObject) {
	name := g.Metadata.Name
	p, err := s.provision(ctx, g, spec, check, services, deployments)
	if err != nil {
		s.log.Warn("provision", "gateway", name, "error", err)
	}
	if !p.refused {
		delete(s.refused, name)
	} else if s.refused[name] != p.message {
		s.refused[name] = p.message
		s.log.Warn("address not assigned", "gateway", name, "reason", p.message)
	}
	var old GatewayStatus
	if len(g.Status) > 0 {
		_ = json.Unmarshal(g.Status, &old)
	}
	next := gatewayStatus(check, p, attached, old, time.Now().UTC().Truncate(time.Second))
	if err := putStatus(ctx, s.gateways, g, old, next); err != nil {
		s.log.Warn("write status", "kind", KindGateway, "name", name, "error", err)
	}
}
