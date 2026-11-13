package proxy

import (
	"bytes"
	"crypto/tls"
	"log/slog"
	"sync"

	"github.com/garamsh/gaon/lib/api"
)

// The keys of the Secret that holds a certificate.
const (
	certKey = "tls.crt"
	keyKey  = "tls.key"
)

// certificates holds the certificate each HTTPS listener presents, by the name
// of its Secret. A pair that does not load leaves the last good one in place.
type certificates struct {
	log     *slog.Logger
	mu      sync.Mutex
	entries map[string]*certEntry
}

type certEntry struct {
	// crt and key are the bytes last read, good or not, so an unchanged Secret is parsed once.
	crt, key []byte
	read     bool
	cert     *tls.Certificate
	// problem is the last thing logged about the Secret, so a repeat is not logged again.
	problem string
}

func newCertificates(log *slog.Logger) *certificates {
	return &certificates{log: log, entries: map[string]*certEntry{}}
}

// get returns the current certificate of the Secret.
func (c *certificates) get(secret string) *tls.Certificate {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[secret]; e != nil {
		return e.cert
	}
	return nil
}

// sync loads the certificate of each wanted Secret from secrets, and forgets
// the Secrets no longer wanted. It logs a problem once. A log line names the
// Secret and the standard library's error, and never a value.
func (c *certificates) sync(wanted []string, secrets []api.Secret) {
	byName := make(map[string]api.Secret, len(secrets))
	for _, s := range secrets {
		byName[s.Metadata.Name] = s
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	keep := map[string]bool{}
	for _, name := range wanted {
		keep[name] = true
		e := c.entries[name]
		if e == nil {
			e = &certEntry{}
			c.entries[name] = e
		}
		s, ok := byName[name]
		if !ok {
			c.report(name, e, "the Secret is not among the Secrets labelled for certificates")
			continue
		}
		crt, key := s.Data[certKey], s.Data[keyKey]
		if e.read && bytes.Equal(crt, e.crt) && bytes.Equal(key, e.key) {
			continue
		}
		e.crt, e.key, e.read = bytes.Clone(crt), bytes.Clone(key), true
		pair, err := tls.X509KeyPair(crt, key)
		if err != nil {
			c.report(name, e, "the Secret holds no valid certificate and key: "+err.Error())
			continue
		}
		e.cert, e.problem = &pair, ""
		c.log.Info("loaded the certificate", "secret", name)
	}
	for name := range c.entries {
		if !keep[name] {
			delete(c.entries, name)
		}
	}
}

func (c *certificates) report(name string, e *certEntry, problem string) {
	if e.problem == problem {
		return
	}
	e.problem = problem
	keeping := "no certificate is served for it"
	if e.cert != nil {
		keeping = "the last good certificate stays in use"
	}
	c.log.Warn("certificate not loaded", "secret", name, "problem", problem, "effect", keeping)
}
