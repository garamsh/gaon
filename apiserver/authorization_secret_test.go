package apiserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// secretFixture is an RBAC server with a token signing key, the Nodes node-1
// and node-2, the Secrets s1 and s2, the ConfigMaps c1 and c2, the
// ServiceAccounts web and other, and the MicroVMs
//   - m1, of web, bound to node-1, which reads s1 and c1;
//   - m2, of web, bound to node-2, which reads s2 and c2;
//   - m3, of web, not bound.
type secretFixture struct {
	admin, node1, node2 *api.Client
	m1, m2, m3          *api.MicroVM
}

func newSecretFixture(t *testing.T) *secretFixture {
	t.Helper()
	c := newCluster(t)
	key := newSigningKey(t)
	c.cfg.ServiceAccountIssuer, c.cfg.ServiceAccountSigningKeyFile = key.issuer, key.file
	addr, _ := c.start(t, apiserver.ModeRBAC)
	f := &secretFixture{}
	f.admin, _ = c.clients(t, addr, adminID)
	f.node1, _ = c.clients(t, addr, nodeID("node-1"))
	f.node2, _ = c.clients(t, addr, nodeID("node-2"))
	for _, n := range []string{"node-1", "node-2"} {
		_, err := f.admin.PutNode(ctx(t), node(n))
		require.NoError(t, err)
	}
	for _, n := range []string{"s1", "s2"} {
		_, err := f.admin.PutSecret(ctx(t), secret(n, map[string][]byte{"k": []byte("v")}))
		require.NoError(t, err)
	}
	for _, n := range []string{"c1", "c2"} {
		_, err := f.admin.PutConfigMap(ctx(t), configMap(n, map[string]string{"k": "v"}))
		require.NoError(t, err)
	}
	for _, n := range []string{"web", "other"} {
		_, err := f.admin.PutServiceAccount(ctx(t), serviceAccount(n))
		require.NoError(t, err)
	}
	f.m1 = f.vm(t, "m1", "s1", "c1", "node-1")
	f.m2 = f.vm(t, "m2", "s2", "c2", "node-2")
	f.m3 = f.vm(t, "m3", "s2", "c2", "")
	return f
}

// vm creates a MicroVM of web that reads secret and configMap, bound to node
// where node is not empty, and returns it as the API server stores it.
func (f *secretFixture) vm(t *testing.T, name, secret, configMap, node string) *api.MicroVM {
	t.Helper()
	m := microvm(name)
	m.Spec.ServiceAccountName = "web"
	m.Spec.Env = []api.EnvVar{
		{Name: "A", ValueFrom: &api.ValueSource{SecretKeyRef: &api.KeyReference{Name: secret, Key: "k"}}},
		{Name: "B", ValueFrom: &api.ValueSource{ConfigMapKeyRef: &api.KeyReference{Name: configMap, Key: "k"}}},
	}
	m, err := f.admin.PutMicroVM(ctx(t), m)
	require.NoError(t, err)
	if node != "" {
		_, err = f.admin.BindMicroVM(ctx(t), name, api.Binding{ResourceVersion: m.Metadata.ResourceVersion, NodeName: node, BindingEpoch: 1})
		require.NoError(t, err)
		m, err = f.admin.GetMicroVM(ctx(t), name)
		require.NoError(t, err)
	}
	return m
}

func TestNodeAuthorizer_ANodeGetsOnlyASecretAMicroVMBoundToItReferences(t *testing.T) {
	f := newSecretFixture(t)

	_, err := f.node1.GetSecret(ctx(t), "s1")
	assert.NoError(t, err, "control: s1 is read by m1, bound to node-1")
	_, err = f.node2.GetSecret(ctx(t), "s1")
	assert.ErrorIs(t, err, api.ErrForbidden, "s1 is read only by a MicroVM bound to node-1")
	_, err = f.node1.GetSecret(ctx(t), "s2")
	assert.ErrorIs(t, err, api.ErrForbidden, "s2 is read by m2 and by an unbound m3, not by a MicroVM of node-1")
	_, err = f.node2.GetSecret(ctx(t), "s2")
	assert.NoError(t, err, "control: s2 is read by m2, bound to node-2")
}

