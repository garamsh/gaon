package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/garamsh/gaon/gaon-cli/internal/command"
)

func TestRun_ReachesTheServerOverMutualTLSWhenGivenTheThreeFiles(t *testing.T) {
	a := newMTLSAPI(t)
	args := append(append([]string{"--server", a.URL}, a.flags()...), "get", "microvm")
	var out, errOut bytes.Buffer

	code := run(context.Background(), args, environment{}, &out, &errOut)

	assert.Equal(t, command.ExitOK, code, errOut.String())
	assert.Equal(t, int32(1), a.requests.Load())
	assert.Equal(t, int32(1), a.withCerts.Load(), "the server saw the client certificate")
}

func TestRun_RefusesAPartialTLSSetBeforeAnyRequest(t *testing.T) {
	a := newMTLSAPI(t)
	tests := []struct {
		name    string
		tls     []string
		missing string
	}{
		{"ca only", []string{"--tls-ca", a.ca}, "--tls-cert, --tls-key"},
		{"cert and key", []string{"--tls-cert", a.cert, "--tls-key", a.key}, "--tls-ca"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append(append([]string{"--server", a.URL}, tt.tls...), "get", "microvm")
			var out, errOut bytes.Buffer

			code := run(context.Background(), args, environment{}, &out, &errOut)

			assert.Equal(t, command.ExitUsage, code)
			assert.Contains(t, errOut.String(), tt.missing)
			assert.Zero(t, a.requests.Load())
		})
	}
	var out, errOut bytes.Buffer
	args := append(append([]string{"--server", a.URL}, a.flags()...), "get", "microvm")
	code := run(context.Background(), args, environment{}, &out, &errOut)
	assert.Equal(t, command.ExitOK, code, "control: the three together are accepted")
}
