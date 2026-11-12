//go:build extensions

// Package extensions runs the built gaon services against an embedded etcd
// with authorization on and shows the extension points work: custom
// resources, an external controller under RBAC, an admission webhook and a
// scheduler extender. Nothing here needs KVM; `make e2e-extensions` sets the
// environment the test reads.
package extensions

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

const (
	// bootstrapToken is the run's one token, in the form the Node Agent and
	// the API server require.
	bootstrapToken = "gaonxx.0123456789abcdef"
	// The Nodes: bigNode has more capacity, so LeastAllocated prefers it, and
	// its name sorts last, so the default order does not.
	smallNode, bigNode = "node-a", "node-b"
	// The extender keeps the Machines with this name prefix off bigNode.
	filteredPrefix = "filtered-"
	// machineImage is a Machine's image; the fake mvmd pulls nothing.
	machineImage = "gaon-93/image@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	crdGroup   = "apps.gaon.garam.sh"
	crdVersion = "v1alpha1"
	crdPlural  = "widgets"

	blockedField   = "blocked"
	webhookMessage = "a Widget may not set spec.blocked"
	webhookName    = "block.widgets.test.gaon.garam.sh"

	pollEvery = 100 * time.Millisecond
	waitFor   = 90 * time.Second
)

var widgetType = api.CustomResourceType{Group: crdGroup, Version: crdVersion, Kind: "Widget", Plural: crdPlural}

// cluster is the running system and the clients of the test.
type cluster struct {
	ca     *authority
	server string // https://host:port
	root   string // the compute API root
	// admin is the administrator's client of the compute group; rbac, defs and
	// hooks are its clients of the other groups.
	admin, rbac, defs, hooks *api.Client
	adminCR                  *api.CustomResourceClient
	// agentServices are the processes whose logs must show no refusal.
	services []*service
	extender *extender
	hook     identity
}

func TestExtensions(t *testing.T) {
	binDir := os.Getenv("E2E_EXT_SERVICES")
	if binDir == "" {
		t.Fatal("E2E_EXT_SERVICES is required; make e2e-extensions sets it")
	}
	c := startCluster(t, binDir)

	t.Run("agents_register_and_machines_run_under_rbac", c.agentsRegister)
	t.Run("custom_resource_is_served", c.customResourceServed)
	t.Run("external_controller_is_bound_by_rbac", c.externalController)
	t.Run("webhook_refuses_a_marked_object", c.webhookRefuses)
	t.Run("extender_filters_out_the_preferred_node", c.extenderFilters)
	t.Run("no_service_logged_forbidden", c.noForbidden)
}

