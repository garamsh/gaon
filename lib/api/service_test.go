package api_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newService(name string) *api.Service {
	return &api.Service{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindService},
		Metadata: api.ObjectMeta{Name: name},
		Spec: api.ServiceSpec{
			Selector: map[string]string{"app": "web"},
			Ports:    []api.ServicePort{{Protocol: api.ProtocolTCP, Port: 80, TargetPort: &api.PortRef{Number: 8080}}},
		},
	}
}

func TestServiceValidate(t *testing.T) {
	two := func() []api.ServicePort {
		return []api.ServicePort{
			{Name: "http", Protocol: api.ProtocolTCP, Port: 80},
			{Name: "dns", Protocol: api.ProtocolUDP, Port: 53},
		}
	}
	tests := map[string]struct {
		mutate func(s *api.Service)
		valid  bool
	}{
		"complete":                   {func(s *api.Service) {}, true},
		"wrong kind":                 {func(s *api.Service) { s.Kind = api.KindEndpoints }, false},
		"wrong group version":        {func(s *api.Service) { s.APIVersion = "v1" }, false},
		"name of 63 characters":      {func(s *api.Service) { s.Metadata.Name = "a" + strings.Repeat("b", 62) }, true},
		"name of 64 characters":      {func(s *api.Service) { s.Metadata.Name = "a" + strings.Repeat("b", 63) }, false},
		"name with a dot":            {func(s *api.Service) { s.Metadata.Name = "web.prod" }, false},
		"name starting with a digit": {func(s *api.Service) { s.Metadata.Name = "1web" }, false},
		"name ending with a dash":    {func(s *api.Service) { s.Metadata.Name = "web-" }, false},
		"invalid metadata":           {func(s *api.Service) { s.Metadata.Labels = map[string]string{"bad key!": "v"} }, false},
		"invalid selector":           {func(s *api.Service) { s.Spec.Selector = map[string]string{"bad key!": "v"} }, false},
		"no selector":                {func(s *api.Service) { s.Spec.Selector = nil }, true},
		"type ClusterIP":             {func(s *api.Service) { s.Spec.Type = api.ServiceClusterIP }, true},
		"type LoadBalancer":          {func(s *api.Service) { s.Spec.Type = api.ServiceLoadBalancer }, true},
		"unknown type":               {func(s *api.Service) { s.Spec.Type = "NodePort" }, false},
		"no ports":                   {func(s *api.Service) { s.Spec.Ports = nil }, false},
		"no ports and headless":      {func(s *api.Service) { s.Spec.Ports = nil; s.Spec.ClusterIP = api.ClusterIPNone }, true},
		"headless LoadBalancer":      {func(s *api.Service) { s.Spec.ClusterIP = api.ClusterIPNone; s.Spec.Type = api.ServiceLoadBalancer }, false},
		"port 0":                     {func(s *api.Service) { s.Spec.Ports[0].Port = 0 }, false},
		"port 65536":                 {func(s *api.Service) { s.Spec.Ports[0].Port = 65536 }, false},
		"port 65535":                 {func(s *api.Service) { s.Spec.Ports[0].Port = 65535 }, true},
		"protocol SCTP":              {func(s *api.Service) { s.Spec.Ports[0].Protocol = "SCTP" }, false},
		"protocol empty":             {func(s *api.Service) { s.Spec.Ports[0].Protocol = "" }, false},
		"two named ports":            {func(s *api.Service) { s.Spec.Ports = two() }, true},
		"two ports, one unnamed":     {func(s *api.Service) { s.Spec.Ports = two(); s.Spec.Ports[1].Name = "" }, false},
		"two ports, one name twice":  {func(s *api.Service) { s.Spec.Ports = two(); s.Spec.Ports[1].Name = "http" }, false},
		"port name in capitals":      {func(s *api.Service) { s.Spec.Ports[0].Name = "HTTP" }, false},
		"port name of 64 characters": {func(s *api.Service) { s.Spec.Ports[0].Name = "a" + strings.Repeat("b", 63) }, false},
		"port and protocol twice": {func(s *api.Service) {
			s.Spec.Ports = two()
			s.Spec.Ports[1].Port = 80
			s.Spec.Ports[1].Protocol = api.ProtocolTCP
		}, false},
		"port twice over two protocols":  {func(s *api.Service) { s.Spec.Ports = two(); s.Spec.Ports[1].Port = 80 }, true},
		"targetPort a name":              {func(s *api.Service) { s.Spec.Ports[0].TargetPort = &api.PortRef{Name: "http"} }, true},
		"targetPort a name of 16":        {func(s *api.Service) { s.Spec.Ports[0].TargetPort = &api.PortRef{Name: strings.Repeat("a", 16)} }, false},
		"targetPort a bad name":          {func(s *api.Service) { s.Spec.Ports[0].TargetPort = &api.PortRef{Name: "Http"} }, false},
		"targetPort a name and a number": {func(s *api.Service) { s.Spec.Ports[0].TargetPort = &api.PortRef{Name: "http", Number: 80} }, false},
		"targetPort 0":                   {func(s *api.Service) { s.Spec.Ports[0].TargetPort = &api.PortRef{} }, false},
		"targetPort 65536":               {func(s *api.Service) { s.Spec.Ports[0].TargetPort = &api.PortRef{Number: 65536} }, false},
		"clusterIP an address":           {func(s *api.Service) { s.Spec.ClusterIP = "10.96.0.10" }, true},
		"clusterIP an IPv6 address":      {func(s *api.Service) { s.Spec.ClusterIP = "fd00:96::10" }, true},
		"clusterIP not an address":       {func(s *api.Service) { s.Spec.ClusterIP = "web" }, false},
		"clusterIP a prefix":             {func(s *api.Service) { s.Spec.ClusterIP = "10.96.0.0/16" }, false},
		"clusterIP unspecified":          {func(s *api.Service) { s.Spec.ClusterIP = "0.0.0.0" }, false},
		"clusterIP loopback":             {func(s *api.Service) { s.Spec.ClusterIP = "127.0.0.1" }, false},
		"clusterIP link-local":           {func(s *api.Service) { s.Spec.ClusterIP = "169.254.1.1" }, false},
		"clusterIP multicast":            {func(s *api.Service) { s.Spec.ClusterIP = "224.0.0.1" }, false},
		"clusterIP with a zone":          {func(s *api.Service) { s.Spec.ClusterIP = "fd00::1%eth0" }, false},
		"loadBalancerIP on ClusterIP":    {func(s *api.Service) { s.Spec.LoadBalancerIP = "192.168.1.50" }, false},
		"loadBalancerIP on LoadBalancer": {func(s *api.Service) { s.Spec.Type = api.ServiceLoadBalancer; s.Spec.LoadBalancerIP = "192.168.1.50" }, true},
		"loadBalancerIP not an address":  {func(s *api.Service) { s.Spec.Type = api.ServiceLoadBalancer; s.Spec.LoadBalancerIP = "lan" }, false},
		"status on a LoadBalancer": {func(s *api.Service) {
			s.Spec.Type = api.ServiceLoadBalancer
			s.Status.LoadBalancer = &api.LoadBalancerStatus{Ingress: []api.LoadBalancerIngress{{IP: "192.168.1.50"}}}
		}, true},
		"status on a ClusterIP": {func(s *api.Service) {
			s.Status.LoadBalancer = &api.LoadBalancerStatus{Ingress: []api.LoadBalancerIngress{{IP: "192.168.1.50"}}}
		}, false},
		"status with two addresses": {func(s *api.Service) {
			s.Spec.Type = api.ServiceLoadBalancer
			s.Status.LoadBalancer = &api.LoadBalancerStatus{Ingress: []api.LoadBalancerIngress{{IP: "192.168.1.50"}, {IP: "192.168.1.51"}}}
		}, false},
		"status with a bad address": {func(s *api.Service) {
			s.Spec.Type = api.ServiceLoadBalancer
			s.Status.LoadBalancer = &api.LoadBalancerStatus{Ingress: []api.LoadBalancerIngress{{IP: "lan"}}}
		}, false},
		"status with no addresses on a ClusterIP": {func(s *api.Service) { s.Status.LoadBalancer = &api.LoadBalancerStatus{} }, true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newService("web")
			tt.mutate(s)
			err := s.Validate()
			if tt.valid {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, api.ErrInvalid)
		})
	}
}

