package apiserver

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// tlsGiven reports whether c names the TLS files, and refuses a partial set.
func (c Config) tlsGiven() (bool, error) {
	n := 0
	for _, f := range []string{c.TLSClientCA, c.TLSCert, c.TLSKey} {
		if f != "" {
			n++
		}
	}
	if n != 0 && n != 3 {
		return false, errors.New("tls-client-ca, tls-cert and tls-key are given all together or not at all")
	}
	return n == 3, nil
}

// serverTLS returns the TLS configuration that verifies a client certificate
// against the client CA where one is given and does not require one, or nil
// where c names no TLS files.
func (c Config) serverTLS() (*tls.Config, error) {
	given, err := c.tlsGiven()
	if err != nil || !given {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("load server certificate: %w", err)
	}
	pem, err := os.ReadFile(c.TLSClientCA)
	if err != nil {
		return nil, fmt.Errorf("read client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("client CA %s holds no certificate", c.TLSClientCA)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}, nil
}

// validateAgentTLS refuses one of the two agent client files without the
// other, and either without the TLS files, because the client CA that verifies
// the agent's certificate is one of them.
func (c Config) validateAgentTLS() error {
	if (c.AgentCert == "") != (c.AgentKey == "") {
		return errors.New("agent-client-cert-file and agent-client-key-file are given together or not at all")
	}
	given, err := c.tlsGiven()
	if err != nil {
		return err
	}
	if c.AgentCert != "" && !given {
		return errors.New("agent-client-cert-file needs tls-client-ca, tls-cert and tls-key: tls-client-ca verifies the agent's certificate")
	}
	return nil
}

// agentClient returns the client that calls the Node Agents, or nil where c
// names no agent client certificate.
func (c Config) agentClient() (*agentClient, error) {
	if err := c.validateAgentTLS(); err != nil || c.AgentCert == "" {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(c.AgentCert, c.AgentKey)
	if err != nil {
		return nil, fmt.Errorf("load agent client certificate: %w", err)
	}
	pem, err := os.ReadFile(c.TLSClientCA)
	if err != nil {
		return nil, fmt.Errorf("read client CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("client CA %s holds no certificate", c.TLSClientCA)
	}
	return &agentClient{cert: cert, roots: roots}, nil
}
