package gateway_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/garamsh/gaon/gateway/internal/gateway"
	"github.com/garamsh/gaon/lib/api"
)

func TestRun_RemovingTheAddressAfterCreateIsReportedAsAChange(t *testing.T) {
	f := newFakeAPI(t)
	f.addGateway("edge", "uid-edge", `{"addresses":[{"type":"IPAddress","value":"192.168.1.50"}],"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
	f.addGateway("pooled", "uid-pooled", `{"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
	f.start(t)
	gatewayReady(t, f, "edge")
	gatewayReady(t, f, "pooled")

	f.setGatewaySpec("edge", `{"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
	eventually(t, func() bool {
		s, _ := f.gatewayStatus("edge")
		return cond(s.Conditions, gateway.CondProgrammed).Reason == gateway.ReasonAddressNotAssigned
	}, "the removal is reported")
	s, _ := f.gatewayStatus("edge")
	assert.Equal(t, api.ConditionFalse, cond(s.Conditions, gateway.CondProgrammed).Status)
	assert.Equal(t, []gateway.Address{{Type: "IPAddress", Value: "192.168.1.50"}}, s.Addresses)
	svc, _ := f.service("gateway-edge")
	assert.Equal(t, "192.168.1.50", svc.Spec.LoadBalancerIP)

	f.settle(t)
	p, _ := f.gatewayStatus("pooled")
	assert.Equal(t, api.ConditionTrue, cond(p.Conditions, gateway.CondProgrammed).Status, "control: a Gateway that never asked for an address is not reported")
}

func TestRun_AnAddressChangeDoesNotHoldBackTheListenersOfTheSameEdit(t *testing.T) {
	f := newFakeAPI(t)
	f.addGateway("edge", "uid-edge", `{"addresses":[{"type":"IPAddress","value":"192.168.1.50"}],"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
	f.start(t)
	gatewayReady(t, f, "edge")

	f.setGatewaySpec("edge", `{"addresses":[{"type":"IPAddress","value":"192.168.1.51"}],"listeners":[{"name":"web","port":80,"protocol":"HTTP"},{"name":"alt","port":8080,"protocol":"HTTP"}]}`)
	eventually(t, func() bool {
		svc, _ := f.service("gateway-edge")
		dep, _ := f.object("deployments", "gateway-edge")
		return len(svc.Spec.Ports) == 2 && strings.Contains(string(dep.Spec), `"port":8080`)
	}, "the Service and the Deployment get the new port")
	eventually(t, func() bool {
		s, _ := f.gatewayStatus("edge")
		return cond(s.Conditions, gateway.CondProgrammed).Reason == gateway.ReasonAddressNotAssigned
	}, "the address change is still reported")
	svc, _ := f.service("gateway-edge")
	assert.Equal(t, "192.168.1.50", svc.Spec.LoadBalancerIP, "control: the address is not applied")
}

func TestRun_AServiceTheServerRefusesWithNoAddressAskedIsNotAnAddressFault(t *testing.T) {
	for _, reason := range []api.Reason{api.ReasonInvalid, api.ReasonConflict} {
		t.Run(string(reason), func(t *testing.T) {
			f := newFakeAPI(t)
			f.refuseAddress("", reason) // the fake refuses a Service that asks for no address
			f.addGateway("pooled", "uid-pooled", `{"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
			f.addGateway("asked", "uid-asked", `{"addresses":[{"type":"IPAddress","value":"192.168.1.70"}],"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
			f.start(t)

			gatewayReady(t, f, "asked")
			eventually(t, func() bool { _, ok := f.gatewayStatus("pooled"); return ok }, "the Gateway has a status")
			s, _ := f.gatewayStatus("pooled")
			assert.Equal(t, api.ConditionTrue, cond(s.Conditions, gateway.CondAccepted).Status, "no address was asked for, so none was refused")
			programmed := cond(s.Conditions, gateway.CondProgrammed)
			assert.Equal(t, api.ConditionFalse, programmed.Status)
			assert.NotEqual(t, gateway.ReasonAddressNotAssigned, programmed.Reason)
			_, ok := f.object("deployments", "gateway-pooled")
			assert.False(t, ok)
		})
	}
}

func TestRun_ADeploymentAnotherObjectOwnsIsNotTakenOver(t *testing.T) {
	f := newFakeAPI(t)
	f.seed("deployments", "gateway-edge", map[string]any{
		"apiVersion": gateway.WorkloadsAPIVersion, "kind": gateway.KindDeployment,
		"metadata": map[string]any{"ownerReferences": []any{map[string]any{"apiVersion": gateway.APIVersion, "kind": "Gateway", "name": "edge", "uid": "uid-older"}}},
		"spec":     map[string]any{"replicas": 3},
	})
	f.addGateway("edge", "uid-edge", `{"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
	f.addGateway("fine", "uid-fine", `{"listeners":[{"name":"web","port":80,"protocol":"HTTP"}]}`)
	f.start(t)

	gatewayReady(t, f, "fine")
	eventually(t, func() bool { _, ok := f.gatewayStatus("edge"); return ok }, "the Gateway has a status")
	s, _ := f.gatewayStatus("edge")
	assert.Equal(t, api.ConditionFalse, cond(s.Conditions, gateway.CondProgrammed).Status)
	f.settle(t)
	assert.Zero(t, f.putsOf("deployments/gateway-edge"), "the Deployment is not written")
	_, ok := f.object("deployments", "gateway-fine")
	assert.True(t, ok, "control: a Gateway with no such Deployment gets its own")
}
