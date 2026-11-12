package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gaon-cli/internal/command"
)

// countingServer is a plain HTTP API server that counts its requests.
func countingServer(t *testing.T) (url string, requests *atomic.Int32) {
	t.Helper()
	requests = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"apiVersion":"compute.gaon.garam.sh/v1alpha1","kind":"MicroVMList","metadata":{"resourceVersion":"1"},"items":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, requests
}

// writeFile writes text to dir/name and returns the path.
func writeFile(t *testing.T, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, []byte(text), 0o600))
	return p
}

// plainConfig is a config file whose current context reaches server over
// plain HTTP.
func plainConfig(server string) string {
	return fmt.Sprintf(`current-context: c
clusters:
- name: k
  cluster:
    server: %s
contexts:
- name: c
  context:
    cluster: k
`, server)
}

// mtlsConfig is a config file whose current context reaches the server of a
// over mutual TLS, naming the files by the given paths.
func mtlsConfig(a *mtlsAPI, ca, cert, key string) string {
	return fmt.Sprintf(`current-context: c
clusters:
- name: k
  cluster:
    server: %s
    certificate-authority: %s
users:
- name: u
  user:
    client-certificate: %s
    client-key: %s
contexts:
- name: c
  context:
    cluster: k
    user: u
`, a.URL, ca, cert, key)
}

