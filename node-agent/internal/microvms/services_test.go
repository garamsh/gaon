package microvms_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
)

// exposingNetwork records what it is asked to expose, and answers err.
type exposingNetwork struct {
	emptyNetwork
	mu       sync.Mutex
	routes   []plugin.Route
	announce []string
	n        int
	err      error
}

func (e *exposingNetwork) Expose(_ context.Context, routes []plugin.Route, announce []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.routes, e.announce, e.n = routes, announce, e.n+1
	return e.err
}

func (e *exposingNetwork) seen() ([]plugin.Route, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.routes, e.announce
}

func (e *exposingNetwork) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.n
}

func svc(name, clusterIP string, ports ...api.ServicePort) api.Service {
	return api.Service{
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.ServiceSpec{ClusterIP: clusterIP, Ports: ports},
	}
}

func loadBalancer(s api.Service, lan string) api.Service {
	s.Spec.Type = api.ServiceLoadBalancer
	s.Status.LoadBalancer = &api.LoadBalancerStatus{Ingress: []api.LoadBalancerIngress{{IP: lan}}}
	return s
}

func endpoints(name string, subsets ...api.EndpointSubset) api.Endpoints {
	return api.Endpoints{Metadata: api.ObjectMeta{Name: name}, Subsets: subsets}
}

func addr(ip, microvm string) api.EndpointAddress {
	return api.EndpointAddress{IP: ip, TargetRef: &api.ObjectReference{APIVersion: api.GroupVersion, Kind: api.KindMicroVM, Name: microvm}}
}

func routeOf(routes []plugin.Route, address string, port uint32) *plugin.Route {
	for i := range routes {
		if routes[i].Address == address && routes[i].Port == port {
			return &routes[i]
		}
	}
	return nil
}

func TestExpose_PassesARouteForEachPortOfEachServiceAndOneForTheLANAddressOfALoadBalancer(t *testing.T) {
	n := &exposingNetwork{}
	e := start(t, true, nil, func(e *env) { e.network = n })
	e.gaon.SetMicroVM(microvm("web-0", 1, node, api.RunAlways))
	e.gaon.SetMicroVM(microvm("web-1", 1, "n2", api.RunAlways))
	e.gaon.SetService(loadBalancer(svc("web", "10.96.0.10",
		api.ServicePort{Name: "http", Protocol: api.ProtocolTCP, Port: 80},
		api.ServicePort{Name: "metrics", Protocol: api.ProtocolTCP, Port: 9090},
		api.ServicePort{Name: "dns", Protocol: api.ProtocolUDP, Port: 53},
	), "192.168.1.200"))
	e.gaon.SetEndpoints(endpoints("web",
		api.EndpointSubset{
			Addresses: []api.EndpointAddress{addr("10.0.0.2", "web-0"), addr("10.0.0.3", "web-1")},
			Ports: []api.EndpointPort{
				{Name: "http", Port: 8080, Protocol: api.ProtocolTCP},
				{Name: "metrics", Port: 9091, Protocol: api.ProtocolTCP},
				{Name: "dns", Port: 5353, Protocol: api.ProtocolUDP},
				{Name: "dns", Port: 5354, Protocol: api.ProtocolTCP},
			},
		}))

	var routes []plugin.Route
	eventually(t, "the routes of two addresses and three ports", func() bool { routes, _ = n.seen(); return len(routes) == 6 })

	http := routeOf(routes, "10.96.0.10", 80)
	require.NotNil(t, http)
	assert.Equal(t, plugin.Route{
		Service: "web", Address: "10.96.0.10", Protocol: plugin.TCP, Port: 80,
		Backends: []plugin.Backend{{Address: "10.0.0.2", Port: 8080}, {Address: "10.0.0.3", Port: 8080}},
	}, *http, "the endpoints on every node are backends, with the port of the Service port's name")
	dns := routeOf(routes, "10.96.0.10", 53)
	require.NotNil(t, dns)
	assert.Equal(t, plugin.UDP, dns.Protocol)
	assert.Equal(t, []plugin.Backend{{Address: "10.0.0.2", Port: 5353}, {Address: "10.0.0.3", Port: 5353}}, dns.Backends, "the UDP port of the name, not its TCP port")
	metrics := routeOf(routes, "10.96.0.10", 9090)
	require.NotNil(t, metrics)
	assert.Equal(t, []plugin.Backend{{Address: "10.0.0.2", Port: 9091}, {Address: "10.0.0.3", Port: 9091}}, metrics.Backends, "the port of its own name, not the http one")
	lan := routeOf(routes, "192.168.1.200", 80)
	require.NotNil(t, lan)
	assert.True(t, lan.LAN)
	assert.Equal(t, http.Backends, lan.Backends, "the LAN address is translated to the same backends")
	assert.False(t, http.LAN, "control: the cluster IP is not a LAN address")
}

