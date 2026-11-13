package gateway

import (
	"fmt"
	"slices"

	"github.com/garamsh/gaon/lib/api"
)

// maxGatewayName is the longest Gateway name whose Service name,
// proxyPrefix plus the name, is still a DNS label.
const maxGatewayName = 63 - len(proxyPrefix)

// listenerCheck is the verdict on one listener.
type listenerCheck struct {
	listener Listener
	// served is true when the proxy serves the listener.
	served bool
	// reason and message say why a listener is not served: reason is
	// ReasonInvalid, ReasonProtocolConflict or ReasonHostnameConflict.
	reason, message string
	// badCertificate is set when the listener's certificate reference is the fault.
	badCertificate bool
}

// gatewayCheck is the verdict on a Gateway's spec.
type gatewayCheck struct {
	// invalid is why the whole Gateway is refused, or empty.
	invalid   string
	listeners []listenerCheck
}

// servedListeners returns the listeners the proxy serves.
func (g gatewayCheck) servedListeners() []Listener {
	var out []Listener
	for _, l := range g.listeners {
		if l.served {
			out = append(out, l.listener)
		}
	}
	return out
}

func (g gatewayCheck) listener(name string) (listenerCheck, bool) {
	for _, l := range g.listeners {
		if l.listener.Name == name {
			return l, true
		}
	}
	return listenerCheck{}, false
}

// checkGateway makes the checks of a Gateway the schema cannot make.
func checkGateway(name string, spec GatewaySpec) gatewayCheck {
	var g gatewayCheck
	switch {
	case len(name) > maxGatewayName:
		g.invalid = fmt.Sprintf("the name is longer than %d characters", maxGatewayName)
	case len(spec.Listeners) == 0:
		g.invalid = "the Gateway has no listener"
	case len(spec.Addresses) > 1:
		g.invalid = "the Gateway asks for more than one address"
	}
	if g.invalid != "" {
		return g
	}
	names := map[string]bool{}
	for _, l := range spec.Listeners {
		c := listenerCheck{listener: l, served: true}
		if names[l.Name] {
			c.served, c.reason, c.message = false, ReasonInvalid, "the name is used by an earlier listener"
		} else if msg, cert := checkTLS(l); msg != "" {
			c.served, c.reason, c.message, c.badCertificate = false, ReasonInvalid, msg, cert
		}
		names[l.Name] = true
		g.listeners = append(g.listeners, c)
	}
	markConflicts(g.listeners)
	markBindCollisions(g.listeners)
	return g
}

// checkTLS reports why a listener's tls does not fit its protocol, and whether
// the fault is the certificate reference.
func checkTLS(l Listener) (string, bool) {
	switch l.Protocol {
	case ProtocolHTTP:
		if l.TLS != nil {
			return "an HTTP listener has no tls", false
		}
	case ProtocolHTTPS:
		switch {
		case l.TLS == nil || l.TLS.Mode != ModeTerminate:
			return "an HTTPS listener has tls with mode Terminate", false
		case len(l.TLS.CertificateRefs) != 1:
			return "an HTTPS listener has exactly one certificate reference", true
		}
	case ProtocolTLS:
		switch {
		case l.TLS == nil || l.TLS.Mode != ModePassthrough:
			return "a TLS listener has tls with mode Passthrough", false
		case len(l.TLS.CertificateRefs) != 0:
			return "a TLS listener has no certificate reference", true
		}
	}
	return "", false
}

// markConflicts refuses the listeners of a port that cannot share it: those of
// different protocols, one with no hostname beside another, and those that
// name the same hostname.
func markConflicts(ls []listenerCheck) {
	byPort := map[int32][]int{}
	for i, l := range ls {
		if l.served {
			byPort[l.listener.Port] = append(byPort[l.listener.Port], i)
		}
	}
	for port, idx := range byPort {
		if len(idx) < 2 {
			continue
		}
		protocols, anyHost, hosts := map[string]bool{}, false, map[string][]int{}
		for _, i := range idx {
			protocols[ls[i].listener.Protocol] = true
			anyHost = anyHost || ls[i].listener.Hostname == ""
			hosts[ls[i].listener.Hostname] = append(hosts[ls[i].listener.Hostname], i)
		}
		for _, i := range idx {
			switch {
			case len(protocols) > 1:
				refuse(&ls[i], ReasonProtocolConflict, fmt.Sprintf("listeners of port %d have different protocols", port))
			case anyHost:
				refuse(&ls[i], ReasonHostnameConflict, fmt.Sprintf("a listener with no hostname must be alone on port %d", port))
			case len(hosts[ls[i].listener.Hostname]) > 1:
				refuse(&ls[i], ReasonHostnameConflict, fmt.Sprintf("another listener of port %d has hostname %s", port, ls[i].listener.Hostname))
			}
		}
	}
}

// markBindCollisions refuses a listener of a port at which the proxy listens
// for a listener of a lower port (BindPort), so that two ports never share one
// bound port.
func markBindCollisions(ls []listenerCheck) {
	shifted := map[int32]int32{}
	for _, l := range ls {
		if l.served && l.listener.Port < firstUnprivilegedPort {
			shifted[BindPort(l.listener.Port)] = l.listener.Port
		}
	}
	for i := range ls {
		if low, ok := shifted[ls[i].listener.Port]; ok && ls[i].served {
			refuse(&ls[i], ReasonInvalid, fmt.Sprintf("the proxy listens on port %d for the listener of port %d", ls[i].listener.Port, low))
		}
	}
}

func refuse(l *listenerCheck, reason, message string) {
	l.served, l.reason, l.message = false, reason, message
}

// checkRoute makes the checks of a route the schema cannot make, and reports
// the first fault, or an empty string.
func checkRoute(kind string, spec RouteSpec) string {
	switch {
	case len(spec.ParentRefs) == 0:
		return "the route has no parent reference"
	case len(spec.Rules) == 0:
		return "the route has no rule"
	case kind == KindTLSRoute && len(spec.Rules) != 1:
		return "a TLSRoute has exactly one rule"
	}
	for i, r := range spec.Rules {
		switch {
		case len(r.BackendRefs) != 1:
			return fmt.Sprintf("rule %d has %d backend references, want one", i, len(r.BackendRefs))
		case kind == KindHTTPRoute && len(r.Matches) == 0:
			return fmt.Sprintf("rule %d has no match", i)
		case kind == KindHTTPRoute && len(r.Filters) > 1:
			return fmt.Sprintf("rule %d has more than one filter", i)
		case kind == KindHTTPRoute && len(r.Filters) == 1 && len(r.Matches) != 1:
			return fmt.Sprintf("rule %d has a filter and so exactly one match", i)
		}
	}
	return ""
}

// servedPorts returns the distinct ports of the listeners, ascending.
func servedPorts(ls []Listener) []int32 {
	var ports []int32
	for _, l := range ls {
		if !slices.Contains(ports, l.Port) {
			ports = append(ports, l.Port)
		}
	}
	slices.Sort(ports)
	return ports
}

func condition(typ string, ok bool, reason, message string) api.Condition {
	status := api.ConditionFalse
	if ok {
		status = api.ConditionTrue
	}
	return api.Condition{Type: typ, Status: status, Reason: reason, Message: message}
}