// startCluster starts etcd, gaon-apiserver with RBAC and mutual TLS, the
// controllers, the scheduler configured with LeastAllocated and an extender,
// and two Node Agents over fake mvmds.
func startCluster(t *testing.T, binDir string) *cluster {
	t.Helper()
	dir, err := os.MkdirTemp("", runTag+"-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	ca := newAuthority(t, dir)
	serverID := ca.server(t, "apiserver")
	adminID := ca.client(t, "test-admin", "gaon:admins")
	controllersID := ca.client(t, "controllers", "gaon:controllers")
	schedulerID := ca.client(t, "scheduler", "gaon:schedulers")

	tokenFile := writeFile(t, filepath.Join(dir, "bootstrap-token"), []byte(bootstrapToken+"\n"))
	apiserver := startService(t, dir, binDir, "apiserver", "gaon-apiserver",
		"--listen", "127.0.0.1:0", "--etcd-endpoints", startEtcd(t, dir),
		"--tls-client-ca", ca.CertFile, "--tls-cert", serverID.CertFile, "--tls-key", serverID.KeyFile,
		"--bootstrap-token-file", tokenFile, "--authorization-mode", "RBAC")
	server := "https://" + apiserver.serverAddress(t)
	c := &cluster{ca: ca, server: server, root: server + "/apis/" + api.GroupVersion, hook: ca.server(t, "webhook")}
	c.admin = c.client(t, adminID, api.GroupVersion)
	c.rbac = c.client(t, adminID, api.RBACGroupVersion)
	c.defs = c.client(t, adminID, api.APIExtensionsGroupVersion)
	c.hooks = c.client(t, adminID, api.AdmissionRegistrationGroupVersion)
	c.adminCR = c.customClient(t, adminID)
	c.extender = startExtender(t, filteredPrefix, bigNode)
	require.Eventually(t, func() bool { return c.listsClusterRoles() }, 30*time.Second, pollEvery, "the API server bootstraps its roles")

	tlsFlags := func(id identity) []string {
		return []string{"-tls-ca", ca.CertFile, "-tls-cert", id.CertFile, "-tls-key", id.KeyFile}
	}
	c.services = append(c.services,
		startService(t, dir, binDir, "controllers", "gaon-controllers", append([]string{"-api", c.root, "-signing-cert", ca.CertFile, "-signing-key", ca.KeyFile}, tlsFlags(controllersID)...)...),
		startService(t, dir, binDir, "scheduler", "gaon-scheduler", append([]string{"-api", c.root, "-interval", "1s", "-config", c.schedulerConfig(t, dir)}, tlsFlags(schedulerID)...)...),
	)
	// A socket path is limited to about 100 bytes, so the sockets sit in dir,
	// which is short.
	for _, n := range []struct {
		name   string
		cpus   uint32
		memory uint64
	}{{smallNode, 4, 8 << 30}, {bigNode, 8, 16 << 30}} {
		// The Node is admitted before its agent starts: the agent's first calls
		// then meet an admitted Node, and no refusal is logged for the wait.
		ctx, cancel := within(30 * time.Second)
		_, err := c.admin.PutNode(ctx, &api.Node{
			TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNode},
			Metadata: api.ObjectMeta{Name: n.name},
			Spec:     api.NodeSpec{Admitted: true},
		})
		cancel()
		require.NoError(t, err, "admit "+n.name)
		m := startMvmd(t, dir, n.name, n.cpus, n.memory)
		certs := filepath.Join(dir, n.name+"-certs")
		require.NoError(t, os.Mkdir(certs, 0o700))
		c.services = append(c.services, startService(t, dir, binDir, n.name+"-agent", "gaon-node-agent",
			"--api-server", c.root, "--node-name", n.name, "--mvmd-socket", m.socket,
			"--bootstrap-token-file", tokenFile, "--cert-dir", certs, "--tls-ca", ca.CertFile,
			"--resync-period", "1s"))
	}
	return c
}

func (c *cluster) listsClusterRoles() bool {
	ctx, cancel := within(5 * time.Second)
	defer cancel()
	l, err := c.rbac.ListClusterRoles(ctx)
	return err == nil && len(l.Items) > 0
}

// schedulerConfig writes the scheduler's configuration: LeastAllocated and the
// extender.
func (c *cluster) schedulerConfig(t *testing.T, dir string) string {
	t.Helper()
	cfg := fmt.Sprintf(`apiVersion: scheduler.gaon.garam.sh/v1alpha1
kind: SchedulerConfiguration
scorePlugins:
  - name: LeastAllocated
extenders:
  - urlPrefix: %s
    filterVerb: filter
    timeout: 5s
`, c.extender.URL)
	return writeFile(t, filepath.Join(dir, "scheduler.yaml"), []byte(cfg))
}

func (c *cluster) client(t *testing.T, id identity, groupVersion string) *api.Client {
	t.Helper()
	httpClient, err := api.NewTLSClient(c.ca.CertFile, id.CertFile, id.KeyFile)
	require.NoError(t, err)
	return api.NewClient(c.server+"/apis/"+groupVersion, httpClient)
}

func (c *cluster) customClient(t *testing.T, id identity) *api.CustomResourceClient {
	t.Helper()
	httpClient, err := api.NewTLSClient(c.ca.CertFile, id.CertFile, id.KeyFile)
	require.NoError(t, err)
	cr, err := api.NewCustomResourceClient(c.server, httpClient, widgetType)
	require.NoError(t, err)
	return cr
}

