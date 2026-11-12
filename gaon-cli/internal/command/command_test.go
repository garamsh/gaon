package command_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	tests := []struct {
		name, file, message string
	}{
		{"invalid spec", invalid, "runStrategy"},
		{"unknown field", unknownField, "colour"},
		{"unknown kind", unknownKind, "Widget"},
		{"malformed YAML", notYAML, "document 2"},
		{"no resource", "# nothing\n", "no resource"},
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

const volumeYAML = `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: Volume
metadata:
  name: data
spec:
  storageClass: local
  accessModes: [ReadWriteOnce]
  storage: 10Gi
`

var (
	t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// tableOf names the table each kind prints, for the objects seedCluster stores.
	tableOf = map[string]string{
		"machine": "NAME  PHASE    NODE  EPOCH\nold   Pending  n2    3\nweb   Running  n1    2\n",
		"node":    "NAME  STATE     ADMITTED\nn1    Admitted  true\n",
		"lease":   "NAME  HOLDER  DURATION\nn1    n1      15s\n",
		"volume":  "NAME  CLASS  SIZE  PHASE      NODE\ndata  local  10Gi  Available  n1\n",
	}
)

// seedCluster stores one or two objects of each kind and verifies they are there.
func seedCluster(t *testing.T, f *fakeServer) {
	t.Helper()
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
		Spec:     api.LeaseSpec{Holder: "n1", RenewTime: t0, DurationSeconds: 15},
	})
	f.seed("/volumes/data", api.Volume{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolume},
		Metadata: api.ObjectMeta{Name: "data"},
		Spec:     api.VolumeSpec{StorageClass: api.StorageClassLocal, AccessModes: []string{api.AccessReadWriteOnce}, Storage: "10Gi", NodeName: "n1"},
		Status:   api.VolumeStatus{Phase: api.VolumeAvailable},
	})
	require.Equal(t, api.MachineRunning, load[api.Machine](f, "/machines/web").Status.Phase)
	require.Equal(t, api.VolumeAvailable, load[api.Volume](f, "/volumes/data").Status.Phase)
}

func TestGet_ListsAKindWithoutANameAndReadsOneWithAName(t *testing.T) {
	f := newFakeServer(t)
	seedCluster(t, f)

	tests := []struct {
		name   string
		args   []string
		stdout string
	}{
		{"every machine", []string{"get", "machine"}, tableOf["machine"]},
		{"one machine", []string{"get", "machine", "web"}, "NAME  PHASE    NODE  EPOCH\nweb   Running  n1    2\n"},
		{"a void status reads as Pending", []string{"get", "machine", "old"}, "NAME  PHASE    NODE  EPOCH\nold   Pending  n2    3\n"},
		{"every node", []string{"get", "node"}, tableOf["node"]},
		{"one lease", []string{"get", "lease", "n1"}, tableOf["lease"]},
		{"every volume", []string{"get", "volume"}, tableOf["volume"]},
		{"one volume", []string{"get", "volume", "data"}, tableOf["volume"]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, code := execute(t, f, tt.args...)
			require.Equal(t, command.ExitOK, code, stderr)
			assert.Equal(t, tt.stdout, stdout)
		})
	}

	t.Run("a missing resource fails", func(t *testing.T) {
		_, stderr, code := execute(t, f, "get", "machine", "nope")
		assert.Equal(t, command.ExitFailure, code)
		assert.Contains(t, stderr, "NotFound")
	})

	t.Run("an empty kind prints its header", func(t *testing.T) {
		empty := newFakeServer(t)
		stdout, stderr, code := execute(t, empty, "get", "node")
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, "NAME  STATE  ADMITTED\n", stdout)
	})
}

