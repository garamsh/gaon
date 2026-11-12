// Package certificate gets the node agent's client certificate with a
// bootstrap token or from the pair it kept, and renews it before it expires.
package certificate

import (
	"crypto/tls"
	"sync"
	"time"
)

// The names of the files the agent keeps in its certificate directory.
const (
	KeyFile  = "node-agent.key"
	CertFile = "node-agent.crt"
)

const (
	// pollPeriod is the time between two reads of a request awaiting its decision.
	pollPeriod = time.Second
	// retryAfter is the wait after a renewal that failed.
	retryAfter = time.Minute
)

// Params names the Node the certificate is for and where its files are kept.
type Params struct {
	// NodeName is the CN of the certificate.
	NodeName string
	// Dir is the directory of the key and the certificate.
	Dir string
}

// Holder holds the pair the agent presents, so that a client built before
// the pair exists presents the pair the Service obtains.
type Holder struct {
	mu   sync.RWMutex
	pair *tls.Certificate
}

// ClientCertificate is the tls.Config.GetClientCertificate of the agent's
// client. It presents the current pair, or none while there is none.
func (h *Holder) ClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.pair == nil {
		return &tls.Certificate{}, nil
	}
	return h.pair, nil
}

func (h *Holder) get() *tls.Certificate {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pair
}

func (h *Holder) set(pair *tls.Certificate) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pair = pair
}
