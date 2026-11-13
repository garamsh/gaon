package microvms

// These tests are internal for the reason restart_test.go is: renewal is
// timed by the agent's clock, which moves only when a test advances it.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

const (
	tokenLifetime = time.Hour
	renewAfter    = 48 * time.Minute
)

// unique returns a random value that stands for a secret in a test.
func unique(t *testing.T) string {
	t.Helper()
	b := make([]byte, 12)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return "dummy-" + hex.EncodeToString(b)
}

func rigSecret(name string, data map[string]string) api.Secret {
	s := api.Secret{Metadata: api.ObjectMeta{Name: name}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

// logs replaces the service's logger with one that keeps its lines.
func (r *rig) logs() *bytes.Buffer {
	var b bytes.Buffer
	r.svc.log = slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &b
}

// beginWith stores the MicroVM as rigMicroVM gives it, changed by edit, and
// runs it until mvmd holds it Running.
func (r *rig) beginWith(edit func(*api.MicroVM)) {
	r.t.Helper()
	m := rigMicroVM(1, api.RunAlways)
	m.Metadata.UID = "uid-m1"
	edit(&m)
	r.put(m)
	r.mustRound()
	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
}

func withAccount(m *api.MicroVM) { m.Spec.ServiceAccountName = "sa" }

func (r *rig) token() string {
	r.t.Helper()
	b, ok := r.mvmd.File(rigID, TokenPath)
	require.True(r.t, ok, "the guest holds a token file")
	return string(b)
}

func (r *rig) writes() int {
	n := 0
	for _, c := range r.mvmd.Calls() {
		if c == "WriteFile "+rigID {
			n++
		}
	}
	return n
}

func (r *rig) ready() api.Condition {
	r.t.Helper()
	conds := r.gaon.MicroVM("m1").Status.Conditions
	require.Len(r.t, conds, 1)
	return conds[0]
}

func TestToken_IsAFileAtTheFixedPathOnceTheMicroVMRuns(t *testing.T) {
	r := newRig(t, nil)
	r.gaon.SetServiceAccount("sa")

	r.beginWith(withAccount)

	issued := r.gaon.IssuedTokens()
	require.Len(t, issued, 1)
	assert.Equal(t, issued[0], r.token())
	req, _ := r.mvmd.CreateRequest(rigID)
	var file *v1alpha2.File
	for _, f := range req.GetFiles() {
		if f.GetPath() == TokenPath {
			file = f
		}
	}
	require.NotNil(t, file, "the token is in the create request, so it exists at boot")
	assert.EqualValues(t, 0o600, file.GetMode())
	assert.Zero(t, file.GetUid())
	assert.Zero(t, file.GetGid())
	got := r.gaon.TokenRequests()
	require.Len(t, got, 1)
	assert.Equal(t, []string{rigAudience}, got[0].Audiences)
	assert.Equal(t, api.BoundObject{Name: "m1", UID: "uid-m1"}, got[0].BoundMicroVM)
}

func TestToken_IsAbsentWithoutAServiceAccount(t *testing.T) {
	r := newRig(t, nil)
	r.gaon.SetServiceAccount("sa")

	r.beginWith(func(*api.MicroVM) {})

	assert.Empty(t, r.gaon.TokenRequests())
	_, ok := r.mvmd.File(rigID, TokenPath)
	assert.False(t, ok)
}

func TestToken_IsReplacedAfterEightyPercentOfItsLifetime(t *testing.T) {
	r := newRig(t, nil)
	r.gaon.SetServiceAccount("sa")
	r.beginWith(withAccount)
	first := r.token()

	r.clk.Advance(renewAfter - time.Second)
	r.mustRound()
	assert.Equal(t, first, r.token(), "before 80% the token stays")
	assert.Zero(t, r.writes())

	r.clk.Advance(time.Second)
	r.mustRound()
	issued := r.gaon.IssuedTokens()
	require.Len(t, issued, 2)
	assert.Equal(t, issued[1], r.token(), "at 80% the file holds the new token")
	assert.Equal(t, 1, r.writes())

	r.clk.Advance(renewAfter - time.Second)
	r.mustRound()
	assert.Len(t, r.gaon.IssuedTokens(), 2, "the next renewal is 80% after the last one")
	r.clk.Advance(time.Second)
	r.mustRound()
	assert.Len(t, r.gaon.IssuedTokens(), 3)
}

func TestToken_KeepsTheOldWholeFileWhenAWriteBreaksAndIsRetried(t *testing.T) {
	r := newRig(t, nil)
	r.gaon.SetServiceAccount("sa")
	r.beginWith(withAccount)
	first := r.token()
	r.clk.Advance(renewAfter)

	r.mvmd.FailWriteFiles(true)
	r.mustRound()
	assert.Equal(t, first, r.token(), "a reader of the file sees the old token whole")
	r.mvmd.FailWriteFiles(false)
	r.mustRound()
	issued := r.gaon.IssuedTokens()
	assert.Equal(t, issued[len(issued)-1], r.token(), "the retry replaces it")
	assert.NotEqual(t, first, r.token())
}

func TestToken_ARefusedRenewalIsRetriedAndReportedOnceTheTokenExpired(t *testing.T) {
	for name, fail := range map[string]struct{ on, off func(*rig) }{
		"the API server refuses": {func(r *rig) { r.gaon.FailTokens(true) }, func(r *rig) { r.gaon.FailTokens(false) }},
		"mvmd refuses the write": {func(r *rig) { r.mvmd.FailWriteFiles(true) }, func(r *rig) { r.mvmd.FailWriteFiles(false) }},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			r.gaon.SetServiceAccount("sa")
			r.beginWith(withAccount)
			first := r.token()
			fail.on(r)

			r.clk.Advance(renewAfter)
			r.mustRound()
			r.mustRound()
			assert.Equal(t, api.ConditionTrue, r.ready().Status, "the token is still valid")
			attempts := len(r.gaon.TokenRequests())
			assert.Greater(t, attempts, 2, "the renewal is retried on each round")

			r.clk.Advance(tokenLifetime - renewAfter - time.Second)
			r.mustRound()
			assert.Equal(t, api.ConditionTrue, r.ready().Status, "a second short of expiry")
			r.clk.Advance(time.Second)
			r.mustRound()
			ready := r.ready()
			assert.Equal(t, api.ConditionFalse, ready.Status)
			assert.Equal(t, ReasonTokenExpired, ready.Reason)
			assert.Equal(t, api.MicroVMRunning, r.gaon.MicroVM("m1").Status.Phase)
			assert.Equal(t, first, r.token(), "the expired token is still the file's")

			fail.off(r)
			r.mustRound()
			assert.Equal(t, api.ConditionTrue, r.ready().Status, "a renewal that is accepted clears it")
			assert.NotEqual(t, first, r.token())
		})
	}
}

