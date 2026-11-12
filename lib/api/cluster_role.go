package api

// VerbAll, ResourceAll and APIGroupAll are the wildcard of a rule: it matches
// every value of its field.
const (
	VerbAll     = "*"
	ResourceAll = "*"
	APIGroupAll = "*"
)

// The verbs a rule grants, besides VerbAll.
const (
	VerbGet    = "get"
	VerbList   = "list"
	VerbWatch  = "watch"
	VerbCreate = "create"
	VerbUpdate = "update"
	VerbDelete = "delete"
)

// ClusterRole is a set of permissions, cluster-wide. It grants nothing until
// a ClusterRoleBinding names it.
type ClusterRole struct {
	TypeMeta
	Metadata ObjectMeta   `json:"metadata"`
	Rules    []PolicyRule `json:"rules"`
}

// ClusterRoleList is the answer to listing ClusterRoles.
type ClusterRoleList struct {
	TypeMeta
	Metadata ListMeta      `json:"metadata"`
	Items    []ClusterRole `json:"items"`
}

// PolicyRule grants the verbs on the resources of the API groups. A request
// matches it when each of its group, resource and verb is listed or the list
// holds the wildcard. A resource is a name such as "machines", and a
// subresource is written "machines/status".
type PolicyRule struct {
	APIGroups []string `json:"apiGroups"`
	Resources []string `json:"resources"`
	Verbs     []string `json:"verbs"`
}

// Validate checks the identity of r and that each rule lists at least one
// API group, resource and verb, and only known verbs.
func (r *ClusterRole) Validate() error {
	if err := validateIdentity(r.TypeMeta, RBACGroupVersion, KindClusterRole, r.Metadata); err != nil {
		return err
	}
	for i, rule := range r.Rules {
		if err := rule.validate(); err != nil {
			return invalid("rules[%d]: %v", i, err)
		}
	}
	return nil
}

func (p PolicyRule) validate() error {
	if len(p.APIGroups) == 0 {
		return invalid("apiGroups is empty")
	}
	if len(p.Resources) == 0 {
		return invalid("resources is empty")
	}
	for _, v := range append(append([]string{}, p.APIGroups...), p.Resources...) {
		if v == "" {
			return invalid("apiGroups or resources holds an empty name")
		}
	}
	if len(p.Verbs) == 0 {
		return invalid("verbs is empty")
	}
	for _, v := range p.Verbs {
		switch v {
		case VerbAll, VerbGet, VerbList, VerbWatch, VerbCreate, VerbUpdate, VerbDelete:
		default:
			return invalid("verb %q is not one of *, get, list, watch, create, update, delete", v)
		}
	}
	return nil
}
