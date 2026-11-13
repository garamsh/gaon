# Gateway

What the gateway is: the kinds `Gateway`, `HTTPRoute` and `TLSRoute`, how a Gateway holds one LAN address and listens on several ports, what its proxy does with a request or a connection, and which unit registers and runs it. This document states the contract. The unit `gateway/` serves the kinds through `gaon-gateway-controller`; the proxy `gaon-gateway-proxy` serves the listeners and routes.

Basis: [ADR 0032](./adr/0032-the-gateway-is-gateway-httproute-and-tlsroute-kinds-of-their-own-group-a-gateway-unit-registers-and-runs-with-a-loadbalancer-service-and-a-deployment-of-proxies-for-each-gateway.md), with [ADR 0009](./adr/0009-a-user-registers-a-resource-kind-with-a-customresourcedefinition-the-api-server-serves-it-under-its-own-group.md) for the definition and [ADR 0027](./adr/0027-a-service-allocates-its-addresses-from-two-configured-ranges-endpoints-lists-its-ready-microvms-and-a-networkpolicy-selects-microvms-by-label.md) for the Service and [ADR 0039](./adr/0039-the-gateway-proxy-reads-only-the-secrets-labelled-as-certificates-and-reports-a-certificate-it-cannot-load-in-its-log.md) for the proxy's Secrets. Goal #117 gives the decisions cited as D6, D14, D16 and D18. The Kubernetes Gateway API statements hold for release v1.2.0 of kubernetes-sigs/gateway-api: `Gateway` and `HTTPRoute` in `apis/v1`, `TLSRoute` in `apis/v1alpha2`.

## Current decisions

### The kinds

- **Group and version.** `gateway.gaon.garam.sh/v1alpha1` for all three, cluster scoped, each with a `status` subresource. Plurals `gateways`, `httproutes` and `tlsroutes`; list kinds `GatewayList`, `HTTPRouteList` and `TLSRouteList`.
- **The model is the Gateway API's** (D18): a field this document names has that field's meaning in v1.2.0, and a field it does not name is absent and pruned ([resource-api.md](./resource-api.md) §CustomResourceDefinition). Each departure is listed in §Departures from the Gateway API.
- **Checks the schema cannot make.** The schema subset has no array length bound and no cross-field rule, so the controller checks them and reports in `status`; the API server refuses only a field of the wrong type, an enum value or a `pattern` mismatch.

### Gateway

- **`spec.listeners`**, at least one, each:
  - `name`, unique in the Gateway, a DNS label.
  - `port`, 1 to 65535.
  - `protocol`: `HTTP` (plain HTTP), `HTTPS` (TLS terminated at the gateway, then HTTP) or `TLS` (TLS passed through) (D18).
  - `hostname`, optional, an exact name or a wildcard `*.<name>`; absent, the listener takes every host or server name.
  - `tls`, required for `HTTPS` and `TLS` and absent for `HTTP`. `mode` is `Terminate` for `HTTPS` and `Passthrough` for `TLS`. `certificateRefs` is one entry `{name}` naming a `Secret` for `HTTPS` and is absent for `TLS`.
- **`spec.addresses`**, at most one `{type: IPAddress, value}`, the LAN address the Gateway asks for; absent, the pool grants one.
- **Listeners that share a port** have the same `protocol` and different `hostname`s, and a listener with no `hostname` is alone on its port. A listener that breaks this is `Conflicted` and not served.
- **`status`**, written by the controller only: `addresses` (the address held, as `{type, value}`), `conditions` and `listeners`, each `{name, attachedRoutes, conditions}`. Conditions are the Gateway API's: `Accepted` and `Programmed` on the Gateway; `Accepted`, `Conflicted`, `ResolvedRefs` and `Programmed` on a listener.

### HTTPRoute

- **`spec.parentRefs`**, at least one, each `{name, sectionName}`: a Gateway and, optionally, one of its listeners. Only `HTTP` and `HTTPS` listeners accept an `HTTPRoute`.
- **`spec.hostnames`**, optional, each an exact name or a wildcard `*.<name>`; absent, the route takes the hostnames of the listener it attaches to.
- **`spec.rules`**, at least one, each:
  - `matches`, at least one, each `{path: {type: PathPrefix, value}}`; `value` begins with `/`.
  - `filters`, at most one, `{type: URLRewrite, urlRewrite: {path: {type: ReplacePrefixMatch, replacePrefixMatch}}}`. The rule then has exactly one match, as the Gateway API requires.
  - `backendRefs`, exactly one, `{name, port}`: a Service and one of its `port` numbers.
- **`status.parents`**, written by the controller: for each parent reference, `{parentRef, conditions}` with `Accepted` and `ResolvedRefs`.

### TLSRoute

