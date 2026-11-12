package api_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newAdmissionRequest(op api.Operation) *api.AdmissionRequest {
	q := &api.AdmissionRequest{
		UID:       "705ab4f5-6393-11e8-b7cc-42010a800002",
		Kind:      api.GroupVersionKind{Group: api.Group, Version: api.Version, Kind: "MicroVM"},
		Resource:  api.GroupVersionResource{Group: api.Group, Version: api.Version, Resource: "microvms"},
		Name:      "m1",
		Operation: op,
		UserInfo:  api.UserInfo{Username: "alice", Groups: []string{"dev"}},
	}
	switch op {
	case api.OperationCreate:
		q.Object = json.RawMessage(`{"kind":"MicroVM"}`)
	case api.OperationUpdate:
		q.Object = json.RawMessage(`{"kind":"MicroVM"}`)
		q.OldObject = json.RawMessage(`{"kind":"MicroVM"}`)
	case api.OperationDelete:
		q.OldObject = json.RawMessage(`{"kind":"MicroVM"}`)
	}
	return q
}

func newReviewOf(q *api.AdmissionRequest) *api.AdmissionReview {
	return &api.AdmissionReview{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionGroupVersion, Kind: api.KindAdmissionReview},
		Request:  q,
	}
}

func newReviewResponse() *api.AdmissionReview {
	return &api.AdmissionReview{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionGroupVersion, Kind: api.KindAdmissionReview},
		Response: &api.AdmissionResponse{UID: "u1", Allowed: true},
	}
}

func TestAdmissionReviewValidateChecksIdentityAndSides(t *testing.T) {
	require.NoError(t, newReviewOf(newAdmissionRequest(api.OperationCreate)).Validate(), "control: a request")
	require.NoError(t, newReviewResponse().Validate(), "control: a response")

	r := newReviewOf(newAdmissionRequest(api.OperationCreate))
	r.APIVersion = api.GroupVersion
	assert.ErrorIs(t, r.Validate(), api.ErrInvalid)
	r = newReviewOf(newAdmissionRequest(api.OperationCreate))
	r.Kind = "Other"
	assert.ErrorIs(t, r.Validate(), api.ErrInvalid)

	both := newReviewOf(newAdmissionRequest(api.OperationCreate))
	both.Response = &api.AdmissionResponse{UID: "u1"}
	assert.ErrorContains(t, both.Validate(), "not both")
	empty := newReviewResponse()
	empty.Response = nil
	assert.ErrorContains(t, empty.Validate(), "carries a request or a response")
}

func TestAdmissionRequestValidateRefusesWhatItCannotDescribe(t *testing.T) {
	require.NoError(t, newReviewOf(newAdmissionRequest(api.OperationCreate)).Validate(), "control")
	for name, edit := range map[string]func(*api.AdmissionRequest){
		"no uid":              func(q *api.AdmissionRequest) { q.UID = "" },
		"no kind group":       func(q *api.AdmissionRequest) { q.Kind.Group = "" },
		"no kind version":     func(q *api.AdmissionRequest) { q.Kind.Version = "" },
		"no kind kind":        func(q *api.AdmissionRequest) { q.Kind.Kind = "" },
		"no resource group":   func(q *api.AdmissionRequest) { q.Resource.Group = "" },
		"no resource version": func(q *api.AdmissionRequest) { q.Resource.Version = "" },
		"no resource":         func(q *api.AdmissionRequest) { q.Resource.Resource = "" },
		"no name":             func(q *api.AdmissionRequest) { q.Name = "" },
		"no operation":        func(q *api.AdmissionRequest) { q.Operation = "" },
		"the wildcard":        func(q *api.AdmissionRequest) { q.Operation = api.OperationAll },
	} {
		q := newAdmissionRequest(api.OperationCreate)
		edit(q)
		assert.ErrorIs(t, newReviewOf(q).Validate(), api.ErrInvalid, name)
	}
}

func TestAdmissionRequestValidateChecksObjectsPerOperation(t *testing.T) {
	obj := json.RawMessage(`{"kind":"MicroVM"}`)
	cases := []struct {
		op          api.Operation
		object, old json.RawMessage
		ok          bool
	}{
		{api.OperationCreate, obj, nil, true},
		{api.OperationCreate, nil, nil, false},
		{api.OperationCreate, obj, obj, false},
		{api.OperationUpdate, obj, obj, true},
		{api.OperationUpdate, obj, nil, false},
		{api.OperationUpdate, nil, obj, false},
		{api.OperationDelete, nil, obj, true},
		{api.OperationDelete, obj, obj, false},
		{api.OperationDelete, nil, nil, false},
	}
	for _, c := range cases {
		q := newAdmissionRequest(c.op)
		q.Object, q.OldObject = c.object, c.old
		err := newReviewOf(q).Validate()
		if c.ok {
			assert.NoError(t, err, "%s object=%v old=%v", c.op, c.object != nil, c.old != nil)
		} else {
			assert.ErrorIs(t, err, api.ErrInvalid, "%s object=%v old=%v", c.op, c.object != nil, c.old != nil)
		}
	}
}

