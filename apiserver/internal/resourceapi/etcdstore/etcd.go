// Package etcdstore is the etcd implementation of resourceapi.Store: an
// object's revision is the etcd revision it was last modified at.
package etcdstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	"github.com/garamsh/gaon/apiserver/internal/resourceapi"
	"github.com/garamsh/gaon/lib/api"
)

// Store is a resourceapi.Store over one etcd cluster.
type Store struct {
	cli *clientv3.Client
}

// New connects to the etcd endpoints and checks that one answers, each within
// timeout.
func New(ctx context.Context, endpoints []string, timeout time.Duration) (*Store, error) {
	cli, err := clientv3.New(clientv3.Config{Endpoints: endpoints, DialTimeout: timeout, Logger: zap.NewNop()})
	if err != nil {
		return nil, fmt.Errorf("connect to etcd: %w", err)
	}
	// One member answering is enough: the others may be down while the cluster
	// still has a quorum, and the client reaches the cluster through any member.
	var failures []error
	for _, endpoint := range endpoints {
		probe, cancel := context.WithTimeout(ctx, timeout)
		_, err := cli.Status(probe, endpoint)
		cancel()
		if err == nil {
			return &Store{cli: cli}, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", endpoint, err))
	}
	_ = cli.Close()
	return nil, fmt.Errorf("reach etcd: %w", errors.Join(failures...))
}

// Close closes the connection to etcd.
func (s *Store) Close() error {
	return s.cli.Close()
}

func (s *Store) Get(ctx context.Context, key string) (resourceapi.Entry, error) {
	resp, err := s.cli.Get(ctx, key)
	if err != nil {
		return resourceapi.Entry{}, fmt.Errorf("get %s: %w", key, err)
	}
	if len(resp.Kvs) == 0 {
		return resourceapi.Entry{}, resourceapi.ErrNotFound
	}
	return entry(resp.Kvs[0].Key, resp.Kvs[0].Value, resp.Kvs[0].ModRevision), nil
}

func (s *Store) List(ctx context.Context, prefix string) ([]resourceapi.Entry, int64, error) {
	resp, err := s.cli.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, 0, fmt.Errorf("list %s: %w", prefix, err)
	}
	entries := make([]resourceapi.Entry, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		entries = append(entries, entry(kv.Key, kv.Value, kv.ModRevision))
	}
	return entries, resp.Header.Revision, nil
}

func (s *Store) Create(ctx context.Context, key string, value []byte) (int64, error) {
	resp, err := s.cli.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, string(value))).
		Commit()
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", key, err)
	}
	if !resp.Succeeded {
		return 0, resourceapi.ErrExists
	}
	return resp.Header.Revision, nil
}

func (s *Store) Update(ctx context.Context, key string, value []byte, revision int64) (int64, error) {
	resp, err := s.cli.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(key), "=", revision)).
		Then(clientv3.OpPut(key, string(value))).
		Else(clientv3.OpGet(key, clientv3.WithKeysOnly())).
		Commit()
	if err != nil {
		return 0, fmt.Errorf("update %s: %w", key, err)
	}
	if !resp.Succeeded {
		return 0, missingOrStale(resp)
	}
	return resp.Header.Revision, nil
}

func (s *Store) Delete(ctx context.Context, key string, revision int64) (resourceapi.Entry, error) {
	resp, err := s.cli.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(key), "=", revision)).
		Then(clientv3.OpDelete(key, clientv3.WithPrevKV())).
		Else(clientv3.OpGet(key, clientv3.WithKeysOnly())).
		Commit()
	if err != nil {
		return resourceapi.Entry{}, fmt.Errorf("delete %s: %w", key, err)
	}
	if !resp.Succeeded {
		return resourceapi.Entry{}, missingOrStale(resp)
	}
	prev := resp.Responses[0].GetResponseDeleteRange().PrevKvs[0]
	return entry(prev.Key, prev.Value, resp.Header.Revision), nil
}

func (s *Store) Transact(ctx context.Context, writes []resourceapi.Write) (int64, error) {
	var conds []clientv3.Cmp
	var ops []clientv3.Op
	for _, w := range writes {
		conds = append(conds, clientv3.Compare(clientv3.ModRevision(w.Key), "=", w.Revision))
		if w.Value != nil {
			ops = append(ops, clientv3.OpPut(w.Key, string(w.Value)))
		}
	}
	resp, err := s.cli.Txn(ctx).If(conds...).Then(ops...).Commit()
	if err != nil {
		return 0, fmt.Errorf("transact over %d keys: %w", len(writes), err)
	}
	if !resp.Succeeded {
		return 0, resourceapi.ErrStale
	}
	return resp.Header.Revision, nil
}

func (s *Store) Watch(ctx context.Context, prefix string, revision int64) (<-chan resourceapi.Event, error) {
	in := s.cli.Watch(ctx, prefix, clientv3.WithPrefix(), clientv3.WithRev(revision+1), clientv3.WithPrevKV())
	out := make(chan resourceapi.Event)
	go func() {
		defer close(out)
		for resp := range in {
			if err := responseError(resp); err != nil {
				send(ctx, out, resourceapi.Event{Err: err})
				return
			}
			for _, ev := range resp.Events {
				converted, err := event(ev)
				if err != nil {
					send(ctx, out, resourceapi.Event{Err: err})
					return
				}
				if !send(ctx, out, converted) {
					return
				}
			}
		}
	}()
	return out, nil
}

func send(ctx context.Context, out chan<- resourceapi.Event, ev resourceapi.Event) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// responseError returns the failure a watch response reports. A compacted
// revision is reported as ErrCompacted.
func responseError(resp clientv3.WatchResponse) error {
	if resp.CompactRevision != 0 {
		return resourceapi.ErrCompacted
	}
	if err := resp.Err(); err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	return nil
}

func event(ev *clientv3.Event) (resourceapi.Event, error) {
	switch {
	case ev.Type == clientv3.EventTypeDelete:
		if ev.PrevKv == nil {
			return resourceapi.Event{}, errors.New("watch: deletion carries no previous value")
		}
		return resourceapi.Event{Type: api.EventDeleted, Entry: entry(ev.Kv.Key, ev.PrevKv.Value, ev.Kv.ModRevision)}, nil
	case ev.IsCreate():
		return resourceapi.Event{Type: api.EventAdded, Entry: entry(ev.Kv.Key, ev.Kv.Value, ev.Kv.ModRevision)}, nil
	default:
		modified := resourceapi.Event{Type: api.EventModified, Entry: entry(ev.Kv.Key, ev.Kv.Value, ev.Kv.ModRevision)}
		if ev.PrevKv != nil {
			modified.Previous = ev.PrevKv.Value
		}
		return modified, nil
	}
}

// missingOrStale reads the Else branch of a conditional write: no key is
// ErrNotFound, and a key at another revision is ErrStale.
func missingOrStale(resp *clientv3.TxnResponse) error {
	if len(resp.Responses[0].GetResponseRange().Kvs) == 0 {
		return resourceapi.ErrNotFound
	}
	return resourceapi.ErrStale
}

func entry(key, value []byte, revision int64) resourceapi.Entry {
	return resourceapi.Entry{Key: string(key), Value: value, Revision: revision}
}
