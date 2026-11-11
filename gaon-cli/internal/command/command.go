// Package command implements the gaon commands over the resource API client.
package command

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/garamsh/gaon/lib/api"
)

// Exit codes Run returns.
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const usage = `usage:
  gaon [--server URL] apply -f FILE
  gaon [--server URL] get [-o yaml|table] KIND NAME
  gaon [--server URL] list [-o yaml|table] KIND
  gaon [--server URL] delete KIND NAME`

// usageError is a mistake in the command line, as against a failed request.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// Run executes one gaon command, args being the command line after the
// global flags, and returns the process exit code.
func Run(ctx context.Context, c *api.Client, args []string, stdout, stderr io.Writer) int {
	err := run(ctx, c, args, stdout)
	var u *usageError
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprintln(stdout, usage)
		return ExitOK
	case errors.As(err, &u):
		fmt.Fprintf(stderr, "gaon: %v\n%s\n", err, usage)
		return ExitUsage
	default:
		fmt.Fprintf(stderr, "gaon: %v\n", err)
		return ExitFailure
	}
}

func run(ctx context.Context, c *api.Client, args []string, out io.Writer) error {
	if len(args) == 0 {
		return usagef("no command")
	}
	rest := args[1:]
	switch args[0] {
	case "apply":
		return runApply(ctx, c, rest, out)
	case "get":
		return runGet(ctx, c, rest, out)
	case "list":
		return runList(ctx, c, rest, out)
	case "delete":
		return runDelete(ctx, c, rest, out)
	case "-h", "-help", "--help", "help":
		return flag.ErrHelp
	default:
		return usagef("unknown command %q", args[0])
	}
}

// parseFlags parses args for a command whose flags precede its operands.
func parseFlags(name string, args []string, define func(*flag.FlagSet)) ([]string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	define(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, err
		}
		return nil, usagef("%s: %v", name, err)
	}
	return fs.Args(), nil
}

// lookupKind resolves the operand naming a kind.
func lookupKind(operand string) (kind, error) {
	k, ok := kindByName(strings.ToLower(operand))
	if !ok {
		return kind{}, usagef("unknown kind %q, want one of %s", operand, strings.Join(kindNames(), ", "))
	}
	return k, nil
}

func runApply(ctx context.Context, c *api.Client, args []string, out io.Writer) error {
	var file string
	operands, err := parseFlags("apply", args, func(fs *flag.FlagSet) { fs.StringVar(&file, "f", "", "YAML file") })
	if err != nil {
		return err
	}
	if file == "" || len(operands) != 0 {
		return usagef("apply: want -f FILE and no other operand")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	docs, err := parseDocuments(data)
	if err != nil {
		return fmt.Errorf("apply %s: %w", file, err)
	}
	if len(docs) == 0 {
		return fmt.Errorf("apply %s: no resource in the file", file)
	}
	// Every document is checked before the first is sent.
	type item struct {
		kind   kind
		change change
	}
	items := make([]item, len(docs))
	for i, d := range docs {
		k, ok := kindByAPIKind(d.apiKind)
		if !ok {
			return fmt.Errorf("apply %s: document %d: unknown kind %q", file, i+1, d.apiKind)
		}
		if k.parse == nil {
			return refusal("apply", k, "PUT", "/{name}")
		}
		ch, err := k.parse(d.json)
		if err != nil {
			return fmt.Errorf("apply %s: document %d: %s: %w", file, i+1, k.name, err)
		}
		items[i] = item{k, ch}
	}
	for _, it := range items {
		verb, err := applyOne(ctx, c, it.change)
		label := it.kind.name + "/" + it.change.name
		if err != nil {
			return fmt.Errorf("apply %s: %w", label, err)
		}
		fmt.Fprintf(out, "%s %s\n", label, verb)
	}
	return nil
}

// applyOne sends ch as a compare-and-swap and reports "created" or
// "configured". The version it swaps against is the file's, or else the
// current one, so a change made after that read fails the write. It is never
// retried.
func applyOne(ctx context.Context, c *api.Client, ch change) (string, error) {
	version, verb := ch.version, "configured"
	if version == "" {
		cur, err := ch.current(ctx, c)
		switch {
		case errors.Is(err, api.ErrNotFound):
			verb = "created"
		case err != nil:
			return "", err
		default:
			version = cur
		}
	}
	err := ch.send(ctx, c, version)
	if errors.Is(err, api.ErrConflict) || errors.Is(err, api.ErrAlreadyExists) {
		return "", fmt.Errorf("not applied, changed by someone else since it was read: %w", err)
	}
	if err != nil {
		return "", err
	}
	return verb, nil
}

func runGet(ctx context.Context, c *api.Client, args []string, out io.Writer) error {
	format := "yaml"
	operands, err := parseFlags("get", args, func(fs *flag.FlagSet) { fs.StringVar(&format, "o", format, "yaml or table") })
	if err != nil {
		return err
	}
	if len(operands) != 2 {
		return usagef("get: want KIND NAME")
	}
	k, err := lookupKind(operands[0])
	if err != nil {
		return err
	}
	if err := checkFormat(format); err != nil {
		return err
	}
	if k.get == nil {
		return refusal("get", k, "GET", "/{name}")
	}
	res, err := k.get(ctx, c, operands[1])
	if err != nil {
		return fmt.Errorf("get %s/%s: %w", k.name, operands[1], err)
	}
	return render(out, format, res)
}

func runList(ctx context.Context, c *api.Client, args []string, out io.Writer) error {
	format := "table"
	operands, err := parseFlags("list", args, func(fs *flag.FlagSet) { fs.StringVar(&format, "o", format, "yaml or table") })
	if err != nil {
		return err
	}
	if len(operands) != 1 {
		return usagef("list: want KIND")
	}
	k, err := lookupKind(operands[0])
	if err != nil {
		return err
	}
	if err := checkFormat(format); err != nil {
		return err
	}
	if k.list == nil {
		return refusal("list", k, "GET", "")
	}
	res, err := k.list(ctx, c)
	if err != nil {
		return fmt.Errorf("list %s: %w", k.name, err)
	}
	return render(out, format, res)
}

func runDelete(ctx context.Context, c *api.Client, args []string, out io.Writer) error {
	operands, err := parseFlags("delete", args, func(*flag.FlagSet) {})
	if err != nil {
		return err
	}
	if len(operands) != 2 {
		return usagef("delete: want KIND NAME")
	}
	k, err := lookupKind(operands[0])
	if err != nil {
		return err
	}
	if k.del == nil {
		return refusal("delete", k, "DELETE", "/{name}")
	}
	if err := k.del(ctx, c, operands[1]); err != nil {
		return fmt.Errorf("delete %s/%s: %w", k.name, operands[1], err)
	}
	fmt.Fprintf(out, "%s/%s marked for deletion\n", k.name, operands[1])
	return nil
}

func checkFormat(format string) error {
	if format != "yaml" && format != "table" {
		return usagef("unknown output format %q, want yaml or table", format)
	}
	return nil
}

func render(out io.Writer, format string, res result) error {
	if format == "yaml" {
		b, err := encodeYAML(res.object)
		if err != nil {
			return err
		}
		_, err = out.Write(b)
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(res.header, "\t"))
	for _, row := range res.rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	return w.Flush()
}
