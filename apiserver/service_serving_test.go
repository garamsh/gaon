package apiserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newSvc(name string) *api.Service {
	return &api.Service{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindService},
		Metadata: api.ObjectMeta{Name: name},
		Spec: api.ServiceSpec{
			Selector: map[string]string{"app": "web"},
			Ports:    []api.ServicePort{{Protocol: api.ProtocolTCP, Port: 80}},
		},
	}
}

func newEndpoints(name string) *api.Endpoints {
	return &api.Endpoints{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEndpoints},
		Metadata: api.ObjectMeta{Name: name},
		Subsets: []api.EndpointSubset{{
			Addresses: []api.EndpointAddress{{IP: "10.244.1.5", TargetRef: &api.ObjectReference{APIVersion: api.GroupVersion, Kind: api.KindMicroVM, Name: "web-0"}}},
			Ports:     []api.EndpointPort{{Name: "http", Port: 8080, Protocol: api.ProtocolTCP}},
		}},
	}
}

func newPolicy(name string) *api.NetworkPolicy {
	return &api.NetworkPolicy{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNetworkPolicy},
		Metadata: api.ObjectMeta{Name: name},
		Spec: api.NetworkPolicySpec{
			PodSelector: api.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Ingress: []api.IngressRule{{
				From:  []api.NetworkPolicyPeer{{PodSelector: &api.LabelSelector{MatchLabels: map[string]string{"app": "api"}}}},
				Ports: []api.NetworkPolicyPort{{Protocol: api.ProtocolTCP, Port: 8080}},
			}},
		},
	}
}

// ranged is a server given the service prefix and the LAN pool.
func ranged(t *testing.T) *env {
	t.Helper()
	return newEnvWith(t, envOptions{serviceRange: "10.96.0.0/24", lanPool: "192.168.50.0/28"})
}