// runGaon runs the CLI with a temporary home directory, so no test reads the
// real one, and returns what it printed.
func runGaon(t *testing.T, env environment, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	if env.home == "" {
		env.home = t.TempDir()
	}
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, env, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestRun_ReachesAMutualTLSServerFromTheConfigFileAlone(t *testing.T) {
	a := newMTLSAPI(t)
	text := mtlsConfig(a, a.ca, a.cert, a.key)
	home := t.TempDir()
	defaultFile := writeFile(t, home, ".gaon/config", text)
	other := writeFile(t, t.TempDir(), "elsewhere.yaml", text)

	tests := []struct {
		name string
		env  environment
		args []string
	}{
		{"default path", environment{home: home}, []string{"get", "microvm"}},
		{"GAON_CONFIG", environment{config: other}, []string{"get", "microvm"}},
		{"--config", environment{}, []string{"--config", other, "get", "microvm"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a.requests.Store(0)
			a.withCerts.Store(0)

			code, _, stderr := runGaon(t, tt.env, tt.args...)

			assert.Equal(t, command.ExitOK, code, stderr)
			assert.Equal(t, int32(1), a.requests.Load())
			assert.Equal(t, int32(1), a.withCerts.Load(), "the server saw the client certificate")
		})
	}
	require.FileExists(t, defaultFile)
}

func TestRun_FindsTheConfigFileByFlagThenEnvironmentThenDefault(t *testing.T) {
	defURL, defHits := countingServer(t)
	envURL, envHits := countingServer(t)
	flagURL, flagHits := countingServer(t)
	home := t.TempDir()
	writeFile(t, home, ".gaon/config", plainConfig(defURL))
	envFile := writeFile(t, t.TempDir(), "env.yaml", plainConfig(envURL))
	flagFile := writeFile(t, t.TempDir(), "flag.yaml", plainConfig(flagURL))

	tests := []struct {
		name string
		env  environment
		args []string
		hits [3]int32 // default, environment, flag
	}{
		{"default alone", environment{home: home}, []string{"get", "microvm"}, [3]int32{1, 0, 0}},
		{"environment over default", environment{home: home, config: envFile}, []string{"get", "microvm"}, [3]int32{0, 1, 0}},
		{"flag over environment and default", environment{home: home, config: envFile}, []string{"--config", flagFile, "get", "microvm"}, [3]int32{0, 0, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defHits.Store(0)
			envHits.Store(0)
			flagHits.Store(0)

			code, _, stderr := runGaon(t, tt.env, tt.args...)

			assert.Equal(t, command.ExitOK, code, stderr)
			assert.Equal(t, tt.hits, [3]int32{defHits.Load(), envHits.Load(), flagHits.Load()})
		})
	}
}

func TestRun_AFlagOrGAONServerOverridesTheFilesServer(t *testing.T) {
	fileURL, fileHits := countingServer(t)
	envURL, envHits := countingServer(t)
	flagURL, flagHits := countingServer(t)
	file := writeFile(t, t.TempDir(), "config", plainConfig(fileURL))

	tests := []struct {
		name string
		env  environment
		args []string
		hits [3]int32 // file, environment, flag
	}{
		{"control: neither, the file's server", environment{}, []string{"--config", file, "get", "microvm"}, [3]int32{1, 0, 0}},
		{"GAON_SERVER", environment{server: envURL}, []string{"--config", file, "get", "microvm"}, [3]int32{0, 1, 0}},
		{"--server", environment{}, []string{"--config", file, "--server", flagURL, "get", "microvm"}, [3]int32{0, 0, 1}},
		{"--server over GAON_SERVER", environment{server: envURL}, []string{"--config", file, "--server", flagURL, "get", "microvm"}, [3]int32{0, 0, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileHits.Store(0)
			envHits.Store(0)
			flagHits.Store(0)

			code, _, stderr := runGaon(t, tt.env, tt.args...)

			assert.Equal(t, command.ExitOK, code, stderr)
			assert.Equal(t, tt.hits, [3]int32{fileHits.Load(), envHits.Load(), flagHits.Load()})
		})
	}
}

func TestRun_ATLSFlagOverridesTheSameFileOfTheConfigFile(t *testing.T) {
	a := newMTLSAPI(t)
	notACA := writeFile(t, t.TempDir(), "not-a-ca.pem", "not a certificate\n")
	missing := filepath.Join(t.TempDir(), "missing.pem")

	t.Run("control: the file alone is accepted", func(t *testing.T) {
		a.requests.Store(0)
		file := writeFile(t, t.TempDir(), "config", mtlsConfig(a, a.ca, a.cert, a.key))

		code, _, stderr := runGaon(t, environment{}, "--config", file, "get", "microvm")

		assert.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, int32(1), a.requests.Load())
	})

	// A flag naming a file that does not fit refuses the command only if the
	// flag, and not the file, is the one in effect.
	refused := []struct{ name, flag, value string }{
		{"--tls-ca", "--tls-ca", notACA},
		{"--tls-cert", "--tls-cert", missing},
		{"--tls-key", "--tls-key", missing},
	}
	for _, tt := range refused {
		t.Run(tt.name+" beats the file", func(t *testing.T) {
			a.requests.Store(0)
			file := writeFile(t, t.TempDir(), "config", mtlsConfig(a, a.ca, a.cert, a.key))

			code, _, _ := runGaon(t, environment{}, "--config", file, tt.flag, tt.value, "get", "microvm")

			assert.Equal(t, command.ExitUsage, code)
			assert.Zero(t, a.requests.Load())
		})
	}

	t.Run("the three flags beat three files that do not fit", func(t *testing.T) {
		a.requests.Store(0)
		file := writeFile(t, t.TempDir(), "config", mtlsConfig(a, notACA, missing, missing))

		code, _, _ := runGaon(t, environment{}, "--config", file, "get", "microvm")
		assert.Equal(t, command.ExitUsage, code, "control: the file alone is refused")
		assert.Zero(t, a.requests.Load())

		code, _, stderr := runGaon(t, environment{}, append([]string{"--config", file}, append(a.flags(), "get", "microvm")...)...)
		assert.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, int32(1), a.requests.Load())
	})
}

func TestRun_ResolvesFilePathsOfTheConfigFileAgainstItsDirectory(t *testing.T) {
	a := newMTLSAPI(t)
	dir := filepath.Dir(a.ca)
	file := writeFile(t, dir, "config", mtlsConfig(a, filepath.Base(a.ca), filepath.Base(a.cert), filepath.Base(a.key)))

	code, _, stderr := runGaon(t, environment{}, "--config", file, "get", "microvm")

	assert.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, int32(1), a.withCerts.Load())
}

