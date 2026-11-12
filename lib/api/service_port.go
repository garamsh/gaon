package api

// maxServicePortName is the longest name of a Service port, a DNS label as in
// Kubernetes.
const maxServicePortName = 63

// validateServicePorts checks every port of a Service and that names, and
// port and protocol pairs, are each unique. A Service that is not headless has
// a port, and one with several names them all.
func validateServicePorts(ports []ServicePort, headless bool) error {
	if len(ports) == 0 && !headless {
		return invalid("spec.ports is empty, want at least one port unless the Service is headless")
	}
	names := map[string]bool{}
	pairs := map[ServicePort]bool{}
	for i, p := range ports {
		if p.Port < 1 || p.Port > 65535 {
			return invalid("spec.ports[%d].port is %d, want 1 to 65535", i, p.Port)
		}
		if p.Protocol != ProtocolTCP && p.Protocol != ProtocolUDP {
			return invalid("spec.ports[%d].protocol is %q, want TCP or UDP", i, p.Protocol)
		}
		switch {
		case p.Name == "" && len(ports) > 1:
			return invalid("spec.ports[%d].name is empty, want a name because the Service has %d ports", i, len(ports))
		case p.Name != "" && (len(p.Name) > maxServicePortName || !portName.MatchString(p.Name)):
			return invalid("spec.ports[%d].name is %q, want a DNS label of at most %d characters", i, p.Name, maxServicePortName)
		case p.Name != "" && names[p.Name]:
			return invalid("spec.ports[%d].name %q is used twice", i, p.Name)
		}
		if p.Name != "" {
			names[p.Name] = true
		}
		if err := validateTargetPort(i, p.TargetPort); err != nil {
			return err
		}
		pair := ServicePort{Port: p.Port, Protocol: p.Protocol}
		if pairs[pair] {
			return invalid("spec.ports[%d] repeats port %d %s", i, p.Port, p.Protocol)
		}
		pairs[pair] = true
	}
	return nil
}

// validateTargetPort checks that a target port is a number from 1 to 65535 or
// a name of at most 15 characters, as a MicroVM names its ports.
func validateTargetPort(i int, t *PortRef) error {
	switch {
	case t == nil:
		return nil
	case t.Name != "" && t.Number != 0:
		return invalid("spec.ports[%d].targetPort names both a name and a number, want one", i)
	case t.Name != "":
		if len(t.Name) > maxPortName || !portName.MatchString(t.Name) {
			return invalid("spec.ports[%d].targetPort is %q, want a DNS label of at most %d characters", i, t.Name, maxPortName)
		}
	case t.Number < 1 || t.Number > 65535:
		return invalid("spec.ports[%d].targetPort is %d, want a name or a number from 1 to 65535", i, t.Number)
	}
	return nil
}
