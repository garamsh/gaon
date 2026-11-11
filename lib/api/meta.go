package api

import "time"

// The API group and version of the resources in this package.
const (
	Group        = "compute.gaon.garam.sh"
	Version      = "v1alpha1"
	GroupVersion = Group + "/" + Version
)

// The kinds of the resources in this package.
const (
	KindMachine     = "Machine"
	KindMachineList = "MachineList"
	KindVolume      = "Volume"
	KindNode        = "Node"
	KindLease       = "Lease"
)

// ResourceVersion is the version of an object in the Store. Clients treat it
// as opaque and compare it only for equality.
type ResourceVersion string

// Epoch is a Machine's binding epoch: it grows by one at each rebinding.
type Epoch int64

// TypeMeta names the group, version and kind of an object.
type TypeMeta struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}

// ObjectMeta is the metadata every resource carries.
type ObjectMeta struct {
	Name              string           `json:"name"`
	ResourceVersion   ResourceVersion  `json:"resourceVersion,omitempty"`
	Generation        int64            `json:"generation,omitempty"`
	DeletionTimestamp *time.Time       `json:"deletionTimestamp,omitempty"`
	Finalizers        []string         `json:"finalizers,omitempty"`
	OwnerReferences   []OwnerReference `json:"ownerReferences,omitempty"`
}

// OwnerReference names the object that owns another, for garbage collection.
type OwnerReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

// ListMeta is the metadata of a list: the version to watch from afterwards.
type ListMeta struct {
	ResourceVersion ResourceVersion `json:"resourceVersion"`
}

// Condition carries detail about a resource's state. Status is "True",
// "False" or "Unknown".
type Condition struct {
	Type               string     `json:"type"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason,omitempty"`
	Message            string     `json:"message,omitempty"`
	LastTransitionTime *time.Time `json:"lastTransitionTime,omitempty"`
}

// validateIdentity checks the type and the name every resource must carry.
func validateIdentity(t TypeMeta, kind string, m ObjectMeta) error {
	if t.APIVersion != GroupVersion {
		return invalid("apiVersion is %q, want %q", t.APIVersion, GroupVersion)
	}
	if t.Kind != kind {
		return invalid("kind is %q, want %q", t.Kind, kind)
	}
	if m.Name == "" {
		return invalid("metadata.name is empty")
	}
	return nil
}
