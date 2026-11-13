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
	root     string
	http     *http.Client
	token    string
	selector Selector
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

// WithLabelSelector makes the lists and the watches of the Client ask only for
// the objects whose labels sel selects, in the query parameter labelSelector.
// A watch by it sends Added when an object starts to match, and Deleted when
// it stops matching or is removed. The empty selector asks for every object.
// Reads and writes of one object are not changed.
func WithLabelSelector(sel Selector) ClientOption {
	return func(c *Client) { c.selector = sel }
}

// listQuery is the query of a list or a watch by c, or nil for none.
func (c *Client) listQuery() url.Values {
	if len(c.selector) == 0 {
		return nil
	}
	return url.Values{LabelSelectorParam: {c.selector.String()}}
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

// PutMicroVM creates m, or changes its spec when m carries the ResourceVersion
// it was read at (PUT /microvms/{name}).
func (c *Client) PutMicroVM(ctx context.Context, m *MicroVM) (*MicroVM, error) {
	return put(ctx, c, "/microvms/"+url.PathEscape(m.Metadata.Name), m)
}

// BindMicroVM binds a MicroVM to a node and sets its epoch
// (POST /microvms/{name}/binding).
func (c *Client) BindMicroVM(ctx context.Context, name string, b Binding) (*MicroVM, error) {
	return call[MicroVM](ctx, c, http.MethodPost, "/microvms/"+url.PathEscape(name)+"/binding", nil, b)
}

// PutMicroVMStatus writes m's status (PUT /microvms/{name}/status).
func (c *Client) PutMicroVMStatus(ctx context.Context, m *MicroVM) (*MicroVM, error) {
	return put(ctx, c, "/microvms/"+url.PathEscape(m.Metadata.Name)+"/status", m)
}

// DeleteMicroVM sets the deletionTimestamp of a MicroVM, if it is still at
// version rv (DELETE /microvms/{name}).
func (c *Client) DeleteMicroVM(ctx context.Context, name string, rv ResourceVersion) (*MicroVM, error) {
	if rv == "" {
		return nil, errors.New("delete microvm: resource version is empty")
	}
	return call[MicroVM](ctx, c, http.MethodDelete, "/microvms/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetMicroVM returns the named MicroVM (GET /microvms/{name}).
func (c *Client) GetMicroVM(ctx context.Context, name string) (*MicroVM, error) {
	return call[MicroVM](ctx, c, http.MethodGet, "/microvms/"+url.PathEscape(name), nil, nil)
}

// ListMicroVMs returns every MicroVM and the version to watch from
// (GET /microvms).
func (c *Client) ListMicroVMs(ctx context.Context) (*MicroVMList, error) {
	return call[MicroVMList](ctx, c, http.MethodGet, "/microvms", c.listQuery(), nil)
}

// WatchMicroVMs streams the changes made after version rv
// (GET /microvms?watch=true). When rv has expired, Next returns ErrExpired:
// list again and watch from the list's version.
func (c *Client) WatchMicroVMs(ctx context.Context, rv ResourceVersion) (*Watch[MicroVM], error) {
	return watch[MicroVM](ctx, c, "/microvms", KindMicroVM, rv)
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
	return call[NodeList](ctx, c, http.MethodGet, "/nodes", c.listQuery(), nil)
}

// WatchNodes streams the changes made after version rv
// (GET /nodes?watch=true), as WatchMicroVMs does.
func (c *Client) WatchNodes(ctx context.Context, rv ResourceVersion) (*Watch[Node], error) {
	return watch[Node](ctx, c, "/nodes", KindNode, rv)
}

// GetLease returns the named Lease (GET /leases/{name}).
func (c *Client) GetLease(ctx context.Context, name string) (*Lease, error) {
	return call[Lease](ctx, c, http.MethodGet, "/leases/"+url.PathEscape(name), nil, nil)
}

// ListLeases returns every Lease and the version to watch from (GET /leases).
func (c *Client) ListLeases(ctx context.Context) (*LeaseList, error) {
	return call[LeaseList](ctx, c, http.MethodGet, "/leases", c.listQuery(), nil)
}

// WatchLeases streams the changes made after version rv
// (GET /leases?watch=true), as WatchMicroVMs does.
func (c *Client) WatchLeases(ctx context.Context, rv ResourceVersion) (*Watch[Lease], error) {
	return watch[Lease](ctx, c, "/leases", KindLease, rv)
}

// PutVolume creates v, or changes it when v carries the ResourceVersion it
// was read at (PUT /volumes/{name}). A Volume's spec is immutable after
// creation, so a change is for its finalizers.
func (c *Client) PutVolume(ctx context.Context, v *Volume) (*Volume, error) {
	return put(ctx, c, "/volumes/"+url.PathEscape(v.Metadata.Name), v)
}

// DeleteVolume sets the deletionTimestamp of a Volume, or removes one with no
// finalizer, if it is still at version rv (DELETE /volumes/{name}).
func (c *Client) DeleteVolume(ctx context.Context, name string, rv ResourceVersion) (*Volume, error) {
	if rv == "" {
		return nil, errors.New("delete volume: resource version is empty")
	}
	return call[Volume](ctx, c, http.MethodDelete, "/volumes/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetVolume returns the named Volume (GET /volumes/{name}).
func (c *Client) GetVolume(ctx context.Context, name string) (*Volume, error) {
	return call[Volume](ctx, c, http.MethodGet, "/volumes/"+url.PathEscape(name), nil, nil)
}

// ListVolumes returns every Volume and the version to watch from
// (GET /volumes).
func (c *Client) ListVolumes(ctx context.Context) (*VolumeList, error) {
	return call[VolumeList](ctx, c, http.MethodGet, "/volumes", c.listQuery(), nil)
}

// WatchVolumes streams the changes made after version rv
// (GET /volumes?watch=true), as WatchMicroVMs does.
func (c *Client) WatchVolumes(ctx context.Context, rv ResourceVersion) (*Watch[Volume], error) {
	return watch[Volume](ctx, c, "/volumes", KindVolume, rv)
}

// PutVolumeStatus writes v's status (PUT /volumes/{name}/status).
func (c *Client) PutVolumeStatus(ctx context.Context, v *Volume) (*Volume, error) {
	return put(ctx, c, "/volumes/"+url.PathEscape(v.Metadata.Name)+"/status", v)
}

// PutStorageClass creates c, or changes it when c carries the ResourceVersion
// it was read at (PUT /storageclasses/{name}).
func (c *Client) PutStorageClass(ctx context.Context, class *StorageClass) (*StorageClass, error) {
	return put(ctx, c, "/storageclasses/"+url.PathEscape(class.Metadata.Name), class)
}

// DeleteStorageClass removes the named StorageClass, if it is still at version
// rv (DELETE /storageclasses/{name}).
func (c *Client) DeleteStorageClass(ctx context.Context, name string, rv ResourceVersion) (*StorageClass, error) {
	if rv == "" {
		return nil, errors.New("delete storageclass: resource version is empty")
	}
	return call[StorageClass](ctx, c, http.MethodDelete, "/storageclasses/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetStorageClass returns the named StorageClass (GET /storageclasses/{name}).
func (c *Client) GetStorageClass(ctx context.Context, name string) (*StorageClass, error) {
	return call[StorageClass](ctx, c, http.MethodGet, "/storageclasses/"+url.PathEscape(name), nil, nil)
}

// ListStorageClasses returns every StorageClass and the version to watch from
// (GET /storageclasses).
func (c *Client) ListStorageClasses(ctx context.Context) (*StorageClassList, error) {
	return call[StorageClassList](ctx, c, http.MethodGet, "/storageclasses", c.listQuery(), nil)
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
	return call[CertificateSigningRequestList](ctx, c, http.MethodGet, "/certificatesigningrequests", c.listQuery(), nil)
}

// WatchCertificateSigningRequests streams the changes made after version rv
// (GET /certificatesigningrequests?watch=true), as WatchMicroVMs does.
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
	return call[ClusterRoleList](ctx, c, http.MethodGet, "/clusterroles", c.listQuery(), nil)
}

// WatchClusterRoles streams the changes made after version rv
// (GET /clusterroles?watch=true), as WatchMicroVMs does.
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
	return call[ClusterRoleBindingList](ctx, c, http.MethodGet, "/clusterrolebindings", c.listQuery(), nil)
}

// WatchClusterRoleBindings streams the changes made after version rv
// (GET /clusterrolebindings?watch=true), as WatchMicroVMs does.
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
	return call[CustomResourceDefinitionList](ctx, c, http.MethodGet, "/customresourcedefinitions", c.listQuery(), nil)
}

// WatchCustomResourceDefinitions streams the changes made after version rv
// (GET /customresourcedefinitions?watch=true), as WatchMicroVMs does.
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
	return call[MutatingWebhookConfigurationList](ctx, c, http.MethodGet, "/mutatingwebhookconfigurations", c.listQuery(), nil)
}

// WatchMutatingWebhookConfigurations streams the changes made after version rv
// (GET /mutatingwebhookconfigurations?watch=true), as WatchMicroVMs does.
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
	return call[ValidatingWebhookConfigurationList](ctx, c, http.MethodGet, "/validatingwebhookconfigurations", c.listQuery(), nil)
}

// WatchValidatingWebhookConfigurations streams the changes made after version rv
// (GET /validatingwebhookconfigurations?watch=true), as WatchMicroVMs does.
func (c *Client) WatchValidatingWebhookConfigurations(ctx context.Context, rv ResourceVersion) (*Watch[ValidatingWebhookConfiguration], error) {
	return watch[ValidatingWebhookConfiguration](ctx, c, "/validatingwebhookconfigurations", KindValidatingWebhookConfiguration, rv)
}

// PutCoordinationLease creates l, or changes it when l carries the
// ResourceVersion it was read at (PUT /leases/{name}). Call it on a client
// whose root is the coordination group's. Two holders that write at the same
// version cannot both succeed, so the write is the compare-and-swap of a lock.
func (c *Client) PutCoordinationLease(ctx context.Context, l *CoordinationLease) (*CoordinationLease, error) {
	return put(ctx, c, "/leases/"+url.PathEscape(l.Metadata.Name), l)
}

// GetCoordinationLease returns the named Lease (GET /leases/{name}). Call it on
// a client whose root is the coordination group's.
func (c *Client) GetCoordinationLease(ctx context.Context, name string) (*CoordinationLease, error) {
	return call[CoordinationLease](ctx, c, http.MethodGet, "/leases/"+url.PathEscape(name), nil, nil)
}

// ListCoordinationLeases returns every Lease and the version to watch from
// (GET /leases). Call it on a client whose root is the coordination group's.
func (c *Client) ListCoordinationLeases(ctx context.Context) (*CoordinationLeaseList, error) {
	return call[CoordinationLeaseList](ctx, c, http.MethodGet, "/leases", c.listQuery(), nil)
}

// WatchCoordinationLeases streams the changes made after version rv
// (GET /leases?watch=true), as WatchMicroVMs does. Call it on a client whose
// root is the coordination group's.
func (c *Client) WatchCoordinationLeases(ctx context.Context, rv ResourceVersion) (*Watch[CoordinationLease], error) {
	return watch[CoordinationLease](ctx, c, "/leases", KindCoordinationLease, rv)
}

// DeleteCoordinationLease removes the named Lease if it is still at version rv
// (DELETE /leases/{name}). Call it on a client whose root is the coordination
// group's.
func (c *Client) DeleteCoordinationLease(ctx context.Context, name string, rv ResourceVersion) (*CoordinationLease, error) {
	if rv == "" {
		return nil, errors.New("delete lease: resource version is empty")
	}
	return call[CoordinationLease](ctx, c, http.MethodDelete, "/leases/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// PutClusterEvent creates e, or changes it when e carries the ResourceVersion
// it was read at (PUT /events/{name}).
func (c *Client) PutClusterEvent(ctx context.Context, e *ClusterEvent) (*ClusterEvent, error) {
	return put(ctx, c, "/events/"+url.PathEscape(e.Metadata.Name), e)
}

// GetClusterEvent returns the named Event (GET /events/{name}).
func (c *Client) GetClusterEvent(ctx context.Context, name string) (*ClusterEvent, error) {
	return call[ClusterEvent](ctx, c, http.MethodGet, "/events/"+url.PathEscape(name), nil, nil)
}

// ListClusterEvents returns every Event and the version to watch from
// (GET /events).
func (c *Client) ListClusterEvents(ctx context.Context) (*ClusterEventList, error) {
	return call[ClusterEventList](ctx, c, http.MethodGet, "/events", c.listQuery(), nil)
}

// WatchClusterEvents streams the changes made after version rv
// (GET /events?watch=true), as WatchMicroVMs does.
func (c *Client) WatchClusterEvents(ctx context.Context, rv ResourceVersion) (*Watch[ClusterEvent], error) {
	return watch[ClusterEvent](ctx, c, "/events", KindEvent, rv)
}

// DeleteClusterEvent removes the named Event if it is still at version rv
// (DELETE /events/{name}).
func (c *Client) DeleteClusterEvent(ctx context.Context, name string, rv ResourceVersion) (*ClusterEvent, error) {
	if rv == "" {
		return nil, errors.New("delete event: resource version is empty")
	}
	return call[ClusterEvent](ctx, c, http.MethodDelete, "/events/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

func watch[T any](ctx context.Context, c *Client, path, kind string, rv ResourceVersion) (*Watch[T], error) {
	if rv == "" {
		return nil, fmt.Errorf("watch %s: resource version is empty", path)
	}
	query := c.listQuery()
	if query == nil {
		query = url.Values{}
	}
	query.Set("watch", "true")
	query.Set("resourceVersion", string(rv))
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

// PutSecret creates o, or changes it when o carries the ResourceVersion it was
// read at (PUT /secrets/{name}). Call it on a client whose root is the compute
// group's.
func (c *Client) PutSecret(ctx context.Context, o *Secret) (*Secret, error) {
	return put(ctx, c, "/secrets/"+url.PathEscape(o.Metadata.Name), o)
}

// DeleteSecret removes the named Secret, if it is still at version rv
// (DELETE /secrets/{name}).
func (c *Client) DeleteSecret(ctx context.Context, name string, rv ResourceVersion) (*Secret, error) {
	if rv == "" {
		return nil, errors.New("delete secrets: resource version is empty")
	}
	return call[Secret](ctx, c, http.MethodDelete, "/secrets/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetSecret returns the named Secret (GET /secrets/{name}).
func (c *Client) GetSecret(ctx context.Context, name string) (*Secret, error) {
	return call[Secret](ctx, c, http.MethodGet, "/secrets/"+url.PathEscape(name), nil, nil)
}

// ListSecrets returns every Secret and the version to watch from
// (GET /secrets).
func (c *Client) ListSecrets(ctx context.Context) (*SecretList, error) {
	return call[SecretList](ctx, c, http.MethodGet, "/secrets", c.listQuery(), nil)
}

// WatchSecrets streams the changes made after version rv
// (GET /secrets?watch=true).
func (c *Client) WatchSecrets(ctx context.Context, rv ResourceVersion) (*Watch[Secret], error) {
	return watch[Secret](ctx, c, "/secrets", KindSecret, rv)
}

// PutConfigMap creates o, or changes it when o carries the ResourceVersion it was
// read at (PUT /configmaps/{name}). Call it on a client whose root is the compute
// group's.
func (c *Client) PutConfigMap(ctx context.Context, o *ConfigMap) (*ConfigMap, error) {
	return put(ctx, c, "/configmaps/"+url.PathEscape(o.Metadata.Name), o)
}

// DeleteConfigMap removes the named ConfigMap, if it is still at version rv
// (DELETE /configmaps/{name}).
func (c *Client) DeleteConfigMap(ctx context.Context, name string, rv ResourceVersion) (*ConfigMap, error) {
	if rv == "" {
		return nil, errors.New("delete configmaps: resource version is empty")
	}
	return call[ConfigMap](ctx, c, http.MethodDelete, "/configmaps/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetConfigMap returns the named ConfigMap (GET /configmaps/{name}).
func (c *Client) GetConfigMap(ctx context.Context, name string) (*ConfigMap, error) {
	return call[ConfigMap](ctx, c, http.MethodGet, "/configmaps/"+url.PathEscape(name), nil, nil)
}

// ListConfigMaps returns every ConfigMap and the version to watch from
// (GET /configmaps).
func (c *Client) ListConfigMaps(ctx context.Context) (*ConfigMapList, error) {
	return call[ConfigMapList](ctx, c, http.MethodGet, "/configmaps", c.listQuery(), nil)
}

// WatchConfigMaps streams the changes made after version rv
// (GET /configmaps?watch=true).
func (c *Client) WatchConfigMaps(ctx context.Context, rv ResourceVersion) (*Watch[ConfigMap], error) {
	return watch[ConfigMap](ctx, c, "/configmaps", KindConfigMap, rv)
}

// PutServiceAccount creates o, or changes it when o carries the ResourceVersion it was
// read at (PUT /serviceaccounts/{name}). Call it on a client whose root is the compute
// group's.
func (c *Client) PutServiceAccount(ctx context.Context, o *ServiceAccount) (*ServiceAccount, error) {
	return put(ctx, c, "/serviceaccounts/"+url.PathEscape(o.Metadata.Name), o)
}

// DeleteServiceAccount removes the named ServiceAccount, if it is still at version rv
// (DELETE /serviceaccounts/{name}).
func (c *Client) DeleteServiceAccount(ctx context.Context, name string, rv ResourceVersion) (*ServiceAccount, error) {
	if rv == "" {
		return nil, errors.New("delete serviceaccounts: resource version is empty")
	}
	return call[ServiceAccount](ctx, c, http.MethodDelete, "/serviceaccounts/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetServiceAccount returns the named ServiceAccount (GET /serviceaccounts/{name}).
func (c *Client) GetServiceAccount(ctx context.Context, name string) (*ServiceAccount, error) {
	return call[ServiceAccount](ctx, c, http.MethodGet, "/serviceaccounts/"+url.PathEscape(name), nil, nil)
}

// ListServiceAccounts returns every ServiceAccount and the version to watch from
// (GET /serviceaccounts).
func (c *Client) ListServiceAccounts(ctx context.Context) (*ServiceAccountList, error) {
	return call[ServiceAccountList](ctx, c, http.MethodGet, "/serviceaccounts", c.listQuery(), nil)
}

// WatchServiceAccounts streams the changes made after version rv
// (GET /serviceaccounts?watch=true).
func (c *Client) WatchServiceAccounts(ctx context.Context, rv ResourceVersion) (*Watch[ServiceAccount], error) {
	return watch[ServiceAccount](ctx, c, "/serviceaccounts", KindServiceAccount, rv)
}

// CreateServiceAccountToken asks for a token of the ServiceAccount name, bound
// to the MicroVM req names (POST /serviceaccounts/{name}/token). The answer's
// status carries the token; nothing is stored.
func (c *Client) CreateServiceAccountToken(ctx context.Context, name string, req *TokenRequest) (*TokenRequest, error) {
	return call[TokenRequest](ctx, c, http.MethodPost, "/serviceaccounts/"+url.PathEscape(name)+"/token", nil, req)
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

// PutService creates o, or changes it when o carries the ResourceVersion it was
// read at (PUT /services/{name}). Call it on a client whose root is the compute
// group's.
func (c *Client) PutService(ctx context.Context, o *Service) (*Service, error) {
	return put(ctx, c, "/services/"+url.PathEscape(o.Metadata.Name), o)
}

// DeleteService removes the named Service, if it is still at version rv
// (DELETE /services/{name}).
func (c *Client) DeleteService(ctx context.Context, name string, rv ResourceVersion) (*Service, error) {
	if rv == "" {
		return nil, errors.New("delete services: resource version is empty")
	}
	return call[Service](ctx, c, http.MethodDelete, "/services/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetService returns the named Service (GET /services/{name}).
func (c *Client) GetService(ctx context.Context, name string) (*Service, error) {
	return call[Service](ctx, c, http.MethodGet, "/services/"+url.PathEscape(name), nil, nil)
}

// ListServices returns every Service and the version to watch from
// (GET /services).
func (c *Client) ListServices(ctx context.Context) (*ServiceList, error) {
	return call[ServiceList](ctx, c, http.MethodGet, "/services", c.listQuery(), nil)
}

// WatchServices streams the changes made after version rv
// (GET /services?watch=true).
func (c *Client) WatchServices(ctx context.Context, rv ResourceVersion) (*Watch[Service], error) {
	return watch[Service](ctx, c, "/services", KindService, rv)
}

// PutEndpoints creates o, or changes it when o carries the ResourceVersion it was
// read at (PUT /endpoints/{name}). Call it on a client whose root is the compute
// group's.
func (c *Client) PutEndpoints(ctx context.Context, o *Endpoints) (*Endpoints, error) {
	return put(ctx, c, "/endpoints/"+url.PathEscape(o.Metadata.Name), o)
}

// DeleteEndpoints removes the named Endpoints, if it is still at version rv
// (DELETE /endpoints/{name}).
func (c *Client) DeleteEndpoints(ctx context.Context, name string, rv ResourceVersion) (*Endpoints, error) {
	if rv == "" {
		return nil, errors.New("delete endpoints: resource version is empty")
	}
	return call[Endpoints](ctx, c, http.MethodDelete, "/endpoints/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetEndpoints returns the named Endpoints (GET /endpoints/{name}).
func (c *Client) GetEndpoints(ctx context.Context, name string) (*Endpoints, error) {
	return call[Endpoints](ctx, c, http.MethodGet, "/endpoints/"+url.PathEscape(name), nil, nil)
}

// ListEndpoints returns every Endpoints and the version to watch from
// (GET /endpoints).
func (c *Client) ListEndpoints(ctx context.Context) (*EndpointsList, error) {
	return call[EndpointsList](ctx, c, http.MethodGet, "/endpoints", c.listQuery(), nil)
}

// WatchEndpoints streams the changes made after version rv
// (GET /endpoints?watch=true).
func (c *Client) WatchEndpoints(ctx context.Context, rv ResourceVersion) (*Watch[Endpoints], error) {
	return watch[Endpoints](ctx, c, "/endpoints", KindEndpoints, rv)
}

// PutNetworkPolicy creates o, or changes it when o carries the ResourceVersion it was
// read at (PUT /networkpolicies/{name}). Call it on a client whose root is the compute
// group's.
func (c *Client) PutNetworkPolicy(ctx context.Context, o *NetworkPolicy) (*NetworkPolicy, error) {
	return put(ctx, c, "/networkpolicies/"+url.PathEscape(o.Metadata.Name), o)
}

// DeleteNetworkPolicy removes the named NetworkPolicy, if it is still at version rv
// (DELETE /networkpolicies/{name}).
func (c *Client) DeleteNetworkPolicy(ctx context.Context, name string, rv ResourceVersion) (*NetworkPolicy, error) {
	if rv == "" {
		return nil, errors.New("delete networkpolicies: resource version is empty")
	}
	return call[NetworkPolicy](ctx, c, http.MethodDelete, "/networkpolicies/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetNetworkPolicy returns the named NetworkPolicy (GET /networkpolicies/{name}).
func (c *Client) GetNetworkPolicy(ctx context.Context, name string) (*NetworkPolicy, error) {
	return call[NetworkPolicy](ctx, c, http.MethodGet, "/networkpolicies/"+url.PathEscape(name), nil, nil)
}

// ListNetworkPolicies returns every NetworkPolicy and the version to watch from
// (GET /networkpolicies).
func (c *Client) ListNetworkPolicies(ctx context.Context) (*NetworkPolicyList, error) {
	return call[NetworkPolicyList](ctx, c, http.MethodGet, "/networkpolicies", c.listQuery(), nil)
}

// WatchNetworkPolicies streams the changes made after version rv
// (GET /networkpolicies?watch=true).
func (c *Client) WatchNetworkPolicies(ctx context.Context, rv ResourceVersion) (*Watch[NetworkPolicy], error) {
	return watch[NetworkPolicy](ctx, c, "/networkpolicies", KindNetworkPolicy, rv)
}
