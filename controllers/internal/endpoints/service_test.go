package endpoints_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/controllers/internal/endpoints"
	"github.com/garamsh/gaon/lib/api"
)

var web = map[string]string{"app": "web"}

func newService(name string, selector map[string]string, ports ...api.ServicePort) api.Service {
	return api.Service{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindService},
		Metadata: api.ObjectMeta{Name: name, UID: api.UID("uid-" + name)},
		Spec:     api.ServiceSpec{Selector: selector, Ports: ports},
	}
}

func tcp(name string, port int32) api.ServicePort {
	return api.ServicePort{Name: name, Protocol: api.ProtocolTCP, Port: port}
}

func targeting(p api.ServicePort, t api.PortRef) api.ServicePort {
	p.TargetPort = &t
	return p
}

// newMicroVM returns a Ready MicroVM with the labels and the address ip.
func newMicroVM(name string, labels map[string]string, ip string) api.MicroVM {
	now := time.Date(2026, 11, 13, 11, 0, 0, 0, time.UTC)
	return api.MicroVM{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVM},
		Metadata: api.ObjectMeta{Name: name, UID: api.UID("uid-" + name), Labels: labels},
		Status: api.MicroVMStatus{
			Phase:      api.MicroVMRunning,
			Addresses:  []string{ip},
			Conditions: []api.Condition{{Type: "Ready", Status: api.ConditionTrue, LastTransitionTime: &now}},
		},
	}
}

func withReady(m api.MicroVM, s api.ConditionStatus) api.MicroVM {
	m.Status.Conditions = []api.Condition{{Type: "Ready", Status: s}}
	return m
}

func withPorts(m api.MicroVM, ports ...api.MicroVMPort) api.MicroVM {
	m.Spec.Ports = ports
	return m
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// start runs the controller against f on a fake clock until the test ends.
func start(t *testing.T, f *fakeAPI) *fakeClock {
	t.Helper()
	clock := newFakeClock()
	svc := endpoints.NewService(f.client, clock, quiet())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		assert.ErrorIs(t, <-done, context.Canceled)
	})
	return clock
}

// The sentinel Service and MicroVM give a test an event to wait for: a watch
// delivers its events in order, so once the controller has written the
// sentinel's Endpoints it has handled every event sent before.
const sentinel = "sentinel"

var sentinelLabels = map[string]string{"role": sentinel}

func sentinelService() api.Service {
	return newService(sentinel, sentinelLabels, tcp("", 1))
}

func sentinelMicroVM() api.MicroVM {
	return newMicroVM(sentinel, sentinelLabels, "10.9.0.1")
}

// afterMicroVMEvents returns once the controller has handled every MicroVM
// event sent before the call.
func afterMicroVMEvents(f *fakeAPI, n int) {
	f.t.Helper()
	ip := fmt.Sprintf("10.9.1.%d", n)
	f.emitMicroVM(api.EventModified, newMicroVM(sentinel, sentinelLabels, ip))
	f.waitStored(sentinel, func(e api.Endpoints) bool { return len(ips(e)) == 1 && ips(e)[0] == ip })
}

// afterServiceEvents returns once the controller has handled every Service
// event sent before the call.
func afterServiceEvents(f *fakeAPI, port int32) {
	f.t.Helper()
	f.emitService(api.EventModified, newService(sentinel, sentinelLabels, tcp("", port)))
	f.waitStored(sentinel, func(e api.Endpoints) bool {
		return len(e.Subsets) == 1 && e.Subsets[0].Ports[0].Port == port
	})
}

// putsOf counts the writes to the Endpoints of the named Service.
func putsOf(f *fakeAPI, name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.puts {
		if e.Metadata.Name == name {
			n++
		}
	}
	return n
}