func TestGet_PrintsATableByDefaultAndTheObjectOnRequest(t *testing.T) {
	f := newFakeServer(t)
	seedCluster(t, f)

	t.Run("table is the default", func(t *testing.T) {
		byDefault, stderr, code := execute(t, f, "get", "machine", "web")
		require.Equal(t, command.ExitOK, code, stderr)
		asked, _, _ := execute(t, f, "get", "-o", "table", "machine", "web")
		assert.Equal(t, asked, byDefault)
		assert.True(t, strings.HasPrefix(byDefault, "NAME  PHASE"), byDefault)
	})

	t.Run("yaml names fields as the API does", func(t *testing.T) {
		stdout, stderr, code := execute(t, f, "get", "-o", "yaml", "machine", "web")
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Contains(t, stdout, "kind: Machine\n")
		assert.Contains(t, stdout, "runStrategy: Always")
		assert.Contains(t, stdout, "bindingEpoch: 2")
	})

	t.Run("json is the object", func(t *testing.T) {
		stdout, stderr, code := execute(t, f, "get", "-o", "json", "machine", "web")
		require.Equal(t, command.ExitOK, code, stderr)
		var m api.Machine
		require.NoError(t, json.Unmarshal([]byte(stdout), &m))
		assert.Equal(t, "web", m.Metadata.Name)
		assert.Equal(t, api.Epoch(2), m.Spec.BindingEpoch)
		assert.True(t, strings.HasSuffix(stdout, "}\n"), "one trailing newline")
	})

	t.Run("a list in yaml and json is the list object", func(t *testing.T) {
		stdout, stderr, code := execute(t, f, "get", "-o", "yaml", "machine")
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Contains(t, stdout, "kind: MachineList")
		assert.Contains(t, stdout, "name: old")
		assert.Contains(t, stdout, "name: web")

		stdout, stderr, code = execute(t, f, "get", "-o", "json", "machine")
		require.Equal(t, command.ExitOK, code, stderr)
		var l api.MachineList
		require.NoError(t, json.Unmarshal([]byte(stdout), &l))
		assert.Len(t, l.Items, 2)
	})
}

func TestGet_AcceptsAKindAsItsSingularItsPluralOrItsShortName(t *testing.T) {
	f := newFakeServer(t)
	seedCluster(t, f)

	spellings := map[string][]string{
		"machine": {"machine", "machines", "mc", "Machines", "MC"},
		"node":    {"node", "nodes", "no", "Nodes"},
		"lease":   {"lease", "leases", "le"},
		"volume":  {"volume", "volumes", "vol", "Volumes"},
	}
	for kind, names := range spellings {
		for _, name := range names {
			t.Run(kind+" as "+name, func(t *testing.T) {
				stdout, stderr, code := execute(t, f, "get", name)
				require.Equal(t, command.ExitOK, code, stderr)
				assert.Equal(t, tableOf[kind], stdout)
			})
		}
	}
}

func TestGet_ReadsFlagsWhereverTheyStandAmongTheOperands(t *testing.T) {
	f := newFakeServer(t)
	seedCluster(t, f)
	want, stderr, code := execute(t, f, "get", "-o", "yaml", "machine", "web")
	require.Equal(t, command.ExitOK, code, stderr)
	require.Contains(t, want, "kind: Machine")

	tests := []struct {
		name string
		args []string
	}{
		{"before the operands", []string{"get", "-o", "yaml", "machine", "web"}},
		{"between them", []string{"get", "machine", "-o", "yaml", "web"}},
		{"after them", []string{"get", "machine", "web", "-o", "yaml"}},
		{"with an equals sign", []string{"get", "machine", "web", "-o=yaml"}},
		{"before a double dash", []string{"get", "-o", "yaml", "--", "machine", "web"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, code := execute(t, f, tt.args...)
			require.Equal(t, command.ExitOK, code, stderr)
			assert.Equal(t, want, stdout)
		})
	}

	t.Run("after a double dash a flag is an operand", func(t *testing.T) {
		_, stderr, code := execute(t, f, "get", "machine", "--", "web", "-o", "yaml")
		assert.Equal(t, command.ExitUsage, code)
		assert.Contains(t, stderr, "get: want KIND [NAME]")
	})

	t.Run("an unknown flag after an operand is refused", func(t *testing.T) {
		_, stderr, code := execute(t, f, "get", "machine", "web", "-x")
		assert.Equal(t, command.ExitUsage, code)
		assert.Contains(t, stderr, "-x")
	})
}

func TestApply_CreatesAVolumeAndDeleteMarksIt(t *testing.T) {
	f := newFakeServer(t)

	stdout, stderr, code := execute(t, f, "apply", "-f", writeFile(t, volumeYAML))
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "volume/data created\n", stdout)
	assert.Equal(t, "10Gi", string(load[api.Volume](f, "/volumes/data").Spec.Storage))

	stdout, stderr, code = execute(t, f, "delete", "volumes", "data")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "volume/data marked for deletion\n", stdout)
	assert.NotNil(t, load[api.Volume](f, "/volumes/data").Metadata.DeletionTimestamp)
}

