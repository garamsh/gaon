package apiserver_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// startServing runs the API server in mode and returns when it answers a
// request, or fails the test with what Run returned when it stops first.
func (c *cluster) startServing(t *testing.T, mode string) (addr string, stop func() error) {
	t.Helper()
	cfg := c.cfg
	cfg.AuthorizationMode = mode
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- apiserver.Run(ctx, cfg, ln, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	stopped := false
	stop = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		return <-done
	}
	t.Cleanup(func() { _ = stop() })

	_, rbac := c.clients(t, ln.Addr().String(), adminID)
	probed := make(chan error, 1)
	go func() {
		_, err := rbac.ListClusterRoles(ctx)
		probed <- err
	}()
	select {
	case err := <-done:
		stopped = true
		require.FailNow(t, "the server stopped before it served", "Run returned: %v", err)
	case err := <-probed:
		require.NoError(t, err, "the server serves, and the admin is bound")
	}
	return ln.Addr().String(), stop
}

// registerFailWebhooks registers a mutating and a validating configuration,
// each with a Fail webhook on resources at path of w.
func registerFailWebhooks(t *testing.T, c *cluster, addr string, w *webhookServer, path string, resources ...string) {
	t.Helper()
	hook := w.hook("start.hooks.example", path, resources...)
	hook.FailurePolicy = api.FailurePolicyFail
	admin := c.admissionClient(t, addr, adminID)
	_, err := admin.PutMutatingWebhookConfiguration(ctx(t), &api.MutatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindMutatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: "start-mutating"},
		Webhooks: []api.Webhook{hook},
	})
	require.NoError(t, err)
	_, err = admin.PutValidatingWebhookConfiguration(ctx(t), &api.ValidatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindValidatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: "start-validating"},
		Webhooks: []api.Webhook{hook},
	})
	require.NoError(t, err)
}

func TestStart_ServesAndCreatesTheBootstrapObjectsWhereAFailWebhookIsUnreachable(t *testing.T) {
	c := newCluster(t)
	addr, stop := c.start(t, apiserver.ModeAlwaysAllow)
	compute, _ := c.clients(t, addr, adminID)
	local, err := compute.GetStorageClass(ctx(t), api.StorageClassLocal)
	require.NoError(t, err)
	_, err = compute.DeleteStorageClass(ctx(t), api.StorageClassLocal, local.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = compute.GetStorageClass(ctx(t), api.StorageClassLocal)
	require.ErrorIs(t, err, api.ErrNotFound, "setup: the class local is gone before the webhooks are registered")

	dead := newWebhookServer(t)
	dead.srv.Close()
	registerFailWebhooks(t, c, addr, dead, "/", "clusterroles", "clusterrolebindings", "storageclasses")
	_, err = compute.PutStorageClass(ctx(t), &api.StorageClass{
		TypeMeta:    api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindStorageClass},
		Metadata:    api.ObjectMeta{Name: "ceph"},
		Provisioner: "ceph",
	})
	assert.ErrorIs(t, err, api.ErrInternalError, "control: the unreachable Fail webhook refuses a user's write")
	require.NoError(t, stop())

	addr, _ = c.startServing(t, apiserver.ModeRBAC)
	compute, rbac := c.clients(t, addr, adminID)
	for _, role := range []string{"gaon:scheduler", "gaon:controllers", "gaon:admin"} {
		_, err := rbac.GetClusterRole(ctx(t), role)
		assert.NoError(t, err, "clusterrole "+role)
		_, err = rbac.GetClusterRoleBinding(ctx(t), role)
		assert.NoError(t, err, "clusterrolebinding "+role)
	}
	class, err := compute.GetStorageClass(ctx(t), api.StorageClassLocal)
	require.NoError(t, err)
	assert.Equal(t, api.StorageClassLocal, class.Provisioner)
	assert.True(t, class.AllowVolumeExpansion)
}

func TestStart_CallsNoWebhookForABootstrapObjectThatExists(t *testing.T) {
	c := newCluster(t)
	addr, stop := c.startServing(t, apiserver.ModeRBAC)
	hook := newWebhookServer(t)
	registerFailWebhooks(t, c, addr, hook, "/", "*", "*/*")
	require.NoError(t, stop())

	addr, _ = c.startServing(t, apiserver.ModeRBAC)
	assert.Empty(t, hook.received(), "no webhook is called at start")

	_, rbac := c.clients(t, addr, adminID)
	_, err := rbac.PutClusterRole(ctx(t), clusterRole("viewer", rule(api.Group, "microvms", api.VerbGet)))
	require.NoError(t, err)
	assert.NotEmpty(t, hook.received(), "control: the same webhook is called for a user's write")
}
