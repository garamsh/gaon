package job

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// The StatefulSet kind's identity and the labels the unit puts on the MicroVMs
// and the Volumes it creates for one.
const (
	KindStatefulSet     = "StatefulSet"
	PluralStatefulSet   = "statefulsets"
	SingularStatefulSet = "statefulset"

	// StatefulSetLabel carries the name of the StatefulSet a MicroVM or a
	// Volume belongs to.
	StatefulSetLabel = Group + "/statefulset"
	// OrdinalLabel carries a replica's ordinal, and ReplicaNameLabel its name,
	// so that whatever serves a name can select one replica by label.
	OrdinalLabel     = Group + "/ordinal"
	ReplicaNameLabel = Group + "/replica-name"
)

// The pod management policies and the update types of a StatefulSet.
const (
	ManagementOrderedReady = "OrderedReady"
	ManagementParallel     = "Parallel"
	UpdateOnDelete         = "OnDelete"
)

// StatefulSetType is the StatefulSet kind as the custom resource client names it.
var StatefulSetType = api.CustomResourceType{Group: Group, Version: Version, Kind: KindStatefulSet, Plural: PluralStatefulSet}

// VolumeClaimTemplate is the spec of the Volume each replica gets for the
// entry of the template's volumes that carries Name.
type VolumeClaimTemplate struct {
	Name string         `json:"name"`
	Spec api.VolumeSpec `json:"spec"`
}

// StatefulRollingUpdate keeps the replicas below Partition out of a rollout.
type StatefulRollingUpdate struct {
	Partition int32 `json:"partition,omitempty"`
}

// UpdateStrategy is how a changed template replaces the replicas made from the
// old one. An absent Type is RollingUpdate.
type UpdateStrategy struct {
	Type          string                 `json:"type,omitempty"`
	RollingUpdate *StatefulRollingUpdate `json:"rollingUpdate,omitempty"`
}

// StatefulSetSpec is the spec of a StatefulSet: Replicas MicroVMs named
// <name>-<ordinal> made from Template, each with a Volume of its own for every
// claim template.
type StatefulSetSpec struct {
	Replicas             int32                 `json:"replicas"`
	Selector             LabelSelector         `json:"selector"`
	Template             api.MicroVMSpec       `json:"template"`
	VolumeClaimTemplates []VolumeClaimTemplate `json:"volumeClaimTemplates,omitempty"`
	PodManagementPolicy  string                `json:"podManagementPolicy,omitempty"`
	UpdateStrategy       UpdateStrategy        `json:"updateStrategy,omitempty"`
}

// StatefulSetStatus is the status of a StatefulSet, counted from the MicroVMs
// it owns that are not being deleted and have not failed.
type StatefulSetStatus struct {
	ObservedGeneration int64 `json:"observedGeneration"`
	Replicas           int32 `json:"replicas"`
	ReadyReplicas      int32 `json:"readyReplicas"`
	UpdatedReplicas    int32 `json:"updatedReplicas"`
}

// rollout is what a spec asks of the replicas: how many, in which order and
// how a change of the template reaches them.
type rollout struct {
	replicas  int
	parallel  bool
	onDelete  bool
	partition int
}

// rollout reads the policy and the update strategy. It refuses a value the
// schema should have refused and a claim template that no entry of the
// template's volumes mounts.
func (s StatefulSetSpec) rollout() (rollout, error) {
	r := rollout{replicas: int(s.Replicas)}
	switch s.PodManagementPolicy {
	case "", ManagementOrderedReady:
	case ManagementParallel:
		r.parallel = true
	default:
		return r, fmt.Errorf("spec.podManagementPolicy is %q, want %s or %s", s.PodManagementPolicy, ManagementOrderedReady, ManagementParallel)
	}
	switch s.UpdateStrategy.Type {
	case "", StrategyRollingUpdate:
		if ru := s.UpdateStrategy.RollingUpdate; ru != nil {
			if ru.Partition < 0 {
				return r, fmt.Errorf("spec.updateStrategy.rollingUpdate.partition is %d, want at least 0", ru.Partition)
			}
			r.partition = int(ru.Partition)
		}
	case UpdateOnDelete:
		r.onDelete = true
	default:
		return r, fmt.Errorf("spec.updateStrategy.type is %q, want %s or %s", s.UpdateStrategy.Type, StrategyRollingUpdate, UpdateOnDelete)
	}
	seen := map[string]bool{}
	for _, c := range s.VolumeClaimTemplates {
		if c.Name == "" || seen[c.Name] {
			return r, fmt.Errorf("spec.volumeClaimTemplates names %q, want a name that is set and unique", c.Name)
		}
		seen[c.Name] = true
		if !mounts(s.Template.Volumes, c.Name) {
			return r, fmt.Errorf("spec.volumeClaimTemplates %q is mounted by no entry of spec.template.volumes", c.Name)
		}
	}
	return r, nil
}

func mounts(volumes []api.MicroVMVolume, name string) bool {
	for _, v := range volumes {
		if v.Name == name {
			return true
		}
	}
	return false
}

// volumeName is the Volume of replica ordinal of the set for the claim template.
func volumeName(claim, set string, ordinal int) string {
	return claim + "-" + set + "-" + strconv.Itoa(ordinal)
}

