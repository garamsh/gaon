package endpoints

import (
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// subsetsFor returns the subsets of the Endpoints of svc over the MicroVMs it
// selects. A MicroVM goes into the subset of the ports it resolves, so MicroVMs
// whose named targetPort resolves to different numbers fall into different
// subsets. A MicroVM that resolves none of the Service's ports is left out.
func subsetsFor(svc *api.Service, microvms map[string]*api.MicroVM) []api.EndpointSubset {
	names := make([]string, 0, len(microvms))
	for name := range microvms {
		names = append(names, name)
	}
	slices.Sort(names)

	var subsets []api.EndpointSubset
	index := map[string]int{}
	for _, name := range names {
		m := microvms[name]
		if !listed(m) || !selects(svc.Spec.Selector, m.Metadata.Labels) {
			continue
		}
		ip, ok := firstAddress(m)
		if !ok {
			continue
		}
		ports := resolvePorts(svc.Spec.Ports, m)
		if len(ports) == 0 {
			continue
		}
		key := portsKey(ports)
		i, seen := index[key]
		if !seen {
			i = len(subsets)
			index[key] = i
			subsets = append(subsets, api.EndpointSubset{Ports: ports})
		}
		subsets[i].Addresses = append(subsets[i].Addresses, api.EndpointAddress{
			IP:        ip,
			TargetRef: &api.ObjectReference{APIVersion: api.GroupVersion, Kind: api.KindMicroVM, Name: m.Metadata.Name, UID: m.Metadata.UID},
		})
	}
	slices.SortFunc(subsets, func(a, b api.EndpointSubset) int {
		return strings.Compare(portsKey(a.Ports), portsKey(b.Ports))
	})
	return subsets
}

// firstAddress returns the first of m's addresses that an Endpoints accepts:
// a global unicast IP without a zone.
func firstAddress(m *api.MicroVM) (string, bool) {
	for _, text := range m.Status.Addresses {
		addr, err := netip.ParseAddr(text)
		if err == nil && addr.Zone() == "" && addr.IsGlobalUnicast() {
			return text, true
		}
	}
	return "", false
}

// resolvePorts returns the Service ports as m serves them, each with its
// targetPort resolved. A port whose named targetPort m does not declare with
// that protocol is left out.
func resolvePorts(ports []api.ServicePort, m *api.MicroVM) []api.EndpointPort {
	var out []api.EndpointPort
	for _, p := range ports {
		number, ok := resolveTarget(p, m)
		if !ok {
			continue
		}
		out = append(out, api.EndpointPort{Name: p.Name, Port: number, Protocol: p.Protocol})
	}
	return out
}

func resolveTarget(p api.ServicePort, m *api.MicroVM) (int32, bool) {
	switch {
	case p.TargetPort == nil:
		return p.Port, true
	case p.TargetPort.Name == "":
		return p.TargetPort.Number, true
	}
	for _, mp := range m.Spec.Ports {
		if mp.Name == p.TargetPort.Name && mp.Protocol == p.Protocol {
			return mp.Port, true
		}
	}
	return 0, false
}

// portsKey is equal for two port lists exactly when they are equal.
func portsKey(ports []api.EndpointPort) string {
	var b strings.Builder
	for _, p := range ports {
		b.WriteString(p.Name)
		b.WriteByte('/')
		b.WriteString(string(p.Protocol))
		b.WriteByte('/')
		b.WriteString(strconv.Itoa(int(p.Port)))
		b.WriteByte(' ')
	}
	return b.String()
}
