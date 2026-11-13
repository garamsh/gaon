package job

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// The Deployment kind's identity and the labels the unit puts on the MicroVMs
// it creates for one.
const (
	KindDeployment     = "Deployment"
	PluralDeployment   = "deployments"
	SingularDeployment = "deployment"

	// DeploymentLabel carries the name of the Deployment a MicroVM belongs to.
	DeploymentLabel = Group + "/deployment"
	// TemplateHashLabel carries the hash of the template a MicroVM was made
	// from. A MicroVM is updated when it equals the hash of the current one.
	TemplateHashLabel = Group + "/template-hash"
)

// The strategy types of a Deployment.
const (
	StrategyRollingUpdate = "RollingUpdate"
	StrategyRecreate      = "Recreate"
)

// DefaultRollingBound is the maxSurge and the maxUnavailable of a rolling
// update that names none.
const DefaultRollingBound Bound = "25%"

// BoundPattern is the schema's pattern of the string form of maxSurge and
// maxUnavailable: a whole number, or a whole number of percent.
const BoundPattern = `^(0|[1-9][0-9]{0,8})%?$`

var boundExpr = regexp.MustCompile(BoundPattern)

// DeploymentType is the Deployment kind as the custom resource client names it.
var DeploymentType = api.CustomResourceType{Group: Group, Version: Version, Kind: KindDeployment, Plural: PluralDeployment}

// LabelSelector names the labels every replica carries.
type LabelSelector struct {
	MatchLabels map[string]string `json:"matchLabels"`
}

// Bound is a whole number or a whole number of percent, in its string form.
// It reads a JSON integer or a JSON string.
type Bound string

// UnmarshalJSON reads an integer as its digits and a string as itself, and
// refuses any other JSON value.
func (b *Bound) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		return json.Unmarshal(data, (*string)(b))
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("want an integer or a string: %w", err)
	}
	*b = Bound(n)
	return nil
}

// RollingUpdate bounds a rollout: MaxSurge replicas may exist above Replicas
// and MaxUnavailable below it may be not Ready. Each is a number or a
// percentage of Replicas.
type RollingUpdate struct {
	MaxSurge       Bound `json:"maxSurge,omitempty"`
	MaxUnavailable Bound `json:"maxUnavailable,omitempty"`
}

// Strategy is how a changed template replaces the replicas made from the old
// one. An absent Type is RollingUpdate.
type Strategy struct {
	Type          string         `json:"type,omitempty"`
	RollingUpdate *RollingUpdate `json:"rollingUpdate,omitempty"`
}

// DeploymentSpec is the spec of a Deployment: Replicas MicroVMs made from
// Template, each carrying Selector's labels.
type DeploymentSpec struct {
	Replicas int32           `json:"replicas"`
	Selector LabelSelector   `json:"selector"`
	Template api.MicroVMSpec `json:"template"`
	Strategy Strategy        `json:"strategy,omitempty"`
}

// DeploymentStatus is the status of a Deployment, counted from the MicroVMs it
// owns that are not being deleted and have not failed.
type DeploymentStatus struct {
	ObservedGeneration int64 `json:"observedGeneration"`
	Replicas           int32 `json:"replicas"`
	ReadyReplicas      int32 `json:"readyReplicas"`
	UpdatedReplicas    int32 `json:"updatedReplicas"`
	AvailableReplicas  int32 `json:"availableReplicas"`
}

// bounds returns how many replicas a rollout may add above replicas and how
// many below may be not Ready. A surge rounds up and an unavailability rounds
// down; both zero would stop a rollout, so one replica may then be unavailable.
func (s Strategy) bounds(replicas int) (surge, unavailable int, err error) {
	switch s.Type {
	case StrategyRecreate:
		return 0, replicas, nil
	case "", StrategyRollingUpdate:
	default:
		return 0, 0, fmt.Errorf("spec.strategy.type is %q, want %s or %s", s.Type, StrategyRollingUpdate, StrategyRecreate)
	}
	var in RollingUpdate
	if s.RollingUpdate != nil {
		in = *s.RollingUpdate
	}
	if surge, err = resolveBound("maxSurge", in.MaxSurge, replicas, true); err != nil {
		return 0, 0, err
	}
	if unavailable, err = resolveBound("maxUnavailable", in.MaxUnavailable, replicas, false); err != nil {
		return 0, 0, err
	}
	if surge == 0 && unavailable == 0 {
		unavailable = 1
	}
	return surge, unavailable, nil
}