// agentsRegister shows the Node Agents get certificates by CertificateSigningRequest,
// register their Nodes and keep Leases, and that a Machine is bound and Running.
func (c *cluster) agentsRegister(t *testing.T) {
	ctx, cancel := within(5 * time.Minute)
	defer cancel()
	for _, n := range []string{smallNode, bigNode} {
		require.Eventually(t, func() bool { return c.eligible(ctx, n) }, waitFor, pollEvery, n+" is Admitted and Ready")
	}

	// The agents' certificates are what the signer issued for their requests.
	requests, err := c.admin.ListCertificateSigningRequests(ctx)
	require.NoError(t, err)
	signed := map[string]bool{}
	for _, r := range requests.Items {
		if cert := parseCertificate(t, r.Status.Certificate); cert != nil {
			signed[cert.Subject.CommonName] = true
		}
	}
	assert.True(t, signed[smallNode] && signed[bigNode], "the signer issued a certificate to each agent, got %v", signed)

	// Each agent keeps its Lease: the version moves between two reads.
	for _, n := range []string{smallNode, bigNode} {
		var first *api.Lease
		require.Eventually(t, func() bool {
			l, err := c.admin.GetLease(ctx, n)
			first = l
			return err == nil
		}, waitFor, pollEvery, n+" has a Lease")
		assert.Equal(t, n, first.Spec.Holder)
		require.Eventually(t, func() bool {
			l, err := c.admin.GetLease(ctx, n)
			return err == nil && l.Metadata.ResourceVersion != first.Metadata.ResourceVersion
		}, waitFor, pollEvery, n+" renews its Lease")
	}

	m := c.machine("plain-1", "1", "1Gi")
	_, err = c.admin.PutMachine(ctx, m)
	require.NoError(t, err)
	bound := c.waitRunning(t, ctx, "plain-1")
	assert.Contains(t, []string{smallNode, bigNode}, bound.Spec.NodeName)
	assert.Equal(t, api.Epoch(1), bound.Spec.BindingEpoch)
}

func (c *cluster) eligible(ctx context.Context, name string) bool {
	n, err := c.admin.GetNode(ctx, name)
	if err != nil || n.Status.State != api.NodeStateAdmitted {
		return false
	}
	for _, cond := range n.Status.Conditions {
		if cond.Type == api.NodeReady && cond.Status == api.ConditionTrue {
			return true
		}
	}
	return false
}

func parseCertificate(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert
}

func (c *cluster) machine(name, cpu, memory string) *api.Machine {
	return &api.Machine{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMachine},
		Metadata: api.ObjectMeta{Name: name},
		Spec: api.MachineSpec{
			Image:        machineImage,
			RunStrategy:  api.RunAlways,
			RootDiskSize: "256Mi",
			Resources:    api.ResourceRequirements{Requests: api.ResourceList{CPU: api.Quantity(cpu), Memory: api.Quantity(memory)}},
		},
	}
}

// waitRunning waits for the Machine to be bound and reported Running at its
// binding epoch, and returns it.
func (c *cluster) waitRunning(t *testing.T, ctx context.Context, name string) *api.Machine {
	t.Helper()
	var got *api.Machine
	require.Eventually(t, func() bool {
		m, err := c.admin.GetMachine(ctx, name)
		if err != nil {
			return false
		}
		got = m
		return m.Spec.NodeName != "" && m.EffectivePhase() == api.MachineRunning
	}, waitFor, pollEvery, "Machine "+name+" is bound and Running")
	return got
}

// widget is a Widget with the given spec JSON.
func widget(name, spec string) *api.CustomObject {
	return &api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: crdGroup + "/" + crdVersion, Kind: widgetType.Kind},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     json.RawMessage(spec),
	}
}

