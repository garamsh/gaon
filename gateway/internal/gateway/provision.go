package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"github.com/garamsh/gaon/lib/api"
)

const (
	// proxyPrefix begins the name of a Gateway's Service and Deployment.
	proxyPrefix = "gateway-"
	// caPath is where the proxy finds the CA of the API server.
	caPath = "/etc/gaon/gateway/ca.crt"
	// caKey is the key of the CA's ConfigMap.
	caKey = "ca.crt"
)

// ProxyConfig is what the controller puts in the Deployment of a Gateway's
// proxies.
type ProxyConfig struct {
	// Image is the proxy's image.
	Image string
	// Server is the API server's address the proxy is given.
	Server string
	// CAConfigMap is the ConfigMap whose key ca.crt is the CA the proxy trusts.
	CAConfigMap string
	// ServiceAccount is the ServiceAccount the proxy runs as.
	ServiceAccount string
}

// provisioned is what the Service and the Deployment of a Gateway came to.
type provisioned struct {
	// refused is set when the LAN address cannot be held, which refuses the Gateway.
	refused bool
	// programmed is set when the Service holds an address and the Deployment exists.
	programmed      bool
	reason, message string
	// address is the address the Service holds, or empty.
	address string
}

// deploymentSpec is the spec of a workloads Deployment, as far as the
// controller writes it.
type deploymentSpec struct {
	Replicas int32 `json:"replicas"`
	Selector struct {
		MatchLabels map[string]string `json:"matchLabels"`
	} `json:"selector"`
	Template api.MicroVMSpec `json:"template"`
}

func ownerOf(gw *api.CustomObject) api.OwnerReference {
	return api.OwnerReference{APIVersion: APIVersion, Kind: KindGateway, Name: gw.Metadata.Name, UID: gw.Metadata.UID, Controller: true}
}

// ownedBy reports whether meta names the Gateway as an owner.
func ownedBy(meta api.ObjectMeta, gw *api.CustomObject) bool {
	for _, o := range meta.OwnerReferences {
		if o.Kind == KindGateway && o.UID == gw.Metadata.UID {
			return true
		}
	}
	return false
}

func desiredService(gw *api.CustomObject, spec GatewaySpec, ports []int32) *api.Service {
	svc := &api.Service{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindService},
		Metadata: api.ObjectMeta{
			Name:            proxyPrefix + gw.Metadata.Name,
			Labels:          map[string]string{ManagedByLabel: Manager, GatewayLabel: gw.Metadata.Name},
			OwnerReferences: []api.OwnerReference{ownerOf(gw)},
		},
		Spec: api.ServiceSpec{
			Type:     api.ServiceLoadBalancer,
			Selector: map[string]string{GatewayLabel: gw.Metadata.Name},
		},
	}
	if len(spec.Addresses) == 1 {
		svc.Spec.LoadBalancerIP = spec.Addresses[0].Value
	}
	for _, p := range ports {
		svc.Spec.Ports = append(svc.Spec.Ports, api.ServicePort{Name: portName(p), Protocol: api.ProtocolTCP, Port: p, TargetPort: &api.PortRef{Number: p}})
	}
	return svc
}

func portName(p int32) string { return "port-" + strconv.Itoa(int(p)) }

func desiredDeployment(gw *api.CustomObject, cfg ProxyConfig, ports []int32) (*api.CustomObject, error) {
	name := gw.Metadata.Name
	mode := int32(0o444)
	tmpl := api.MicroVMSpec{
		Image: cfg.Image,
		Resources: api.ResourceRequirements{
			Requests: api.ResourceList{CPU: "100m", Memory: "128Mi"},
			Limits:   api.ResourceList{Memory: "256Mi"},
		},
		RunStrategy:        api.RunAlways,
		Args:               []string{"-gateway=" + name, "-server=" + cfg.Server, "-tls-ca=" + caPath},
		Files:              []api.MicroVMFile{{Path: caPath, Mode: &mode, ValueFrom: &api.ValueSource{ConfigMapKeyRef: &api.KeyReference{Name: cfg.CAConfigMap, Key: caKey}}}},
		ServiceAccountName: cfg.ServiceAccount,
		ReadinessProbe:     &api.Probe{TCPSocket: &api.TCPSocketAction{Port: api.PortRef{Number: ports[0]}}},
	}
	for _, p := range ports {
		tmpl.Ports = append(tmpl.Ports, api.MicroVMPort{Name: portName(p), Port: p, Protocol: api.ProtocolTCP})
	}
	spec := deploymentSpec{Replicas: 1, Template: tmpl}
	spec.Selector.MatchLabels = map[string]string{GatewayLabel: name}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	return &api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: WorkloadsAPIVersion, Kind: KindDeployment},
		Metadata: api.ObjectMeta{
			Name:            proxyPrefix + name,
			Labels:          map[string]string{ManagedByLabel: Manager, GatewayLabel: name},
			OwnerReferences: []api.OwnerReference{ownerOf(gw)},
		},
		Spec: raw,
	}, nil
}