func resolveBound(name string, value Bound, replicas int, roundUp bool) (int, error) {
	if value == "" {
		value = DefaultRollingBound
	}
	if !boundExpr.MatchString(string(value)) {
		return 0, fmt.Errorf("spec.strategy.rollingUpdate.%s is %q, want a number or a percentage", name, value)
	}
	number, isPercent := strings.CutSuffix(string(value), "%")
	n, err := strconv.Atoi(number)
	if err != nil {
		return 0, err
	}
	if !isPercent {
		return n, nil
	}
	total := replicas * n
	if roundUp {
		total += 99
	}
	return min(total/100, math.MaxInt32), nil
}

// replicaTemplate is the spec of a replica: the template, always running and
// not bound.
func replicaTemplate(t api.MicroVMSpec) api.MicroVMSpec {
	t.RunStrategy = api.RunAlways
	t.NodeName, t.BindingEpoch = "", 0
	return t
}

// templateHash names a replica template for the label of the MicroVMs made
// from it.
func templateHash(t api.MicroVMSpec) (string, error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:10], nil
}

// DeploymentDefinition returns the CustomResourceDefinition of the Deployment
// kind.
func DeploymentDefinition() *api.CustomResourceDefinition {
	zero := 0.0
	count := func(desc string) api.JSONSchemaProps { return integerSchema(desc, &zero) }
	bound := func(desc string) api.JSONSchemaProps {
		return api.JSONSchemaProps{IntOrString: true, Description: desc, Pattern: BoundPattern}
	}
	spec := api.JSONSchemaProps{
		Type:     api.SchemaObject,
		Required: []string{"replicas", "selector", "template"},
		Properties: map[string]api.JSONSchemaProps{
			"replicas": count("MicroVMs to keep"),
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
			"strategy": {
				Type: api.SchemaObject,
				Properties: map[string]api.JSONSchemaProps{
					"type": {
						Type: api.SchemaString, Description: "RollingUpdate when absent",
						Enum: []json.RawMessage{json.RawMessage(`"` + StrategyRollingUpdate + `"`), json.RawMessage(`"` + StrategyRecreate + `"`)},
					},
					"rollingUpdate": {
						Type: api.SchemaObject,
						Properties: map[string]api.JSONSchemaProps{
							"maxSurge":       bound("replicas above spec.replicas during a rollout, an integer or a string of a number or a percentage; 25% when absent"),
							"maxUnavailable": bound("replicas below spec.replicas that may be not Ready, an integer or a string of a number or a percentage; 25% when absent"),
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
			"availableReplicas":  count("of those, the ones that are available: Ready"),
		},
	}
	root := api.JSONSchemaProps{Type: api.SchemaObject, Properties: map[string]api.JSONSchemaProps{"spec": spec, "status": status}}
	return &api.CustomResourceDefinition{
		TypeMeta: api.TypeMeta{APIVersion: api.APIExtensionsGroupVersion, Kind: api.KindCustomResourceDefinition},
		Metadata: api.ObjectMeta{Name: PluralDeployment + "." + Group, Labels: map[string]string{ManagedByLabel: Manager}},
		Spec: api.CustomResourceDefinitionSpec{
			Group: Group,
			Scope: api.ScopeCluster,
			Names: api.CustomResourceNames{Kind: KindDeployment, ListKind: KindDeployment + "List", Plural: PluralDeployment, Singular: SingularDeployment},
			Versions: []api.CustomResourceVersion{{
				Name: Version, Served: true, Storage: true,
				Schema:       api.CustomResourceValidation{OpenAPIV3Schema: &root},
				Subresources: &api.CustomResourceSubresources{Status: &api.StatusSubresource{}},
			}},
		},
	}
}