func widgetDefinition() *api.CustomResourceDefinition {
	prop := func(t api.SchemaType) api.JSONSchemaProps { return api.JSONSchemaProps{Type: t} }
	object := func(props map[string]api.JSONSchemaProps) api.JSONSchemaProps {
		return api.JSONSchemaProps{Type: api.SchemaObject, Properties: props}
	}
	return &api.CustomResourceDefinition{
		TypeMeta: api.TypeMeta{APIVersion: api.APIExtensionsGroupVersion, Kind: api.KindCustomResourceDefinition},
		Metadata: api.ObjectMeta{Name: crdPlural + "." + crdGroup},
		Spec: api.CustomResourceDefinitionSpec{
			Group: crdGroup,
			Names: api.CustomResourceNames{Kind: widgetType.Kind, ListKind: "WidgetList", Plural: crdPlural, Singular: "widget"},
			Scope: api.ScopeCluster,
			Versions: []api.CustomResourceVersion{{
				Name: crdVersion, Served: true, Storage: true,
				Schema: api.CustomResourceValidation{OpenAPIV3Schema: &api.JSONSchemaProps{
					Type: api.SchemaObject,
					Properties: map[string]api.JSONSchemaProps{
						"spec":   object(map[string]api.JSONSchemaProps{"size": prop(api.SchemaInteger), blockedField: prop(api.SchemaBoolean)}),
						"status": object(map[string]api.JSONSchemaProps{"observedSize": prop(api.SchemaInteger)}),
					},
				}},
				Subresources: &api.CustomResourceSubresources{Status: &api.StatusSubresource{}},
			}},
		},
	}
}

// customResourceServed shows a definition becomes Established and its objects
// can be created, read, listed and watched.
func (c *cluster) customResourceServed(t *testing.T) {
	ctx, cancel := within(2 * time.Minute)
	defer cancel()
	_, err := c.defs.PutCustomResourceDefinition(ctx, widgetDefinition())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		d, err := c.defs.GetCustomResourceDefinition(ctx, crdPlural+"."+crdGroup)
		if err != nil {
			return false
		}
		for _, cond := range d.Status.Conditions {
			if cond.Type == api.CRDEstablished && cond.Status == api.ConditionTrue {
				return true
			}
		}
		return false
	}, waitFor, pollEvery, "the definition is Established")

	created, err := c.adminCR.Put(ctx, widget("w1", `{"size":3}`))
	require.NoError(t, err)
	got, err := c.adminCR.Get(ctx, "w1")
	require.NoError(t, err)
	assert.JSONEq(t, `{"size":3}`, string(got.Spec))
	assert.Equal(t, created.Metadata.ResourceVersion, got.Metadata.ResourceVersion)

	listed, err := c.adminCR.List(ctx)
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	assert.Equal(t, "w1", listed.Items[0].Metadata.Name)

	w, err := c.adminCR.Watch(ctx, listed.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer func() { _ = w.Close() }()
	_, err = c.adminCR.Put(ctx, widget("w2", `{"size":5}`))
	require.NoError(t, err)
	ev, err := w.Next()
	require.NoError(t, err)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "w2", ev.Object.Metadata.Name)
}

// withStatus is o with the status an external controller writes for it: the
// size its spec asks for.
func withStatus(o *api.CustomObject) *api.CustomObject {
	var spec struct {
		Size int `json:"size"`
	}
	_ = json.Unmarshal(o.Spec, &spec)
	out := *o
	out.Status = json.RawMessage(fmt.Sprintf(`{"observedSize":%d}`, spec.Size))
	return &out
}

