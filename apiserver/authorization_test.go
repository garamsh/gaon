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

	// The controllers create a Machine.
	_, err = stranger.PutMachine(ctx(t), machine("m1"))
	assert.ErrorIs(t, err, api.ErrForbidden)
	m, err := ctrl.PutMachine(ctx(t), machine("m1"))
	require.NoError(t, err)

	// The scheduler lists Machines and binds one.
	_, err = stranger.ListMachines(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = sched.ListMachines(ctx(t))
	require.NoError(t, err)
	binding := api.Binding{ResourceVersion: m.Metadata.ResourceVersion, NodeName: "node-1", BindingEpoch: 1}
	_, err = ctrl.BindMachine(ctx(t), "m1", binding)
	assert.ErrorIs(t, err, api.ErrForbidden, "the controllers' group on the scheduler's path")
	m, err = sched.BindMachine(ctx(t), "m1", binding)
	require.NoError(t, err)

	// The Node Agent reports the status of the Machine bound to it.
	running := *m
	running.Status = api.MachineStatus{Phase: api.MachineRunning, BindingEpoch: 1}
	_, err = stranger.PutMachineStatus(ctx(t), &running)
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = agent.PutMachineStatus(ctx(t), &running)
	require.NoError(t, err)

	// The controllers write the status of a Node.
	n.Status.State = api.NodeStateDiscovered
	_, err = sched.PutNodeStatus(ctx(t), n)
	assert.ErrorIs(t, err, api.ErrForbidden, "the scheduler does not write nodes/status")
	_, err = ctrl.PutNodeStatus(ctx(t), n)
	require.NoError(t, err)

	// An administrator writes roles and deletes a Machine.
	_, err = ctrlRBAC.PutClusterRole(ctx(t), clusterRole("viewer", rule(api.Group, "machines", api.VerbGet)))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = strangerRBAC.PutClusterRole(ctx(t), clusterRole("viewer", rule(api.Group, "machines", api.VerbGet)))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("viewer", rule(api.Group, "machines", api.VerbGet)))
	require.NoError(t, err)
	current, err := admin.GetMachine(ctx(t), "m1")
	require.NoError(t, err)
	_, err = ctrl.DeleteMachine(ctx(t), "m1", current.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden, "the controllers hold no delete")
	_, err = admin.DeleteMachine(ctx(t), "m1", current.Metadata.ResourceVersion)
	require.NoError(t, err)
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
	_, err = denied.PutMachine(ctx(t), machine("m1"))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = allowed.PutMachine(ctx(t), machine("m1"))
	assert.NoError(t, err, "control: the same request in AlwaysAllow")
}

func TestRBAC_TheDefaultModeIsAlwaysAllow(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, "")
	stranger, strangerRBAC := c.clients(t, addr, strangerID)

	_, err := stranger.PutMachine(ctx(t), machine("m1"))
	assert.NoError(t, err)
	_, err = strangerRBAC.PutClusterRole(ctx(t), clusterRole("anything", rule("*", "*", api.VerbAll)))
	assert.NoError(t, err, "in AlwaysAllow a role decides nothing, so any caller may write one")
}

func TestRBAC_RolesAndBindingsDecideNothingInAlwaysAllow(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeAlwaysAllow)
	stranger, strangerRBAC := c.clients(t, addr, strangerID)
	_, err := strangerRBAC.PutClusterRole(ctx(t), clusterRole("nothing", rule(api.Group, "machines", api.VerbGet)))
	require.NoError(t, err)
	_, err = strangerRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("nothing", "nothing", userSubject("stranger")))
	require.NoError(t, err)

	_, err = stranger.PutMachine(ctx(t), machine("m1"))

	assert.NoError(t, err, "a role that grants get does not stop a create")
}

func TestRBAC_ARoleEditTakesEffectOnTheNextRequest(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	_, adminRBAC := c.clients(t, addr, adminID)
	reader, _ := c.clients(t, addr, who{"reader", nil})
	role, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("lister", rule(api.Group, "machines", api.VerbList)))
	require.NoError(t, err)

	_, err = reader.ListMachines(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "no binding yet")
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("lister", "lister", userSubject("reader")))
	require.NoError(t, err)
	_, err = reader.ListMachines(ctx(t))
	assert.NoError(t, err, "control: bound to a role that lists")

	role.Rules = []api.PolicyRule{rule(api.Group, "machines", api.VerbGet)}
	_, err = adminRBAC.PutClusterRole(ctx(t), role)
	require.NoError(t, err)
	_, err = reader.ListMachines(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "the role no longer lists")
}

