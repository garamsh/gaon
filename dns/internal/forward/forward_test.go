package forward_test

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/dns/internal/forward"
)

func TestParseUpstream(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{"192.0.2.53", "192.0.2.53:53"},
		{"192.0.2.53:5353", "192.0.2.53:5353"},
		{"2001:db8::53", "[2001:db8::53]:53"},
		{"[2001:db8::53]:5353", "[2001:db8::53]:5353"},
	}
	for _, tt := range tests {
		got, err := forward.ParseUpstream(tt.text)
		require.NoError(t, err, tt.text)
		assert.Equal(t, tt.want, got.String(), tt.text)
	}
}

func TestParseUpstreamRefusesANameAndABadPort(t *testing.T) {
	for _, text := range []string{"", "dns.example.org", "192.0.2.53:", "192.0.2.53:http", "192.0.2.53:70000"} {
		_, err := forward.ParseUpstream(text)
		assert.Error(t, err, text)
	}
}

func TestConfiguredIsFalseWithoutUpstreams(t *testing.T) {
	assert.False(t, forward.New(nil).Configured())
	assert.True(t, forward.New([]netip.AddrPort{netip.MustParseAddrPort("192.0.2.53:53")}).Configured())
}
