// Package job serves the workload kinds: the Job, a run-to-completion workload
// that runs MicroVMs from a template until enough of them succeed, the CronJob,
// which creates a Job on a schedule, and the Deployment, which keeps a number
// of MicroVMs running from a template and rolls a change of it out.
package job

import (
	"github.com/garamsh/gaon/lib/api"
)

// The kind's identity and the labels the unit puts on what it writes.
const (
	Group      = "workloads.gaon.garam.sh"
	Version    = "v1alpha1"
	APIVersion = Group + "/" + Version
	Kind       = "Job"
	Plural     = "jobs"
	Singular   = "job"

	// ManagedByLabel marks the definition and the MicroVMs the unit wrote.
	ManagedByLabel = Group + "/managed-by"
	// Manager is the value of ManagedByLabel and the unit's certificate name.
	Manager = "gaon-workloads"
	// JobLabel carries the name of the Job a MicroVM belongs to.
	JobLabel = Group + "/job"
)

// The condition types of a Job. At most one is True.
const (
	CondComplete = "Complete"
	CondFailed   = "Failed"
)

// The reasons of a Failed condition.
const (
	ReasonBackoffLimitExceeded = "BackoffLimitExceeded"
	ReasonInvalidTemplate      = "InvalidTemplate"
)

// Type is the Job kind as the custom resource client names it.
var Type = api.CustomResourceType{Group: Group, Version: Version, Kind: Kind, Plural: Plural}

// Spec is the spec of a Job. The Job is complete once Completions MicroVMs
// made from Template have succeeded, and failed once more than BackoffLimit
// have failed.
type Spec struct {
	Completions  int32           `json:"completions"`
	BackoffLimit int32           `json:"backoffLimit,omitempty"`
	Template     api.MicroVMSpec `json:"template"`
}

// Status is the status of a Job, counted from the MicroVMs it owns.
type Status struct {
	Active     int32           `json:"active"`
	Succeeded  int32           `json:"succeeded"`
	Failed     int32           `json:"failed"`
	Conditions []api.Condition `json:"conditions,omitempty"`
}

// finished reports whether the status carries a condition that is True.
func (s Status) finished() bool {
	for _, c := range s.Conditions {
		if c.Status == api.ConditionTrue {
			return true
		}
	}
	return false
}

// jobSpecSchema is the schema of a Job's spec, which a CronJob's jobTemplate
// shares.
func jobSpecSchema() api.JSONSchemaProps {
	one, zero := 1.0, 0.0
	return api.JSONSchemaProps{
		Type:     api.SchemaObject,
		Required: []string{"completions", "template"},
		Properties: map[string]api.JSONSchemaProps{
			"completions":  integerSchema("MicroVMs that must succeed", &one),
			"backoffLimit": integerSchema("MicroVMs that may fail before the Job fails; zero when absent", &zero),
			"template": {
				Type: api.SchemaObject, Description: "the spec of each MicroVM; runStrategy is forced to Once",
				PreserveUnknownFields: true,
			},
		},
	}
}

func integerSchema(desc string, min *float64) api.JSONSchemaProps {
	return api.JSONSchemaProps{Type: api.SchemaInteger, Description: desc, Minimum: min}
}

// Definition returns the CustomResourceDefinition of the Job kind.
func Definition() *api.CustomResourceDefinition {
	zero := 0.0
	spec := jobSpecSchema()
	condition := api.JSONSchemaProps{
		Type:     api.SchemaObject,
		Required: []string{"type", "status"},
		Properties: map[string]api.JSONSchemaProps{
			"type":               {Type: api.SchemaString},
			"status":             {Type: api.SchemaString},
			"reason":             {Type: api.SchemaString},
			"message":            {Type: api.SchemaString},
			"lastTransitionTime": {Type: api.SchemaString},
		},
	}
	status := api.JSONSchemaProps{
		Type: api.SchemaObject,
		Properties: map[string]api.JSONSchemaProps{
			"active":     integerSchema("owned MicroVMs that have not ended", &zero),
			"succeeded":  integerSchema("owned MicroVMs that succeeded", &zero),
			"failed":     integerSchema("owned MicroVMs that failed", &zero),
			"conditions": {Type: api.SchemaArray, Items: &condition},
		},
	}
	root := api.JSONSchemaProps{Type: api.SchemaObject, Properties: map[string]api.JSONSchemaProps{"spec": spec, "status": status}}
	return &api.CustomResourceDefinition{
		TypeMeta: api.TypeMeta{APIVersion: api.APIExtensionsGroupVersion, Kind: api.KindCustomResourceDefinition},
		Metadata: api.ObjectMeta{Name: Plural + "." + Group, Labels: map[string]string{ManagedByLabel: Manager}},
		Spec: api.CustomResourceDefinitionSpec{
			Group: Group,
			Scope: api.ScopeCluster,
			Names: api.CustomResourceNames{Kind: Kind, ListKind: Kind + "List", Plural: Plural, Singular: Singular},
			Versions: []api.CustomResourceVersion{{
				Name: Version, Served: true, Storage: true,
				Schema:       api.CustomResourceValidation{OpenAPIV3Schema: &root},
				Subresources: &api.CustomResourceSubresources{Status: &api.StatusSubresource{}},
			}},
		},
	}
}
