package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newCoordinationLease(name string) *api.CoordinationLease {
	return &api.CoordinationLease{
		TypeMeta: api.TypeMeta{APIVersion: api.CoordinationGroupVersion, Kind: api.KindCoordinationLease},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.CoordinationLeaseSpec{Holder: "controller-1", RenewTime: time.Now().UTC(), DurationSeconds: 15},
	}
}

func TestCoordinationLeaseValidateChecksIdentityAndMetadata(t *testing.T) {
	require.NoError(t, newCoordinationLease("leader").Validate(), "control")

	for name, edit := range map[string]func(*api.CoordinationLease){
		"the compute group": func(l *api.CoordinationLease) { l.APIVersion = api.GroupVersion },
		"another kind":      func(l *api.CoordinationLease) { l.Kind = "Node" },
		"no name":           func(l *api.CoordinationLease) { l.Metadata.Name = "" },
		"a bad label":       func(l *api.CoordinationLease) { l.Metadata.Labels = map[string]string{"bad key": "v"} },
		"two controllers": func(l *api.CoordinationLease) {
			ref := api.OwnerReference{APIVersion: "v", Kind: "K", Name: "n", UID: "u", Controller: true}
			l.Metadata.OwnerReferences = []api.OwnerReference{ref, ref}
		},
	} {
		l := newCoordinationLease("leader")
		edit(l)
		assert.ErrorIs(t, l.Validate(), api.ErrInvalid, name)
	}
}

func TestCoordinationLeaseValidateChecksTheSpec(t *testing.T) {
	for _, d := range []int32{0, -1} {
		l := newCoordinationLease("leader")
		l.Spec.DurationSeconds = d
		assert.ErrorIs(t, l.Validate(), api.ErrInvalid, "duration %d", d)
	}

	l := newCoordinationLease("leader")
	l.Spec.RenewTime = time.Time{}
	assert.ErrorIs(t, l.Validate(), api.ErrInvalid, "a holder without a renew time")

	l.Spec.Holder = ""
	assert.NoError(t, l.Validate(), "a released lease has no holder and needs no renew time")
}

func TestClientCoordinationLeaseUsesItsOwnPaths(t *testing.T) {
	ctx := testContext(t)
	var method, path, query string
	ts := newRecordingServer(t, func(r *http.Request) { method, path, query = r.Method, r.URL.Path, r.URL.RawQuery })
	c := api.NewClient(ts.URL+"/coordination", nil)

	_, err := c.PutCoordinationLease(ctx, newCoordinationLease("leader"))
	require.NoError(t, err)
	assert.Equal(t, http.MethodPut, method)
	assert.Equal(t, "/coordination/leases/leader", path)

	_, err = c.GetCoordinationLease(ctx, "leader")
	require.NoError(t, err)
	assert.Equal(t, "/coordination/leases/leader", path)

	_, err = c.DeleteCoordinationLease(ctx, "leader", "7")
	require.NoError(t, err)
	assert.Equal(t, http.MethodDelete, method)
	assert.Equal(t, "resourceVersion=7", query)

	_, err = c.DeleteCoordinationLease(ctx, "leader", "")
	assert.Error(t, err, "a delete without a version is not sent")
}
