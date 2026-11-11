// Package apiserver serves the v1alpha1 resource API of gaon over etcd.
package apiserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/garamsh/gaon/apiserver/internal/resourceapi"
	"github.com/garamsh/gaon/apiserver/internal/resourceapi/etcdstore"
)

const shutdownTimeout = 5 * time.Second

// Run serves the API on ln over the etcd cell of cfg until ctx ends. It
// refuses a listener that is not on a loopback address, because the API has
// no authentication yet.
func Run(ctx context.Context, cfg Config, ln net.Listener, log *slog.Logger) error {
	if addr, ok := ln.Addr().(*net.TCPAddr); !ok || !addr.IP.IsLoopback() {
		return fmt.Errorf("listener %s is not a loopback address", ln.Addr())
	}
	st, err := etcdstore.New(ctx, cfg.Endpoints, cfg.DialTimeout)
	if err != nil {
		return err
	}
	defer st.Close()
	srv := &http.Server{
		Handler:           newHandler(resourceapi.NewService(st, time.Now), log),
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 10 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(stop); err != nil {
		return err
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
