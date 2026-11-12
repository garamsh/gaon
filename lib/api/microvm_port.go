package api

import "regexp"

// Protocol is the transport protocol of a MicroVM's port.
type Protocol string

// The protocols of a port.
const (
	ProtocolTCP Protocol = "TCP"
	ProtocolUDP Protocol = "UDP"
)

// maxPortName is the longest name of a port, as in Kubernetes.
const maxPortName = 15

var portName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// MicroVMPort is a port a MicroVM listens on. A name is optional and, when
// given, is what a probe refers to the port by.
type MicroVMPort struct {
	Name     string   `json:"name,omitempty"`
	Port     int32    `json:"port"`
	Protocol Protocol `json:"protocol"`
}

// validatePorts checks every port and that names, and port and protocol
// pairs, are each unique.
func validatePorts(ports []MicroVMPort) error {
	names := map[string]bool{}
	pairs := map[MicroVMPort]bool{}
	for i, p := range ports {
		if p.Port < 1 || p.Port > 65535 {
			return invalid("spec.ports[%d].port is %d, want 1 to 65535", i, p.Port)
		}
		if p.Protocol != ProtocolTCP && p.Protocol != ProtocolUDP {
			return invalid("spec.ports[%d].protocol is %q, want TCP or UDP", i, p.Protocol)
		}
		if p.Name != "" {
			if len(p.Name) > maxPortName || !portName.MatchString(p.Name) {
				return invalid("spec.ports[%d].name is %q, want a DNS label of at most %d characters", i, p.Name, maxPortName)
			}
			if names[p.Name] {
				return invalid("spec.ports[%d].name %q is used twice", i, p.Name)
			}
			names[p.Name] = true
		}
		pair := MicroVMPort{Port: p.Port, Protocol: p.Protocol}
		if pairs[pair] {
			return invalid("spec.ports[%d] repeats port %d %s", i, p.Port, p.Protocol)
		}
		pairs[pair] = true
	}
	return nil
}