func TestDescribe_PrintsTheObjectAndItsStatusConditions(t *testing.T) {
	f := newFakeServer(t)
	seedCluster(t, f)
	m := newMachine("broken", api.RunAlways)
	m.Spec.Image, m.Spec.NodeName, m.Spec.Volumes = "ubuntu", "n1", []api.MachineVolume{{Name: "data", MountPath: "/data"}}
	m.Spec.Resources.Requests = api.ResourceList{CPU: "500m", Memory: "1Gi"}
	m.Status = api.MachineStatus{Phase: api.MachineFailed, Conditions: []api.Condition{
		{Type: "Ready", Status: api.ConditionFalse, Reason: "ImagePullFailed", Message: "no such image", LastTransitionTime: &t0},
		{Type: "Scheduled", Status: api.ConditionTrue},
	}}
	f.seed("/machines/broken", m)
	f.seed("/nodes/n2", api.Node{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNode},
		Metadata: api.ObjectMeta{Name: "n2"},
		Status: api.NodeStatus{State: api.NodeStateDiscovered, Addresses: []string{"10.0.0.2"}, Conditions: []api.Condition{
			{Type: "LeaseExpired", Status: api.ConditionTrue, Reason: "LeaseMissing"},
		}},
	})

	tests := []struct {
		name    string
		args    []string
		matches []string
		absent  []string
	}{
		{"a machine with conditions", []string{"describe", "machine", "broken"}, []string{
			`(?m)^Name:\s+broken$`,
			`(?m)^Kind:\s+Machine$`,
			`(?m)^Image:\s+ubuntu$`,
			`(?m)^Requests:\s+cpu=500m, memory=1Gi$`,
			`(?m)^Volumes:\s+data:/data$`,
			`(?m)^Conditions:$`,
			`(?m)^\s+Ready\s+False\s+ImagePullFailed\s+2026-01-01T00:00:00Z\s+no such image$`,
			`(?m)^\s+Scheduled\s+True\s+-\s+-\s+-$`,
		}, nil},
		{"a node, by short name", []string{"describe", "no", "n2"}, []string{
			`(?m)^State:\s+Discovered$`,
			`(?m)^Addresses:\s+10\.0\.0\.2$`,
			`(?m)^\s+LeaseExpired\s+True\s+LeaseMissing`,
		}, nil},
		{"a machine with no condition", []string{"describe", "machine", "web"}, []string{
			`(?m)^Phase:\s+Running$`,
			"(?m)^Conditions:\n  -$",
		}, nil},
		{"a lease has no conditions section", []string{"describe", "lease", "n1"}, []string{
			`(?m)^Holder:\s+n1$`,
			`(?m)^Duration:\s+15s$`,
		}, []string{"Conditions"}},
		{"a volume", []string{"describe", "volume", "data"}, []string{
			`(?m)^Storage class:\s+local$`,
			`(?m)^Storage:\s+10Gi$`,
			`(?m)^Node:\s+n1$`,
		}, []string{"Conditions"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, code := execute(t, f, tt.args...)
			require.Equal(t, command.ExitOK, code, stderr)
			for _, re := range tt.matches {
				assert.Regexp(t, re, stdout)
			}
			for _, s := range tt.absent {
				assert.NotContains(t, stdout, s)
			}
		})
	}

	t.Run("a missing object fails", func(t *testing.T) {
		_, stderr, code := execute(t, f, "describe", "machine", "nope")
		assert.Equal(t, command.ExitFailure, code)
		assert.Contains(t, stderr, "NotFound")
	})
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
		{[]string{"delete", "nodes", "n1"}, "DELETE /nodes/{name}"},
		{[]string{"delete", "lease", "n1"}, "DELETE /leases/{name}"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			f := newFakeServer(t)
			seedCluster(t, f)
			before := f.count()

			_, stderr, code := execute(t, f, tt.args...)

			assert.Equal(t, command.ExitFailure, code)
			assert.Contains(t, stderr, "refused")
			assert.Contains(t, stderr, tt.path)
			assert.Equal(t, before, f.count(), "requests sent")
		})
	}

	t.Run("control: a verb with a path is sent", func(t *testing.T) {
		f := newFakeServer(t)
		f.seed("/machines/web", newMachine("web", api.RunAlways))
		_, stderr, code := execute(t, f, "delete", "machine", "web")
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, []string{"GET /machines/web", "DELETE /machines/web"}, f.seen())
	})

	t.Run("control: the volume paths the contract gained are sent", func(t *testing.T) {
		f := newFakeServer(t)
		seedCluster(t, f)
		_, stderr, code := execute(t, f, "delete", "volume", "data")
		require.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, []string{"GET /volumes/data", "DELETE /volumes/data"}, f.seen())
	})
}

