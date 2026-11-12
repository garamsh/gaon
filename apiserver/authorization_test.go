package apiserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

func TestRBAC_EachComponentReachesItsPathsAndAnotherGroupIsRefused(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	admin, adminRBAC := c.clients(t, addr, adminID)
	sched, _ := c.clients(t, addr, schedulerID)
	ctrl, ctrlRBAC := c.clients(t, addr, controllersID)
	agent, _ := c.clients(t, addr, nodeID("node-1"))
	stranger, strangerRBAC := c.clients(t, addr, strangerID)

	// A Node Agent creates its Node and its Lease.
	_, err := stranger.PutNode(ctx(t), node("node-1"))
	assert.ErrorIs(t, err, api.ErrForbidden)
	n, err := agent.PutNode(ctx(t), node("node-1"))
	require.NoError(t, err)
	_, err = stranger.PutLease(ctx(t), lease("node-1"))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = agent.PutLease(ctx(t), lease("node-1"))
	require.NoError(t, err)

	// The controllers create a MicroVM.
	_, err = stranger.PutMicroVM(ctx(t), microvm("m1"))
	assert.ErrorIs(t, err, api.ErrForbidden)
	m, err := ctrl.PutMicroVM(ctx(t), microvm("m1"))
	require.NoError(t, err)

	// The scheduler lists MicroVMs and binds one.
	_, err = stranger.ListMicroVMs(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = sched.ListMicroVMs(ctx(t))
	require.NoError(t, err)
	binding := api.Binding{ResourceVersion: m.Metadata.ResourceVersion, NodeName: "node-1", BindingEpoch: 1}
	_, err = ctrl.BindMicroVM(ctx(t), "m1", binding)
	assert.ErrorIs(t, err, api.ErrForbidden, "the controllers' group on the scheduler's path")
	m, err = sched.BindMicroVM(ctx(t), "m1", binding)
	require.NoError(t, err)

	// The Node Agent reports the status of the MicroVM bound to it.
	running := *m
	running.Status = api.MicroVMStatus{Phase: api.MicroVMRunning, BindingEpoch: 1}
	_, err = stranger.PutMicroVMStatus(ctx(t), &running)
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = agent.PutMicroVMStatus(ctx(t), &running)
	require.NoError(t, err)

	// The controllers write the status of a Node.
	n.Status.State = api.NodeStateDiscovered
	_, err = sched.PutNodeStatus(ctx(t), n)
	assert.ErrorIs(t, err, api.ErrForbidden, "the scheduler does not write nodes/status")
	_, err = ctrl.PutNodeStatus(ctx(t), n)
	require.NoError(t, err)

	// An administrator writes roles, and the controllers delete a MicroVM.
	_, err = ctrlRBAC.PutClusterRole(ctx(t), clusterRole("viewer", rule(api.Group, "microvms", api.VerbGet)))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = strangerRBAC.PutClusterRole(ctx(t), clusterRole("viewer", rule(api.Group, "microvms", api.VerbGet)))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("viewer", rule(api.Group, "microvms", api.VerbGet)))
	require.NoError(t, err)
	current, err := admin.GetMicroVM(ctx(t), "m1")
	require.NoError(t, err)
	_, err = sched.DeleteMicroVM(ctx(t), "m1", current.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden, "the scheduler holds no delete")
	_, err = ctrl.DeleteMicroVM(ctx(t), "m1", current.Metadata.ResourceVersion)
	require.NoError(t, err, "control: the controllers hold delete on every resource (ADR 0021)")
}

func TestRBAC_ACallerWithNoBindingIsForbiddenEverywhereAndAlwaysAllowServesIt(t *testing.T) {
	c := newCluster(t)
	rbacAddr, _ := c.start(t, apiserver.ModeRBAC)
	openAddr, _ := c.start(t, apiserver.ModeAlwaysAllow)
	denied, _ := c.clients(t, rbacAddr, strangerID)
	allowed, _ := c.clients(t, openAddr, strangerID)

	_, err := denied.ListNodes(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = allowed.ListNodes(ctx(t))
	assert.NoError(t, err, "control: the same request in AlwaysAllow")
	_, err = denied.PutMicroVM(ctx(t), microvm("m1"))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = allowed.PutMicroVM(ctx(t), microvm("m1"))
	assert.NoError(t, err, "control: the same request in AlwaysAllow")
}

func TestRBAC_TheDefaultModeIsAlwaysAllow(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, "")
	stranger, strangerRBAC := c.clients(t, addr, strangerID)

	_, err := stranger.PutMicroVM(ctx(t), microvm("m1"))
	assert.NoError(t, err)
	_, err = strangerRBAC.PutClusterRole(ctx(t), clusterRole("anything", rule("*", "*", api.VerbAll)))
	assert.NoError(t, err, "in AlwaysAllow a role decides nothing, so any caller may write one")
}

