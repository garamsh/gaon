// Package gateway serves the gateway kinds: it registers the Gateway,
// HTTPRoute and TLSRoute definitions, validates what users write, decides which
// routes attach to which listeners, writes that to the objects' status, and
// creates the Service and the Deployment of proxies each Gateway needs.
package gateway

import (
	"encoding/json"

	"github.com/garamsh/gaon/lib/api"
)

// The identity of the kinds and the labels the unit puts on what it writes.
const (
	Group      = "gateway.gaon.garam.sh"
	Version    = "v1alpha1"
	APIVersion = Group + "/" + Version

	KindGateway   = "Gateway"
	KindHTTPRoute = "HTTPRoute"
	KindTLSRoute  = "TLSRoute"

	// ManagedByLabel marks the definitions and the Services and Deployments the unit wrote.
	ManagedByLabel = Group + "/managed-by"
	// Manager is the value of ManagedByLabel and the unit's certificate name.
	Manager = "gaon-gateway-controller"
	// GatewayLabel carries the name of the Gateway a Service or a proxy belongs to.
	GatewayLabel = Group + "/gateway"

	// WorkloadsGroup is the group of the Deployment kind.
	WorkloadsGroup      = "workloads.gaon.garam.sh"
	WorkloadsAPIVersion = WorkloadsGroup + "/" + Version
	KindDeployment      = "Deployment"
)

// The types of the kinds as the custom resource client names them.
var (
	GatewayType    = api.CustomResourceType{Group: Group, Version: Version, Kind: KindGateway, Plural: "gateways"}
	HTTPRouteType  = api.CustomResourceType{Group: Group, Version: Version, Kind: KindHTTPRoute, Plural: "httproutes"}
	TLSRouteType   = api.CustomResourceType{Group: Group, Version: Version, Kind: KindTLSRoute, Plural: "tlsroutes"}
	DeploymentType = api.CustomResourceType{Group: WorkloadsGroup, Version: Version, Kind: KindDeployment, Plural: "deployments"}
)

// The listener protocols, TLS modes and address type.
const (
	ProtocolHTTP  = "HTTP"
	ProtocolHTTPS = "HTTPS"
	ProtocolTLS   = "TLS"

	ModeTerminate   = "Terminate"
	ModePassthrough = "Passthrough"

	AddressIPAddress = "IPAddress"
)

// The condition types.
const (
	CondAccepted     = "Accepted"
	CondProgrammed   = "Programmed"
	CondConflicted   = "Conflicted"
	CondResolvedRefs = "ResolvedRefs"
)

// The reasons of the conditions.
const (
	ReasonAccepted           = "Accepted"
	ReasonProgrammed         = "Programmed"
	ReasonPending            = "Pending"
	ReasonInvalid            = "Invalid"
	ReasonListenersNotValid  = "ListenersNotValid"
	ReasonAddressNotAssigned = "AddressNotAssigned"
	ReasonNoConflicts        = "NoConflicts"
	ReasonProtocolConflict   = "ProtocolConflict"
	ReasonHostnameConflict   = "HostnameConflict"
	ReasonResolvedRefs       = "ResolvedRefs"
	ReasonInvalidCertificate = "InvalidCertificateRef"
	ReasonNoMatchingParent   = "NoMatchingParent"
	ReasonNotAllowed         = "NotAllowedByListeners"
	ReasonNoMatchingHostname = "NoMatchingListenerHostname"
	ReasonUnsupportedValue   = "UnsupportedValue"
	ReasonBackendNotFound    = "BackendNotFound"
)

// Address is a LAN address, as the Gateway API's GatewaySpecAddress.
type Address struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// SecretRef names the Secret that holds a listener's certificate.
type SecretRef struct {
	Name string `json:"name"`
}

// ListenerTLS is the TLS configuration of a listener.
type ListenerTLS struct {
	Mode            string      `json:"mode"`
	CertificateRefs []SecretRef `json:"certificateRefs,omitempty"`
}

// Listener is a port, a protocol and a hostname a Gateway listens on.
type Listener struct {
	Name     string       `json:"name"`
	Port     int32        `json:"port"`
	Protocol string       `json:"protocol"`
	Hostname string       `json:"hostname,omitempty"`
	TLS      *ListenerTLS `json:"tls,omitempty"`
}

// GatewaySpec is the spec of a Gateway.
type GatewaySpec struct {
	Listeners []Listener `json:"listeners"`
	Addresses []Address  `json:"addresses,omitempty"`
}

// ListenerStatus is what the controller records for a listener.
type ListenerStatus struct {
	Name           string          `json:"name"`
	AttachedRoutes int32           `json:"attachedRoutes"`
	Conditions     []api.Condition `json:"conditions,omitempty"`
}

// GatewayStatus is the status of a Gateway.
type GatewayStatus struct {
	Addresses  []Address        `json:"addresses,omitempty"`
	Conditions []api.Condition  `json:"conditions,omitempty"`
	Listeners  []ListenerStatus `json:"listeners,omitempty"`
}

// ParentRef names a Gateway and, optionally, one of its listeners.
type ParentRef struct {
	Name        string `json:"name"`
	SectionName string `json:"sectionName,omitempty"`
}

// BackendRef names a Service and one of its ports.
type BackendRef struct {
	Name string `json:"name"`
	Port int32  `json:"port"`
}

// Rule is a rule of an HTTPRoute or a TLSRoute. A TLSRoute's rule has only
// BackendRefs. Matches and Filters are counted and not read.
type Rule struct {
	Matches     []json.RawMessage `json:"matches,omitempty"`
	Filters     []json.RawMessage `json:"filters,omitempty"`
	BackendRefs []BackendRef      `json:"backendRefs"`
}

// RouteSpec is the spec of an HTTPRoute or a TLSRoute.
type RouteSpec struct {
	ParentRefs []ParentRef `json:"parentRefs"`
	Hostnames  []string    `json:"hostnames,omitempty"`
	Rules      []Rule      `json:"rules"`
}

// ParentStatus is what the controller records for one parent reference.
type ParentStatus struct {
	ParentRef  ParentRef       `json:"parentRef"`
	Conditions []api.Condition `json:"conditions,omitempty"`
}

// RouteStatus is the status of an HTTPRoute or a TLSRoute.
type RouteStatus struct {
	Parents []ParentStatus `json:"parents,omitempty"`
}
