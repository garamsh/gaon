package command_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gaon-cli/internal/command"
	"github.com/garamsh/gaon/lib/api"
)

// A fabricated value, not a credential.
const secretValue = "fabricated-value-7"

var secretValueBase64 = base64.StdEncoding.EncodeToString([]byte(secretValue))

const coreKindsYAML = `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: Secret
metadata:
  name: db
data:
  password: ` + "ZmFicmljYXRlZC12YWx1ZS03" + `
---
apiVersion: compute.gaon.garam.sh/v1alpha1
kind: ConfigMap
metadata:
  name: app
data:
  mode: fast
---
apiVersion: compute.gaon.garam.sh/v1alpha1
kind: ServiceAccount
metadata:
  name: runner
---
apiVersion: compute.gaon.garam.sh/v1alpha1
kind: Service
metadata:
  name: web
spec:
  selector:
    app: web
  ports:
    - name: http
      protocol: TCP
      port: 80
      targetPort: 8080
---
apiVersion: compute.gaon.garam.sh/v1alpha1
kind: Endpoints
metadata:
  name: external
subsets:
  - addresses:
      - ip: 10.1.2.3
    ports:
      - port: 5432
        protocol: TCP
---
apiVersion: compute.gaon.garam.sh/v1alpha1
kind: NetworkPolicy
metadata:
  name: deny-web
spec:
  podSelector:
    matchLabels:
      app: web
  ingress:
    - from:
        - ipBlock:
            cidr: 10.0.0.0/8
      ports:
        - protocol: TCP
          port: 80
`

const serviceYAML = `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: Service
metadata:
  name: web
spec:
  selector:
    app: web
  ports:
    - protocol: TCP
      port: 80
`

func newService(clusterIP string) api.Service {
	return api.Service{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindService},
		Metadata: api.ObjectMeta{Name: "web"},
		Spec: api.ServiceSpec{
			Selector:  map[string]string{"app": "web"},
			Ports:     []api.ServicePort{{Protocol: api.ProtocolTCP, Port: 80}},
			ClusterIP: clusterIP,
		},
	}
}

func TestApply_ReachesEachCoreKindAndReadsItBack(t *testing.T) {
	f := newFakeServer(t)

	stdout, stderr, code := execute(t, f, "apply", "-f", writeFile(t, coreKindsYAML))
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "secret/db created\nconfigmap/app created\nserviceaccount/runner created\n"+
		"service/web created\nendpoints/external created\nnetworkpolicy/deny-web created\n", stdout)

	assert.Equal(t, []byte(secretValue), load[api.Secret](f, "/secrets/db").Data["password"])
	assert.Equal(t, "fast", load[api.ConfigMap](f, "/configmaps/app").Data["mode"])
	assert.Equal(t, "runner", load[api.ServiceAccount](f, "/serviceaccounts/runner").Metadata.Name)
	assert.Equal(t, int32(8080), load[api.Service](f, "/services/web").Spec.Ports[0].TargetPort.Number)
	assert.Equal(t, "10.1.2.3", load[api.Endpoints](f, "/endpoints/external").Subsets[0].Addresses[0].IP)
	assert.Equal(t, "10.0.0.0/8", load[api.NetworkPolicy](f, "/networkpolicies/deny-web").Spec.Ingress[0].From[0].IPBlock.CIDR)

	for _, args := range [][]string{
		{"get", "secret", "db"}, {"get", "configmap", "app"}, {"get", "serviceaccount", "runner"},
		{"get", "service", "web"}, {"get", "endpoints", "external"}, {"get", "networkpolicy", "deny-web"},
	} {
		_, stderr, code := execute(t, f, args...)
		assert.Equal(t, command.ExitOK, code, "%v: %s", args, stderr)
	}

	t.Run("a second run of the same file writes nothing", func(t *testing.T) {
		before := f.writes()
		stdout, stderr, code := execute(t, f, "apply", "-f", writeFile(t, coreKindsYAML))
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, "secret/db unchanged\nconfigmap/app unchanged\nserviceaccount/runner unchanged\n"+
			"service/web unchanged\nendpoints/external unchanged\nnetworkpolicy/deny-web unchanged\n", stdout)
		assert.Equal(t, before, f.writes())
	})
}

