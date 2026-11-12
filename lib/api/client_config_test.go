package api_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func TestClientConfigurationKindsUseTheirPaths(t *testing.T) {
	ctx := testContext(t)
	var got []string
	ts := newRecordingServer(t, func(r *http.Request) { got = append(got, r.Method+" "+r.URL.Path) })
	c := api.NewClient(ts.URL+"/compute", nil)

	calls := []struct {
		want string
		do   func() error
	}{
		{"PUT /compute/secrets/creds", func() error { _, err := c.PutSecret(ctx, newSecret("creds")); return err }},
		{"GET /compute/secrets/creds", func() error { _, err := c.GetSecret(ctx, "creds"); return err }},
		{"GET /compute/secrets", func() error { _, err := c.ListSecrets(ctx); return err }},
		{"DELETE /compute/secrets/creds", func() error { _, err := c.DeleteSecret(ctx, "creds", "1"); return err }},
		{"PUT /compute/configmaps/conf", func() error { _, err := c.PutConfigMap(ctx, newConfigMap("conf")); return err }},
		{"GET /compute/configmaps/conf", func() error { _, err := c.GetConfigMap(ctx, "conf"); return err }},
		{"GET /compute/configmaps", func() error { _, err := c.ListConfigMaps(ctx); return err }},
		{"DELETE /compute/configmaps/conf", func() error { _, err := c.DeleteConfigMap(ctx, "conf", "1"); return err }},
		{"PUT /compute/serviceaccounts/web", func() error { _, err := c.PutServiceAccount(ctx, newServiceAccount("web")); return err }},
		{"GET /compute/serviceaccounts/web", func() error { _, err := c.GetServiceAccount(ctx, "web"); return err }},
		{"GET /compute/serviceaccounts", func() error { _, err := c.ListServiceAccounts(ctx); return err }},
		{"DELETE /compute/serviceaccounts/web", func() error { _, err := c.DeleteServiceAccount(ctx, "web", "1"); return err }},
		{"POST /compute/serviceaccounts/web/token", func() error { _, err := c.CreateServiceAccountToken(ctx, "web", newTokenRequest()); return err }},
	}
	for _, call := range calls {
		got = nil
		require.NoError(t, call.do(), call.want)
		assert.Equal(t, []string{call.want}, got)
	}
}

func TestClientDeleteOfConfigurationKindsNeedsAVersion(t *testing.T) {
	ctx := testContext(t)
	ts := newRecordingServer(t, func(r *http.Request) { t.Errorf("sent %s %s", r.Method, r.URL.Path) })
	c := api.NewClient(ts.URL+"/compute", nil)

	_, err := c.DeleteSecret(ctx, "creds", "")
	assert.Error(t, err)
	_, err = c.DeleteConfigMap(ctx, "conf", "")
	assert.Error(t, err)
	_, err = c.DeleteServiceAccount(ctx, "web", "")
	assert.Error(t, err)
}

func TestClientTokenRequestSendsTheRequestAndReadsTheToken(t *testing.T) {
	ctx := testContext(t)
	var sent *http.Request
	ts := newRecordingServer(t, func(r *http.Request) { sent = r })
	c := api.NewClient(ts.URL+"/compute", nil)

	out, err := c.CreateServiceAccountToken(ctx, "web", newTokenRequest())
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "application/json", sent.Header.Get("Content-Type"))
}
