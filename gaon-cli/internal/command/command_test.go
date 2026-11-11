package command_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gaon-cli/internal/command"
	"github.com/garamsh/gaon/lib/api"
)

const machineYAML = `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: Machine
metadata:
  name: web
spec:
  image: ubuntu
  runStrategy: Always
  resources:
    requests:
      cpu: 500m
      memory: 1Gi
`

const nodeYAML = `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: Node
metadata:
  name: n1
spec:
  admitted: true
`

const leaseYAML = `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: Lease
metadata:
  name: n1
spec:
  holder: n1
  renewTime: "2026-01-01T00:00:00Z"
  durationSeconds: 15
`

// execute runs one gaon command against f and returns what it printed.
func execute(t *testing.T, f *fakeServer, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = command.Run(context.Background(), api.NewClient(f.url(), nil), args, &out, &errOut)
	return out.String(), errOut.String(), code
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "resources.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func newMachine(name string, strategy api.RunStrategy) api.Machine {
	return api.Machine{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMachine},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.MachineSpec{RunStrategy: strategy},
	}
}

func TestApply_CreatesAndThenUpdatesEachResource(t *testing.T) {
	f := newFakeServer(t)
	file := writeFile(t, machineYAML+"---\n"+nodeYAML+"---\n"+leaseYAML)

	stdout, stderr, code := execute(t, f, "apply", "-f", file)
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "machine/web created\nnode/n1 created\nlease/n1 created\n", stdout)
	assert.Equal(t, api.RunAlways, load[api.Machine](f, "/machines/web").Spec.RunStrategy)
	assert.True(t, load[api.Node](f, "/nodes/n1").Spec.Admitted)
	assert.Equal(t, int32(15), load[api.Lease](f, "/leases/n1").Spec.DurationSeconds)

	changed := writeFile(t, `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: Machine
metadata:
  name: web
spec:
  runStrategy: Halted
`)
	stdout, stderr, code = execute(t, f, "apply", "-f", changed)
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "machine/web configured\n", stdout)
	assert.Equal(t, api.RunHalted, load[api.Machine](f, "/machines/web").Spec.RunStrategy)
}

func TestApply_ReportsAConcurrentChangeAndDoesNotOverwriteIt(t *testing.T) {
	changeBehindTheCLI := func(f *fakeServer) {
		f.beforeWrite = func(method, path string) {
			if method == "PUT" {
				theirs := newMachine("web", api.RunOnce)
				theirs.Metadata.ResourceVersion = api.ResourceVersion(versionOf(f.objects[path]))
				f.store(path, toMap(t, theirs))
				f.beforeWrite = nil
			}
		}
	}
	file := writeFile(t, machineYAML)

	tests := []struct {
		name       string
		concurrent bool
		code       int
		strategy   api.RunStrategy
	}{
		{"nobody else writes", false, command.ExitOK, api.RunAlways},
		{"someone writes between the read and the write", true, command.ExitFailure, api.RunOnce},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t)
			f.seed("/machines/web", newMachine("web", api.RunHalted))
			if tt.concurrent {
				changeBehindTheCLI(f)
			}

			_, stderr, code := execute(t, f, "apply", "-f", file)

			assert.Equal(t, tt.code, code, stderr)
			assert.Equal(t, tt.strategy, load[api.Machine](f, "/machines/web").Spec.RunStrategy)
			if tt.concurrent {
				assert.Contains(t, stderr, "changed by someone else")
				assert.Contains(t, stderr, "Conflict")
			}
		})
	}
}

func TestApply_ReportsACreateThatLostTheRace(t *testing.T) {
	f := newFakeServer(t)
	f.beforeWrite = func(method, path string) {
		f.store(path, toMap(t, newMachine("web", api.RunOnce)))
		f.beforeWrite = nil
	}

	_, stderr, code := execute(t, f, "apply", "-f", writeFile(t, machineYAML))

	assert.Equal(t, command.ExitFailure, code)
	assert.Contains(t, stderr, "changed by someone else")
	assert.Contains(t, stderr, "AlreadyExists")
	assert.Equal(t, api.RunOnce, load[api.Machine](f, "/machines/web").Spec.RunStrategy)
}

func TestApply_UsesTheVersionTheFileCarries(t *testing.T) {
	f := newFakeServer(t)
	current := f.seed("/machines/web", newMachine("web", api.RunHalted))
	f.seed("/machines/web", newMachine("web", api.RunOnce)) // moves the stored version on

	withVersion := func(rv api.ResourceVersion) string {
		return strings.Replace(machineYAML, "  name: web\n", "  name: web\n  resourceVersion: \""+string(rv)+"\"\n", 1)
	}

	_, stderr, code := execute(t, f, "apply", "-f", writeFile(t, withVersion(current)))
	assert.Equal(t, command.ExitFailure, code)
	assert.Contains(t, stderr, "Conflict")
	assert.Equal(t, api.RunOnce, load[api.Machine](f, "/machines/web").Spec.RunStrategy)

	latest := load[api.Machine](f, "/machines/web").Metadata.ResourceVersion
	_, stderr, code = execute(t, f, "apply", "-f", writeFile(t, withVersion(latest)))
	assert.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, api.RunAlways, load[api.Machine](f, "/machines/web").Spec.RunStrategy)
}

