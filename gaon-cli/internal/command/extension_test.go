package command_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gaon-cli/internal/command"
	"github.com/garamsh/gaon/lib/api"
)

const (
	rbacRoot   = "/apis/" + api.RBACGroupVersion
	extRoot    = "/apis/" + api.APIExtensionsGroupVersion
	widgetRoot = "/apis/example.com/v1"
)

const clusterRoleYAML = `apiVersion: rbac.gaon.garam.sh/v1alpha1
kind: ClusterRole
metadata:
  name: reader
rules:
  - apiGroups: ["compute.gaon.garam.sh"]
    resources: ["machines"]
    verbs: ["get", "list"]
`

const clusterRoleBindingYAML = `apiVersion: rbac.gaon.garam.sh/v1alpha1
kind: ClusterRoleBinding
metadata:
  name: readers
roleRef:
  kind: ClusterRole
  name: reader
subjects:
  - kind: User
    name: alice
`

const definitionYAML = `apiVersion: apiextensions.gaon.garam.sh/v1alpha1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  scope: Cluster
  names:
    kind: Widget
    listKind: WidgetList
    plural: widgets
    singular: widget
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
`

const widgetYAML = `apiVersion: example.com/v1
kind: Widget
metadata:
  name: w2
spec:
  size: 3
`

// csrPEM is a PEM certificate request, as spec.request carries it.
func csrPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "alice"}}, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func csrYAML(t *testing.T, name string) string {
	t.Helper()
	return "apiVersion: compute.gaon.garam.sh/v1alpha1\nkind: CertificateSigningRequest\nmetadata:\n  name: " + name +
		"\nspec:\n  request: |\n" + "    " + strings.ReplaceAll(strings.TrimSpace(csrPEM(t)), "\n", "\n    ") + "\n"
}

func newDefinition(group, plural, singular, kind string, established bool) api.CustomResourceDefinition {
	d := api.CustomResourceDefinition{
		TypeMeta: api.TypeMeta{APIVersion: api.APIExtensionsGroupVersion, Kind: api.KindCustomResourceDefinition},
		Metadata: api.ObjectMeta{Name: plural + "." + group},
		Spec: api.CustomResourceDefinitionSpec{
			Group:    group,
			Names:    api.CustomResourceNames{Kind: kind, ListKind: kind + "List", Plural: plural, Singular: singular},
			Scope:    api.ScopeCluster,
			Versions: []api.CustomResourceVersion{{Name: "v1", Served: true, Storage: true}},
		},
	}
	status := api.ConditionFalse
	if established {
		status = api.ConditionTrue
	}
	d.Status.Conditions = []api.Condition{{Type: api.CRDEstablished, Status: status}}
	return d
}

func seedDefinition(f *fakeServer, d api.CustomResourceDefinition) {
	f.seed(extRoot+"/customresourcedefinitions/"+d.Metadata.Name, d)
}

func newWidget(name string) map[string]any {
	return map[string]any{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"size": 3},
	}
}

// seedExtensions stores one object of each extension kind.
func seedExtensions(t *testing.T, f *fakeServer) {
	t.Helper()
	f.seed(rbacRoot+"/clusterroles/reader", api.ClusterRole{
		TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRole},
		Metadata: api.ObjectMeta{Name: "reader"},
		Rules: []api.PolicyRule{
			{APIGroups: []string{api.Group}, Resources: []string{"machines"}, Verbs: []api.Verb{"get", "list"}},
			{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []api.Verb{"get"}},
		},
	})
	f.seed(rbacRoot+"/clusterrolebindings/readers", api.ClusterRoleBinding{
		TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRoleBinding},
		Metadata: api.ObjectMeta{Name: "readers"},
		RoleRef:  api.RoleRef{Kind: api.KindClusterRole, Name: "reader"},
		Subjects: []api.Subject{{Kind: api.SubjectUser, Name: "alice"}, {Kind: api.SubjectGroup, Name: "dev"}},
	})
	seedDefinition(f, newDefinition("example.com", "widgets", "widget", "Widget", true))
	f.seed("/certificatesigningrequests/alice", api.CertificateSigningRequest{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindCertificateSigningRequest},
		Metadata: api.ObjectMeta{Name: "alice"},
		Spec:     api.CertificateSigningRequestSpec{Request: "x", Username: "alice"},
		Status: api.CertificateSigningRequestStatus{
			Conditions:  []api.Condition{{Type: api.CSRApproved, Status: api.ConditionTrue, Reason: "Approved"}},
			Certificate: "pem",
		},
	})
	f.seed("/certificatesigningrequests/bob", api.CertificateSigningRequest{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindCertificateSigningRequest},
		Metadata: api.ObjectMeta{Name: "bob"},
		Spec:     api.CertificateSigningRequestSpec{Request: "x", Username: "bob"},
	})
	f.seed(widgetRoot+"/widgets/w1", newWidget("w1"))
	require.Equal(t, "reader", load[api.ClusterRole](f, rbacRoot+"/clusterroles/reader").Metadata.Name)
}

