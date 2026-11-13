package gateway_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gateway/internal/gateway"
	"github.com/garamsh/gaon/lib/api"
)

const (
	cert = `{"mode":"Terminate","certificateRefs":[{"name":"site-cert"}]}`
	// webSpec has an HTTP and an HTTPS listener, the HTTP port shared by two hostnames.
	webSpec = `{"addresses":[{"type":"IPAddress","value":"192.168.1.50"}],"listeners":[
		{"name":"a","port":80,"protocol":"HTTP","hostname":"a.example.com"},
		{"name":"b","port":80,"protocol":"HTTP","hostname":"b.example.com"},
		{"name":"secure","port":443,"protocol":"HTTPS","tls":` + cert + `}]}`
)

// gatewayReady waits until the Gateway has a status that says it is programmed.
func gatewayReady(t *testing.T, f *fakeAPI, name string) gateway.GatewayStatus {
	t.Helper()
	eventually(t, func() bool {
		s, ok := f.gatewayStatus(name)
		return ok && cond(s.Conditions, gateway.CondProgrammed).Status == api.ConditionTrue
	}, name+" is programmed")
	s, _ := f.gatewayStatus(name)
	return s
}

func TestRun_AGatewayGetsItsServiceAndItsDeploymentBothOwned(t *testing.T) {
	f := newFakeAPI(t)
	f.addGateway("edge", "uid-edge", webSpec)
	f.start(t)

	s := gatewayReady(t, f, "edge")
	owner := api.OwnerReference{APIVersion: gateway.APIVersion, Kind: "Gateway", Name: "edge", UID: "uid-edge", Controller: true}

	svc, ok := f.service("gateway-edge")
	require.True(t, ok)
	assert.Equal(t, api.ServiceLoadBalancer, svc.Spec.Type)
	assert.Equal(t, "192.168.1.50", svc.Spec.LoadBalancerIP)
	assert.Equal(t, map[string]string{gateway.GatewayLabel: "edge"}, svc.Spec.Selector)
	require.Len(t, svc.Spec.Ports, 2, "one port for each distinct listener port, and port 80 is shared")
	assert.Equal(t, int32(80), svc.Spec.Ports[0].Port)
	assert.Equal(t, int32(443), svc.Spec.Ports[1].Port)
	assert.Equal(t, api.ProtocolTCP, svc.Spec.Ports[0].Protocol)
	assert.Equal(t, []api.OwnerReference{owner}, svc.Metadata.OwnerReferences)
	assert.Equal(t, gateway.Manager, svc.Metadata.Labels[gateway.ManagedByLabel])

	dep, ok := f.object("deployments", "gateway-edge")
	require.True(t, ok, "the Deployment is created once the Service holds its address")
	assert.Equal(t, gateway.WorkloadsAPIVersion, dep.APIVersion)
	assert.Equal(t, []api.OwnerReference{owner}, dep.Metadata.OwnerReferences)
	var spec struct {
		Replicas int32 `json:"replicas"`
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
		Template api.MicroVMSpec `json:"template"`
	}
	require.NoError(t, json.Unmarshal(dep.Spec, &spec))
	assert.Equal(t, int32(1), spec.Replicas)
	assert.Equal(t, svc.Spec.Selector, spec.Selector.MatchLabels, "the proxies carry the label the Service selects")
	assert.Equal(t, proxy.Image, spec.Template.Image)
	assert.Contains(t, spec.Template.Args, "-gateway=edge")
	assert.Contains(t, spec.Template.Args, "-server="+proxy.Server)
	assert.Equal(t, proxy.ServiceAccount, spec.Template.ServiceAccountName)
	require.Len(t, spec.Template.Ports, 2)
	assert.Equal(t, int32(80), spec.Template.Ports[0].Port)
	assert.Equal(t, int32(443), spec.Template.Ports[1].Port)
	assert.Equal(t, api.RunAlways, spec.Template.RunStrategy)

	assert.Equal(t, []gateway.Address{{Type: "IPAddress", Value: "192.168.1.50"}}, s.Addresses)
	assert.Equal(t, api.ConditionTrue, cond(s.Conditions, gateway.CondAccepted).Status)
	require.Len(t, s.Listeners, 3)
	for _, l := range s.Listeners {
		assert.Equal(t, api.ConditionTrue, cond(l.Conditions, gateway.CondAccepted).Status, l.Name)
		assert.Equal(t, api.ConditionFalse, cond(l.Conditions, gateway.CondConflicted).Status, l.Name)
		assert.Equal(t, api.ConditionTrue, cond(l.Conditions, gateway.CondResolvedRefs).Status, l.Name)
		assert.Equal(t, api.ConditionTrue, cond(l.Conditions, gateway.CondProgrammed).Status, l.Name)
		assert.Zero(t, l.AttachedRoutes, l.Name)
	}

	f.settle(t)
	assert.Equal(t, 1, f.putsOf("services/gateway-edge"), "a Service that matches is not written again")
	assert.Equal(t, 1, f.putsOf("deployments/gateway-edge"))
	assert.Equal(t, 1, f.statusPutsOf("gateways/edge"), "a status that did not change is not written again")
}

