package certificate

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ensureDir creates dir with mode 0700 when it is missing.
func ensureDir(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("certificate directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create certificate directory: %w", err)
	}
	return nil
}

// loadPair reads the pair in dir and checks it is a valid one for node at now.
func loadPair(dir, node string, now time.Time) (*tls.Certificate, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, CertFile))
	if err != nil {
		return nil, fmt.Errorf("read certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, KeyFile))
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	return parsePair(certPEM, keyPEM, node, now)
}

// parsePair returns the pair the PEM files hold when the key matches the
// certificate and the certificate is one for node at now.
func parsePair(certPEM, keyPEM []byte, node string, now time.Time) (*tls.Certificate, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("certificate and key: %w", err)
	}
	pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	if err := checkLeaf(pair.Leaf, node, now); err != nil {
		return nil, err
	}
	return &pair, nil
}

// writePair puts the key, mode 0600, and the certificate in dir. Each is
// written to a temporary file in dir first and both are then renamed into
// place, so a crash leaves each file whole.
func writePair(dir string, keyPEM, certPEM []byte) error {
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{{KeyFile, keyPEM, 0o600}, {CertFile, certPEM, 0o644}}
	var temps []string
	defer func() {
		for _, t := range temps {
			_ = os.Remove(t)
		}
	}()
	for _, f := range files {
		tmp, err := writeTemp(dir, f.name, f.data, f.mode)
		if err != nil {
			return err
		}
		temps = append(temps, tmp)
	}
	for i, f := range files {
		if err := os.Rename(temps[i], filepath.Join(dir, f.name)); err != nil {
			return fmt.Errorf("replace %s: %w", f.name, err)
		}
		temps[i] = ""
	}
	return nil
}

func writeTemp(dir, name string, data []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return "", fmt.Errorf("write %s: %w", name, err)
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write %s: %w", name, err)
	}
	return f.Name(), nil
}
