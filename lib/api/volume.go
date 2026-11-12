package api

// VolumePhase is the phase a Volume's status reports.
type VolumePhase string

// The Volume phases.
const (
	VolumePending   VolumePhase = "Pending"
	VolumeAvailable VolumePhase = "Available"
	VolumeBound     VolumePhase = "Bound"
	VolumeReleased  VolumePhase = "Released"
	VolumeFailed    VolumePhase = "Failed"
)

// The storage class a Node Agent serves itself, and the one access mode it
// offers. A Volume of any other class waits Pending for a plugin that serves
// it.
const (
	StorageClassLocal   = "local"
	AccessReadWriteOnce = "ReadWriteOnce"
)

// NodeAgentFinalizer is the finalizer the API server adds to a local Volume
// when it pins the Volume to a Node, and the Node's agent removes once no
// Machine has the Volume attached and its disk is deleted. It is the name of
// the Machine finalizer of the same owner.
const NodeAgentFinalizer = Group + "/node-agent"

// Volume is a disk a Machine can mount.
type Volume struct {
	TypeMeta
	Metadata ObjectMeta   `json:"metadata"`
	Spec     VolumeSpec   `json:"spec"`
	Status   VolumeStatus `json:"status"`
}

// VolumeList is the answer to listing Volumes.
type VolumeList struct {
	TypeMeta
	Metadata ListMeta `json:"metadata"`
	Items    []Volume `json:"items"`
}

// VolumeSpec is the desired state of a Volume. A user writes it at creation
// and it is immutable after, except NodeName, which only the binding of a
// Machine that uses the Volume sets.
type VolumeSpec struct {
	StorageClass string   `json:"storageClass,omitempty"`
	AccessModes  []string `json:"accessModes,omitempty"`
	Storage      Quantity `json:"storage,omitempty"`
	DataSource   string   `json:"dataSource,omitempty"`
	NodeName     string   `json:"nodeName,omitempty"`
}

// VolumeStatus is the observed state of a Volume.
type VolumeStatus struct {
	Phase      VolumePhase `json:"phase,omitempty"`
	Attachment *Attachment `json:"attachment,omitempty"`
	Capacity   Quantity    `json:"capacity,omitempty"`
}

// Attachment names the Machine, and the epoch of its binding, that a Volume
// is attached to.
type Attachment struct {
	MachineName  string `json:"machineName"`
	BindingEpoch Epoch  `json:"bindingEpoch"`
}

// Validate checks the identity and the spec of v. A Volume of the local
// class is a disk of a positive size, ReadWriteOnce, with no data source.
func (v *Volume) Validate() error {
	if err := validateIdentity(v.TypeMeta, GroupVersion, KindVolume, v.Metadata); err != nil {
		return err
	}
	spec := v.Spec
	if spec.StorageClass == "" {
		return invalid("spec.storageClass is empty")
	}
	if spec.StorageClass != StorageClassLocal {
		return nil
	}
	if len(spec.AccessModes) != 1 || spec.AccessModes[0] != AccessReadWriteOnce {
		return invalid("spec.accessModes is %v, want [%s] for class %s", spec.AccessModes, AccessReadWriteOnce, StorageClassLocal)
	}
	if size, err := spec.Storage.Value(); err != nil || size.Sign() <= 0 {
		return invalid("spec.storage is %q, want a positive size for class %s", spec.Storage, StorageClassLocal)
	}
	if spec.DataSource != "" {
		return invalid("spec.dataSource is %q, want empty for class %s", spec.DataSource, StorageClassLocal)
	}
	return nil
}

// ValidateCreate checks v as a create: Validate, and no nodeName, which only
// the binding of a Machine sets.
func (v *Volume) ValidateCreate() error {
	if err := v.Validate(); err != nil {
		return err
	}
	if v.Spec.NodeName != "" {
		return invalid("spec.nodeName is %q, want empty: only a binding sets it", v.Spec.NodeName)
	}
	return nil
}

// ValidateUpdate checks that v changes nothing of old's spec, nodeName
// included.
func (v *Volume) ValidateUpdate(old *Volume) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if !sameJSON(v.Spec, old.Spec) {
		return invalid("spec changes after creation")
	}
	return nil
}

// Pin pins a local v to node: it sets spec.nodeName and adds
// NodeAgentFinalizer in the same change. It changes nothing of a Volume of
// another class, or one already pinned to node. It returns ErrConflict for a
// Volume that is being deleted or is pinned to another Node, and leaves v
// unchanged.
func (v *Volume) Pin(node string) error {
	if v.Metadata.DeletionTimestamp != nil {
		return NewAPIError(ReasonConflict, "volume %q is being deleted", v.Metadata.Name)
	}
	if v.Spec.NodeName != "" && v.Spec.NodeName != node {
		return NewAPIError(ReasonConflict, "volume %q is pinned to node %q, not %q", v.Metadata.Name, v.Spec.NodeName, node)
	}
	if v.Spec.StorageClass != StorageClassLocal || v.Spec.NodeName == node {
		return nil
	}
	v.Spec.NodeName = node
	v.Metadata.Finalizers = append(v.Metadata.Finalizers, NodeAgentFinalizer)
	return nil
}

// PinVolumes pins each Volume m uses, found by name in volumes, to node, for
// the binding of m to node, and returns the Volumes it changed. A name with
// no Volume is ErrInvalid, and a Volume that refuses (see Pin) is
// ErrConflict. On a refusal no Volume is changed.
func (m *Machine) PinVolumes(node string, volumes map[string]*Volume) ([]*Volume, error) {
	var used []*Volume
	seen := map[string]bool{}
	for _, mv := range m.Spec.Volumes {
		if seen[mv.Name] {
			continue
		}
		seen[mv.Name] = true
		v, ok := volumes[mv.Name]
		if !ok {
			return nil, invalid("machine %q uses volume %q, which does not exist", m.Metadata.Name, mv.Name)
		}
		probe := *v
		if err := probe.Pin(node); err != nil {
			return nil, err
		}
		used = append(used, v)
	}
	var changed []*Volume
	for _, v := range used {
		before := v.Spec.NodeName
		_ = v.Pin(node)
		if v.Spec.NodeName != before {
			changed = append(changed, v)
		}
	}
	return changed, nil
}

// ValidateStatus checks that the phase of v's status is a known one.
func (v *Volume) ValidateStatus() error {
	switch v.Status.Phase {
	case VolumePending, VolumeAvailable, VolumeBound, VolumeReleased, VolumeFailed:
		return nil
	default:
		return invalid("status.phase is %q, want Pending, Available, Bound, Released or Failed", v.Status.Phase)
	}
}

// Attach attaches v to the named Machine at its current epoch. It succeeds
// only if v is not being deleted and is unattached or attached at a lower
// epoch; otherwise it returns ErrConflict and leaves v unchanged.
func (v *Volume) Attach(machine string, machineEpoch Epoch) error {
	if v.Metadata.DeletionTimestamp != nil {
		return NewAPIError(ReasonConflict, "volume %q is being deleted", v.Metadata.Name)
	}
	if a := v.Status.Attachment; a != nil && a.BindingEpoch >= machineEpoch {
		return NewAPIError(ReasonConflict, "volume %q is attached to machine %q at epoch %d", v.Metadata.Name, a.MachineName, a.BindingEpoch)
	}
	v.Status.Attachment = &Attachment{MachineName: machine, BindingEpoch: machineEpoch}
	return nil
}
