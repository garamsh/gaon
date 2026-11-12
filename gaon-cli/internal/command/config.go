package command

import (
	"errors"
	"fmt"
	"io"

	"github.com/garamsh/gaon/gaon-cli/internal/config"
)

// RunConfig executes one `gaon config` command, args being what follows
// `config`, on the config file at path, and returns the process exit code.
func RunConfig(path string, args []string, stdout, stderr io.Writer) int {
	return exit(runConfig(path, args, stdout), stdout, stderr)
}

func runConfig(path string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return usagef("config: want current-context or use-context")
	}
	var act func(*config.File) error
	switch args[0] {
	case "current-context":
		if len(args) != 1 {
			return usagef("config current-context: want no operand")
		}
		act = func(f *config.File) error {
			if f.CurrentContext == "" {
				return fmt.Errorf("config file %s has no current-context", path)
			}
			fmt.Fprintln(out, f.CurrentContext)
			return nil
		}
	case "use-context":
		if len(args) != 2 {
			return usagef("config use-context: want NAME")
		}
		act = func(f *config.File) error { return f.UseContext(args[1]) }
	default:
		return usagef("unknown config command %q", args[0])
	}
	f, err := config.Load(path, true)
	if err == nil {
		err = act(f)
	}
	var ce *config.Error
	if errors.As(err, &ce) {
		return usagef("%v", err)
	}
	return err
}