func TestServiceValidateUpdateKeepsTheAddressesAndTheType(t *testing.T) {
	stored := newService("web")
	stored.Spec.ClusterIP = "10.96.0.10"
	tests := map[string]struct {
		mutate func(s *api.Service)
		valid  bool
	}{
		"selector and ports changed":   {func(s *api.Service) { s.Spec.Selector = map[string]string{"app": "api"}; s.Spec.Ports[0].Port = 81 }, true},
		"unchanged":                    {func(s *api.Service) {}, true},
		"clusterIP changed":            {func(s *api.Service) { s.Spec.ClusterIP = "10.96.0.11" }, false},
		"clusterIP removed":            {func(s *api.Service) { s.Spec.ClusterIP = "" }, false},
		"clusterIP made headless":      {func(s *api.Service) { s.Spec.ClusterIP = api.ClusterIPNone }, false},
		"type changed":                 {func(s *api.Service) { s.Spec.Type = api.ServiceLoadBalancer }, false},
		"type spelled out is the same": {func(s *api.Service) { s.Spec.Type = api.ServiceClusterIP }, true},
		"invalid after the update":     {func(s *api.Service) { s.Spec.Ports = nil }, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newService("web")
			s.Spec.ClusterIP = "10.96.0.10"
			tt.mutate(s)
			err := s.ValidateUpdate(stored)
			if tt.valid {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, api.ErrInvalid)
		})
	}

	lb := newService("web")
	lb.Spec.Type = api.ServiceLoadBalancer
	lb.Spec.LoadBalancerIP = "192.168.1.50"
	changed := *lb
	changed.Spec.LoadBalancerIP = "192.168.1.51"
	assert.ErrorIs(t, changed.ValidateUpdate(lb), api.ErrInvalid, "the LAN address never changes")
	same := *lb
	assert.NoError(t, same.ValidateUpdate(lb))
}

