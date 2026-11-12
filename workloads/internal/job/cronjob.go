package job

import (
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// The CronJob kind's identity and the labels the unit puts on the Jobs it
// creates.
const (
	KindCronJob     = "CronJob"
	PluralCronJob   = "cronjobs"
	SingularCronJob = "cronjob"

	// CronJobLabel carries the name of the CronJob a Job was created by.
	CronJobLabel = Group + "/cronjob"
	// ScheduledLabel carries the minute a Job was scheduled for, as Unix minutes.
	ScheduledLabel = Group + "/scheduled-minute"
)

// DefaultHistoryLimit is the number of finished Jobs a CronJob keeps when its
// spec sets none.
const DefaultHistoryLimit = 3

// CronJobType is the CronJob kind as the custom resource client names it.
var CronJobType = api.CustomResourceType{Group: Group, Version: Version, Kind: KindCronJob, Plural: PluralCronJob}

// CronJobSpec is the spec of a CronJob: at each time Schedule names it creates
// a Job from JobTemplate, and keeps the HistoryLimit newest finished ones.
type CronJobSpec struct {
	Schedule     string `json:"schedule"`
	JobTemplate  Spec   `json:"jobTemplate"`
	HistoryLimit int32  `json:"historyLimit,omitempty"`
}

// CronJobStatus is the status of a CronJob.
type CronJobStatus struct {
	// LastScheduleTime is the latest scheduled time the unit has dealt with,
	// whether it created a Job for it or skipped it.
	LastScheduleTime *time.Time `json:"lastScheduleTime,omitempty"`
}

// CronJobDefinition returns the CustomResourceDefinition of the CronJob kind.
func CronJobDefinition() *api.CustomResourceDefinition {
	one := 1.0
	spec := api.JSONSchemaProps{
		Type:     api.SchemaObject,
		Required: []string{"schedule", "jobTemplate"},
		Properties: map[string]api.JSONSchemaProps{
			"schedule": {
				Type:        api.SchemaString,
				Description: "five fields: minute, hour, day of month, month, day of week; read in UTC",
				Pattern:     SchedulePattern,
			},
			"jobTemplate":  jobSpecSchema(),
			"historyLimit": integerSchema("finished Jobs kept, the oldest deleted first; 3 when absent", &one),
		},
	}
	status := api.JSONSchemaProps{
		Type:       api.SchemaObject,
		Properties: map[string]api.JSONSchemaProps{"lastScheduleTime": {Type: api.SchemaString}},
	}
	root := api.JSONSchemaProps{Type: api.SchemaObject, Properties: map[string]api.JSONSchemaProps{"spec": spec, "status": status}}
	return &api.CustomResourceDefinition{
		TypeMeta: api.TypeMeta{APIVersion: api.APIExtensionsGroupVersion, Kind: api.KindCustomResourceDefinition},
		Metadata: api.ObjectMeta{Name: PluralCronJob + "." + Group, Labels: map[string]string{ManagedByLabel: Manager}},
		Spec: api.CustomResourceDefinitionSpec{
			Group: Group,
			Scope: api.ScopeCluster,
			Names: api.CustomResourceNames{Kind: KindCronJob, ListKind: KindCronJob + "List", Plural: PluralCronJob, Singular: SingularCronJob},
			Versions: []api.CustomResourceVersion{{
				Name: Version, Served: true, Storage: true,
				Schema:       api.CustomResourceValidation{OpenAPIV3Schema: &root},
				Subresources: &api.CustomResourceSubresources{Status: &api.StatusSubresource{}},
			}},
		},
	}
}
