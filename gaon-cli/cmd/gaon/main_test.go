package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/garamsh/gaon/gaon-cli/internal/command"
)

func TestRun_TakesTheServerFromTheFlagOrTheDefault(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"apiVersion":"compute.gaon.garam.sh/v1alpha1","kind":"MachineList","metadata":{"resourceVersion":"1"},"items":[]}`))
	}))
	t.Cleanup(srv.Close)

	tests := []struct {
		name          string
		args          []string
		defaultServer string
		code          int
		requests      int32
	}{
		{"flag", []string{"--server", srv.URL, "get", "machine"}, "", command.ExitOK, 1},
		{"default", []string{"get", "machine"}, srv.URL, command.ExitOK, 1},
		{"flag beats default", []string{"--server", srv.URL, "get", "machine"}, "http://127.0.0.1:1", command.ExitOK, 1},
		{"neither", []string{"get", "machine"}, "", command.ExitUsage, 0},
		{"unknown global flag", []string{"--bogus", "get", "machine"}, srv.URL, command.ExitUsage, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests.Store(0)
			var out, errOut bytes.Buffer

			code := run(context.Background(), tt.args, environment{server: tt.defaultServer}, &out, &errOut)

			assert.Equal(t, tt.code, code, errOut.String())
			assert.Equal(t, tt.requests, requests.Load())
		})
	}
}
