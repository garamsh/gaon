package api

// Verb is an action a rule grants on a resource.
type Verb string

// The verbs a rule grants. VerbAll matches every verb.
const (
	VerbAll    Verb = "*"
	VerbGet    Verb = "get"
	VerbList   Verb = "list"
	VerbWatch  Verb = "watch"
	VerbCreate Verb = "create"
	VerbUpdate Verb = "update"
	VerbDelete Verb = "delete"
)

// ResourceAll and APIGroupAll are the wildcard of a rule: each matches every
// value of its field.
const (
	ResourceAll = "*"
	APIGroupAll = "*"
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
// holds the wildcard. A resource is a name such as "microvms", and a
// subresource is written "microvms/status". A LabelSelector, in the syntax of
// ParseSelector, narrows the rule to the objects whose labels it selects;
// empty, the rule has no such limit.
type PolicyRule struct {
	APIGroups     []string `json:"apiGroups"`
	Resources     []string `json:"resources"`
	Verbs         []Verb   `json:"verbs"`
	LabelSelector string   `json:"labelSelector,omitempty"`
}

// Selector returns the parsed LabelSelector of p.
func (p PolicyRule) Selector() (Selector, error) {
	return ParseSelector(p.LabelSelector)
}

// AllowsLabels reports whether p's label selector lets a request on an object
// with these labels through: always without a selector, otherwise only when the
// labels satisfy it. It judges get, update, delete, and create, which is
// judged on the labels of the object in the request body.
func (p PolicyRule) AllowsLabels(labels map[string]string) bool {
	sel, err := p.Selector()
	return err == nil && sel.Matches(labels)
}

// AllowsSelector reports whether p's label selector lets a list or a watch by
// asked through: always without a selector, otherwise only when asked selects
// nothing outside it. A request without a selector is refused by a rule with
// one.
func (p PolicyRule) AllowsSelector(asked Selector) bool {
	sel, err := p.Selector()
	return err == nil && asked.Within(sel)
}

// Validate checks the identity of r and that each rule lists at least one
// API group, resource and verb, only known verbs, and a label selector that
// parses.
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
	_, err := p.Selector()
	return err
}