func TestNodeAuthorizer_ANodeGetsOnlyAConfigMapAMicroVMBoundToItReferences(t *testing.T) {
	f := newSecretFixture(t)

	_, err := f.node1.GetConfigMap(ctx(t), "c1")
	assert.NoError(t, err, "control: c1 is read by m1, bound to node-1")
	_, err = f.node2.GetConfigMap(ctx(t), "c1")
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node1.GetConfigMap(ctx(t), "c2")
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node2.GetConfigMap(ctx(t), "c2")
	assert.NoError(t, err, "control: c2 is read by m2, bound to node-2")
}

func TestNodeAuthorizer_ANodeNeitherListsNorWatchesSecretsOrConfigMaps(t *testing.T) {
	f := newSecretFixture(t)
	secrets, err := f.admin.ListSecrets(ctx(t))
	require.NoError(t, err, "control: an administrator lists Secrets")
	configMaps, err := f.admin.ListConfigMaps(ctx(t))
	require.NoError(t, err, "control: an administrator lists ConfigMaps")

	_, err = f.node1.ListSecrets(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node1.WatchSecrets(ctx(t), secrets.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node1.ListConfigMaps(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = f.node1.WatchConfigMaps(ctx(t), configMaps.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden)
	w, err := f.admin.WatchSecrets(ctx(t), secrets.Metadata.ResourceVersion)
	require.NoError(t, err, "control: an administrator watches Secrets")
	w.Close()
}

func TestNodeAuthorizer_ANodeRequestsATokenOnlyForAMicroVMBoundToIt(t *testing.T) {
	f := newSecretFixture(t)

	_, err := f.node1.CreateServiceAccountToken(ctx(t), "web", tokenRequest(f.m1, 0, "gaon"))
	assert.NoError(t, err, "control: node-1, for m1 bound to it, of web")

	_, err = f.node2.CreateServiceAccountToken(ctx(t), "web", tokenRequest(f.m1, 0, "gaon"))
	assert.ErrorIs(t, err, api.ErrForbidden, "m1 is bound to node-1")
	_, err = f.node1.CreateServiceAccountToken(ctx(t), "web", tokenRequest(f.m2, 0, "gaon"))
	assert.ErrorIs(t, err, api.ErrForbidden, "m2 is bound to node-2")
	_, err = f.node1.CreateServiceAccountToken(ctx(t), "web", tokenRequest(f.m3, 0, "gaon"))
	assert.ErrorIs(t, err, api.ErrForbidden, "m3 is bound to no Node")
	_, err = f.node1.CreateServiceAccountToken(ctx(t), "other", tokenRequest(f.m1, 0, "gaon"))
	assert.ErrorIs(t, err, api.ErrForbidden, "m1 names web, not other")

	wrongUID := tokenRequest(f.m1, 0, "gaon")
	wrongUID.Spec.BoundMicroVM.UID = "not-its-uid"
	_, err = f.node1.CreateServiceAccountToken(ctx(t), "web", wrongUID)
	assert.ErrorIs(t, err, api.ErrForbidden, "the uid of another MicroVM")

	gone := tokenRequest(f.m1, 0, "gaon")
	gone.Spec.BoundMicroVM.Name = "gone"
	_, err = f.node1.CreateServiceAccountToken(ctx(t), "web", gone)
	assert.ErrorIs(t, err, api.ErrForbidden, "a MicroVM that does not exist")

	_, err = f.admin.CreateServiceAccountToken(ctx(t), "web", tokenRequest(f.m3, 0, "gaon"))
	assert.NoError(t, err, "control: an administrator is granted the token request by its role, for any MicroVM")
}
