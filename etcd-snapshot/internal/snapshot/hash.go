package snapshot

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
)

// checkHash checks the snapshot in f against the SHA-256 hash etcd appends to
// the database, and returns f's size. Where f holds no such hash it is an
// error: a snapshot that cannot be checked is not stored.
func checkHash(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	// etcd's database is a multiple of 512 bytes, so that a size 32 above one
	// is how a reader tells a snapshot that carries the hash.
	if size%512 != sha256.Size {
		return 0, fmt.Errorf("no sha256 hash after the database: %d bytes", size)
	}

	h := sha256.New()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	if _, err := io.CopyN(h, f, size-sha256.Size); err != nil {
		return 0, err
	}
	trailer := make([]byte, sha256.Size)
	if _, err := io.ReadFull(f, trailer); err != nil {
		return 0, err
	}
	if !bytes.Equal(h.Sum(nil), trailer) {
		return 0, errors.New("the sha256 hash does not match the database")
	}
	return size, nil
}

// sha256Of returns the SHA-256 of all of f, the digest the store verifies, and
// leaves f at its start.
func sha256Of(f *os.File) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return sum, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return sum, err
	}
	return sum, nil
}
