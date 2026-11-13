package proxy

import (
	"encoding/json"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/garamsh/gaon/gateway/internal/gateway"
	"github.com/garamsh/gaon/lib/api"
)

// table is what the proxy serves: the listeners of its Gateway by port, each
// with the routes attached to it. Apply builds a new one and swaps it in, so a
// request reads one table from start to end.
type table struct {
	ports map[int32]*portTable
}

// portTable is the listeners that share a port, all of one protocol.
type portTable struct {
	protocol  string
	listeners []*listenerTable
}

type listenerTable struct {
	name     string
	hostname string
	// secret is the Secret of the certificate of an HTTPS listener.
	secret string
	routes []route
}

type route struct {
	name string
	// hosts are the hostnames the route answers to on this listener; "" is any.
	hosts []string
	rules []rule
}

type rule struct {
	// prefixes are the path prefixes of the rule's matches, without a trailing slash.
	prefixes []string
	// rewrite replaces the matched prefix when hasRewrite is set.
	rewrite    string
	hasRewrite bool
	backend    *backend
}

// backend is the addresses of a rule's Service, as host:port, taken in turn.
type backend struct {
	addrs []string
	next  atomic.Uint64
}

func (b *backend) pick() (string, bool) {
	if b == nil || len(b.addrs) == 0 {
		return "", false
	}
	return b.addrs[int((b.next.Add(1)-1)%uint64(len(b.addrs)))], true
}

// match is the rule a request or a connection reached and the prefix it matched.
type match struct {
	rule   *rule
	prefix string
}

// secrets returns the names of the Secrets the HTTPS listeners reference.
func (t *table) secrets() []string {
	var out []string
	for _, pt := range t.ports {
		for _, l := range pt.listeners {
			if l.secret != "" {
				out = append(out, l.secret)
			}
		}
	}
	return out
}

// listener returns the listener of the port whose hostname is the best match
// for name.
func (pt *portTable) listener(name string) *listenerTable {
	var best *listenerTable
	for _, l := range pt.listeners {
		if hostMatches(l.hostname, name) && (best == nil || hostRank(l.hostname) > hostRank(best.hostname)) {
			best = l
		}
	}
	return best
}

// lookup returns the rule that serves a request for host and path on the
// listeners whose hostname matches serverName: the route with the best host,
// and then the longest prefix.
func (pt *portTable) lookup(serverName, host, path string) (match, bool) {
	var best match
	bestHost, bestPrefix := -1, -1
	l := pt.listener(serverName)
	if l == nil {
		return best, false
	}
	for _, r := range l.routes {
		for _, h := range r.hosts {
			if !hostMatches(h, host) {
				continue
			}
			for i := range r.rules {
				for _, p := range r.rules[i].prefixes {
					if !prefixMatches(p, path) {
						continue
					}
					if hr := hostRank(h); hr > bestHost || (hr == bestHost && len(p) > bestPrefix) {
						best, bestHost, bestPrefix = match{rule: &r.rules[i], prefix: p}, hr, len(p)
					}
				}
			}
		}
	}
	return best, bestHost >= 0
}

// pathMatch is a match of an HTTPRoute rule, as the controller validates it.
type pathMatch struct {
	Path struct {
		Value string `json:"value"`
	} `json:"path"`
}

// urlRewrite is the filter of an HTTPRoute rule, as the controller validates it.
type urlRewrite struct {
	URLRewrite struct {
		Path struct {
			ReplacePrefixMatch string `json:"replacePrefixMatch"`
		} `json:"path"`
	} `json:"urlRewrite"`
}

// buildTable makes the table of the Gateway name from the objects. A route is
// served on a listener only where the controller recorded the parent reference
// as Accepted.
func buildTable(name string, o Objects) *table {
	t := &table{ports: map[int32]*portTable{}}
	var spec gateway.GatewaySpec
	found := false
	for _, g := range o.Gateways {
		if g.Metadata.Name == name && json.Unmarshal(g.Spec, &spec) == nil {
			found = true
		}
	}
	if !found {
		return t
	}
	byName := map[string]*listenerTable{}
	for _, l := range gateway.ServedListeners(name, spec) {
		pt := t.ports[l.Port]
		if pt == nil {
			pt = &portTable{protocol: l.Protocol}
			t.ports[l.Port] = pt
		}
		lt := &listenerTable{name: l.Name, hostname: strings.ToLower(l.Hostname)}
		if l.Protocol == gateway.ProtocolHTTPS && l.TLS != nil && len(l.TLS.CertificateRefs) > 0 {
			lt.secret = l.TLS.CertificateRefs[0].Name
		}
		pt.listeners = append(pt.listeners, lt)
		byName[l.Name] = lt
	}

	res := resolver{services: map[string]api.Service{}, endpoints: map[string]api.Endpoints{}}
	for _, s := range o.Services {
		res.services[s.Metadata.Name] = s
	}
	for _, e := range o.Endpoints {
		res.endpoints[e.Metadata.Name] = e
	}
	for _, set := range []struct {
		kind   string
		routes []api.CustomObject
	}{{gateway.KindHTTPRoute, o.HTTPRoutes}, {gateway.KindTLSRoute, o.TLSRoutes}} {
		routes := append([]api.CustomObject(nil), set.routes...)
		sort.Slice(routes, func(i, j int) bool { return routes[i].Metadata.Name < routes[j].Metadata.Name })
		for _, obj := range routes {
			attach(set.kind, obj, name, spec, byName, res)
		}
	}
	return t
}

