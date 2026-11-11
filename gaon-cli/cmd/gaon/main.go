// Command gaon applies, gets, lists and deletes gaon resources through the
// resource API.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/garamsh/gaon/gaon-cli/internal/command"
	"github.com/garamsh/gaon/lib/api"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Getenv("GAON_SERVER"), os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run reads the global flags, builds the client and runs the command.
// defaultServer is the server used when --server is not given.
func run(ctx context.Context, args []string, defaultServer string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gaon", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", defaultServer, "API root URL, http://<host>:<port>/apis/compute.gaon.garam.sh/v1alpha1")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "gaon: %v\n", err)
		return command.ExitUsage
	}
	if *server == "" {
		fmt.Fprintln(stderr, "gaon: no server: set --server or GAON_SERVER")
		return command.ExitUsage
	}
	return command.Run(ctx, api.NewClient(*server, nil), fs.Args(), stdout, stderr)
}
