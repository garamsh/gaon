package api

import "fmt"

// MaxEndpointsAddresses is the most addresses an Endpoints holds across its
// subsets, Kubernetes' bound on an Endpoints object.
const MaxEndpointsAddresses = 1000

// Endpoints lists the ready MicroVM addresses and ports behind the Service of
// the same name. It has no spec and no status.
type Endpoints struct {
	TypeMeta
	Metadata ObjectMeta       `json:"metadata"`
	Subsets  []EndpointSubset `json:"subsets,omitempty"`
}

// EndpointsList is the answer to listing Endpoints.
type EndpointsList struct {
	TypeMeta
	Metadata ListMeta    `json:"metadata"`
	Items    []Endpoints `json:"items"`
}

// EndpointSubset is a set of addresses that all serve the same set of ports.
// MicroVMs that resolve a named targetPort to different numbers fall in
// different subsets.
type EndpointSubset struct {
	Addresses []EndpointAddress `json:"addresses"`
	Ports     []EndpointPort    `json:"ports"`
}

// EndpointAddress is the address of one ready MicroVM. TargetRef names the
// MicroVM, and is absent for an address a user wrote for a Service with no
// selector.
type EndpointAddress struct {
	IP        string           `json:"ip"`
	TargetRef *ObjectReference `json:"targetRef,omitempty"`
}

// EndpointPort is a port of a MicroVM, resolved from a Service port's
// targetPort. Name is the Service port's.
type EndpointPort struct {
	Name     string   `json:"name,omitempty"`
	Port     int32    `json:"port"`
	Protocol Protocol `json:"protocol"`
}

// Validate checks the identity, the name and the metadata of e, and that every
// subset holds addresses and ports that are valid and not too many.
func (e *Endpoints) Validate() error {
	if err := validateIdentity(e.TypeMeta, GroupVersion, KindEndpoints, e.Metadata); err != nil {
		return err
	}
	if len(e.Metadata.Name) > maxServiceName || !serviceName.MatchString(e.Metadata.Name) {
		return invalid("metadata.name is %q, want the name of a Service: a DNS label of at most %d characters that starts with a letter", e.Metadata.Name, maxServiceName)
	}
	if err := e.Metadata.Validate(); err != nil {
		return err
	}
	total := 0
	for i, s := range e.Subsets {
		if err := s.validate(i); err != nil {
			return err
		}
		total += len(s.Addresses)
	}
	if total > MaxEndpointsAddresses {
		return invalid("subsets hold %d addresses, want at most %d", total, MaxEndpointsAddresses)
	}
	return nil
}

func (s EndpointSubset) validate(i int) error {
	if len(s.Addresses) == 0 {
		return invalid("subsets[%d].addresses is empty, want an address", i)
	}
	if len(s.Ports) == 0 {
		return invalid("subsets[%d].ports is empty, want a port", i)
	}
	for j, a := range s.Addresses {
		if _, err := parseServiceAddress(fmt.Sprintf("subsets[%d].addresses[%d].ip", i, j), a.IP); err != nil {
			return err
		}
		if r := a.TargetRef; r != nil && (r.APIVersion != GroupVersion || r.Kind != KindMicroVM || r.Name == "") {
			return invalid("subsets[%d].addresses[%d].targetRef is %s %s %q, want a MicroVM of %s", i, j, r.APIVersion, r.Kind, r.Name, GroupVersion)
		}
	}
	names := map[string]bool{}
	pairs := map[EndpointPort]bool{}
	for j, p := range s.Ports {
		if p.Port < 1 || p.Port > 65535 {
			return invalid("subsets[%d].ports[%d].port is %d, want 1 to 65535", i, j, p.Port)
		}
		if p.Protocol != ProtocolTCP && p.Protocol != ProtocolUDP {
			return invalid("subsets[%d].ports[%d].protocol is %q, want TCP or UDP", i, j, p.Protocol)
		}
		if p.Name != "" {
			if len(p.Name) > maxServicePortName || !portName.MatchString(p.Name) {
				return invalid("subsets[%d].ports[%d].name is %q, want a DNS label of at most %d characters", i, j, p.Name, maxServicePortName)
			}
			if names[p.Name] {
				return invalid("subsets[%d].ports[%d].name %q is used twice", i, j, p.Name)
			}
			names[p.Name] = true
		}
		pair := EndpointPort{Port: p.Port, Protocol: p.Protocol}
		if pairs[pair] {
			return invalid("subsets[%d].ports[%d] repeats port %d %s", i, j, p.Port, p.Protocol)
		}
		pairs[pair] = true
	}
	return nil
}
