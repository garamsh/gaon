package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAWS stands in for IAM Roles Anywhere and S3 on one local address. It
// stores what S3 would store and refuses what S3 would refuse; it does not
// check request signatures.
type fakeAWS struct {
	*httptest.Server

	// anchor is the CA a Roles Anywhere trust anchor holds.
	anchor *authority
	// accessKeyID is the key the issued credentials carry, which S3 requests
	// must be signed with.
	accessKeyID string

	mu      sync.Mutex
	objects map[string][]byte
	// checksums are the x-amz-checksum-sha256 headers by object key.
	checksums map[string]string
	// sessionCN is the CN of the certificate that asked for each session.
	sessionCN []string
	// denyPut makes S3 refuse every upload.
	denyPut bool
}

func newFakeAWS(t *testing.T, anchor *authority) *fakeAWS {
	t.Helper()
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	f := &fakeAWS{
		anchor:      anchor,
		accessKeyID: "ASIA" + strings.ToUpper(hex.EncodeToString(id)),
		objects:     map[string][]byte{},
		checksums:   map[string]string{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAWS) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/sessions" {
		f.createSession(w, r)
		return
	}
	f.putObject(w, r)
}

// createSession answers a CreateSession request signed by a certificate of the
// trust anchor's CA.
func (f *fakeAWS) createSession(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-X509-ECDSA-SHA256 ") {
		http.Error(w, `{"message":"not a SigV4-X509 request"}`, http.StatusForbidden)
		return
	}
	der, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Amz-X509"))
	if err != nil {
		http.Error(w, `{"message":"no certificate"}`, http.StatusForbidden)
		return
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		http.Error(w, `{"message":"bad certificate"}`, http.StatusForbidden)
		return
	}
	pool := x509.NewCertPool()
	pool.AddCert(f.anchor.cert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		http.Error(w, `{"message":"certificate not signed by the trust anchor"}`, http.StatusForbidden)
		return
	}
	f.mu.Lock()
	f.sessionCN = append(f.sessionCN, cert.Subject.CommonName)
	f.mu.Unlock()

	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"credentialSet": []map[string]any{{
			"credentials": map[string]any{
				"accessKeyId":     f.accessKeyID,
				"secretAccessKey": hex.EncodeToString(secret),
				"sessionToken":    hex.EncodeToString(secret),
				"expiration":      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			},
		}},
	})
}

// putObject takes a path-style PutObject: it refuses a request not signed with
// the issued access key, and bytes whose SHA-256 differs from the checksum
// header, as S3 does.
func (f *fakeAWS) putObject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
		return
	}
	if !strings.Contains(r.Header.Get("Authorization"), "Credential="+f.accessKeyID+"/") {
		s3Error(w, http.StatusForbidden, "InvalidAccessKeyId")
		return
	}
	f.mu.Lock()
	deny := f.denyPut
	f.mu.Unlock()
	if deny {
		s3Error(w, http.StatusForbidden, "AccessDenied")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s3Error(w, http.StatusBadRequest, "IncompleteBody")
		return
	}
	want := r.Header.Get("X-Amz-Checksum-Sha256")
	sum := sha256.Sum256(body)
	if want == "" || want != base64.StdEncoding.EncodeToString(sum[:]) {
		s3Error(w, http.StatusBadRequest, "BadDigest")
		return
	}
	f.mu.Lock()
	key := strings.TrimPrefix(r.URL.Path, "/")
	f.objects[key], f.checksums[key] = body, want
	f.mu.Unlock()
	w.Header().Set("X-Amz-Checksum-Sha256", want)
	w.WriteHeader(http.StatusOK)
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
}

// object returns what is stored under key, bucket included.
func (f *fakeAWS) object(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	return bytes.Clone(b), ok
}

// keys lists the keys stored.
func (f *fakeAWS) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.objects {
		out = append(out, k)
	}
	return out
}