var extensionTables = map[string]string{
	"clusterrole":              "NAME    RULES\nreader  2\n",
	"clusterrolebinding":       "NAME     ROLE    SUBJECTS\nreaders  reader  User/alice, Group/dev\n",
	"customresourcedefinition": "NAME                 KIND    VERSION  ESTABLISHED\nwidgets.example.com  Widget  v1       True\n",
	"certificatesigningrequest": "NAME   USERNAME  STATE\n" +
		"alice  alice     Approved\n" +
		"bob    bob       Pending\n",
	"widget": "NAME  GENERATION\nw1    0\n",
}

func TestGet_AcceptsTheExtensionKindsAsSingularPluralOrShortName(t *testing.T) {
	f := newFakeServer(t)
	seedExtensions(t, f)

	spellings := map[string][]string{
		"clusterrole":               {"clusterrole", "clusterroles", "cr", "ClusterRole"},
		"clusterrolebinding":        {"clusterrolebinding", "clusterrolebindings", "crb"},
		"customresourcedefinition":  {"customresourcedefinition", "customresourcedefinitions", "crd"},
		"certificatesigningrequest": {"certificatesigningrequest", "certificatesigningrequests", "csr"},
	}
	for kind, names := range spellings {
		for _, name := range names {
			t.Run(kind+" as "+name, func(t *testing.T) {
				stdout, stderr, code := execute(t, f, "get", name)
				require.Equal(t, command.ExitOK, code, stderr)
				assert.Equal(t, extensionTables[kind], stdout)
			})
		}
	}

	t.Run("one object by name", func(t *testing.T) {
		stdout, stderr, code := execute(t, f, "get", "crb", "readers")
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, extensionTables["clusterrolebinding"], stdout)
	})
}

func TestGet_ADeniedRequestReadsDeniedEvenWhenApprovedToo(t *testing.T) {
	f := newFakeServer(t)
	f.seed("/certificatesigningrequests/mallory", api.CertificateSigningRequest{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindCertificateSigningRequest},
		Metadata: api.ObjectMeta{Name: "mallory"},
		Spec:     api.CertificateSigningRequestSpec{Request: "x", Username: "mallory"},
		Status: api.CertificateSigningRequestStatus{Conditions: []api.Condition{
			{Type: api.CSRDenied, Status: api.ConditionTrue},
			{Type: api.CSRApproved, Status: api.ConditionTrue},
		}},
	})
	f.seed("/certificatesigningrequests/erin", api.CertificateSigningRequest{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindCertificateSigningRequest},
		Metadata: api.ObjectMeta{Name: "erin"},
		Spec:     api.CertificateSigningRequestSpec{Request: "x", Username: "erin"},
		Status: api.CertificateSigningRequestStatus{Conditions: []api.Condition{
			{Type: api.CSRApproved, Status: api.ConditionTrue},
			{Type: api.CSRDenied, Status: api.ConditionFalse},
		}},
	})

	stdout, stderr, code := execute(t, f, "get", "csr")

	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "NAME     USERNAME  STATE\nerin     erin      Approved\nmallory  mallory   Denied\n", stdout)
}

