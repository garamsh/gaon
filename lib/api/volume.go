package api

import "slices"

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

// The storage class a Node Agent serves itself, which is also the name and the
// provisioner of the StorageClass the API server creates for it, and the one
// access mode it offers. A Volume of any other class waits Pending for a plugin
// that serves it.
const (
	StorageClassLocal   = "local"
	AccessReadWriteOnce = "ReadWriteOnce"
)

// VolumeConditionResizing is the type of the condition a Volume's status
// carries while spec.storage is larger than status.capacity. Its status is
// True and its reason and message say where the growth stands; the plugin that
// grows the disk removes it once status.capacity has caught up.
const VolumeConditionResizing = "Resizing"

// NodeAgentFinalizer is the finalizer the API server adds to a local Volume
// when it pins the Volume to a Node, and the Node's agent removes once no
// MicroVM has the Volume attached and its disk is deleted. It is the name of
// the MicroVM finalizer of the same owner.
const NodeAgentFinalizer = Group + "/node-agent"

// Volume is a disk a MicroVM can mount.
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
// and it is immutable after, except Storage, which ValidateResize lets grow,
// and NodeName, which only the binding of a MicroVM that uses the Volume
// sets.
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
	Conditions []Condition `json:"conditions,omitempty"`
}

// Attachment names the MicroVM, and the epoch of its binding, that a Volume
// is attached to.
type Attachment struct {
	MicroVMName  string `json:"microvmName"`
	BindingEpoch Epoch  `json:"bindingEpoch"`
}

// Validate checks the identity and the spec of v. A Volume of the local
// class is a disk of a positive size, ReadWriteOnce, whose data source, if
// any, is a name. CreateFromSnapshot holds the rules of one that names a
// VolumeSnapshot.
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
		return validateObjectName("spec.dataSource", spec.DataSource)
	}
	return nil
}

// ValidateCreate checks v as a create: Validate, and no nodeName, which only
// the binding of a MicroVM sets.
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
// included. ValidateResize is the check that lets storage grow.
func (v *Volume) ValidateUpdate(old *Volume) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if !sameJSON(v.Spec, old.Spec) {
		return invalid("spec changes after creation")
	}
	return nil
}

// ValidateResize checks v as an update of old that changes nothing of the spec
// but storage, which it may change as follows. Storage may not fall below
// old's status.capacity, so a Volume never shrinks, and a request for more than
// the disk holds may be lowered down to it, except that a Volume with a data
// source never takes a storage below the one it holds, which create checked
// against the snapshot's restore size. A change of storage on a Volume
// that is being deleted is ErrConflict. A growth, a storage above old's, needs
// a class that allows expansion: class is the StorageClass v names, nil when
// none exists.
func (v *Volume) ValidateResize(old *Volume, class *StorageClass) error {
	if err := v.Validate(); err != nil {
		return err
	}
	frozen := v.Spec
	frozen.Storage = old.Spec.Storage
	if !sameJSON(frozen, old.Spec) {
		return invalid("spec changes a field other than storage")
	}
	want, err := v.Spec.Storage.Value()
	if err != nil {
		return invalid("spec.storage: %v", err)
	}
	was, err := old.Spec.Storage.Value()
	if err != nil {
		return invalid("spec.storage of the stored volume: %v", err)
	}
	if want.Cmp(was) == 0 {
		return nil
	}
	if v.Spec.DataSource != "" && want.Cmp(was) < 0 {
		return invalid("spec.storage is %q, below %q, which was checked against the restore size of volume snapshot %q", v.Spec.Storage, old.Spec.Storage, v.Spec.DataSource)
	}
	have, err := old.Status.Capacity.Value()
	if err != nil {
		return invalid("status.capacity: %v", err)
	}
	if want.Cmp(have) < 0 {
		return invalid("spec.storage is %q, below status.capacity %q: a volume does not shrink", v.Spec.Storage, old.Status.Capacity)
	}
	if old.Metadata.DeletionTimestamp != nil {
		return NewAPIError(ReasonConflict, "volume %q is being deleted", old.Metadata.Name)
	}
	if want.Cmp(was) > 0 {
		if class == nil {
			return invalid("spec.storage grows, and storage class %q does not exist", v.Spec.StorageClass)
		}
		if !class.AllowVolumeExpansion {
			return invalid("spec.storage grows, and storage class %q does not allow volume expansion", v.Spec.StorageClass)
		}
	}
	return nil
}

