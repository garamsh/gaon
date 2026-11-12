package apiserver_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// imageOf returns the image of the Machine in a call's object.
func imageOf(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var m api.Machine
	require.NoError(t, json.Unmarshal(raw, &m))
	return m.Spec.Image
}

func TestAdmission_AMutatingPatchIsAppliedBeforeTheServersOwnValidation(t *testing.T) {
	e := newEnv(t)
	w := newWebhookServer(t)
	w.answer = func(c call) *api.AdmissionResponse {
		return patch(c, `[{"op":"replace","path":"/spec/runStrategy","value":"Always"}]`)
	}
	e.mutating(t, "fix", w.hook("a.hooks.example", "/", "machines"))
	m := machine("m1")
	m.Spec.RunStrategy = ""

	created, err := e.api.PutMachine(ctx(t), m)

	require.NoError(t, err, "the body is invalid until the patch is applied")
	assert.Equal(t, api.RunAlways, created.Spec.RunStrategy)
}

func TestAdmission_AValidatingWebhookSeesThePatchedObjectAfterTheServersValidation(t *testing.T) {
	e := newEnv(t)
	mut := newWebhookServer(t)
	val := newWebhookServer(t)
	patchTo := func(ops string) {
		mut.answer = func(c call) *api.AdmissionResponse { return patch(c, ops) }
	}
	e.mutating(t, "patch", mut.hook("a.hooks.example", "/", "machines"))
	e.validating(t, "watch", val.hook("b.hooks.example", "/", "machines"))

	// A patch that makes the body invalid is refused before any validating call.
	patchTo(`[{"op":"replace","path":"/spec/runStrategy","value":"Bogus"}]`)
	_, err := e.api.PutMachine(ctx(t), machine("m1"))
	assert.ErrorIs(t, err, api.ErrInvalid)
	assert.Empty(t, val.received(), "the server's validation comes before the validating webhooks")

	// Control: a valid patch reaches the validating webhook, patched.
	patchTo(setImage)
	_, err = e.api.PutMachine(ctx(t), machine("m1"))
	require.NoError(t, err)
	require.Len(t, val.received(), 1)
	assert.Equal(t, "patched", imageOf(t, val.received()[0].review.Request.Object))
}

func TestAdmission_AMutatingPatchIsSeenByTheNextWebhookInNameOrder(t *testing.T) {
	e := newEnv(t)
	w := newWebhookServer(t)
	w.answer = func(c call) *api.AdmissionResponse {
		switch c.path {
		case "/a":
			return patch(c, `[{"op":"replace","path":"/spec/image","value":"from-a"}]`)
		case "/b":
			if imageOf(t, c.review.Request.Object) == "from-a" {
				return patch(c, `[{"op":"replace","path":"/spec/image","value":"from-b"}]`)
			}
		}
		return allow(c)
	}
	// Registered in the opposite order to their names.
	e.mutating(t, "b-config", w.hook("b.hooks.example", "/b", "machines"))
	e.mutating(t, "a-config", w.hook("a.hooks.example", "/a", "machines"))

	created, err := e.api.PutMachine(ctx(t), machine("m1"))

	require.NoError(t, err)
	assert.Equal(t, "from-b", created.Spec.Image)
	require.Len(t, w.received(), 2)
	assert.Equal(t, "/a", w.received()[0].path)
	assert.Equal(t, "/b", w.received()[1].path)
}

func TestAdmission_TheWebhooksOfOneConfigurationAreCalledInListedOrder(t *testing.T) {
	e := newEnv(t)
	w := newWebhookServer(t)
	w.answer = func(c call) *api.AdmissionResponse {
		if c.path == "/first" {
			return patch(c, `[{"op":"replace","path":"/spec/image","value":"from-first"}]`)
		}
		if imageOf(t, c.review.Request.Object) == "from-first" {
			return patch(c, `[{"op":"replace","path":"/spec/image","value":"from-second"}]`)
		}
		return allow(c)
	}
	// The names sort the other way round.
	e.mutating(t, "cfg", w.hook("z.hooks.example", "/first", "machines"), w.hook("a.hooks.example", "/second", "machines"))

	created, err := e.api.PutMachine(ctx(t), machine("m1"))

	require.NoError(t, err)
	assert.Equal(t, "from-second", created.Spec.Image)
}

