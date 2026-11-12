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

// Volume is a disk a Machine can mount.
type Volume struct {
	TypeMeta
	Metadata ObjectMeta   `json:"metadata"`
	Spec     VolumeSpec   `json:"spec"`
	Status   VolumeStatus `json:"status"`
}

// VolumeSpec is the desired state of a Volume. NodeName is for node-local
// storage classes only, and set by the binding.
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

// Validate checks the identity of v.
func (v *Volume) Validate() error {
	return validateIdentity(v.TypeMeta, GroupVersion, KindVolume, v.Metadata)
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
// only if v is unattached or attached at a lower epoch; otherwise it returns
// ErrConflict and leaves v unchanged.
func (v *Volume) Attach(machine string, machineEpoch Epoch) error {
	if a := v.Status.Attachment; a != nil && a.BindingEpoch >= machineEpoch {
		return NewAPIError(ReasonConflict, "volume %q is attached to machine %q at epoch %d", v.Metadata.Name, a.MachineName, a.BindingEpoch)
	}
	v.Status.Attachment = &Attachment{MachineName: machine, BindingEpoch: machineEpoch}
	return nil
}
