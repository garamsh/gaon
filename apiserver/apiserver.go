// Package apiserver serves the v1alpha1 resource API of gaon over etcd.
package apiserver

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/garamsh/gaon/apiserver/internal/authorization"
	"github.com/garamsh/gaon/apiserver/internal/resourceapi"
	"github.com/garamsh/gaon/apiserver/internal/resourceapi/etcdstore"
	"github.com/garamsh/gaon/apiserver/internal/token"
	"github.com/garamsh/gaon/lib/api"
)

const shutdownTimeout = 5 * time.Second

// Run serves the API on ln over the etcd cell of cfg until ctx ends. It
// serves TLS where cfg names the TLS files, and without them refuses a
// listener that is not on a loopback address.
func Run(ctx context.Context, cfg Config, ln net.Listener, log *slog.Logger) error {
	tlsCfg, err := cfg.serverTLS()
	if err != nil {
		return err
	}
	tokens, err := cfg.bootstrapTokens()
	if err != nil {
		return err
	}
	if err := cfg.validateAuthorization(); err != nil {
		return err
	}
	if err := cfg.validateTokenSigning(); err != nil {
		return err
	}
	var signer resourceapi.TokenSigner
	var verifier *token.Verifier
	if cfg.ServiceAccountSigningKeyFile != "" {
		sg, err := token.LoadSigner(cfg.ServiceAccountIssuer, cfg.ServiceAccountSigningKeyFile)
		if err != nil {
			return err
		}
		signer = sg
		rotated, err := token.LoadPublicKeys(cfg.ServiceAccountVerifyKeyFiles)
		if err != nil {
			return err
		}
		if verifier, err = token.NewVerifier(cfg.ServiceAccountIssuer, append([]*ecdsa.PublicKey{sg.Public()}, rotated...)...); err != nil {
			return err
		}
	}
	if addr, ok := ln.Addr().(*net.TCPAddr); tlsCfg == nil && (!ok || !addr.IP.IsLoopback()) {
		return fmt.Errorf("listener %s is not a loopback address and no TLS files are given", ln.Addr())
	}
	ranges, err := cfg.ranges()
	if err != nil {
		return err
	}
	etcdTLS, err := cfg.etcdTLS()
	if err != nil {
		return err
	}
	st, err := etcdstore.New(ctx, cfg.Endpoints, etcdTLS, etcdstore.Options{
		DialTimeout:      cfg.DialTimeout,
		KeepAliveTime:    cfg.EtcdKeepAliveTime,
		KeepAliveTimeout: cfg.EtcdKeepAliveTimeout,
	})
	if err != nil {
		return err
	}
	defer st.Close()
	svc := resourceapi.NewService(st, time.Now, signer, ranges)
	if err := resourceapi.EnsureLocalStorageClass(ctx, svc); err != nil {
		return fmt.Errorf("create storageclass %s: %w", api.StorageClassLocal, err)
	}
	az := authorization.AlwaysAllow()
	if cfg.AuthorizationMode == ModeRBAC {
		if err := authorization.Bootstrap(ctx, svc); err != nil {
			return err
		}
		az = authorization.NewRBAC(svc)
	}
	var accounts *accountTokens
	if verifier != nil {
		audiences := cfg.APIAudiences
		if len(audiences) == 0 {
			audiences = []string{cfg.ServiceAccountIssuer}
		}
		accounts = &accountTokens{verifier: verifier, audiences: audiences, microvm: svc.GetMicroVM, now: time.Now}
	}
	srv := &http.Server{
		Handler:           newHandler(svc, az, tokens, accounts, log),
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         tlsCfg,
	}
	done := make(chan error, 1)
	go func() {
		if tlsCfg == nil {
			done <- srv.Serve(ln)
			return
		}
		done <- srv.ServeTLS(ln, "", "")
	}()
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
