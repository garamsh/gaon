package main

import (
	"bytes"
	"errors"
	"flag"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFlagsDefaults(t *testing.T) {
	cfg, err := parseFlags([]string{"-server", "https://127.0.0.1:8443", "-tls-ca", "/ca.pem"}, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, "cluster.local", cfg.server.Zone)
	assert.Equal(t, ":1053", cfg.listen)
	assert.Equal(t, tokenPath, cfg.server.TokenFile)
	assert.Empty(t, cfg.server.Upstreams, "the upstreams are never read from /etc/resolv.conf")
}

func TestParseFlagsTakesRepeatedUpstreams(t *testing.T) {
	cfg, err := parseFlags([]string{
		"-server", "https://127.0.0.1:8443", "-tls-ca", "/ca.pem",
		"-zone", "gaon.internal", "-upstream", "192.0.2.53", "-upstream", "192.0.2.54:5353",
	}, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, "gaon.internal", cfg.server.Zone)
	require.Len(t, cfg.server.Upstreams, 2)
	assert.Equal(t, "192.0.2.53:53", cfg.server.Upstreams[0].String())
	assert.Equal(t, "192.0.2.54:5353", cfg.server.Upstreams[1].String())
}

func TestParseFlagsRefusesWhatItCannotRun(t *testing.T) {
	_, err := parseFlags([]string{"-tls-ca", "/ca.pem"}, &bytes.Buffer{})
	assert.Error(t, err, "no API server")

	_, err = parseFlags([]string{"-server", "https://127.0.0.1:8443"}, &bytes.Buffer{})
	assert.Error(t, err, "no CA file")

	_, err = parseFlags([]string{"-server", "https://127.0.0.1:8443", "-tls-ca", "/ca.pem", "-upstream", "not-an-address"}, &bytes.Buffer{})
	assert.Error(t, err, "an upstream that is not an address")

	// Control: the same flags without the bad upstream parse.
	_, err = parseFlags([]string{"-server", "https://127.0.0.1:8443", "-tls-ca", "/ca.pem"}, &bytes.Buffer{})
	assert.NoError(t, err)
}

func TestParseFlagsReportsHelp(t *testing.T) {
	_, err := parseFlags([]string{"-h"}, &bytes.Buffer{})
	assert.True(t, errors.Is(err, flag.ErrHelp))
}
