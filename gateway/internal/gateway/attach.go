package gateway

import (
	"fmt"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// attachment is the decision on one parent reference of a route.
type attachment struct {
	ref             ParentRef
	accepted        bool
	reason, message string
	// listeners are the listeners the route attaches to.
	listeners []string
}

// How far a listener got in accepting a route, so that the reason given is
// that of the listener that came closest.
const (
	stageNone = iota
	stageNotServed
	stageProtocol
	stageHostname
)

// accepts reports whether a listener's protocol takes the kind.
func accepts(kind, protocol string) bool {
	if kind == KindTLSRoute {
		return protocol == ProtocolTLS
	}
	return protocol == ProtocolHTTP || protocol == ProtocolHTTPS
}

// hostsIntersect reports whether some name matches both a and b, each an exact
// name or *.<name>. An empty name matches every name.
func hostsIntersect(a, b string) bool {
	wa, wb := strings.HasPrefix(a, "*."), strings.HasPrefix(b, "*.")
	switch {
	case a == "" || b == "" || a == b:
		return true
	case wa && wb:
		return strings.HasSuffix(a[1:], b[1:]) || strings.HasSuffix(b[1:], a[1:])
	case wa:
		return strings.HasSuffix(b, a[1:])
	case wb:
		return strings.HasSuffix(a, b[1:])
	}
	return false
}

// routeHostsIntersect reports whether a hostname of the route and the
// listener's hostname intersect, a route with no hostname taking the listener's.
func routeHostsIntersect(hostnames []string, listener string) bool {
	if len(hostnames) == 0 {
		return true
	}
	for _, h := range hostnames {
		if hostsIntersect(h, listener) {
			return true
		}
	}
	return false
}

// attach decides each parent reference of a route against the checked
// Gateways.
func attach(kind string, spec RouteSpec, gateways map[string]gatewayCheck) []attachment {
	out := make([]attachment, 0, len(spec.ParentRefs))
	for _, ref := range spec.ParentRefs {
		out = append(out, attachParent(kind, spec, ref, gateways))
	}
	return out
}

func attachParent(kind string, spec RouteSpec, ref ParentRef, gateways map[string]gatewayCheck) attachment {
	a := attachment{ref: ref}
	g, ok := gateways[ref.Name]
	if !ok {
		a.reason, a.message = ReasonNoMatchingParent, fmt.Sprintf("Gateway %s does not exist", ref.Name)
		return a
	}
	stage, reason, message := stageNone, ReasonNoMatchingParent, fmt.Sprintf("Gateway %s has no listener %s", ref.Name, ref.SectionName)
	for _, l := range g.listeners {
		if ref.SectionName != "" && l.listener.Name != ref.SectionName {
			continue
		}
		next, why, msg := stageNotServed, ReasonNoMatchingParent, fmt.Sprintf("listener %s is not served: %s", l.listener.Name, l.message)
		switch {
		case !l.served:
		case !accepts(kind, l.listener.Protocol):
			next, why, msg = stageProtocol, ReasonNotAllowed, fmt.Sprintf("listener %s has protocol %s, which a %s does not use", l.listener.Name, l.listener.Protocol, kind)
		case !routeHostsIntersect(spec.Hostnames, l.listener.Hostname):
			next, why, msg = stageHostname, ReasonNoMatchingHostname, fmt.Sprintf("no hostname of the route intersects the hostname of listener %s", l.listener.Name)
		default:
			a.listeners = append(a.listeners, l.listener.Name)
			continue
		}
		if next > stage {
			stage, reason, message = next, why, msg
		}
	}
	if len(a.listeners) > 0 {
		a.accepted, a.reason, a.message = true, ReasonAccepted, ""
		return a
	}
	a.reason, a.message = reason, message
	return a
}

// resolveBackends checks that each backend of the route names a Service that
// exists and has the port. It reports the first that does not.
func resolveBackends(spec RouteSpec, services map[string]api.Service) (bool, string) {
	for _, r := range spec.Rules {
		for _, b := range r.BackendRefs {
			svc, ok := services[b.Name]
			if !ok {
				return false, fmt.Sprintf("Service %s does not exist", b.Name)
			}
			found := false
			for _, p := range svc.Spec.Ports {
				found = found || p.Port == b.Port
			}
			if !found {
				return false, fmt.Sprintf("Service %s has no port %d", b.Name, b.Port)
			}
		}
	}
	return true, ""
}
