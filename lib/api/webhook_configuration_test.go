package api_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newWebhook(t *testing.T, name string) api.Webhook {
	t.Helper()
	return api.Webhook{
		Name: name,
		ClientConfig: api.WebhookClientConfig{
			URL:      "https://hooks.example.com:8443/admit",
			CABundle: newTestCA(t, "webhook-ca").pem,
		},
		Rules: []api.WebhookRule{{
			Operations:  []api.Operation{api.OperationCreate, api.OperationUpdate},
			APIGroups:   []string{api.Group},
			APIVersions: []string{api.Version},
			Resources:   []string{"microvms", "microvms/status"},
		}},
	}
}

func newMutatingConfiguration(t *testing.T, name string) *api.MutatingWebhookConfiguration {
	t.Helper()
	return &api.MutatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindMutatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: name},
		Webhooks: []api.Webhook{newWebhook(t, "defaults.example.com")},
	}
}

func newValidatingConfiguration(t *testing.T, name string) *api.ValidatingWebhookConfiguration {
	t.Helper()
	return &api.ValidatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindValidatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: name},
		Webhooks: []api.Webhook{newWebhook(t, "policy.example.com")},
	}
}

// validateWebhooks validates webhooks in a configuration of each kind and
// returns the errors, so a rule is checked on both.
func validateWebhooks(t *testing.T, webhooks ...api.Webhook) []error {
	t.Helper()
	m := newMutatingConfiguration(t, "m")
	m.Webhooks = webhooks
	v := newValidatingConfiguration(t, "v")
	v.Webhooks = webhooks
	return []error{m.Validate(), v.Validate()}
}

func assertInvalid(t *testing.T, errs []error, msgAndArgs ...any) {
	t.Helper()
	for _, err := range errs {
		assert.ErrorIs(t, err, api.ErrInvalid, msgAndArgs...)
	}
}

func assertValid(t *testing.T, errs []error, msgAndArgs ...any) {
	t.Helper()
	for _, err := range errs {
		assert.NoError(t, err, msgAndArgs...)
	}
}

func TestWebhookConfigurationValidateChecksIdentity(t *testing.T) {
	require.NoError(t, newMutatingConfiguration(t, "c").Validate(), "control: the admissionregistration group, the right kind and a name")
	require.NoError(t, newValidatingConfiguration(t, "c").Validate())

	m := newMutatingConfiguration(t, "c")
	m.APIVersion = api.GroupVersion
	assert.ErrorIs(t, m.Validate(), api.ErrInvalid)
	m = newMutatingConfiguration(t, "c")
	m.Kind = api.KindValidatingWebhookConfiguration
	assert.ErrorIs(t, m.Validate(), api.ErrInvalid, "a mutating configuration is not a validating one")
	assert.ErrorIs(t, newMutatingConfiguration(t, "").Validate(), api.ErrInvalid)

	v := newValidatingConfiguration(t, "c")
	v.APIVersion = api.GroupVersion
	assert.ErrorIs(t, v.Validate(), api.ErrInvalid)
	v = newValidatingConfiguration(t, "c")
	v.Kind = api.KindMutatingWebhookConfiguration
	assert.ErrorIs(t, v.Validate(), api.ErrInvalid)
	assert.ErrorIs(t, newValidatingConfiguration(t, "").Validate(), api.ErrInvalid)
}

func TestWebhookValidateRefusesANameThatIsNotADomainOfThreeLabels(t *testing.T) {
	for _, name := range []string{"a.b.c", "my-hook.example.com", "a1.b-2.c3.d4"} {
		assertValid(t, validateWebhooks(t, newWebhook(t, name)), "control: %q", name)
	}
	long := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 63)
	assertValid(t, validateWebhooks(t, newWebhook(t, long[:253])), "control: 253 characters")
	for _, name := range []string{"", "hook", "hook.example", "Hook.example.com", "hook..example.com", "-hook.example.com", "hook_x.example.com", "hook.example.com.", long + ".e"} {
		assertInvalid(t, validateWebhooks(t, newWebhook(t, name)), "name %q", name)
	}
}

func TestWebhookValidateRefusesADuplicateNameWithinAConfiguration(t *testing.T) {
	assertValid(t, validateWebhooks(t, newWebhook(t, "a.example.com"), newWebhook(t, "b.example.com")), "control: two names")
	assertInvalid(t, validateWebhooks(t, newWebhook(t, "a.example.com"), newWebhook(t, "b.example.com"), newWebhook(t, "a.example.com")))
}

