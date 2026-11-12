package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxBody = 1 << 20

// Client calls the resource API. Every write is a compare-and-swap on the
// ResourceVersion the object or request carries; an empty one creates.
type Client struct {
	root  string
	http  *http.Client
	token string
}

// ClientOption changes how a Client sends its requests.
type ClientOption func(*Client)

// WithBearerToken sends token as the Authorization: Bearer header of every
// request. The token is a credential, so the Client refuses to send a request
// whose URL is not https. The option adds to the http client's own TLS
// identity and takes none away: a client that presents a certificate keeps
// presenting it.
func WithBearerToken(token string) ClientOption {
	return func(c *Client) { c.token = token }
}

// NewClient returns a Client for the API whose group and version root is
// root. A nil httpClient means http.DefaultClient. A watch is one long
// response, so httpClient must not set a Timeout.
func NewClient(root string, httpClient *http.Client, opts ...ClientOption) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	c := &Client{root: strings.TrimSuffix(root, "/"), http: httpClient}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// PutMachine creates m, or changes its spec when m carries the ResourceVersion
// it was read at (PUT /machines/{name}).
func (c *Client) PutMachine(ctx context.Context, m *Machine) (*Machine, error) {
	return put(ctx, c, "/machines/"+url.PathEscape(m.Metadata.Name), m)
}

// BindMachine binds a Machine to a node and sets its epoch
// (POST /machines/{name}/binding).
func (c *Client) BindMachine(ctx context.Context, name string, b Binding) (*Machine, error) {
	return call[Machine](ctx, c, http.MethodPost, "/machines/"+url.PathEscape(name)+"/binding", nil, b)
}

// PutMachineStatus writes m's status (PUT /machines/{name}/status).
func (c *Client) PutMachineStatus(ctx context.Context, m *Machine) (*Machine, error) {
	return put(ctx, c, "/machines/"+url.PathEscape(m.Metadata.Name)+"/status", m)
}

