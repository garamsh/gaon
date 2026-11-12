// Command gaon applies, gets, describes and deletes gaon resources through the
// resource API.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/garamsh/gaon/gaon-cli/internal/command"
	"github.com/garamsh/gaon/gaon-cli/internal/config"
	"github.com/garamsh/gaon/lib/api"
)

// environment is what the process hands the CLI besides its arguments.
type environment struct {
	server string // GAON_SERVER
	config string // GAON_CONFIG
	home   string // the home directory, "" when there is none
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	home, _ := os.UserHomeDir()
	env := environment{server: os.Getenv("GAON_SERVER"), config: os.Getenv("GAON_CONFIG"), home: home}
	code := run(ctx, os.Args[1:], env, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run reads the global flags and the config file, builds the HTTP client the
// TLS settings describe and runs the command. A flag beats the environment,
// which beats the config file's current context.
func run(ctx context.Context, args []string, env environment, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gaon", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", env.server, "API root URL, http://<host>:<port>/apis/compute.gaon.garam.sh/v1alpha1")
	configPath := fs.String("config", env.config, "config file, default ~/.gaon/config")
	files := api.RegisterTLSFlags(fs)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "gaon: %v\n", err)
		return command.ExitUsage
	}
	// A file named by --config or GAON_CONFIG must exist; the default may not.
	mustExist := *configPath != ""
	if !mustExist && env.home != "" {
		*configPath = filepath.Join(env.home, ".gaon", "config")
	}
	if rest := fs.Args(); len(rest) > 0 && rest[0] == "config" {
		if *configPath == "" {
			fmt.Fprintln(stderr, "gaon: no config file: set --config or GAON_CONFIG")
			return command.ExitUsage
		}
		return command.RunConfig(*configPath, rest[1:], stdout, stderr)
	}

	var sel config.Selected
	if *configPath != "" {
		cfg, err := config.Load(*configPath, mustExist)
		if err == nil {
			sel, err = cfg.Selected()
		}
		if err != nil {
			fmt.Fprintf(stderr, "gaon: %v\n", err)
			return command.ExitUsage
		}
	}
	if *server == "" {
		*server = sel.Server
	}
	if *server == "" {
		fmt.Fprintln(stderr, "gaon: no server: set --server or GAON_SERVER, or a current-context in the config file")
		return command.ExitUsage
	}
	for _, f := range []struct{ flag, file *string }{
		{&files.CA, &sel.CA}, {&files.Cert, &sel.Cert}, {&files.Key, &sel.Key},
	} {
		if *f.flag == "" {
			*f.flag = *f.file
		}
	}
	httpClient, err := files.Client()
	if err != nil {
		fmt.Fprintf(stderr, "gaon: %v\n", err)
		return command.ExitUsage
	}
	return command.Run(ctx, *server, httpClient, fs.Args(), stdout, stderr)
}
