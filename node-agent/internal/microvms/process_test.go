package microvms_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
	"github.com/garamsh/gaon/node-agent/internal/microvms"
)

// syncBuffer is a log sink the service writes while a test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// dummy returns a random value that stands for a secret in a test.
func dummy(t *testing.T) string {
	t.Helper()
	b := make([]byte, 12)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return "dummy-" + hex.EncodeToString(b)
}

func withLog(b *syncBuffer) func(*env) { return func(e *env) { e.log = b } }

// failed waits for the MicroVM to be Failed and returns its Ready condition.
func (e *env) failed(t *testing.T, name string) api.Condition {
	t.Helper()
	eventually(t, name+" is Failed", func() bool { return e.gaon.MicroVM(name).Status.Phase == api.MicroVMFailed })
	conds := e.gaon.MicroVM(name).Status.Conditions
	require.Len(t, conds, 1)
	return conds[0]
}

func (e *env) created(id string) bool {
	_, ok := e.mvmd.CreateRequest(id)
	return ok
}

func secretRef(name, key string) *api.ValueSource {
	return &api.ValueSource{SecretKeyRef: &api.KeyReference{Name: name, Key: key}}
}

func configRef(name, key string) *api.ValueSource {
	return &api.ValueSource{ConfigMapKeyRef: &api.KeyReference{Name: name, Key: key}}
}

