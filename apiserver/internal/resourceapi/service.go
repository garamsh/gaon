// Package resourceapi holds the rules of the v1alpha1 resource API over a
// Store: who may write which section of an object, and what a write changes.
package resourceapi

import (
	"context"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// Service is the resource API of resource-api.md §Paths. A failure the
// caller caused is an *api.APIError; any other error is the server's.
type Service interface {
	PutMachine(ctx context.Context, name string, m *api.Machine) (*api.Machine, error)
	BindMachine(ctx context.Context, name string, b api.Binding) (*api.Machine, error)
	PutMachineStatus(ctx context.Context, name string, m *api.Machine) (*api.Machine, error)
	DeleteMachine(ctx context.Context, name string, rv api.ResourceVersion) (*api.Machine, error)
	GetMachine(ctx context.Context, name string) (*api.Machine, error)
	ListMachines(ctx context.Context) (*api.MachineList, error)
	WatchMachines(ctx context.Context, rv api.ResourceVersion) (<-chan api.WatchEvent, error)

	PutVolumeStatus(ctx context.Context, name string, v *api.Volume) (*api.Volume, error)

	PutNode(ctx context.Context, name string, n *api.Node) (*api.Node, error)
	PutNodeStatus(ctx context.Context, name string, n *api.Node) (*api.Node, error)
	GetNode(ctx context.Context, name string) (*api.Node, error)
	ListNodes(ctx context.Context) (*api.NodeList, error)
	WatchNodes(ctx context.Context, rv api.ResourceVersion) (<-chan api.WatchEvent, error)

	PutLease(ctx context.Context, name string, l *api.Lease) (*api.Lease, error)
	GetLease(ctx context.Context, name string) (*api.Lease, error)
	ListLeases(ctx context.Context) (*api.LeaseList, error)
	WatchLeases(ctx context.Context, rv api.ResourceVersion) (<-chan api.WatchEvent, error)

	PutCertificateSigningRequest(ctx context.Context, name, username string, r *api.CertificateSigningRequest) (*api.CertificateSigningRequest, error)
	PutCertificateSigningRequestStatus(ctx context.Context, name string, r *api.CertificateSigningRequest) (*api.CertificateSigningRequest, error)
	GetCertificateSigningRequest(ctx context.Context, name string) (*api.CertificateSigningRequest, error)
	ListCertificateSigningRequests(ctx context.Context) (*api.CertificateSigningRequestList, error)
	WatchCertificateSigningRequests(ctx context.Context, rv api.ResourceVersion) (<-chan api.WatchEvent, error)
}

var (
	machines = kind[api.Machine]{"machines", func(m *api.Machine) *api.ObjectMeta { return &m.Metadata }, func(m *api.Machine) any { return m.Spec }}
	volumes  = kind[api.Volume]{"volumes", func(v *api.Volume) *api.ObjectMeta { return &v.Metadata }, func(v *api.Volume) any { return v.Spec }}
	nodes    = kind[api.Node]{"nodes", func(n *api.Node) *api.ObjectMeta { return &n.Metadata }, func(n *api.Node) any { return n.Spec }}
	leases   = kind[api.Lease]{"leases", func(l *api.Lease) *api.ObjectMeta { return &l.Metadata }, func(l *api.Lease) any { return l.Spec }}

	certificateSigningRequests = kind[api.CertificateSigningRequest]{"certificatesigningrequests", func(r *api.CertificateSigningRequest) *api.ObjectMeta { return &r.Metadata }, func(r *api.CertificateSigningRequest) any { return r.Spec }}
)

type service struct {
	st  Store
	now func() time.Time
}

// NewService returns the Service over st. now is the clock that stamps a
// deletion.
func NewService(st Store, now func() time.Time) Service {
	return &service{st: st, now: now}
}

func (s *service) GetMachine(ctx context.Context, name string) (*api.Machine, error) {
	m, _, err := machines.get(ctx, s.st, name)
	return m, err
}

func (s *service) ListMachines(ctx context.Context) (*api.MachineList, error) {
	items, rv, err := machines.list(ctx, s.st)
	if err != nil {
		return nil, err
	}
	return &api.MachineList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMachineList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchMachines(ctx context.Context, rv api.ResourceVersion) (<-chan api.WatchEvent, error) {
	return machines.watch(ctx, s.st, rv)
}

func (s *service) GetNode(ctx context.Context, name string) (*api.Node, error) {
	n, _, err := nodes.get(ctx, s.st, name)
	return n, err
}

func (s *service) ListNodes(ctx context.Context) (*api.NodeList, error) {
	items, rv, err := nodes.list(ctx, s.st)
	if err != nil {
		return nil, err
	}
	return &api.NodeList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNodeList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchNodes(ctx context.Context, rv api.ResourceVersion) (<-chan api.WatchEvent, error) {
	return nodes.watch(ctx, s.st, rv)
}

func (s *service) GetLease(ctx context.Context, name string) (*api.Lease, error) {
	l, _, err := leases.get(ctx, s.st, name)
	return l, err
}

func (s *service) ListLeases(ctx context.Context) (*api.LeaseList, error) {
	items, rv, err := leases.list(ctx, s.st)
	if err != nil {
		return nil, err
	}
	return &api.LeaseList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindLeaseList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchLeases(ctx context.Context, rv api.ResourceVersion) (<-chan api.WatchEvent, error) {
	return leases.watch(ctx, s.st, rv)
}

func (s *service) GetCertificateSigningRequest(ctx context.Context, name string) (*api.CertificateSigningRequest, error) {
	r, _, err := certificateSigningRequests.get(ctx, s.st, name)
	return r, err
}

func (s *service) ListCertificateSigningRequests(ctx context.Context) (*api.CertificateSigningRequestList, error) {
	items, rv, err := certificateSigningRequests.list(ctx, s.st)
	if err != nil {
		return nil, err
	}
	return &api.CertificateSigningRequestList{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindCertificateSigningRequestList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchCertificateSigningRequests(ctx context.Context, rv api.ResourceVersion) (<-chan api.WatchEvent, error) {
	return certificateSigningRequests.watch(ctx, s.st, rv)
}