func TestRBAC_RolesAndBindingsDecideNothingInAlwaysAllow(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeAlwaysAllow)
	stranger, strangerRBAC := c.clients(t, addr, strangerID)
	_, err := strangerRBAC.PutClusterRole(ctx(t), clusterRole("nothing", rule(api.Group, "microvms", api.VerbGet)))
	require.NoError(t, err)
	_, err = strangerRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("nothing", "nothing", userSubject("stranger")))
	require.NoError(t, err)

	_, err = stranger.PutMicroVM(ctx(t), microvm("m1"))

	assert.NoError(t, err, "a role that grants get does not stop a create")
}

func TestRBAC_ARoleEditTakesEffectOnTheNextRequest(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	_, adminRBAC := c.clients(t, addr, adminID)
	reader, _ := c.clients(t, addr, who{"reader", nil})
	role, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("lister", rule(api.Group, "microvms", api.VerbList)))
	require.NoError(t, err)

	_, err = reader.ListMicroVMs(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "no binding yet")
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("lister", "lister", userSubject("reader")))
	require.NoError(t, err)
	_, err = reader.ListMicroVMs(ctx(t))
	assert.NoError(t, err, "control: bound to a role that lists")

	role.Rules = []api.PolicyRule{rule(api.Group, "microvms", api.VerbGet)}
	_, err = adminRBAC.PutClusterRole(ctx(t), role)
	require.NoError(t, err)
	_, err = reader.ListMicroVMs(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "the role no longer lists")
}

func TestRBAC_AWildcardRuleMatchesEveryValueAndOnlyAsTheWholeName(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	_, adminRBAC := c.clients(t, addr, adminID)
	holder, _ := c.clients(t, addr, who{"holder", nil})
	partial, _ := c.clients(t, addr, who{"partial", nil})
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("star", rule("*", "microvms", api.VerbAll)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("star", "star", userSubject("holder")))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("prefix", rule(api.Group, "mach*", api.VerbAll)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("prefix", "prefix", userSubject("partial")))
	require.NoError(t, err)

	_, err = partial.ListMicroVMs(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "mach* is a plain name, not a pattern")
	_, err = holder.ListMicroVMs(ctx(t))
	assert.NoError(t, err, "control: * as the whole name")

	_, err = holder.ListNodes(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "the rule names microvms, not nodes")
	_, err = holder.PutMicroVM(ctx(t), microvm("m1"))
	assert.NoError(t, err, "control: * covers the group and the verb")
	m, err := holder.GetMicroVM(ctx(t), "m1")
	require.NoError(t, err)
	_, err = holder.DeleteMicroVM(ctx(t), "m1", m.Metadata.ResourceVersion)
	assert.NoError(t, err, "control: * covers delete")
}

func TestRBAC_ASubresourceNeedsItsOwnRule(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	admin, adminRBAC := c.clients(t, addr, adminID)
	holder, _ := c.clients(t, addr, who{"holder", nil})
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("microvms-only", rule(api.Group, "microvms", api.VerbUpdate)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("microvms-only", "microvms-only", userSubject("holder")))
	require.NoError(t, err)
	m, err := admin.PutMicroVM(ctx(t), microvm("m1"))
	require.NoError(t, err)

	status := *m
	status.Status = api.MicroVMStatus{Phase: api.MicroVMRunning}
	_, err = holder.PutMicroVMStatus(ctx(t), &status)
	assert.ErrorIs(t, err, api.ErrForbidden, "update on microvms is not update on microvms/status")
	_, err = holder.PutMicroVM(ctx(t), m)
	assert.NoError(t, err, "control: update on microvms")
}

func TestRBAC_ThePutVerbIsCreateWithoutAVersionAndUpdateWithOne(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	admin, adminRBAC := c.clients(t, addr, adminID)
	creator, _ := c.clients(t, addr, who{"creator", nil})
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("create-only", rule(api.Group, "microvms", api.VerbCreate)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("create-only", "create-only", userSubject("creator")))
	require.NoError(t, err)
	m, err := admin.PutMicroVM(ctx(t), microvm("existing"))
	require.NoError(t, err)

	_, err = creator.PutMicroVM(ctx(t), m)
	assert.ErrorIs(t, err, api.ErrForbidden, "a body with a version is an update")
	_, err = creator.PutMicroVM(ctx(t), microvm("fresh"))
	assert.NoError(t, err, "control: a body without a version is a create")
}