func TestApply_ARefusedCoreDocumentSendsNothing(t *testing.T) {
	f := newFakeServer(t)
	// A Service with no port that is not headless fails lib/api validation.
	noPort := `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: Service
metadata:
  name: bad
spec:
  selector:
    app: web
`
	_, _, code := execute(t, f, "apply", "-f", writeFile(t, coreKindsYAML+"---\n"+noPort))
	assert.Equal(t, command.ExitFailure, code)
	assert.Zero(t, f.writes())

	_, stderr, code := execute(t, f, "apply", "-f", writeFile(t, coreKindsYAML))
	require.Equal(t, command.ExitOK, code, stderr)
}

func TestApply_AServiceKeepsTheAddressesTheServerAllocated(t *testing.T) {
	t.Run("a document without clusterIP is unchanged", func(t *testing.T) {
		f := newFakeServer(t)
		f.seed("/services/web", newService("10.96.0.10"))

		stdout, stderr, code := execute(t, f, "apply", "-f", writeFile(t, serviceYAML))
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, "service/web unchanged\n", stdout)
		assert.Zero(t, f.writes())
	})

	t.Run("a document without loadBalancerIP is unchanged", func(t *testing.T) {
		f := newFakeServer(t)
		stored := newService("10.96.0.10")
		stored.Spec.Type, stored.Spec.LoadBalancerIP = api.ServiceLoadBalancer, "192.168.1.50"
		stored.Status.LoadBalancer = &api.LoadBalancerStatus{Ingress: []api.LoadBalancerIngress{{IP: "192.168.1.50"}}}
		f.seed("/services/web", stored)

		stdout, stderr, code := execute(t, f, "apply", "-f", writeFile(t, serviceYAML+"  type: LoadBalancer\n"))
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, "service/web unchanged\n", stdout)
		assert.Zero(t, f.writes())
	})

	t.Run("control: a changed port is sent and the stored address is not in the document", func(t *testing.T) {
		f := newFakeServer(t)
		f.seed("/services/web", newService("10.96.0.10"))

		stdout, stderr, code := execute(t, f, "apply", "-f", writeFile(t, strings.Replace(serviceYAML, "port: 80", "port: 81", 1)))
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, "service/web configured\n", stdout)
		assert.Equal(t, 1, f.writes())
		assert.Empty(t, load[api.Service](f, "/services/web").Spec.ClusterIP, "the server, not the CLI, keeps the stored address")
	})

	t.Run("control: a document naming another clusterIP is sent for the server to refuse", func(t *testing.T) {
		f := newFakeServer(t)
		f.seed("/services/web", newService("10.96.0.10"))

		stdout, stderr, code := execute(t, f, "apply", "-f", writeFile(t, serviceYAML+"  clusterIP: 10.96.0.99\n"))
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, "service/web configured\n", stdout)
		assert.Equal(t, 1, f.writes())
	})
}

func seedCoreKinds(t *testing.T, f *fakeServer) {
	t.Helper()
	_, stderr, code := execute(t, f, "apply", "-f", writeFile(t, coreKindsYAML))
	require.Equal(t, command.ExitOK, code, stderr)
}

func TestGet_PrintsEachCoreKindAsATableUnderEveryName(t *testing.T) {
	f := newFakeServer(t)
	seedCoreKinds(t, f)

	tests := []struct {
		names []string
		table string
	}{
		{[]string{"secret", "secrets"}, "NAME  KEYS\ndb    1\n"},
		{[]string{"configmap", "configmaps", "cm"}, "NAME  KEYS\napp   1\n"},
		{[]string{"serviceaccount", "serviceaccounts", "sa"}, "NAME\nrunner\n"},
		{[]string{"service", "services", "svc"}, "NAME  TYPE       CLUSTER-IP  EXTERNAL-IP  PORTS\nweb   ClusterIP  -           -            http:80/TCP->8080\n"},
		{[]string{"endpoints", "ep"}, "NAME      ADDRESSES\nexternal  1\n"},
		{[]string{"networkpolicy", "networkpolicies", "netpol"}, "NAME      POLICY-TYPES  INGRESS  EGRESS\ndeny-web  Ingress       1        0\n"},
	}
	for _, tt := range tests {
		for _, name := range tt.names {
			t.Run(name, func(t *testing.T) {
				stdout, stderr, code := execute(t, f, "get", name)
				require.Equal(t, command.ExitOK, code, stderr)
				assert.Equal(t, tt.table, stdout)
			})
		}
	}
}

