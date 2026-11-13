package csrsigner

import (
	"crypto/x509"
	"crypto/x509/pkix"
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
	oidServerAuth  = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 1}
	oidCommonName  = asn1.ObjectIdentifier{2, 5, 4, 3}
	oidOrg         = asn1.ObjectIdentifier{2, 5, 4, 10}
)

// denial is a failed rule: the reason and message of the Denied condition.
type denial struct {
	reason, message string
}

// approve applies the approval rule to r in order and returns the parsed
// request and the extended key usage to sign, or the first failure.
func approve(r *api.CertificateSigningRequest) (*x509.CertificateRequest, []x509.ExtKeyUsage, *denial) {
	block, _ := pem.Decode([]byte(r.Spec.Request))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, nil, &denial{reasonInvalidRequest, "spec.request is not a PEM CERTIFICATE REQUEST"}
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, nil, &denial{reasonInvalidRequest, fmt.Sprintf("spec.request does not parse: %v", err)}
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, nil, &denial{reasonInvalidRequest, fmt.Sprintf("spec.request signature does not verify: %v", err)}
	}

	// A certificate's Organization values are its groups, so the subject is
	// one CN and the one Organization that marks a Node.
	cn, org, ok := nodeSubject(csr.Subject.Names)
	if !ok {
		return nil, nil, &denial{reasonInvalidSubject, fmt.Sprintf("subject has %d attributes: it must be exactly one CN and one O=%s", len(csr.Subject.Names), api.NodeGroup)}
	}
	if org != api.NodeGroup {
		return nil, nil, &denial{reasonInvalidSubject, fmt.Sprintf("subject O %q is not %s", org, api.NodeGroup)}
	}
	if cn == "" || strings.HasPrefix(cn, usernamePrefix) {
		return nil, nil, &denial{reasonInvalidSubject, fmt.Sprintf("subject CN %q is not a Node name", cn)}
	}

	usages := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	for _, ext := range csr.Extensions {
		u, ok := allowedExtKeyUsage(ext.Id, ext.Value)
		if !ok {
			return nil, nil, &denial{reasonDisallowedExt, fmt.Sprintf("extension %s is not allowed: only an extended key usage of client auth, or of client auth and server auth, is", ext.Id)}
		}
		usages = u
	}

	if !strings.HasPrefix(r.Spec.Username, usernamePrefix) && r.Spec.Username != cn {
		return nil, nil, &denial{reasonUnauthorized, fmt.Sprintf("username %q is neither a bootstrap token nor the CN %q", r.Spec.Username, cn)}
	}
	return csr, usages, nil
}

// allowedExtKeyUsage returns the usage of an extended key usage extension that
// holds exactly client auth, or exactly client auth and server auth.
func allowedExtKeyUsage(id asn1.ObjectIdentifier, value []byte) ([]x509.ExtKeyUsage, bool) {
	if !id.Equal(oidExtKeyUsage) {
		return nil, false
	}
	var oids []asn1.ObjectIdentifier
	rest, err := asn1.Unmarshal(value, &oids)
	if err != nil || len(rest) != 0 {
		return nil, false
	}
	equal := func(want ...asn1.ObjectIdentifier) bool {
		return slices.EqualFunc(oids, want, asn1.ObjectIdentifier.Equal)
	}
	switch {
	case equal(oidClientAuth):
		return []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, true
	case equal(oidClientAuth, oidServerAuth):
		return []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, true
	}
	return nil, false
}

// nodeSubject returns the CN and the Organization of a subject made of exactly
// one of each and nothing else, in either order.
func nodeSubject(names []pkix.AttributeTypeAndValue) (cn, org string, ok bool) {
	var haveCN, haveOrg bool
	for _, n := range names {
		v, isString := n.Value.(string)
		switch {
		case !isString:
			return "", "", false
		case n.Type.Equal(oidCommonName) && !haveCN:
			cn, haveCN = v, true
		case n.Type.Equal(oidOrg) && !haveOrg:
			org, haveOrg = v, true
		default:
			return "", "", false
		}
	}
	return cn, org, haveCN && haveOrg
}
