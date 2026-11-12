package scheduler_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/scheduler/internal/scheduler"
)

const configHeader = "apiVersion: scheduler.gaon.garam.sh/v1alpha1\nkind: SchedulerConfiguration\n"

// writeConfig writes a configuration file holding body and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scheduler.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func loadConfig(t *testing.T, body string) scheduler.Config {
	t.Helper()
	cfg, err := scheduler.LoadConfig(writeConfig(t, body))
	require.NoError(t, err)
	return cfg
}

func reconcileWith(t *testing.T, client scheduler.API, cfg scheduler.Config) error {
	t.Helper()
	return scheduler.NewService(client, slog.New(slog.NewTextHandler(io.Discard, nil)), scheduler.WithConfig(cfg)).Reconcile(context.Background())
}

const leastAllocatedYAML = "scorePlugins:\n  - name: LeastAllocated\n"

// stubCall is a request the stub extender received.
type stubCall struct {
	Verb      string
	Machine   api.Machine
	NodeNames []string
}

// stubExtender is an extender over httptest whose verbs answer as given.
type stubExtender struct {
	URL string // the urlPrefix

	mu    sync.Mutex
	calls []stubCall
}

type stubVerbs map[string]func(w http.ResponseWriter, c stubCall)

func newStubExtender(t *testing.T, verbs stubVerbs) *stubExtender {
	t.Helper()
	s := &stubExtender{}
	ts := httptest.NewServer(s.handler(t, verbs))
	t.Cleanup(ts.Close)
	s.URL = ts.URL + "/ext"
	return s
}