func TestEndpoints_ListsAMicroVMThatMeetsAllFourConditionsAndOnlyThatOne(t *testing.T) {
	deleting := time.Date(2026, 11, 13, 11, 30, 0, 0, time.UTC)
	tests := []struct {
		name string
		edit func(m *api.MicroVM)
		want []string
	}{
		{"all four met", func(m *api.MicroVM) {}, []string{"10.0.0.2", "10.0.0.1"}},
		{"not selected", func(m *api.MicroVM) { m.Metadata.Labels = map[string]string{"app": "db"} }, []string{"10.0.0.1"}},
		{"has a deletionTimestamp", func(m *api.MicroVM) { m.Metadata.DeletionTimestamp = &deleting }, []string{"10.0.0.1"}},
		{"Ready is False", func(m *api.MicroVM) { *m = withReady(*m, api.ConditionFalse) }, []string{"10.0.0.1"}},
		{"Ready is Unknown", func(m *api.MicroVM) { *m = withReady(*m, api.ConditionUnknown) }, []string{"10.0.0.1"}},
		{"has no Ready condition", func(m *api.MicroVM) { m.Status.Conditions = nil }, []string{"10.0.0.1"}},
		{"has no address", func(m *api.MicroVM) { m.Status.Addresses = nil }, []string{"10.0.0.1"}},
		{"has only an address no Endpoints accepts", func(m *api.MicroVM) { m.Status.Addresses = []string{"not-an-ip"} }, []string{"10.0.0.1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t)
			variant := newMicroVM("a", web, "10.0.0.2")
			tt.edit(&variant)
			f.seed([]api.Service{newService("web", web, tcp("", 80))}, []api.MicroVM{variant, newMicroVM("b", web, "10.0.0.1")})
			start(t, f)

			got := f.waitStored("web", func(api.Endpoints) bool { return true })
			assert.Equal(t, tt.want, ips(got))
			for _, s := range got.Subsets {
				for _, a := range s.Addresses {
					name := map[string]string{"10.0.0.2": "a", "10.0.0.1": "b"}[a.IP]
					assert.Equal(t, &api.ObjectReference{APIVersion: api.GroupVersion, Kind: api.KindMicroVM, Name: name, UID: api.UID("uid-" + name)}, a.TargetRef)
				}
			}
		})
	}
}

func TestEndpoints_ResolvesTargetPortsAndSplitsMicroVMsWhoseNumbersDiffer(t *testing.T) {
	f := newFakeAPI(t)
	svc := newService("web", web,
		targeting(tcp("http", 80), api.PortRef{Name: "web"}),
		tcp("metrics", 9100),
		targeting(tcp("admin", 8000), api.PortRef{Number: 8001}),
	)
	webPort := func(n int32, p api.Protocol) api.MicroVMPort {
		return api.MicroVMPort{Name: "web", Port: n, Protocol: p}
	}
	f.seed([]api.Service{svc}, []api.MicroVM{
		withPorts(newMicroVM("a", web, "10.0.0.1"), webPort(8080, api.ProtocolTCP)),
		withPorts(newMicroVM("b", web, "10.0.0.2"), webPort(8081, api.ProtocolTCP)),
		withPorts(newMicroVM("c", web, "10.0.0.3"), webPort(8080, api.ProtocolTCP)),
		newMicroVM("d", web, "10.0.0.4"),
		withPorts(newMicroVM("e", web, "10.0.0.5"), webPort(8080, api.ProtocolUDP)),
	})
	start(t, f)

	got := f.waitStored("web", func(api.Endpoints) bool { return true })
	rest := []api.EndpointPort{{Name: "metrics", Port: 9100, Protocol: api.ProtocolTCP}, {Name: "admin", Port: 8001, Protocol: api.ProtocolTCP}}
	port := func(n int32) []api.EndpointPort {
		return append([]api.EndpointPort{{Name: "http", Port: n, Protocol: api.ProtocolTCP}}, rest...)
	}
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.3"}, ips(api.Endpoints{Subsets: got.Subsets[:1]}))
	require.Len(t, got.Subsets, 3)
	assert.Equal(t, port(8080), got.Subsets[0].Ports, "a and c share the number web resolves to")
	assert.Equal(t, port(8081), got.Subsets[1].Ports, "b resolves web to another number, so it is in another subset")
	assert.Equal(t, []string{"10.0.0.2"}, ips(api.Endpoints{Subsets: got.Subsets[1:2]}))
	assert.Equal(t, rest, got.Subsets[2].Ports, "d declares no port web, and e declares it as UDP: neither resolves http, both resolve the others")
	assert.Equal(t, []string{"10.0.0.4", "10.0.0.5"}, ips(api.Endpoints{Subsets: got.Subsets[2:]}))
}

func TestEndpoints_WritesAnEmptyEndpointsForAServiceWithNoReadyMicroVM(t *testing.T) {
	f := newFakeAPI(t)
	f.seed([]api.Service{newService("web", web, tcp("", 80))}, []api.MicroVM{withReady(newMicroVM("a", web, "10.0.0.1"), api.ConditionFalse)})
	start(t, f)

	got := f.waitStored("web", func(api.Endpoints) bool { return true })
	assert.Empty(t, got.Subsets)
	assert.Equal(t, "web", got.Metadata.Name)
}

