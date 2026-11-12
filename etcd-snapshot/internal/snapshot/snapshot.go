// Package snapshot takes a consistent snapshot of the etcd cluster, checks it
// against its own integrity hash and puts it in a bucket.
package snapshot

import (
	"crypto/sha256"
	"io"
	"strconv"
	"time"
)

// KeyTimeLayout is the layout of the UTC time in an object's key.
const KeyTimeLayout = "20060102T150405Z"

// Revision is the revision of the etcd store a snapshot holds.
type Revision int64

// Object is a snapshot ready to put: Body holds Size bytes and SHA256 is their
// digest.
type Object struct {
	Key    string
	Body   io.ReadSeeker
	Size   int64
	SHA256 [sha256.Size]byte
}

// Key returns the key of the snapshot taken at t with revision r under prefix:
// the prefix, the UTC time, "-rev", the revision and ".db".
func Key(prefix string, t time.Time, r Revision) string {
	return prefix + t.UTC().Format(KeyTimeLayout) + "-rev" + strconv.FormatInt(int64(r), 10) + ".db"
}