func TestAdmission_AMutatingPatchThatChangesAnIdentityFieldIsAFailedCall(t *testing.T) {
	patches := map[string]string{
		"apiVersion":      `[{"op":"add","path":"/apiVersion","value":"compute.gaon.garam.sh/v9"}]`,
		"kind":            `[{"op":"add","path":"/kind","value":"Node"}]`,
		"name":            `[{"op":"add","path":"/metadata/name","value":"renamed"}]`,
		"resourceVersion": `[{"op":"add","path":"/metadata/resourceVersion","value":"5"}]`,
	}
	for field, ops := range patches {
		t.Run(field, func(t *testing.T) {
			for _, policy := range []api.FailurePolicy{api.FailurePolicyFail, api.FailurePolicyIgnore} {
				e := newEnv(t)
				w := newWebhookServer(t)
				w.answer = func(c call) *api.AdmissionResponse {
					return patch(c, `[{"op":"replace","path":"/spec/image","value":"patched"},`+ops[1:])
				}
				h := w.hook("a.hooks.example", "/", "machines")
				h.FailurePolicy = policy
				e.mutating(t, "cfg", h)

				created, err := e.api.PutMachine(ctx(t), machine("m1"))

				if policy == api.FailurePolicyFail {
					assert.ErrorIs(t, err, api.ErrInternalError)
					_, err = e.api.GetMachine(ctx(t), "m1")
					assert.ErrorIs(t, err, api.ErrNotFound)
					continue
				}
				require.NoError(t, err, "the webhook is skipped")
				assert.Equal(t, "alpine", created.Spec.Image, "and none of its patch is applied")
			}
		})
	}

	t.Run("control", func(t *testing.T) {
		e := newEnv(t)
		w := newWebhookServer(t)
		w.answer = func(c call) *api.AdmissionResponse { return patch(c, setImage) }
		e.mutating(t, "cfg", w.hook("a.hooks.example", "/", "machines"))

		created, err := e.api.PutMachine(ctx(t), machine("m1"))

		require.NoError(t, err)
		assert.Equal(t, "patched", created.Spec.Image)
	})
}

func TestAdmission_AnIdentityPatchOnAnUpdateIsAFailedCall(t *testing.T) {
	e := newEnv(t)
	created := e.createMachine(t, machine("m1"))
	w := newWebhookServer(t)
	w.answer = func(c call) *api.AdmissionResponse {
		return patch(c, `[{"op":"replace","path":"/metadata/resourceVersion","value":"1"}]`)
	}
	e.mutating(t, "cfg", w.hook("a.hooks.example", "/", "machines"))
	next := *created
	next.Spec.IdleTimeoutSeconds = 30

	_, err := e.api.PutMachine(ctx(t), &next)

	assert.ErrorIs(t, err, api.ErrInternalError)
}

func TestAdmission_ARefusalIsForbiddenWhateverTheStatusCodeSays(t *testing.T) {
	stages := map[string]func(*env, *testing.T, api.Webhook){
		"mutating":   func(e *env, t *testing.T, h api.Webhook) { e.mutating(t, "cfg", h) },
		"validating": func(e *env, t *testing.T, h api.Webhook) { e.validating(t, "cfg", h) },
	}
	for stage, register := range stages {
		for _, policy := range []api.FailurePolicy{api.FailurePolicyFail, api.FailurePolicyIgnore} {
			t.Run(stage+" "+string(policy), func(t *testing.T) {
				e := newEnv(t)
				w := newWebhookServer(t)
				refuse := true
				w.answer = func(c call) *api.AdmissionResponse {
					if refuse {
						return deny(c, http.StatusConflict, "no machines today")
					}
					return allow(c)
				}
				h := w.hook("refuse.hooks.example", "/", "machines")
				h.FailurePolicy = policy
				register(e, t, h)

				_, err := e.api.PutMachine(ctx(t), machine("m1"))

				require.ErrorIs(t, err, api.ErrForbidden)
				assert.NotErrorIs(t, err, api.ErrConflict)
				assert.Contains(t, err.Error(), "refuse.hooks.example")
				assert.Contains(t, err.Error(), "no machines today")
				_, err = e.api.GetMachine(ctx(t), "m1")
				assert.ErrorIs(t, err, api.ErrNotFound, "a refused write is not stored")

				// Control: the same write is accepted when the webhook allows.
				refuse = false
				_, err = e.api.PutMachine(ctx(t), machine("m1"))
				assert.NoError(t, err)
			})
		}
	}
}