- **`spec.parentRefs`**, as an HTTPRoute's, to a listener of protocol `TLS` only.
- **`spec.hostnames`**, optional, server names as an HTTPRoute's hostnames.
- **`spec.rules`**, exactly one, with `backendRefs`, exactly one, `{name, port}` as an HTTPRoute's.
- **`status.parents`** as an HTTPRoute's.

### The unit

- **Registration.** `gateway/` creates or updates the three definitions at start, `gateways.gateway.gaon.garam.sh`, `httproutes.gateway.gaon.garam.sh` and `tlsroutes.gateway.gaon.garam.sh`, as `workloads/` registers its kinds ([workloads.md](./workloads.md) §Registration).
- **Two binaries.** `gaon-gateway-controller` registers the kinds and runs the controller; `gaon-gateway-proxy` is the proxy. The controller is a client of the public API with its own certificate and acts only while it holds a coordination Lease named `gaon-gateway-controller`, as [workloads.md](./workloads.md) §Election states for `gaon-workloads`. The proxy needs no Lease.
- **What the controller creates** for each Gateway it accepts, named `gateway-<Gateway name>` and owned by the Gateway (an owner reference with its `uid`), so that deleting the Gateway deletes both and the controller deletes neither:
  - a `Service` of type `LoadBalancer` whose `loadBalancerIP` is `spec.addresses[0].value`, with one TCP port for each distinct listener port and a selector on a label naming the Gateway;
  - a `Deployment` of the `workloads` group, one replica, whose MicroVM template runs the proxy, carries that label, names the Gateway to serve and holds a ServiceAccount.
- **What it decides.** It validates each Gateway and route and writes their `status`, sweeping every 2 seconds by default and writing only what changed.
  - A route is attached to a listener when a parent reference names the Gateway, `sectionName` is absent or names the listener, the listener's protocol accepts the kind, and a hostname of the route and the listener's `hostname` intersect (or either has none). The parent condition `Accepted` records this, with reason `NoMatchingParent`, `NotAllowedByListeners` or `NoMatchingListenerHostname` where it is False.
  - A backend whose Service or port does not exist leaves the route `Accepted` and makes `ResolvedRefs` False with reason `BackendNotFound`.
  - A route that breaks a check in §The kinds is `Accepted` False with reason `UnsupportedValue`. A route with no parent reference has an empty `status.parents`.
  - A Gateway whose name leaves its Service name longer than 63 characters (a name of more than 55), that has no listener or has more than one address is `Accepted` False with reason `Invalid` and gets no Service. A listener that is `Conflicted` is not served; the Gateway is `Accepted` with reason `ListenersNotValid` while some other listener is.
  - A Service named `gateway-<Gateway name>` that another object owns is not taken over: `Programmed` is False with reason `Invalid`.
  - The proxy serves a route on a listener only where `status.parents` records it `Accepted`.
- **What the proxy reads**, through the API with its ServiceAccount token: its Gateway, the routes, the Services and Endpoints, and the Secrets labelled `gateway.gaon.garam.sh/certificate=true`. It lists and watches each kind, reads the token file again at each list and watch, and on losing the API server keeps serving what it last held and retries every 2 seconds.
- **Flags of the proxy.** `-gateway`, `-server` and `-tls-ca`, all required, and `-token-file`, which defaults to `/var/run/secrets/gaon.garam.sh/serviceaccount/token`. The controller's Deployment passes the first three.
- **Ports.** The proxy opens a port for each distinct port of its Gateway's served listeners and closes one the Gateway no longer has or whose protocol changed. A port that cannot be bound, or whose listener fails with an error that is not temporary, is logged and opened again at the next change; a temporary error of accepting a passthrough connection is retried after a delay of up to a second.

### LAN address

- **A Gateway holds its address through its Service** (D18). The Service is a `LoadBalancer` whose `loadBalancerIP` names the address; [resource-api.md](./resource-api.md) §Service, Endpoints and NetworkPolicy states that the pool refuses a second holder and releases the address when the Service is deleted. The network plugin announces the address and forwards its ports to the Service's endpoints.
- **The address is chosen once.** A Service's `loadBalancerIP` never changes, so a change of `spec.addresses` after create, including removing it, is not applied: the Gateway's `Programmed` condition is False with reason `AddressNotAssigned`, and the user deletes the Gateway and creates it again. Only the address is held: a change to the listeners in the same edit is applied to the Service and the Deployment.
- **An address that cannot be held** (outside the pool, or held by another Service) is `Invalid` or `Conflict` from the API server when the Gateway asked for one in `spec.addresses`; the controller reports it as `Accepted` False and `Programmed` False with reason `AddressNotAssigned`, and creates no Deployment. A Service refused where no address was asked for is not an address fault: `Programmed` is False with reason `Invalid` for `Invalid`, and `Pending` for any other error, and `Accepted` stays True.
- **`attachedRoutes`** counts each route once on a listener, however many of its parent references reach that listener.
- **`status.addresses`** is the Service's `status.loadBalancer.ingress[0].ip`.