func TestToken_IsNotInALogAStatusOrAnError(t *testing.T) {
	r := newRig(t, nil)
	logs := r.logs()
	r.gaon.SetServiceAccount("sa")
	r.beginWith(withAccount)
	var errs []string
	r.mvmd.FailWriteFiles(true)
	r.clk.Advance(renewAfter)
	for i := 0; i < 2; i++ {
		if err := r.round(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	r.clk.Advance(tokenLifetime)
	if err := r.round(); err != nil {
		errs = append(errs, err.Error())
	}
	r.mvmd.FailWriteFiles(false)
	r.gaon.FailTokens(true)
	if err := r.round(); err != nil {
		errs = append(errs, err.Error())
	}

	assert.Contains(t, logs.String(), "created microvm", "control: the creation is logged")
	assert.Contains(t, logs.String(), "cannot renew the token", "control: the refused renewal is logged")
	assert.Equal(t, ReasonTokenExpired, r.ready().Reason, "control: the expiry is in the status")
	status, err := json.Marshal(r.gaon.MicroVM("m1").Status)
	require.NoError(t, err)
	issued := r.gaon.IssuedTokens()
	require.NotEmpty(t, issued)
	for _, token := range issued {
		assert.NotContains(t, logs.String(), token)
		assert.NotContains(t, string(status), token)
		assert.NotContains(t, strings.Join(errs, "\n"), token)
	}
}

func TestToken_FailsAMicroVMThatTheSpecOrTheAccountRefuses(t *testing.T) {
	tests := map[string]struct {
		edit   func(*api.MicroVM)
		reason string
		want   string
	}{
		"a user file at the token path": {
			edit:   func(m *api.MicroVM) { m.Spec.Files = []api.MicroVMFile{{Path: TokenPath, Content: "x"}} },
			reason: ReasonInvalidSpec, want: TokenPath,
		},
		"a user that is a name": {
			edit:   func(m *api.MicroVM) { m.Spec.User = "app" },
			reason: ReasonInvalidSpec, want: "numeric uid:gid",
		},
		"a ServiceAccount that does not exist": {
			edit:   func(m *api.MicroVM) { m.Spec.ServiceAccountName = "absent" },
			reason: ReasonReferenceNotFound, want: `ServiceAccount "absent" not found`,
		},
		"as many files as the bound": {
			edit: func(m *api.MicroVM) {
				for i := 0; i < api.MaxFiles; i++ {
					m.Spec.Files = append(m.Spec.Files, api.MicroVMFile{Path: "/f/" + string(rune('a'+i/26)) + string(rune('a'+i%26)), Content: "x"})
				}
			},
			reason: ReasonFilesOverBound, want: "more than",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			r.gaon.SetServiceAccount("sa")
			m := rigMicroVM(1, api.RunAlways)
			m.Metadata.UID = "uid-m1"
			m.Spec.ServiceAccountName = "sa"
			tt.edit(&m)
			r.put(m)
			r.mustRound()

			ready := r.ready()
			assert.Equal(t, api.MicroVMFailed, r.gaon.MicroVM("m1").Status.Phase)
			assert.Equal(t, tt.reason, ready.Reason)
			assert.Contains(t, ready.Message, tt.want)
			_, held := r.mvmd.Machine(rigID)
			assert.False(t, held, "mvmd creates nothing")
		})
	}
}

func TestToken_IsOwnedByTheNumericUserOfTheMicroVM(t *testing.T) {
	r := newRig(t, nil)
	r.gaon.SetServiceAccount("sa")

	r.beginWith(func(m *api.MicroVM) { withAccount(m); m.Spec.User = "1000:2000" })

	req, _ := r.mvmd.CreateRequest(rigID)
	for _, f := range req.GetFiles() {
		if f.GetPath() == TokenPath {
			assert.EqualValues(t, 1000, f.GetUid())
			assert.EqualValues(t, 2000, f.GetGid())
			return
		}
	}
	t.Fatal("no token file in the create request")
}

func TestFiles_ASecretsChangeReachesTheFileOfARunningMicroVM(t *testing.T) {
	r := newRig(t, nil)
	v1, v2 := unique(t), unique(t)
	r.gaon.SetSecret(rigSecret("creds", map[string]string{"pw": v1}))
	r.beginWith(func(m *api.MicroVM) {
		m.Spec.Env = []api.EnvVar{{Name: "PW", ValueFrom: &api.ValueSource{SecretKeyRef: &api.KeyReference{Name: "creds", Key: "pw"}}}}
		m.Spec.Files = []api.MicroVMFile{
			{Path: "/run/pw", ValueFrom: &api.ValueSource{SecretKeyRef: &api.KeyReference{Name: "creds", Key: "pw"}}},
			{Path: "/etc/static", Content: "fixed"},
		}
	})
	got, _ := r.mvmd.File(rigID, "/run/pw")
	require.Equal(t, v1, string(got), "control: the file holds the value at start")
	r.mustRound()
	assert.Zero(t, r.writes(), "an unchanged value is not written again")

	r.gaon.SetSecret(rigSecret("creds", map[string]string{"pw": v2}))
	r.mustRound()

	got, _ = r.mvmd.File(rigID, "/run/pw")
	assert.Equal(t, v2, string(got), "the file holds the new value")
	assert.Equal(t, 1, r.writes())
	req, _ := r.mvmd.CreateRequest(rigID)
	assert.Equal(t, v1, req.GetEnv()[0].GetValue(), "env is fixed at start")
	r.mustRound()
	assert.Equal(t, 1, r.writes(), "the new value is written once")
}

func TestFiles_AConfigMapsLargeChangeIsWrittenWhole(t *testing.T) {
	r := newRig(t, nil)
	r.gaon.SetConfigMap(api.ConfigMap{Metadata: api.ObjectMeta{Name: "cfg"}, Data: map[string]string{"conf": "small"}})
	r.beginWith(func(m *api.MicroVM) {
		m.Spec.Files = []api.MicroVMFile{{Path: "/etc/conf", ValueFrom: &api.ValueSource{ConfigMapKeyRef: &api.KeyReference{Name: "cfg", Key: "conf"}}}}
	})
	large := strings.Repeat("0123456789", 20000)

	r.gaon.SetConfigMap(api.ConfigMap{Metadata: api.ObjectMeta{Name: "cfg"}, Data: map[string]string{"conf": large}})
	r.mustRound()

	got, _ := r.mvmd.File(rigID, "/etc/conf")
	assert.Len(t, got, len(large))
	assert.True(t, bytes.Equal([]byte(large), got), "the content is the whole value")
}

func TestFiles_AnUnreadableValueKeepsTheOldContentAndAFailedWriteIsRetried(t *testing.T) {
	r := newRig(t, nil)
	logs := r.logs()
	v1, v2 := unique(t), unique(t)
	r.gaon.SetSecret(rigSecret("creds", map[string]string{"pw": v1}))
	r.beginWith(func(m *api.MicroVM) {
		m.Spec.Files = []api.MicroVMFile{{Path: "/run/pw", ValueFrom: &api.ValueSource{SecretKeyRef: &api.KeyReference{Name: "creds", Key: "pw"}}}}
	})

	r.gaon.SetSecret(rigSecret("creds", map[string]string{"other": v2}))
	r.mustRound()
	got, _ := r.mvmd.File(rigID, "/run/pw")
	assert.Equal(t, v1, string(got), "a key that is gone leaves the file as it was")
	assert.Contains(t, logs.String(), `Secret \"creds\" has no key \"pw\"`, "control: the failure is logged")

	r.gaon.SetSecret(rigSecret("creds", map[string]string{"pw": v2}))
	r.mvmd.FailWriteFiles(true)
	r.mustRound()
	got, _ = r.mvmd.File(rigID, "/run/pw")
	assert.Equal(t, v1, string(got), "a write that breaks leaves the old content")
	assert.Contains(t, logs.String(), "cannot replace a file", "control: the failed write is logged")
	r.mvmd.FailWriteFiles(false)
	r.mustRound()
	got, _ = r.mvmd.File(rigID, "/run/pw")
	assert.Equal(t, v2, string(got), "the next round writes it")
	assert.NotContains(t, logs.String(), v1)
	assert.NotContains(t, logs.String(), v2)
}

func TestRestart_BootsWithTheEnvTheFilesAndTheTokenResolvedAgain(t *testing.T) {
	r := newRig(t, nil)
	logs := r.logs()
	v1, v2 := unique(t), unique(t)
	r.gaon.SetServiceAccount("sa")
	r.gaon.SetSecret(rigSecret("creds", map[string]string{"pw": v1}))
	r.beginWith(func(m *api.MicroVM) {
		withAccount(m)
		m.Spec.Env = []api.EnvVar{{Name: "PW", ValueFrom: &api.ValueSource{SecretKeyRef: &api.KeyReference{Name: "creds", Key: "pw"}}}}
		m.Spec.Files = []api.MicroVMFile{{Path: "/run/pw", ValueFrom: &api.ValueSource{SecretKeyRef: &api.KeyReference{Name: "creds", Key: "pw"}}}}
	})
	req, _ := r.mvmd.CreateRequest(rigID)
	require.Equal(t, v1, req.GetEnv()[0].GetValue(), "control: mvmd holds the create-time env")
	first := r.token()
	r.gaon.SetSecret(rigSecret("creds", map[string]string{"pw": v2}))

	r.exit(exited(1, 0))
	r.mustRound()
	r.clk.Advance(10 * time.Second)
	r.mustRound()

	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
	req, _ = r.mvmd.CreateRequest(rigID)
	require.Len(t, req.GetEnv(), 1)
	assert.Equal(t, v2, req.GetEnv()[0].GetValue(), "the restart carried the new env")
	got, _ := r.mvmd.File(rigID, "/run/pw")
	assert.Equal(t, v2, string(got), "the guest booted with the new file")
	issued := r.gaon.IssuedTokens()
	require.Len(t, issued, 2, "a token was asked for again at the restart")
	assert.Equal(t, issued[1], r.token(), "the guest booted with the new token")
	assert.NotEqual(t, first, r.token())
	assert.Zero(t, r.writes(), "the boot carried them: nothing is written into the guest afterwards")
	r.mustRound()
	assert.Zero(t, r.writes(), "and a round after the boot has nothing to replace")
	assert.Equal(t, 1, len(req.GetFiles())-1, "the token file is in the record beside the file of the spec")
	assert.NotContains(t, logs.String(), v1)
	assert.NotContains(t, logs.String(), v2)
	assert.NotContains(t, logs.String(), issued[1])
}

func TestRestart_FailsTheMicroVMWhoseReferenceNoLongerResolvesAndStartsItOnceItDoes(t *testing.T) {
	r := newRig(t, nil)
	logs := r.logs()
	v1, v2 := unique(t), unique(t)
	r.gaon.SetSecret(rigSecret("creds", map[string]string{"pw": v1}))
	r.beginWith(func(m *api.MicroVM) {
		m.Spec.Env = []api.EnvVar{{Name: "PW", ValueFrom: &api.ValueSource{SecretKeyRef: &api.KeyReference{Name: "creds", Key: "pw"}}}}
	})
	r.gaon.SetSecret(rigSecret("creds", map[string]string{"other": v2}))

	r.exit(exited(1, 0))
	r.mustRound()
	r.clk.Advance(10 * time.Second)
	r.mustRound()

	r.requireState(v1alpha2.MachineState_MACHINE_STATE_STOPPED)
	assert.Equal(t, 1, r.starts(), "mvmd was not asked to start it")
	assert.Equal(t, api.MicroVMFailed, r.gaon.MicroVM("m1").Status.Phase)
	ready := r.ready()
	assert.Equal(t, ReasonReferenceNotFound, ready.Reason)
	assert.Contains(t, ready.Message, `Secret "creds" has no key "pw"`)

	r.gaon.SetSecret(rigSecret("creds", map[string]string{"pw": v2}))
	r.mustRound()

	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
	req, _ := r.mvmd.CreateRequest(rigID)
	assert.Equal(t, v2, req.GetEnv()[0].GetValue(), "control: the same restart goes through once the key is back")
	assert.Equal(t, api.MicroVMRunning, r.gaon.MicroVM("m1").Status.Phase)
	assert.NotContains(t, logs.String(), v1)
	assert.NotContains(t, logs.String(), v2)
	assert.NotContains(t, ready.Message, v2)
}

func TestRestart_ResolvesAgainWhenAHaltedMicroVMIsStarted(t *testing.T) {
	for _, strategy := range []api.RunStrategy{api.RunAlways, api.RunOnce} {
		t.Run(string(strategy), func(t *testing.T) {
			r := newRig(t, nil)
			v1, v2 := unique(t), unique(t)
			r.gaon.SetSecret(rigSecret("creds", map[string]string{"pw": v1}))
			r.beginWith(func(m *api.MicroVM) {
				m.Spec.Env = []api.EnvVar{{Name: "PW", ValueFrom: &api.ValueSource{SecretKeyRef: &api.KeyReference{Name: "creds", Key: "pw"}}}}
			})
			m := r.gaon.MicroVM("m1")
			m.Spec.RunStrategy = api.RunHalted
			r.put(m)
			r.mustRound()
			r.requireState(v1alpha2.MachineState_MACHINE_STATE_STOPPED)
			r.gaon.SetSecret(rigSecret("creds", map[string]string{"pw": v2}))

			m = r.gaon.MicroVM("m1")
			m.Spec.RunStrategy = strategy
			r.put(m)
			r.mustRound()

			r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
			req, _ := r.mvmd.CreateRequest(rigID)
			assert.Equal(t, v2, req.GetEnv()[0].GetValue())
		})
	}
}

func TestToken_WaitsForAnAudienceTheAgentIsGiven(t *testing.T) {
	r := newRig(t, nil)
	r.gaon.SetServiceAccount("sa")
	r.svc.params.TokenAudience = ""
	m := rigMicroVM(1, api.RunAlways)
	m.Metadata.UID = "uid-m1"
	withAccount(&m)
	r.put(m)

	_ = r.round() // the failure is retried, so whether it is returned is not the point
	assert.Empty(t, r.gaon.TokenRequests(), "no token is asked for without an audience")
	_, held := r.mvmd.Machine(rigID)
	assert.False(t, held, "mvmd creates nothing")

	r.svc.params.TokenAudience = rigAudience
	r.mustRound()
	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
}
