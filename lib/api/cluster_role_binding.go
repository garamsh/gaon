package api

// SubjectKind says what a Subject names.
type SubjectKind string

// The kinds a Subject names.
const (
	SubjectUser  SubjectKind = "User"
	SubjectGroup SubjectKind = "Group"
)

// ClusterRoleBinding grants the permissions of one ClusterRole to its
// subjects, cluster-wide.
type ClusterRoleBinding struct {
	TypeMeta
	Metadata ObjectMeta `json:"metadata"`
	RoleRef  RoleRef    `json:"roleRef"`
	Subjects []Subject  `json:"subjects"`
}

// ClusterRoleBindingList is the answer to listing ClusterRoleBindings.
type ClusterRoleBindingList struct {
	TypeMeta
	Metadata ListMeta             `json:"metadata"`
	Items    []ClusterRoleBinding `json:"items"`
}

// RoleRef names the ClusterRole a binding grants. It cannot change once the
// binding exists.
type RoleRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Subject is a user, by the CN of its certificate, or a group, by an
// Organization of its subject.
type Subject struct {
	Kind SubjectKind `json:"kind"`
	Name string      `json:"name"`
}

// Validate checks the identity of b, that its roleRef names a ClusterRole and
// that each subject is a User or a Group with a name.
func (b *ClusterRoleBinding) Validate() error {
	if err := validateIdentity(b.TypeMeta, RBACGroupVersion, KindClusterRoleBinding, b.Metadata); err != nil {
		return err
	}
	if b.RoleRef.Kind != KindClusterRole || b.RoleRef.Name == "" {
		return invalid("roleRef is %+v, want kind %s and a name", b.RoleRef, KindClusterRole)
	}
	for i, s := range b.Subjects {
		switch s.Kind {
		case SubjectUser, SubjectGroup:
		default:
			return invalid("subjects[%d] has kind %q, want User or Group", i, s.Kind)
		}
		if s.Name == "" {
			return invalid("subjects[%d] has no name", i)
		}
	}
	return nil
}
