package api_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func ptr[T any](v T) *T { return &v }

func secretRef(name, key string) *api.ValueSource {
	return &api.ValueSource{SecretKeyRef: &api.KeyReference{Name: name, Key: key}}
}

func tcpPorts() []api.MicroVMPort {
	return []api.MicroVMPort{{Name: "http", Port: 8080, Protocol: api.ProtocolTCP}, {Port: 53, Protocol: api.ProtocolUDP}}
}

// A spec rule is tested as a pair: a MicroVM that breaks only that rule is
// Invalid, and the nearest MicroVM that does not is valid.
func TestMicroVMValidateSpecRules(t *testing.T) {
	tests := map[string]struct {
		mutate func(m *api.MicroVM)
		valid  bool
	}{
		"every new field set": {func(m *api.MicroVM) {
			m.Spec.Ports = tcpPorts()
			m.Spec.Command = []string{"/bin/app"}
			m.Spec.Args = []string{"--serve"}
			m.Spec.Env = []api.EnvVar{{Name: "A", Value: "1"}, {Name: "TOKEN", ValueFrom: secretRef("creds", "token")}}
			m.Spec.User = "1000:1000"
			m.Spec.WorkingDir = "/srv"
			m.Spec.Files = []api.MicroVMFile{{Path: "/etc/app.conf", Mode: ptr(int32(0o600)), Owner: &api.FileOwner{UID: 1000, GID: 1000}, Content: "x"}}
			m.Spec.ReadinessProbe = &api.Probe{HTTPGet: &api.HTTPGetAction{Path: "/ready", Port: api.PortRef{Name: "http"}}, PeriodSeconds: 5}
			m.Spec.LivenessProbe = &api.Probe{TCPSocket: &api.TCPSocketAction{Port: api.PortRef{Number: 8080}}}
			m.Spec.TerminationGracePeriodSeconds = ptr(int32(0))
			m.Spec.ServiceAccountName = "web"
		}, true},

		"port below range":             {func(m *api.MicroVM) { m.Spec.Ports = []api.MicroVMPort{{Port: 0, Protocol: api.ProtocolTCP}} }, false},
		"port above range":             {func(m *api.MicroVM) { m.Spec.Ports = []api.MicroVMPort{{Port: 65536, Protocol: api.ProtocolTCP}} }, false},
		"port at the top of the range": {func(m *api.MicroVM) { m.Spec.Ports = []api.MicroVMPort{{Port: 65535, Protocol: api.ProtocolTCP}} }, true},
		"port protocol missing":        {func(m *api.MicroVM) { m.Spec.Ports = []api.MicroVMPort{{Port: 80}} }, false},
		"port protocol unknown":        {func(m *api.MicroVM) { m.Spec.Ports = []api.MicroVMPort{{Port: 80, Protocol: "SCTP"}} }, false},
		"port name not a DNS label": {func(m *api.MicroVM) {
			m.Spec.Ports = []api.MicroVMPort{{Name: "Web_1", Port: 80, Protocol: api.ProtocolTCP}}
		}, false},
		"port name of 16 characters": {func(m *api.MicroVM) {
			m.Spec.Ports = []api.MicroVMPort{{Name: strings.Repeat("a", 16), Port: 80, Protocol: api.ProtocolTCP}}
		}, false},
		"port name of 15 characters": {func(m *api.MicroVM) {
			m.Spec.Ports = []api.MicroVMPort{{Name: strings.Repeat("a", 15), Port: 80, Protocol: api.ProtocolTCP}}
		}, true},
		"port name used twice": {func(m *api.MicroVM) {
			m.Spec.Ports = []api.MicroVMPort{{Name: "a", Port: 80, Protocol: api.ProtocolTCP}, {Name: "a", Port: 81, Protocol: api.ProtocolTCP}}
		}, false},
		"port and protocol repeated": {func(m *api.MicroVM) {
			m.Spec.Ports = []api.MicroVMPort{{Port: 80, Protocol: api.ProtocolTCP}, {Port: 80, Protocol: api.ProtocolTCP}}
		}, false},
		"port repeated on UDP and TCP": {func(m *api.MicroVM) {
			m.Spec.Ports = []api.MicroVMPort{{Port: 80, Protocol: api.ProtocolTCP}, {Port: 80, Protocol: api.ProtocolUDP}}
		}, true},
		"command with an empty program": {func(m *api.MicroVM) { m.Spec.Command = []string{""} }, false},
		"command with an argument":      {func(m *api.MicroVM) { m.Spec.Command = []string{"/bin/app", ""} }, true},

		"env name starts with a digit": {func(m *api.MicroVM) { m.Spec.Env = []api.EnvVar{{Name: "1A"}} }, false},
		"env name has a space":         {func(m *api.MicroVM) { m.Spec.Env = []api.EnvVar{{Name: "A B"}} }, false},
		"env name empty":               {func(m *api.MicroVM) { m.Spec.Env = []api.EnvVar{{Name: ""}} }, false},
		"env name with dot and dash":   {func(m *api.MicroVM) { m.Spec.Env = []api.EnvVar{{Name: "a.b-c_1"}} }, true},
		"env name used twice":          {func(m *api.MicroVM) { m.Spec.Env = []api.EnvVar{{Name: "A"}, {Name: "A"}} }, false},
		"env value and valueFrom": {func(m *api.MicroVM) {
			m.Spec.Env = []api.EnvVar{{Name: "A", Value: "1", ValueFrom: secretRef("s", "k")}}
		}, false},
		"env valueFrom with no ref": {func(m *api.MicroVM) { m.Spec.Env = []api.EnvVar{{Name: "A", ValueFrom: &api.ValueSource{}}} }, false},
		"env valueFrom with two refs": {func(m *api.MicroVM) {
			m.Spec.Env = []api.EnvVar{{Name: "A", ValueFrom: &api.ValueSource{
				SecretKeyRef:    &api.KeyReference{Name: "s", Key: "k"},
				ConfigMapKeyRef: &api.KeyReference{Name: "c", Key: "k"},
			}}}
		}, false},
		"env ref name not a subdomain": {func(m *api.MicroVM) { m.Spec.Env = []api.EnvVar{{Name: "A", ValueFrom: secretRef("Bad_Name", "k")}} }, false},
		"env ref key with a slash":     {func(m *api.MicroVM) { m.Spec.Env = []api.EnvVar{{Name: "A", ValueFrom: secretRef("s", "a/b")}} }, false},
		"env ref to a ConfigMap": {func(m *api.MicroVM) {
			m.Spec.Env = []api.EnvVar{{Name: "A", ValueFrom: &api.ValueSource{ConfigMapKeyRef: &api.KeyReference{Name: "c", Key: "k"}}}}
		}, true},

		"user with an empty group": {func(m *api.MicroVM) { m.Spec.User = "app:" }, false},
		"user with three parts":    {func(m *api.MicroVM) { m.Spec.User = "a:b:c" }, false},
		"user by name and group":   {func(m *api.MicroVM) { m.Spec.User = "app:wheel" }, true},
		"working dir relative":     {func(m *api.MicroVM) { m.Spec.WorkingDir = "srv" }, false},

		"file path relative":      {func(m *api.MicroVM) { m.Spec.Files = []api.MicroVMFile{{Path: "etc/a"}} }, false},
		"file path not clean":     {func(m *api.MicroVM) { m.Spec.Files = []api.MicroVMFile{{Path: "/etc/../a"}} }, false},
		"file path is the root":   {func(m *api.MicroVM) { m.Spec.Files = []api.MicroVMFile{{Path: "/"}} }, false},
		"file path with a NUL":    {func(m *api.MicroVM) { m.Spec.Files = []api.MicroVMFile{{Path: "/a\x00b"}} }, false},
		"file path used twice":    {func(m *api.MicroVM) { m.Spec.Files = []api.MicroVMFile{{Path: "/a"}, {Path: "/a"}} }, false},
		"file mode above 0777":    {func(m *api.MicroVM) { m.Spec.Files = []api.MicroVMFile{{Path: "/a", Mode: ptr(int32(0o1000))}} }, false},
		"file mode negative":      {func(m *api.MicroVM) { m.Spec.Files = []api.MicroVMFile{{Path: "/a", Mode: ptr(int32(-1))}} }, false},
		"file mode 0":             {func(m *api.MicroVM) { m.Spec.Files = []api.MicroVMFile{{Path: "/a", Mode: ptr(int32(0))}} }, true},
		"file owner negative uid": {func(m *api.MicroVM) { m.Spec.Files = []api.MicroVMFile{{Path: "/a", Owner: &api.FileOwner{UID: -1}}} }, false},
		"file owner negative gid": {func(m *api.MicroVM) { m.Spec.Files = []api.MicroVMFile{{Path: "/a", Owner: &api.FileOwner{GID: -1}}} }, false},
		"file content and valueFrom": {func(m *api.MicroVM) {
			m.Spec.Files = []api.MicroVMFile{{Path: "/a", Content: "x", ValueFrom: secretRef("s", "k")}}
		}, false},
		"file valueFrom with no ref": {func(m *api.MicroVM) { m.Spec.Files = []api.MicroVMFile{{Path: "/a", ValueFrom: &api.ValueSource{}}} }, false},
		"file from a Secret":         {func(m *api.MicroVM) { m.Spec.Files = []api.MicroVMFile{{Path: "/a", ValueFrom: secretRef("s", "k")}} }, true},
		"65 files": {func(m *api.MicroVM) {
			for i := range api.MaxFiles + 1 {
				m.Spec.Files = append(m.Spec.Files, api.MicroVMFile{Path: "/f" + string(rune('A'+i/26)) + string(rune('a'+i%26))})
			}
		}, false},
		"64 files": {func(m *api.MicroVM) {
			for i := range api.MaxFiles {
				m.Spec.Files = append(m.Spec.Files, api.MicroVMFile{Path: "/f" + string(rune('A'+i/26)) + string(rune('a'+i%26))})
			}
		}, true},
		"inline content above 1 MiB": {func(m *api.MicroVM) {
			m.Spec.Files = []api.MicroVMFile{{Path: "/a", Content: strings.Repeat("x", api.MaxFilesBytes/2)}, {Path: "/b", Content: strings.Repeat("x", api.MaxFilesBytes/2+1)}}
		}, false},
		"inline content of 1 MiB": {func(m *api.MicroVM) {
			m.Spec.Files = []api.MicroVMFile{{Path: "/a", Content: strings.Repeat("x", api.MaxFilesBytes/2)}, {Path: "/b", Content: strings.Repeat("x", api.MaxFilesBytes/2)}}
		}, true},

		"probe with no handler": {func(m *api.MicroVM) { m.Spec.ReadinessProbe = &api.Probe{} }, false},
		"probe with two handlers": {func(m *api.MicroVM) {
			m.Spec.ReadinessProbe = &api.Probe{Exec: &api.ExecAction{Command: []string{"true"}}, TCPSocket: &api.TCPSocketAction{Port: api.PortRef{Number: 80}}}
		}, false},
		"probe exec with an empty program": {func(m *api.MicroVM) { m.Spec.ReadinessProbe = &api.Probe{Exec: &api.ExecAction{Command: []string{""}}} }, false},
		"probe exec with no command":       {func(m *api.MicroVM) { m.Spec.ReadinessProbe = &api.Probe{Exec: &api.ExecAction{}} }, false},
		"probe exec": {func(m *api.MicroVM) {
			m.Spec.ReadinessProbe = &api.Probe{Exec: &api.ExecAction{Command: []string{"true"}}}
		}, true},
		"probe port number out of range": {func(m *api.MicroVM) {
			m.Spec.ReadinessProbe = &api.Probe{TCPSocket: &api.TCPSocketAction{Port: api.PortRef{Number: 70000}}}
		}, false},
		"probe port unset": {func(m *api.MicroVM) { m.Spec.ReadinessProbe = &api.Probe{TCPSocket: &api.TCPSocketAction{}} }, false},
		"probe port name and number": {func(m *api.MicroVM) {
			m.Spec.Ports = tcpPorts()
			m.Spec.ReadinessProbe = &api.Probe{TCPSocket: &api.TCPSocketAction{Port: api.PortRef{Name: "http", Number: 80}}}
		}, false},
		"probe port name not declared": {func(m *api.MicroVM) {
			m.Spec.Ports = tcpPorts()
			m.Spec.ReadinessProbe = &api.Probe{TCPSocket: &api.TCPSocketAction{Port: api.PortRef{Name: "admin"}}}
		}, false},
		"probe port name declared": {func(m *api.MicroVM) {
			m.Spec.Ports = tcpPorts()
			m.Spec.ReadinessProbe = &api.Probe{TCPSocket: &api.TCPSocketAction{Port: api.PortRef{Name: "http"}}}
		}, true},
		"probe httpGet path without slash": {func(m *api.MicroVM) {
			m.Spec.ReadinessProbe = &api.Probe{HTTPGet: &api.HTTPGetAction{Path: "ready", Port: api.PortRef{Number: 80}}}
		}, false},
		"probe httpGet port name not declared": {func(m *api.MicroVM) {
			m.Spec.ReadinessProbe = &api.Probe{HTTPGet: &api.HTTPGetAction{Port: api.PortRef{Name: "admin"}}}
		}, false},
		"probe negative initial delay": {func(m *api.MicroVM) {
			m.Spec.ReadinessProbe = &api.Probe{Exec: &api.ExecAction{Command: []string{"x"}}, InitialDelaySeconds: -1}
		}, false},
		"probe negative period": {func(m *api.MicroVM) {
			m.Spec.ReadinessProbe = &api.Probe{Exec: &api.ExecAction{Command: []string{"x"}}, PeriodSeconds: -1}
		}, false},
		"probe negative timeout": {func(m *api.MicroVM) {
			m.Spec.ReadinessProbe = &api.Probe{Exec: &api.ExecAction{Command: []string{"x"}}, TimeoutSeconds: -1}
		}, false},
		"probe negative success": {func(m *api.MicroVM) {
			m.Spec.ReadinessProbe = &api.Probe{Exec: &api.ExecAction{Command: []string{"x"}}, SuccessThreshold: -1}
		}, false},
		"probe negative failure": {func(m *api.MicroVM) {
			m.Spec.ReadinessProbe = &api.Probe{Exec: &api.ExecAction{Command: []string{"x"}}, FailureThreshold: -1}
		}, false},
		"liveness probe invalid": {func(m *api.MicroVM) { m.Spec.LivenessProbe = &api.Probe{} }, false},
		"liveness success threshold 2": {func(m *api.MicroVM) {
			m.Spec.LivenessProbe = &api.Probe{Exec: &api.ExecAction{Command: []string{"x"}}, SuccessThreshold: 2}
		}, false},
		"liveness success threshold 1": {func(m *api.MicroVM) {
			m.Spec.LivenessProbe = &api.Probe{Exec: &api.ExecAction{Command: []string{"x"}}, SuccessThreshold: 1}
		}, true},
		"readiness success threshold 2": {func(m *api.MicroVM) {
			m.Spec.ReadinessProbe = &api.Probe{Exec: &api.ExecAction{Command: []string{"x"}}, SuccessThreshold: 2}
		}, true},

		"grace period negative":          {func(m *api.MicroVM) { m.Spec.TerminationGracePeriodSeconds = ptr(int32(-1)) }, false},
		"service account name not valid": {func(m *api.MicroVM) { m.Spec.ServiceAccountName = "Web:1" }, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			m := newMicroVM("m1")
			tt.mutate(m)
			err := m.Validate()
			if tt.valid {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, api.ErrInvalid)
		})
	}
}