### Listeners

- **One address, several ports** (D18): the Service has a port for each distinct listener port, and the proxy listens on each.
- **A plain HTTP listener** serves `HTTPRoute`s with no TLS and no redirect: the gateway adds none.
- **A terminating listener** (`HTTPS`) completes the TLS handshake with the listener's certificate, TLS 1.2 or later, and then serves `HTTPRoute`s as a plain listener does. It speaks HTTP/1.1.
- **A passthrough listener** (`TLS`) is §TLS passthrough.
- **A request or connection that matches nothing.** An HTTP request that no route matches is answered 404; one whose backend has no ready address is answered 503. A request whose backend cannot be reached is answered 502. A passthrough connection with no matching server name, or none at all, or whose backend has no ready address, is closed without a reply.

### Routes

- **Matching** is the Gateway API's (D16, D18). A request matches a route of its listener when the `Host` header, without a port, matches one of the route's hostnames (or the listener's, where the route has none) and the path matches a rule's prefix.
  - A prefix matches on path elements: `/abc` matches `/abc`, `/abc/` and `/abc/d`, and not `/abcd`. `/` matches every path.
  - An exact hostname beats a wildcard, a longer wildcard beats a shorter, and among the routes that tie the rule with the longest matching prefix wins. A tie after that goes to the route whose name sorts first. A request that the best-ranked route has no path match for is tried against the next-ranked route.
- **Host and path are forwarded unmodified** (D16, D18): the backend receives the `Host` header and the request path as the client sent them, so a client that signs `Host` still verifies.
- **A rule may replace its matched prefix** (D18): `urlRewrite.path.replacePrefixMatch` replaces the prefix the rule matched, as Gateway API `ReplacePrefixMatch` does, so one host can send `/api/` to a backend with the prefix removed and the rest unmodified to others. `Host` is never rewritten. A rule without the filter forwards the path unmodified.
- **A backend** is a Service and one of its ports (`backendRefs[0]`). The proxy connects to an address of the Service's `Endpoints`, which lists only ready MicroVMs ([resource-api.md](./resource-api.md) §Service, Endpoints and NetworkPolicy), on the port of the Endpoints subset whose name is the Service port's. It does not connect to the Service's own address.

### Forwarded headers

- **`X-Forwarded-For`** (D16): the proxy appends the address of the peer that connected to it to a value the request carries, or sets it where there is none.
- **`X-Forwarded-Proto`** (D16): the proxy sets it to `http` on a plain listener and `https` on a terminating one, replacing any value the request carries.
- **No other header is added**, and none is set on a passthrough connection, which the proxy does not read. The proxy removes the hop-by-hop headers of the request and of the response (`Connection` and the headers it names, `Keep-Alive`, `Te`, `Trailer`, `Transfer-Encoding`, `Proxy-Connection`, `Proxy-Authenticate`, `Proxy-Authorization` and `Upgrade`), as an HTTP/1.1 proxy does, except that an `Upgrade` request the proxy forwards keeps its `Upgrade`, and sets the two headers above after that removal, so a client cannot strip them by naming them in `Connection`.

### TLS passthrough

- **A connection is routed by its server name** (D18): the proxy reads the server name from the ClientHello, picks the `TLSRoute` whose hostnames match it, and copies bytes to an address of the backend's Endpoints without terminating TLS. The backend presents its own certificate.
- **The proxy waits at most 10 seconds for the ClientHello,** and closes the connection when it does not arrive.
- **The proxy does not convey the client's address** to a passthrough backend: the backend sees the proxy's address.

### Certificates

- **A listener's certificate is a `Secret`** (D14), the one `certificateRefs[0].name` names, with the keys `tls.crt` (the certificate chain, PEM) and `tls.key` (its private key, PEM).
- **The proxy reloads it when the Secret changes** (D14): it watches the Secret, and a handshake that starts after the change uses the new pair. A connection already established keeps the pair it began with, and the proxy restarts nothing.
- **A certificate reference that is not well formed** (an `HTTPS` listener without `certificateRefs`, or a `TLS` listener with them) makes the listener's `ResolvedRefs` False with reason `InvalidCertificateRef`. The controller reads no Secret, so it does not judge whether the Secret exists or its pair parses: the proxy does, and reports in its log only, once for each distinct problem, with the Secret's name and the parse error and never a value ([ADR 0039](./adr/0039-the-gateway-proxy-reads-only-the-secrets-labelled-as-certificates-and-reports-a-certificate-it-cannot-load-in-its-log.md)). The proxy keeps serving a pair it had loaded, when the Secret is present and no longer parses, until a valid one replaces it; a listener that never had one fails its handshakes.
- **The Secret carries the label `gateway.gaon.garam.sh/certificate: "true"`.** The proxy's rule is limited to Secrets with that label, because a rule cannot name an object; a Secret without it is read as absent, and a Secret that is deleted or loses the label stops being served at once, which is how an operator revokes a certificate.
- **Renewal is not the gateway's** (D14): a workload the operator declares writes the Secret, and the gateway only reads it.

### ACME

- **HTTP-01 is routed, not terminated** (D14, D18): an `HTTPRoute` that attaches to a listener of protocol `HTTP` on port 80 and matches the path prefix `/.well-known/acme-challenge/` sends the challenge to the Service of the renewal workload, so it reaches a backend. No redirect to HTTPS stands in front of it, because the gateway adds none.

### Long streams

- **Server-sent events and WebSocket connections are passed through.** A response is streamed as it arrives and is not buffered; an `Upgrade` request is forwarded and, once the backend agrees, the connection is a byte copy in both directions.
- **There is no limit on a request's or a response's duration**, and no total lifetime for a connection.
- **The idle timeout** closes a connection that has moved no byte in either direction for 1 hour, whether it is a streaming response, an upgraded connection or a passthrough connection. A connection between requests (HTTP keep-alive) is closed after 60 seconds idle. A backend that must stay silent for longer sends a keep-alive of its own.

### Identity and role

- **The controller's certificate** has the common name `gaon-gateway-controller`. Its ClusterRole and binding, `gaon:gateway-controller`, are in `gateway/deploy/rbac.yaml`: it creates and updates the definitions, Services and Deployments it labels, lists the three kinds, Services and Deployments, updates the three kinds' `status`, and reads, creates and updates its Lease. It has no rule for Secrets, deletes nothing and writes no spec.
- **The proxy** authenticates as the user `gaon:serviceaccount:<name>` ([resource-api.md](./resource-api.md) §Secret, ConfigMap and ServiceAccount). Its ServiceAccount, ClusterRole `gaon:gateway-proxy` and binding are in `gateway/deploy/gateway.yaml`: `list` and `watch` of the three kinds, Services and Endpoints, and of Secrets with the label selector `gateway.gaon.garam.sh/certificate=true`.

### Departures from the Gateway API

Each is a decision of ADR 0032, with its reason there:

- no `GatewayClass`, `gatewayClassName`, `controllerName`, `allowedRoutes`, `ReferenceGrant` or `namespace`;
- listener protocols `HTTP`, `HTTPS` and `TLS` only, `TLS` only with `Passthrough`, one protocol per port, one certificate reference per listener and at most one address;
- `HTTPRoute` has `PathPrefix` matches, the `URLRewrite` filter with `ReplacePrefixMatch`, one backend per rule and no weights, timeouts, redirect, header or mirror filters, or other match types;
- no `GRPCRoute`, `TCPRoute` or `UDPRoute`;
- every version `v1alpha1`;
- a tie between routes is broken by name only, because a gaon object has no creation timestamp.

## Rationale

- Taking the Gateway API's model means a reader who knows Gateway, HTTPRoute and TLSRoute reads these ([ADR 0032](./adr/0032-the-gateway-is-gateway-httproute-and-tlsroute-kinds-of-their-own-group-a-gateway-unit-registers-and-runs-with-a-loadbalancer-service-and-a-deployment-of-proxies-for-each-gateway.md)); the departures keep to what #117 asks.
- A Service and a Deployment hold the address and the replicas, so the gateway adds no write path to the core and the pool's single holder is the existing guarantee.
- One site decides attachment, the controller, so the proxy and the status cannot disagree.
- Forwarding `Host` and the path unmodified by default keeps signed requests valid; the prefix replacement is opt-in so a backend that expects no prefix needs no second proxy.

## Open questions

- **A client's address:** the proxy sees a client's address, and so can set `X-Forwarded-For`, only if the network plugin forwards a LoadBalancer Service's traffic without changing the source. That is the plugin's contract.
- **More than one replica:** the Deployment has one replica, so a proxy's restart interrupts the Gateway; whether a Gateway has a replica count, and how several proxies are spread, is open.
- **HTTP/2** on a terminating listener, and gRPC through it.
- **The idle timeouts** are fixed at the values above; whether the proxy takes flags for them is open.
- **A listener's certificate status in the API:** a pair the proxy cannot load is in its log only; whether it belongs in `status` is open.
- **A Gateway's address change** without deleting the Gateway.
- **Weights, several backends and the matches and filters in §Departures from the Gateway API,** each when a need is met.
- **TLS versions and ciphers** beyond TLS 1.2 or later, and a client certificate on a terminating listener.
- **`X-Forwarded-For` trust:** the proxy appends to a value a client sends and so repeats a forged one; a list of trusted sources is open.
- **Status lag:** a route is served only after the controller records it `Accepted`, so a change takes effect after that write; how long that takes is not measured.