func TestApply_SendsNothingWhenAnyDocumentIsRefused(t *testing.T) {
	invalid := machineYAML + "---\n" + `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: Machine
metadata:
  name: bad
spec:
  runStrategy: Sometimes
`
	unknownField := machineYAML + "---\n" + nodeYAML + "  colour: red\n"
	unknownKind := machineYAML + "---\n" + `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: Widget
metadata:
  name: w
`
	notYAML := machineYAML + "---\nkey: [unclosed\n"
	volume := machineYAML + "---\n" + `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: Volume
metadata:
  name: data
`
	tests := []struct {
		name, file, message string
	}{
		{"invalid spec", invalid, "runStrategy"},
		{"unknown field", unknownField, "colour"},
		{"unknown kind", unknownKind, "Widget"},
		{"malformed YAML", notYAML, "document 2"},
		{"no resource", "# nothing\n", "no resource"},
		{"kind without a path", volume, "PUT /volumes/{name}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t)

			_, stderr, code := execute(t, f, "apply", "-f", writeFile(t, tt.file))

			assert.Equal(t, command.ExitFailure, code)
			assert.Contains(t, stderr, tt.message)
			assert.Zero(t, f.count(), "requests sent")
		})
	}

	t.Run("control: the valid first document alone is sent", func(t *testing.T) {
		f := newFakeServer(t)
		_, stderr, code := execute(t, f, "apply", "-f", writeFile(t, machineYAML))
		require.Equal(t, command.ExitOK, code, stderr)
		assert.NotZero(t, f.count())
	})
}

func TestGet_PrintsYAMLOrATable(t *testing.T) {
	f := newFakeServer(t)
	m := newMachine("web", api.RunAlways)
	m.Spec.NodeName, m.Spec.BindingEpoch = "n1", 2
	m.Status = api.MachineStatus{Phase: api.MachineRunning, BindingEpoch: 2}
	f.seed("/machines/web", m)
	stale := newMachine("old", api.RunAlways)
	stale.Spec.NodeName, stale.Spec.BindingEpoch = "n2", 3
	stale.Status = api.MachineStatus{Phase: api.MachineRunning, BindingEpoch: 2}
	f.seed("/machines/old", stale)
	f.seed("/nodes/n1", api.Node{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNode},
		Metadata: api.ObjectMeta{Name: "n1"},
		Spec:     api.NodeSpec{Admitted: true},
		Status:   api.NodeStatus{State: api.NodeStateAdmitted},
	})
	f.seed("/leases/n1", api.Lease{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindLease},
		Metadata: api.ObjectMeta{Name: "n1"},
		Spec:     api.LeaseSpec{Holder: "n1", DurationSeconds: 15},
	})

	tests := []struct {
		name   string
		args   []string
		stdout string
	}{
		{"machine as YAML by default", []string{"get", "machine", "web"}, "kind: Machine\n"},
		{"machine as table", []string{"get", "-o", "table", "machine", "web"}, "NAME  PHASE    NODE  EPOCH\nweb   Running  n1    2\n"},
		{"a void status reads as Pending", []string{"get", "-o", "table", "machine", "old"}, "NAME  PHASE    NODE  EPOCH\nold   Pending  n2    3\n"},
		{"node as table", []string{"get", "-o", "table", "node", "n1"}, "NAME  STATE     ADMITTED\nn1    Admitted  true\n"},
		{"lease as table", []string{"get", "-o", "table", "lease", "n1"}, "NAME  HOLDER  DURATION\nn1    n1      15s\n"},
		{"kind is case-insensitive", []string{"get", "-o", "table", "Node", "n1"}, "NAME  STATE     ADMITTED\nn1    Admitted  true\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, code := execute(t, f, tt.args...)
			require.Equal(t, command.ExitOK, code, stderr)
			assert.Contains(t, stdout, tt.stdout)
		})
	}

	t.Run("YAML names fields as the API does", func(t *testing.T) {
		stdout, _, code := execute(t, f, "get", "machine", "web")
		require.Equal(t, command.ExitOK, code)
		assert.Contains(t, stdout, "runStrategy: Always")
		assert.Contains(t, stdout, "bindingEpoch: 2")
	})

	t.Run("a missing resource fails", func(t *testing.T) {
		_, stderr, code := execute(t, f, "get", "machine", "nope")
		assert.Equal(t, command.ExitFailure, code)
		assert.Contains(t, stderr, "NotFound")
	})
}