func TestMicroVMWithoutTheNewFieldsKeepsItsJSON(t *testing.T) {
	b, err := json.Marshal(newMicroVM("m1").Spec)
	require.NoError(t, err)
	assert.JSONEq(t, `{"image":"alpine","resources":{"requests":{"cpu":"500m","memory":"4Gi"},"limits":{}},"runStrategy":"Always"}`, string(b))
}

func TestMicroVMNumericPortsNoLongerDecode(t *testing.T) {
	var spec api.MicroVMSpec
	assert.Error(t, json.Unmarshal([]byte(`{"ports":[80,443]}`), &spec))

	require.NoError(t, json.Unmarshal([]byte(`{"ports":[{"name":"web","port":80,"protocol":"TCP"}]}`), &spec),
		"control: the object form decodes")
	assert.Equal(t, []api.MicroVMPort{{Name: "web", Port: 80, Protocol: api.ProtocolTCP}}, spec.Ports)
}

func TestProbePortRefRoundTripsAsNumberOrName(t *testing.T) {
	for in, want := range map[string]api.PortRef{
		`{"port":8080}`:   {Number: 8080},
		`{"port":"http"}`: {Name: "http"},
	} {
		var a api.TCPSocketAction
		require.NoError(t, json.Unmarshal([]byte(in), &a))
		assert.Equal(t, want, a.Port)
		b, err := json.Marshal(a)
		require.NoError(t, err)
		assert.JSONEq(t, in, string(b))
	}
	var a api.TCPSocketAction
	assert.Error(t, json.Unmarshal([]byte(`{"port":true}`), &a))
}