// externalController shows a controller with its own certificate, bound to a
// ClusterRole on the group and the plural, watches the kind and writes status,
// and that a certificate with no binding is forbidden the same write.
func (c *cluster) externalController(t *testing.T) {
	ctx, cancel := within(2 * time.Minute)
	defer cancel()
	controllerID := c.ca.client(t, "widget-controller")
	intruderID := c.ca.client(t, "intruder")
	controller, intruder := c.customClient(t, controllerID), c.customClient(t, intruderID)

	w1, err := c.adminCR.Get(ctx, "w1")
	require.NoError(t, err)
	statusWrite := withStatus(w1)

	// The refusal: no binding names the intruder.
	_, err = intruder.PutStatus(ctx, statusWrite)
	require.ErrorIs(t, err, api.ErrForbidden, "a certificate with no binding writes the status")
	unchanged, err := c.adminCR.Get(ctx, "w1")
	require.NoError(t, err)
	assert.Empty(t, string(unchanged.Status), "the refused write changed nothing")
	// Neither is the controller allowed before its binding exists, so the
	// binding below is what changes the answer.
	_, err = controller.PutStatus(ctx, statusWrite)
	require.ErrorIs(t, err, api.ErrForbidden, "the controller writes the status before it is bound")

	_, err = c.rbac.PutClusterRole(ctx, &api.ClusterRole{
		TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRole},
		Metadata: api.ObjectMeta{Name: "widget-controller"},
		Rules: []api.PolicyRule{
			{APIGroups: []string{crdGroup}, Resources: []string{crdPlural}, Verbs: []api.Verb{api.VerbGet, api.VerbList, api.VerbWatch}},
			{APIGroups: []string{crdGroup}, Resources: []string{crdPlural + "/status"}, Verbs: []api.Verb{api.VerbUpdate}},
		},
	})
	require.NoError(t, err)
	_, err = c.rbac.PutClusterRoleBinding(ctx, &api.ClusterRoleBinding{
		TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRoleBinding},
		Metadata: api.ObjectMeta{Name: "widget-controller"},
		RoleRef:  api.RoleRef{Kind: api.KindClusterRole, Name: "widget-controller"},
		Subjects: []api.Subject{{Kind: api.SubjectUser, Name: "widget-controller"}},
	})
	require.NoError(t, err)

	// The control: the controller's identical write succeeds.
	written, err := controller.PutStatus(ctx, statusWrite)
	require.NoError(t, err, "the bound controller writes the same status")
	assert.JSONEq(t, `{"observedSize":3}`, string(written.Status))

	// The controller watches the kind: an object created now gets its status.
	watch, err := controller.Watch(ctx, mustList(t, ctx, controller).Metadata.ResourceVersion)
	require.NoError(t, err, "the bound controller watches the kind")
	defer func() { _ = watch.Close() }()
	_, err = c.adminCR.Put(ctx, widget("w3", `{"size":7}`))
	require.NoError(t, err)
	for {
		ev, err := watch.Next()
		require.NoError(t, err)
		if ev.Object.Metadata.Name != "w3" {
			continue
		}
		o := ev.Object
		_, err = controller.PutStatus(ctx, withStatus(&o))
		require.NoError(t, err)
		break
	}
	w3, err := c.adminCR.Get(ctx, "w3")
	require.NoError(t, err)
	assert.JSONEq(t, `{"observedSize":7}`, string(w3.Status))
}

func mustList(t *testing.T, ctx context.Context, cr *api.CustomResourceClient) *api.CustomObjectList {
	t.Helper()
	l, err := cr.List(ctx)
	require.NoError(t, err)
	return l
}

// webhookRefuses shows a ValidatingWebhookConfiguration served by the test
// refuses an object that carries the marked field, with the webhook's message,
// and accepts the same write without it.
func (c *cluster) webhookRefuses(t *testing.T) {
	ctx, cancel := within(2 * time.Minute)
	defer cancel()
	hook := startWebhook(t, c.hook, blockedField, webhookMessage)
	_, err := c.hooks.PutValidatingWebhookConfiguration(ctx, &api.ValidatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindValidatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: "widgets"},
		Webhooks: []api.Webhook{{
			Name:         webhookName,
			ClientConfig: api.WebhookClientConfig{URL: hook.URL, CABundle: c.ca.CertPEM},
			Rules: []api.WebhookRule{{
				Operations: []api.Operation{api.OperationCreate, api.OperationUpdate},
				APIGroups:  []string{crdGroup}, APIVersions: []string{crdVersion}, Resources: []string{crdPlural},
			}},
		}},
	})
	require.NoError(t, err)

	_, err = c.adminCR.Put(ctx, widget("marked", `{"size":1,"`+blockedField+`":true}`))
	require.ErrorIs(t, err, api.ErrForbidden, "the marked object is refused")
	assert.ErrorContains(t, err, webhookMessage)
	assert.ErrorContains(t, err, webhookName)
	_, err = c.adminCR.Get(ctx, "marked")
	require.ErrorIs(t, err, api.ErrNotFound, "the refused object was not stored")

	// The control: the same write without the field is accepted.
	_, err = c.adminCR.Put(ctx, widget("unmarked", `{"size":1}`))
	require.NoError(t, err, "the same write without the field")
	_, err = c.adminCR.Get(ctx, "unmarked")
	require.NoError(t, err)
	assert.Equal(t, []string{"marked", "unmarked"}, hook.Seen(), "the webhook was asked about both writes")
}

