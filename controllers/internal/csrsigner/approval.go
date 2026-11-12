package csrsigner

import (
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"slices"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

var (
	oidExtKeyUsage = asn1.ObjectIdentifier{2, 5, 29, 37}
	oidClientAuth  = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 2}
	oidCommonName  = asn1.ObjectIdentifier{2, 5, 4, 3}
)

// denial is a failed rule: the reason and message of the Denied condition.
type denial struct {
	reason, message string
}

// approve applies the approval rule to r in order and returns the parsed
// request, or the first failure.
func approve(r *api.CertificateSigningRequest) (*x509.CertificateRequest, *denial) {
	block, _ := pem.Decode([]byte(r.Spec.Request))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, &denial{reasonInvalidRequest, "spec.request is not a PEM CERTIFICATE REQUEST"}
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, &denial{reasonInvalidRequest, fmt.Sprintf("spec.request does not parse: %v", err)}
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, &denial{reasonInvalidRequest, fmt.Sprintf("spec.request signature does not verify: %v", err)}
	}

	// A certificate's Organization values are its groups, so the subject is
	// one CN and nothing else.
	if names := csr.Subject.Names; len(names) != 1 || !names[0].Type.Equal(oidCommonName) {
		return nil, &denial{reasonInvalidSubject, fmt.Sprintf("subject has %d attributes: it must be exactly one CN", len(names))}
	}
	cn := csr.Subject.CommonName
	if cn == "" || strings.HasPrefix(cn, usernamePrefix) {
		return nil, &denial{reasonInvalidSubject, fmt.Sprintf("subject CN %q is not a Node name", cn)}
	}

	for _, ext := range csr.Extensions {
		if !clientAuthOnly(ext.Id, ext.Value) {
			return nil, &denial{reasonDisallowedExt, fmt.Sprintf("extension %s is not allowed: only an extended key usage of client auth is", ext.Id)}
		}
	}

	if !strings.HasPrefix(r.Spec.Username, usernamePrefix) && r.Spec.Username != cn {
		return nil, &denial{reasonUnauthorized, fmt.Sprintf("username %q is neither a bootstrap token nor the CN %q", r.Spec.Username, cn)}
	}
	return csr, nil
}

// clientAuthOnly reports whether the extension is an extended key usage that
// holds client auth and nothing else.
func clientAuthOnly(id asn1.ObjectIdentifier, value []byte) bool {
	if !id.Equal(oidExtKeyUsage) {
		return false
	}
	var usages []asn1.ObjectIdentifier
	rest, err := asn1.Unmarshal(value, &usages)
	return err == nil && len(rest) == 0 && slices.EqualFunc(usages, []asn1.ObjectIdentifier{oidClientAuth}, asn1.ObjectIdentifier.Equal)
}
