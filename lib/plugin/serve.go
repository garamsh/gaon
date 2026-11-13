package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/garamsh/gaon/lib/api"
	pb "github.com/garamsh/gaon/lib/plugin/v1alpha1"
)

// ServeNetwork serves n as the network plugin called name on its socket in dir,
// until ctx ends. It returns an error when the socket cannot be made, or
// another process already serves it.
func ServeNetwork(ctx context.Context, dir, name string, n Network) error {
	return serve(ctx, dir, KindNetwork, name, func(g *grpc.Server) {
		pb.RegisterNetworkServiceServer(g, networkServer{n: n})
	})
}

// ServeStorage serves s as the storage plugin called name on its socket in
// dir, until ctx ends. It returns an error when the socket cannot be made, or
// another process already serves it.
func ServeStorage(ctx context.Context, dir, name string, s Storage) error {
	return serve(ctx, dir, KindStorage, name, func(g *grpc.Server) {
		pb.RegisterStorageServiceServer(g, storageServer{s: s})
	})
}

func serve(ctx context.Context, dir string, kind Kind, name string, register func(*grpc.Server)) error {
	path, err := SocketPath(dir, kind, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("make plugin directory: %w", err)
	}
	if err := clearStale(path); err != nil {
		return err
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = lis.Close()
		return fmt.Errorf("restrict %s: %w", path, err)
	}
	g := grpc.NewServer()
	register(g)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			g.Stop()
		case <-done:
		}
	}()
	err = g.Serve(lis)
	close(done)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// clearStale removes the socket a stopped plugin left, and refuses to take
// the place of one that still answers.
func clearStale(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	c, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		_ = c.Close()
		return fmt.Errorf("%s is served by another process", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}

func decode(raw []byte) (*api.MicroVM, error) {
	var m api.MicroVM
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode microvm: %v", err)
	}
	return &m, nil
}

type networkServer struct {
	pb.UnimplementedNetworkServiceServer
	n Network
}

func (s networkServer) Policy(ctx context.Context, req *pb.PolicyRequest) (*pb.PolicyResponse, error) {
	m, err := decode(req.GetMicrovm())
	if err != nil {
		return nil, err
	}
	p, err := s.n.Policy(ctx, m)
	var r *Refusal
	if errors.As(err, &r) {
		return &pb.PolicyResponse{Refusal: refusalToWire(r)}, nil
	}
	if err != nil {
		return nil, status.Error(codes.Unknown, err.Error())
	}
	return &pb.PolicyResponse{Policy: policyToWire(p)}, nil
}

func (s networkServer) Enforce(ctx context.Context, req *pb.EnforceRequest) (*pb.EnforceResponse, error) {
	e, ok := s.n.(Enforcer)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "this network plugin does not enforce NetworkPolicy")
	}
	if err := e.Enforce(ctx, limitsFromWire(req.GetMicrovms())); err != nil {
		return nil, status.Error(codes.Unknown, err.Error())
	}
	return &pb.EnforceResponse{}, nil
}

func (s networkServer) Expose(ctx context.Context, req *pb.ExposeRequest) (*pb.ExposeResponse, error) {
	e, ok := s.n.(Exposer)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "this network plugin does not serve Services")
	}
	if err := e.Expose(ctx, routesFromWire(req.GetRoutes()), req.GetAnnounce()); err != nil {
		return nil, status.Error(codes.Unknown, err.Error())
	}
	return &pb.ExposeResponse{}, nil
}

type storageServer struct {
	pb.UnimplementedStorageServiceServer
	s Storage
}

func (s storageServer) Attach(ctx context.Context, req *pb.AttachRequest) (*pb.AttachResponse, error) {
	m, err := decode(req.GetMicrovm())
	if err != nil {
		return nil, err
	}
	disks, err := s.s.Attach(ctx, m)
	var r *Refusal
	if errors.As(err, &r) {
		return &pb.AttachResponse{Refusal: refusalToWire(r)}, nil
	}
	if err != nil {
		return nil, status.Error(codes.Unknown, err.Error())
	}
	out := &pb.AttachResponse{}
	for _, d := range disks {
		out.Disks = append(out.Disks, &pb.DiskAttachment{DiskId: d.DiskID, ReadOnly: d.ReadOnly, MountPath: d.MountPath})
	}
	return out, nil
}

func (s storageServer) Release(ctx context.Context, req *pb.ReleaseRequest) (*pb.ReleaseResponse, error) {
	if err := s.s.Release(ctx, req.GetMachineId()); err != nil {
		return nil, status.Error(codes.Unknown, err.Error())
	}
	return &pb.ReleaseResponse{}, nil
}