func TestGet_ReadsEachGroupAtItsOwnRoot(t *testing.T) {
	f := newFakeServer(t)
	seedExtensions(t, f)

	for _, name := range []string{"cr", "crb", "crd", "csr"} {
		_, stderr, code := execute(t, f, "get", name)
		require.Equal(t, command.ExitOK, code, stderr)
	}

	assert.Equal(t, []string{
		"GET " + rbacRoot + "/clusterroles",
		"GET " + rbacRoot + "/clusterrolebindings",
		"GET " + extRoot + "/customresourcedefinitions",
		"GET /certificatesigningrequests",
	}, f.seen())
}

func TestDescribe_PrintsTheExtensionKinds(t *testing.T) {
	f := newFakeServer(t)
	seedExtensions(t, f)

	tests := []struct {
		name    string
		args    []string
		matches []string
	}{
		{"a cluster role", []string{"describe", "cr", "reader"}, []string{
			`(?m)^Kind:\s+ClusterRole$`,
			`(?m)^Rule 1:\s+apiGroups=\[compute\.gaon\.garam\.sh\] resources=\[machines\] verbs=\[get list\]$`,
			`(?m)^Rule 2:\s+apiGroups=\[\*\] resources=\[\*\] verbs=\[get\]$`,
		}},
		{"a cluster role binding", []string{"describe", "crb", "readers"}, []string{
			`(?m)^Role:\s+ClusterRole/reader$`,
			`(?m)^Subjects:\s+User/alice, Group/dev$`,
		}},
		{"a definition", []string{"describe", "crd", "widgets.example.com"}, []string{
			`(?m)^Group:\s+example\.com$`,
			`(?m)^Versions:\s+v1$`,
			`(?m)^\s+Established\s+True\s`,
		}},
		{"an approved request", []string{"describe", "csr", "alice"}, []string{
			`(?m)^Username:\s+alice$`,
			`(?m)^State:\s+Approved$`,
			`(?m)^Certificate:\s+issued$`,
		}},
		{"a pending request", []string{"describe", "csr", "bob"}, []string{
			`(?m)^State:\s+Pending$`,
			`(?m)^Certificate:\s+-$`,
		}},
		{"a custom object", []string{"describe", "widget", "w1"}, []string{
			`(?m)^Kind:\s+Widget$`,
			`(?m)^Spec:\s+\{"size":3\}$`,
			`(?m)^Status:\s+-$`,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, code := execute(t, f, tt.args...)
			require.Equal(t, command.ExitOK, code, stderr)
			for _, re := range tt.matches {
				assert.Regexp(t, re, stdout)
			}
		})
	}
}

func TestApply_CreatesAndThenUpdatesTheExtensionKinds(t *testing.T) {
	f := newFakeServer(t)
	file := writeFile(t, clusterRoleYAML+"---\n"+clusterRoleBindingYAML+"---\n"+definitionYAML+"---\n"+csrYAML(t, "carol"))

	stdout, stderr, code := execute(t, f, "apply", "-f", file)

	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "clusterrole/reader created\nclusterrolebinding/readers created\n"+
		"customresourcedefinition/widgets.example.com created\ncertificatesigningrequest/carol created\n", stdout)
	assert.Equal(t, "reader", load[api.ClusterRoleBinding](f, rbacRoot+"/clusterrolebindings/readers").RoleRef.Name)
	assert.Equal(t, "Widget", load[api.CustomResourceDefinition](f, extRoot+"/customresourcedefinitions/widgets.example.com").Spec.Names.Kind)
	assert.Equal(t, "carol", load[api.CertificateSigningRequest](f, "/certificatesigningrequests/carol").Metadata.Name)

	changed := strings.Replace(clusterRoleYAML, `verbs: ["get", "list"]`, `verbs: ["get"]`, 1)
	stdout, stderr, code = execute(t, f, "apply", "-f", writeFile(t, changed))
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "clusterrole/reader configured\n", stdout)
	assert.Equal(t, []api.Verb{"get"}, load[api.ClusterRole](f, rbacRoot+"/clusterroles/reader").Rules[0].Verbs)
}