func TestWebhookValidateRefusesAURLThatIsNotPlainHTTPS(t *testing.T) {
	for _, u := range []string{"https://hooks.example.com/admit", "https://hooks.example.com", "https://10.0.0.5:9443/a/b", "https://[::1]:9443/"} {
		w := newWebhook(t, "a.example.com")
		w.ClientConfig.URL = u
		assertValid(t, validateWebhooks(t, w), "control: %q", u)
	}
	for _, u := range []string{
		"", "hooks.example.com", "http://hooks.example.com/admit", "ftp://hooks.example.com", "https://",
		"https:///admit", "https:hooks.example.com", "https://user@hooks.example.com/", "https://user:pw@hooks.example.com/",
		"https://hooks.example.com/admit?a=b", "https://hooks.example.com/admit?", "https://hooks.example.com/admit#frag",
		"https://hooks.example.com/admit#", "https://hooks .example.com/",
	} {
		w := newWebhook(t, "a.example.com")
		w.ClientConfig.URL = u
		assertInvalid(t, validateWebhooks(t, w), "url %q", u)
	}
}

func TestWebhookValidateRefusesACABundleThatHoldsNoCertificate(t *testing.T) {
	ca := newTestCA(t, "ca").pem
	two := append(append([]byte{}, ca...), newTestCA(t, "second").pem...)
	for name, bundle := range map[string][]byte{"one certificate": ca, "two certificates": two} {
		w := newWebhook(t, "a.example.com")
		w.ClientConfig.CABundle = bundle
		assertValid(t, validateWebhooks(t, w), "control: %s", name)
	}
	for name, bundle := range map[string][]byte{
		"absent":                                nil,
		"empty":                                 {},
		"text":                                  []byte("not a certificate"),
		"a PEM block that is not a certificate": []byte("-----BEGIN PRIVATE KEY-----\nMAAA\n-----END PRIVATE KEY-----\n"),
		"a certificate block with bad bytes":    []byte("-----BEGIN CERTIFICATE-----\nMAAA\n-----END CERTIFICATE-----\n"),
	} {
		w := newWebhook(t, "a.example.com")
		w.ClientConfig.CABundle = bundle
		assertInvalid(t, validateWebhooks(t, w), name)
	}
}

func TestWebhookCABundleIsBase64InJSON(t *testing.T) {
	w := newWebhook(t, "a.example.com")
	raw, err := json.Marshal(w.ClientConfig)
	require.NoError(t, err)
	var fields map[string]string
	require.NoError(t, json.Unmarshal(raw, &fields))
	assert.NotContains(t, fields["caBundle"], "BEGIN CERTIFICATE", "the field is base64 of the PEM, as in Kubernetes")

	var back api.WebhookClientConfig
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, w.ClientConfig.CABundle, back.CABundle)
}

func TestWebhookValidateRefusesARuleThatSelectsNothing(t *testing.T) {
	for name, edit := range map[string]func(*api.WebhookRule){
		"no operations":     func(r *api.WebhookRule) { r.Operations = nil },
		"no apiGroups":      func(r *api.WebhookRule) { r.APIGroups = nil },
		"no apiVersions":    func(r *api.WebhookRule) { r.APIVersions = nil },
		"no resources":      func(r *api.WebhookRule) { r.Resources = nil },
		"an empty apiGroup": func(r *api.WebhookRule) { r.APIGroups = []string{api.Group, ""} },
		"an empty version":  func(r *api.WebhookRule) { r.APIVersions = []string{""} },
	} {
		w := newWebhook(t, "a.example.com")
		edit(&w.Rules[0])
		assertInvalid(t, validateWebhooks(t, w), name)
	}

	w := newWebhook(t, "a.example.com")
	w.Rules = nil
	assertInvalid(t, validateWebhooks(t, w), "a webhook with no rules is sent nothing")

	wildcards := newWebhook(t, "a.example.com")
	wildcards.Rules = []api.WebhookRule{{Operations: []api.Operation{"*"}, APIGroups: []string{"*"}, APIVersions: []string{"*"}, Resources: []string{"*"}}}
	assertValid(t, validateWebhooks(t, wildcards), "control: the wildcard is a value in each field")
}

