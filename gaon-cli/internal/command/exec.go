package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// commandExit is the end of a command that ran and exited with a non-zero
// code, which the CLI exits with.
type commandExit struct{ code int }

func (e *commandExit) Error() string { return fmt.Sprintf("command exited with code %d", e.code) }

// envFlag collects the repeatable -e NAME=VALUE.
type envFlag []execEnv

func (f *envFlag) String() string { return "" }

func (f *envFlag) Set(s string) error {
	name, value, ok := strings.Cut(s, "=")
	if !ok || name == "" {
		return fmt.Errorf("%q is not NAME=VALUE", s)
	}
	*f = append(*f, execEnv{Name: name, Value: value})
	return nil
}

// execEnv and execRequest are the body of POST /microvms/{name}/exec.
type execEnv struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

type execRequest struct {
	Command    []string  `json:"command"`
	Env        []execEnv `json:"env,omitempty"`
	WorkingDir string    `json:"workingDir,omitempty"`
}

// execEvent is one line of the exec stream. Object is the error object of an
// Error event, as in a watch.
type execEvent struct {
	Type       string          `json:"type"`
	Stream     string          `json:"stream"`
	Data       []byte          `json:"data"`
	Status     string          `json:"status"`
	ExitCode   int             `json:"exitCode"`
	TermSignal int             `json:"termSignal"`
	Reason     string          `json:"reason"`
	Object     json.RawMessage `json:"object"`
}

func runExec(ctx context.Context, cl *cluster, args []string, out, errOut io.Writer) error {
	// The command is everything after the first "--", so its own flags are not ours.
	i := slices.Index(args, "--")
	if i < 0 {
		return usagef("exec: want NAME -- COMMAND [ARG...]")
	}
	var env envFlag
	var workingDir string
	operands, err := parseFlags("exec", args[:i], func(fs *flag.FlagSet) {
		fs.Var(&env, "e", "NAME=VALUE, repeatable")
		fs.StringVar(&workingDir, "w", "", "working directory")
	})
	if err != nil {
		return err
	}
	argv := args[i+1:]
	if len(operands) != 1 || operands[0] == "" || len(argv) == 0 {
		return usagef("exec: want NAME -- COMMAND [ARG...]")
	}
	name := operands[0]
	body, err := json.Marshal(execRequest{Command: argv, Env: env, WorkingDir: workingDir})
	if err != nil {
		return fmt.Errorf("exec microvm/%s: %w", name, err)
	}
	resp, err := openSubresource(ctx, cl, "POST", name, "exec", nil, body)
	if err != nil {
		return fmt.Errorf("exec microvm/%s: %w", name, err)
	}
	defer resp.Body.Close()
	err = readExec(json.NewDecoder(resp.Body), out, errOut)
	var exit *commandExit
	if err != nil && !errors.As(err, &exit) {
		return fmt.Errorf("exec microvm/%s: %w", name, err)
	}
	return err
}

// readExec prints the output of the stream and returns how it ended. Only an
// Exit event ends it cleanly: a stream that stops without one was cut, and the
// command may still be running.
func readExec(dec *json.Decoder, out, errOut io.Writer) error {
	for {
		var ev execEvent
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("the stream ended without an exit status, the command may still be running")
			}
			return fmt.Errorf("the stream was aborted, the command may still be running: %w", err)
		}
		switch ev.Type {
		case "Output":
			w := out
			if ev.Stream == "Stderr" {
				w = errOut
			}
			if _, err := w.Write(ev.Data); err != nil {
				return err
			}
		case "Exit":
			return exitOf(ev)
		case "Error":
			var e api.APIError
			if err := json.Unmarshal(ev.Object, &e); err != nil || e.Reason == "" {
				return errors.New("the stream carried an error that could not be read")
			}
			return &e
		default:
			return fmt.Errorf("the stream carried an event of type %q", ev.Type)
		}
	}
}

// exitOf is the end an Exit event reports: nil for a command that exited 0.
func exitOf(ev execEvent) error {
	switch ev.Status {
	case "Exited":
		switch {
		case ev.ExitCode == 0:
			return nil
		case ev.ExitCode < 0 || ev.ExitCode > 255:
			// A process exits with the low eight bits, so 256 would read as success.
			return fmt.Errorf("the command exited with code %d, which is no exit status", ev.ExitCode)
		}
		return &commandExit{code: ev.ExitCode}
	case "CutShort":
		return fmt.Errorf("the command was cut short by signal %d: %s", ev.TermSignal, ev.Reason)
	case "NotStarted":
		return fmt.Errorf("the command did not start: %s", ev.Reason)
	}
	return fmt.Errorf("the stream carried an exit of status %q", ev.Status)
}
