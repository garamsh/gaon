package apiserver_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

func TestWebhookConfigurations_AreStoredAndServedByEachVerb(t *testing.T) {
	e := newEnv(t)
	w := newWebhookServer(t)

	m, err := e.admission().PutMutatingWebhookConfiguration(ctx(t), &api.MutatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindMutatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: "defaults"},
		Webhooks: []api.Webhook{w.hook("a.hooks.example", "/", "machines")},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, m.Metadata.ResourceVersion)
	v, err := e.admission().PutValidatingWebhookConfiguration(ctx(t), &api.ValidatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindValidatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: "policy"},
		Webhooks: []api.Webhook{w.hook("b.hooks.example", "/", "machines")},
	})
	require.NoError(t, err)

	gotM, err := e.admission().GetMutatingWebhookConfiguration(ctx(t), "defaults")
	require.NoError(t, err)
	assert.Equal(t, "a.hooks.example", gotM.Webhooks[0].Name)
	gotV, err := e.admission().GetValidatingWebhookConfiguration(ctx(t), "policy")
	require.NoError(t, err)
	assert.Equal(t, "b.hooks.example", gotV.Webhooks[0].Name)
	mutatings, err := e.admission().ListMutatingWebhookConfigurations(ctx(t))
	require.NoError(t, err)
	assert.Len(t, mutatings.Items, 1)
	validatings, err := e.admission().ListValidatingWebhookConfigurations(ctx(t))
	require.NoError(t, err)
	assert.Len(t, validatings.Items, 1)

	_, err = e.admission().PutMutatingWebhookConfiguration(ctx(t), &api.MutatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindMutatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: "defaults"},
		Webhooks: []api.Webhook{w.hook("a.hooks.example", "/", "machines")},
	})
	assert.ErrorIs(t, err, api.ErrAlreadyExists, "a body without a version creates")

	_, err = e.admission().DeleteMutatingWebhookConfiguration(ctx(t), "defaults", "999999")
	assert.ErrorIs(t, err, api.ErrConflict)
	_, err = e.admission().DeleteMutatingWebhookConfiguration(ctx(t), "defaults", m.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = e.admission().DeleteValidatingWebhookConfiguration(ctx(t), "policy", v.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = e.admission().GetMutatingWebhookConfiguration(ctx(t), "defaults")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestWebhookConfigurations_AnUpdateReplacesTheWebhooksAtTheCurrentVersion(t *testing.T) {
	e := newEnv(t)
	w := newWebhookServer(t)
	e.mutating(t, "cfg", w.hook("a.hooks.example", "/", "volumes"))
	stored, err := e.admission().GetMutatingWebhookConfiguration(ctx(t), "cfg")
	require.NoError(t, err)

	stored.Webhooks = []api.Webhook{w.hook("a.hooks.example", "/", "machines")}
	_, err = e.admission().PutMutatingWebhookConfiguration(ctx(t), stored)
	require.NoError(t, err)
	e.createMachine(t, machine("m1"))
	assert.Len(t, w.received(), 1, "the replaced rule is the one in force")

	_, err = e.admission().PutMutatingWebhookConfiguration(ctx(t), stored)
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version")
}

func TestWebhookConfigurations_RefuseAConfigurationThatIsNotValid(t *testing.T) {
	e := newEnv(t)
	w := newWebhookServer(t)
	bad := w.hook("a.hooks.example", "/", "machines")
	bad.ClientConfig.CABundle = nil

	_, err := e.admission().PutMutatingWebhookConfiguration(ctx(t), &api.MutatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindMutatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: "bad"},
		Webhooks: []api.Webhook{bad},
	})
	assert.ErrorIs(t, err, api.ErrInvalid)
	_, err = e.admission().PutValidatingWebhookConfiguration(ctx(t), &api.ValidatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindValidatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: "bad"},
		Webhooks: []api.Webhook{bad},
	})
	assert.ErrorIs(t, err, api.ErrInvalid)
}

func TestWebhookConfigurations_AWatchSeesAnAddedConfiguration(t *testing.T) {
	e := newEnv(t)
	w := newWebhookServer(t)
	list, err := e.admission().ListMutatingWebhookConfigurations(ctx(t))
	require.NoError(t, err)
	watch, err := e.admission().WatchMutatingWebhookConfigurations(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer watch.Close()

	e.mutating(t, "cfg", w.hook("a.hooks.example", "/", "machines"))

	ev, err := watch.Next()
	require.NoError(t, err)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "cfg", ev.Object.Metadata.Name)
}