func TestRun_AGatewayWithNoAddressTakesTheOneThePoolGrants(t *testing.T) {
	f := newFakeAPI(t)
	f.addGateway("pooled", "uid-pooled", `{"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
	f.start(t)

	s := gatewayReady(t, f, "pooled")
	svc, _ := f.service("gateway-pooled")
	assert.Empty(t, svc.Spec.LoadBalancerIP, "the Service asks for no address")
	require.Len(t, s.Addresses, 1)
	assert.Equal(t, svc.Status.LoadBalancer.Ingress[0].IP, s.Addresses[0].Value)
}

func TestRun_ListenersThatCannotShareAPortAreConflictedAndNotServed(t *testing.T) {
	tests := []struct {
		name      string
		listeners string
		conflict  map[string]string // listener to the reason
		ports     []int32
	}{
		{"a listener with no hostname beside another", `[
			{"name":"any","port":80,"protocol":"HTTP"},
			{"name":"named","port":80,"protocol":"HTTP","hostname":"x.example.com"},
			{"name":"alone","port":8080,"protocol":"HTTP"}]`,
			map[string]string{"any": gateway.ReasonHostnameConflict, "named": gateway.ReasonHostnameConflict}, []int32{8080}},
		{"the same hostname twice", `[
			{"name":"one","port":80,"protocol":"HTTP","hostname":"x.example.com"},
			{"name":"two","port":80,"protocol":"HTTP","hostname":"x.example.com"},
			{"name":"other","port":80,"protocol":"HTTP","hostname":"y.example.com"}]`,
			map[string]string{"one": gateway.ReasonHostnameConflict, "two": gateway.ReasonHostnameConflict}, []int32{80}},
		{"two protocols on a port", `[
			{"name":"plain","port":443,"protocol":"HTTP","hostname":"x.example.com"},
			{"name":"pass","port":443,"protocol":"TLS","hostname":"y.example.com","tls":{"mode":"Passthrough"}},
			{"name":"alone","port":80,"protocol":"HTTP"}]`,
			map[string]string{"plain": gateway.ReasonProtocolConflict, "pass": gateway.ReasonProtocolConflict}, []int32{80}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.addGateway("gw", "uid-gw", `{"listeners":`+tt.listeners+`}`)
			f.start(t)

			s := gatewayReady(t, f, "gw")
			assert.Equal(t, api.ConditionTrue, cond(s.Conditions, gateway.CondAccepted).Status, "the Gateway is accepted with the listeners that are valid")
			assert.Equal(t, gateway.ReasonListenersNotValid, cond(s.Conditions, gateway.CondAccepted).Reason)
			for _, l := range s.Listeners {
				reason, conflicted := tt.conflict[l.Name]
				c := cond(l.Conditions, gateway.CondConflicted)
				if conflicted {
					assert.Equal(t, api.ConditionTrue, c.Status, l.Name)
					assert.Equal(t, reason, c.Reason, l.Name)
					assert.Equal(t, api.ConditionFalse, cond(l.Conditions, gateway.CondAccepted).Status, l.Name)
					assert.Equal(t, api.ConditionFalse, cond(l.Conditions, gateway.CondProgrammed).Status, l.Name)
				} else {
					assert.Equal(t, api.ConditionFalse, c.Status, "control: "+l.Name+" shares its port within the rules")
					assert.Equal(t, api.ConditionTrue, cond(l.Conditions, gateway.CondAccepted).Status, l.Name)
				}
			}
			svc, _ := f.service("gateway-gw")
			var got []int32
			for _, p := range svc.Spec.Ports {
				got = append(got, p.Port)
			}
			assert.Equal(t, tt.ports, got, "only the ports of the listeners that are served")
		})
	}
}

func TestRun_AGatewayTheControllerRefusesGetsNoServiceAndAGatewayBesideItDoes(t *testing.T) {
	long := strings.Repeat("g", 56)
	tests := []struct {
		name, gateway, spec, message string
	}{
		{"a name too long for its Service", long, `{"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`, "longer than"},
		{"no listener", "empty", `{"listeners":[]}`, "no listener"},
		{"two addresses", "twice", `{"addresses":[{"type":"IPAddress","value":"10.0.0.1"},{"type":"IPAddress","value":"10.0.0.2"}],"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`, "more than one address"},
		{"an HTTPS listener with no certificate", "nocert", `{"listeners":[{"name":"web","port":443,"protocol":"HTTPS","tls":{"mode":"Terminate"}}]}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.addGateway(tt.gateway, "uid-bad", tt.spec)
			f.addGateway("fine", "uid-fine", `{"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
			f.start(t)

			gatewayReady(t, f, "fine")
			eventually(t, func() bool { _, ok := f.gatewayStatus(tt.gateway); return ok }, "the refused Gateway has a status")
			s, _ := f.gatewayStatus(tt.gateway)
			accepted := cond(s.Conditions, gateway.CondAccepted)
			assert.Equal(t, api.ConditionFalse, accepted.Status)
			assert.Contains(t, accepted.Message+cond(s.Conditions, gateway.CondProgrammed).Message, tt.message)
			assert.Equal(t, api.ConditionFalse, cond(s.Conditions, gateway.CondProgrammed).Status)
			_, hasService := f.service("gateway-" + tt.gateway)
			assert.False(t, hasService, "no Service")
			_, hasDeployment := f.object("deployments", "gateway-"+tt.gateway)
			assert.False(t, hasDeployment, "no Deployment")
			_, controlService := f.service("gateway-fine")
			assert.True(t, controlService, "control: the Gateway beside it is served")
		})
	}
}

func TestRun_AnAddressTheServerRefusesIsAddressNotAssignedAndNoDeploymentIsCreated(t *testing.T) {
	for _, reason := range []api.Reason{api.ReasonInvalid, api.ReasonConflict} {
		t.Run(string(reason), func(t *testing.T) {
			f := newFakeAPI(t)
			f.refuseAddress("10.9.9.9", reason)
			f.addGateway("bad", "uid-bad", `{"addresses":[{"type":"IPAddress","value":"10.9.9.9"}],"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
			f.addGateway("good", "uid-good", `{"addresses":[{"type":"IPAddress","value":"192.168.1.60"}],"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
			f.start(t)

			gatewayReady(t, f, "good")
			eventually(t, func() bool { _, ok := f.gatewayStatus("bad"); return ok }, "the refused Gateway has a status")
			s, _ := f.gatewayStatus("bad")
			accepted, programmed := cond(s.Conditions, gateway.CondAccepted), cond(s.Conditions, gateway.CondProgrammed)
			assert.Equal(t, api.ConditionFalse, accepted.Status)
			assert.Equal(t, gateway.ReasonAddressNotAssigned, accepted.Reason)
			assert.Equal(t, api.ConditionFalse, programmed.Status)
			assert.Equal(t, gateway.ReasonAddressNotAssigned, programmed.Reason)
			assert.Empty(t, s.Addresses)

			f.settle(t)
			_, ok := f.object("deployments", "gateway-bad")
			assert.False(t, ok, "no Deployment while the address cannot be held")
			_, ok = f.service("gateway-bad")
			assert.False(t, ok)
			_, ok = f.object("deployments", "gateway-good")
			assert.True(t, ok, "control: a Gateway whose address is held gets its Deployment")
		})
	}
}

func TestRun_AChangeOfAddressesAfterCreateIsNotAppliedAndSaysSo(t *testing.T) {
	f := newFakeAPI(t)
	one := `{"addresses":[{"type":"IPAddress","value":"192.168.1.50"}],"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`
	f.addGateway("edge", "uid-edge", one)
	f.start(t)
	gatewayReady(t, f, "edge")

	f.setGatewaySpec("edge", strings.Replace(one, "192.168.1.50", "192.168.1.51", 1))
	eventually(t, func() bool {
		s, _ := f.gatewayStatus("edge")
		return cond(s.Conditions, gateway.CondProgrammed).Reason == gateway.ReasonAddressNotAssigned
	}, "the change is reported")
	s, _ := f.gatewayStatus("edge")
	assert.Equal(t, api.ConditionFalse, cond(s.Conditions, gateway.CondProgrammed).Status)
	assert.Equal(t, api.ConditionTrue, cond(s.Conditions, gateway.CondAccepted).Status, "the Gateway stays accepted: it still holds an address")
	assert.Equal(t, []gateway.Address{{Type: "IPAddress", Value: "192.168.1.50"}}, s.Addresses, "the address held is the old one")
	svc, _ := f.service("gateway-edge")
	assert.Equal(t, "192.168.1.50", svc.Spec.LoadBalancerIP, "the Service keeps its address")
	f.settle(t)
	assert.Equal(t, 1, f.putsOf("services/gateway-edge"), "the Service is not rewritten")

	f.setGatewaySpec("edge", one)
	eventually(t, func() bool {
		s, _ := f.gatewayStatus("edge")
		return cond(s.Conditions, gateway.CondProgrammed).Status == api.ConditionTrue
	}, "asking for the held address again is programmed again")
}

func TestRun_AChangeOfListenersBringsTheServiceAndTheDeploymentAlong(t *testing.T) {
	f := newFakeAPI(t)
	f.addGateway("edge", "uid-edge", `{"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
	f.start(t)
	gatewayReady(t, f, "edge")

	f.setGatewaySpec("edge", `{"listeners":[{"name":"web","port":80,"protocol":"HTTP"},{"name":"alt","port":8080,"protocol":"HTTP"}]}`)
	eventually(t, func() bool {
		svc, _ := f.service("gateway-edge")
		return len(svc.Spec.Ports) == 2
	}, "the Service gets the new port")
	eventually(t, func() bool {
		dep, _ := f.object("deployments", "gateway-edge")
		return strings.Contains(string(dep.Spec), `"port":8080`)
	}, "the Deployment's template names the new port")
	assert.Equal(t, 2, f.putsOf("services/gateway-edge"), "control: one write for the change")
}

func TestRun_AServiceAnotherObjectOwnsIsNotTakenOver(t *testing.T) {
	f := newFakeAPI(t)
	f.seed("services", "gateway-edge", map[string]any{
		"apiVersion": api.GroupVersion, "kind": api.KindService,
		"metadata": map[string]any{"ownerReferences": []any{map[string]any{"apiVersion": gateway.APIVersion, "kind": "Gateway", "name": "edge", "uid": "uid-older"}}},
		"spec":     map[string]any{"type": "LoadBalancer"},
	})
	f.addGateway("edge", "uid-edge", `{"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
	f.addGateway("fine", "uid-fine", `{"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
	f.start(t)

	gatewayReady(t, f, "fine")
	eventually(t, func() bool { _, ok := f.gatewayStatus("edge"); return ok }, "the Gateway has a status")
	s, _ := f.gatewayStatus("edge")
	assert.Equal(t, api.ConditionFalse, cond(s.Conditions, gateway.CondProgrammed).Status)
	assert.Equal(t, gateway.ReasonInvalid, cond(s.Conditions, gateway.CondProgrammed).Reason)
	assert.Zero(t, f.putsOf("services/gateway-edge"), "the Service is not written")
	_, ok := f.object("deployments", "gateway-edge")
	assert.False(t, ok)
	_, ok = f.object("deployments", "gateway-fine")
	assert.True(t, ok, "control: a Gateway whose Service is its own gets a Deployment")
}