func TestExpose_AHeadlessServiceAndOneWithoutAnAddressGetNoRouteAndAServiceWithoutEndpointsGetsOneWithoutBackends(t *testing.T) {
	n := &exposingNetwork{}
	e := start(t, true, nil, func(e *env) { e.network = n })
	port := api.ServicePort{Protocol: api.ProtocolTCP, Port: 80}
	e.gaon.SetService(svc("headless", api.ClusterIPNone, port))
	e.gaon.SetService(svc("unallocated", "", port))
	e.gaon.SetService(svc("empty", "10.96.0.20", port))

	var routes []plugin.Route
	eventually(t, "the empty Service's route", func() bool { routes, _ = n.seen(); return len(routes) == 1 })

	assert.Equal(t, "10.96.0.20", routes[0].Address)
	assert.Empty(t, routes[0].Backends, "no ready endpoints, so the plugin refuses")
	assert.Equal(t, "empty", routes[0].Service, "control: the route is the one Service that has an address")
}

func TestExpose_AnEndpointThatGoesLeavesTheRouteAtTheNextRound(t *testing.T) {
	n := &exposingNetwork{}
	e := start(t, true, nil, func(e *env) { e.network = n })
	e.gaon.SetService(svc("web", "10.96.0.10", api.ServicePort{Protocol: api.ProtocolTCP, Port: 80}))
	two := api.EndpointSubset{Addresses: []api.EndpointAddress{addr("10.0.0.2", "a"), addr("10.0.0.3", "b")}, Ports: []api.EndpointPort{{Port: 8080, Protocol: api.ProtocolTCP}}}
	e.gaon.SetEndpoints(endpoints("web", two))
	eventually(t, "both are backends", func() bool {
		r := routeOf(first(n.seen()), "10.96.0.10", 80)
		return r != nil && len(r.Backends) == 2
	})

	one := two
	one.Addresses = two.Addresses[:1]
	e.gaon.SetEndpoints(endpoints("web", one))
	eventually(t, "one is left", func() bool {
		r := routeOf(first(n.seen()), "10.96.0.10", 80)
		return r != nil && len(r.Backends) == 1 && r.Backends[0].Address == "10.0.0.2"
	})

	e.gaon.DeleteService("web")
	eventually(t, "the route goes with the Service", func() bool { return len(first(n.seen())) == 0 })
}

func first(routes []plugin.Route, _ []string) []plugin.Route { return routes }

func TestExpose_OnlyOneNodeAnswersForALANAddressAndItIsTheFirstOfTheNodesRunningItsEndpoints(t *testing.T) {
	for name, tc := range map[string]struct {
		endpointsOn []string
		answers     bool
	}{
		"this node alone":                     {[]string{node}, true},
		"a node that sorts before this one":   {[]string{node, "n0"}, false},
		"a node that sorts after this one":    {[]string{node, "n2"}, true},
		"only a node that sorts after":        {[]string{"n2"}, false},
		"only a node that sorts before":       {[]string{"n0"}, false},
		"this node and one that sorts before": {[]string{"n0", node}, false},
	} {
		t.Run(name, func(t *testing.T) {
			n := &exposingNetwork{}
			e := start(t, true, nil, func(e *env) { e.network = n })
			// This node always runs a MicroVM that is not an endpoint, so a
			// rule that took the nodes of every MicroVM would answer.
			e.gaon.SetMicroVM(microvm("bystander", 1, node, api.RunAlways))
			var addrs []api.EndpointAddress
			for i, on := range tc.endpointsOn {
				name := "web-" + string(rune('a'+i))
				e.gaon.SetMicroVM(microvm(name, 1, on, api.RunAlways))
				addrs = append(addrs, addr("10.0.0."+string(rune('2'+i)), name))
			}
			e.gaon.SetService(loadBalancer(svc("web", "10.96.0.10", api.ServicePort{Protocol: api.ProtocolTCP, Port: 80}), "192.168.1.200"))
			e.gaon.SetEndpoints(endpoints("web", api.EndpointSubset{Addresses: addrs, Ports: []api.EndpointPort{{Port: 8080, Protocol: api.ProtocolTCP}}}))

			eventually(t, "the Service is exposed with its endpoints", func() bool {
				r := routeOf(first(n.seen()), "192.168.1.200", 80)
				return r != nil && len(r.Backends) == len(tc.endpointsOn)
			})
			_, announce := n.seen()
			if tc.answers {
				assert.Equal(t, []string{"192.168.1.200"}, announce)
			} else {
				assert.Empty(t, announce)
			}
		})
	}
}