func secret(name string, data map[string]string) api.Secret {
	s := api.Secret{Metadata: api.ObjectMeta{Name: name}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func configMap(name string, data map[string]string) api.ConfigMap {
	return api.ConfigMap{Metadata: api.ObjectMeta{Name: name}, Data: data}
}

func TestRun_PassesTheProcessToMvmd(t *testing.T) {
	e := start(t, true, nil)
	mode := int32(0o600)
	m := microvm("p1", 1, node, api.RunAlways)
	m.Spec.Command = []string{"/bin/app", "--serve"}
	m.Spec.Args = []string{"-v"}
	m.Spec.User = "1000:100"
	m.Spec.Env = []api.EnvVar{{Name: "A", Value: "1"}, {Name: "B", Value: "two"}}
	m.Spec.Files = []api.MicroVMFile{
		{Path: "/etc/a", Content: "alpha"},
		{Path: "/etc/b", Content: "beta", Mode: &mode, Owner: &api.FileOwner{UID: 5, GID: 6}},
	}
	e.gaon.SetMicroVM(m)

	eventually(t, "p1-e1 runs", e.running("p1-e1"))
	req, ok := e.mvmd.CreateRequest("p1-e1")
	require.True(t, ok)
	assert.Equal(t, []string{"/bin/app", "--serve"}, req.GetCommand())
	assert.Equal(t, []string{"-v"}, req.GetArgs())
	assert.Equal(t, "1000:100", req.GetUser())
	require.Len(t, req.GetEnv(), 2)
	assert.Equal(t, "A", req.GetEnv()[0].GetName())
	assert.Equal(t, "1", req.GetEnv()[0].GetValue())
	assert.Equal(t, "B", req.GetEnv()[1].GetName())
	assert.Equal(t, "two", req.GetEnv()[1].GetValue())
	require.Len(t, req.GetFiles(), 2)
	a, b := req.GetFiles()[0], req.GetFiles()[1]
	assert.Equal(t, "/etc/a", a.GetPath())
	assert.Equal(t, []byte("alpha"), a.GetContent())
	assert.EqualValues(t, 0o644, a.GetMode(), "a file that gives no mode is 0644")
	assert.EqualValues(t, 1000, a.GetUid(), "a file that gives no owner is the MicroVM's user")
	assert.EqualValues(t, 100, a.GetGid())
	assert.EqualValues(t, 0o600, b.GetMode())
	assert.EqualValues(t, 5, b.GetUid())
	assert.EqualValues(t, 6, b.GetGid())
}

func TestRun_GivesAFileWithoutAnOwnerAndAUserRootsOwnership(t *testing.T) {
	e := start(t, true, nil)
	m := microvm("p2", 1, node, api.RunAlways)
	m.Spec.Files = []api.MicroVMFile{{Path: "/etc/a", Content: "x"}}
	e.gaon.SetMicroVM(m)

	eventually(t, "p2-e1 runs", e.running("p2-e1"))
	req, _ := e.mvmd.CreateRequest("p2-e1")
	assert.EqualValues(t, 0, req.GetFiles()[0].GetUid())
	assert.EqualValues(t, 0, req.GetFiles()[0].GetGid())
}

func TestRun_FailsAFileWhoseOwnerItCannotDecide(t *testing.T) {
	e := start(t, true, nil)
	m := microvm("named", 1, node, api.RunAlways)
	m.Spec.User = "app"
	m.Spec.Files = []api.MicroVMFile{{Path: "/etc/a", Content: "x"}}
	e.gaon.SetMicroVM(m)
	ok := microvm("named-ok", 1, node, api.RunAlways)
	ok.Spec.User = "app"
	ok.Spec.Files = []api.MicroVMFile{{Path: "/etc/a", Content: "x", Owner: &api.FileOwner{UID: 1, GID: 1}}}
	e.gaon.SetMicroVM(ok)

	c := e.failed(t, "named")
	assert.Equal(t, microvms.ReasonInvalidSpec, c.Reason)
	assert.Contains(t, c.Message, "/etc/a")
	assert.False(t, e.created("named-e1"))
	eventually(t, "control: the same file with an owner runs", e.running("named-ok-e1"))
}

func TestRun_ResolvesValueFromSecretAndConfigMapWithOneGetEach(t *testing.T) {
	val, cert := dummy(t), dummy(t)
	e := start(t, true, nil)
	e.gaon.SetSecret(secret("creds", map[string]string{"pw": val, "cert": cert}))
	e.gaon.SetConfigMap(configMap("cfg", map[string]string{"mode": "fast", "conf": "k=v"}))
	m := microvm("r1", 1, node, api.RunAlways)
	m.Spec.Env = []api.EnvVar{
		{Name: "PW", ValueFrom: secretRef("creds", "pw")},
		{Name: "MODE", ValueFrom: configRef("cfg", "mode")},
	}
	m.Spec.Files = []api.MicroVMFile{
		{Path: "/run/cert", ValueFrom: secretRef("creds", "cert")},
		{Path: "/etc/conf", ValueFrom: configRef("cfg", "conf")},
	}
	e.gaon.SetMicroVM(m)

	eventually(t, "r1-e1 runs", e.running("r1-e1"))
	req, _ := e.mvmd.CreateRequest("r1-e1")
	assert.Equal(t, val, req.GetEnv()[0].GetValue())
	assert.Equal(t, "fast", req.GetEnv()[1].GetValue())
	assert.Equal(t, []byte(cert), req.GetFiles()[0].GetContent())
	assert.Equal(t, []byte("k=v"), req.GetFiles()[1].GetContent())
	reads := e.gaon.Reads()
	require.GreaterOrEqual(t, len(reads), 2)
	assert.ElementsMatch(t, []string{"GET /secrets/creds", "GET /configmaps/cfg"}, reads[:2],
		"each object is read once to create the MicroVM, by get and never by list or watch")
	assert.Subset(t, []string{"GET /secrets/creds", "GET /configmaps/cfg"}, reads,
		"later rounds read the same objects by get, to replace a changed file")
}

func TestRun_FailsAMicroVMWhoseReferenceIsMissing(t *testing.T) {
	cases := map[string]struct {
		env  api.EnvVar
		file api.MicroVMFile
		want string
	}{
		"Secret": {env: api.EnvVar{Name: "X", ValueFrom: secretRef("absent", "k")}, want: `Secret "absent" not found`},
		"Secret key": {file: api.MicroVMFile{Path: "/f", ValueFrom: secretRef("have", "nokey")},
			want: `Secret "have" has no key "nokey"`},
		"ConfigMap":     {env: api.EnvVar{Name: "X", ValueFrom: configRef("absent", "k")}, want: `ConfigMap "absent" not found`},
		"ConfigMap key": {env: api.EnvVar{Name: "X", ValueFrom: configRef("cfg", "nokey")}, want: `ConfigMap "cfg" has no key "nokey"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			val := dummy(t)
			logs := &syncBuffer{}
			e := start(t, true, nil, withLog(logs))
			e.gaon.SetSecret(secret("have", map[string]string{"present": val}))
			e.gaon.SetConfigMap(configMap("cfg", map[string]string{"present": "v"}))
			m := microvm("miss", 1, node, api.RunAlways)
			if c.env.Name != "" {
				m.Spec.Env = []api.EnvVar{c.env}
			}
			if c.file.Path != "" {
				m.Spec.Files = []api.MicroVMFile{c.file}
			}
			e.gaon.SetMicroVM(m)

			cond := e.failed(t, "miss")
			assert.Equal(t, microvms.ReasonReferenceNotFound, cond.Reason)
			assert.Contains(t, cond.Message, c.want)
			assert.NotContains(t, cond.Message, val)
			assert.False(t, e.created("miss-e1"), "mvmd is not asked to create it")
			assert.NotContains(t, logs.String(), val)
			assert.Contains(t, logs.String(), "microvm cannot be created", "control: the failure is logged")

			// Control: with the object and key in place the MicroVM runs.
			e.gaon.SetSecret(secret("absent", map[string]string{"k": val}))
			e.gaon.SetSecret(secret("have", map[string]string{"present": val, "nokey": val}))
			e.gaon.SetConfigMap(configMap("absent", map[string]string{"k": "v"}))
			e.gaon.SetConfigMap(configMap("cfg", map[string]string{"present": "v", "nokey": "v"}))
			eventually(t, "miss-e1 runs once the reference exists", e.running("miss-e1"))
		})
	}
}

func TestRun_FailsAMicroVMWhoseResolvedFilesAreOverTheBound(t *testing.T) {
	half := []api.MicroVMFile{{Path: "/a", ValueFrom: configRef("cfg", "half")}, {Path: "/b", ValueFrom: configRef("cfg", "half")}}
	cases := map[string]struct {
		files []api.MicroVMFile
		fails bool
	}{
		"exactly 1 MiB runs":     {files: half},
		"1 MiB and a byte fails": {fails: true, files: append(append([]api.MicroVMFile(nil), half...), api.MicroVMFile{Path: "/c", Content: "x"})},
		"64 files run":           {files: numbered(api.MaxFiles)},
		"65 files fail":          {fails: true, files: numbered(api.MaxFiles + 1)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := start(t, true, nil)
			e.gaon.SetConfigMap(configMap("cfg", map[string]string{"half": strings.Repeat("x", api.MaxFilesBytes/2)}))
			m := microvm("big", 1, node, api.RunAlways)
			m.Spec.Files = c.files
			e.gaon.SetMicroVM(m)

			if !c.fails {
				eventually(t, "big-e1 runs", e.running("big-e1"))
				return
			}
			cond := e.failed(t, "big")
			assert.Equal(t, microvms.ReasonFilesOverBound, cond.Reason)
			assert.False(t, e.created("big-e1"), "the bound is checked before CreateMachine")
			assert.NotContains(t, e.mvmd.Calls(), "CreateMachine big-e1")
		})
	}
}

func numbered(n int) []api.MicroVMFile {
	files := make([]api.MicroVMFile, n)
	for i := range files {
		files[i] = api.MicroVMFile{Path: "/f/" + strings.Repeat("a", i+1), Content: "x"}
	}
	return files
}

func TestRun_LogsNoSecretValueForACreateOrAFailedResolution(t *testing.T) {
	val := dummy(t)
	logs := &syncBuffer{}
	e := start(t, true, nil, withLog(logs))
	e.gaon.SetSecret(secret("creds", map[string]string{"pw": val}))
	ok := microvm("ok", 1, node, api.RunAlways)
	ok.Spec.Env = []api.EnvVar{{Name: "PW", ValueFrom: secretRef("creds", "pw")}}
	ok.Spec.Files = []api.MicroVMFile{{Path: "/run/pw", ValueFrom: secretRef("creds", "pw")}}
	e.gaon.SetMicroVM(ok)
	bad := microvm("bad", 1, node, api.RunAlways)
	// The first variable resolves, so the request that fails holds a value.
	bad.Spec.Env = []api.EnvVar{{Name: "PW", ValueFrom: secretRef("creds", "pw")}, {Name: "PW2", ValueFrom: secretRef("creds", "nokey")}}
	e.gaon.SetMicroVM(bad)

	eventually(t, "ok-e1 runs", e.running("ok-e1"))
	cond := e.failed(t, "bad")
	req, _ := e.mvmd.CreateRequest("ok-e1")
	require.Equal(t, val, req.GetEnv()[0].GetValue(), "control: the value did reach mvmd")
	assert.Contains(t, logs.String(), "created microvm", "control: the create is logged")
	assert.Contains(t, logs.String(), "microvm cannot be created", "control: the failed resolution is logged")
	assert.NotContains(t, logs.String(), val)
	assert.NotContains(t, cond.Message, val)
}

func TestRun_ReportsMvmdsPreconditionFailureOnArgsAloneOfAnOldImage(t *testing.T) {
	e := start(t, true, nil)
	e.mvmd.SetLegacyImage("sha256:" + strings.Repeat("a", 64))
	old := microvm("old", 1, node, api.RunAlways)
	old.Spec.Args = []string{"-v"}
	e.gaon.SetMicroVM(old)
	both := microvm("both", 1, node, api.RunAlways)
	both.Spec.Command = []string{"/bin/app"}
	both.Spec.Args = []string{"-v"}
	e.gaon.SetMicroVM(both)

	cond := e.failed(t, "old")
	assert.Equal(t, microvms.ReasonPreconditionFailed, cond.Reason)
	assert.Contains(t, cond.Message, "pull it again", "mvmd's message is on the status")
	eventually(t, "control: command with args on the same image runs", e.running("both-e1"))
}

// askedNetwork records the MicroVMs it is asked a policy for.
type askedNetwork struct {
	emptyNetwork
	mu    sync.Mutex
	asked []string
}

func (n *askedNetwork) Policy(ctx context.Context, m *api.MicroVM) (*plugin.NetworkPolicy, error) {
	n.mu.Lock()
	n.asked = append(n.asked, m.Metadata.Name)
	n.mu.Unlock()
	return n.emptyNetwork.Policy(ctx, m)
}

func (n *askedNetwork) names() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.asked...)
}

func TestRun_FailsAMicroVMThatSetsAWorkingDirectoryBeforeThePolicyAndTheCreate(t *testing.T) {
	network := &askedNetwork{}
	e := start(t, true, nil, func(e *env) { e.network = network })
	dir := microvm("wd", 1, node, api.RunAlways)
	dir.Spec.WorkingDir = "/srv"
	e.gaon.SetMicroVM(dir)
	e.gaon.SetMicroVM(microvm("wd-ok", 1, node, api.RunAlways))

	cond := e.failed(t, "wd")
	assert.Equal(t, microvms.ReasonInvalidSpec, cond.Reason)
	assert.Contains(t, cond.Message, "spec.workingDir")
	assert.Contains(t, cond.Message, "mvmd#519")
	eventually(t, "control: the same MicroVM without workingDir runs", e.running("wd-ok-e1"))
	assert.Equal(t, []string{"wd-ok"}, network.names(), "the network plugin is asked for the control only")
	assert.NotContains(t, e.mvmd.Calls(), "CreateMachine wd-e1")
}
