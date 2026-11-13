package plugin

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/garamsh/gaon/lib/api"
	pb "github.com/garamsh/gaon/lib/plugin/v1alpha1"
)

// NetworkClient calls the network plugin on a socket as a Network.
type NetworkClient struct {
	conn   *grpc.ClientConn
	client pb.NetworkServiceClient
}

// DialNetwork returns the client of the network plugin called name in dir. It
// does not connect: a plugin that is not running makes each call fail with an
// error that is not a *Refusal, and a call made after it starts succeeds.
func DialNetwork(dir, name string) (*NetworkClient, error) {
	conn, err := dial(dir, KindNetwork, name)
	if err != nil {
		return nil, err
	}
	return &NetworkClient{conn: conn, client: pb.NewNetworkServiceClient(conn)}, nil
}

// Close releases the connection.
func (c *NetworkClient) Close() error { return c.conn.Close() }

// Policy calls the plugin. A plugin that does not serve m returns a *Refusal.
func (c *NetworkClient) Policy(ctx context.Context, m *api.MicroVM) (*NetworkPolicy, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode microvm: %w", err)
	}
	resp, err := c.client.Policy(ctx, &pb.PolicyRequest{Microvm: raw})
	if err != nil {
		return nil, err
	}
	if resp.GetRefusal() != nil {
		return nil, refusalFromWire(resp.GetRefusal())
	}
	return policyFromWire(resp.GetPolicy()), nil
}

// Enforce calls the plugin as an Enforcer. A plugin that does not implement
// it answers gRPC Unimplemented.
func (c *NetworkClient) Enforce(ctx context.Context, limits []MicroVMLimits) error {
	_, err := c.client.Enforce(ctx, &pb.EnforceRequest{Microvms: limitsToWire(limits)})
	return err
}

// StorageClient calls the storage plugin on a socket as a Storage.
type StorageClient struct {
	conn   *grpc.ClientConn
	client pb.StorageServiceClient
}

// DialStorage returns the client of the storage plugin called name in dir. It
// does not connect, like DialNetwork.
func DialStorage(dir, name string) (*StorageClient, error) {
	conn, err := dial(dir, KindStorage, name)
	if err != nil {
		return nil, err
	}
	return &StorageClient{conn: conn, client: pb.NewStorageServiceClient(conn)}, nil
}

// Close releases the connection.
func (c *StorageClient) Close() error { return c.conn.Close() }

// Attach calls the plugin. A plugin that does not serve m returns a *Refusal.
func (c *StorageClient) Attach(ctx context.Context, m *api.MicroVM) ([]DiskAttachment, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode microvm: %w", err)
	}
	resp, err := c.client.Attach(ctx, &pb.AttachRequest{Microvm: raw})
	if err != nil {
		return nil, err
	}
	if resp.GetRefusal() != nil {
		return nil, refusalFromWire(resp.GetRefusal())
	}
	var disks []DiskAttachment
	for _, d := range resp.GetDisks() {
		disks = append(disks, DiskAttachment{DiskID: d.GetDiskId(), ReadOnly: d.GetReadOnly()})
	}
	return disks, nil
}

// Release calls the plugin.
func (c *StorageClient) Release(ctx context.Context, id string) error {
	_, err := c.client.Release(ctx, &pb.ReleaseRequest{MachineId: id})
	return err
}

func dial(dir string, kind Kind, name string) (*grpc.ClientConn, error) {
	path, err := SocketPath(dir, kind, name)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient("unix:"+path, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", path, err)
	}
	return conn, nil
}