func TestSecret_ItsDataIsNotPrintedByTheTableOrDescribe(t *testing.T) {
	f := newFakeServer(t)
	seedCoreKinds(t, f)

	for _, args := range [][]string{{"get", "secret"}, {"get", "secret", "db"}, {"describe", "secret", "db"}} {
		stdout, stderr, code := execute(t, f, args...)
		require.Equal(t, command.ExitOK, code, stderr)
		assert.NotContains(t, stdout, secretValue, args)
		assert.NotContains(t, stdout, secretValueBase64, args)
	}
	stdout, _, _ := execute(t, f, "describe", "secret", "db")
	assert.Contains(t, stdout, "Keys:")
	assert.Contains(t, stdout, "password (18 bytes)")

	t.Run("control: -o yaml and -o json print the object as the API returned it", func(t *testing.T) {
		for _, format := range []string{"yaml", "json"} {
			stdout, stderr, code := execute(t, f, "get", "secret", "db", "-o", format)
			require.Equal(t, command.ExitOK, code, stderr)
			assert.Contains(t, stdout, secretValueBase64, format)
		}
	})
}

func TestDescribe_PrintsTheCoreKindsOwnLines(t *testing.T) {
	f := newFakeServer(t)
	seedCoreKinds(t, f)
	stored := newService("10.96.0.10")
	stored.Metadata.Name = "lb"
	stored.Spec.Type, stored.Spec.LoadBalancerIP = api.ServiceLoadBalancer, "192.168.1.50"
	stored.Status.LoadBalancer = &api.LoadBalancerStatus{Ingress: []api.LoadBalancerIngress{{IP: "192.168.1.50"}}}
	f.seed("/services/lb", stored)

	tests := []struct {
		args []string
		want []string
	}{
		{[]string{"describe", "configmap", "app"}, []string{"Kind:", "ConfigMap", "Keys:", "mode"}},
		{[]string{"describe", "serviceaccount", "runner"}, []string{"Kind:", "ServiceAccount", "runner"}},
		{[]string{"describe", "service", "web"}, []string{"Selector:", "app=web", "Ports:", "http:80/TCP->8080"}},
		{[]string{"describe", "service", "lb"}, []string{"Cluster IP:", "10.96.0.10", "Load balancer IP:", "External IP:", "192.168.1.50"}},
		{[]string{"describe", "endpoints", "external"}, []string{"Subset 1:", "addresses=[10.1.2.3] ports=[5432/TCP]"}},
		{[]string{"describe", "networkpolicy", "deny-web"}, []string{"Pod selector:", "app=web", "Policy types:", "Ingress", "Ingress rules:"}},
	}
	for _, tt := range tests {
		stdout, stderr, code := execute(t, f, tt.args...)
		require.Equal(t, command.ExitOK, code, "%v: %s", tt.args, stderr)
		for _, want := range tt.want {
			assert.Contains(t, stdout, want, tt.args)
		}
	}
}

func TestDelete_ReachesEachCoreKind(t *testing.T) {
	tests := []struct{ kind, name, path string }{
		{"secret", "db", "/secrets/db"},
		{"cm", "app", "/configmaps/app"},
		{"sa", "runner", "/serviceaccounts/runner"},
		{"svc", "web", "/services/web"},
		{"ep", "external", "/endpoints/external"},
		{"netpol", "deny-web", "/networkpolicies/deny-web"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			f := newFakeServer(t)
			seedCoreKinds(t, f)
			before := len(f.seen())

			_, stderr, code := execute(t, f, "delete", tt.kind, tt.name)
			require.Equal(t, command.ExitOK, code, stderr)
			assert.Equal(t, []string{"GET " + tt.path, "DELETE " + tt.path}, f.seen()[before:])
		})
	}
}