func TestRBAC_AWildcardRuleMatchesEveryValueAndOnlyAsTheWholeName(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	_, adminRBAC := c.clients(t, addr, adminID)
	holder, _ := c.clients(t, addr, who{"holder", nil})
	partial, _ := c.clients(t, addr, who{"partial", nil})
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("star", rule("*", "machines", api.VerbAll)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("star", "star", userSubject("holder")))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("prefix", rule(api.Group, "mach*", api.VerbAll)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("prefix", "prefix", userSubject("partial")))
	require.NoError(t, err)

	_, err = partial.ListMachines(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "mach* is a plain name, not a pattern")
	_, err = holder.ListMachines(ctx(t))
	assert.NoError(t, err, "control: * as the whole name")

	_, err = holder.ListNodes(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "the rule names machines, not nodes")
	_, err = holder.PutMachine(ctx(t), machine("m1"))
	assert.NoError(t, err, "control: * covers the group and the verb")
	m, err := holder.GetMachine(ctx(t), "m1")
	require.NoError(t, err)
	_, err = holder.DeleteMachine(ctx(t), "m1", m.Metadata.ResourceVersion)
	assert.NoError(t, err, "control: * covers delete")
}

func TestRBAC_ASubresourceNeedsItsOwnRule(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	admin, adminRBAC := c.clients(t, addr, adminID)
	holder, _ := c.clients(t, addr, who{"holder", nil})
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("machines-only", rule(api.Group, "machines", api.VerbUpdate)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("machines-only", "machines-only", userSubject("holder")))
	require.NoError(t, err)
	m, err := admin.PutMachine(ctx(t), machine("m1"))
	require.NoError(t, err)

	status := *m
	status.Status = api.MachineStatus{Phase: api.MachineRunning}
	_, err = holder.PutMachineStatus(ctx(t), &status)
	assert.ErrorIs(t, err, api.ErrForbidden, "update on machines is not update on machines/status")
	_, err = holder.PutMachine(ctx(t), m)
	assert.NoError(t, err, "control: update on machines")
}

func TestRBAC_ThePutVerbIsCreateWithoutAVersionAndUpdateWithOne(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	admin, adminRBAC := c.clients(t, addr, adminID)
	creator, _ := c.clients(t, addr, who{"creator", nil})
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("create-only", rule(api.Group, "machines", api.VerbCreate)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("create-only", "create-only", userSubject("creator")))
	require.NoError(t, err)
	m, err := admin.PutMachine(ctx(t), machine("existing"))
	require.NoError(t, err)

	_, err = creator.PutMachine(ctx(t), m)
	assert.ErrorIs(t, err, api.ErrForbidden, "a body with a version is an update")
	_, err = creator.PutMachine(ctx(t), machine("fresh"))
	assert.NoError(t, err, "control: a body without a version is a create")
}

func TestRBAC_AListAndAWatchAreDistinctVerbs(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	_, adminRBAC := c.clients(t, addr, adminID)
	lister, _ := c.clients(t, addr, who{"lister", nil})
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("list-only", rule(api.Group, "machines", api.VerbList)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("list-only", "list-only", userSubject("lister")))
	require.NoError(t, err)

	_, err = lister.WatchMachines(ctx(t), "1")
	assert.ErrorIs(t, err, api.ErrForbidden, "list does not allow watch")
	_, err = lister.ListMachines(ctx(t))
	assert.NoError(t, err, "control: list")
}

func TestRBAC_AGroupBindingGivesItsRoleToEveryCertificateWithTheOrganization(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	_, adminRBAC := c.clients(t, addr, adminID)
	member, _ := c.clients(t, addr, who{"member", []string{"team", "other"}})
	outsider, _ := c.clients(t, addr, who{"member", []string{"another"}})
	_, err := adminRBAC.PutClusterRole(ctx(t), clusterRole("lister", rule(api.Group, "machines", api.VerbList)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("team-lists", "lister", groupSubject("team")))
	require.NoError(t, err)

	_, err = outsider.ListMachines(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "the same CN without the Organization")
	_, err = member.ListMachines(ctx(t))
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
	_, err = holder.ListMachines(ctx(t))
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