func TestMicroVMUpdateFreezesTheNewFields(t *testing.T) {
	mutations := map[string]func(m *api.MicroVM){
		"ports":      func(m *api.MicroVM) { m.Spec.Ports = []api.MicroVMPort{{Port: 80, Protocol: api.ProtocolTCP}} },
		"command":    func(m *api.MicroVM) { m.Spec.Command = []string{"/x"} },
		"args":       func(m *api.MicroVM) { m.Spec.Args = []string{"x"} },
		"env":        func(m *api.MicroVM) { m.Spec.Env = []api.EnvVar{{Name: "A"}} },
		"user":       func(m *api.MicroVM) { m.Spec.User = "1" },
		"workingDir": func(m *api.MicroVM) { m.Spec.WorkingDir = "/x" },
		"files":      func(m *api.MicroVM) { m.Spec.Files = []api.MicroVMFile{{Path: "/x"}} },
		"readinessProbe": func(m *api.MicroVM) {
			m.Spec.ReadinessProbe = &api.Probe{Exec: &api.ExecAction{Command: []string{"x"}}}
		},
		"livenessProbe":                 func(m *api.MicroVM) { m.Spec.LivenessProbe = &api.Probe{Exec: &api.ExecAction{Command: []string{"x"}}} },
		"terminationGracePeriodSeconds": func(m *api.MicroVM) { m.Spec.TerminationGracePeriodSeconds = ptr(int32(5)) },
		"serviceAccountName":            func(m *api.MicroVM) { m.Spec.ServiceAccountName = "web" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			old, next := newMicroVM("m1"), newMicroVM("m1")
			mutate(next)
			require.NoError(t, next.Validate(), "the change is valid; only freezing refuses it")
			assert.ErrorIs(t, next.ValidateUpdate(old), api.ErrInvalid)
		})
	}
}

