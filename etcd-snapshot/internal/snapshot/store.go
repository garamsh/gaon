package snapshot

import "context"

// Store keeps snapshots outside the cluster.
type Store interface {
	// Put stores o under o.Key, which the store accepts only when the object's
	// bytes match o.SHA256. A Put that fails leaves no object.
	Put(ctx context.Context, o Object) error
}
