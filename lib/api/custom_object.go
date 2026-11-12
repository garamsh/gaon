package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// CustomResourceType names a kind a CustomResourceDefinition registered: the
// group and version of its root, its kind and its plural, the path segment.
type CustomResourceType struct {
	Group   string
	Version string
	Kind    string
	Plural  string
}

// CustomObject is an object of a registered kind, held without a Go type per
// kind. Spec and Status are JSON objects the kind's schema describes.
type CustomObject struct {
	TypeMeta
	Metadata ObjectMeta      `json:"metadata"`
	Spec     json.RawMessage `json:"spec,omitempty"`
	Status   json.RawMessage `json:"status,omitempty"`
}

// CustomObjectList is the answer to listing the objects of a kind.
type CustomObjectList struct {
	TypeMeta
	Metadata ListMeta       `json:"metadata"`
	Items    []CustomObject `json:"items"`
}

// Validate checks that o is an object of type t with a name, and that its
// spec and status, when present, are JSON objects. The schema of the kind
// decides the rest.
func (o *CustomObject) Validate(t CustomResourceType) error {
	if err := validateIdentity(o.TypeMeta, t.Group+"/"+t.Version, t.Kind, o.Metadata); err != nil {
		return err
	}
	for field, raw := range map[string]json.RawMessage{"spec": o.Spec, "status": o.Status} {
		if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] != '{' {
			return invalid("%s is not a JSON object", field)
		}
	}
	return nil
}

// CustomResourceClient calls the API of one registered kind, under
// /apis/<group>/<version>. The same compare-and-swap, watch and error shapes
// hold as for the compute kinds.
type CustomResourceClient struct {
	c *Client
	t CustomResourceType
}

// NewCustomResourceClient returns a client for the kind t on the API server at
// server, an address such as https://host:6443. It refuses a type with an empty
// field, and takes the options of NewClient.
func NewCustomResourceClient(server string, httpClient *http.Client, t CustomResourceType, opts ...ClientOption) (*CustomResourceClient, error) {
	if t.Group == "" || t.Version == "" || t.Kind == "" || t.Plural == "" {
		return nil, fmt.Errorf("custom resource type %+v has an empty field", t)
	}
	root := strings.TrimSuffix(server, "/") + "/apis/" + url.PathEscape(t.Group) + "/" + url.PathEscape(t.Version)
	return &CustomResourceClient{c: NewClient(root, httpClient, opts...), t: t}, nil
}

func (r *CustomResourceClient) path(name string) string {
	return "/" + url.PathEscape(r.t.Plural) + "/" + url.PathEscape(name)
}

// Put creates o, or changes it when o carries the ResourceVersion it was read
// at (PUT /<plural>/{name}).
func (r *CustomResourceClient) Put(ctx context.Context, o *CustomObject) (*CustomObject, error) {
	return put(ctx, r.c, r.path(o.Metadata.Name), o)
}

// PutStatus writes o's status (PUT /<plural>/{name}/status). The path exists
// only when the definition enables the status subresource.
func (r *CustomResourceClient) PutStatus(ctx context.Context, o *CustomObject) (*CustomObject, error) {
	return put(ctx, r.c, r.path(o.Metadata.Name)+"/status", o)
}

// Delete sets the deletionTimestamp of the named object, if it is still at
// version rv (DELETE /<plural>/{name}).
func (r *CustomResourceClient) Delete(ctx context.Context, name string, rv ResourceVersion) (*CustomObject, error) {
	if rv == "" {
		return nil, errors.New("delete " + r.t.Plural + ": resource version is empty")
	}
	return call[CustomObject](ctx, r.c, http.MethodDelete, r.path(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// Get returns the named object (GET /<plural>/{name}).
func (r *CustomResourceClient) Get(ctx context.Context, name string) (*CustomObject, error) {
	return call[CustomObject](ctx, r.c, http.MethodGet, r.path(name), nil, nil)
}

// List returns every object of the kind and the version to watch from
// (GET /<plural>).
func (r *CustomResourceClient) List(ctx context.Context) (*CustomObjectList, error) {
	return call[CustomObjectList](ctx, r.c, http.MethodGet, "/"+url.PathEscape(r.t.Plural), nil, nil)
}

// Watch streams the changes made after version rv (GET /<plural>?watch=true),
// as WatchMachines does.
func (r *CustomResourceClient) Watch(ctx context.Context, rv ResourceVersion) (*Watch[CustomObject], error) {
	return watch[CustomObject](ctx, r.c, "/"+url.PathEscape(r.t.Plural), r.t.Kind, rv)
}