func TestServiceKeepAllocatedFillsOnlyEmptyAddresses(t *testing.T) {
	stored := newService("web")
	stored.Spec.Type = api.ServiceLoadBalancer
	stored.Spec.ClusterIP = "10.96.0.10"
	stored.Spec.LoadBalancerIP = "192.168.1.50"
	update := func(mutate func(s *api.Service)) *api.Service {
		s := newService("web")
		s.Spec.Type = api.ServiceLoadBalancer
		mutate(s)
		s.KeepAllocated(stored)
		return s
	}

	both := update(func(s *api.Service) {})
	assert.Equal(t, "10.96.0.10", both.Spec.ClusterIP)
	assert.Equal(t, "192.168.1.50", both.Spec.LoadBalancerIP)
	assert.NoError(t, both.ValidateUpdate(stored), "an update that carries neither address is accepted")

	same := update(func(s *api.Service) { s.Spec.ClusterIP = "10.96.0.10" })
	assert.NoError(t, same.ValidateUpdate(stored))

	other := update(func(s *api.Service) { s.Spec.ClusterIP = "10.96.0.11" })
	assert.Equal(t, "10.96.0.11", other.Spec.ClusterIP, "a value that is given is not replaced")
	assert.ErrorIs(t, other.ValidateUpdate(stored), api.ErrInvalid)

	otherLAN := update(func(s *api.Service) { s.Spec.LoadBalancerIP = "192.168.1.51" })
	assert.ErrorIs(t, otherLAN.ValidateUpdate(stored), api.ErrInvalid)
}

func TestServiceSpecEffectiveTypeAndHeadless(t *testing.T) {
	assert.Equal(t, api.ServiceClusterIP, api.ServiceSpec{}.EffectiveType())
	assert.Equal(t, api.ServiceLoadBalancer, api.ServiceSpec{Type: api.ServiceLoadBalancer}.EffectiveType())
	assert.True(t, api.ServiceSpec{ClusterIP: api.ClusterIPNone}.Headless())
	assert.False(t, api.ServiceSpec{ClusterIP: "10.96.0.10"}.Headless())
	assert.False(t, api.ServiceSpec{}.Headless())
}

func TestServicePortTargetPortIsAStringOrANumberInJSON(t *testing.T) {
	var named, numbered api.ServicePort
	require.NoError(t, json.Unmarshal([]byte(`{"protocol":"TCP","port":80,"targetPort":"http"}`), &named))
	require.NoError(t, json.Unmarshal([]byte(`{"protocol":"TCP","port":80,"targetPort":8080}`), &numbered))
	assert.Equal(t, &api.PortRef{Name: "http"}, named.TargetPort)
	assert.Equal(t, &api.PortRef{Number: 8080}, numbered.TargetPort)

	out, err := json.Marshal(named)
	require.NoError(t, err)
	assert.JSONEq(t, `{"protocol":"TCP","port":80,"targetPort":"http"}`, string(out))
	out, err = json.Marshal(api.ServicePort{Protocol: api.ProtocolTCP, Port: 80})
	require.NoError(t, err)
	assert.JSONEq(t, `{"protocol":"TCP","port":80}`, string(out), "an absent targetPort is omitted")
}

func TestClientServicePutGetListDelete(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	_, err := c.GetService(ctx, "web")
	assert.ErrorIs(t, err, api.ErrNotFound)

	created, err := c.PutService(ctx, newService("web"))
	require.NoError(t, err)
	_, err = c.PutService(ctx, newService("web"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists)

	bad := newService("web2")
	bad.Spec.Ports = nil
	_, err = c.PutService(ctx, bad)
	assert.ErrorIs(t, err, api.ErrInvalid)

	got, err := c.GetService(ctx, "web")
	require.NoError(t, err)
	assert.Equal(t, &api.PortRef{Number: 8080}, got.Spec.Ports[0].TargetPort)

	list, err := c.ListServices(ctx)
	require.NoError(t, err)
	assert.Equal(t, api.KindServiceList, list.Kind)
	require.Len(t, list.Items, 1)

	w, err := c.WatchServices(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	assert.Equal(t, api.EventAdded, nextEvent(t, w).Type)

	_, err = c.DeleteService(ctx, "web", "")
	assert.Error(t, err, "a delete names the version it deletes")
	_, err = c.DeleteService(ctx, "web", created.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = c.GetService(ctx, "web")
	assert.ErrorIs(t, err, api.ErrNotFound)
}
