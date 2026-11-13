package gateway_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/garamsh/gaon/gateway/internal/gateway"
	"github.com/garamsh/gaon/lib/api"
)

func TestRun_ARouteIsCountedOnceOnAListenerHoweverManyParentReferencesReachIt(t *testing.T) {
	f := newFakeAPI(t)
	f.addGateway("edge", "uid-edge", routeGateway)
	f.addService("web", 80)
	f.addRoute("httproutes", "twice", httpSpec(`[{"name":"edge"},{"name":"edge","sectionName":"web"}]`, `[]`))
	f.addRoute("httproutes", "once", httpSpec(`[{"name":"edge","sectionName":"web"}]`, `[]`))
	f.start(t)

	eventually(t, func() bool {
		s, _ := f.gatewayStatus("edge")
		return len(s.Listeners) == 2 && s.Listeners[0].AttachedRoutes >= 2
	}, "both routes are counted on web")
	f.settle(t)
	s, _ := f.gatewayStatus("edge")
	assert.Equal(t, int32(2), s.Listeners[0].AttachedRoutes, "control: the route with one reference counts once, and so does the one with two")
}

func TestRun_ARouteStatusIsWrittenOnlyWhenItChanges(t *testing.T) {
	f := newFakeAPI(t)
	f.addGateway("edge", "uid-edge", routeGateway)
	f.addRoute("httproutes", "r", httpSpec(`[{"name":"edge"}]`, `[]`))
	f.addRoute("tlsroutes", "t", tlsSpec(`[{"name":"edge"}]`))
	f.start(t)

	parentConds(t, f, "httproutes", "r")
	parentConds(t, f, "tlsroutes", "t")
	f.settle(t)
	assert.Equal(t, 1, f.statusPutsOf("httproutes/r"), "an unchanged status is not written again")
	assert.Equal(t, 1, f.statusPutsOf("tlsroutes/t"))

	f.addService("web", 80)
	eventually(t, func() bool {
		s, _ := f.routeStatus("httproutes", "r")
		return len(s.Parents) == 1 && cond(s.Parents[0].Conditions, gateway.CondResolvedRefs).Status == api.ConditionTrue
	}, "the backend resolves")
	f.settle(t)
	assert.Equal(t, 2, f.statusPutsOf("httproutes/r"), "control: a change is written, once")
}