func TestList_PrintsATableOrYAML(t *testing.T) {
	f := newFakeServer(t)
	for _, name := range []string{"b", "a"} {
		m := newMachine(name, api.RunOnce)
		m.Status.Phase = api.MachinePending
		f.seed("/machines/"+name, m)
	}

	stdout, stderr, code := execute(t, f, "list", "machine")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "NAME  PHASE    NODE  EPOCH\na     Pending  -     0\nb     Pending  -     0\n", stdout)

	stdout, stderr, code = execute(t, f, "list", "-o", "yaml", "machine")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Contains(t, stdout, "kind: MachineList")
	assert.Contains(t, stdout, "name: a")
	assert.Contains(t, stdout, "name: b")

	stdout, stderr, code = execute(t, f, "list", "node")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "NAME  STATE  ADMITTED\n", stdout)

	_, stderr, code = execute(t, f, "list", "lease")
	assert.Equal(t, command.ExitOK, code, stderr)
}

func TestDelete_MarksAMachineForDeletionAtItsCurrentVersion(t *testing.T) {
	t.Run("deletes", func(t *testing.T) {
		f := newFakeServer(t)
		f.seed("/machines/web", newMachine("web", api.RunAlways))

		stdout, stderr, code := execute(t, f, "delete", "machine", "web")

		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, "machine/web marked for deletion\n", stdout)
		assert.NotNil(t, load[api.Machine](f, "/machines/web").Metadata.DeletionTimestamp)
	})

	t.Run("a change after the read is reported", func(t *testing.T) {
		f := newFakeServer(t)
		f.seed("/machines/web", newMachine("web", api.RunAlways))
		f.beforeWrite = func(method, path string) {
			f.store(path, toMap(t, newMachine("web", api.RunOnce)))
			f.beforeWrite = nil
		}

		_, stderr, code := execute(t, f, "delete", "machine", "web")

		assert.Equal(t, command.ExitFailure, code)
		assert.Contains(t, stderr, "Conflict")
		assert.Nil(t, load[api.Machine](f, "/machines/web").Metadata.DeletionTimestamp)
	})

	t.Run("a missing machine fails", func(t *testing.T) {
		f := newFakeServer(t)
		_, stderr, code := execute(t, f, "delete", "machine", "nope")
		assert.Equal(t, command.ExitFailure, code)
		assert.Contains(t, stderr, "NotFound")
	})
}

func TestVerbWithoutAPath_IsRefusedLocallyNamingThePath(t *testing.T) {
	tests := []struct {
		args []string
		path string
	}{
		{[]string{"delete", "node", "n1"}, "DELETE /nodes/{name}"},
		{[]string{"delete", "lease", "n1"}, "DELETE /leases/{name}"},
		{[]string{"delete", "volume", "data"}, "DELETE /volumes/{name}"},
		{[]string{"get", "volume", "data"}, "GET /volumes/{name}"},
		{[]string{"list", "volume"}, "GET /volumes"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			f := newFakeServer(t)
			f.seed("/nodes/n1", map[string]any{"metadata": map[string]any{"name": "n1"}})
			f.seed("/leases/n1", map[string]any{"metadata": map[string]any{"name": "n1"}})
			f.seed("/volumes/data", map[string]any{"metadata": map[string]any{"name": "data"}})

			_, stderr, code := execute(t, f, tt.args...)

			assert.Equal(t, command.ExitFailure, code)
			assert.Contains(t, stderr, "refused")
			assert.Contains(t, stderr, tt.path)
			assert.Zero(t, f.count(), "requests sent")
		})
	}

	t.Run("control: a verb with a path is sent", func(t *testing.T) {
		f := newFakeServer(t)
		f.seed("/machines/web", newMachine("web", api.RunAlways))
		_, stderr, code := execute(t, f, "delete", "machine", "web")
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, []string{"GET /machines/web", "DELETE /machines/web"}, f.seen())
	})
}

func TestUsageMistakes_ExitWithUsageAndSendNothing(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no command", nil},
		{"unknown command", []string{"frobnicate"}},
		{"unknown kind", []string{"get", "widget", "x"}},
		{"get without a name", []string{"get", "machine"}},
		{"list with a name", []string{"list", "machine", "web"}},
		{"unknown output format", []string{"get", "-o", "json", "machine", "web"}},
		{"unknown flag", []string{"get", "-x", "machine", "web"}},
		{"apply without a file", []string{"apply"}},
		{"delete without a name", []string{"delete", "machine"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t)

			_, stderr, code := execute(t, f, tt.args...)

			assert.Equal(t, command.ExitUsage, code)
			assert.Contains(t, stderr, "usage:")
			assert.Contains(t, stderr, "/apis/compute.gaon.garam.sh/v1alpha1")
			assert.Zero(t, f.count(), "requests sent")
		})
	}

	t.Run("help succeeds", func(t *testing.T) {
		f := newFakeServer(t)
		stdout, _, code := execute(t, f, "help")
		assert.Equal(t, command.ExitOK, code)
		assert.Contains(t, stdout, "gaon [--server URL] apply -f FILE")
		assert.Contains(t, stdout, "/apis/compute.gaon.garam.sh/v1alpha1")
	})
}

func TestApply_ReportsAnUnreadableFile(t *testing.T) {
	f := newFakeServer(t)
	_, stderr, code := execute(t, f, "apply", "-f", filepath.Join(t.TempDir(), "missing.yaml"))
	assert.Equal(t, command.ExitFailure, code)
	assert.Contains(t, stderr, "missing.yaml")
}
