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

// loadPair reads the pair file in dir and checks it is a valid one for node at
// now.
func loadPair(dir, node string, now time.Time) (*tls.Certificate, error) {
	data, err := os.ReadFile(filepath.Join(dir, PairFile))
	if err != nil {
		return nil, fmt.Errorf("read pair: %w", err)
	}
	return parsePair(data, node, now)
}

// parsePair returns the pair the PEM data holds, the certificate block and the
// key block in either order, when the key matches the certificate and the
// certificate is one for node at now.
func parsePair(data []byte, node string, now time.Time) (*tls.Certificate, error) {
	pair, err := tls.X509KeyPair(data, data)
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

// joinPair returns the certificate block followed by the key block.
func joinPair(certPEM, keyPEM []byte) []byte {
	return append(append([]byte{}, certPEM...), keyPEM...)
}

// syncDir is replaced by a test to observe the directory sync.
var syncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// writePair puts the certificate block and then the key block in the pair file
// in dir, mode 0600. It writes a temporary file in dir and renames it into
// place, so a crash leaves the old pair or the new one whole, and syncs dir so
// the rename survives a power cut.
func writePair(dir string, keyPEM, certPEM []byte) error {
	tmp, err := writeTemp(dir, PairFile, joinPair(certPEM, keyPEM), 0o600)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, PairFile)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", PairFile, err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync %s directory: %w", PairFile, err)
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
