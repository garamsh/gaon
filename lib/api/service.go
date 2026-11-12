package api

import (
	"net/netip"
	"regexp"
)

// ServiceType is how a Service is reached.
type ServiceType string

// The types of a Service. An empty type is ClusterIP.
const (
	ServiceClusterIP    ServiceType = "ClusterIP"
	ServiceLoadBalancer ServiceType = "LoadBalancer"
)

// ClusterIPNone is the clusterIP of a headless Service: no address is
// allocated for it.
const ClusterIPNone = "None"

// maxServiceName is the longest name of a Service, a DNS label as in
// Kubernetes, because DNS names it.
const maxServiceName = 63

var serviceName = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

// Service gives the MicroVMs its selector selects one stable address. The API
// server allocates the address, and for a LoadBalancer one from the LAN pool
// as well; Endpoints lists the MicroVMs behind it.
type Service struct {
	TypeMeta
	Metadata ObjectMeta    `json:"metadata"`
	Spec     ServiceSpec   `json:"spec"`
	Status   ServiceStatus `json:"status"`
}

// ServiceList is the answer to listing Services.
type ServiceList struct {
	TypeMeta
	Metadata ListMeta  `json:"metadata"`
	Items    []Service `json:"items"`
}

// ServiceSpec is what a user writes. Selector selects MicroVMs by equality of
// labels; empty, it selects none and the Service's Endpoints is written by its
// user. ClusterIP is empty to have an address allocated, an address of the
// service prefix to ask for one, or ClusterIPNone; once stored it never
// changes. LoadBalancerIP asks for an address of the LAN pool and is empty to
// have one allocated.
type ServiceSpec struct {
	Selector       map[string]string `json:"selector,omitempty"`
	Ports          []ServicePort     `json:"ports,omitempty"`
	Type           ServiceType       `json:"type,omitempty"`
	ClusterIP      string            `json:"clusterIP,omitempty"`
	LoadBalancerIP string            `json:"loadBalancerIP,omitempty"`
}

// ServicePort is a port the Service listens on. TargetPort is the port of the
// selected MicroVM it forwards to, a number or a name its spec.ports
// declares, and is Port when absent. Name is required when the Service has
// more than one port.
type ServicePort struct {
	Name       string   `json:"name,omitempty"`
	Protocol   Protocol `json:"protocol"`
	Port       int32    `json:"port"`
	TargetPort *PortRef `json:"targetPort,omitempty"`
}

// ServiceStatus is written by the API server only.
type ServiceStatus struct {
	LoadBalancer *LoadBalancerStatus `json:"loadBalancer,omitempty"`
}

// LoadBalancerStatus holds the LAN address a LoadBalancer Service holds.
type LoadBalancerStatus struct {
	Ingress []LoadBalancerIngress `json:"ingress,omitempty"`
}

// LoadBalancerIngress is one LAN address of a LoadBalancer Service.
type LoadBalancerIngress struct {
	IP string `json:"ip"`
}

// EffectiveType is the type a reader acts on: ClusterIP for an empty one.
func (s ServiceSpec) EffectiveType() ServiceType {
	if s.Type == "" {
		return ServiceClusterIP
	}
	return s.Type
}

// Headless reports whether s asks for no address.
func (s ServiceSpec) Headless() bool { return s.ClusterIP == ClusterIPNone }

// Validate checks the identity, the name and the metadata of s, its selector,
// ports and addresses, and the shape of its status.
func (s *Service) Validate() error {
	if err := validateIdentity(s.TypeMeta, GroupVersion, KindService, s.Metadata); err != nil {
		return err
	}
	if len(s.Metadata.Name) > maxServiceName || !serviceName.MatchString(s.Metadata.Name) {
		return invalid("metadata.name is %q, want a DNS label of at most %d characters that starts with a letter", s.Metadata.Name, maxServiceName)
	}
	if err := s.Metadata.Validate(); err != nil {
		return err
	}
	if err := s.Spec.validate(); err != nil {
		return err
	}
	return s.Status.validate(s.Spec)
}

// ValidateUpdate checks that s keeps the type, clusterIP and loadBalancerIP
// of old, and is otherwise valid. The API server keeps the stored status.
func (s *Service) ValidateUpdate(old *Service) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Spec.EffectiveType() != old.Spec.EffectiveType() {
		return invalid("spec.type is %q, want the stored %q: the type never changes", s.Spec.EffectiveType(), old.Spec.EffectiveType())
	}
	if s.Spec.ClusterIP != old.Spec.ClusterIP {
		return invalid("spec.clusterIP is %q, want the stored %q: the address never changes", s.Spec.ClusterIP, old.Spec.ClusterIP)
	}
	if s.Spec.LoadBalancerIP != old.Spec.LoadBalancerIP {
		return invalid("spec.loadBalancerIP is %q, want the stored %q: the address never changes", s.Spec.LoadBalancerIP, old.Spec.LoadBalancerIP)
	}
	return nil
}

func (s ServiceSpec) validate() error {
	if err := ValidateLabels(s.Selector); err != nil {
		return invalid("spec.selector: %v", err)
	}
	switch s.EffectiveType() {
	case ServiceClusterIP:
		if s.LoadBalancerIP != "" {
			return invalid("spec.loadBalancerIP is %q, want empty for type ClusterIP", s.LoadBalancerIP)
		}
	case ServiceLoadBalancer:
		if s.Headless() {
			return invalid("spec.clusterIP is %q, want an address or empty for type LoadBalancer", ClusterIPNone)
		}
		if s.LoadBalancerIP != "" {
			if _, err := parseServiceAddress("spec.loadBalancerIP", s.LoadBalancerIP); err != nil {
				return err
			}
		}
	default:
		return invalid("spec.type is %q, want ClusterIP or LoadBalancer", s.Type)
	}
	if s.ClusterIP != "" && !s.Headless() {
		if _, err := parseServiceAddress("spec.clusterIP", s.ClusterIP); err != nil {
			return err
		}
	}
	return validateServicePorts(s.Ports, s.Headless())
}

func (st ServiceStatus) validate(spec ServiceSpec) error {
	lb := st.LoadBalancer
	if lb == nil || len(lb.Ingress) == 0 {
		return nil
	}
	if spec.EffectiveType() != ServiceLoadBalancer {
		return invalid("status.loadBalancer holds an address, want none for type %s", spec.EffectiveType())
	}
	if len(lb.Ingress) > 1 {
		return invalid("status.loadBalancer.ingress holds %d addresses, want at most 1", len(lb.Ingress))
	}
	_, err := parseServiceAddress("status.loadBalancer.ingress[0].ip", lb.Ingress[0].IP)
	return err
}

// parseServiceAddress parses a unicast address that is not a loopback or
// link-local one and has no zone.
func parseServiceAddress(field, text string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(text)
	if err != nil || addr.Zone() != "" || !addr.IsGlobalUnicast() {
		return netip.Addr{}, invalid("%s is %q, want an IP address that is not unspecified, loopback, link-local or multicast", field, text)
	}
	return addr, nil
}