func TestUnknownKind_IsRefusedWithTheKindsThereAreAndAKnownOneIsServed(t *testing.T) {
	f := newFakeServer(t)
	seedCluster(t, f)
	before := f.count()

	for _, args := range [][]string{
		{"get", "widget"},
		{"get", "widgets", "x"},
		{"get", "machinez"},
		{"describe", "widget", "x"},
		{"delete", "widget", "x"},
	} {
		_, stderr, code := execute(t, f, args...)

		assert.Equal(t, command.ExitUsage, code, args)
		message := strings.SplitN(stderr, "\n", 2)[0]
		assert.Contains(t, message, "unknown kind", args)
		assert.Contains(t, message, "want one of machine (machines, mc)", args)
	}
	assert.Equal(t, before, f.count(), "requests sent")

	for _, args := range [][]string{{"get", "machines"}, {"describe", "mc", "web"}, {"delete", "machine", "old"}} {
		_, stderr, code := execute(t, f, args...)
		assert.Equal(t, command.ExitOK, code, "%v: %s", args, stderr)
	}
	assert.Greater(t, f.count(), before)
}

func TestGet_RefusesAnUnknownOutputFormatAndServesTheKnownOnes(t *testing.T) {
	f := newFakeServer(t)
	seedCluster(t, f)
	before := f.count()

	_, stderr, code := execute(t, f, "get", "-o", "xml", "machine", "web")
	assert.Equal(t, command.ExitUsage, code)
	assert.Contains(t, stderr, `unknown output format "xml", want table, yaml or json`)
	assert.Equal(t, before, f.count(), "requests sent")

	for _, format := range []string{"table", "yaml", "json"} {
		stdout, stderr, code := execute(t, f, "get", "-o", format, "machine", "web")
		assert.Equal(t, command.ExitOK, code, "%s: %s", format, stderr)
		assert.NotEmpty(t, stdout, format)
	}
}

func TestListVerb_IsNotACommand(t *testing.T) {
	f := newFakeServer(t)
	seedCluster(t, f)
	before := f.count()

	_, stderr, code := execute(t, f, "list", "machine")
	assert.Equal(t, command.ExitUsage, code)
	assert.Contains(t, stderr, `unknown command "list"`)
	assert.Equal(t, before, f.count(), "requests sent")

	stdout, stderr, code := execute(t, f, "get", "machine")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, tableOf["machine"], stdout)
}

func TestUsageMistakes_ExitWithUsageAndSendNothing(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no command", nil},
		{"unknown command", []string{"frobnicate"}},
		{"get without a kind", []string{"get"}},
		{"get with three operands", []string{"get", "machine", "web", "extra"}},
		{"unknown flag", []string{"get", "-x", "machine", "web"}},
		{"apply without a file", []string{"apply"}},
		{"delete without a name", []string{"delete", "machine"}},
		{"describe without a name", []string{"describe", "machine"}},
		{"describe with a flag", []string{"describe", "-o", "yaml", "machine", "web"}},
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

	t.Run("control: a complete command is accepted", func(t *testing.T) {
		f := newFakeServer(t)
		seedCluster(t, f)
		_, stderr, code := execute(t, f, "describe", "machine", "web")
		assert.Equal(t, command.ExitOK, code, stderr)
	})

	t.Run("help succeeds", func(t *testing.T) {
		f := newFakeServer(t)
		stdout, _, code := execute(t, f, "help")
		assert.Equal(t, command.ExitOK, code)
		assert.Contains(t, stdout, "gaon [--server URL] apply -f FILE")
		assert.Contains(t, stdout, "gaon [--server URL] describe KIND NAME")
		assert.Contains(t, stdout, "volume (volumes, vol)")
		assert.Contains(t, stdout, "/apis/compute.gaon.garam.sh/v1alpha1")
	})
}

func TestApply_ReportsAnUnreadableFile(t *testing.T) {
	f := newFakeServer(t)
	_, stderr, code := execute(t, f, "apply", "-f", filepath.Join(t.TempDir(), "missing.yaml"))
	assert.Equal(t, command.ExitFailure, code)
	assert.Contains(t, stderr, "missing.yaml")
}