func TestRun_RefusesAConfigFileThatIsMissingOrDoesNotFit(t *testing.T) {
	url, hits := countingServer(t)
	tests := []struct {
		name      string
		bad, good string
	}{
		{"malformed YAML", "clusters: [\n", plainConfig("SERVER")},
		{"a field the file does not have", plainConfig("SERVER") + "bogus: 1\n", plainConfig("SERVER")},
		{"current context names no context", strings.Replace(plainConfig("SERVER"), "current-context: c", "current-context: nope", 1), plainConfig("SERVER")},
		{"context names a cluster that is not defined", strings.Replace(plainConfig("SERVER"), "cluster: k\n", "cluster: nope\n", 1), plainConfig("SERVER")},
		{"context names no cluster at all", strings.Replace(plainConfig("SERVER"), "    cluster: k\n", "    user: \"\"\n", 1), plainConfig("SERVER")},
		{"context names a user that is not defined", plainConfig("SERVER") + "    user: nope\n", plainConfig("SERVER")},
		{"cluster has no server", strings.Replace(plainConfig("SERVER"), "    server: SERVER\n", "    certificate-authority: ca.pem\n", 1), plainConfig("SERVER")},
		{"user has a certificate and no key", "users:\n- name: u\n  user:\n    client-certificate: cert.pem\n" + plainConfig("SERVER") + "    user: u\n", "users:\n- name: u\n  user: {}\n" + plainConfig("SERVER") + "    user: u\n"},
		{"cluster defined twice", strings.Replace(plainConfig("SERVER"), "contexts:", "- name: k\n  cluster:\n    server: SERVER\ncontexts:", 1), plainConfig("SERVER")},
		{"cluster without a name", strings.Replace(plainConfig("SERVER"), "contexts:", "- cluster:\n    server: SERVER\ncontexts:", 1), strings.Replace(plainConfig("SERVER"), "contexts:", "- name: spare\n  cluster:\n    server: SERVER\ncontexts:", 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits.Store(0)
			dir := t.TempDir()
			bad := writeFile(t, dir, "bad.yaml", strings.ReplaceAll(tt.bad, "SERVER", url))
			good := writeFile(t, dir, "good.yaml", strings.ReplaceAll(tt.good, "SERVER", url))

			code, _, stderr := runGaon(t, environment{}, "--config", bad, "get", "microvm")
			assert.Equal(t, command.ExitUsage, code)
			assert.Contains(t, stderr, bad)
			assert.Zero(t, hits.Load())

			code, _, stderr = runGaon(t, environment{}, "--config", good, "get", "microvm")
			assert.Equal(t, command.ExitOK, code, "control: the fixed file is accepted: "+stderr)
			assert.Equal(t, int32(1), hits.Load())
		})
	}

	t.Run("a --server does not hide a file that does not fit", func(t *testing.T) {
		hits.Store(0)
		bad := writeFile(t, t.TempDir(), "bad.yaml", "clusters: [\n")

		code, _, stderr := runGaon(t, environment{}, "--config", bad, "--server", url, "get", "microvm")

		assert.Equal(t, command.ExitUsage, code)
		assert.Contains(t, stderr, bad)
		assert.Zero(t, hits.Load())
	})
}

func TestRun_RefusesAMissingConfigFileOnlyWhenSomethingNamedIt(t *testing.T) {
	url, hits := countingServer(t)
	missing := filepath.Join(t.TempDir(), "missing.yaml")

	tests := []struct {
		name string
		env  environment
		args []string
	}{
		{"--config", environment{}, []string{"--config", missing, "--server", url, "get", "microvm"}},
		{"GAON_CONFIG", environment{config: missing}, []string{"--server", url, "get", "microvm"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits.Store(0)

			code, _, stderr := runGaon(t, tt.env, tt.args...)

			assert.Equal(t, command.ExitUsage, code)
			assert.Contains(t, stderr, missing)
			assert.Zero(t, hits.Load())
		})
	}

	t.Run("control: a missing default file is no file", func(t *testing.T) {
		hits.Store(0)

		code, _, stderr := runGaon(t, environment{}, "--server", url, "get", "microvm")

		assert.Equal(t, command.ExitOK, code, stderr)
		assert.Equal(t, int32(1), hits.Load())
	})
}

func TestRun_NamesTheConfigFileWhenNoServerIsGiven(t *testing.T) {
	// A file with no current context gives no server.
	file := writeFile(t, t.TempDir(), "config", "clusters: []\n")

	code, _, stderr := runGaon(t, environment{}, "--config", file, "get", "microvm")

	assert.Equal(t, command.ExitUsage, code)
	assert.Contains(t, stderr, "current-context")
}

