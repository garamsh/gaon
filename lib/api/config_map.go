package api

// ConfigMap is a map of configuration values a MicroVM references by key, as
// an environment variable or the content of a file. It has no spec and no
// status.
type ConfigMap struct {
	TypeMeta
	Metadata ObjectMeta        `json:"metadata"`
	Data     map[string]string `json:"data,omitempty"`
}

// ConfigMapList is the answer to listing ConfigMaps.
type ConfigMapList struct {
	TypeMeta
	Metadata ListMeta    `json:"metadata"`
	Items    []ConfigMap `json:"items"`
}

// Validate checks the identity, the name and the metadata of c, and its keys
// and size.
func (c *ConfigMap) Validate() error {
	if err := validateIdentity(c.TypeMeta, GroupVersion, KindConfigMap, c.Metadata); err != nil {
		return err
	}
	if err := validateObjectName("metadata.name", c.Metadata.Name); err != nil {
		return err
	}
	if err := c.Metadata.Validate(); err != nil {
		return err
	}
	return validateData(c.Data)
}