func TestApply_RefusesAnInvalidExtensionObjectAndSendsNoWrite(t *testing.T) {
	noRules := strings.Replace(clusterRoleYAML, `verbs: ["get", "list"]`, `verbs: []`, 1)
	badRef := strings.Replace(clusterRoleBindingYAML, "kind: ClusterRole\n", "kind: Role\n", 1)
	notPEM := strings.Replace(csrYAML(t, "carol"), "BEGIN CERTIFICATE REQUEST", "BEGIN SOMETHING", 1)
	tests := []struct {
		name, file, message string
	}{
		{"a rule with no verb", noRules, "verbs"},
		{"a binding of another kind of role", badRef, "roleRef"},
		{"a request that is not PEM", notPEM, "spec.request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t)

			_, stderr, code := execute(t, f, "apply", "-f", writeFile(t, clusterRoleYAML+"---\n"+tt.file))

			assert.Equal(t, command.ExitFailure, code)
			assert.Contains(t, stderr, tt.message)
			assert.Zero(t, f.writes(), "writes sent")
		})
	}

	t.Run("control: the valid documents are sent", func(t *testing.T) {
		f := newFakeServer(t)
		_, stderr, code := execute(t, f, "apply", "-f", writeFile(t, clusterRoleYAML+"---\n"+clusterRoleBindingYAML))
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, 2, f.writes())
	})
}

func TestDelete_RemovesTheKindsWithADeletePath(t *testing.T) {
	f := newFakeServer(t)
	seedExtensions(t, f)

	tests := []struct {
		args   []string
		stdout string
		sent   []string
	}{
		{[]string{"delete", "cr", "reader"}, "clusterrole/reader deleted\n",
			[]string{"GET " + rbacRoot + "/clusterroles/reader", "DELETE " + rbacRoot + "/clusterroles/reader"}},
		{[]string{"delete", "crb", "readers"}, "clusterrolebinding/readers deleted\n",
			[]string{"GET " + rbacRoot + "/clusterrolebindings/readers", "DELETE " + rbacRoot + "/clusterrolebindings/readers"}},
		{[]string{"delete", "crd", "widgets.example.com"}, "customresourcedefinition/widgets.example.com deleted\n",
			[]string{"GET " + extRoot + "/customresourcedefinitions/widgets.example.com", "DELETE " + extRoot + "/customresourcedefinitions/widgets.example.com"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			before := len(f.seen())

			stdout, stderr, code := execute(t, f, tt.args...)

			require.Equal(t, command.ExitOK, code, stderr)
			assert.Equal(t, tt.stdout, stdout)
			assert.Equal(t, tt.sent, f.seen()[before:])
		})
	}
}

func TestVerbWithoutAPathOnAnExtensionKind_IsRefusedLocallyNamingThePath(t *testing.T) {
	f := newFakeServer(t)
	seedExtensions(t, f)

	tests := []struct {
		args    []string
		refused string // the path named, or empty when the verb has one
	}{
		{[]string{"delete", "csr", "alice"}, "DELETE /certificatesigningrequests/{name}"},
		{[]string{"delete", "certificatesigningrequests", "alice"}, "DELETE /certificatesigningrequests/{name}"},
		{[]string{"get", "csr", "alice"}, ""},
		{[]string{"get", "csr"}, ""},
		{[]string{"describe", "csr", "alice"}, ""},
		{[]string{"apply", "-f", writeFile(t, csrYAML(t, "carol"))}, ""},
		{[]string{"delete", "cr", "reader"}, ""},
		{[]string{"delete", "crb", "readers"}, ""},
		{[]string{"delete", "crd", "widgets.example.com"}, ""},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args[:min(len(tt.args), 3)], " "), func(t *testing.T) {
			before := f.count()

			_, stderr, code := execute(t, f, tt.args...)

			if tt.refused == "" {
				assert.Equal(t, command.ExitOK, code, stderr)
				assert.Greater(t, f.count(), before, "the verb has a path, so a request is sent")
				return
			}
			assert.Equal(t, command.ExitFailure, code)
			assert.Contains(t, stderr, "refused")
			assert.Contains(t, stderr, tt.refused)
			assert.Equal(t, before, f.count(), "requests sent")
		})
	}
}

