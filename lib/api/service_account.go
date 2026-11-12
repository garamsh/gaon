package api

// ServiceAccount is the identity a workload holds: a MicroVM names one in
// spec.serviceAccountName, and the API server signs tokens for it. It has no
// spec and no status.
type ServiceAccount struct {
	TypeMeta
	Metadata ObjectMeta `json:"metadata"`
}

// ServiceAccountList is the answer to listing ServiceAccounts.
type ServiceAccountList struct {
	TypeMeta
	Metadata ListMeta         `json:"metadata"`
	Items    []ServiceAccount `json:"items"`
}

// Validate checks the identity, the name and the metadata of a.
func (a *ServiceAccount) Validate() error {
	if err := validateIdentity(a.TypeMeta, GroupVersion, KindServiceAccount, a.Metadata); err != nil {
		return err
	}
	if err := validateObjectName("metadata.name", a.Metadata.Name); err != nil {
		return err
	}
	return a.Metadata.Validate()
}