// CreateFromSnapshot checks the create of v against s, the VolumeSnapshot that
// v.spec.dataSource names, nil when none exists, and for the class local pins
// v to the Node that holds s, as Pin does. It changes nothing of a Volume with
// no data source. A missing snapshot, a storage class other than the
// snapshot's and a storage below its restore size are ErrInvalid; a snapshot
// that is not ready to use, or is being deleted, is ErrConflict. On a refusal v
// is unchanged.
func (v *Volume) CreateFromSnapshot(s *VolumeSnapshot) error {
	if v.Spec.DataSource == "" {
		return nil
	}
	if s == nil {
		return invalid("spec.dataSource names volume snapshot %q, which does not exist", v.Spec.DataSource)
	}
	if s.Metadata.DeletionTimestamp != nil {
		return NewAPIError(ReasonConflict, "volume snapshot %q is being deleted", s.Metadata.Name)
	}
	if !s.Status.ReadyToUse {
		return NewAPIError(ReasonConflict, "volume snapshot %q is not ready to use", s.Metadata.Name)
	}
	if v.Spec.StorageClass != s.Status.StorageClass {
		return invalid("spec.storageClass is %q, want %q, the class of volume snapshot %q", v.Spec.StorageClass, s.Status.StorageClass, s.Metadata.Name)
	}
	want, err := v.Spec.Storage.Value()
	if err != nil {
		return invalid("spec.storage: %v", err)
	}
	floor, err := s.Status.RestoreSize.Value()
	if err != nil {
		return invalid("status.restoreSize of volume snapshot %q: %v", s.Metadata.Name, err)
	}
	if want.Cmp(floor) < 0 {
		return invalid("spec.storage is %q, below %q, the restore size of volume snapshot %q", v.Spec.Storage, s.Status.RestoreSize, s.Metadata.Name)
	}
	return v.Pin(s.Status.NodeName)
}

// Pin pins a local v to node: it sets spec.nodeName and adds
// NodeAgentFinalizer, unless v holds it already, in the same change. It changes nothing of a Volume of
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
	if !slices.Contains(v.Metadata.Finalizers, NodeAgentFinalizer) {
		v.Metadata.Finalizers = append(v.Metadata.Finalizers, NodeAgentFinalizer)
	}
	return nil
}

// PinVolumes pins each Volume m uses, found by name in volumes, to node, for
// the binding of m to node, and returns the Volumes it changed. A name with
// no Volume is ErrInvalid, and a Volume that refuses (see Pin) is
// ErrConflict. On a refusal no Volume is changed.
func (m *MicroVM) PinVolumes(node string, volumes map[string]*Volume) ([]*Volume, error) {
	var used []*Volume
	seen := map[string]bool{}
	for _, mv := range m.Spec.Volumes {
		if seen[mv.Name] {
			continue
		}
		seen[mv.Name] = true
		v, ok := volumes[mv.Name]
		if !ok {
			return nil, invalid("microvm %q uses volume %q, which does not exist", m.Metadata.Name, mv.Name)
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

// ValidateStatus checks that the phase of v's status is a known one and that
// each of its conditions has a known status.
func (v *Volume) ValidateStatus() error {
	switch v.Status.Phase {
	case VolumePending, VolumeAvailable, VolumeBound, VolumeReleased, VolumeFailed:
	default:
		return invalid("status.phase is %q, want Pending, Available, Bound, Released or Failed", v.Status.Phase)
	}
	for _, c := range v.Status.Conditions {
		if err := c.validate(); err != nil {
			return err
		}
	}
	return nil
}

// Attach attaches v to the named MicroVM at its current epoch. It succeeds
// only if v is not being deleted and is unattached or attached at a lower
// epoch; otherwise it returns ErrConflict and leaves v unchanged.
func (v *Volume) Attach(microvm string, microvmEpoch Epoch) error {
	if v.Metadata.DeletionTimestamp != nil {
		return NewAPIError(ReasonConflict, "volume %q is being deleted", v.Metadata.Name)
	}
	if a := v.Status.Attachment; a != nil && a.BindingEpoch >= microvmEpoch {
		return NewAPIError(ReasonConflict, "volume %q is attached to microvm %q at epoch %d", v.Metadata.Name, a.MicroVMName, a.BindingEpoch)
	}
	v.Status.Attachment = &Attachment{MicroVMName: microvm, BindingEpoch: microvmEpoch}
	return nil
}
