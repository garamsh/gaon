package api_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newEndpoints(name string) *api.Endpoints {
	return &api.Endpoints{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEndpoints},
		Metadata: api.ObjectMeta{Name: name},
		Subsets: []api.EndpointSubset{{
			Addresses: []api.EndpointAddress{{
				IP:        "10.244.1.5",
				TargetRef: &api.ObjectReference{APIVersion: api.GroupVersion, Kind: api.KindMicroVM, Name: "web-0"},
			}},
			Ports: []api.EndpointPort{{Name: "http", Port: 8080, Protocol: api.ProtocolTCP}},
		}},
	}
}

func TestEndpointsValidate(t *testing.T) {
	many := func(n int) []api.EndpointAddress {
		a := make([]api.EndpointAddress, n)
		for i := range a {
			a[i] = api.EndpointAddress{IP: "10.244.1.5"}
		}
		return a
	}
	tests := map[string]struct {
		mutate func(e *api.Endpoints)
		valid  bool
	}{
		"complete":                 {func(e *api.Endpoints) {}, true},
		"no subsets":               {func(e *api.Endpoints) { e.Subsets = nil }, true},
		"wrong kind":               {func(e *api.Endpoints) { e.Kind = api.KindService }, false},
		"wrong group version":      {func(e *api.Endpoints) { e.APIVersion = "v1" }, false},
		"name of a Service":        {func(e *api.Endpoints) { e.Metadata.Name = "a" + strings.Repeat("b", 62) }, true},
		"name of 64 characters":    {func(e *api.Endpoints) { e.Metadata.Name = "a" + strings.Repeat("b", 63) }, false},
		"name with a dot":          {func(e *api.Endpoints) { e.Metadata.Name = "web.prod" }, false},
		"invalid metadata":         {func(e *api.Endpoints) { e.Metadata.Labels = map[string]string{"bad key!": "v"} }, false},
		"subset without addresses": {func(e *api.Endpoints) { e.Subsets[0].Addresses = nil }, false},
		"subset without ports":     {func(e *api.Endpoints) { e.Subsets[0].Ports = nil }, false},
		"address not an IP":        {func(e *api.Endpoints) { e.Subsets[0].Addresses[0].IP = "web-0" }, false},
		"address loopback":         {func(e *api.Endpoints) { e.Subsets[0].Addresses[0].IP = "127.0.0.1" }, false},
		"address without a ref":    {func(e *api.Endpoints) { e.Subsets[0].Addresses[0].TargetRef = nil }, true},
		"ref of another kind":      {func(e *api.Endpoints) { e.Subsets[0].Addresses[0].TargetRef.Kind = api.KindNode }, false},
		"ref of another group":     {func(e *api.Endpoints) { e.Subsets[0].Addresses[0].TargetRef.APIVersion = "v1" }, false},
		"ref without a name":       {func(e *api.Endpoints) { e.Subsets[0].Addresses[0].TargetRef.Name = "" }, false},
		"port 0":                   {func(e *api.Endpoints) { e.Subsets[0].Ports[0].Port = 0 }, false},
		"port 65536":               {func(e *api.Endpoints) { e.Subsets[0].Ports[0].Port = 65536 }, false},
		"protocol SCTP":            {func(e *api.Endpoints) { e.Subsets[0].Ports[0].Protocol = "SCTP" }, false},
		"port without a name":      {func(e *api.Endpoints) { e.Subsets[0].Ports[0].Name = "" }, true},
		"port name in capitals":    {func(e *api.Endpoints) { e.Subsets[0].Ports[0].Name = "HTTP" }, false},
		"port name twice": {func(e *api.Endpoints) {
			e.Subsets[0].Ports = append(e.Subsets[0].Ports, api.EndpointPort{Name: "http", Port: 9090, Protocol: api.ProtocolTCP})
		}, false},
		"port and protocol twice": {func(e *api.Endpoints) {
			e.Subsets[0].Ports = append(e.Subsets[0].Ports, api.EndpointPort{Name: "alt", Port: 8080, Protocol: api.ProtocolTCP})
		}, false},
		"port twice over protocols": {func(e *api.Endpoints) {
			e.Subsets[0].Ports = append(e.Subsets[0].Ports, api.EndpointPort{Name: "alt", Port: 8080, Protocol: api.ProtocolUDP})
		}, true},
		"1000 addresses": {func(e *api.Endpoints) { e.Subsets[0].Addresses = many(api.MaxEndpointsAddresses) }, true},
		"1001 addresses": {func(e *api.Endpoints) { e.Subsets[0].Addresses = many(api.MaxEndpointsAddresses + 1) }, false},
		"1001 addresses over two subsets": {func(e *api.Endpoints) {
			e.Subsets[0].Addresses = many(500)
			e.Subsets = append(e.Subsets, api.EndpointSubset{Addresses: many(501), Ports: []api.EndpointPort{{Port: 9090, Protocol: api.ProtocolTCP}}})
		}, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			e := newEndpoints("web")
			tt.mutate(e)
			err := e.Validate()
			if tt.valid {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, api.ErrInvalid)
		})
	}
}

func TestClientEndpointsPutGetListDelete(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	_, err := c.GetEndpoints(ctx, "web")
	assert.ErrorIs(t, err, api.ErrNotFound)

	created, err := c.PutEndpoints(ctx, newEndpoints("web"))
	require.NoError(t, err)

	bad := newEndpoints("web2")
	bad.Subsets[0].Ports = nil
	_, err = c.PutEndpoints(ctx, bad)
	assert.ErrorIs(t, err, api.ErrInvalid)

	got, err := c.GetEndpoints(ctx, "web")
	require.NoError(t, err)
	assert.Equal(t, "web-0", got.Subsets[0].Addresses[0].TargetRef.Name)

	list, err := c.ListEndpoints(ctx)
	require.NoError(t, err)
	assert.Equal(t, api.KindEndpointsList, list.Kind)
	require.Len(t, list.Items, 1)

	w, err := c.WatchEndpoints(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	assert.Equal(t, api.EventAdded, nextEvent(t, w).Type)

	_, err = c.DeleteEndpoints(ctx, "web", created.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = c.GetEndpoints(ctx, "web")
	assert.ErrorIs(t, err, api.ErrNotFound)
}
