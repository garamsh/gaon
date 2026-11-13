package api

// The kinds of the VolumeSnapshot resource.
const (
	KindVolumeSnapshot     = "VolumeSnapshot"
	KindVolumeSnapshotList = "VolumeSnapshotList"
)

// VolumeSnapshotConditionSnapshotting is the type of the condition a
// VolumeSnapshot's status carries from the provisioner's first attempt until
// status.readyToUse is true. Its status is True and its reason and message say
// where the snapshot stands; the provisioner removes it in the write that sets
// readyToUse.
const VolumeSnapshotConditionSnapshotting = "Snapshotting"

// VolumeSnapshot is a copy of a Volume's disk that a Volume can be created
// from.
type VolumeSnapshot struct {
	TypeMeta
	Metadata ObjectMeta           `json:"metadata"`
	Spec     VolumeSnapshotSpec   `json:"spec"`
	Status   VolumeSnapshotStatus `json:"status"`
}

// VolumeSnapshotList is the answer to listing VolumeSnapshots.
type VolumeSnapshotList struct {
	TypeMeta
	Metadata ListMeta         `json:"metadata"`
	Items    []VolumeSnapshot `json:"items"`
}

// VolumeSnapshotSpec is the desired state of a VolumeSnapshot, written by a
// user at creation and immutable after.
type VolumeSnapshotSpec struct {
	VolumeName string `json:"volumeName"`
}

// VolumeSnapshotStatus is the observed state of a VolumeSnapshot, written by
// the provisioner of the source Volume's class. RestoreSize, StorageClass and
// NodeName are set with ReadyToUse and do not change after.
type VolumeSnapshotStatus struct {
	ReadyToUse   bool        `json:"readyToUse"`
	RestoreSize  Quantity    `json:"restoreSize,omitempty"`
	StorageClass string      `json:"storageClass,omitempty"`
	NodeName     string      `json:"nodeName,omitempty"`
	Conditions   []Condition `json:"conditions,omitempty"`
}

// Validate checks the identity of s and that it names a Volume.
func (s *VolumeSnapshot) Validate() error {
	if err := validateIdentity(s.TypeMeta, GroupVersion, KindVolumeSnapshot, s.Metadata); err != nil {
		return err
	}
	return validateObjectName("spec.volumeName", s.Spec.VolumeName)
}

// ValidateUpdate checks that s changes nothing of old's spec.
func (s *VolumeSnapshot) ValidateUpdate(old *VolumeSnapshot) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Spec.VolumeName != old.Spec.VolumeName {
		return invalid("spec.volumeName changes from %q to %q", old.Spec.VolumeName, s.Spec.VolumeName)
	}
	return nil
}

// ValidateStatus checks that each condition of s's status has a known status,
// and that a snapshot that is ready to use carries a positive restore size and
// its storage class, and for the class local the Node that holds it.
func (s *VolumeSnapshot) ValidateStatus() error {
	for _, c := range s.Status.Conditions {
		if err := c.validate(); err != nil {
			return err
		}
	}
	st := s.Status
	if !st.ReadyToUse {
		return nil
	}
	if size, err := st.RestoreSize.Value(); err != nil || size.Sign() <= 0 {
		return invalid("status.restoreSize is %q, want a positive size when readyToUse", st.RestoreSize)
	}
	if st.StorageClass == "" {
		return invalid("status.storageClass is empty when readyToUse")
	}
	if st.StorageClass == StorageClassLocal && st.NodeName == "" {
		return invalid("status.nodeName is empty, want the Node that holds a snapshot of class %s", StorageClassLocal)
	}
	return nil
}

// ValidateStatusUpdate checks s's status as a write over old's: ValidateStatus,
// and once old is ready to use, still ready with the same restore size,
// storage class and Node, since a Volume may have been pinned by them.
func (s *VolumeSnapshot) ValidateStatusUpdate(old *VolumeSnapshot) error {
	if err := s.ValidateStatus(); err != nil {
		return err
	}
	if !old.Status.ReadyToUse {
		return nil
	}
	if !s.Status.ReadyToUse || !sameQuantity(s.Status.RestoreSize, old.Status.RestoreSize) || s.Status.StorageClass != old.Status.StorageClass || s.Status.NodeName != old.Status.NodeName {
		return invalid("status.readyToUse, restoreSize, storageClass and nodeName change after the snapshot was ready to use")
	}
	return nil
}

// sameQuantity reports whether a and b are the same amount, whatever their
// units.
func sameQuantity(a, b Quantity) bool {
	x, errX := a.Value()
	y, errY := b.Value()
	return errX == nil && errY == nil && x.Cmp(y) == 0
}