func TestWebhookValidateRefusesAnUnknownOperation(t *testing.T) {
	w := newWebhook(t, "a.example.com")
	w.Rules[0].Operations = []api.Operation{"CREATE", "UPDATE", "DELETE", "*"}
	assertValid(t, validateWebhooks(t, w), "control: every operation, and the wildcard")
	for _, op := range []api.Operation{"", "create", "CONNECT", "PATCH", "Create"} {
		w := newWebhook(t, "a.example.com")
		w.Rules[0].Operations = []api.Operation{"CREATE", op}
		assertInvalid(t, validateWebhooks(t, w), "operation %q", op)
	}
}

func TestWebhookValidateRefusesAResourceThatIsNotAPattern(t *testing.T) {
	for _, res := range []string{"*", "*/*", "microvms", "microvms/status", "microvms/*", "*/status", "custom-things", "things2/sub-x"} {
		w := newWebhook(t, "a.example.com")
		w.Rules[0].Resources = []string{res}
		assertValid(t, validateWebhooks(t, w), "control: %q", res)
	}
	for _, res := range []string{"", "/", "microvms/", "/status", "a/b/c", "MicroVMs", "microvms/Status", "ma*", "**", "-microvms", "microvms status"} {
		w := newWebhook(t, "a.example.com")
		w.Rules[0].Resources = []string{"microvms", res}
		assertInvalid(t, validateWebhooks(t, w), "resource %q", res)
	}
}

func TestWebhookValidateRefusesAFailurePolicyButFailOrIgnore(t *testing.T) {
	for _, p := range []api.FailurePolicy{"", "Fail", "Ignore"} {
		w := newWebhook(t, "a.example.com")
		w.FailurePolicy = p
		assertValid(t, validateWebhooks(t, w), "control: %q", p)
	}
	for _, p := range []api.FailurePolicy{"fail", "ignore", "Allow", "Retry"} {
		w := newWebhook(t, "a.example.com")
		w.FailurePolicy = p
		assertInvalid(t, validateWebhooks(t, w), "policy %q", p)
	}
}

func TestWebhookValidateRefusesATimeoutOutsideOneToThirty(t *testing.T) {
	for _, s := range []int32{1, 10, 30} {
		w := newWebhook(t, "a.example.com")
		w.TimeoutSeconds = &s
		assertValid(t, validateWebhooks(t, w), "control: %d", s)
	}
	for _, s := range []int32{0, -1, 31, 600} {
		w := newWebhook(t, "a.example.com")
		w.TimeoutSeconds = &s
		assertInvalid(t, validateWebhooks(t, w), "timeoutSeconds %d", s)
	}
	assertValid(t, validateWebhooks(t, newWebhook(t, "a.example.com")), "control: an absent timeout is the default")
}

func TestWebhookDefaults(t *testing.T) {
	w := newWebhook(t, "a.example.com")
	assert.Equal(t, api.FailurePolicyFail, w.FailurePolicyOrDefault())
	assert.Equal(t, 10*time.Second, w.Timeout())

	five := int32(5)
	w.FailurePolicy, w.TimeoutSeconds = api.FailurePolicyIgnore, &five
	assert.Equal(t, api.FailurePolicyIgnore, w.FailurePolicyOrDefault())
	assert.Equal(t, 5*time.Second, w.Timeout())
}

func TestValidateRefusalNamesTheWebhook(t *testing.T) {
	bad := newWebhook(t, "b.example.com")
	bad.Rules = nil
	errs := validateWebhooks(t, newWebhook(t, "a.example.com"), bad)
	for _, err := range errs {
		assert.ErrorContains(t, err, "webhooks[1]", "the refusal says which webhook")
	}
}