func (s *stubExtender) handler(t *testing.T, verbs stubVerbs) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verb := strings.TrimPrefix(r.URL.Path, "/ext/")
		h, ok := verbs[verb]
		if !ok || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Machine   api.Machine `json:"machine"`
			NodeNames []string    `json:"nodeNames"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("extender request does not decode: %v", err)
		}
		c := stubCall{Verb: verb, Machine: body.Machine, NodeNames: body.NodeNames}
		s.mu.Lock()
		s.calls = append(s.calls, c)
		s.mu.Unlock()
		h(w, c)
	})
}

func (s *stubExtender) callsTo(verb string) []stubCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []stubCall
	for _, c := range s.calls {
		if c.Verb == verb {
			out = append(out, c)
		}
	}
	return out
}

func passAll(w http.ResponseWriter, c stubCall) {
	answer(w, map[string]any{"nodeNames": c.NodeNames})
}

func answer(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// scoresOf answers a prioritize request with the scores given.
func scoresOf(scores map[string]int) func(w http.ResponseWriter, c stubCall) {
	return func(w http.ResponseWriter, c stubCall) {
		var out []map[string]any
		for _, n := range c.NodeNames {
			if s, ok := scores[n]; ok {
				out = append(out, map[string]any{"host": n, "score": s})
			}
		}
		answer(w, out)
	}
}

func extenderYAML(prefix string, lines ...string) string {
	return "extenders:\n  - urlPrefix: " + prefix + "\n" + indent(lines)
}

func indent(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("    " + l + "\n")
	}
	return b.String()
}

// twoNodes adds a (4 cpu, 8Gi) and b (10 cpu, 10Gi), both empty: a Machine
// requesting 1 cpu and 1Gi scores 81 on a and 90 on b under LeastAllocated,
// and a sorts first.
func twoNodes(f *fakeAPI) {
	f.addNode(node("a", "4", "8Gi"))
	f.addNode(node("b", "10", "10Gi"))
	f.addMachine(newMachine("m1", "1", "1Gi"))
}

func TestLeastAllocated_ChoosesTheNodeWithTheMostFreeShare(t *testing.T) {
	t.Run("enabled, the emptier Node wins over the first by name", func(t *testing.T) {
		f, client := newFakeAPI(t)
		twoNodes(f)
		require.NoError(t, reconcileWith(t, client, loadConfig(t, configHeader+leastAllocatedYAML)))
		assert.Equal(t, "b", f.machine("m1").Spec.NodeName)
	})

	t.Run("control: with no configuration the first fitting Node by name wins", func(t *testing.T) {
		f, client := newFakeAPI(t)
		twoNodes(f)
		reconcile(t, client)
		assert.Equal(t, "a", f.machine("m1").Spec.NodeName)
	})

	t.Run("a binding made earlier in the pass lowers the score of its Node", func(t *testing.T) {
		f, client := newFakeAPI(t)
		f.addNode(node("a", "4", "4Gi"))
		f.addNode(node("b", "4", "4Gi"))
		f.addMachine(newMachine("m1", "2", "2Gi"))
		f.addMachine(newMachine("m2", "1", "1Gi"))
		require.NoError(t, reconcileWith(t, client, loadConfig(t, configHeader+leastAllocatedYAML)))
		assert.Equal(t, "a", f.machine("m1").Spec.NodeName, "equal scores go to the first by name")
		assert.Equal(t, "b", f.machine("m2").Spec.NodeName, "a is now half allocated")
	})

	t.Run("the Machines already bound count as allocated", func(t *testing.T) {
		f, client := newFakeAPI(t)
		f.addNode(node("a", "4", "4Gi"))
		f.addNode(node("b", "4", "4Gi"))
		f.addMachine(bound(newMachine("old", "3", "3Gi"), "a", 1))
		f.addMachine(newMachine("m1", "1", "1Gi"))
		require.NoError(t, reconcileWith(t, client, loadConfig(t, configHeader+leastAllocatedYAML)))
		assert.Equal(t, "b", f.machine("m1").Spec.NodeName)
	})

	t.Run("a Node the Machine does not fit is not scored", func(t *testing.T) {
		f, client := newFakeAPI(t)
		f.addNode(node("a", "100", "100Gi"))
		f.addNode(node("b", "1", "1Gi"))
		f.addMachine(newMachine("m1", "2", "1Gi"))
		require.NoError(t, reconcileWith(t, client, loadConfig(t, configHeader+leastAllocatedYAML)))
		assert.Equal(t, "a", f.machine("m1").Spec.NodeName)
	})
}

func TestScore_IsTheSumOfEachPluginScoreTimesItsWeight(t *testing.T) {
	// LeastAllocated puts b 9 ahead of a at weight 1 and 27 ahead at weight 3.
	// The extender gives a 2, which is 20 on the plugins' scale.
	tests := []struct {
		name   string
		weight string
		want   string
	}{
		{"weight 1: 20 outweighs 9", "weight: 1", "a"},
		{"weight 3: 27 outweighs 20", "weight: 3", "b"},
		{"the weight defaults to 1", "", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, client := newFakeAPI(t)
			twoNodes(f)
			ext := newStubExtender(t, stubVerbs{"prioritize": scoresOf(map[string]int{"a": 2})})
			plugin := leastAllocatedYAML
			if tt.weight != "" {
				plugin += "    " + tt.weight + "\n"
			}
			cfg := loadConfig(t, configHeader+plugin+extenderYAML(ext.URL, "prioritizeVerb: prioritize"))

			require.NoError(t, reconcileWith(t, client, cfg))

			assert.Equal(t, tt.want, f.machine("m1").Spec.NodeName)
		})
	}
}

func TestExtender_FilterRemovesANodeAndReceivesWhatPassedTheBuiltIns(t *testing.T) {
	setup := func(t *testing.T, verbs stubVerbs) (*fakeAPI, *stubExtender, scheduler.Config, *api.Client) {
		f, client := newFakeAPI(t)
		f.addNode(node("a", "4", "8Gi"))
		f.addNode(node("b", "4", "8Gi"))
		f.addNode(withCondition(node("c", "4", "8Gi"), api.NodeReady, api.ConditionFalse))
		f.addNode(node("d", "1", "1Gi"))
		f.addMachine(newMachine("m1", "2", "1Gi"))
		ext := newStubExtender(t, verbs)
		return f, ext, loadConfig(t, configHeader+extenderYAML(ext.URL, "filterVerb: filter")), client
	}

	t.Run("a Node the extender fails is not chosen", func(t *testing.T) {
		f, ext, cfg, client := setup(t, stubVerbs{"filter": func(w http.ResponseWriter, c stubCall) {
			answer(w, map[string]any{"nodeNames": []string{"b"}, "failedNodes": map[string]string{"a": "no GPU"}})
		}})
		require.NoError(t, reconcileWith(t, client, cfg))
		assert.Equal(t, "b", f.machine("m1").Spec.NodeName)

		calls := ext.callsTo("filter")
		require.Len(t, calls, 1)
		assert.Equal(t, "m1", calls[0].Machine.Metadata.Name)
		assert.Equal(t, []string{"a", "b"}, calls[0].NodeNames, "c is not Ready and d is too small, so neither is sent")
	})

	t.Run("control: an extender that passes every Node leaves the first by name", func(t *testing.T) {
		f, _, cfg, client := setup(t, stubVerbs{"filter": passAll})
		require.NoError(t, reconcileWith(t, client, cfg))
		assert.Equal(t, "a", f.machine("m1").Spec.NodeName)
	})

	t.Run("a Node the extender leaves out of both lists is refused", func(t *testing.T) {
		f, _, cfg, client := setup(t, stubVerbs{"filter": func(w http.ResponseWriter, c stubCall) {
			answer(w, map[string]any{"nodeNames": []string{"b"}})
		}})
		require.NoError(t, reconcileWith(t, client, cfg))
		assert.Equal(t, "b", f.machine("m1").Spec.NodeName)
	})
}

func TestExtender_PrioritizeChangesTheChoiceAndFilterThenPrioritizeSeeTheSameShortList(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(node("a", "4", "8Gi"))
	f.addNode(node("b", "4", "8Gi"))
	f.addNode(node("c", "4", "8Gi"))
	f.addMachine(newMachine("m1", "1", "1Gi"))
	ext := newStubExtender(t, stubVerbs{
		"filter": func(w http.ResponseWriter, c stubCall) {
			answer(w, map[string]any{"nodeNames": []string{"a", "b"}, "failedNodes": map[string]string{"c": "full"}})
		},
		"prioritize": scoresOf(map[string]int{"a": 1, "b": 7, "c": 10}),
	})
	cfg := loadConfig(t, configHeader+extenderYAML(ext.URL, "filterVerb: filter", "prioritizeVerb: prioritize"))

	require.NoError(t, reconcileWith(t, client, cfg))

	assert.Equal(t, "b", f.machine("m1").Spec.NodeName, "b scores highest among those that passed; c scored 10 but was filtered")
	prio := ext.callsTo("prioritize")
	require.Len(t, prio, 1)
	assert.Equal(t, []string{"a", "b"}, prio[0].NodeNames)

	t.Run("control: with no prioritize verb the first by name wins", func(t *testing.T) {
		f, client := newFakeAPI(t)
		f.addNode(node("a", "4", "8Gi"))
		f.addNode(node("b", "4", "8Gi"))
		f.addMachine(newMachine("m1", "1", "1Gi"))
		cfg := loadConfig(t, configHeader+extenderYAML(ext.URL, "filterVerb: filter"))
		require.NoError(t, reconcileWith(t, client, cfg))
		assert.Equal(t, "a", f.machine("m1").Spec.NodeName)
	})
}

func TestExtender_ScoreIsMultipliedByItsWeightAndTen(t *testing.T) {
	// LeastAllocated at weight 3 puts b 27 ahead. The extender gives a 1:
	// 10 at weight 1, 30 at weight 3, and 1 or 3 if it were not scaled by ten.
	tests := []struct {
		name   string
		weight string
		want   string
	}{
		{"weight 1: 10 does not outweigh 27", "weight: 1", "b"},
		{"weight 2: 20 does not outweigh 27", "weight: 2", "b"},
		{"weight 3: 30 outweighs 27", "weight: 3", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, client := newFakeAPI(t)
			twoNodes(f)
			ext := newStubExtender(t, stubVerbs{"prioritize": scoresOf(map[string]int{"a": 1})})
			cfg := loadConfig(t, configHeader+leastAllocatedYAML+"    weight: 3\n"+extenderYAML(ext.URL, "prioritizeVerb: prioritize", tt.weight))
			require.NoError(t, reconcileWith(t, client, cfg))
			assert.Equal(t, tt.want, f.machine("m1").Spec.NodeName)
		})
	}

	t.Run("control: at weight 1 and a score of 3 the extender outweighs a 27 lead", func(t *testing.T) {
		f, client := newFakeAPI(t)
		twoNodes(f)
		ext := newStubExtender(t, stubVerbs{"prioritize": scoresOf(map[string]int{"a": 3})})
		cfg := loadConfig(t, configHeader+leastAllocatedYAML+"    weight: 3\n"+extenderYAML(ext.URL, "prioritizeVerb: prioritize"))
		require.NoError(t, reconcileWith(t, client, cfg))
		assert.Equal(t, "a", f.machine("m1").Spec.NodeName)
	})
}

// failures are the ways a call to an extender fails.
func failures(t *testing.T) []struct {
	name    string
	verb    string
	handler func(w http.ResponseWriter, c stubCall)
	message string
} {
	return []struct {
		name    string
		verb    string
		handler func(w http.ResponseWriter, c stubCall)
		message string
	}{
		{"filter status 500", "filter", func(w http.ResponseWriter, _ stubCall) { w.WriteHeader(http.StatusInternalServerError) }, "status 500"},
		{"filter body that does not decode", "filter", func(w http.ResponseWriter, _ stubCall) { _, _ = w.Write([]byte("not json")) }, "decode answer"},
		{"filter error field", "filter", func(w http.ResponseWriter, _ stubCall) { answer(w, map[string]any{"error": "backend down"}) }, "backend down"},
		{"filter passes a Node that was not sent", "filter", func(w http.ResponseWriter, _ stubCall) { answer(w, map[string]any{"nodeNames": []string{"zz"}}) }, "not sent"},
		{"filter timeout", "filter", func(w http.ResponseWriter, _ stubCall) { time.Sleep(500 * time.Millisecond) }, "Client.Timeout"},
		{"prioritize status 503", "prioritize", func(w http.ResponseWriter, _ stubCall) { w.WriteHeader(http.StatusServiceUnavailable) }, "status 503"},
		{"prioritize score above 10", "prioritize", scoresOf(map[string]int{"a": 11}), "outside 0 to 10"},
		{"prioritize body that does not decode", "prioritize", func(w http.ResponseWriter, _ stubCall) { _, _ = w.Write([]byte("{")) }, "decode answer"},
	}
}

func TestExtender_FailureLeavesAnUnboundMachineUnboundWithExtenderErrorUnlessIgnorable(t *testing.T) {
	for _, tt := range failures(t) {
		t.Run(tt.name, func(t *testing.T) {
			verbs := stubVerbs{"filter": passAll, "prioritize": scoresOf(nil)}
			verbs[tt.verb] = tt.handler
			ext := newStubExtender(t, verbs)
			lines := []string{"filterVerb: filter", "prioritizeVerb: prioritize", "timeout: 200ms"}

			t.Run("not ignorable", func(t *testing.T) {
				f, client := newFakeAPI(t)
				twoNodes(f)
				cfg := loadConfig(t, configHeader+extenderYAML(ext.URL, lines...))

				require.NoError(t, reconcileWith(t, client, cfg), "the failed Machine is reported on its status, not as a failed pass")

				m := f.machine("m1")
				assert.Empty(t, m.Spec.NodeName)
				assert.Zero(t, f.bindingCount())
				require.Len(t, m.Status.Conditions, 1)
				c := m.Status.Conditions[0]
				assert.Equal(t, scheduler.ConditionScheduled, c.Type)
				assert.Equal(t, api.ConditionFalse, c.Status)
				assert.Equal(t, scheduler.ReasonExtenderError, c.Reason)
				assert.Contains(t, c.Message, tt.message)
				assert.Contains(t, c.Message, ext.URL)
			})

			t.Run("ignorable is skipped and the Machine is bound", func(t *testing.T) {
				f, client := newFakeAPI(t)
				twoNodes(f)
				cfg := loadConfig(t, configHeader+extenderYAML(ext.URL, append(lines, "ignorable: true")...))

				require.NoError(t, reconcileWith(t, client, cfg))

				assert.Equal(t, "a", f.machine("m1").Spec.NodeName)
				assert.Zero(t, f.statusPutCount())
			})
		})
	}
}

func TestExtender_FailureLeavesABoundMachineBoundAndItsStatusAlone(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(withCondition(node("dead", "4", "8Gi"), api.NodeLeaseExpired, api.ConditionTrue))
	f.addNode(node("b", "4", "8Gi"))
	f.addMachine(bound(newMachine("m1", "1", "1Gi"), "dead", 1))
	var failing atomic.Bool
	failing.Store(true)
	ext := newStubExtender(t, stubVerbs{"filter": func(w http.ResponseWriter, c stubCall) {
		if failing.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		passAll(w, c)
	}})
	cfg := loadConfig(t, configHeader+extenderYAML(ext.URL, "filterVerb: filter"))

	require.NoError(t, reconcileWith(t, client, cfg))
	m := f.machine("m1")
	assert.Equal(t, "dead", m.Spec.NodeName)
	assert.Zero(t, f.bindingCount())
	assert.Zero(t, f.statusPutCount())

	failing.Store(false)
	require.NoError(t, reconcileWith(t, client, cfg))
	assert.Equal(t, "b", f.machine("m1").Spec.NodeName, "control: the same Machine is rebound once the extender answers")
}

func TestExtender_IsNotCalledWhenNoNodePassedTheBuiltInFilters(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(node("a", "1", "1Gi"))
	f.addMachine(newMachine("m1", "2", "1Gi"))
	ext := newStubExtender(t, stubVerbs{"filter": passAll})
	cfg := loadConfig(t, configHeader+extenderYAML(ext.URL, "filterVerb: filter"))

	require.NoError(t, reconcileWith(t, client, cfg))

	assert.Empty(t, ext.callsTo("filter"))
	assert.Equal(t, scheduler.ReasonUnschedulable, f.machine("m1").Status.Conditions[0].Reason)
}

func TestUnschedulableMessage_CountsTheNodesEachFilterAndExtenderRefusedByReason(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(withCondition(node("down", "4", "8Gi"), api.NodeReady, api.ConditionFalse))
	f.addNode(node("small1", "1", "1Gi"))
	f.addNode(node("small2", "1", "1Gi"))
	f.addNode(node("x", "4", "8Gi"))
	f.addNode(node("y", "4", "8Gi"))
	f.addNode(node("z", "4", "8Gi"))
	f.addMachine(newMachine("m1", "2", "1Gi"))
	ext := newStubExtender(t, stubVerbs{"filter": func(w http.ResponseWriter, c stubCall) {
		answer(w, map[string]any{"failedNodes": map[string]string{"x": "no GPU", "y": "no GPU", "z": "rack drained"}})
	}})
	cfg := loadConfig(t, configHeader+extenderYAML(ext.URL, "filterVerb: filter"))

	require.NoError(t, reconcileWith(t, client, cfg))

	m := f.machine("m1")
	require.Len(t, m.Status.Conditions, 1)
	c := m.Status.Conditions[0]
	assert.Equal(t, scheduler.ReasonUnschedulable, c.Reason)
	assert.Contains(t, c.Message, "5 of 6 Nodes are Admitted, Ready and not LeaseExpired")
	assert.Contains(t, c.Message, `cpu "2"`)
	assert.Contains(t, c.Message, "NodeEligible refused 1 (Node is not Admitted, Ready and not LeaseExpired)")
	assert.Contains(t, c.Message, "NodeResourcesFit refused 2 (insufficient free cpu or memory)")
	assert.Contains(t, c.Message, "extender "+ext.URL+" refused 2 (no GPU)")
	assert.Contains(t, c.Message, "extender "+ext.URL+" refused 1 (rack drained)")
}

func TestExtender_OverHTTPSTrustsOnlyItsCAAndPresentsTheClientCertificate(t *testing.T) {
	var peers sync.Map
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peers.Store(len(r.TLS.PeerCertificates), true)
		var body struct {
			NodeNames []string `json:"nodeNames"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		answer(w, map[string]any{"nodeNames": body.NodeNames[1:]})
	}))
	ts.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600))
	certFile, keyFile := selfSignedClientCert(t, dir)
	base := []string{"filterVerb: filter", "tlsCAFile: " + caFile}

	t.Run("with the CA and a client certificate the call is made and answered", func(t *testing.T) {
		f, client := newFakeAPI(t)
		twoNodes(f)
		cfg := loadConfig(t, configHeader+extenderYAML(ts.URL, append(base, "tlsCertFile: "+certFile, "tlsKeyFile: "+keyFile)...))
		require.NoError(t, reconcileWith(t, client, cfg))
		assert.Equal(t, "b", f.machine("m1").Spec.NodeName, "the extender dropped the first Node")
		_, sawCert := peers.Load(1)
		assert.True(t, sawCert, "the server saw the client certificate")
	})

	t.Run("without a client certificate the server refuses and the Machine is not bound", func(t *testing.T) {
		f, client := newFakeAPI(t)
		twoNodes(f)
		cfg := loadConfig(t, configHeader+extenderYAML(ts.URL, base...))
		require.NoError(t, reconcileWith(t, client, cfg))
		assert.Empty(t, f.machine("m1").Spec.NodeName)
		assert.Equal(t, scheduler.ReasonExtenderError, f.machine("m1").Status.Conditions[0].Reason)
	})

	t.Run("a CA that did not sign the server certificate is refused", func(t *testing.T) {
		f, client := newFakeAPI(t)
		twoNodes(f)
		cfg := loadConfig(t, configHeader+extenderYAML(ts.URL, "filterVerb: filter", "tlsCAFile: "+certFile, "tlsCertFile: "+certFile, "tlsKeyFile: "+keyFile))
		require.NoError(t, reconcileWith(t, client, cfg))
		assert.Empty(t, f.machine("m1").Spec.NodeName)
	})
}

func selfSignedClientCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "scheduler"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certFile, keyFile
}

func TestLoadConfig_RefusesEachError(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	ts := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600))
	certFile, keyFile := selfSignedClientCert(t, dir)
	const loopback = "http://127.0.0.1:9000/ext"
	const secure = "https://127.0.0.1:9000/ext"
	ext := func(prefix string, lines ...string) string {
		return configHeader + extenderYAML(prefix, lines...)
	}

	tests := []struct {
		name string
		body string
		want string
	}{
		{"empty file", "", "EOF"},
		{"wrong apiVersion", "apiVersion: v1\nkind: SchedulerConfiguration\n", "apiVersion"},
		{"wrong kind", "apiVersion: scheduler.gaon.garam.sh/v1alpha1\nkind: Other\n", "kind"},
		{"unknown top-level field", configHeader + "bogus: 1\n", "bogus"},
		{"unknown plugin field", configHeader + "scorePlugins:\n  - name: LeastAllocated\n    bogus: 1\n", "bogus"},
		{"unknown extender field", ext(loopback, "filterVerb: f", "bogus: 1"), "bogus"},
		{"unknown plugin", configHeader + "scorePlugins:\n  - name: MostAllocated\n", "unknown plugin"},
		{"NodeEligible cannot be configured", configHeader + "scorePlugins:\n  - name: NodeEligible\n", "unknown plugin"},
		{"plugin named twice", configHeader + "scorePlugins:\n  - name: LeastAllocated\n  - name: LeastAllocated\n", "named twice"},
		{"plugin weight 0", configHeader + "scorePlugins:\n  - name: LeastAllocated\n    weight: 0\n", "below 1"},
		{"plugin weight -2", configHeader + "scorePlugins:\n  - name: LeastAllocated\n    weight: -2\n", "below 1"},
		{"two documents", configHeader + "---\n" + configHeader, "more than one document"},
		{"extender weight 0", ext(loopback, "filterVerb: f", "weight: 0"), "below 1"},
		{"extender timeout 0", ext(loopback, "filterVerb: f", "timeout: 0s"), "not positive"},
		{"extender timeout negative", ext(loopback, "filterVerb: f", "timeout: -1s"), "not positive"},
		{"extender timeout not a duration", ext(loopback, "filterVerb: f", "timeout: soon"), "timeout"},
		{"extender with no verb", ext(loopback), "neither filterVerb nor prioritizeVerb"},
		{"extender with no urlPrefix", configHeader + "extenders:\n  - filterVerb: f\n", "urlPrefix"},
		{"http to a non-loopback address", ext("http://10.0.0.5:9000/ext", "filterVerb: f"), "loopback"},
		{"http to a name", ext("http://localhost:9000/ext", "filterVerb: f"), "loopback"},
		{"http with tls files", ext(loopback, "filterVerb: f", "tlsCAFile: "+caFile), "https"},
		{"https with no CA file", ext(secure, "filterVerb: f"), "tlsCAFile"},
		{"https with a certificate and no key", ext(secure, "filterVerb: f", "tlsCAFile: "+caFile, "tlsCertFile: "+certFile), "both tlsCertFile and tlsKeyFile"},
		{"https with a key and no certificate", ext(secure, "filterVerb: f", "tlsCAFile: "+caFile, "tlsKeyFile: "+keyFile), "both tlsCertFile and tlsKeyFile"},
		{"https with a CA file that is missing", ext(secure, "filterVerb: f", "tlsCAFile: "+filepath.Join(dir, "none.pem")), "tls ca"},
		{"scheme other than http and https", ext("ftp://127.0.0.1/ext", "filterVerb: f"), "scheme"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := scheduler.LoadConfig(writeConfig(t, tt.body))

			assert.ErrorContains(t, err, tt.want)
		})
	}

	t.Run("a file that does not exist", func(t *testing.T) {
		_, err := scheduler.LoadConfig(filepath.Join(dir, "missing.yaml"))
		assert.Error(t, err)
	})

	controls := map[string]string{
		"only the header":                        configHeader,
		"a plugin with a weight":                 configHeader + "scorePlugins:\n  - name: LeastAllocated\n    weight: 1\n",
		"an http extender on a loopback address": ext(loopback, "filterVerb: f", "weight: 1", "timeout: 1s", "ignorable: true"),
		"an http extender on IPv6 loopback":      ext("http://[::1]:9000/ext", "prioritizeVerb: p"),
		"an https extender with a CA file":       ext(secure, "filterVerb: f", "tlsCAFile: "+caFile),
		"an https extender with all three":       ext(secure, "filterVerb: f", "tlsCAFile: "+caFile, "tlsCertFile: "+certFile, "tlsKeyFile: "+keyFile),
	}
	for name, body := range controls {
		t.Run("control: "+name, func(t *testing.T) {
			_, err := scheduler.LoadConfig(writeConfig(t, body))
			assert.NoError(t, err)
		})
	}
}

func TestExtender_TimeoutIsTheConfiguredOne(t *testing.T) {
	// A call that answers after 300ms succeeds with no timeout set, and fails
	// with a 50ms timeout.
	slow := func(w http.ResponseWriter, c stubCall) {
		time.Sleep(300 * time.Millisecond)
		passAll(w, c)
	}
	ext := newStubExtender(t, stubVerbs{"filter": slow})

	f, client := newFakeAPI(t)
	twoNodes(f)
	require.NoError(t, reconcileWith(t, client, loadConfig(t, configHeader+extenderYAML(ext.URL, "filterVerb: filter"))))
	assert.Equal(t, "a", f.machine("m1").Spec.NodeName)

	f, client = newFakeAPI(t)
	twoNodes(f)
	require.NoError(t, reconcileWith(t, client, loadConfig(t, configHeader+extenderYAML(ext.URL, "filterVerb: filter", "timeout: 50ms"))))
	assert.Empty(t, f.machine("m1").Spec.NodeName)
}
