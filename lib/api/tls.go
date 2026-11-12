package api

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// NewTLSClient returns an http.Client that speaks mutual TLS 1.3: it trusts
// the certificates in caFile and no other root, and presents the certificate
// in certFile with the key in keyFile. The client sets no Timeout, so it is
// fit for a watch.
func NewTLSClient(caFile, certFile, keyFile string) (*http.Client, error) {
	roots, err := loadRoots(caFile)
	if err != nil {
		return nil, err
	}
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("tls cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("tls key: %w", err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("tls cert %s with key %s: %w", certFile, keyFile, err)
	}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			RootCAs:      roots,
			Certificates: []tls.Certificate{pair},
		},
		ForceAttemptHTTP2: true,
	}}, nil
}

// NewTLSClientFunc returns an http.Client that speaks mutual TLS 1.3 like
// NewTLSClient, but takes the client certificate from getCert at each
// handshake, so a certificate that changes is presented by the connections made
// after the change. A connection already open keeps the certificate it was made
// with. getCert may return an empty certificate, which presents none.
func NewTLSClientFunc(caFile string, getCert func(*tls.CertificateRequestInfo) (*tls.Certificate, error)) (*http.Client, error) {
	roots, err := loadRoots(caFile)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion:           tls.VersionTLS13,
			RootCAs:              roots,
			GetClientCertificate: getCert,
		},
		ForceAttemptHTTP2: true,
	}}, nil
}

// NewServerTLSClient returns an http.Client that speaks TLS 1.3 and presents no
// certificate: it trusts the certificates in caFile and no other root. It is
// the client of a caller that holds only a bearer token (WithBearerToken). The
// client sets no Timeout, so it is fit for a watch.
func NewServerTLSClient(caFile string) (*http.Client, error) {
	roots, err := loadRoots(caFile)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots},
		ForceAttemptHTTP2: true,
	}}, nil
}

func loadRoots(caFile string) (*x509.CertPool, error) {
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("tls ca: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("tls ca %s: no certificate found", caFile)
	}
	return roots, nil
}

// TLSFiles holds the paths of the three files of a client's mutual TLS
// identity. The zero value means plain HTTP.
type TLSFiles struct {
	CA, Cert, Key string
}

// RegisterTLSFlags registers --tls-ca, --tls-cert and --tls-key on fs and
// returns the files they fill.
func RegisterTLSFlags(fs *flag.FlagSet) *TLSFiles {
	f := &TLSFiles{}
	fs.StringVar(&f.CA, "tls-ca", "", "file of the CA certificates the server's certificate must chain to")
	fs.StringVar(&f.Cert, "tls-cert", "", "file of the client certificate presented to the server")
	fs.StringVar(&f.Key, "tls-key", "", "file of the key of --tls-cert")
	return f
}

// Client returns the http.Client the files describe: a mutual TLS client when
// all three are set, a plain one when none is, and an error naming the unset
// flags when only some are.
func (f *TLSFiles) Client() (*http.Client, error) {
	var unset []string
	for _, p := range []struct{ flag, path string }{
		{"--tls-ca", f.CA}, {"--tls-cert", f.Cert}, {"--tls-key", f.Key},
	} {
		if p.path == "" {
			unset = append(unset, p.flag)
		}
	}
	switch len(unset) {
	case 0:
		return NewTLSClient(f.CA, f.Cert, f.Key)
	case 3:
		return &http.Client{}, nil
	default:
		return nil, errors.New("tls: give all of --tls-ca, --tls-cert and --tls-key or none; missing " + strings.Join(unset, ", "))
	}
}