func TestClientMutatingWebhookConfigurationPutGetListDelete(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	_, err := c.GetMutatingWebhookConfiguration(ctx, "c")
	assert.ErrorIs(t, err, api.ErrNotFound)

	created, err := c.PutMutatingWebhookConfiguration(ctx, newMutatingConfiguration(t, "c"))
	require.NoError(t, err)
	_, err = c.PutMutatingWebhookConfiguration(ctx, newMutatingConfiguration(t, "c"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists)

	update := *created
	update.Webhooks = []api.Webhook{newWebhook(t, "other.example.com")}
	updated, err := c.PutMutatingWebhookConfiguration(ctx, &update)
	require.NoError(t, err)
	_, err = c.PutMutatingWebhookConfiguration(ctx, &update)
	assert.ErrorIs(t, err, api.ErrConflict, "the first update made the version stale")

	got, err := c.GetMutatingWebhookConfiguration(ctx, "c")
	require.NoError(t, err)
	assert.Equal(t, "other.example.com", got.Webhooks[0].Name)

	list, err := c.ListMutatingWebhookConfigurations(ctx)
	require.NoError(t, err)
	assert.Equal(t, api.KindMutatingWebhookConfigurationList, list.Kind)
	require.Len(t, list.Items, 1)
	assert.Equal(t, updated.Metadata.ResourceVersion, list.Metadata.ResourceVersion)

	_, err = c.DeleteMutatingWebhookConfiguration(ctx, "c", created.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrConflict)
	_, err = c.DeleteMutatingWebhookConfiguration(ctx, "c", "")
	assert.Error(t, err, "a delete names the version it deletes")
	_, err = c.DeleteMutatingWebhookConfiguration(ctx, "c", updated.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = c.GetMutatingWebhookConfiguration(ctx, "c")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestClientValidatingWebhookConfigurationPutGetListDelete(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	_, err := c.GetValidatingWebhookConfiguration(ctx, "c")
	assert.ErrorIs(t, err, api.ErrNotFound)

	created, err := c.PutValidatingWebhookConfiguration(ctx, newValidatingConfiguration(t, "c"))
	require.NoError(t, err)
	_, err = c.PutValidatingWebhookConfiguration(ctx, newValidatingConfiguration(t, "c"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists)

	update := *created
	update.Webhooks = []api.Webhook{newWebhook(t, "other.example.com")}
	updated, err := c.PutValidatingWebhookConfiguration(ctx, &update)
	require.NoError(t, err)
	_, err = c.PutValidatingWebhookConfiguration(ctx, &update)
	assert.ErrorIs(t, err, api.ErrConflict)

	got, err := c.GetValidatingWebhookConfiguration(ctx, "c")
	require.NoError(t, err)
	assert.Equal(t, "other.example.com", got.Webhooks[0].Name)

	list, err := c.ListValidatingWebhookConfigurations(ctx)
	require.NoError(t, err)
	assert.Equal(t, api.KindValidatingWebhookConfigurationList, list.Kind)
	require.Len(t, list.Items, 1)

	_, err = c.DeleteValidatingWebhookConfiguration(ctx, "c", "")
	assert.Error(t, err)
	_, err = c.DeleteValidatingWebhookConfiguration(ctx, "c", updated.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = c.GetValidatingWebhookConfiguration(ctx, "c")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestClientPutWebhookConfigurationRefusesAnInvalidOne(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	bad := newValidatingConfiguration(t, "c")
	bad.Webhooks[0].ClientConfig.URL = "http://hooks.example.com/"
	_, err := c.PutValidatingWebhookConfiguration(ctx, bad)
	assert.ErrorIs(t, err, api.ErrInvalid)
	_, err = c.PutValidatingWebhookConfiguration(ctx, newValidatingConfiguration(t, "c"))
	assert.NoError(t, err, "control: the same configuration with an https URL is accepted")
}

func TestClientWatchWebhookConfigurationsDeliversTheirKindOnly(t *testing.T) {
	ctx := testContext(t)
	s, c := newMemoryServer(t)
	_, err := c.PutValidatingWebhookConfiguration(ctx, newValidatingConfiguration(t, "validating"))
	require.NoError(t, err)
	created, err := c.PutMutatingWebhookConfiguration(ctx, newMutatingConfiguration(t, "mutating"))
	require.NoError(t, err)

	w, err := c.WatchMutatingWebhookConfigurations(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	ev := nextEvent(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "mutating", ev.Object.Metadata.Name, "the validating configuration's event is not delivered")

	_, err = c.DeleteMutatingWebhookConfiguration(ctx, "mutating", created.Metadata.ResourceVersion)
	require.NoError(t, err)
	ev = nextEvent(t, w)
	assert.Equal(t, api.EventDeleted, ev.Type)

	v, err := c.WatchValidatingWebhookConfigurations(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = v.Close() })
	vev := nextEvent(t, v)
	assert.Equal(t, "validating", vev.Object.Metadata.Name)

	s.compact()
	stale, err := c.WatchMutatingWebhookConfigurations(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = stale.Close() })
	_, err = stale.Next()
	assert.ErrorIs(t, err, api.ErrExpired)

	_, err = c.WatchMutatingWebhookConfigurations(ctx, "")
	assert.Error(t, err, "a watch names the version it resumes from")
	_, err = c.WatchValidatingWebhookConfigurations(ctx, "")
	assert.Error(t, err)
}