// DeleteMachine sets the deletionTimestamp of a Machine, if it is still at
// version rv (DELETE /machines/{name}).
func (c *Client) DeleteMachine(ctx context.Context, name string, rv ResourceVersion) (*Machine, error) {
	if rv == "" {
		return nil, errors.New("delete machine: resource version is empty")
	}
	return call[Machine](ctx, c, http.MethodDelete, "/machines/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetMachine returns the named Machine (GET /machines/{name}).
func (c *Client) GetMachine(ctx context.Context, name string) (*Machine, error) {
	return call[Machine](ctx, c, http.MethodGet, "/machines/"+url.PathEscape(name), nil, nil)
}

// ListMachines returns every Machine and the version to watch from
// (GET /machines).
func (c *Client) ListMachines(ctx context.Context) (*MachineList, error) {
	return call[MachineList](ctx, c, http.MethodGet, "/machines", nil, nil)
}

// WatchMachines streams the changes made after version rv
// (GET /machines?watch=true). When rv has expired, Next returns ErrExpired:
// list again and watch from the list's version.
func (c *Client) WatchMachines(ctx context.Context, rv ResourceVersion) (*Watch[Machine], error) {
	return watch[Machine](ctx, c, "/machines", KindMachine, rv)
}

// PutNode creates n, or changes its spec when n carries the ResourceVersion
// it was read at (PUT /nodes/{name}).
func (c *Client) PutNode(ctx context.Context, n *Node) (*Node, error) {
	return put(ctx, c, "/nodes/"+url.PathEscape(n.Metadata.Name), n)
}

// PutNodeStatus writes n's status (PUT /nodes/{name}/status).
func (c *Client) PutNodeStatus(ctx context.Context, n *Node) (*Node, error) {
	return put(ctx, c, "/nodes/"+url.PathEscape(n.Metadata.Name)+"/status", n)
}

// GetNode returns the named Node (GET /nodes/{name}).
func (c *Client) GetNode(ctx context.Context, name string) (*Node, error) {
	return call[Node](ctx, c, http.MethodGet, "/nodes/"+url.PathEscape(name), nil, nil)
}

// ListNodes returns every Node and the version to watch from (GET /nodes).
func (c *Client) ListNodes(ctx context.Context) (*NodeList, error) {
	return call[NodeList](ctx, c, http.MethodGet, "/nodes", nil, nil)
}

// WatchNodes streams the changes made after version rv
// (GET /nodes?watch=true), as WatchMachines does.
func (c *Client) WatchNodes(ctx context.Context, rv ResourceVersion) (*Watch[Node], error) {
	return watch[Node](ctx, c, "/nodes", KindNode, rv)
}

// GetLease returns the named Lease (GET /leases/{name}).
func (c *Client) GetLease(ctx context.Context, name string) (*Lease, error) {
	return call[Lease](ctx, c, http.MethodGet, "/leases/"+url.PathEscape(name), nil, nil)
}

// ListLeases returns every Lease and the version to watch from (GET /leases).
func (c *Client) ListLeases(ctx context.Context) (*LeaseList, error) {
	return call[LeaseList](ctx, c, http.MethodGet, "/leases", nil, nil)
}

// WatchLeases streams the changes made after version rv
// (GET /leases?watch=true), as WatchMachines does.
func (c *Client) WatchLeases(ctx context.Context, rv ResourceVersion) (*Watch[Lease], error) {
	return watch[Lease](ctx, c, "/leases", KindLease, rv)
}

// PutVolumeStatus writes v's status (PUT /volumes/{name}/status).
func (c *Client) PutVolumeStatus(ctx context.Context, v *Volume) (*Volume, error) {
	return put(ctx, c, "/volumes/"+url.PathEscape(v.Metadata.Name)+"/status", v)
}

// PutLease creates l, or renews it when l carries the ResourceVersion it was
// read at (PUT /leases/{name}).
func (c *Client) PutLease(ctx context.Context, l *Lease) (*Lease, error) {
	return put(ctx, c, "/leases/"+url.PathEscape(l.Metadata.Name), l)
}

// PutCertificateSigningRequest creates r (PUT /certificatesigningrequests/{name}).
// The spec of a request is immutable, so r carries no ResourceVersion. The API
// server sets spec.username.
func (c *Client) PutCertificateSigningRequest(ctx context.Context, r *CertificateSigningRequest) (*CertificateSigningRequest, error) {
	return put(ctx, c, "/certificatesigningrequests/"+url.PathEscape(r.Metadata.Name), r)
}

// PutCertificateSigningRequestStatus writes r's status, the signer's answer
// (PUT /certificatesigningrequests/{name}/status).
func (c *Client) PutCertificateSigningRequestStatus(ctx context.Context, r *CertificateSigningRequest) (*CertificateSigningRequest, error) {
	return put(ctx, c, "/certificatesigningrequests/"+url.PathEscape(r.Metadata.Name)+"/status", r)
}

// GetCertificateSigningRequest returns the named request
// (GET /certificatesigningrequests/{name}).
func (c *Client) GetCertificateSigningRequest(ctx context.Context, name string) (*CertificateSigningRequest, error) {
	return call[CertificateSigningRequest](ctx, c, http.MethodGet, "/certificatesigningrequests/"+url.PathEscape(name), nil, nil)
}

// ListCertificateSigningRequests returns every request and the version to
// watch from (GET /certificatesigningrequests).
func (c *Client) ListCertificateSigningRequests(ctx context.Context) (*CertificateSigningRequestList, error) {
	return call[CertificateSigningRequestList](ctx, c, http.MethodGet, "/certificatesigningrequests", nil, nil)
}

// WatchCertificateSigningRequests streams the changes made after version rv
// (GET /certificatesigningrequests?watch=true), as WatchMachines does.
func (c *Client) WatchCertificateSigningRequests(ctx context.Context, rv ResourceVersion) (*Watch[CertificateSigningRequest], error) {
	return watch[CertificateSigningRequest](ctx, c, "/certificatesigningrequests", KindCertificateSigningRequest, rv)
}

// PutClusterRole creates o, or changes it when o carries the ResourceVersion it was
// read at (PUT /clusterroles/{name}). Call it on a client whose root is the RBAC
// group's.
func (c *Client) PutClusterRole(ctx context.Context, o *ClusterRole) (*ClusterRole, error) {
	return put(ctx, c, "/clusterroles/"+url.PathEscape(o.Metadata.Name), o)
}

// DeleteClusterRole removes the named ClusterRole, if it is still at version rv
// (DELETE /clusterroles/{name}).
func (c *Client) DeleteClusterRole(ctx context.Context, name string, rv ResourceVersion) (*ClusterRole, error) {
	if rv == "" {
		return nil, errors.New("delete clusterroles: resource version is empty")
	}
	return call[ClusterRole](ctx, c, http.MethodDelete, "/clusterroles/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetClusterRole returns the named ClusterRole (GET /clusterroles/{name}).
func (c *Client) GetClusterRole(ctx context.Context, name string) (*ClusterRole, error) {
	return call[ClusterRole](ctx, c, http.MethodGet, "/clusterroles/"+url.PathEscape(name), nil, nil)
}

// ListClusterRoles returns every ClusterRole and the version to watch from
// (GET /clusterroles).
func (c *Client) ListClusterRoles(ctx context.Context) (*ClusterRoleList, error) {
	return call[ClusterRoleList](ctx, c, http.MethodGet, "/clusterroles", nil, nil)
}

// WatchClusterRoles streams the changes made after version rv
// (GET /clusterroles?watch=true), as WatchMachines does.
func (c *Client) WatchClusterRoles(ctx context.Context, rv ResourceVersion) (*Watch[ClusterRole], error) {
	return watch[ClusterRole](ctx, c, "/clusterroles", KindClusterRole, rv)
}

// PutClusterRoleBinding creates o, or changes it when o carries the ResourceVersion it was
// read at (PUT /clusterrolebindings/{name}). Call it on a client whose root is the RBAC
// group's.
func (c *Client) PutClusterRoleBinding(ctx context.Context, o *ClusterRoleBinding) (*ClusterRoleBinding, error) {
	return put(ctx, c, "/clusterrolebindings/"+url.PathEscape(o.Metadata.Name), o)
}

// DeleteClusterRoleBinding removes the named ClusterRoleBinding, if it is still at version rv
// (DELETE /clusterrolebindings/{name}).
func (c *Client) DeleteClusterRoleBinding(ctx context.Context, name string, rv ResourceVersion) (*ClusterRoleBinding, error) {
	if rv == "" {
		return nil, errors.New("delete clusterrolebindings: resource version is empty")
	}
	return call[ClusterRoleBinding](ctx, c, http.MethodDelete, "/clusterrolebindings/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetClusterRoleBinding returns the named ClusterRoleBinding (GET /clusterrolebindings/{name}).
func (c *Client) GetClusterRoleBinding(ctx context.Context, name string) (*ClusterRoleBinding, error) {
	return call[ClusterRoleBinding](ctx, c, http.MethodGet, "/clusterrolebindings/"+url.PathEscape(name), nil, nil)
}

// ListClusterRoleBindings returns every ClusterRoleBinding and the version to watch from
// (GET /clusterrolebindings).
func (c *Client) ListClusterRoleBindings(ctx context.Context) (*ClusterRoleBindingList, error) {
	return call[ClusterRoleBindingList](ctx, c, http.MethodGet, "/clusterrolebindings", nil, nil)
}

// WatchClusterRoleBindings streams the changes made after version rv
// (GET /clusterrolebindings?watch=true), as WatchMachines does.
func (c *Client) WatchClusterRoleBindings(ctx context.Context, rv ResourceVersion) (*Watch[ClusterRoleBinding], error) {
	return watch[ClusterRoleBinding](ctx, c, "/clusterrolebindings", KindClusterRoleBinding, rv)
}

// PutCustomResourceDefinition creates o, or changes it when o carries the
// ResourceVersion it was read at (PUT /customresourcedefinitions/{name}). Call
// it on a client whose root is the apiextensions group's. The API server
// writes the status.
func (c *Client) PutCustomResourceDefinition(ctx context.Context, o *CustomResourceDefinition) (*CustomResourceDefinition, error) {
	return put(ctx, c, "/customresourcedefinitions/"+url.PathEscape(o.Metadata.Name), o)
}

// DeleteCustomResourceDefinition sets the deletionTimestamp of the named
// definition, if it is still at version rv
// (DELETE /customresourcedefinitions/{name}). The API server then deletes the
// definition's objects.
func (c *Client) DeleteCustomResourceDefinition(ctx context.Context, name string, rv ResourceVersion) (*CustomResourceDefinition, error) {
	if rv == "" {
		return nil, errors.New("delete customresourcedefinitions: resource version is empty")
	}
	return call[CustomResourceDefinition](ctx, c, http.MethodDelete, "/customresourcedefinitions/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetCustomResourceDefinition returns the named definition
// (GET /customresourcedefinitions/{name}).
func (c *Client) GetCustomResourceDefinition(ctx context.Context, name string) (*CustomResourceDefinition, error) {
	return call[CustomResourceDefinition](ctx, c, http.MethodGet, "/customresourcedefinitions/"+url.PathEscape(name), nil, nil)
}

// ListCustomResourceDefinitions returns every definition and the version to
// watch from (GET /customresourcedefinitions).
func (c *Client) ListCustomResourceDefinitions(ctx context.Context) (*CustomResourceDefinitionList, error) {
	return call[CustomResourceDefinitionList](ctx, c, http.MethodGet, "/customresourcedefinitions", nil, nil)
}

// WatchCustomResourceDefinitions streams the changes made after version rv
// (GET /customresourcedefinitions?watch=true), as WatchMachines does.
func (c *Client) WatchCustomResourceDefinitions(ctx context.Context, rv ResourceVersion) (*Watch[CustomResourceDefinition], error) {
	return watch[CustomResourceDefinition](ctx, c, "/customresourcedefinitions", KindCustomResourceDefinition, rv)
}

// PutMutatingWebhookConfiguration creates o, or changes it when o carries the ResourceVersion it was
// read at (PUT /mutatingwebhookconfigurations/{name}). Call it on a client whose root is the
// admissionregistration group's.
func (c *Client) PutMutatingWebhookConfiguration(ctx context.Context, o *MutatingWebhookConfiguration) (*MutatingWebhookConfiguration, error) {
	return put(ctx, c, "/mutatingwebhookconfigurations/"+url.PathEscape(o.Metadata.Name), o)
}

// DeleteMutatingWebhookConfiguration removes the named MutatingWebhookConfiguration, if it is still at version rv
// (DELETE /mutatingwebhookconfigurations/{name}).
func (c *Client) DeleteMutatingWebhookConfiguration(ctx context.Context, name string, rv ResourceVersion) (*MutatingWebhookConfiguration, error) {
	if rv == "" {
		return nil, errors.New("delete mutatingwebhookconfigurations: resource version is empty")
	}
	return call[MutatingWebhookConfiguration](ctx, c, http.MethodDelete, "/mutatingwebhookconfigurations/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetMutatingWebhookConfiguration returns the named MutatingWebhookConfiguration (GET /mutatingwebhookconfigurations/{name}).
func (c *Client) GetMutatingWebhookConfiguration(ctx context.Context, name string) (*MutatingWebhookConfiguration, error) {
	return call[MutatingWebhookConfiguration](ctx, c, http.MethodGet, "/mutatingwebhookconfigurations/"+url.PathEscape(name), nil, nil)
}

// ListMutatingWebhookConfigurations returns every MutatingWebhookConfiguration and the version to watch from
// (GET /mutatingwebhookconfigurations).
func (c *Client) ListMutatingWebhookConfigurations(ctx context.Context) (*MutatingWebhookConfigurationList, error) {
	return call[MutatingWebhookConfigurationList](ctx, c, http.MethodGet, "/mutatingwebhookconfigurations", nil, nil)
}

// WatchMutatingWebhookConfigurations streams the changes made after version rv
// (GET /mutatingwebhookconfigurations?watch=true), as WatchMachines does.
func (c *Client) WatchMutatingWebhookConfigurations(ctx context.Context, rv ResourceVersion) (*Watch[MutatingWebhookConfiguration], error) {
	return watch[MutatingWebhookConfiguration](ctx, c, "/mutatingwebhookconfigurations", KindMutatingWebhookConfiguration, rv)
}

// PutValidatingWebhookConfiguration creates o, or changes it when o carries the ResourceVersion it was
// read at (PUT /validatingwebhookconfigurations/{name}). Call it on a client whose root is the
// admissionregistration group's.
func (c *Client) PutValidatingWebhookConfiguration(ctx context.Context, o *ValidatingWebhookConfiguration) (*ValidatingWebhookConfiguration, error) {
	return put(ctx, c, "/validatingwebhookconfigurations/"+url.PathEscape(o.Metadata.Name), o)
}

// DeleteValidatingWebhookConfiguration removes the named ValidatingWebhookConfiguration, if it is still at version rv
// (DELETE /validatingwebhookconfigurations/{name}).
func (c *Client) DeleteValidatingWebhookConfiguration(ctx context.Context, name string, rv ResourceVersion) (*ValidatingWebhookConfiguration, error) {
	if rv == "" {
		return nil, errors.New("delete validatingwebhookconfigurations: resource version is empty")
	}
	return call[ValidatingWebhookConfiguration](ctx, c, http.MethodDelete, "/validatingwebhookconfigurations/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetValidatingWebhookConfiguration returns the named ValidatingWebhookConfiguration (GET /validatingwebhookconfigurations/{name}).
func (c *Client) GetValidatingWebhookConfiguration(ctx context.Context, name string) (*ValidatingWebhookConfiguration, error) {
	return call[ValidatingWebhookConfiguration](ctx, c, http.MethodGet, "/validatingwebhookconfigurations/"+url.PathEscape(name), nil, nil)
}

// ListValidatingWebhookConfigurations returns every ValidatingWebhookConfiguration and the version to watch from
// (GET /validatingwebhookconfigurations).
func (c *Client) ListValidatingWebhookConfigurations(ctx context.Context) (*ValidatingWebhookConfigurationList, error) {
	return call[ValidatingWebhookConfigurationList](ctx, c, http.MethodGet, "/validatingwebhookconfigurations", nil, nil)
}

// WatchValidatingWebhookConfigurations streams the changes made after version rv
// (GET /validatingwebhookconfigurations?watch=true), as WatchMachines does.
func (c *Client) WatchValidatingWebhookConfigurations(ctx context.Context, rv ResourceVersion) (*Watch[ValidatingWebhookConfiguration], error) {
	return watch[ValidatingWebhookConfiguration](ctx, c, "/validatingwebhookconfigurations", KindValidatingWebhookConfiguration, rv)
}

func watch[T any](ctx context.Context, c *Client, path, kind string, rv ResourceVersion) (*Watch[T], error) {
	if rv == "" {
		return nil, fmt.Errorf("watch %s: resource version is empty", path)
	}
	query := url.Values{"watch": {"true"}, "resourceVersion": {string(rv)}}
	resp, err := c.send(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, decodeError(resp)
	}
	return newWatch[T](kind, resp.Body), nil
}

func put[T any](ctx context.Context, c *Client, path string, in *T) (*T, error) {
	return call[T](ctx, c, http.MethodPut, path, nil, in)
}

func call[T any](ctx context.Context, c *Client, method, path string, query url.Values, in any) (*T, error) {
	var out T
	if err := c.do(ctx, method, path, query, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// do sends one request and decodes a successful JSON answer into out.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return fmt.Errorf("%s %s: encode request: %w", method, path, err)
		}
	}
	resp, err := c.send(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return decodeError(resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, body []byte) (*http.Response, error) {
	target := c.root + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		if req.URL.Scheme != "https" {
			return nil, fmt.Errorf("%s %s: refusing to send a bearer token over %s", method, path, req.URL.Scheme)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
}

// decodeError returns the APIError in a failed response, or an opaque error
// naming its status when the body holds none.
func decodeError(resp *http.Response) error {
	var e APIError
	err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&e)
	if err != nil || e.Reason == "" {
		return fmt.Errorf("%s %s: unexpected response %s", resp.Request.Method, resp.Request.URL.Path, resp.Status)
	}
	return &e
}
