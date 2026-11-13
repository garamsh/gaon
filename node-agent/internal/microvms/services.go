package microvms

import (
	"context"
	"fmt"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
)

// expose passes the Services and their ready backends to the network plugin,
// when it serves them, together with the LAN addresses this node answers for.
// A plugin that does not (it answers Unimplemented, or is not an Exposer) is
// left alone: Services are not served on this node.
func (s *service) expose(ctx context.Context) error {
	x, ok := s.network.(plugin.Exposer)
	if !ok {
		return nil
	}
	services, err := s.api.ListServices(ctx)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	endpoints, err := s.api.ListEndpoints(ctx)
	if err != nil {
		return fmt.Errorf("list endpoints: %w", err)
	}
	routes, announce := exposure(s.cache, services.Items, endpoints.Items)
	if err := x.Expose(ctx, routes, announce); err != nil {
		if status.Code(err) == codes.Unimplemented {
			// With no Service to serve there is nothing left unserved, so
			// only then is it not worth a warning.
			if len(routes) > 0 {
				s.log.Warn("Services exist and the network plugin does not serve them", "error", err)
			} else {
				s.log.Debug("the network plugin does not serve Services", "error", err)
			}
			return nil
		}
		return fmt.Errorf("network plugin expose: %w", err)
	}
	return nil
}

// exposure returns a route for each port of each Service that has an address,
// and the LAN addresses this node answers for. A headless Service has no
// address and no route. A route has the ready backends its Service's
// Endpoints lists for the port, none when there are none. A LoadBalancer
// Service has a route for its LAN address as well as for its cluster IP.
func exposure(c *cache, services []api.Service, endpoints []api.Endpoints) ([]plugin.Route, []string) {
	byName := map[string]*api.Endpoints{}
	for i := range endpoints {
		byName[endpoints[i].Metadata.Name] = &endpoints[i]
	}
	var routes []plugin.Route
	var announce []string
	for _, svc := range services {
		if svc.Spec.Headless() || svc.Spec.ClusterIP == "" {
			continue
		}
		ep := byName[svc.Metadata.Name]
		var lan []string
		if svc.Spec.EffectiveType() == api.ServiceLoadBalancer && svc.Status.LoadBalancer != nil {
			for _, in := range svc.Status.LoadBalancer.Ingress {
				lan = append(lan, in.IP)
			}
		}
		for _, p := range svc.Spec.Ports {
			base := plugin.Route{Service: svc.Metadata.Name, Protocol: wireProtocol(p.Protocol), Port: uint32(p.Port), Backends: backends(ep, p)}
			cluster := base
			cluster.Address = svc.Spec.ClusterIP
			routes = append(routes, cluster)
			for _, ip := range lan {
				r := base
				r.Address, r.LAN = ip, true
				routes = append(routes, r)
			}
		}
		if len(lan) > 0 && c.answers(ep) {
			announce = append(announce, lan...)
		}
	}
	return routes, announce
}

// backends returns the addresses and ports ep lists for the Service port p,
// which are those of its ports named as p and of p's protocol.
func backends(ep *api.Endpoints, p api.ServicePort) []plugin.Backend {
	if ep == nil {
		return nil
	}
	var out []plugin.Backend
	for _, sub := range ep.Subsets {
		for _, port := range sub.Ports {
			if port.Name != p.Name || wireProtocol(port.Protocol) != wireProtocol(p.Protocol) {
				continue
			}
			for _, a := range sub.Addresses {
				out = append(out, plugin.Backend{Address: a.IP, Port: uint32(port.Port)})
			}
		}
	}
	return out
}

func wireProtocol(p api.Protocol) plugin.Protocol {
	if p == api.ProtocolUDP {
		return plugin.UDP
	}
	return plugin.TCP
}

// answers reports whether this node answers for the LAN addresses of a
// Service whose Endpoints is ep. Of the nodes that run a MicroVM ep lists, the
// one whose name sorts first does; with none listed, of the nodes that run
// any MicroVM. Every agent reads the same MicroVMs, so exactly one node
// decides it does, and a node that no MicroVM runs on never does: an agent
// cannot list the nodes.
func (c *cache) answers(ep *api.Endpoints) bool {
	var nodes []string
	if ep != nil {
		for _, sub := range ep.Subsets {
			for _, a := range sub.Addresses {
				if a.TargetRef == nil {
					continue
				}
				if m, ok := c.microvms[a.TargetRef.Name]; ok && m.Spec.NodeName != "" {
					nodes = append(nodes, m.Spec.NodeName)
				}
			}
		}
	}
	if len(nodes) == 0 {
		for _, m := range c.microvms {
			if m.Spec.NodeName != "" {
				nodes = append(nodes, m.Spec.NodeName)
			}
		}
	}
	return len(nodes) > 0 && slices.Min(nodes) == c.node
}