// canonical decodes raw into a deploymentSpec and encodes it again, so that two
// encodings of one spec compare equal.
func canonical(raw json.RawMessage) string {
	var spec deploymentSpec
	if json.Unmarshal(raw, &spec) != nil {
		return string(raw)
	}
	out, _ := json.Marshal(spec)
	return string(out)
}

func ingressIP(svc api.Service) string {
	if svc.Status.LoadBalancer != nil && len(svc.Status.LoadBalancer.Ingress) > 0 {
		return svc.Status.LoadBalancer.Ingress[0].IP
	}
	return ""
}

// provision makes the Service and then the Deployment of the Gateway, and
// brings them to the listeners and the address. services and deployments are
// every Service and Deployment that exist, and provision records what it
// writes in services.
func (s *service) provision(ctx context.Context, gw *api.CustomObject, spec GatewaySpec, check gatewayCheck, services map[string]api.Service, deployments map[string]api.CustomObject) (provisioned, error) {
	served := check.servedListeners()
	if check.invalid != "" || len(served) == 0 {
		message := "no listener of the Gateway is served"
		if check.invalid != "" {
			message = check.invalid
		}
		return provisioned{reason: ReasonInvalid, message: message}, nil
	}
	ports := servedPorts(served)
	name := proxyPrefix + gw.Metadata.Name
	want := desiredService(gw, spec, ports)

	have, exists := services[name]
	switch {
	case !exists:
		created, err := s.services.PutService(ctx, want)
		if errors.Is(err, api.ErrInvalid) || errors.Is(err, api.ErrConflict) {
			return provisioned{refused: true, reason: ReasonAddressNotAssigned, message: err.Error()}, nil
		}
		if err != nil {
			return provisioned{reason: ReasonPending, message: "the Service is not written yet"}, fmt.Errorf("create service %s: %w", name, err)
		}
		have = *created
	case !ownedBy(have.Metadata, gw):
		return provisioned{reason: ReasonInvalid, message: fmt.Sprintf("Service %s exists and another object owns it", name)}, nil
	default:
		held := ingressIP(have)
		if held == "" {
			held = have.Spec.LoadBalancerIP
		}
		if want.Spec.LoadBalancerIP != "" && want.Spec.LoadBalancerIP != held {
			return provisioned{reason: ReasonAddressNotAssigned, address: ingressIP(have),
				message: fmt.Sprintf("the Gateway holds %s and a change of spec.addresses is not applied: delete the Gateway and create it again", held)}, nil
		}
		if !reflect.DeepEqual(have.Spec.Ports, want.Spec.Ports) || !reflect.DeepEqual(have.Spec.Selector, want.Spec.Selector) {
			next := have
			next.Spec.Ports, next.Spec.Selector = want.Spec.Ports, want.Spec.Selector
			updated, err := s.services.PutService(ctx, &next)
			if err != nil {
				return provisioned{reason: ReasonPending, message: "the Service is not updated yet", address: ingressIP(have)}, fmt.Errorf("update service %s: %w", name, err)
			}
			have = *updated
		}
	}
	services[name] = have

	address := ingressIP(have)
	if address == "" {
		return provisioned{reason: ReasonPending, message: "the Service holds no address yet"}, nil
	}
	if err := s.provisionDeployment(ctx, gw, name, ports, deployments); err != nil {
		return provisioned{reason: ReasonPending, message: "the Deployment is not written yet", address: address}, err
	}
	return provisioned{programmed: true, reason: ReasonProgrammed, address: address}, nil
}

// provisionDeployment creates the Deployment of the proxies, or brings the
// stored one to the wanted template.
func (s *service) provisionDeployment(ctx context.Context, gw *api.CustomObject, name string, ports []int32, deployments map[string]api.CustomObject) error {
	want, err := desiredDeployment(gw, s.cfg.Proxy, ports)
	if err != nil {
		return err
	}
	have, exists := deployments[name]
	switch {
	case !exists:
	case !ownedBy(have.Metadata, gw):
		return fmt.Errorf("deployment %s exists and another object owns it", name)
	case canonical(have.Spec) == canonical(want.Spec):
		return nil
	default:
		want.Metadata.ResourceVersion = have.Metadata.ResourceVersion
	}
	stored, err := s.deployments.Put(ctx, want)
	if err != nil {
		return fmt.Errorf("write deployment %s: %w", name, err)
	}
	deployments[name] = *stored
	return nil
}