func TestWebhookConfigurations_AreAuthorizedAsTheirOwnResources(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	w := newWebhookServer(t)
	admin := c.admissionClient(t, addr, adminID)
	_, adminRBAC := c.clients(t, addr, adminID)
	holder := c.admissionClient(t, addr, who{"holder", nil})
	stranger := c.admissionClient(t, addr, strangerID)
	mutating := &api.MutatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindMutatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: "cfg"},
		Webhooks: []api.Webhook{w.hook("a.hooks.example", "/", "machines")},
	}
	validating := &api.ValidatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindValidatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: "cfg"},
		Webhooks: []api.Webhook{w.hook("a.hooks.example", "/", "machines")},
	}

	// A caller without a role is refused every verb of both kinds.
	_, err := stranger.PutMutatingWebhookConfiguration(ctx(t), mutating)
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = stranger.PutValidatingWebhookConfiguration(ctx(t), validating)
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = stranger.ListMutatingWebhookConfigurations(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = stranger.ListValidatingWebhookConfigurations(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden)

	// A role on one kind's resource lets the holder write and read that kind
	// and not the other.
	all := []api.Verb{api.VerbGet, api.VerbList, api.VerbCreate, api.VerbUpdate, api.VerbDelete}
	_, err = adminRBAC.PutClusterRole(ctx(t), clusterRole("mutating-only", rule(api.AdmissionRegistrationGroup, "mutatingwebhookconfigurations", all...)))
	require.NoError(t, err)
	_, err = adminRBAC.PutClusterRoleBinding(ctx(t), clusterRoleBinding("mutating-only", "mutating-only", userSubject("holder")))
	require.NoError(t, err)

	created, err := holder.PutMutatingWebhookConfiguration(ctx(t), mutating)
	require.NoError(t, err)
	_, err = holder.GetMutatingWebhookConfiguration(ctx(t), "cfg")
	require.NoError(t, err)
	_, err = holder.ListMutatingWebhookConfigurations(ctx(t))
	require.NoError(t, err)
	_, err = holder.PutValidatingWebhookConfiguration(ctx(t), validating)
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = holder.ListValidatingWebhookConfigurations(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = holder.DeleteMutatingWebhookConfiguration(ctx(t), "cfg", created.Metadata.ResourceVersion)
	require.NoError(t, err)

	// The admin holds both.
	m, err := admin.PutMutatingWebhookConfiguration(ctx(t), mutating)
	require.NoError(t, err)
	v, err := admin.PutValidatingWebhookConfiguration(ctx(t), validating)
	require.NoError(t, err)
	_, err = holder.DeleteValidatingWebhookConfiguration(ctx(t), "cfg", v.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrForbidden)
	_, err = admin.DeleteMutatingWebhookConfiguration(ctx(t), "cfg", m.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = admin.DeleteValidatingWebhookConfiguration(ctx(t), "cfg", v.Metadata.ResourceVersion)
	require.NoError(t, err)
}

func TestAdmission_AWriteThatNoRuleMatchesIsNotSent(t *testing.T) {
	rule := func(w *webhookServer, mutate func(*api.WebhookRule)) api.Webhook {
		h := w.hook("a.hooks.example", "/", "machines")
		h.Rules[0].Operations = []api.Operation{api.OperationCreate}
		h.Rules[0].APIGroups = []string{api.Group}
		h.Rules[0].APIVersions = []string{api.Version}
		mutate(&h.Rules[0])
		return h
	}
	misses := map[string]func(*api.WebhookRule){
		"operation": func(r *api.WebhookRule) { r.Operations = []api.Operation{api.OperationUpdate, api.OperationDelete} },
		"group":     func(r *api.WebhookRule) { r.APIGroups = []string{"other.gaon.garam.sh"} },
		"version":   func(r *api.WebhookRule) { r.APIVersions = []string{"v2"} },
		"resource":  func(r *api.WebhookRule) { r.Resources = []string{"nodes"} },
		"a subresource of the resource": func(r *api.WebhookRule) {
			r.Resources = []string{"machines/status"}
		},
	}
	for name, miss := range misses {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			w := newWebhookServer(t)
			h := rule(w, miss)
			e.mutating(t, "m", h)
			e.validating(t, "v", h)

			e.createMachine(t, machine("m1"))
			assert.Empty(t, w.received(), "no rule matches a create of machines")

			// Control: the rule with the matching field is sent the same write.
			e.mutating(t, "hit", rule(w, func(*api.WebhookRule) {}))
			e.createMachine(t, machine("m2"))
			assert.Len(t, w.received(), 1)
		})
	}
}