// fault is a way for a call to a webhook to fail. serve and answer shape the
// webhook; h shapes its registration.
type fault struct {
	serve  func(w http.ResponseWriter, r *http.Request, c call)
	answer func(c call) *api.AdmissionResponse
	h      func(t *testing.T, h *api.Webhook)
}

func faults() map[string]fault {
	badBody := func(status int, body string) fault {
		return fault{serve: func(w http.ResponseWriter, r *http.Request, c call) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}}
	}
	return map[string]fault{
		"timeout": {
			serve: func(w http.ResponseWriter, r *http.Request, c call) { <-r.Context().Done() },
			h: func(t *testing.T, h *api.Webhook) {
				one := int32(1)
				h.TimeoutSeconds = &one
			},
		},
		"an untrusted certificate": {
			h: func(t *testing.T, h *api.Webhook) { h.ClientConfig.CABundle = newAuthority(t, "other ca").pem },
		},
		// The body is a valid answer, which a reader that ignored the status
		// would use.
		"a status other than 200": {serve: func(w http.ResponseWriter, r *http.Request, c call) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(api.AdmissionReview{
				TypeMeta: api.TypeMeta{APIVersion: api.AdmissionGroupVersion, Kind: api.KindAdmissionReview},
				Response: patch(c, setImage),
			})
		}},
		"an undecodable body": badBody(http.StatusOK, "this is not json"),
		// The target of the redirect is a webhook that answers well, which a
		// client that followed the redirect would take.
		"a redirect": {serve: func(w http.ResponseWriter, r *http.Request, c call) {
			if r.URL.Path == "/target" {
				reply(w, patch(c, setImage))
				return
			}
			http.Redirect(w, r, "/target", http.StatusTemporaryRedirect)
		}},
		// A valid answer behind more whitespace than the server reads.
		"an answer over 16 MiB": {serve: func(w http.ResponseWriter, r *http.Request, c call) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(bytes.Repeat([]byte(" "), 17<<20))
			_ = json.NewEncoder(w).Encode(api.AdmissionReview{
				TypeMeta: api.TypeMeta{APIVersion: api.AdmissionGroupVersion, Kind: api.KindAdmissionReview},
				Response: patch(c, setImage),
			})
		}},
		"a wrong uid": {answer: func(c call) *api.AdmissionResponse {
			resp := patch(c, setImage)
			resp.UID = "someone-else"
			return resp
		}},
		"a bad patch": {answer: func(c call) *api.AdmissionResponse {
			return patch(c, `[{"op":"add","path":"/spec/image","value":"patched"},{"op":"remove","path":"/spec/nothing"}]`)
		}},
		"a malformed patch": {answer: func(c call) *api.AdmissionResponse { return patch(c, `this is not a patch`) }},
	}
}