func TestAdmissionResponseValidateChecksThePatch(t *testing.T) {
	patch := []byte(`[{"op":"add","path":"/metadata/labels/x","value":"y"}]`)
	with := func(edit func(*api.AdmissionResponse)) error {
		r := newReviewResponse()
		r.Response.PatchType, r.Response.Patch = api.PatchTypeJSONPatch, patch
		edit(r.Response)
		return r.Validate()
	}
	require.NoError(t, with(func(*api.AdmissionResponse) {}), "control: allowed with a JSONPatch")
	require.NoError(t, newReviewResponse().Validate(), "control: allowed with no patch")

	for name, edit := range map[string]func(*api.AdmissionResponse){
		"no uid":               func(p *api.AdmissionResponse) { p.UID = "" },
		"a patch without type": func(p *api.AdmissionResponse) { p.PatchType = "" },
		"a type without patch": func(p *api.AdmissionResponse) { p.Patch = nil },
		"another patch type":   func(p *api.AdmissionResponse) { p.PatchType = "StrategicMerge" },
		"a patch on a refusal": func(p *api.AdmissionResponse) { p.Allowed = false },
		"a patch not an array": func(p *api.AdmissionResponse) { p.Patch = []byte(`{"op":"add"}`) },
		"a patch not JSON":     func(p *api.AdmissionResponse) { p.Patch = []byte(`nope`) },
		"an unknown op":        func(p *api.AdmissionResponse) { p.Patch = []byte(`[{"op":"merge","path":"/a"}]`) },
		"an op without a path": func(p *api.AdmissionResponse) { p.Patch = []byte(`[{"op":"remove"}]`) },
		"a second bad op": func(p *api.AdmissionResponse) {
			p.Patch = []byte(`[{"op":"add","path":"/a"},{"op":"x","path":"/b"}]`)
		},
	} {
		assert.ErrorIs(t, with(edit), api.ErrInvalid, name)
	}

	assert.NoError(t, with(func(p *api.AdmissionResponse) { p.Patch = []byte(`[]`) }), "an empty patch is a patch of no operations")
	assert.NoError(t, with(func(p *api.AdmissionResponse) { p.Patch = []byte(`[{"op":"add","path":""}]`) }), "an empty path addresses the document")
}

func TestAdmissionReviewValidateResponseTo(t *testing.T) {
	r := newReviewResponse()
	require.NoError(t, r.ValidateResponseTo("u1"), "control: the uid is copied")
	assert.ErrorContains(t, r.ValidateResponseTo("u2"), "response.uid")

	assert.ErrorContains(t, newReviewOf(newAdmissionRequest(api.OperationCreate)).ValidateResponseTo("u1"), "no response")
	bad := newReviewResponse()
	bad.APIVersion = "x"
	assert.ErrorIs(t, bad.ValidateResponseTo("u1"), api.ErrInvalid)
}

func TestAdmissionReviewJSON(t *testing.T) {
	r := newReviewResponse()
	r.Response.PatchType = api.PatchTypeJSONPatch
	r.Response.Patch = []byte(`[{"op":"add","path":"/a","value":1}]`)
	r.Response.Status = &api.AdmissionStatus{Code: 403, Message: "no"}
	raw, err := json.Marshal(r)
	require.NoError(t, err)

	var fields struct {
		Response struct {
			Patch     string `json:"patch"`
			PatchType string `json:"patchType"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(raw, &fields))
	assert.Equal(t, "JSONPatch", fields.Response.PatchType)
	assert.Equal(t, "W3sib3AiOiJhZGQiLCJwYXRoIjoiL2EiLCJ2YWx1ZSI6MX1d", fields.Response.Patch, "the patch is base64")

	var back api.AdmissionReview
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, r, &back)

	req := newReviewOf(newAdmissionRequest(api.OperationUpdate))
	raw, err = json.Marshal(req)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"object":{"kind":"MicroVM"}`, "object is embedded JSON, not a string")
	assert.NotContains(t, string(raw), `"response"`)
}
