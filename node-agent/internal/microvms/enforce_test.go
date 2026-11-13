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

// enforcingNetwork records what it is asked to enforce, and answers err.
type enforcingNetwork struct {
	emptyNetwork
	mu   sync.Mutex
	last []plugin.MicroVMLimits
	n    int
	err  error
}

func (e *enforcingNetwork) Enforce(_ context.Context, limits []plugin.MicroVMLimits) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.last, e.n = limits, e.n+1
	return e.err
}

func (e *enforcingNetwork) limits() []plugin.MicroVMLimits {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.last
}

func labelled(m api.MicroVM, labels map[string]string, addrs ...string) api.MicroVM {
	m.Metadata.Labels = labels
	m.Status.Addresses = addrs
	return m
}

func policy(name string, spec api.NetworkPolicySpec) api.NetworkPolicy {
	return api.NetworkPolicy{Metadata: api.ObjectMeta{Name: name}, Spec: spec}
}

func TestEnforce_PassesTheEffectiveLimitsOfTheMicroVMsHereAndLeavesAnUnselectedDirectionAndMicroVMUnset(t *testing.T) {
	n := &enforcingNetwork{}
	e := start(t, true, nil, func(e *env) { e.network = n })
	e.gaon.SetMicroVM(labelled(microvm("client", 1, "other", api.RunAlways), map[string]string{"app": "client"}, "10.0.0.9"))
	e.gaon.SetMicroVM(labelled(microvm("web", 1, node, api.RunAlways), map[string]string{"app": "web"}))
	e.gaon.SetMicroVM(microvm("free", 1, node, api.RunAlways))
	e.gaon.SetNetworkPolicy(policy("web", api.NetworkPolicySpec{
		PodSelector: api.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
		PolicyTypes: []api.PolicyType{api.PolicyIngress, api.PolicyEgress},
		Ingress: []api.IngressRule{{
			From:  []api.NetworkPolicyPeer{{PodSelector: &api.LabelSelector{MatchLabels: map[string]string{"app": "client"}}}},
			Ports: []api.NetworkPolicyPort{{Protocol: api.ProtocolTCP, Port: 80}},
		}},
		Egress: []api.EgressRule{{To: []api.NetworkPolicyPeer{{IPBlock: &api.IPBlock{CIDR: "10.2.0.0/16", Except: []string{"10.2.1.0/24"}}}}}},
	}))
	eventually(t, "both run", func() bool { return e.running("web-e1")() && e.running("free-e1")() })

	var web, free *plugin.MicroVMLimits
	eventually(t, "both are limited by name", func() bool {
		web, free = nil, nil
		for _, l := range n.limits() {
			switch l.MicroVM {
			case "web":
				web = &l
			case "free":
				free = &l
			}
			_ = l
		}
		return web != nil && free != nil
	})
	require.Equal(t, []string{"10.0.0.2"}, web.Addresses)
	assert.Equal(t, &plugin.Limit{Allow: []plugin.Allow{{Prefix: "10.0.0.9/32", Ports: []plugin.PolicyPort{{Protocol: plugin.TCP, Port: 80}}}}}, web.Ingress)
	assert.Equal(t, &plugin.Limit{Allow: []plugin.Allow{{Prefix: "10.2.0.0/16", Except: []string{"10.2.1.0/24"}}}}, web.Egress)
	assert.Nil(t, free.Ingress, "no policy selects free")
	assert.Nil(t, free.Egress, "no policy selects free")
	for _, l := range n.limits() {
		assert.NotEqual(t, "client", l.MicroVM, "a MicroVM bound elsewhere is another node's to limit")
	}
}

func TestEnforce_ARemovedPolicyLeavesTheMicroVMUnrestrictedAtTheNextRound(t *testing.T) {
	n := &enforcingNetwork{}
	e := start(t, true, nil, func(e *env) { e.network = n })
	e.gaon.SetMicroVM(labelled(microvm("web", 1, node, api.RunAlways), map[string]string{"app": "web"}))
	e.gaon.SetNetworkPolicy(policy("deny", api.NetworkPolicySpec{PodSelector: api.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}))
	eventually(t, "web is denied ingress", func() bool {
		l := n.limits()
		return len(l) == 1 && l[0].Ingress != nil
	})

	e.gaon.DeleteNetworkPolicy("deny")
	eventually(t, "web is unrestricted", func() bool {
		l := n.limits()
		return len(l) == 1 && l[0].Ingress == nil
	})
}

func TestEnforce_UnimplementedIsNotAFailureButAnyOtherErrorIsLogged(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		logged bool
	}{
		"unimplemented": {status.Error(codes.Unimplemented, "no"), false},
		"unavailable":   {status.Error(codes.Unavailable, "down"), true},
	} {
		t.Run(name, func(t *testing.T) {
			n := &enforcingNetwork{err: tc.err}
			log := &syncBuffer{}
			e := start(t, true, nil, func(e *env) { e.network, e.log = n, log })
			e.gaon.SetMicroVM(microvm("web", 1, node, api.RunAlways))

			eventually(t, "web runs", e.running("web-e1"))
			eventually(t, "it was asked more than once", func() bool { n.mu.Lock(); defer n.mu.Unlock(); return n.n > 1 })
			eventually(t, "web reports Running", e.phase("web", 1, api.MicroVMRunning))
			assert.Equal(t, tc.logged, strings.Contains(log.String(), "network plugin enforce"), log.String())
		})
	}
}

func TestEnforce_AWarningNamesAPluginThatDoesNotEnforceOnlyWhileAPolicySelectsAMicroVMHere(t *testing.T) {
	const warning = "level=WARN msg=\"NetworkPolicy selects MicroVMs on this node"
	n := &enforcingNetwork{err: status.Error(codes.Unimplemented, "no")}
	log := &syncBuffer{}
	e := start(t, true, nil, func(e *env) { e.network, e.log = n, log })
	e.gaon.SetMicroVM(labelled(microvm("web", 1, node, api.RunAlways), map[string]string{"app": "web"}))
	eventually(t, "web runs", e.running("web-e1"))
	eventually(t, "it was asked more than once", func() bool { n.mu.Lock(); defer n.mu.Unlock(); return n.n > 1 })
	assert.NotContains(t, log.String(), warning, "control: no policy selects a MicroVM")

	e.gaon.SetNetworkPolicy(policy("deny", api.NetworkPolicySpec{PodSelector: api.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}))
	eventually(t, "the warning is logged", func() bool { return strings.Contains(log.String(), warning) })
}