func TestAdmission_AFailedCallIsInternalUnderFailAndSkippedUnderIgnore(t *testing.T) {
	cases := faults()
	stages := map[string]func(*env, *testing.T, api.Webhook){
		"mutating":   func(e *env, t *testing.T, h api.Webhook) { e.mutating(t, "cfg", h) },
		"validating": func(e *env, t *testing.T, h api.Webhook) { e.validating(t, "cfg", h) },
	}
	for stage, register := range stages {
		for name, f := range cases {
			t.Run(stage+"/"+name, func(t *testing.T) {
				for _, policy := range []api.FailurePolicy{api.FailurePolicyFail, api.FailurePolicyIgnore} {
					e := newEnv(t)
					w := newWebhookServer(t)
					w.serve, w.answer = f.serve, f.answer
					h := w.hook("a.hooks.example", "/", "machines")
					h.FailurePolicy = policy
					if f.h != nil {
						f.h(t, &h)
					}
					register(e, t, h)

					created, err := e.api.PutMachine(ctx(t), machine("m1"))

					if policy == api.FailurePolicyFail {
						assert.ErrorIs(t, err, api.ErrInternalError, "%s under Fail", name)
						_, err = e.api.GetMachine(ctx(t), "m1")
						assert.ErrorIs(t, err, api.ErrNotFound, "the write is not stored")
						continue
					}
					require.NoError(t, err, "%s under Ignore", name)
					assert.Equal(t, "alpine", created.Spec.Image, "a skipped webhook's patch is not applied")
				}
			})
		}
	}
}

func TestAdmission_AWebhookThatAnswersWithoutAFaultIsTheControl(t *testing.T) {
	e := newEnv(t)
	w := newWebhookServer(t)
	w.answer = func(c call) *api.AdmissionResponse { return patch(c, setImage) }
	e.mutating(t, "cfg", w.hook("a.hooks.example", "/", "machines"))

	created, err := e.api.PutMachine(ctx(t), machine("m1"))

	require.NoError(t, err)
	assert.Equal(t, "patched", created.Spec.Image)
}

func TestAdmission_AValidatingWebhookThatAnswersWithAPatchIsAFailedCall(t *testing.T) {
	for _, policy := range []api.FailurePolicy{api.FailurePolicyFail, api.FailurePolicyIgnore} {
		e := newEnv(t)
		w := newWebhookServer(t)
		w.answer = func(c call) *api.AdmissionResponse { return patch(c, setImage) }
		h := w.hook("a.hooks.example", "/", "machines")
		h.FailurePolicy = policy
		e.validating(t, "cfg", h)

		created, err := e.api.PutMachine(ctx(t), machine("m1"))

		if policy == api.FailurePolicyFail {
			assert.ErrorIs(t, err, api.ErrInternalError)
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, "alpine", created.Spec.Image)
	}
}

func TestAdmission_ADeleteIsSentWithTheOldObjectAndCanBeRefused(t *testing.T) {
	e := newEnv(t)
	created := e.createMachine(t, machine("m1"))
	w := newWebhookServer(t)
	refuse := true
	w.answer = func(c call) *api.AdmissionResponse {
		if refuse {
			return deny(c, 0, "keep it")
		}
		return allow(c)
	}
	e.validating(t, "cfg", w.hook("a.hooks.example", "/", "machines"))

	_, err := e.api.DeleteMachine(ctx(t), "m1", created.Metadata.ResourceVersion)

	require.ErrorIs(t, err, api.ErrForbidden)
	q := w.received()[0].review.Request
	assert.Equal(t, api.OperationDelete, q.Operation)
	assert.NotEmpty(t, q.OldObject)
	assert.Empty(t, q.Object)
	_, err = e.api.GetMachine(ctx(t), "m1")
	assert.NoError(t, err, "a refused delete leaves the object")

	// Control: the same delete goes through when the webhook allows.
	refuse = false
	_, err = e.api.DeleteMachine(ctx(t), "m1", created.Metadata.ResourceVersion)
	assert.NoError(t, err)
}

func TestAdmission_AMutatingPatchAnsweredToADeleteIsAFailedCall(t *testing.T) {
	e := newEnv(t)
	created := e.createMachine(t, machine("m1"))
	w := newWebhookServer(t)
	w.answer = func(c call) *api.AdmissionResponse { return patch(c, setImage) }
	e.mutating(t, "cfg", w.hook("a.hooks.example", "/", "machines"))

	_, err := e.api.DeleteMachine(ctx(t), "m1", created.Metadata.ResourceVersion)

	assert.ErrorIs(t, err, api.ErrInternalError, "a delete has no object to patch")
}

