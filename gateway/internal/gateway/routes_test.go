package gateway_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gateway/internal/gateway"
	"github.com/garamsh/gaon/lib/api"
)

// routeGateway has an HTTP listener for *.example.com and a TLS passthrough listener.
const routeGateway = `{"listeners":[
	{"name":"web","port":80,"protocol":"HTTP","hostname":"*.example.com"},
	{"name":"pass","port":443,"protocol":"TLS","hostname":"secure.example.com","tls":{"mode":"Passthrough"}}]}`

// httpSpec is an HTTPRoute spec with one rule to the backend web:80, with parents and hostnames as JSON.
func httpSpec(parents, hostnames string) string {
	return `{"parentRefs":` + parents + `,"hostnames":` + hostnames + `,"rules":[{"matches":[{"path":{"type":"PathPrefix","value":"/"}}],"backendRefs":[{"name":"web","port":80}]}]}`
}

// tlsSpec is a TLSRoute spec with one rule to the backend web:80.
func tlsSpec(parents string) string {
	return `{"parentRefs":` + parents + `,"hostnames":["secure.example.com"],"rules":[{"backendRefs":[{"name":"web","port":80}]}]}`
}

// parentConds waits for the route's status to name a parent and returns that parent's conditions.
func parentConds(t *testing.T, f *fakeAPI, plural, name string) []api.Condition {
	t.Helper()
	eventually(t, func() bool {
		s, ok := f.routeStatus(plural, name)
		return ok && len(s.Parents) == 1 && cond(s.Parents[0].Conditions, gateway.CondAccepted).Type != ""
	}, name+" has a status")
	s, _ := f.routeStatus(plural, name)
	return s.Parents[0].Conditions
}

func TestRun_ARouteIsAcceptedOnAMatchingListenerAndRefusedOtherwise(t *testing.T) {
	tests := []struct {
		name, plural, spec string
		accepted           bool
		reason             string
	}{
		{"an HTTPRoute on a matching HTTP listener", "httproutes", httpSpec(`[{"name":"edge"}]`, `["app.example.com"]`), true, gateway.ReasonAccepted},
		{"an HTTPRoute that names its listener", "httproutes", httpSpec(`[{"name":"edge","sectionName":"web"}]`, `[]`), true, gateway.ReasonAccepted},
		{"an HTTPRoute on a TLS listener", "httproutes", httpSpec(`[{"name":"edge","sectionName":"pass"}]`, `[]`), false, gateway.ReasonNotAllowed},
		{"hostnames that do not intersect the listener's", "httproutes", httpSpec(`[{"name":"edge"}]`, `["app.other.test"]`), false, gateway.ReasonNoMatchingHostname},
		{"a Gateway that does not exist", "httproutes", httpSpec(`[{"name":"missing"}]`, `[]`), false, gateway.ReasonNoMatchingParent},
		{"a listener that does not exist", "httproutes", httpSpec(`[{"name":"edge","sectionName":"nope"}]`, `[]`), false, gateway.ReasonNoMatchingParent},
		{"a TLSRoute on a TLS listener", "tlsroutes", tlsSpec(`[{"name":"edge"}]`), true, gateway.ReasonAccepted},
		{"a TLSRoute on an HTTP listener", "tlsroutes", tlsSpec(`[{"name":"edge","sectionName":"web"}]`), false, gateway.ReasonNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.addGateway("edge", "uid-edge", routeGateway)
			f.addService("web", 80)
			f.addRoute(tt.plural, "r", tt.spec)
			f.start(t)

			conds := parentConds(t, f, tt.plural, "r")
			accepted := cond(conds, gateway.CondAccepted)
			assert.Equal(t, tt.reason, accepted.Reason)
			if tt.accepted {
				assert.Equal(t, api.ConditionTrue, accepted.Status)
			} else {
				assert.Equal(t, api.ConditionFalse, accepted.Status)
			}
			// The refusals above have the control of the accepted rows: the same Gateway, Service and shape.
			eventually(t, func() bool {
				s, _ := f.gatewayStatus("edge")
				return len(s.Listeners) == 2
			}, "the Gateway has a status")
			s, _ := f.gatewayStatus("edge")
			var attached int32
			for _, l := range s.Listeners {
				attached += l.AttachedRoutes
			}
			if tt.accepted {
				assert.Equal(t, int32(1), attached, "an accepted route is counted on its listener")
			} else {
				assert.Zero(t, attached, "a refused route is not counted")
			}
		})
	}
}