func TestCustomKind_IsServedByItsDefinitionsNames(t *testing.T) {
	f := newFakeServer(t)
	seedExtensions(t, f)

	for _, name := range []string{"widget", "widgets", "Widgets", "widgets.example.com"} {
		t.Run("get "+name, func(t *testing.T) {
			stdout, stderr, code := execute(t, f, "get", name)
			require.Equal(t, command.ExitOK, code, stderr)
			assert.Equal(t, extensionTables["widget"], stdout)
		})
	}

	t.Run("the objects are read at the group's root", func(t *testing.T) {
		before := len(f.seen())
		_, stderr, code := execute(t, f, "get", "-o", "yaml", "widget", "w1")
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, []string{
			"GET " + extRoot + "/customresourcedefinitions",
			"GET " + widgetRoot + "/widgets/w1",
		}, f.seen()[before:])
	})

	t.Run("apply creates and then updates an object", func(t *testing.T) {
		stdout, stderr, code := execute(t, f, "apply", "-f", writeFile(t, widgetYAML))
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, "widget/w2 created\n", stdout)
		assert.JSONEq(t, `{"size":3}`, string(load[api.CustomObject](f, widgetRoot+"/widgets/w2").Spec))

		stdout, stderr, code = execute(t, f, "apply", "-f", writeFile(t, strings.Replace(widgetYAML, "size: 3", "size: 4", 1)))
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, "widget/w2 configured\n", stdout)
		assert.JSONEq(t, `{"size":4}`, string(load[api.CustomObject](f, widgetRoot+"/widgets/w2").Spec))
	})

	t.Run("delete removes an object", func(t *testing.T) {
		stdout, stderr, code := execute(t, f, "delete", "widgets", "w1")
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, "widget/w1 deleted\n", stdout)
	})
}

func TestCustomKind_IsRefusedWhenItIsNotAServedRegisteredKind(t *testing.T) {
	f := newFakeServer(t)
	seedExtensions(t, f)
	seedDefinition(f, newDefinition("example.com", "gadgets", "gadget", "Gadget", false))

	gizmo := strings.NewReplacer("Widget", "Gizmo").Replace(widgetYAML)
	gadget := strings.NewReplacer("Widget", "Gadget").Replace(widgetYAML)
	otherVersion := strings.Replace(widgetYAML, "example.com/v1", "example.com/v2", 1)
	otherGroup := strings.Replace(widgetYAML, "example.com/v1", "other.example.com/v1", 1)
	specList := strings.Replace(widgetYAML, "spec:\n  size: 3", "spec: [3]", 1)
	noName := strings.Replace(widgetYAML, "  name: w2\n", "", 1)

	apply := []struct {
		name, file, message string
	}{
		{"a kind nobody registered", gizmo, `unknown kind "Gizmo"`},
		{"a kind whose definition is not Established", gadget, `unknown kind "Gadget"`},
		{"a version the definition does not have", otherVersion, `unknown kind "Widget"`},
		{"a group the definition is not in", otherGroup, `unknown kind "Widget"`},
		{"a spec that is not an object", specList, "spec is not a JSON object"},
		{"an object with no name", noName, "name"},
	}
	for _, tt := range apply {
		t.Run("apply "+tt.name, func(t *testing.T) {
			before := f.writes()

			_, stderr, code := execute(t, f, "apply", "-f", writeFile(t, tt.file))

			assert.Equal(t, command.ExitFailure, code)
			assert.Contains(t, stderr, tt.message)
			assert.Equal(t, before, f.writes(), "writes sent")
		})
	}

	for _, name := range []string{"gizmo", "gadgets", "gadget"} {
		t.Run("get "+name, func(t *testing.T) {
			_, stderr, code := execute(t, f, "get", name)

			assert.Equal(t, command.ExitUsage, code)
			message := strings.SplitN(stderr, "\n", 2)[0]
			assert.Contains(t, message, "unknown kind")
			assert.Contains(t, message, "widget (widgets)", "the registered kinds are listed")
			assert.NotContains(t, message, "gadget (", "an unestablished kind is not")
		})
	}

	t.Run("control: the registered kind is served", func(t *testing.T) {
		before := f.writes()
		stdout, stderr, code := execute(t, f, "apply", "-f", writeFile(t, widgetYAML))
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, "widget/w2 created\n", stdout)
		assert.Equal(t, before+1, f.writes())

		_, stderr, code = execute(t, f, "get", "widget")
		assert.Equal(t, command.ExitOK, code, stderr)
	})
}

