package apiserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// runnerFixture is an RBAC server with a token signing key where the service
// account runner is bound to a ClusterRole that grants create, get, list,
// watch and delete on secrets only for Secrets labeled runner=r1. The
// Secrets mine (runner=r1), foreign (runner=r2) and bare (no labels) are
// stored, and token is the bearer token of runner.
type runnerFixture struct {
	admin   *api.Client
	addr    string
	token   string
	caFile  string
	mine    *api.Secret
	foreign *api.Secret
	bare    *api.Secret
}

func newRunnerFixture(t *testing.T) *runnerFixture {
	t.Helper()
	c := newCluster(t)
	key := newSigningKey(t)
	c.cfg.ServiceAccountIssuer, c.cfg.ServiceAccountSigningKeyFile = key.issuer, key.file
	addr, _ := c.start(t, apiserver.ModeRBAC)
	f := &runnerFixture{addr: addr, caFile: c.caFile}
	var adminRBAC *api.Client
	f.admin, adminRBAC = c.clients(t, addr, adminID)

	_, err := f.admin.PutServiceAccount(ctx(t), serviceAccount("runner"))
	require.NoError(t, err)
	m := microvm("m1")
	m.Spec.ServiceAccountName = "runner"
	m, err = f.admin.PutMicroVM(ctx(t), m)
	require.NoError(t, err)
	issued, err := f.admin.CreateServiceAccountToken(ctx(t), "runner", tokenRequest(m, 0, key.issuer))
	require.NoError(t, err)
	f.token = issued.Status.Token

	role := rule(api.Group, "secrets", api.VerbCreate, api.VerbGet, api.VerbList, api.VerbWatch, api.VerbDelete)
	role.LabelSelector = "runner=r1"
	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("runner-secrets", role))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("runner-secrets", "runner-secrets", userSubject(api.ServiceAccountSubject("runner"))))
	require.NoError(t, err)

	f.mine, err = f.admin.PutSecret(ctx(t), labeledSecret(secret("mine", nil), "runner", "r1"))
	require.NoError(t, err)
	f.foreign, err = f.admin.PutSecret(ctx(t), labeledSecret(secret("foreign", nil), "runner", "r2"))
	require.NoError(t, err)
	f.bare, err = f.admin.PutSecret(ctx(t), secret("bare", nil))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"runner": "r1"}, f.mine.Metadata.Labels, "setup: mine is stored with its label")
	require.Equal(t, map[string]string{"runner": "r2"}, f.foreign.Metadata.Labels, "setup: foreign is stored with its label")
	require.Empty(t, f.bare.Metadata.Labels, "setup: bare is stored without labels")
	return f
}

// runner is the client of the service account token holder, whose lists and
// watches ask for text; the empty text asks for no selector.
func (f *runnerFixture) runner(t *testing.T, text string) *api.Client {
	t.Helper()
	hc, err := api.NewServerTLSClient(f.caFile)
	require.NoError(t, err)
	opts := []api.ClientOption{api.WithBearerToken(f.token)}
	if text != "" {
		opts = append(opts, api.WithLabelSelector(mustSelector(t, text)))
	}
	return api.NewClient("https://"+f.addr+apiRoot, hc, opts...)
}

func labeledSecret(s *api.Secret, kv ...string) *api.Secret {
	s.Metadata.Labels = map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		s.Metadata.Labels[kv[i]] = kv[i+1]
	}
	return s
}

func TestSecretSelectorRule_CreateIsJudgedOnTheLabelsOfTheBody(t *testing.T) {
	f := newRunnerFixture(t)
	runner := f.runner(t, "")

	_, err := runner.PutSecret(ctx(t), secret("new-bare", nil))
	assert.ErrorIs(t, err, api.ErrForbidden, "no labels")
	_, err = runner.PutSecret(ctx(t), labeledSecret(secret("new-foreign", nil), "runner", "r2"))
	assert.ErrorIs(t, err, api.ErrForbidden, "labels outside the selector")
	_, err = runner.PutSecret(ctx(t), labeledSecret(secret("new-mine", nil), "runner", "r1"))
	assert.NoError(t, err, "control: labels inside it")
}

func TestSecretSelectorRule_GetIsJudgedOnTheStoredLabels(t *testing.T) {
	f := newRunnerFixture(t)
	runner := f.runner(t, "")

	_, err := runner.GetSecret(ctx(t), "bare")
	assert.ErrorIs(t, err, api.ErrForbidden, "a Secret with no labels")
	_, err = runner.GetSecret(ctx(t), "foreign")
	assert.ErrorIs(t, err, api.ErrForbidden, "a Secret with labels outside the selector")
	_, err = runner.GetSecret(ctx(t), "missing")
	assert.ErrorIs(t, err, api.ErrForbidden, "a missing Secret counts as having no labels")
	_, err = runner.GetSecret(ctx(t), "mine")
	assert.NoError(t, err, "control: a Secret inside it")
}

func TestSecretSelectorRule_DeleteIsJudgedOnTheStoredLabels(t *testing.T) {
	f := newRunnerFixture(t)
	runner := f.runner(t, "")

	_, err := runner.DeleteSecret(ctx(t), "bare", f.bare.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden, "a Secret with no labels")
	_, err = runner.DeleteSecret(ctx(t), "foreign", f.foreign.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden, "a Secret with labels outside the selector")
	_, err = runner.DeleteSecret(ctx(t), "mine", f.mine.Metadata.ResourceVersion)
	assert.NoError(t, err, "control: a Secret inside it")
}

func TestSecretSelectorRule_ListAndWatchNeedASelectorWithinTheRules(t *testing.T) {
	f := newRunnerFixture(t)
	list, err := f.admin.ListSecrets(ctx(t))
	require.NoError(t, err)

	_, err = f.runner(t, "").ListSecrets(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "list without a selector asks for more than the rule allows")
	_, err = f.runner(t, "runner in (r1,r2)").ListSecrets(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "list with a wider selector")
	got, err := f.runner(t, "runner=r1").ListSecrets(ctx(t))
	require.NoError(t, err, "control: list with the rule's selector")
	require.Len(t, got.Items, 1)
	assert.Equal(t, "mine", got.Items[0].Metadata.Name)
	_, err = f.runner(t, "runner=r1,tier=a").ListSecrets(ctx(t))
	assert.NoError(t, err, "control: list with a narrower selector")

	_, err = f.runner(t, "").WatchSecrets(ctx(t), list.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden, "watch without a selector")
	_, err = f.runner(t, "runner in (r1,r2)").WatchSecrets(ctx(t), list.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden, "watch with a wider selector")
	w, err := f.runner(t, "runner=r1").WatchSecrets(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err, "control: watch with the rule's selector")
	w.Close()
}
