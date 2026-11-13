package gateway

import (
	"encoding/json"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

const (
	dnsLabel    = `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	hostPattern = `^(\*\.)?[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	pathPattern = `^/`
)

func str(desc string) api.JSONSchemaProps {
	return api.JSONSchemaProps{Type: api.SchemaString, Description: desc}
}

func pattern(desc, p string) api.JSONSchemaProps {
	s := str(desc)
	s.Pattern = p
	return s
}

func enum(desc string, values ...string) api.JSONSchemaProps {
	s := str(desc)
	for _, v := range values {
		raw, _ := json.Marshal(v)
		s.Enum = append(s.Enum, raw)
	}
	return s
}

func port(desc string) api.JSONSchemaProps {
	lo, hi := 1.0, 65535.0
	return api.JSONSchemaProps{Type: api.SchemaInteger, Description: desc, Minimum: &lo, Maximum: &hi}
}

func array(items api.JSONSchemaProps) api.JSONSchemaProps {
	return api.JSONSchemaProps{Type: api.SchemaArray, Items: &items}
}

func object(required []string, props map[string]api.JSONSchemaProps) api.JSONSchemaProps {
	return api.JSONSchemaProps{Type: api.SchemaObject, Required: required, Properties: props}
}

func conditionSchema() api.JSONSchemaProps {
	return object([]string{"type", "status"}, map[string]api.JSONSchemaProps{
		"type":               str("the condition's type"),
		"status":             enum("whether the condition holds", "True", "False", "Unknown"),
		"reason":             str("why, in one word"),
		"message":            str("why, for a person"),
		"lastTransitionTime": str("when status last changed"),
	})
}

func addressSchema() api.JSONSchemaProps {
	return object([]string{"type", "value"}, map[string]api.JSONSchemaProps{
		"type":  enum("the kind of address", AddressIPAddress),
		"value": str("the address"),
	})
}

func parentRefSchema() api.JSONSchemaProps {
	return object([]string{"name"}, map[string]api.JSONSchemaProps{
		"name":        pattern("the Gateway", dnsLabel),
		"sectionName": pattern("a listener of the Gateway", dnsLabel),
	})
}

func gatewaySpecSchema() api.JSONSchemaProps {
	listener := object([]string{"name", "port", "protocol"}, map[string]api.JSONSchemaProps{
		"name":     pattern("unique in the Gateway", dnsLabel),
		"port":     port("the port the listener serves"),
		"protocol": enum("HTTP, HTTPS (TLS terminated) or TLS (TLS passed through)", ProtocolHTTP, ProtocolHTTPS, ProtocolTLS),
		"hostname": pattern("an exact name or *.<name>; absent, every host", hostPattern),
		"tls": object([]string{"mode"}, map[string]api.JSONSchemaProps{
			"mode":            enum("Terminate for HTTPS, Passthrough for TLS", ModeTerminate, ModePassthrough),
			"certificateRefs": array(object([]string{"name"}, map[string]api.JSONSchemaProps{"name": str("a Secret")})),
		}),
	})
	return object([]string{"listeners"}, map[string]api.JSONSchemaProps{
		"listeners": array(listener),
		"addresses": array(addressSchema()),
	})
}

func gatewayStatusSchema() api.JSONSchemaProps {
	attached := api.JSONSchemaProps{Type: api.SchemaInteger, Description: "routes attached to the listener"}
	zero := 0.0
	attached.Minimum = &zero
	return object(nil, map[string]api.JSONSchemaProps{
		"addresses":  array(addressSchema()),
		"conditions": array(conditionSchema()),
		"listeners": array(object([]string{"name", "attachedRoutes"}, map[string]api.JSONSchemaProps{
			"name":           str("the listener"),
			"attachedRoutes": attached,
			"conditions":     array(conditionSchema()),
		})),
	})
}

func backendRefSchema() api.JSONSchemaProps {
	return object([]string{"name", "port"}, map[string]api.JSONSchemaProps{
		"name": str("a Service"),
		"port": port("one of the Service's ports"),
	})
}

func routeSpecSchema(rule api.JSONSchemaProps) api.JSONSchemaProps {
	return object([]string{"parentRefs", "rules"}, map[string]api.JSONSchemaProps{
		"parentRefs": array(parentRefSchema()),
		"hostnames":  array(pattern("an exact name or *.<name>", hostPattern)),
		"rules":      array(rule),
	})
}

func httpRuleSchema() api.JSONSchemaProps {
	match := object([]string{"path"}, map[string]api.JSONSchemaProps{
		"path": object([]string{"type", "value"}, map[string]api.JSONSchemaProps{
			"type":  enum("how value matches", "PathPrefix"),
			"value": pattern("begins with /", pathPattern),
		}),
	})
	filter := object([]string{"type"}, map[string]api.JSONSchemaProps{
		"type": enum("the filter", "URLRewrite"),
		"urlRewrite": object(nil, map[string]api.JSONSchemaProps{
			"path": object([]string{"type"}, map[string]api.JSONSchemaProps{
				"type":               enum("how the path is rewritten", "ReplacePrefixMatch"),
				"replacePrefixMatch": str("replaces the matched prefix"),
			}),
		}),
	})
	return object([]string{"matches", "backendRefs"}, map[string]api.JSONSchemaProps{
		"matches":     array(match),
		"filters":     array(filter),
		"backendRefs": array(backendRefSchema()),
	})
}

func tlsRuleSchema() api.JSONSchemaProps {
	return object([]string{"backendRefs"}, map[string]api.JSONSchemaProps{
		"backendRefs": array(backendRefSchema()),
	})
}

func routeStatusSchema() api.JSONSchemaProps {
	return object(nil, map[string]api.JSONSchemaProps{
		"parents": array(object([]string{"parentRef"}, map[string]api.JSONSchemaProps{
			"parentRef":  parentRefSchema(),
			"conditions": array(conditionSchema()),
		})),
	})
}

func definition(t api.CustomResourceType, spec, status api.JSONSchemaProps) *api.CustomResourceDefinition {
	root := object(nil, map[string]api.JSONSchemaProps{"spec": spec, "status": status})
	return &api.CustomResourceDefinition{
		TypeMeta: api.TypeMeta{APIVersion: api.APIExtensionsGroupVersion, Kind: api.KindCustomResourceDefinition},
		Metadata: api.ObjectMeta{Name: t.Plural + "." + t.Group, Labels: map[string]string{ManagedByLabel: Manager}},
		Spec: api.CustomResourceDefinitionSpec{
			Group: t.Group,
			Scope: api.ScopeCluster,
			Names: api.CustomResourceNames{Kind: t.Kind, ListKind: t.Kind + "List", Plural: t.Plural, Singular: strings.ToLower(t.Kind)},
			Versions: []api.CustomResourceVersion{{
				Name: t.Version, Served: true, Storage: true,
				Schema:       api.CustomResourceValidation{OpenAPIV3Schema: &root},
				Subresources: &api.CustomResourceSubresources{Status: &api.StatusSubresource{}},
			}},
		},
	}
}

// Definitions returns the CustomResourceDefinitions of the Gateway, HTTPRoute
// and TLSRoute kinds.
func Definitions() []*api.CustomResourceDefinition {
	return []*api.CustomResourceDefinition{
		definition(GatewayType, gatewaySpecSchema(), gatewayStatusSchema()),
		definition(HTTPRouteType, routeSpecSchema(httpRuleSchema()), routeStatusSchema()),
		definition(TLSRouteType, routeSpecSchema(tlsRuleSchema()), routeStatusSchema()),
	}
}