// attach adds the route to the listeners its Accepted parent references reach.
func attach(kind string, obj api.CustomObject, gw string, gwSpec gateway.GatewaySpec, listeners map[string]*listenerTable, res resolver) {
	var spec gateway.RouteSpec
	var status gateway.RouteStatus
	if json.Unmarshal(obj.Spec, &spec) != nil {
		return
	}
	if len(obj.Status) > 0 && json.Unmarshal(obj.Status, &status) != nil {
		return
	}
	done := map[string]bool{}
	for _, ref := range spec.ParentRefs {
		if ref.Name != gw || !accepted(status, ref) {
			continue
		}
		for _, ln := range gateway.AttachedListeners(kind, spec, ref, gw, gwSpec) {
			lt := listeners[ln]
			if lt == nil || done[ln] {
				continue
			}
			done[ln] = true
			if r, ok := makeRoute(kind, obj.Metadata.Name, spec, lt.hostname, res); ok {
				lt.routes = append(lt.routes, r)
			}
		}
	}
}

func accepted(status gateway.RouteStatus, ref gateway.ParentRef) bool {
	for _, p := range status.Parents {
		if p.ParentRef != ref {
			continue
		}
		for _, c := range p.Conditions {
			if c.Type == gateway.CondAccepted && c.Status == api.ConditionTrue {
				return true
			}
		}
	}
	return false
}

// makeRoute returns the route as served on a listener with the hostname.
func makeRoute(kind, name string, spec gateway.RouteSpec, listenerHost string, res resolver) (route, bool) {
	r := route{name: name}
	if len(spec.Hostnames) == 0 {
		r.hosts = []string{listenerHost}
	}
	for _, h := range spec.Hostnames {
		if host, ok := intersect(strings.ToLower(h), listenerHost); ok {
			r.hosts = append(r.hosts, host)
		}
	}
	if len(r.hosts) == 0 {
		return r, false
	}
	for _, rl := range spec.Rules {
		ru := rule{prefixes: []string{"/"}}
		if kind == gateway.KindHTTPRoute && len(rl.Matches) > 0 {
			ru.prefixes = nil
			for _, raw := range rl.Matches {
				var m pathMatch
				if json.Unmarshal(raw, &m) == nil && strings.HasPrefix(m.Path.Value, "/") {
					ru.prefixes = append(ru.prefixes, trimPrefix(m.Path.Value))
				}
			}
		}
		for _, raw := range rl.Filters {
			var f urlRewrite
			if json.Unmarshal(raw, &f) == nil {
				ru.rewrite, ru.hasRewrite = f.URLRewrite.Path.ReplacePrefixMatch, true
			}
		}
		if len(ru.prefixes) == 0 {
			continue
		}
		if len(rl.BackendRefs) > 0 {
			ru.backend = res.backend(rl.BackendRefs[0])
		}
		r.rules = append(r.rules, ru)
	}
	return r, len(r.rules) > 0
}

// resolver turns a backend reference into the ready addresses of the Service.
type resolver struct {
	services  map[string]api.Service
	endpoints map[string]api.Endpoints
}

// backend returns the addresses Endpoints lists for the Service port the
// reference names; none when the Service, the port or the Endpoints is absent.
func (r resolver) backend(ref gateway.BackendRef) *backend {
	b := &backend{}
	svc, ok := r.services[ref.Name]
	if !ok {
		return b
	}
	var port *api.ServicePort
	for i := range svc.Spec.Ports {
		if svc.Spec.Ports[i].Port == ref.Port {
			port = &svc.Spec.Ports[i]
		}
	}
	if port == nil {
		return b
	}
	for _, s := range r.endpoints[ref.Name].Subsets {
		for _, p := range s.Ports {
			if p.Name != port.Name || p.Protocol != api.ProtocolTCP {
				continue
			}
			for _, a := range s.Addresses {
				b.addrs = append(b.addrs, net.JoinHostPort(a.IP, strconv.Itoa(int(p.Port))))
			}
		}
	}
	return b
}