func TestEndpoints_FollowsAMicroVMThatBecomesReadyStopsBeingReadyChangesLabelsAndIsDeleted(t *testing.T) {
	f := newFakeAPI(t)
	f.seed([]api.Service{newService("web", web, tcp("", 80))}, []api.MicroVM{withReady(newMicroVM("a", web, "10.0.0.1"), api.ConditionFalse)})
	start(t, f)
	f.waitStored("web", func(e api.Endpoints) bool { return len(e.Subsets) == 0 })

	steps := []struct {
		name string
		typ  api.EventType
		mvm  api.MicroVM
		want []string
	}{
		{"becomes Ready", api.EventModified, newMicroVM("a", web, "10.0.0.1"), []string{"10.0.0.1"}},
		{"a new Ready MicroVM is added", api.EventAdded, newMicroVM("b", web, "10.0.0.2"), []string{"10.0.0.1", "10.0.0.2"}},
		{"stops being Ready", api.EventModified, withReady(newMicroVM("a", web, "10.0.0.1"), api.ConditionFalse), []string{"10.0.0.2"}},
		{"its address changes", api.EventModified, newMicroVM("b", web, "10.0.0.3"), []string{"10.0.0.3"}},
		{"its labels stop matching", api.EventModified, newMicroVM("b", map[string]string{"app": "other"}, "10.0.0.3"), nil},
		{"is labelled again", api.EventModified, newMicroVM("b", web, "10.0.0.3"), []string{"10.0.0.3"}},
		{"is deleted", api.EventDeleted, newMicroVM("b", web, "10.0.0.3"), nil},
	}
	for _, s := range steps {
		f.emitMicroVM(s.typ, s.mvm)
		got := f.waitStored("web", func(e api.Endpoints) bool { return fmt.Sprint(ips(e)) == fmt.Sprint(s.want) })
		assert.Equal(t, s.want, ips(got), s.name)
	}
}

func TestEndpoints_FollowsAChangeToTheServicesSelectorAndPortsAndWritesANewService(t *testing.T) {
	f := newFakeAPI(t)
	api2 := map[string]string{"app": "api"}
	f.seed([]api.Service{newService("web", web, tcp("", 80))}, []api.MicroVM{newMicroVM("a", web, "10.0.0.1"), newMicroVM("b", api2, "10.0.0.2")})
	start(t, f)
	f.waitStored("web", func(e api.Endpoints) bool { return fmt.Sprint(ips(e)) == "[10.0.0.1]" })

	f.emitService(api.EventModified, newService("web", api2, tcp("", 80)))
	got := f.waitStored("web", func(e api.Endpoints) bool { return fmt.Sprint(ips(e)) == "[10.0.0.2]" })
	assert.Equal(t, int32(80), got.Subsets[0].Ports[0].Port)

	f.emitService(api.EventModified, newService("web", api2, tcp("", 90)))
	got = f.waitStored("web", func(e api.Endpoints) bool { return len(e.Subsets) == 1 && e.Subsets[0].Ports[0].Port == 90 })
	assert.Equal(t, []string{"10.0.0.2"}, ips(got))

	f.emitService(api.EventAdded, newService("fresh", web, tcp("", 80)))
	got = f.waitStored("fresh", func(api.Endpoints) bool { return true })
	assert.Equal(t, []string{"10.0.0.1"}, ips(got))
}

func TestEndpoints_NeverWritesOrDeletesTheEndpointsOfAServiceWithoutASelector(t *testing.T) {
	f := newFakeAPI(t)
	user := api.Endpoints{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEndpoints},
		Metadata: api.ObjectMeta{Name: "manual"},
		Subsets: []api.EndpointSubset{{
			Addresses: []api.EndpointAddress{{IP: "192.0.2.1"}},
			Ports:     []api.EndpointPort{{Port: 80, Protocol: api.ProtocolTCP}},
		}},
	}
	f.seedEndpoints(user)
	before, _ := f.stored("manual")
	f.seed([]api.Service{newService("manual", nil, tcp("", 80)), sentinelService()},
		[]api.MicroVM{newMicroVM("a", map[string]string{}, "10.0.0.1"), newMicroVM("b", web, "10.0.0.2"), sentinelMicroVM()})
	start(t, f)
	f.waitStored(sentinel, func(api.Endpoints) bool { return true })
	afterMicroVMEvents(f, 1)

	after, _ := f.stored("manual")
	assert.Equal(t, before, after, "the user's Endpoints is as the user wrote it")
	assert.Zero(t, putsOf(f, "manual"))

	// Control: the same Service with a selector is written, so the silence above
	// is the missing selector and not a controller that wrote nothing.
	f.emitService(api.EventModified, newService("manual", web, tcp("", 80)))
	got := f.waitStored("manual", func(e api.Endpoints) bool { return fmt.Sprint(ips(e)) == "[10.0.0.2]" })
	assert.Equal(t, 1, putsOf(f, "manual"))
	assert.NotEqual(t, before.Subsets, got.Subsets)

	// Removing the selector hands the Endpoints back to its user: the controller
	// neither rewrites nor deletes it, and the fake fails the test on a DELETE.
	f.emitService(api.EventModified, newService("manual", nil, tcp("", 80)))
	afterServiceEvents(f, 2)
	f.emitMicroVM(api.EventModified, newMicroVM("b", web, "10.0.0.7"))
	afterMicroVMEvents(f, 2)
	still, ok := f.stored("manual")
	require.True(t, ok)
	assert.Equal(t, got, still)
	assert.Equal(t, 1, putsOf(f, "manual"))
}