func TestRun_AcceptedRoutesAreCountedOnTheListenersTheyAttachTo(t *testing.T) {
	f := newFakeAPI(t)
	f.addGateway("edge", "uid-edge", routeGateway)
	f.addService("web", 80)
	f.addRoute("httproutes", "one", httpSpec(`[{"name":"edge","sectionName":"web"}]`, `[]`))
	f.addRoute("httproutes", "two", httpSpec(`[{"name":"edge"}]`, `["a.example.com"]`))
	f.addRoute("tlsroutes", "three", tlsSpec(`[{"name":"edge"}]`))
	f.start(t)

	eventually(t, func() bool {
		s, _ := f.gatewayStatus("edge")
		return len(s.Listeners) == 2 && s.Listeners[0].AttachedRoutes == 2 && s.Listeners[1].AttachedRoutes == 1
	}, "two routes on web and one on pass")
}

func TestRun_ABackendThatIsNotThereLeavesTheRouteAcceptedButNotResolved(t *testing.T) {
	tests := []struct {
		name     string
		service  bool
		resolved bool
	}{
		{"no Service of that name", false, false},
		{"a Service without the port", true, false},
		{"a Service with the port", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.addGateway("edge", "uid-edge", routeGateway)
			if tt.service {
				if tt.resolved {
					f.addService("web", 80)
				} else {
					f.addService("web", 9090)
				}
			}
			f.addRoute("httproutes", "r", httpSpec(`[{"name":"edge"}]`, `["a.example.com"]`))
			f.start(t)

			conds := parentConds(t, f, "httproutes", "r")
			assert.Equal(t, api.ConditionTrue, cond(conds, gateway.CondAccepted).Status, "the parent matches either way")
			resolved := cond(conds, gateway.CondResolvedRefs)
			if tt.resolved {
				assert.Equal(t, api.ConditionTrue, resolved.Status)
				return
			}
			assert.Equal(t, api.ConditionFalse, resolved.Status)
			assert.Equal(t, gateway.ReasonBackendNotFound, resolved.Reason)
			assert.Contains(t, resolved.Message, "web")
		})
	}
}

func TestRun_ARouteTheControllerCannotServeIsUnsupportedValue(t *testing.T) {
	filterWithTwoMatches := `{"parentRefs":[{"name":"edge"}],"rules":[{"matches":[{"path":{"type":"PathPrefix","value":"/"}},{"path":{"type":"PathPrefix","value":"/b"}}],"filters":[{"type":"RequestHeaderModifier"}],"backendRefs":[{"name":"web","port":80}]}]}`
	twoBackends := `{"parentRefs":[{"name":"edge"}],"rules":[{"matches":[{"path":{"type":"PathPrefix","value":"/"}}],"backendRefs":[{"name":"web","port":80},{"name":"web","port":80}]}]}`
	twoTLSRules := `{"parentRefs":[{"name":"edge"}],"rules":[{"backendRefs":[{"name":"web","port":80}]},{"backendRefs":[{"name":"web","port":80}]}]}`
	tests := []struct {
		name, plural, spec string
	}{
		{"a filter beside two matches", "httproutes", filterWithTwoMatches},
		{"two backends in a rule", "httproutes", twoBackends},
		{"two rules in a TLSRoute", "tlsroutes", twoTLSRules},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.addGateway("edge", "uid-edge", routeGateway)
			f.addService("web", 80)
			f.addRoute(tt.plural, "bad", tt.spec)
			f.addRoute("httproutes", "good", httpSpec(`[{"name":"edge"}]`, `["a.example.com"]`))
			f.start(t)

			conds := parentConds(t, f, tt.plural, "bad")
			accepted := cond(conds, gateway.CondAccepted)
			assert.Equal(t, api.ConditionFalse, accepted.Status)
			assert.Equal(t, gateway.ReasonUnsupportedValue, accepted.Reason)
			good := parentConds(t, f, "httproutes", "good")
			assert.Equal(t, api.ConditionTrue, cond(good, gateway.CondAccepted).Status, "control: a route of the shape served is accepted")
		})
	}
}

func TestRun_ARouteWithNoParentGetsNoParentStatus(t *testing.T) {
	f := newFakeAPI(t)
	f.addGateway("edge", "uid-edge", routeGateway)
	f.addService("web", 80)
	f.addRoute("httproutes", "orphan", `{"parentRefs":[],"rules":[{"matches":[{"path":{"type":"PathPrefix","value":"/"}}],"backendRefs":[{"name":"web","port":80}]}]}`)
	f.addRoute("httproutes", "good", httpSpec(`[{"name":"edge"}]`, `[]`))
	f.start(t)

	parentConds(t, f, "httproutes", "good")
	s, _ := f.routeStatus("httproutes", "orphan")
	require.Empty(t, s.Parents, "there is no parent to report on")
}