func TestRun_ConfigCurrentContextPrintsItAndUseContextSwitchesIt(t *testing.T) {
	aURL, aHits := countingServer(t)
	bURL, bHits := countingServer(t)
	file := writeFile(t, t.TempDir(), "config", fmt.Sprintf(`current-context: first
clusters:
- name: one
  cluster:
    server: %s
- name: two
  cluster:
    server: %s
contexts:
- name: first
  context:
    cluster: one
- name: second
  context:
    cluster: two
`, aURL, bURL))

	code, stdout, stderr := runGaon(t, environment{}, "--config", file, "config", "current-context")
	assert.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "first\n", stdout)

	code, _, stderr = runGaon(t, environment{}, "--config", file, "get", "microvm")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, [2]int32{1, 0}, [2]int32{aHits.Load(), bHits.Load()})

	code, _, stderr = runGaon(t, environment{}, "--config", file, "config", "use-context", "second")
	assert.Equal(t, command.ExitOK, code, stderr)

	code, stdout, _ = runGaon(t, environment{}, "--config", file, "config", "current-context")
	assert.Equal(t, command.ExitOK, code)
	assert.Equal(t, "second\n", stdout)
	code, _, stderr = runGaon(t, environment{}, "--config", file, "get", "microvm")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, [2]int32{1, 1}, [2]int32{aHits.Load(), bHits.Load()}, "the next command reaches the second context's server")

	written, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Contains(t, string(written), aURL, "the other entries are kept")
	assert.Contains(t, string(written), bURL)
}

func TestRun_ConfigCommandsRefuseWhatTheyCannotDo(t *testing.T) {
	url, _ := countingServer(t)
	good := plainConfig(url)
	dir := t.TempDir()
	file := writeFile(t, dir, "config", good)
	missing := filepath.Join(dir, "missing")

	t.Run("use-context of a context that is not defined leaves the file as it was", func(t *testing.T) {
		code, _, stderr := runGaon(t, environment{}, "--config", file, "config", "use-context", "nope")
		assert.Equal(t, command.ExitUsage, code)
		assert.Contains(t, stderr, file)
		after, err := os.ReadFile(file)
		require.NoError(t, err)
		assert.Equal(t, good, string(after))

		code, _, stderr = runGaon(t, environment{}, "--config", file, "config", "use-context", "c")
		assert.Equal(t, command.ExitOK, code, "control: a defined context is accepted: "+stderr)
	})

	t.Run("a missing file", func(t *testing.T) {
		for _, sub := range []string{"current-context", "use-context c"} {
			code, _, stderr := runGaon(t, environment{}, append([]string{"--config", missing, "config"}, strings.Fields(sub)...)...)
			assert.Equal(t, command.ExitUsage, code, sub)
			assert.Contains(t, stderr, missing)
		}
	})

	t.Run("a file with no current context is a failure, not a usage error", func(t *testing.T) {
		none := writeFile(t, dir, "none", "clusters: []\n")
		code, stdout, stderr := runGaon(t, environment{}, "--config", none, "config", "current-context")
		assert.Equal(t, command.ExitFailure, code)
		assert.Empty(t, stdout)
		assert.Contains(t, stderr, none)
	})

	t.Run("a mistake in the command", func(t *testing.T) {
		for _, args := range [][]string{
			{"config"},
			{"config", "nope"},
			{"config", "use-context"},
			{"config", "use-context", "a", "b"},
			{"config", "current-context", "a"},
		} {
			code, _, _ := runGaon(t, environment{}, append([]string{"--config", file}, args...)...)
			assert.Equal(t, command.ExitUsage, code, strings.Join(args, " "))
		}
	})
}

func TestRun_NeverPrintsTheContentOfACredentialFile(t *testing.T) {
	a := newMTLSAPI(t)
	const secret = "SECRET-KEY-MATERIAL"
	badKey := writeFile(t, t.TempDir(), "key.pem", secret+"\n")
	file := writeFile(t, t.TempDir(), "config", mtlsConfig(a, a.ca, a.cert, badKey))

	code, stdout, stderr := runGaon(t, environment{}, "--config", file, "get", "microvm")

	assert.Equal(t, command.ExitUsage, code, "setup: the key does not fit")
	assert.NotContains(t, stdout+stderr, secret)
	assert.Contains(t, stderr, badKey, "control: the error names the file")
}
