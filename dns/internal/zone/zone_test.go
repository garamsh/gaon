package zone_test

import (
	"testing"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"

	"github.com/garamsh/gaon/dns/internal/zone"
)

func TestNewRefusesAnInvalidZone(t *testing.T) {
	for _, name := range []string{"", ".", "a..b"} {
		_, err := zone.New(name)
		assert.Error(t, err, "%q", name)
	}
	// Control: a domain name is accepted, with or without the final dot.
	for _, name := range []string{"cluster.local", "Cluster.Local."} {
		_, err := zone.New(name)
		assert.NoError(t, err, name)
	}
}

func TestCoversTheZoneAndNamesBelowItOnly(t *testing.T) {
	z, err := zone.New("cluster.local")
	assert.NoError(t, err)
	for _, name := range []string{"cluster.local.", "A.B.CLUSTER.local."} {
		assert.True(t, z.Covers(name), name)
	}
	for _, name := range []string{"local.", "xcluster.local.", "cluster.local.example.org.", "."} {
		assert.False(t, z.Covers(name), name)
	}
}

func TestAnswerIsServfailBeforeAnyList(t *testing.T) {
	z, err := zone.New("cluster.local")
	assert.NoError(t, err)
	reply := z.Answer(new(dns.Msg).SetQuestion("web.default.svc.cluster.local.", dns.TypeA))
	assert.Equal(t, dns.RcodeServerFailure, reply.Rcode)
}
