// Package resourceapi holds the rules of the v1alpha1 resource API over a
// Store: who may write which section of an object, and what a write changes.
package resourceapi

import (
	"context"
	"time"

	"github.com/garamsh/gaon/apiserver/internal/admission"
	"github.com/garamsh/gaon/lib/api"
)

// Service is the resource API of resource-api.md §Paths. A failure the
// caller caused is an *api.APIError; any other error is the server's.
type Service interface {
	PutMicroVM(ctx context.Context, name string, m *api.MicroVM) (*api.MicroVM, error)
	BindMicroVM(ctx context.Context, name string, b api.Binding) (*api.MicroVM, error)
	PutMicroVMStatus(ctx context.Context, name string, m *api.MicroVM) (*api.MicroVM, error)
	DeleteMicroVM(ctx context.Context, name string, rv api.ResourceVersion) (*api.MicroVM, error)
	GetMicroVM(ctx context.Context, name string) (*api.MicroVM, error)
	ListMicroVMs(ctx context.Context, sel api.Selector) (*api.MicroVMList, error)
	WatchMicroVMs(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutVolume(ctx context.Context, name string, v *api.Volume) (*api.Volume, error)
	PutVolumeStatus(ctx context.Context, name string, v *api.Volume) (*api.Volume, error)
	DeleteVolume(ctx context.Context, name string, rv api.ResourceVersion) (*api.Volume, error)
	GetVolume(ctx context.Context, name string) (*api.Volume, error)
	ListVolumes(ctx context.Context, sel api.Selector) (*api.VolumeList, error)
	WatchVolumes(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutNode(ctx context.Context, name string, n *api.Node) (*api.Node, error)
	PutNodeStatus(ctx context.Context, name string, n *api.Node) (*api.Node, error)
	GetNode(ctx context.Context, name string) (*api.Node, error)
	ListNodes(ctx context.Context, sel api.Selector) (*api.NodeList, error)
	WatchNodes(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutLease(ctx context.Context, name string, l *api.Lease) (*api.Lease, error)
	GetLease(ctx context.Context, name string) (*api.Lease, error)
	ListLeases(ctx context.Context, sel api.Selector) (*api.LeaseList, error)
	WatchLeases(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutCertificateSigningRequest(ctx context.Context, name, username string, r *api.CertificateSigningRequest) (*api.CertificateSigningRequest, error)
	PutCertificateSigningRequestStatus(ctx context.Context, name string, r *api.CertificateSigningRequest) (*api.CertificateSigningRequest, error)
	GetCertificateSigningRequest(ctx context.Context, name string) (*api.CertificateSigningRequest, error)
	ListCertificateSigningRequests(ctx context.Context, sel api.Selector) (*api.CertificateSigningRequestList, error)
	WatchCertificateSigningRequests(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutClusterRole(ctx context.Context, name string, r *api.ClusterRole) (*api.ClusterRole, error)
	DeleteClusterRole(ctx context.Context, name string, rv api.ResourceVersion) (*api.ClusterRole, error)
	GetClusterRole(ctx context.Context, name string) (*api.ClusterRole, error)
	ListClusterRoles(ctx context.Context, sel api.Selector) (*api.ClusterRoleList, error)
	WatchClusterRoles(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutClusterRoleBinding(ctx context.Context, name string, b *api.ClusterRoleBinding) (*api.ClusterRoleBinding, error)
	DeleteClusterRoleBinding(ctx context.Context, name string, rv api.ResourceVersion) (*api.ClusterRoleBinding, error)
	GetClusterRoleBinding(ctx context.Context, name string) (*api.ClusterRoleBinding, error)
	ListClusterRoleBindings(ctx context.Context, sel api.Selector) (*api.ClusterRoleBindingList, error)
	WatchClusterRoleBindings(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutCustomResourceDefinition(ctx context.Context, name string, d *api.CustomResourceDefinition) (*api.CustomResourceDefinition, error)
	DeleteCustomResourceDefinition(ctx context.Context, name string, rv api.ResourceVersion) (*api.CustomResourceDefinition, error)
	GetCustomResourceDefinition(ctx context.Context, name string) (*api.CustomResourceDefinition, error)
	ListCustomResourceDefinitions(ctx context.Context, sel api.Selector) (*api.CustomResourceDefinitionList, error)
	WatchCustomResourceDefinitions(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutMutatingWebhookConfiguration(ctx context.Context, name string, c *api.MutatingWebhookConfiguration) (*api.MutatingWebhookConfiguration, error)
	DeleteMutatingWebhookConfiguration(ctx context.Context, name string, rv api.ResourceVersion) (*api.MutatingWebhookConfiguration, error)
	GetMutatingWebhookConfiguration(ctx context.Context, name string) (*api.MutatingWebhookConfiguration, error)
	ListMutatingWebhookConfigurations(ctx context.Context, sel api.Selector) (*api.MutatingWebhookConfigurationList, error)
	WatchMutatingWebhookConfigurations(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutValidatingWebhookConfiguration(ctx context.Context, name string, c *api.ValidatingWebhookConfiguration) (*api.ValidatingWebhookConfiguration, error)
	DeleteValidatingWebhookConfiguration(ctx context.Context, name string, rv api.ResourceVersion) (*api.ValidatingWebhookConfiguration, error)
	GetValidatingWebhookConfiguration(ctx context.Context, name string) (*api.ValidatingWebhookConfiguration, error)
	ListValidatingWebhookConfigurations(ctx context.Context, sel api.Selector) (*api.ValidatingWebhookConfigurationList, error)
	WatchValidatingWebhookConfigurations(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutCoordinationLease(ctx context.Context, name string, l *api.CoordinationLease) (*api.CoordinationLease, error)
	DeleteCoordinationLease(ctx context.Context, name string, rv api.ResourceVersion) (*api.CoordinationLease, error)
	GetCoordinationLease(ctx context.Context, name string) (*api.CoordinationLease, error)
	ListCoordinationLeases(ctx context.Context, sel api.Selector) (*api.CoordinationLeaseList, error)
	WatchCoordinationLeases(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutClusterEvent(ctx context.Context, name string, e *api.ClusterEvent) (*api.ClusterEvent, error)
	DeleteClusterEvent(ctx context.Context, name string, rv api.ResourceVersion) (*api.ClusterEvent, error)
	GetClusterEvent(ctx context.Context, name string) (*api.ClusterEvent, error)
	ListClusterEvents(ctx context.Context, sel api.Selector) (*api.ClusterEventList, error)
	WatchClusterEvents(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutSecret(ctx context.Context, name string, o *api.Secret) (*api.Secret, error)
	DeleteSecret(ctx context.Context, name string, rv api.ResourceVersion) (*api.Secret, error)
	GetSecret(ctx context.Context, name string) (*api.Secret, error)
	ListSecrets(ctx context.Context, sel api.Selector) (*api.SecretList, error)
	WatchSecrets(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutConfigMap(ctx context.Context, name string, o *api.ConfigMap) (*api.ConfigMap, error)
	DeleteConfigMap(ctx context.Context, name string, rv api.ResourceVersion) (*api.ConfigMap, error)
	GetConfigMap(ctx context.Context, name string) (*api.ConfigMap, error)
	ListConfigMaps(ctx context.Context, sel api.Selector) (*api.ConfigMapList, error)
	WatchConfigMaps(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutServiceAccount(ctx context.Context, name string, o *api.ServiceAccount) (*api.ServiceAccount, error)
	DeleteServiceAccount(ctx context.Context, name string, rv api.ResourceVersion) (*api.ServiceAccount, error)
	GetServiceAccount(ctx context.Context, name string) (*api.ServiceAccount, error)
	ListServiceAccounts(ctx context.Context, sel api.Selector) (*api.ServiceAccountList, error)
	WatchServiceAccounts(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutService(ctx context.Context, name string, o *api.Service) (*api.Service, error)
	DeleteService(ctx context.Context, name string, rv api.ResourceVersion) (*api.Service, error)
	GetService(ctx context.Context, name string) (*api.Service, error)
	ListServices(ctx context.Context, sel api.Selector) (*api.ServiceList, error)
	WatchServices(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutEndpoints(ctx context.Context, name string, o *api.Endpoints) (*api.Endpoints, error)
	DeleteEndpoints(ctx context.Context, name string, rv api.ResourceVersion) (*api.Endpoints, error)
	GetEndpoints(ctx context.Context, name string) (*api.Endpoints, error)
	ListEndpoints(ctx context.Context, sel api.Selector) (*api.EndpointsList, error)
	WatchEndpoints(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutStorageClass(ctx context.Context, name string, c *api.StorageClass) (*api.StorageClass, error)
	DeleteStorageClass(ctx context.Context, name string, rv api.ResourceVersion) (*api.StorageClass, error)
	GetStorageClass(ctx context.Context, name string) (*api.StorageClass, error)
	ListStorageClasses(ctx context.Context, sel api.Selector) (*api.StorageClassList, error)
	WatchStorageClasses(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	PutNetworkPolicy(ctx context.Context, name string, o *api.NetworkPolicy) (*api.NetworkPolicy, error)
	DeleteNetworkPolicy(ctx context.Context, name string, rv api.ResourceVersion) (*api.NetworkPolicy, error)
	GetNetworkPolicy(ctx context.Context, name string) (*api.NetworkPolicy, error)
	ListNetworkPolicies(ctx context.Context, sel api.Selector) (*api.NetworkPolicyList, error)
	WatchNetworkPolicies(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)

	// CreateServiceAccountToken signs a token of the ServiceAccount name for
	// req.Spec.BoundMicroVM and returns req with the token in its status. The
	// request is not stored and is not sent to a webhook.
	CreateServiceAccountToken(ctx context.Context, name string, req *api.TokenRequest) (*api.TokenRequest, error)

	// Labels returns the labels of the object named name of the resource in
	// group, built-in or custom, or nil where there is no such object. The
	// authorizer judges a label selector rule on them.
	Labels(ctx context.Context, group, resource, name string) (map[string]string, error)

	PutCustomObject(ctx context.Context, p CustomPath, name string, o *api.CustomObject) (*api.CustomObject, error)
	PutCustomObjectStatus(ctx context.Context, p CustomPath, name string, o *api.CustomObject) (*api.CustomObject, error)
	DeleteCustomObject(ctx context.Context, p CustomPath, name string, rv api.ResourceVersion) (*api.CustomObject, error)
	GetCustomObject(ctx context.Context, p CustomPath, name string) (*api.CustomObject, error)
	ListCustomObjects(ctx context.Context, p CustomPath, sel api.Selector) (*api.CustomObjectList, error)
	WatchCustomObjects(ctx context.Context, p CustomPath, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error)
}

var (
	microvms = kind[api.MicroVM]{"microvms", func(m *api.MicroVM) *api.ObjectMeta { return &m.Metadata }, func(m *api.MicroVM) any { return m.Spec }, gvk(api.Group, api.KindMicroVM)}
	volumes  = kind[api.Volume]{"volumes", func(v *api.Volume) *api.ObjectMeta { return &v.Metadata }, func(v *api.Volume) any { return v.Spec }, gvk(api.Group, api.KindVolume)}
	nodes    = kind[api.Node]{"nodes", func(n *api.Node) *api.ObjectMeta { return &n.Metadata }, func(n *api.Node) any { return n.Spec }, gvk(api.Group, api.KindNode)}
	leases   = kind[api.Lease]{"leases", func(l *api.Lease) *api.ObjectMeta { return &l.Metadata }, func(l *api.Lease) any { return l.Spec }, gvk(api.Group, api.KindLease)}

	// The Lease of the coordination group is stored under a plural that holds
	// the group, as a custom kind is, so that it is not the Node's Lease.
	coordinationLeases = kind[api.CoordinationLease]{"leases." + api.CoordinationGroup, func(l *api.CoordinationLease) *api.ObjectMeta { return &l.Metadata }, func(l *api.CoordinationLease) any { return l.Spec }, gvk(api.CoordinationGroup, api.KindCoordinationLease)}
	clusterEvents      = kind[api.ClusterEvent]{"events", func(e *api.ClusterEvent) *api.ObjectMeta { return &e.Metadata }, func(e *api.ClusterEvent) any { return e.Spec }, gvk(api.Group, api.KindEvent)}

	secrets         = kind[api.Secret]{"secrets", func(o *api.Secret) *api.ObjectMeta { return &o.Metadata }, func(o *api.Secret) any { return o.Data }, gvk(api.Group, api.KindSecret)}
	configMaps      = kind[api.ConfigMap]{"configmaps", func(o *api.ConfigMap) *api.ObjectMeta { return &o.Metadata }, func(o *api.ConfigMap) any { return o.Data }, gvk(api.Group, api.KindConfigMap)}
	serviceAccounts = kind[api.ServiceAccount]{"serviceaccounts", func(o *api.ServiceAccount) *api.ObjectMeta { return &o.Metadata }, func(*api.ServiceAccount) any { return nil }, gvk(api.Group, api.KindServiceAccount)}

	services        = kind[api.Service]{"services", func(o *api.Service) *api.ObjectMeta { return &o.Metadata }, func(o *api.Service) any { return o.Spec }, gvk(api.Group, api.KindService)}
	endpointsKind   = kind[api.Endpoints]{"endpoints", func(o *api.Endpoints) *api.ObjectMeta { return &o.Metadata }, func(o *api.Endpoints) any { return o.Subsets }, gvk(api.Group, api.KindEndpoints)}
	networkPolicies = kind[api.NetworkPolicy]{"networkpolicies", func(o *api.NetworkPolicy) *api.ObjectMeta { return &o.Metadata }, func(o *api.NetworkPolicy) any { return o.Spec }, gvk(api.Group, api.KindNetworkPolicy)}

	certificateSigningRequests = kind[api.CertificateSigningRequest]{"certificatesigningrequests", func(r *api.CertificateSigningRequest) *api.ObjectMeta { return &r.Metadata }, func(r *api.CertificateSigningRequest) any { return r.Spec }, gvk(api.Group, api.KindCertificateSigningRequest)}

	clusterRoles        = kind[api.ClusterRole]{"clusterroles", func(r *api.ClusterRole) *api.ObjectMeta { return &r.Metadata }, func(r *api.ClusterRole) any { return r.Rules }, gvk(api.RBACGroup, api.KindClusterRole)}
	clusterRoleBindings = kind[api.ClusterRoleBinding]{"clusterrolebindings", func(b *api.ClusterRoleBinding) *api.ObjectMeta { return &b.Metadata }, func(b *api.ClusterRoleBinding) any { return [2]any{b.RoleRef, b.Subjects} }, gvk(api.RBACGroup, api.KindClusterRoleBinding)}

	mutatingWebhookConfigurations   = kind[api.MutatingWebhookConfiguration]{"mutatingwebhookconfigurations", func(c *api.MutatingWebhookConfiguration) *api.ObjectMeta { return &c.Metadata }, func(c *api.MutatingWebhookConfiguration) any { return c.Webhooks }, gvk(api.AdmissionRegistrationGroup, api.KindMutatingWebhookConfiguration)}
	validatingWebhookConfigurations = kind[api.ValidatingWebhookConfiguration]{"validatingwebhookconfigurations", func(c *api.ValidatingWebhookConfiguration) *api.ObjectMeta { return &c.Metadata }, func(c *api.ValidatingWebhookConfiguration) any { return c.Webhooks }, gvk(api.AdmissionRegistrationGroup, api.KindValidatingWebhookConfiguration)}

	storageClasses = kind[api.StorageClass]{"storageclasses", func(c *api.StorageClass) *api.ObjectMeta { return &c.Metadata }, func(c *api.StorageClass) any { return [2]any{c.Provisioner, c.AllowVolumeExpansion} }, gvk(api.Group, api.KindStorageClass)}

	customResourceDefinitions = kind[api.CustomResourceDefinition]{"customresourcedefinitions", func(d *api.CustomResourceDefinition) *api.ObjectMeta { return &d.Metadata }, func(d *api.CustomResourceDefinition) any { return d.Spec }, gvk(api.APIExtensionsGroup, api.KindCustomResourceDefinition)}
)

// gvk is the kind of a core or built-in group, at the one version the API has.
func gvk(group, kind string) api.GroupVersionKind {
	return api.GroupVersionKind{Group: group, Version: api.Version, Kind: kind}
}

type service struct {
	st     Store
	now    func() time.Time
	hooks  *admission.Caller
	signer TokenSigner
	ranges Ranges
}

// NewService returns the Service over st. now is the clock that stamps a
// deletion and a token. signer signs service account tokens and is nil where
// the server has no signing key, which refuses every token request. ranges
// are the two ranges Service addresses are allocated from.
func NewService(st Store, now func() time.Time, signer TokenSigner, ranges Ranges) Service {
	return &service{st: st, now: now, hooks: &admission.Caller{Source: webhookSource{st}}, signer: signer, ranges: ranges}
}

func (s *service) GetMicroVM(ctx context.Context, name string) (*api.MicroVM, error) {
	m, _, err := microvms.get(ctx, s.st, name)
	return m, err
}

func (s *service) ListMicroVMs(ctx context.Context, sel api.Selector) (*api.MicroVMList, error) {
	items, rv, err := microvms.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.MicroVMList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVMList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchMicroVMs(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return microvms.watch(ctx, s.st, rv, sel)
}

func (s *service) GetNode(ctx context.Context, name string) (*api.Node, error) {
	n, _, err := nodes.get(ctx, s.st, name)
	return n, err
}

func (s *service) ListNodes(ctx context.Context, sel api.Selector) (*api.NodeList, error) {
	items, rv, err := nodes.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.NodeList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNodeList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchNodes(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return nodes.watch(ctx, s.st, rv, sel)
}

func (s *service) GetLease(ctx context.Context, name string) (*api.Lease, error) {
	l, _, err := leases.get(ctx, s.st, name)
	return l, err
}

func (s *service) ListLeases(ctx context.Context, sel api.Selector) (*api.LeaseList, error) {
	items, rv, err := leases.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.LeaseList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindLeaseList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchLeases(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return leases.watch(ctx, s.st, rv, sel)
}

func (s *service) GetCertificateSigningRequest(ctx context.Context, name string) (*api.CertificateSigningRequest, error) {
	r, _, err := certificateSigningRequests.get(ctx, s.st, name)
	return r, err
}

func (s *service) ListCertificateSigningRequests(ctx context.Context, sel api.Selector) (*api.CertificateSigningRequestList, error) {
	items, rv, err := certificateSigningRequests.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.CertificateSigningRequestList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindCertificateSigningRequestList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchCertificateSigningRequests(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return certificateSigningRequests.watch(ctx, s.st, rv, sel)
}
