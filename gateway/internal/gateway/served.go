package gateway

// CertificateLabel marks, with CertificateLabelValue, a Secret the proxies may
// read. The proxy's role selects Secrets by it, because a rule names no object
// (ADR 0039).
const (
	CertificateLabel      = Group + "/certificate"
	CertificateLabelValue = "true"
)

// ServedListeners returns the listeners of the Gateway name that the
// controller serves, in the order of the spec.
func ServedListeners(name string, spec GatewaySpec) []Listener {
	return checkGateway(name, spec).servedListeners()
}

// AttachedListeners returns the names of the listeners of the Gateway gw that
// a route of the kind reaches through ref, by the rules the controller applies
// when it records the parent reference as Accepted.
func AttachedListeners(kind string, route RouteSpec, ref ParentRef, gw string, spec GatewaySpec) []string {
	return attachParent(kind, route, ref, map[string]gatewayCheck{gw: checkGateway(gw, spec)}).listeners
}

// ProxyArgs returns the arguments the controller starts the proxy of a Gateway
// with: the flags of gaon-gateway-proxy.
func ProxyArgs(gateway, server string) []string {
	return []string{"-gateway=" + gateway, "-server=" + server, "-tls-ca=" + ProxyCAPath}
}