func TestMicroVMReferencesSecretsAndConfigMaps(t *testing.T) {
	m := newMicroVM("m1")
	m.Spec.Env = []api.EnvVar{{Name: "A", ValueFrom: secretRef("env-secret", "k")}, {Name: "B", ValueFrom: &api.ValueSource{ConfigMapKeyRef: &api.KeyReference{Name: "env-config", Key: "k"}}}}
	m.Spec.Files = []api.MicroVMFile{
		{Path: "/a", ValueFrom: secretRef("file-secret", "k")},
		{Path: "/b", ValueFrom: &api.ValueSource{ConfigMapKeyRef: &api.KeyReference{Name: "file-config", Key: "k"}}},
		{Path: "/c", Content: "inline"},
	}
	for _, name := range []string{"env-secret", "file-secret"} {
		assert.True(t, m.ReferencesSecret(name), name)
		assert.False(t, m.ReferencesConfigMap(name), "%s is a Secret, not a ConfigMap", name)
	}
	for _, name := range []string{"env-config", "file-config"} {
		assert.True(t, m.ReferencesConfigMap(name), name)
		assert.False(t, m.ReferencesSecret(name), "%s is a ConfigMap, not a Secret", name)
	}
	assert.False(t, m.ReferencesSecret("other"))
	assert.False(t, newMicroVM("m2").ReferencesSecret("env-secret"))
}
