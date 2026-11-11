// Package nodeagent is gaon-node-agent: it registers the node, keeps its
// Lease and mvmd's fencing deadline renewed, and runs the Machines bound to
// it on mvmd.
package nodeagent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/liveness"
	"github.com/garamsh/gaon/node-agent/internal/machines"
	"github.com/garamsh/gaon/node-agent/internal/node"
	"github.com/garamsh/mvmd/lib/runtimeapi"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Run connects to mvmd, then runs the agent until ctx ends. It returns an
// error only when it cannot start.
func Run(ctx context.Context, cfg Config, clk clock.Clock, log *slog.Logger) error {
	conn, err := runtimeapi.Dial(ctx, cfg.MvmdSocket)
	if err != nil {
		return fmt.Errorf("dial mvmd at %s: %w", cfg.MvmdSocket, err)
	}
	defer func() { _ = conn.Close() }()
	runtime := v1alpha2.NewRuntimeServiceClient(conn)
	client := api.NewClient(cfg.APIServer, nil)

	nodes := node.NewService(cfg.NodeName, client, runtime, clk, log)
	live := liveness.NewService(cfg.NodeName, cfg.Liveness, client, runtime, clk, log)
	runs := machines.NewService(cfg.NodeName, machines.Params{
		Resync:            cfg.ResyncPeriod,
		Retry:             cfg.ResyncPeriod,
		RestartBackoff:    cfg.RestartBackoff,
		RestartBackoffMax: cfg.RestartBackoffMax,
		RestartResetAfter: cfg.RestartResetAfter,
		ReadTimeout:       cfg.Liveness.RPCTimeout,
	}, client, runtime, clk, log)

	var wg sync.WaitGroup
	for _, run := range []func(context.Context){
		live.Run,
		func(ctx context.Context) { nodes.Run(ctx, cfg.ResyncPeriod) },
		runs.Run,
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run(ctx)
		}()
	}
	wg.Wait()
	return nil
}
