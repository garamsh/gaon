// Package etcd is the snapshot Source backed by an etcd cluster's maintenance
// service.
package etcd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	etcdutl "go.etcd.io/etcd/etcdutl/v3/snapshot"
	"go.uber.org/zap"

	"github.com/garamsh/gaon/etcd-snapshot/internal/snapshot"
)

// Config says how to reach etcd.
type Config struct {
	// Endpoints are the etcd client endpoints.
	Endpoints []string
	// CAFile, CertFile and KeyFile are the paths of the CA that signs etcd's
	// certificate and of the client certificate and key presented to etcd. All
	// three are set or none is; with none, etcd is dialled without TLS.
	CAFile, CertFile, KeyFile string
	// DialTimeout bounds the connection to etcd.
	DialTimeout time.Duration
}

// Validate refuses a partial set of the TLS files and names each that is
// missing.
func (c Config) Validate() error {
	var missing []string
	for _, f := range []struct{ flag, value string }{
		{"etcd-ca-file", c.CAFile},
		{"etcd-cert-file", c.CertFile},
		{"etcd-key-file", c.KeyFile},
	} {
		if f.value == "" {
			missing = append(missing, f.flag)
		}
	}
	if len(missing) == 0 || len(missing) == 3 {
		return nil
	}
	return fmt.Errorf("etcd-ca-file, etcd-cert-file and etcd-key-file are given all together or not at all: missing %s", strings.Join(missing, ", "))
}

// tlsConfig returns the TLS configuration that verifies etcd against the CA and
// presents the client certificate, or nil where c names no TLS files.
func (c Config) tlsConfig() (*tls.Config, error) {
	if err := c.Validate(); err != nil || c.CAFile == "" {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load etcd client certificate: %w", err)
	}
	pem, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read etcd CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("etcd CA %s holds no certificate", c.CAFile)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
	}, nil
}

// Source takes snapshots through an etcd client.
type Source struct {
	client *clientv3.Client
}

// New dials etcd and returns the Source. Close releases the connection.
func New(cfg Config) (*Source, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, errors.New("no etcd endpoint given")
	}
	tlsCfg, err := cfg.tlsConfig()
	if err != nil {
		return nil, err
	}
	c, err := clientv3.New(clientv3.Config{
		Endpoints:   cfg.Endpoints,
		DialTimeout: cfg.DialTimeout,
		TLS:         tlsCfg,
	})
	if err != nil {
		return nil, fmt.Errorf("dial etcd: %w", err)
	}
	return &Source{client: c}, nil
}

// Close closes the connection to etcd.
func (s *Source) Close() error { return s.client.Close() }

// Take writes one snapshot to the file at path through the maintenance
// snapshot stream. A stream that ends short of its end is an error.
func (s *Source) Take(ctx context.Context, path string) (err error) {
	resp, err := s.client.SnapshotWithVersion(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Snapshot.Close() }()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err := io.Copy(f, resp.Snapshot); err != nil {
		return fmt.Errorf("read snapshot stream: %w", err)
	}
	return f.Sync()
}

// Revision reads the revision of the store from the snapshot file at path, as
// etcdutl does: the revision of the last entry of its key bucket. The snapshot
// stream carries no revision of its own in etcd 3.6.
func (s *Source) Revision(path string) (snapshot.Revision, error) {
	status, err := etcdutl.NewV3(zap.NewNop()).Status(path)
	if err != nil {
		return 0, err
	}
	return snapshot.Revision(status.Revision), nil
}