func TestAdmission_AWriteToAConfigurationKindIsNeverSent(t *testing.T) {
	e := newEnv(t)
	w := newWebhookServer(t)
	e.mutating(t, "all-mutating", w.everything())
	e.validating(t, "all-validating", w.everything())
	require.Empty(t, w.received(), "registering the configurations sends nothing")

	cfg, err := e.admission().GetMutatingWebhookConfiguration(ctx(t), "all-mutating")
	require.NoError(t, err)
	updated, err := e.admission().PutMutatingWebhookConfiguration(ctx(t), cfg)
	require.NoError(t, err)
	_, err = e.admission().DeleteMutatingWebhookConfiguration(ctx(t), "all-mutating", updated.Metadata.ResourceVersion)
	require.NoError(t, err)
	assert.Empty(t, w.received(), "an update and a delete of a configuration reach no webhook, whatever its rules")

	// Control: a write to another kind is sent to the same webhooks.
	e.createMachine(t, machine("m1"))
	assert.Len(t, w.received(), 1, "the validating webhook still matches")
}

func TestAdmission_AReadIsNeverSent(t *testing.T) {
	e := newEnv(t)
	w := newWebhookServer(t)
	e.createMachine(t, machine("m1"))
	e.mutating(t, "all-mutating", w.everything())
	e.validating(t, "all-validating", w.everything())

	_, err := e.api.GetMachine(ctx(t), "m1")
	require.NoError(t, err)
	_, err = e.api.ListMachines(ctx(t))
	require.NoError(t, err)
	assert.Empty(t, w.received())

	// Control: a write is.
	e.createMachine(t, machine("m2"))
	assert.Len(t, w.received(), 2)
}

func TestAdmission_ACallCarriesTheWriteInTheShapeOfADR0010(t *testing.T) {
	e := newEnv(t)
	w := newWebhookServer(t)
	e.validating(t, "all", w.everything())

	created := e.createMachine(t, machine("m1"))
	calls := w.received()
	require.Len(t, calls, 1)
	c := calls[0]
	assert.Equal(t, "POST", c.method)
	assert.Equal(t, "application/json", c.contentType)
	assert.Equal(t, api.AdmissionGroupVersion, c.review.APIVersion)
	assert.Equal(t, api.KindAdmissionReview, c.review.Kind)
	q := c.review.Request
	require.NotNil(t, q)
	assert.NotEmpty(t, q.UID)
	assert.Equal(t, api.GroupVersionKind{Group: api.Group, Version: api.Version, Kind: api.KindMachine}, q.Kind)
	assert.Equal(t, api.GroupVersionResource{Group: api.Group, Version: api.Version, Resource: "machines"}, q.Resource)
	assert.Empty(t, q.SubResource)
	assert.Equal(t, "m1", q.Name)
	assert.Equal(t, api.OperationCreate, q.Operation)
	assert.NotEmpty(t, q.Object, "a create carries object")
	assert.Empty(t, q.OldObject, "and no oldObject")
	var sent api.Machine
	require.NoError(t, json.Unmarshal(q.Object, &sent))
	assert.Equal(t, api.MachinePending, sent.Status.Phase, "the object is the one about to be stored")

	// An update carries both objects, at their own versions.
	next := *created
	next.Spec.IdleTimeoutSeconds = 30
	updated, err := e.api.PutMachine(ctx(t), &next)
	require.NoError(t, err)
	q = w.received()[1].review.Request
	assert.Equal(t, api.OperationUpdate, q.Operation)
	var newer, older api.Machine
	require.NoError(t, json.Unmarshal(q.Object, &newer))
	require.NoError(t, json.Unmarshal(q.OldObject, &older))
	assert.Equal(t, int32(30), newer.Spec.IdleTimeoutSeconds)
	assert.Equal(t, int32(0), older.Spec.IdleTimeoutSeconds)
	assert.Equal(t, created.Metadata.ResourceVersion, older.Metadata.ResourceVersion)

	// A delete carries oldObject and no object.
	_, err = e.api.DeleteMachine(ctx(t), "m1", updated.Metadata.ResourceVersion)
	require.NoError(t, err)
	q = w.received()[2].review.Request
	assert.Equal(t, api.OperationDelete, q.Operation)
	assert.Empty(t, q.Object)
	assert.NotEmpty(t, q.OldObject)
}