func TestRBAC_AListAndAWatchAreDistinctVerbs(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	_, adminRBAC := c.clients(t, addr, adminID)
	lister, _ := c.clients(t, addr, who{"lister", nil})
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("list-only", rule(api.Group, "microvms", api.VerbList)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("list-only", "list-only", userSubject("lister")))
	require.NoError(t, err)

	_, err = lister.WatchMicroVMs(ctx(t), "1")
	assert.ErrorIs(t, err, api.ErrForbidden, "list does not allow watch")
	_, err = lister.ListMicroVMs(ctx(t))
	assert.NoError(t, err, "control: list")
}

func TestRBAC_AGroupBindingGivesItsRoleToEveryCertificateWithTheOrganization(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	_, adminRBAC := c.clients(t, addr, adminID)
	member, _ := c.clients(t, addr, who{"member", []string{"team", "other"}})
	outsider, _ := c.clients(t, addr, who{"member", []string{"another"}})
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("lister", rule(api.Group, "microvms", api.VerbList)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("team-lists", "lister", groupSubject("team")))
	require.NoError(t, err)

	_, err = outsider.ListMicroVMs(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "the same CN without the Organization")
	_, err = member.ListMicroVMs(ctx(t))
	assert.NoError(t, err, "control: one of its Organizations is the bound group")
}

func TestRBAC_ABearerTokenIsNeitherWidenedNorNarrowedByRoles(t *testing.T) {
	c := newCluster(t)
	c.cfg.BootstrapTokenFile = writeFile(t, "tokens", []byte(tokenA+"\n"))
	addr, _ := c.start(t, apiserver.ModeRBAC)
	_, adminRBAC := c.clients(t, addr, adminID)
	hc, err := api.NewServerTLSClient(c.caFile)
	require.NoError(t, err)
	holder := api.NewClient("https://"+addr+apiRoot, hc, api.WithBearerToken(tokenA))

	_, err = holder.PutCertificateSigningRequest(ctx(t), csr(t, "node-1"))
	assert.NoError(t, err, "a token mints a request with no binding at all")
	_, err = holder.GetCertificateSigningRequest(ctx(t), "node-1")
	assert.NoError(t, err, "and reads the one it created")

	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("token-admin", "gaon:admin", userSubject("bootstrap:abcdef")))
	require.NoError(t, err)
	_, err = holder.ListMicroVMs(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "a binding to gaon:admin does not widen a token")
	_, err = holder.ListCertificateSigningRequests(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden)
}

func TestParseConfig_AuthorizationMode(t *testing.T) {
	tlsFlags := []string{"--tls-client-ca=ca.pem", "--tls-cert=cert.pem", "--tls-key=key.pem"}
	for name, tc := range map[string]struct {
		args    []string
		want    string
		wantErr string
	}{
		"default":                 {nil, apiserver.ModeAlwaysAllow, ""},
		"AlwaysAllow":             {[]string{"--authorization-mode=AlwaysAllow"}, apiserver.ModeAlwaysAllow, ""},
		"RBAC with the TLS flags": {append([]string{"--authorization-mode=RBAC"}, tlsFlags...), apiserver.ModeRBAC, ""},
		"RBAC without TLS":        {[]string{"--authorization-mode=RBAC"}, "", "needs tls-client-ca"},
		"an unknown mode":         {[]string{"--authorization-mode=Webhook"}, "", `"Webhook" is not AlwaysAllow or RBAC`},
		"a lowercase mode":        {[]string{"--authorization-mode=rbac"}, "", `"rbac" is not AlwaysAllow or RBAC`},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := apiserver.ParseConfig(tc.args)

			if tc.wantErr != "" {
				assert.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.AuthorizationMode)
		})
	}
}

func TestRun_RefusesRBACWithoutTheTLSFiles(t *testing.T) {
	c := newCluster(t)
	cfg := c.cfg
	cfg.TLSClientCA, cfg.TLSCert, cfg.TLSKey = "", "", ""
	cfg.AuthorizationMode = apiserver.ModeRBAC
	ln := listenLoopback(t)

	err := apiserver.Run(ctx(t), cfg, ln, nil)

	assert.ErrorContains(t, err, "needs tls-client-ca")
}

func TestRun_RefusesAnUnknownAuthorizationMode(t *testing.T) {
	c := newCluster(t)
	cfg := c.cfg
	cfg.AuthorizationMode = "Webhook"
	ln := listenLoopback(t)

	err := apiserver.Run(ctx(t), cfg, ln, nil)

	assert.ErrorContains(t, err, `"Webhook" is not AlwaysAllow or RBAC`)
}
