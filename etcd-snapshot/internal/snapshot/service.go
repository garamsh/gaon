package snapshot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// Config configures the Service.
type Config struct {
	// Dir holds the directory the snapshot is written to; empty is the
	// system's temporary directory.
	Dir string
	// Prefix starts every key.
	Prefix string
}

// Service takes snapshots.
type Service interface {
	// Run takes one snapshot, checks it against its integrity hash and puts it
	// in the Store under Key. It returns an error when any step fails, and puts
	// nothing when the check fails.
	Run(ctx context.Context) error
}

type service struct {
	source Source
	store  Store
	clock  Clock
	cfg    Config
	log    *slog.Logger
}

// NewService returns a Service.
func NewService(source Source, store Store, clock Clock, cfg Config, log *slog.Logger) Service {
	return &service{source: source, store: store, clock: clock, cfg: cfg, log: log}
}

func (s *service) Run(ctx context.Context) error {
	dir, err := os.MkdirTemp(s.cfg.Dir, "gaon-etcd-snapshot-")
	if err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "snapshot.db")

	if err := s.source.Take(ctx, path); err != nil {
		return fmt.Errorf("take snapshot: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer func() { _ = f.Close() }()
	size, err := checkHash(f)
	if err != nil {
		return fmt.Errorf("check snapshot: %w", err)
	}
	rev, err := s.source.Revision(path)
	if err != nil {
		return fmt.Errorf("read snapshot revision: %w", err)
	}
	sum, err := sha256Of(f)
	if err != nil {
		return fmt.Errorf("sum snapshot: %w", err)
	}

	o := Object{Key: Key(s.cfg.Prefix, s.clock.Now(), rev), Body: f, Size: size, SHA256: sum}
	if err := s.store.Put(ctx, o); err != nil {
		return fmt.Errorf("put snapshot: %w", err)
	}
	s.log.Info("snapshot stored", "key", o.Key, "revision", int64(rev), "bytes", size)
	return nil
}
