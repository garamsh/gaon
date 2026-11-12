package api

import (
	"crypto/rand"
	"fmt"
	"slices"
)

// NewUID returns a random version 4 UUID, the form of a UID the server sets.
func NewUID() UID {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("read random bytes: %v", err))
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return UID(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]))
}

// Validate checks the labels, the annotations and the owner references of m.
// It does not check the name, which the kind does, or the UID, which the
// server sets at create and an update may not change (ValidateUpdate).
func (m ObjectMeta) Validate() error {
	if err := ValidateLabels(m.Labels); err != nil {
		return invalid("metadata.labels: %v", err)
	}
	if err := ValidateAnnotations(m.Annotations); err != nil {
		return invalid("metadata.annotations: %v", err)
	}
	controllers := 0
	for i, ref := range m.OwnerReferences {
		if ref.APIVersion == "" || ref.Kind == "" || ref.Name == "" {
			return invalid("metadata.ownerReferences[%d]: apiVersion, kind and name are required", i)
		}
		if ref.Controller {
			controllers++
		}
	}
	if controllers > 1 {
		return invalid("metadata.ownerReferences: %d owners are the controller, want at most one", controllers)
	}
	return nil
}

// ValidateCreate checks m as the metadata of a create: Validate, and a UID on
// every owner reference. A UID m carries is replaced by the server's.
func (m ObjectMeta) ValidateCreate() error {
	if err := m.Validate(); err != nil {
		return err
	}
	for i, ref := range m.OwnerReferences {
		if ref.UID == "" {
			return invalid("metadata.ownerReferences[%d]: uid is required", i)
		}
	}
	return nil
}

// ValidateUpdate checks m as the metadata of an update of old: Validate, a UID
// that is empty or old's, and a UID on every owner reference that old does not
// hold. An object stored before UIDs existed has none, and its references may
// have none, until it is written.
func (m ObjectMeta) ValidateUpdate(old ObjectMeta) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if m.UID != "" && old.UID != "" && m.UID != old.UID {
		return invalid("metadata.uid is %q, want the stored %q: a uid never changes", m.UID, old.UID)
	}
	for i, ref := range m.OwnerReferences {
		if ref.UID == "" && !slices.Contains(old.OwnerReferences, ref) {
			return invalid("metadata.ownerReferences[%d]: uid is required on a reference the object did not hold", i)
		}
	}
	return nil
}