func TestAdmission_AStatusWriteAndABindingAreSentOnTheirSubresources(t *testing.T) {
	e := newEnv(t)
	w := newWebhookServer(t)
	e.createNode(t, "n1")
	m := e.createMachine(t, machine("m1"))
	e.validating(t, "all", w.everything())

	bound := e.bind(t, m, "n1", 1)
	q := w.received()[0].review.Request
	assert.Equal(t, api.OperationCreate, q.Operation)
	assert.Equal(t, "binding", q.SubResource)
	assert.Equal(t, "machines", q.Resource.Resource)
	assert.Equal(t, "m1", q.Name)
	assert.Equal(t, "Binding", q.Kind.Kind)
	assert.Empty(t, q.OldObject)
	var b api.Binding
	require.NoError(t, json.Unmarshal(q.Object, &b))
	assert.Equal(t, "n1", b.NodeName)

	running := *bound
	running.Status = api.MachineStatus{Phase: api.MachineRunning, BindingEpoch: 1}
	_, err := e.api.PutMachineStatus(ctx(t), &running)
	require.NoError(t, err)
	q = w.received()[1].review.Request
	assert.Equal(t, api.OperationUpdate, q.Operation)
	assert.Equal(t, "status", q.SubResource)
	assert.Equal(t, api.KindMachine, q.Kind.Kind)
	var newer, older api.Machine
	require.NoError(t, json.Unmarshal(q.Object, &newer))
	require.NoError(t, json.Unmarshal(q.OldObject, &older))
	assert.Equal(t, api.MachineRunning, newer.Status.Phase)
	assert.Equal(t, api.MachinePending, older.Status.Phase)
}

func TestAdmission_ARuleOnASubresourceSelectsThatSubresourceOnly(t *testing.T) {
	e := newEnv(t)
	w := newWebhookServer(t)
	e.createNode(t, "n1")
	m := e.createMachine(t, machine("m1"))
	e.validating(t, "status", w.hook("a.hooks.example", "/", "machines/status"))

	bound := e.bind(t, m, "n1", 1)
	assert.Empty(t, w.received(), "the binding is machines/binding")
	next := *bound
	next.Spec.IdleTimeoutSeconds = 30
	updated, err := e.api.PutMachine(ctx(t), &next)
	require.NoError(t, err)
	assert.Empty(t, w.received(), "the machine itself is not a subresource")

	running := *updated
	running.Status = api.MachineStatus{Phase: api.MachineRunning, BindingEpoch: 1}
	_, err = e.api.PutMachineStatus(ctx(t), &running)
	require.NoError(t, err)
	assert.Len(t, w.received(), 1)
}

func TestAdmission_TheCallerIsTheIdentityOfTheCertificate(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	w := newWebhookServer(t)
	admin, adminAdmission := c.admissionClient(t, addr, adminID), c.admissionClient(t, addr, adminID)
	_ = admin
	_, err := adminAdmission.PutValidatingWebhookConfiguration(ctx(t), &api.ValidatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindValidatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: "all"},
		Webhooks: []api.Webhook{w.everything()},
	})
	require.NoError(t, err)
	compute, _ := c.clients(t, addr, controllersID)

	_, err = compute.PutMachine(ctx(t), machine("m1"))
	require.NoError(t, err)

	calls := w.received()
	require.Len(t, calls, 1)
	assert.Equal(t, api.UserInfo{Username: "gaon-controllers", Groups: []string{"gaon:controllers"}}, calls[0].review.Request.UserInfo)
}