func TestAdmission_ADeleteOfAnObjectAlreadyBeingDeletedIsNotSent(t *testing.T) {
	e := newEnv(t)
	m := machine("m1")
	m.Metadata.Finalizers = []string{"example.com/hold"}
	bound := e.createMachine(t, m)
	w := newWebhookServer(t)
	e.validating(t, "cfg", w.hook("a.hooks.example", "/", "machines"))

	first, err := e.api.DeleteMachine(ctx(t), "m1", bound.Metadata.ResourceVersion)
	require.NoError(t, err)
	require.NotNil(t, first.Metadata.DeletionTimestamp, "a machine with a finalizer is marked for deletion first")
	require.Len(t, w.received(), 1)

	_, err = e.api.DeleteMachine(ctx(t), "m1", first.Metadata.ResourceVersion)
	require.NoError(t, err)
	assert.Len(t, w.received(), 1, "the repeat changes nothing and is not sent")
}

func TestAdmission_ACustomObjectIsSentWithItsGroupAndPlural(t *testing.T) {
	e := newEnv(t)
	e.define(t, widgetDefinition(t, false))
	w := newWebhookServer(t)
	w.answer = func(c call) *api.AdmissionResponse {
		return patch(c, `[{"op":"replace","path":"/spec/size","value":5}]`)
	}
	h := w.hook("a.hooks.example", "/", "widgets")
	h.Rules[0].APIGroups = []string{appsGroup}
	e.mutating(t, "cfg", h)
	e.validating(t, "other", func() api.Webhook {
		o := w.hook("b.hooks.example", "/", "widgets")
		o.Rules[0].APIGroups = []string{"other.gaon.garam.sh"}
		return o
	}())

	created, err := e.custom(t, widgetType).Put(ctx(t), widget("w1", `{"size":3}`))

	require.NoError(t, err)
	assert.JSONEq(t, `{"size":5}`, string(created.Spec), "the patch is applied to the custom object")
	require.Len(t, w.received(), 1, "a rule of another group does not match")
	q := w.received()[0].review.Request
	assert.Equal(t, api.GroupVersionKind{Group: appsGroup, Version: "v1alpha1", Kind: "Widget"}, q.Kind)
	assert.Equal(t, api.GroupVersionResource{Group: appsGroup, Version: "v1alpha1", Resource: "widgets"}, q.Resource)
	assert.Equal(t, api.OperationCreate, q.Operation)
}

func TestAdmission_ADefinitionWriteAndItsCascadedDeletesAreSent(t *testing.T) {
	e := newEnv(t)
	w := newWebhookServer(t)
	hook := w.hook("a.hooks.example", "/", "customresourcedefinitions", "widgets")
	e.validating(t, "cfg", hook)

	definition := e.define(t, widgetDefinition(t, false))
	require.Len(t, w.received(), 1)
	q := w.received()[0].review.Request
	assert.Equal(t, api.APIExtensionsGroup, q.Resource.Group)
	assert.Equal(t, "customresourcedefinitions", q.Resource.Resource)
	assert.Equal(t, api.OperationCreate, q.Operation)

	_, err := e.custom(t, widgetType).Put(ctx(t), widget("w1", `{"size":3}`))
	require.NoError(t, err)
	_, err = e.extensions().DeleteCustomResourceDefinition(ctx(t), definition.Metadata.Name, definition.Metadata.ResourceVersion)
	require.NoError(t, err)

	var deletes []string
	for _, c := range w.received() {
		if c.review.Request.Operation == api.OperationDelete {
			deletes = append(deletes, c.review.Request.Resource.Resource)
		}
	}
	assert.ElementsMatch(t, []string{"customresourcedefinitions", "widgets"}, deletes,
		"deleting a definition deletes its objects, each of which is sent")
}