// extenderFilters shows the extender keeps a Machine off the Node
// LeastAllocated would choose, and that a Machine it does not filter goes to
// that Node.
func (c *cluster) extenderFilters(t *testing.T) {
	ctx, cancel := within(3 * time.Minute)
	defer cancel()

	// The filtered Machine goes first: LeastAllocated prefers bigNode for it
	// and, with the Nodes unloaded, scores smallNode lower.
	c.assertBigNodeIsFreer(t, ctx)
	_, err := c.admin.PutMachine(ctx, c.machine(filteredPrefix+"1", "2", "2Gi"))
	require.NoError(t, err)
	filtered := c.waitRunning(t, ctx, filteredPrefix+"1")
	assert.Equal(t, smallNode, filtered.Spec.NodeName, "the extender filtered out %s", bigNode)
	assert.Contains(t, c.extender.Refusals(), filteredPrefix+"1@"+bigNode)

	// The control: an unfiltered Machine goes to the Node LeastAllocated chooses.
	c.assertBigNodeIsFreer(t, ctx)
	_, err = c.admin.PutMachine(ctx, c.machine("control-1", "2", "2Gi"))
	require.NoError(t, err)
	control := c.waitRunning(t, ctx, "control-1")
	assert.Equal(t, bigNode, control.Spec.NodeName, "LeastAllocated chooses the Node with the most free capacity")
	assert.NotContains(t, c.extender.Refusals(), "control-1@"+bigNode)
	assert.Contains(t, c.extender.Asked(), "control-1", "the extender was asked about the control too")
}

// assertBigNodeIsFreer checks the two Nodes' free cpu and memory differ, with
// bigNode ahead on both, from their capacity and the Machines bound to them.
func (c *cluster) assertBigNodeIsFreer(t *testing.T, ctx context.Context) {
	t.Helper()
	free := map[string][2]*big.Rat{}
	for _, name := range []string{smallNode, bigNode} {
		n, err := c.admin.GetNode(ctx, name)
		require.NoError(t, err)
		cpu, err := n.Status.Capacity.CPU.Value()
		require.NoError(t, err)
		mem, err := n.Status.Capacity.Memory.Value()
		require.NoError(t, err)
		free[name] = [2]*big.Rat{cpu, mem}
	}
	// The Machines already placed must not be what makes the Nodes differ.
	require.Equal(t, 1, free[bigNode][0].Cmp(free[smallNode][0]), "%s has more cpu capacity than %s", bigNode, smallNode)
	require.Equal(t, 1, free[bigNode][1].Cmp(free[smallNode][1]), "%s has more memory capacity than %s", bigNode, smallNode)
	machines, err := c.admin.ListMachines(ctx)
	require.NoError(t, err)
	for _, m := range machines.Items {
		f, ok := free[m.Spec.NodeName]
		if !ok {
			continue
		}
		cpu, err := m.Spec.Resources.Requests.CPU.Value()
		require.NoError(t, err)
		mem, err := m.Spec.Resources.Requests.Memory.Value()
		require.NoError(t, err)
		free[m.Spec.NodeName] = [2]*big.Rat{new(big.Rat).Sub(f[0], cpu), new(big.Rat).Sub(f[1], mem)}
	}
	require.Equal(t, 1, free[bigNode][0].Cmp(free[smallNode][0]), "%s has more free cpu than %s: %v against %v", bigNode, smallNode, free[bigNode][0], free[smallNode][0])
	require.Equal(t, 1, free[bigNode][1].Cmp(free[smallNode][1]), "%s has more free memory than %s", bigNode, smallNode)
}

// noForbidden shows no gaon service met a refusal under RBAC: the agents, the
// controllers and the scheduler are all authorized for what they did.
func (c *cluster) noForbidden(t *testing.T) {
	for _, s := range c.services {
		out := s.output(t)
		assert.NotEmpty(t, out, "%s logged something, so its log is the one read", s.name)
		assert.NotContains(t, strings.ToLower(out), "forbidden", "%s met a refusal:\n%s", s.name, out)
		assert.False(t, s.exited(), "%s is still running", s.name)
	}
}
