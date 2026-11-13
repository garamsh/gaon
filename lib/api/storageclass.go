package api

import "regexp"

// The kinds of the StorageClass resource.
const (
	KindStorageClass     = "StorageClass"
	KindStorageClassList = "StorageClassList"
)

// provisionerPattern is the form of a provisioner: the name of the extension
// that serves the class.
var provisionerPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// StorageClass names the provisioner that serves the Volumes of a class,
// and says whether their storage may grow. A Volume names its class by
// spec.storageClass. It has no spec and no status: its fields are its own, as
// those of a ClusterRole are.
type StorageClass struct {
	TypeMeta
	Metadata             ObjectMeta `json:"metadata"`
	Provisioner          string     `json:"provisioner"`
	AllowVolumeExpansion bool       `json:"allowVolumeExpansion,omitempty"`
}

// StorageClassList is the answer to listing StorageClasses.
type StorageClassList struct {
	TypeMeta
	Metadata ListMeta       `json:"metadata"`
	Items    []StorageClass `json:"items"`
}

// Validate checks the identity of c and that its provisioner has the form of a
// provisioner name. The class named StorageClassLocal has the provisioner "local"
// and no other class has it, because Volume.Validate holds the rules of the
// local class by that name.
func (c *StorageClass) Validate() error {
	if err := validateIdentity(c.TypeMeta, GroupVersion, KindStorageClass, c.Metadata); err != nil {
		return err
	}
	if !provisionerPattern.MatchString(c.Provisioner) {
		return invalid("provisioner is %q, want a name matching %s", c.Provisioner, provisionerPattern)
	}
	if (c.Metadata.Name == StorageClassLocal) != (c.Provisioner == StorageClassLocal) {
		return invalid("provisioner %q and name %q: the provisioner %q belongs to the class %q alone", c.Provisioner, c.Metadata.Name, StorageClassLocal, StorageClassLocal)
	}
	return nil
}

// ValidateUpdate checks c as an update of old: Validate, and the same
// provisioner. Only allowVolumeExpansion may change.
func (c *StorageClass) ValidateUpdate(old *StorageClass) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Provisioner != old.Provisioner {
		return invalid("provisioner changes from %q to %q", old.Provisioner, c.Provisioner)
	}
	return nil
}
