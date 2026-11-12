// Command foreignplugin is a network plugin the Node Agent did not build and
// shares no code with beyond the plugin API: it serves the network "acme" in
// the directory named by its first argument, gives every MicroVM an ICMP
// ingress rule, and refuses a MicroVM whose name starts with "forbidden".
//
// It is test data of the agent's tests, built by them with go build.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
)

type acme struct{}

func (acme) Policy(_ context.Context, m *api.MicroVM) (*plugin.NetworkPolicy, error) {
	if strings.HasPrefix(m.Metadata.Name, "forbidden") {
		return nil, &plugin.Refusal{Reason: "AcmeForbidden", Message: "acme does not serve " + m.Metadata.Name}
	}
	return &plugin.NetworkPolicy{Ingress: []plugin.NetworkRule{{Prefix: "10.9.0.0/16", Protocol: plugin.ICMP}}}, nil
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: foreignplugin <plugin dir>")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := plugin.ServeNetwork(ctx, os.Args[1], "acme", acme{}); err != nil {
		log.Fatal(err)
	}
}