func TestEndpoints_NamesItsServiceAsTheOwnerOfTheEndpointsItWrites(t *testing.T) {
	f := newFakeAPI(t)
	foreign := api.OwnerReference{APIVersion: "example.test/v1", Kind: "Widget", Name: "w", UID: "uid-w"}
	f.seedEndpoints(api.Endpoints{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEndpoints},
		Metadata: api.ObjectMeta{Name: "old", OwnerReferences: []api.OwnerReference{foreign}, Labels: map[string]string{"keep": "me"}},
	})
	f.seed([]api.Service{newService("web", web, tcp("", 80)), newService("old", web, tcp("", 80))}, []api.MicroVM{newMicroVM("a", web, "10.0.0.1")})
	start(t, f)

	own := func(name string) api.OwnerReference {
		return api.OwnerReference{APIVersion: api.GroupVersion, Kind: api.KindService, Name: name, UID: api.UID("uid-" + name), Controller: true}
	}
	got := f.waitStored("web", func(api.Endpoints) bool { return true })
	assert.Equal(t, []api.OwnerReference{own("web")}, got.Metadata.OwnerReferences)

	old := f.waitStored("old", func(e api.Endpoints) bool { return len(e.Subsets) == 1 })
	assert.Equal(t, []api.OwnerReference{foreign, own("old")}, old.Metadata.OwnerReferences, "an existing Endpoints keeps its other owners and labels")
	assert.Equal(t, map[string]string{"keep": "me"}, old.Metadata.Labels)
}

func TestEndpoints_DoesNotWriteAnEndpointsThatAlreadyListsWhatItShould(t *testing.T) {
	f := newFakeAPI(t)
	f.seedEndpoints(api.Endpoints{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEndpoints},
		Metadata: api.ObjectMeta{Name: "web", OwnerReferences: []api.OwnerReference{{APIVersion: api.GroupVersion, Kind: api.KindService, Name: "web", UID: "uid-web", Controller: true}}},
		Subsets: []api.EndpointSubset{{
			Addresses: []api.EndpointAddress{{IP: "10.0.0.1", TargetRef: &api.ObjectReference{APIVersion: api.GroupVersion, Kind: api.KindMicroVM, Name: "a", UID: "uid-a"}}},
			Ports:     []api.EndpointPort{{Port: 80, Protocol: api.ProtocolTCP}},
		}},
	})
	f.seed([]api.Service{newService("web", web, tcp("", 80)), sentinelService()}, []api.MicroVM{newMicroVM("a", web, "10.0.0.1"), sentinelMicroVM()})
	start(t, f)
	f.waitStored(sentinel, func(api.Endpoints) bool { return true })
	afterMicroVMEvents(f, 1)
	assert.Zero(t, putsOf(f, "web"))

	// Control: a change to the MicroVM's address is written.
	f.emitMicroVM(api.EventModified, newMicroVM("a", web, "10.0.0.5"))
	f.waitStored("web", func(e api.Endpoints) bool { return fmt.Sprint(ips(e)) == "[10.0.0.5]" })
	assert.Equal(t, 1, putsOf(f, "web"))
}

