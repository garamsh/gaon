package api

import "time"

// The API group and version of the compute resources in this package.
const (
	Group        = "compute.gaon.garam.sh"
	Version      = "v1alpha1"
	GroupVersion = Group + "/" + Version
)

// The API group and version of the roles and bindings, served under their own
// root beside the compute group's.
const (
	RBACGroup        = "rbac.gaon.garam.sh"
	RBACGroupVersion = RBACGroup + "/" + Version
)

// The API group and version of the resource definitions, served under their
// own root beside the compute group's.
const (
	APIExtensionsGroup        = "apiextensions.gaon.garam.sh"
	APIExtensionsGroupVersion = APIExtensionsGroup + "/" + Version
)

// The API group and version of the admission webhook configurations, served
// under their own root beside the compute group's.
const (
	AdmissionRegistrationGroup        = "admissionregistration.gaon.garam.sh"
	AdmissionRegistrationGroupVersion = AdmissionRegistrationGroup + "/" + Version
)

// The API group and version of the generic Lease, served under its own root
// beside the compute group's.
const (
	CoordinationGroup        = "coordination.gaon.garam.sh"
	CoordinationGroupVersion = CoordinationGroup + "/" + Version
)

// The API group and version of AdmissionReview, the body of a call to an
// admission webhook. No path serves it.
const (
	AdmissionGroup        = "admission.gaon.garam.sh"
	AdmissionGroupVersion = AdmissionGroup + "/" + Version
)

// The kinds of the resources in this package.
const (
	KindMicroVM     = "MicroVM"
	KindMicroVMList = "MicroVMList"
	KindVolume      = "Volume"
	KindVolumeList  = "VolumeList"
	KindNode        = "Node"
	KindNodeList    = "NodeList"
	KindLease       = "Lease"
	KindLeaseList   = "LeaseList"
	KindEvent       = "Event"
	KindEventList   = "EventList"

	KindCertificateSigningRequest     = "CertificateSigningRequest"
	KindCertificateSigningRequestList = "CertificateSigningRequestList"

	KindCoordinationLease     = "Lease"
	KindCoordinationLeaseList = "LeaseList"

	KindClusterRole            = "ClusterRole"
	KindClusterRoleList        = "ClusterRoleList"
	KindClusterRoleBinding     = "ClusterRoleBinding"
	KindClusterRoleBindingList = "ClusterRoleBindingList"

	KindCustomResourceDefinition     = "CustomResourceDefinition"
	KindCustomResourceDefinitionList = "CustomResourceDefinitionList"

	KindMutatingWebhookConfiguration       = "MutatingWebhookConfiguration"
	KindMutatingWebhookConfigurationList   = "MutatingWebhookConfigurationList"
	KindValidatingWebhookConfiguration     = "ValidatingWebhookConfiguration"
	KindValidatingWebhookConfigurationList = "ValidatingWebhookConfigurationList"
	KindAdmissionReview                    = "AdmissionReview"
)

// ResourceVersion is the version of an object in the Store. Clients treat it
// as opaque and compare it only for equality.
type ResourceVersion string

// Epoch is a MicroVM's binding epoch: it grows by one at each rebinding.
type Epoch int64

// TypeMeta names the group, version and kind of an object.
type TypeMeta struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}

// UID identifies one incarnation of an object: the server sets it at create
// and it never changes, so a deleted and recreated object of the same name has
// another.
type UID string

// ObjectMeta is the metadata every resource carries.
type ObjectMeta struct {
	Name              string            `json:"name"`
	UID               UID               `json:"uid,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	ResourceVersion   ResourceVersion   `json:"resourceVersion,omitempty"`
	Generation        int64             `json:"generation,omitempty"`
	DeletionTimestamp *time.Time        `json:"deletionTimestamp,omitempty"`
	Finalizers        []string          `json:"finalizers,omitempty"`
	OwnerReferences   []OwnerReference  `json:"ownerReferences,omitempty"`
}

// OwnerReference names the object that owns another, for garbage collection.
// UID is the owner's, so a reference does not follow a recreated owner of the
// same name. Controller marks the one owner that manages the dependent, and
// BlockOwnerDeletion that the dependent holds up the deletion of its owner.
type OwnerReference struct {
	APIVersion         string `json:"apiVersion"`
	Kind               string `json:"kind"`
	Name               string `json:"name"`
	UID                UID    `json:"uid,omitempty"`
	Controller         bool   `json:"controller,omitempty"`
	BlockOwnerDeletion bool   `json:"blockOwnerDeletion,omitempty"`
}

// ListMeta is the metadata of a list: the version to watch from afterwards.
type ListMeta struct {
	ResourceVersion ResourceVersion `json:"resourceVersion"`
}

// ConditionStatus is the status of a Condition.
type ConditionStatus string

// The condition statuses.
const (
	ConditionTrue    ConditionStatus = "True"
	ConditionFalse   ConditionStatus = "False"
	ConditionUnknown ConditionStatus = "Unknown"
)

// Condition carries detail about a resource's state.
type Condition struct {
	Type               string          `json:"type"`
	Status             ConditionStatus `json:"status"`
	Reason             string          `json:"reason,omitempty"`
	Message            string          `json:"message,omitempty"`
	LastTransitionTime *time.Time      `json:"lastTransitionTime,omitempty"`
}

func (c Condition) validate() error {
	switch c.Status {
	case ConditionTrue, ConditionFalse, ConditionUnknown:
		return nil
	default:
		return invalid("condition %q has status %q, want True, False or Unknown", c.Type, c.Status)
	}
}

// validateIdentity checks the type and the name every resource must carry.
func validateIdentity(t TypeMeta, groupVersion, kind string, m ObjectMeta) error {
	if t.APIVersion != groupVersion {
		return invalid("apiVersion is %q, want %q", t.APIVersion, groupVersion)
	}
	if t.Kind != kind {
		return invalid("kind is %q, want %q", t.Kind, kind)
	}
	if m.Name == "" {
		return invalid("metadata.name is empty")
	}
	return nil
}