func TestCustomKind_ANameTwoDefinitionsShareIsAmbiguousUntilItIsQualified(t *testing.T) {
	f := newFakeServer(t)
	seedDefinition(f, newDefinition("a.example.com", "things", "thing", "Thing", true))
	seedDefinition(f, newDefinition("b.example.com", "things", "thing", "Thing", true))
	f.seed("/apis/a.example.com/v1/things/t", map[string]any{
		"apiVersion": "a.example.com/v1", "kind": "Thing", "metadata": map[string]any{"name": "t"},
	})

	for _, name := range []string{"thing", "things"} {
		t.Run(name, func(t *testing.T) {
			_, stderr, code := execute(t, f, "get", name)

			assert.Equal(t, command.ExitUsage, code)
			assert.Contains(t, stderr, "ambiguous")
			assert.Contains(t, stderr, "things.a.example.com")
			assert.Contains(t, stderr, "things.b.example.com")
		})
	}

	t.Run("control: the definition's name picks one", func(t *testing.T) {
		stdout, stderr, code := execute(t, f, "get", "things.a.example.com")
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, "NAME  GENERATION\nt     0\n", stdout)
	})
}

func TestCustomKind_ABuiltInNameIsNotTakenByADefinition(t *testing.T) {
	f := newFakeServer(t)
	seedCluster(t, f)
	seedDefinition(f, newDefinition("other.example.com", "machines", "machine", "Machine", true))
	f.seed("/apis/other.example.com/v1/machines/m", map[string]any{
		"apiVersion": "other.example.com/v1", "kind": "Machine", "metadata": map[string]any{"name": "m"},
	})

	stdout, stderr, code := execute(t, f, "get", "machine")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, tableOf["machine"], stdout)

	t.Run("control: the definition's own name reaches its objects", func(t *testing.T) {
		stdout, stderr, code := execute(t, f, "get", "machines.other.example.com")
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, "NAME  GENERATION\nm     0\n", stdout)
	})
}

func TestServer_WithoutTheComputeRootCannotReachOtherGroups(t *testing.T) {
	f := newFakeServer(t)
	seedExtensions(t, f)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"get a role", []string{"get", "clusterrole"}, "does not end in /apis/compute.gaon.garam.sh/v1alpha1"},
		{"apply a role", []string{"apply", "-f", writeFile(t, clusterRoleYAML)}, "does not end in /apis/compute.gaon.garam.sh/v1alpha1"},
		{"get a registered kind", []string{"get", "widget"}, "registered kinds are not looked up"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, code := executeAt(t, f.url(), tt.args...)

			assert.Equal(t, command.ExitUsage, code)
			assert.Contains(t, stderr, tt.want)
			assert.Zero(t, f.count(), "requests sent")
		})
	}

	t.Run("a compute document before a role is not sent either", func(t *testing.T) {
		before := f.writes()

		_, stderr, code := executeAt(t, f.url(), "apply", "-f", writeFile(t, machineYAML+"---\n"+clusterRoleYAML))

		assert.Equal(t, command.ExitUsage, code, stderr)
		assert.Equal(t, before, f.writes(), "writes sent")
	})

	t.Run("control: the compute kinds are served at a root without the suffix", func(t *testing.T) {
		before := f.writes()

		stdout, stderr, code := executeAt(t, f.url(), "apply", "-f", writeFile(t, machineYAML))

		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, "machine/web created\n", stdout)
		assert.Equal(t, before+1, f.writes())
	})

	t.Run("control: the same commands with the compute root are served", func(t *testing.T) {
		for _, args := range [][]string{{"get", "clusterrole"}, {"get", "widget"}, {"apply", "-f", writeFile(t, clusterRoleYAML)}} {
			_, stderr, code := execute(t, f, args...)
			assert.Equal(t, command.ExitOK, code, "%v: %s", args, stderr)
		}
	})

	t.Run("a trailing slash on the root is the same root", func(t *testing.T) {
		_, stderr, code := executeAt(t, f.url()+computePrefix+"/", "get", "crd")
		assert.Equal(t, command.ExitOK, code, stderr)
	})
}
