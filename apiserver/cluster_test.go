package apiserver_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// rbacAPIRoot is the root of the RBAC group, which the server is documented to
// serve.
const rbacAPIRoot = "/apis/rbac.gaon.garam.sh/v1alpha1"

// who is the subject of a client certificate: its CN and its Organization
// values, as deployment.md §Certificates names them.
type who struct {
	cn   string
	orgs []string
}

var (
	schedulerID   = who{"gaon-scheduler", []string{"gaon:schedulers"}}
	controllersID = who{"gaon-controllers", []string{"gaon:controllers"}}
	adminID       = who{"gaon-admin", []string{"gaon:admins"}}
	strangerID    = who{"stranger", []string{"gaon:strangers"}}
)

// nodeID is a Node Agent's certificate: the Node's name and the Organization
// gaon:nodes, as the signer issues it (ADR 0022).
func nodeID(name string) who { return who{cn: name, orgs: []string{api.NodeGroup}} }

// issueFor signs a client certificate whose subject is w, and returns it with
// its key, both PEM.
func (a *authority) issueFor(t *testing.T, w who) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: w.cn, Organization: w.orgs},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// cluster is one embedded etcd member, the CAs of a deployment and the TLS
// configuration of an API server over them. An API server can be started and
// stopped over it more than once.
type cluster struct {
	etcd               *clientv3.Client
	serverCA, clientCA *authority
	caFile             string
	cfg                apiserver.Config
}

func newCluster(t *testing.T) *cluster {
	t.Helper()
	endpoint := startEtcd(t)
	etcd, err := clientv3.New(clientv3.Config{Endpoints: []string{endpoint}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { _ = etcd.Close() })
	c := &cluster{etcd: etcd, serverCA: newAuthority(t, "server ca"), clientCA: newAuthority(t, "client ca")}
	certPEM, keyPEM := c.serverCA.issue(t, "gaon-apiserver")
	c.caFile = writeFile(t, "server-ca.pem", c.serverCA.pem)
	c.cfg = apiserver.Config{
		Endpoints:   []string{endpoint},
		DialTimeout: 5 * time.Second,
		TLSClientCA: writeFile(t, "client-ca.pem", c.clientCA.pem),
		TLSCert:     writeFile(t, "server.pem", certPEM),
		TLSKey:      writeFile(t, "server-key.pem", keyPEM),
	}
	return c
}

// start runs the API server in mode and returns its address and a function
// that stops it and returns what Run returned.
func (c *cluster) start(t *testing.T, mode string) (addr string, stop func() error) {
	t.Helper()
	cfg := c.cfg
	cfg.AuthorizationMode = mode
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- apiserver.Run(ctx, cfg, ln, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	stopped := false
	stop = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		return <-done
	}
	t.Cleanup(func() { _ = stop() })
	return ln.Addr().String(), stop
}

// clients returns the client of the caller whose certificate has the subject
// w, for the compute group and for the RBAC group.
func (c *cluster) clients(t *testing.T, addr string, w who) (compute, rbac *api.Client) {
	t.Helper()
	certPEM, keyPEM := c.clientCA.issueFor(t, w)
	hc, err := api.NewTLSClient(c.caFile, writeFile(t, "cert.pem", certPEM), writeFile(t, "key.pem", keyPEM))
	require.NoError(t, err)
	hc.Timeout = 10 * time.Second
	return api.NewClient("https://"+addr+apiRoot, hc), api.NewClient("https://"+addr+rbacAPIRoot, hc)
}

func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func rule(group, resource string, verbs ...api.Verb) api.PolicyRule {
	return api.PolicyRule{APIGroups: []string{group}, Resources: []string{resource}, Verbs: verbs}
}

func clusterRole(name string, rules ...api.PolicyRule) *api.ClusterRole {
	return &api.ClusterRole{
		TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRole},
		Metadata: api.ObjectMeta{Name: name},
		Rules:    rules,
	}
}

func clusterRoleBinding(name, role string, subjects ...api.Subject) *api.ClusterRoleBinding {
	return &api.ClusterRoleBinding{
		TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRoleBinding},
		Metadata: api.ObjectMeta{Name: name},
		RoleRef:  api.RoleRef{Kind: api.KindClusterRole, Name: role},
		Subjects: subjects,
	}
}

func userSubject(name string) api.Subject  { return api.Subject{Kind: api.SubjectUser, Name: name} }
func groupSubject(name string) api.Subject { return api.Subject{Kind: api.SubjectGroup, Name: name} }
