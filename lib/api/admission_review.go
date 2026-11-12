package api

import (
	"encoding/json"
	"slices"
)

// PatchType names the format of a mutating webhook's patch.
type PatchType string

// PatchTypeJSONPatch is the one patch format: a JSON Patch, RFC 6902.
const PatchTypeJSONPatch PatchType = "JSONPatch"

// AdmissionReview is the body of a call to an admission webhook and of its
// answer: a call carries Request, the answer Response. No path serves it. A
// webhook written in Go decodes the call into it and encodes its answer from
// it.
type AdmissionReview struct {
	TypeMeta
	Request  *AdmissionRequest  `json:"request,omitempty"`
	Response *AdmissionResponse `json:"response,omitempty"`
}

// GroupVersionKind names a kind.
type GroupVersionKind struct {
	Group   string `json:"group"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
}

// GroupVersionResource names a resource.
type GroupVersionResource struct {
	Group    string `json:"group"`
	Version  string `json:"version"`
	Resource string `json:"resource"`
}

// UserInfo is the identity the API server authenticated for the write.
type UserInfo struct {
	Username string   `json:"username"`
	Groups   []string `json:"groups,omitempty"`
}

// AdmissionRequest describes one write. Object is the new object, absent for
// DELETE; OldObject is the stored object, absent for CREATE. Both are JSON
// objects of the kind.
type AdmissionRequest struct {
	UID         string               `json:"uid"`
	Kind        GroupVersionKind     `json:"kind"`
	Resource    GroupVersionResource `json:"resource"`
	SubResource string               `json:"subResource,omitempty"`
	Name        string               `json:"name"`
	Operation   Operation            `json:"operation"`
	UserInfo    UserInfo             `json:"userInfo"`
	Object      json.RawMessage      `json:"object,omitempty"`
	OldObject   json.RawMessage      `json:"oldObject,omitempty"`
}

// AdmissionResponse is a webhook's decision on the request whose UID it
// copies. A mutating webhook that allows may add Patch, a JSON Patch the API
// server applies. The API server answers a refusal as Forbidden whatever
// Status.Code says, and carries Status.Message.
type AdmissionResponse struct {
	UID       string           `json:"uid"`
	Allowed   bool             `json:"allowed"`
	Status    *AdmissionStatus `json:"status,omitempty"`
	PatchType PatchType        `json:"patchType,omitempty"`
	Patch     []byte           `json:"patch,omitempty"`
}

// AdmissionStatus is the reason a webhook gives for its decision.
type AdmissionStatus struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// Validate checks the identity of r, that it carries a request or a response
// and not both, and the rules of the one it carries.
func (r *AdmissionReview) Validate() error {
	if r.APIVersion != AdmissionGroupVersion {
		return invalid("apiVersion is %q, want %q", r.APIVersion, AdmissionGroupVersion)
	}
	if r.Kind != KindAdmissionReview {
		return invalid("kind is %q, want %q", r.Kind, KindAdmissionReview)
	}
	switch {
	case r.Request != nil && r.Response != nil:
		return invalid("an AdmissionReview carries a request or a response, not both")
	case r.Request != nil:
		return r.Request.validate()
	case r.Response != nil:
		return r.Response.validate()
	default:
		return invalid("an AdmissionReview carries a request or a response")
	}
}

// ValidateResponseTo checks that r is a valid response to the request with
// uid: it is a response, and it copies the uid.
func (r *AdmissionReview) ValidateResponseTo(uid string) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r.Response == nil {
		return invalid("the AdmissionReview carries no response")
	}
	if r.Response.UID != uid {
		return invalid("response.uid is %q, want the request's %q", r.Response.UID, uid)
	}
	return nil
}

func (q *AdmissionRequest) validate() error {
	if q.UID == "" {
		return invalid("request.uid is empty")
	}
	if q.Kind.Group == "" || q.Kind.Version == "" || q.Kind.Kind == "" {
		return invalid("request.kind names no group, version and kind")
	}
	if q.Resource.Group == "" || q.Resource.Version == "" || q.Resource.Resource == "" {
		return invalid("request.resource names no group, version and resource")
	}
	if q.Name == "" {
		return invalid("request.name is empty")
	}
	hasObject, hasOld := len(q.Object) > 0, len(q.OldObject) > 0
	switch q.Operation {
	case OperationCreate:
		if !hasObject || hasOld {
			return invalid("a CREATE request carries object and no oldObject")
		}
	case OperationUpdate:
		if !hasObject || !hasOld {
			return invalid("an UPDATE request carries object and oldObject")
		}
	case OperationDelete:
		if hasObject || !hasOld {
			return invalid("a DELETE request carries oldObject and no object")
		}
	default:
		return invalid("request.operation is %q, want CREATE, UPDATE or DELETE", q.Operation)
	}
	return nil
}

var patchOps = []string{"add", "remove", "replace", "move", "copy", "test"}

func (p *AdmissionResponse) validate() error {
	if p.UID == "" {
		return invalid("response.uid is empty")
	}
	if p.PatchType == "" && len(p.Patch) == 0 {
		return nil
	}
	if p.PatchType != PatchTypeJSONPatch || len(p.Patch) == 0 {
		return invalid("response.patchType and response.patch are given together, and the patchType is JSONPatch")
	}
	if !p.Allowed {
		return invalid("a response that refuses carries no patch")
	}
	var ops []struct {
		Op   string  `json:"op"`
		Path *string `json:"path"`
	}
	if err := json.Unmarshal(p.Patch, &ops); err != nil {
		return invalid("response.patch is not a JSON array of patch operations: %v", err)
	}
	for i, op := range ops {
		if !slices.Contains(patchOps, op.Op) || op.Path == nil {
			return invalid("response.patch[%d] has op %q and no usable path", i, op.Op)
		}
	}
	return nil
}