func TestService_CreateReadListWatchAndDelete(t *testing.T) {
	e := ranged(t)

	created, err := e.api.PutService(ctx(t), newSvc("web"))
	require.NoError(t, err)
	assert.NotEmpty(t, created.Metadata.ResourceVersion)
	assert.NotEmpty(t, created.Metadata.UID)
	assert.NotEmpty(t, created.Spec.ClusterIP, "the address is allocated in the create")

	got, err := e.api.GetService(ctx(t), "web")
	require.NoError(t, err)
	assert.Equal(t, created.Spec.ClusterIP, got.Spec.ClusterIP)
	list, err := e.api.ListServices(ctx(t))
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, api.KindServiceList, list.Kind)

	w, err := e.api.WatchServices(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()
	update := *got
	update.Spec.Ports = []api.ServicePort{{Protocol: api.ProtocolTCP, Port: 8080}}
	updated, err := e.api.PutService(ctx(t), &update)
	require.NoError(t, err)
	ev, err := w.Next()
	require.NoError(t, err)
	assert.Equal(t, api.EventModified, ev.Type)
	assert.Equal(t, int32(8080), ev.Object.Spec.Ports[0].Port)

	_, err = e.api.DeleteService(ctx(t), "web", created.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version")
	_, err = e.api.GetService(ctx(t), "web")
	assert.NoError(t, err, "control: the refused delete removed nothing")
	_, err = e.api.DeleteService(ctx(t), "web", updated.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = e.api.GetService(ctx(t), "web")
	assert.ErrorIs(t, err, api.ErrNotFound)
	_, err = e.api.DeleteService(ctx(t), "web", updated.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestService_IsCheckedWithItsValidate(t *testing.T) {
	e := ranged(t)

	bad := newSvc("bad")
	bad.Spec.Ports = nil
	_, err := e.api.PutService(ctx(t), bad)
	assert.ErrorIs(t, err, api.ErrInvalid, "a Service that is not headless has a port")
	capital := newSvc("Web")
	_, err = e.api.PutService(ctx(t), capital)
	assert.ErrorIs(t, err, api.ErrInvalid, "a name that is not a DNS label")
	typed := newSvc("typed")
	typed.Spec.LoadBalancerIP = "192.168.50.3"
	_, err = e.api.PutService(ctx(t), typed)
	assert.ErrorIs(t, err, api.ErrInvalid, "a loadBalancerIP on a ClusterIP Service")
	_, err = e.api.PutService(ctx(t), newSvc("fine"))
	assert.NoError(t, err, "control: a Service that validates")
	list, err := e.api.ListServices(ctx(t))
	require.NoError(t, err)
	assert.Len(t, list.Items, 1, "the refused ones stored nothing")
}

func TestService_APathNameThatDiffersFromTheBodyIsInvalid(t *testing.T) {
	e := ranged(t)
	body := `{"apiVersion":"` + api.GroupVersion + `","kind":"Service","metadata":{"name":"web"},"spec":{"ports":[{"protocol":"TCP","port":80}]}}`

	status, _ := e.send(t, "PUT", "/services/other", body)
	assert.Equal(t, 422, status)
	status, _ = e.send(t, "PUT", "/services/web", body)
	assert.Equal(t, 200, status, "control: the same body at its own path")
}

func TestService_AnUpdateKeepsTheStatusAndRefusesAChangedTypeOrAddress(t *testing.T) {
	e := ranged(t)
	lb := newSvc("lb")
	lb.Spec.Type = api.ServiceLoadBalancer
	created, err := e.api.PutService(ctx(t), lb)
	require.NoError(t, err)
	require.NotNil(t, created.Status.LoadBalancer)

	forged := *created
	forged.Status.LoadBalancer = &api.LoadBalancerStatus{Ingress: []api.LoadBalancerIngress{{IP: "192.168.50.9"}}}
	forged.Spec.ClusterIP = ""
	kept, err := e.api.PutService(ctx(t), &forged)
	require.NoError(t, err)
	assert.Equal(t, created.Status, kept.Status, "an update keeps the stored status")
	assert.Equal(t, created.Spec.ClusterIP, kept.Spec.ClusterIP)

	retype := *kept
	retype.Spec.Type = api.ServiceClusterIP
	retype.Spec.LoadBalancerIP = ""
	_, err = e.api.PutService(ctx(t), &retype)
	assert.ErrorIs(t, err, api.ErrInvalid, "the type never changes")
	moved := *kept
	moved.Spec.ClusterIP = "10.96.0.200"
	_, err = e.api.PutService(ctx(t), &moved)
	assert.ErrorIs(t, err, api.ErrInvalid, "the address never changes")
	same := *kept
	same.Spec.Ports = []api.ServicePort{{Protocol: api.ProtocolTCP, Port: 81}}
	_, err = e.api.PutService(ctx(t), &same)
	assert.NoError(t, err, "control: an update that changes neither")
}

func TestEndpoints_CreateReadListWatchAndDelete(t *testing.T) {
	e := newEnv(t)

	created, err := e.api.PutEndpoints(ctx(t), newEndpoints("web"))
	require.NoError(t, err)
	assert.NotEmpty(t, created.Metadata.ResourceVersion)
	got, err := e.api.GetEndpoints(ctx(t), "web")
	require.NoError(t, err)
	assert.Equal(t, "10.244.1.5", got.Subsets[0].Addresses[0].IP)
	list, err := e.api.ListEndpoints(ctx(t))
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, api.KindEndpointsList, list.Kind)

	w, err := e.api.WatchEndpoints(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()
	update := *got
	update.Subsets = nil
	updated, err := e.api.PutEndpoints(ctx(t), &update)
	require.NoError(t, err)
	ev, err := w.Next()
	require.NoError(t, err)
	assert.Equal(t, api.EventModified, ev.Type)
	assert.Empty(t, ev.Object.Subsets)

	_, err = e.api.DeleteEndpoints(ctx(t), "web", created.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version")
	_, err = e.api.DeleteEndpoints(ctx(t), "web", updated.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = e.api.GetEndpoints(ctx(t), "web")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestEndpoints_IsCheckedWithItsValidate(t *testing.T) {
	e := newEnv(t)

	bad := newEndpoints("bad")
	bad.Subsets[0].Addresses[0].IP = "127.0.0.1"
	_, err := e.api.PutEndpoints(ctx(t), bad)
	assert.ErrorIs(t, err, api.ErrInvalid, "a loopback address")
	noPorts := newEndpoints("noports")
	noPorts.Subsets[0].Ports = nil
	_, err = e.api.PutEndpoints(ctx(t), noPorts)
	assert.ErrorIs(t, err, api.ErrInvalid, "a subset without ports")
	_, err = e.api.PutEndpoints(ctx(t), newEndpoints("fine"))
	assert.NoError(t, err, "control: an Endpoints that validates")
}

func TestNetworkPolicy_CreateReadListWatchAndDelete(t *testing.T) {
	e := newEnv(t)

	created, err := e.api.PutNetworkPolicy(ctx(t), newPolicy("web"))
	require.NoError(t, err)
	assert.NotEmpty(t, created.Metadata.ResourceVersion)
	got, err := e.api.GetNetworkPolicy(ctx(t), "web")
	require.NoError(t, err)
	assert.Equal(t, "web", got.Spec.PodSelector.MatchLabels["app"])
	list, err := e.api.ListNetworkPolicies(ctx(t))
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, api.KindNetworkPolicyList, list.Kind)

	w, err := e.api.WatchNetworkPolicies(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()
	update := *got
	update.Spec.Ingress = nil
	updated, err := e.api.PutNetworkPolicy(ctx(t), &update)
	require.NoError(t, err)
	ev, err := w.Next()
	require.NoError(t, err)
	assert.Equal(t, api.EventModified, ev.Type)
	assert.Empty(t, ev.Object.Spec.Ingress)

	_, err = e.api.DeleteNetworkPolicy(ctx(t), "web", created.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version")
	_, err = e.api.DeleteNetworkPolicy(ctx(t), "web", updated.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = e.api.GetNetworkPolicy(ctx(t), "web")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestNetworkPolicy_IsCheckedWithItsValidate(t *testing.T) {
	e := newEnv(t)

	bad := newPolicy("bad")
	bad.Spec.Ingress[0].Ports[0].Protocol = "SCTP"
	_, err := e.api.PutNetworkPolicy(ctx(t), bad)
	assert.ErrorIs(t, err, api.ErrInvalid, "a protocol that is not TCP or UDP")
	_, err = e.api.PutNetworkPolicy(ctx(t), newPolicy("fine"))
	assert.NoError(t, err, "control: a NetworkPolicy that validates")
}

func TestServiceKinds_AListTakesALabelSelector(t *testing.T) {
	e := ranged(t)
	for _, name := range []string{"a", "b"} {
		l := map[string]string{"tier": "front"}
		if name == "b" {
			l = map[string]string{"tier": "back"}
		}
		s := newSvc(name)
		s.Metadata.Labels = l
		_, err := e.api.PutService(ctx(t), s)
		require.NoError(t, err)
		ep := newEndpoints(name)
		ep.Metadata.Labels = l
		_, err = e.api.PutEndpoints(ctx(t), ep)
		require.NoError(t, err)
		p := newPolicy(name)
		p.Metadata.Labels = l
		_, err = e.api.PutNetworkPolicy(ctx(t), p)
		require.NoError(t, err)
	}
	selected := api.NewClient(e.base+apiRoot, nil, api.WithLabelSelector(mustSelector(t, "tier=front")))

	services, err := selected.ListServices(ctx(t))
	require.NoError(t, err)
	require.Len(t, services.Items, 1)
	assert.Equal(t, "a", services.Items[0].Metadata.Name)
	endpoints, err := selected.ListEndpoints(ctx(t))
	require.NoError(t, err)
	require.Len(t, endpoints.Items, 1)
	assert.Equal(t, "a", endpoints.Items[0].Metadata.Name)
	policies, err := selected.ListNetworkPolicies(ctx(t))
	require.NoError(t, err)
	require.Len(t, policies.Items, 1)
	assert.Equal(t, "a", policies.Items[0].Metadata.Name)

	all, err := e.api.ListServices(ctx(t))
	require.NoError(t, err)
	assert.Len(t, all.Items, 2, "control: without a selector both are listed")
}
