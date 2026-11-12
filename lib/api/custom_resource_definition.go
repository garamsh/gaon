package api

import (
	"regexp"
	"slices"
)

// ResourceScope says whether the objects of a definition belong to a namespace.
type ResourceScope string

// ScopeCluster is the one scope: gaon has no namespaces yet.
const ScopeCluster ResourceScope = "Cluster"

// The condition types of a CustomResourceDefinition, written by the API
// server. CRD is the short form of the resource's name.
const (
	CRDNamesAccepted = "NamesAccepted"
	CRDEstablished   = "Established"
)

// reservedGroups are the groups the core serves. A definition cannot take one.
var reservedGroups = []string{Group, RBACGroup, APIExtensionsGroup, AdmissionRegistrationGroup, AdmissionGroup}

var (
	dnsLabel     = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)
	dnsSubdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$`)
	kindName     = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	versionName  = regexp.MustCompile(`^v[1-9][0-9]*((alpha|beta)[1-9][0-9]*)?$`)
)

const maxDefinitionLen = 253

// CustomResourceDefinition registers a resource kind. Its name is
// <plural>.<group>. The API server serves the kind's objects once the
// definition is Established.
type CustomResourceDefinition struct {
	TypeMeta
	Metadata ObjectMeta                     `json:"metadata"`
	Spec     CustomResourceDefinitionSpec   `json:"spec"`
	Status   CustomResourceDefinitionStatus `json:"status"`
}

// CustomResourceDefinitionList is the answer to listing
// CustomResourceDefinitions.
type CustomResourceDefinitionList struct {
	TypeMeta
	Metadata ListMeta                   `json:"metadata"`
	Items    []CustomResourceDefinition `json:"items"`
}

// CustomResourceDefinitionSpec is what a user registers: the group, the names
// of the kind, its scope and its one version.
type CustomResourceDefinitionSpec struct {
	Group    string                  `json:"group"`
	Names    CustomResourceNames     `json:"names"`
	Scope    ResourceScope           `json:"scope"`
	Versions []CustomResourceVersion `json:"versions"`
}

// CustomResourceNames are the names of a kind.
type CustomResourceNames struct {
	Kind     string `json:"kind"`
	ListKind string `json:"listKind"`
	Plural   string `json:"plural"`
	Singular string `json:"singular"`
}

// CustomResourceVersion is a version of the kind: whether it is served and
// stored, its schema and its subresources.
type CustomResourceVersion struct {
	Name         string                      `json:"name"`
	Served       bool                        `json:"served"`
	Storage      bool                        `json:"storage"`
	Schema       CustomResourceValidation    `json:"schema"`
	Subresources *CustomResourceSubresources `json:"subresources,omitempty"`
}

// CustomResourceValidation holds the schema of the objects of a version.
type CustomResourceValidation struct {
	OpenAPIV3Schema *JSONSchemaProps `json:"openAPIV3Schema"`
}

// CustomResourceSubresources lists the optional subresources of a version.
type CustomResourceSubresources struct {
	Status *StatusSubresource `json:"status,omitempty"`
}

// StatusSubresource enables the /status path. Its JSON is {}.
type StatusSubresource struct{}

// CustomResourceDefinitionStatus is written by the API server: the
// conditions NamesAccepted and Established.
type CustomResourceDefinitionStatus struct {
	Conditions []Condition `json:"conditions,omitempty"`
}

// Validate checks the identity of d, that its name is <plural>.<group>, and
// the rules of its spec.
func (d *CustomResourceDefinition) Validate() error {
	if err := validateIdentity(d.TypeMeta, APIExtensionsGroupVersion, KindCustomResourceDefinition, d.Metadata); err != nil {
		return err
	}
	if want := d.Spec.Names.Plural + "." + d.Spec.Group; d.Metadata.Name != want {
		return invalid("metadata.name is %q, want <plural>.<group>, %q", d.Metadata.Name, want)
	}
	if len(d.Metadata.Name) > maxDefinitionLen {
		return invalid("metadata.name is longer than %d characters", maxDefinitionLen)
	}
	if err := d.validateGroup(); err != nil {
		return err
	}
	if err := d.Spec.Names.validate(); err != nil {
		return err
	}
	if d.Spec.Scope != ScopeCluster {
		return invalid("scope is %q, want %q", d.Spec.Scope, ScopeCluster)
	}
	return d.validateVersion()
}

func (d *CustomResourceDefinition) validateGroup() error {
	g := d.Spec.Group
	if !dnsSubdomain.MatchString(g) {
		return invalid("group %q is not a domain of at least two lowercase labels", g)
	}
	if slices.Contains(reservedGroups, g) {
		return invalid("group %q is served by the core", g)
	}
	return nil
}

func (n CustomResourceNames) validate() error {
	for field, name := range map[string]string{"plural": n.Plural, "singular": n.Singular} {
		if !dnsLabel.MatchString(name) {
			return invalid("names.%s is %q, want a lowercase label", field, name)
		}
	}
	for field, name := range map[string]string{"kind": n.Kind, "listKind": n.ListKind} {
		if !kindName.MatchString(name) {
			return invalid("names.%s is %q, want a name that starts with an uppercase letter", field, name)
		}
	}
	if n.Kind == n.ListKind {
		return invalid("names.kind and names.listKind are both %q", n.Kind)
	}
	return nil
}

func (d *CustomResourceDefinition) validateVersion() error {
	if len(d.Spec.Versions) != 1 {
		return invalid("versions has %d entries, want exactly one", len(d.Spec.Versions))
	}
	v := d.Spec.Versions[0]
	if !versionName.MatchString(v.Name) {
		return invalid("versions[0].name is %q, want a version such as v1, v1alpha1 or v2beta3", v.Name)
	}
	if !v.Served || !v.Storage {
		return invalid("versions[0] must be served and stored")
	}
	if v.Schema.OpenAPIV3Schema == nil {
		return invalid("versions[0].schema.openAPIV3Schema is missing")
	}
	if err := v.Schema.OpenAPIV3Schema.validateRoot(); err != nil {
		return invalid("versions[0]: %v", err)
	}
	return nil
}

// ValidateUpdate checks that d, an update of old, keeps what the stored
// objects depend on: the group, the names and the version name. Validate
// checks the rest.
func (d *CustomResourceDefinition) ValidateUpdate(old *CustomResourceDefinition) error {
	if d.Spec.Group != old.Spec.Group {
		return invalid("group is immutable")
	}
	if d.Spec.Names != old.Spec.Names {
		return invalid("names are immutable")
	}
	if len(d.Spec.Versions) == 1 && len(old.Spec.Versions) == 1 && d.Spec.Versions[0].Name != old.Spec.Versions[0].Name {
		return invalid("the version name is immutable")
	}
	return nil
}

// ValidateStatus checks that each condition of d's status has a known type
// and a known status.
func (d *CustomResourceDefinition) ValidateStatus() error {
	for _, c := range d.Status.Conditions {
		switch c.Type {
		case CRDNamesAccepted, CRDEstablished:
		default:
			return invalid("condition type is %q, want NamesAccepted or Established", c.Type)
		}
		if err := c.validate(); err != nil {
			return err
		}
	}
	return nil
}