func TestEndpoints_WritesAgainAfterTheRetryDelayWhenAWriteFails(t *testing.T) {
	f := newFakeAPI(t)
	f.setFailPuts(1)
	f.seed([]api.Service{newService("web", web, tcp("", 80))}, []api.MicroVM{newMicroVM("a", web, "10.0.0.1")})
	clock := start(t, f)

	require.Eventually(t, func() bool { return f.putCount() == 1 }, 5*time.Second, time.Millisecond)
	clock.waitTimersAbove(t, 0)
	_, stored := f.stored("web")
	assert.False(t, stored, "the failed write stored nothing")
	clock.Advance(endpoints.RetryDelay - time.Second)
	assert.Equal(t, 1, f.putCount(), "the retry waits for the whole delay")

	clock.Advance(time.Second)
	got := f.waitStored("web", func(api.Endpoints) bool { return true })
	assert.Equal(t, []string{"10.0.0.1"}, ips(got))
}

func TestEndpoints_StopsRetryingAWriteForAServiceWhoseSelectorIsRemoved(t *testing.T) {
	f := newFakeAPI(t)
	f.setFailPuts(1)
	f.seed([]api.Service{newService("web", web, tcp("", 80)), sentinelService()}, []api.MicroVM{newMicroVM("a", web, "10.0.0.1"), sentinelMicroVM()})
	clock := start(t, f)
	f.waitStored(sentinel, func(api.Endpoints) bool { return true })
	require.Eventually(t, func() bool { return putsOf(f, "web") == 1 }, 5*time.Second, time.Millisecond)
	clock.waitTimersAbove(t, 0)

	f.emitService(api.EventModified, newService("web", nil, tcp("", 80)))
	afterServiceEvents(f, 2)
	clock.Advance(10 * endpoints.RetryDelay)
	afterMicroVMEvents(f, 1)
	assert.Equal(t, 1, putsOf(f, "web"), "the failed write is not retried once the Service has no selector")
	_, stored := f.stored("web")
	assert.False(t, stored)

	// Control: with the selector back, the same Service is written.
	f.emitService(api.EventModified, newService("web", web, tcp("", 80)))
	f.waitStored("web", func(e api.Endpoints) bool { return fmt.Sprint(ips(e)) == "[10.0.0.1]" })
}

func TestEndpoints_DoesNotRetryAnEndpointsTheAPIRefusesAsInvalid(t *testing.T) {
	f := newFakeAPI(t)
	// Both ports resolve to 8080 over TCP, which an Endpoints cannot hold twice.
	clash := newService("web", web, targeting(tcp("one", 80), api.PortRef{Number: 8080}), targeting(tcp("two", 81), api.PortRef{Number: 8080}))
	f.seed([]api.Service{clash, sentinelService()}, []api.MicroVM{newMicroVM("a", web, "10.0.0.1"), sentinelMicroVM()})
	clock := start(t, f)
	f.waitStored(sentinel, func(api.Endpoints) bool { return true })
	require.Eventually(t, func() bool { return putsOf(f, "web") == 1 }, 5*time.Second, time.Millisecond)

	clock.Advance(10 * endpoints.RetryDelay)
	afterMicroVMEvents(f, 1)
	assert.Equal(t, 1, putsOf(f, "web"), "the refused write is not repeated")
	_, stored := f.stored("web")
	assert.False(t, stored)

	// Control: the same Service with ports that do not clash is written.
	f.emitService(api.EventModified, newService("web", web, targeting(tcp("one", 80), api.PortRef{Number: 8080}), targeting(tcp("two", 81), api.PortRef{Number: 8081})))
	f.waitStored("web", func(e api.Endpoints) bool { return len(e.Subsets) == 1 })
	assert.Equal(t, 2, putsOf(f, "web"))
}

func TestEndpoints_ListsAgainAfterTheRetryDelayWhenAWatchEnds(t *testing.T) {
	f := newFakeAPI(t)
	f.seed([]api.Service{newService("web", web, tcp("", 80))}, []api.MicroVM{newMicroVM("a", web, "10.0.0.1")})
	clock := start(t, f)
	f.waitStored("web", func(e api.Endpoints) bool { return fmt.Sprint(ips(e)) == "[10.0.0.1]" })
	require.Equal(t, 1, f.listCount())

	// A change the ended watch never delivers is found by the list that follows.
	f.seed([]api.Service{newService("web", web, tcp("", 80))}, []api.MicroVM{newMicroVM("a", web, "10.0.0.1"), newMicroVM("b", web, "10.0.0.2")})
	f.mvmEvents <- nil
	clock.waitTimersAbove(t, 0)
	assert.Equal(t, 1, f.listCount(), "the list waits for the retry delay")

	clock.Advance(endpoints.RetryDelay)
	f.waitStored("web", func(e api.Endpoints) bool { return fmt.Sprint(ips(e)) == "[10.0.0.1 10.0.0.2]" })
	assert.Equal(t, 2, f.listCount())
}