func TestExpose_ALoadBalancerWithoutEndpointsIsAnsweredForByTheFirstNodeThatRunsAnyMicroVM(t *testing.T) {
	for name, tc := range map[string]struct {
		other   string
		answers bool
	}{
		"this node is first": {"n2", true},
		"another is first":   {"n0", false},
	} {
		t.Run(name, func(t *testing.T) {
			n := &exposingNetwork{}
			e := start(t, true, nil, func(e *env) { e.network = n })
			e.gaon.SetMicroVM(microvm("here", 1, node, api.RunAlways))
			e.gaon.SetMicroVM(microvm("there", 1, tc.other, api.RunAlways))
			e.gaon.SetService(loadBalancer(svc("web", "10.96.0.10", api.ServicePort{Protocol: api.ProtocolTCP, Port: 80}), "192.168.1.200"))

			eventually(t, "the Service is exposed", func() bool { return routeOf(first(n.seen()), "192.168.1.200", 80) != nil })
			_, announce := n.seen()
			if tc.answers {
				assert.Equal(t, []string{"192.168.1.200"}, announce, "so that the address still refuses")
			} else {
				assert.Empty(t, announce)
			}
		})
	}
}

func TestExpose_AServiceThatIsNotALoadBalancerAnnouncesNothing(t *testing.T) {
	n := &exposingNetwork{}
	e := start(t, true, nil, func(e *env) { e.network = n })
	e.gaon.SetMicroVM(microvm("web", 1, node, api.RunAlways))
	e.gaon.SetService(svc("web", "10.96.0.10", api.ServicePort{Protocol: api.ProtocolTCP, Port: 80}))
	e.gaon.SetEndpoints(endpoints("web", api.EndpointSubset{Addresses: []api.EndpointAddress{addr("10.0.0.2", "web")}, Ports: []api.EndpointPort{{Port: 8080, Protocol: api.ProtocolTCP}}}))

	eventually(t, "the Service is exposed", func() bool {
		r := routeOf(first(n.seen()), "10.96.0.10", 80)
		return r != nil && len(r.Backends) == 1
	})

	_, announce := n.seen()
	assert.Empty(t, announce)
}

func TestExpose_UnimplementedIsNotAFailureButAnyOtherErrorIsLogged(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		logged bool
	}{
		"unimplemented": {status.Error(codes.Unimplemented, "no"), false},
		"unavailable":   {status.Error(codes.Unavailable, "down"), true},
	} {
		t.Run(name, func(t *testing.T) {
			n := &exposingNetwork{err: tc.err}
			log := &syncBuffer{}
			e := start(t, true, nil, func(e *env) { e.network, e.log = n, log })
			e.gaon.SetMicroVM(microvm("web", 1, node, api.RunAlways))

			eventually(t, "web runs", e.running("web-e1"))
			eventually(t, "it was asked more than once", func() bool { return n.calls() > 1 })
			eventually(t, "web reports Running", e.phase("web", 1, api.MicroVMRunning))
			assert.Equal(t, tc.logged, strings.Contains(log.String(), "network plugin expose"), log.String())
		})
	}
}

func TestExpose_AWarningNamesAPluginThatDoesNotServeServicesOnlyWhileAServiceHasARoute(t *testing.T) {
	const warning = "level=WARN msg=\"Services exist and the network plugin does not serve them"
	n := &exposingNetwork{err: status.Error(codes.Unimplemented, "no")}
	log := &syncBuffer{}
	e := start(t, true, nil, func(e *env) { e.network, e.log = n, log })
	e.gaon.SetMicroVM(microvm("web", 1, node, api.RunAlways))
	eventually(t, "web runs", e.running("web-e1"))
	eventually(t, "it was asked more than once", func() bool { return n.calls() > 1 })
	assert.NotContains(t, log.String(), warning, "control: no Service has a route")

	e.gaon.SetService(svc("web", "10.96.0.10", api.ServicePort{Protocol: api.ProtocolTCP, Port: 80}))
	eventually(t, "the warning is logged", func() bool { return strings.Contains(log.String(), warning) })
}