// replicaSpec is the template for replica ordinal: each entry of its volumes
// that carries a claim template's name names that replica's Volume instead.
func replicaSpec(set string, spec StatefulSetSpec, tmpl api.MicroVMSpec, ordinal int) api.MicroVMSpec {
	tmpl.Volumes = append([]api.MicroVMVolume(nil), tmpl.Volumes...)
	for i, v := range tmpl.Volumes {
		for _, c := range spec.VolumeClaimTemplates {
			if v.Name == c.Name {
				tmpl.Volumes[i].Name = volumeName(c.Name, set, ordinal)
			}
		}
	}
	return tmpl
}

// ordinalOf returns the ordinal a MicroVM named name has as a replica of the
// set: a name of the form <set>-<n> with n written without sign or leading zero.
func ordinalOf(set, name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, set+"-")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 || strconv.Itoa(n) != rest {
		return 0, false
	}
	return n, true
}

// StatefulSetDefinition returns the CustomResourceDefinition of the StatefulSet
// kind.
func StatefulSetDefinition() *api.CustomResourceDefinition {
	zero := 0.0
	count := func(desc string) api.JSONSchemaProps { return integerSchema(desc, &zero) }
	str := func(desc string) api.JSONSchemaProps {
		return api.JSONSchemaProps{Type: api.SchemaString, Description: desc}
	}
	enum := func(values ...string) []json.RawMessage {
		var out []json.RawMessage
		for _, v := range values {
			out = append(out, json.RawMessage(`"`+v+`"`))
		}
		return out
	}
	spec := api.JSONSchemaProps{
		Type:     api.SchemaObject,
		Required: []string{"replicas", "selector", "template"},
		Properties: map[string]api.JSONSchemaProps{
			"replicas": count("MicroVMs to keep, named <name>-<ordinal>"),
			"selector": {
				Type: api.SchemaObject, Required: []string{"matchLabels"},
				Properties: map[string]api.JSONSchemaProps{
					"matchLabels": {Type: api.SchemaObject, Description: "labels every replica carries; must not be empty", PreserveUnknownFields: true},
				},
			},
			"template": {
				Type: api.SchemaObject, Description: "the spec of each MicroVM; runStrategy is forced to Always",
				PreserveUnknownFields: true,
			},
			"volumeClaimTemplates": {
				Type: api.SchemaArray, Description: "a Volume per replica for each entry; the template's volumes entry of the same name mounts it",
				Items: &api.JSONSchemaProps{
					Type: api.SchemaObject, Required: []string{"name", "spec"},
					Properties: map[string]api.JSONSchemaProps{
						"name": str("the name of the template's volumes entry that mounts the Volume"),
						"spec": {
							Type: api.SchemaObject, Required: []string{"storageClass"},
							Properties: map[string]api.JSONSchemaProps{
								"storageClass": str("the class of each Volume"),
								"accessModes":  {Type: api.SchemaArray, Items: &api.JSONSchemaProps{Type: api.SchemaString}},
								"storage":      str("the size of each Volume"),
								"dataSource":   str("what each Volume is made from, for a class that serves one"),
							},
						},
					},
				},
			},
			"podManagementPolicy": {
				Type: api.SchemaString, Description: "OrderedReady when absent", Enum: enum(ManagementOrderedReady, ManagementParallel),
			},
			"updateStrategy": {
				Type: api.SchemaObject,
				Properties: map[string]api.JSONSchemaProps{
					"type": {Type: api.SchemaString, Description: "RollingUpdate when absent", Enum: enum(StrategyRollingUpdate, UpdateOnDelete)},
					"rollingUpdate": {
						Type: api.SchemaObject,
						Properties: map[string]api.JSONSchemaProps{
							"partition": count("the replicas below this ordinal are not replaced; 0 when absent"),
						},
					},
				},
			},
		},
	}
	status := api.JSONSchemaProps{
		Type: api.SchemaObject,
		Properties: map[string]api.JSONSchemaProps{
			"observedGeneration": count("the generation the counts were taken for"),
			"replicas":           count("owned MicroVMs that are not being deleted and have not failed"),
			"readyReplicas":      count("of those, the ones whose Ready condition is true"),
			"updatedReplicas":    count("of those, the ones made from the current template"),
		},
	}
	root := api.JSONSchemaProps{Type: api.SchemaObject, Properties: map[string]api.JSONSchemaProps{"spec": spec, "status": status}}
	return &api.CustomResourceDefinition{
		TypeMeta: api.TypeMeta{APIVersion: api.APIExtensionsGroupVersion, Kind: api.KindCustomResourceDefinition},
		Metadata: api.ObjectMeta{Name: PluralStatefulSet + "." + Group, Labels: map[string]string{ManagedByLabel: Manager}},
		Spec: api.CustomResourceDefinitionSpec{
			Group: Group,
			Scope: api.ScopeCluster,
			Names: api.CustomResourceNames{Kind: KindStatefulSet, ListKind: KindStatefulSet + "List", Plural: PluralStatefulSet, Singular: SingularStatefulSet},
			Versions: []api.CustomResourceVersion{{
				Name: Version, Served: true, Storage: true,
				Schema:       api.CustomResourceValidation{OpenAPIV3Schema: &root},
				Subresources: &api.CustomResourceSubresources{Status: &api.StatusSubresource{}},
			}},
		},
	}
}
