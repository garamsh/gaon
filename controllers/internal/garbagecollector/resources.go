package garbagecollector

import (
	"context"
	"fmt"

	"github.com/garamsh/gaon/lib/api"
)

// builtins are the kinds the API server serves itself. A kind with no DELETE
// path in resource-api.md §Paths can own objects and is never collected.
var builtins = []resource{
	{typ: api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindMicroVM, Plural: "microvms"}, deletable: true},
	{typ: api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindVolume, Plural: "volumes"}, deletable: true},
	{typ: api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindEvent, Plural: "events"}, deletable: true},
	{typ: api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindSecret, Plural: "secrets"}, deletable: true},
	{typ: api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindConfigMap, Plural: "configmaps"}, deletable: true},
	{typ: api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindServiceAccount, Plural: "serviceaccounts"}, deletable: true},
	{typ: api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindNode, Plural: "nodes"}},
	{typ: api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindLease, Plural: "leases"}},
	{typ: api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindCertificateSigningRequest, Plural: "certificatesigningrequests"}},
	{typ: api.CustomResourceType{Group: api.RBACGroup, Version: api.Version, Kind: api.KindClusterRole, Plural: "clusterroles"}, deletable: true},
	{typ: api.CustomResourceType{Group: api.RBACGroup, Version: api.Version, Kind: api.KindClusterRoleBinding, Plural: "clusterrolebindings"}, deletable: true},
	{typ: api.CustomResourceType{Group: api.APIExtensionsGroup, Version: api.Version, Kind: api.KindCustomResourceDefinition, Plural: "customresourcedefinitions"}, deletable: true},
	{typ: api.CustomResourceType{Group: api.AdmissionRegistrationGroup, Version: api.Version, Kind: api.KindMutatingWebhookConfiguration, Plural: "mutatingwebhookconfigurations"}, deletable: true},
	{typ: api.CustomResourceType{Group: api.AdmissionRegistrationGroup, Version: api.Version, Kind: api.KindValidatingWebhookConfiguration, Plural: "validatingwebhookconfigurations"}, deletable: true},
	{typ: api.CustomResourceType{Group: api.CoordinationGroup, Version: api.Version, Kind: api.KindCoordinationLease, Plural: "leases"}, deletable: true},
}

// resources returns the built-in kinds and the kinds of the Established
// definitions. When the definitions cannot be listed it returns the built-in
// kinds and the error.
func (s *service) resources(ctx context.Context) ([]resource, error) {
	list, err := s.definitions.ListCustomResourceDefinitions(ctx)
	if err != nil {
		return builtins, fmt.Errorf("list definitions: %w", err)
	}
	out := append([]resource(nil), builtins...)
	for _, d := range list.Items {
		if !established(d) || len(d.Spec.Versions) == 0 {
			continue
		}
		out = append(out, resource{
			typ:       api.CustomResourceType{Group: d.Spec.Group, Version: d.Spec.Versions[0].Name, Kind: d.Spec.Names.Kind, Plural: d.Spec.Names.Plural},
			deletable: true,
		})
	}
	return out, nil
}

func established(d api.CustomResourceDefinition) bool {
	for _, c := range d.Status.Conditions {
		if c.Type == api.CRDEstablished {
			return c.Status == api.ConditionTrue
		}
	}
	return false
}
