package nodeagent

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/certificate"
	"github.com/garamsh/gaon/node-agent/internal/clock"
)

// connect returns the client of the resource API, the holder of the
// certificate the agent keeps and the loop that renews it, both nil when the
// agent does not keep one. When the
// agent keeps its certificate in cfg.CertDir, connect returns once it holds a
// valid pair.
func connect(ctx context.Context, cfg Config, clk clock.Clock, log *slog.Logger) (*api.Client, *certificate.Holder, func(context.Context), error) {
	if cfg.CertDir == "" {
		return api.NewClient(cfg.APIServer, cfg.HTTPClient), nil, nil, nil
	}
	holder := &certificate.Holder{}
	httpClient, err := api.NewTLSClientFunc(cfg.TLSCA, holder.ClientCertificate)
	if err != nil {
		return nil, nil, nil, err
	}
	client := api.NewClient(cfg.APIServer, httpClient)
	var bootstrap certificate.Requests
	if cfg.BootstrapToken != "" {
		tokenClient, err := api.NewServerTLSClient(cfg.TLSCA)
		if err != nil {
			return nil, nil, nil, err
		}
		bootstrap = api.NewClient(cfg.APIServer, tokenClient, api.WithBearerToken(cfg.BootstrapToken))
	}
	certs := certificate.NewService(certificate.Params{NodeName: cfg.NodeName, Dir: cfg.CertDir, Serving: len(cfg.AgentAddresses) > 0}, holder, bootstrap, client, clk, log)
	if err := certs.Start(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("client certificate: %w", err)
	}
	return client, holder, certs.Run, nil
}
