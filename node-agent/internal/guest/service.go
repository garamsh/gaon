package guest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// entrypoint is the exec whose output is the MicroVM's own.
const entrypoint = "entrypoint"

type service struct {
	holder  Holder
	runtime Runtime
	params  Params
}

// NewService returns a Service for the MicroVMs holder holds, on runtime.
func NewService(holder Holder, runtime Runtime, params Params) Service {
	if params.LogIdle <= 0 {
		params.LogIdle = DefaultLogIdle
	}
	if params.CallTimeout <= 0 {
		params.CallTimeout = DefaultCallTimeout
	}
	return &service{holder: holder, runtime: runtime, params: params}
}

// failure is an error with the *api.APIError a caller is answered with, and
// the cause the agent keeps to itself.
type failure struct {
	api   *api.APIError
	cause error
}

func (f *failure) Error() string {
	if f.cause == nil {
		return f.api.Error()
	}
	return f.api.Error() + ": " + f.cause.Error()
}

func (f *failure) Unwrap() []error { return []error{f.api, f.cause} }

func notRunning(cause error, format string, args ...any) error {
	return &failure{api: api.NewAPIError(api.ReasonNotRunning, format, args...), cause: cause}
}

func unavailable(cause error, format string, args ...any) error {
	return &failure{api: api.NewAPIError(api.ReasonUnavailable, format, args...), cause: cause}
}

// runtimeFailure is the failure of a call to mvmd: NotRunning when mvmd says
// it holds no such Machine, Unavailable for anything else.
func runtimeFailure(err error, what string) error {
	switch v1alpha2.KindOf(err) {
	case v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED:
		return notRunning(err, "the runtime holds no machine to %s", what)
	}
	return unavailable(err, "the runtime cannot be reached")
}

// machine returns the id of the Machine the target names, when the agent holds
// the MicroVM at that epoch and uid and mvmd has the Machine Running.
func (s *service) machine(ctx context.Context, t Target) (api.MicroVM, string, error) {
	m, ok := s.holder.Holds(t.Name, t.Epoch, t.UID)
	if !ok {
		return api.MicroVM{}, "", notRunning(nil, "the agent holds no machine for microvm %q at epoch %d with that uid", t.Name, t.Epoch)
	}
	id := fmt.Sprintf("%s-e%d", t.Name, t.Epoch)
	call, cancel := context.WithTimeout(ctx, s.params.CallTimeout)
	defer cancel()
	got, err := s.runtime.InspectMachine(call, &v1alpha2.InspectMachineRequest{MachineId: id})
	if err != nil {
		return api.MicroVM{}, "", runtimeFailure(err, "serve")
	}
	if state := got.GetMachine().GetState(); state != v1alpha2.MachineState_MACHINE_STATE_RUNNING {
		return api.MicroVM{}, "", notRunning(nil, "the machine of microvm %q is %s, want Running", t.Name, state)
	}
	return m, id, nil
}

func (s *service) Log(ctx context.Context, req LogRequest, out LogOutput) error {
	_, id, err := s.machine(ctx, req.Target)
	if err != nil {
		return err
	}
	out.Begin()

	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	chunks := make(chan []byte)
	done := make(chan error, 1)
	go func() {
		done <- s.read(readCtx, id, entrypoint, 0, func(c *v1alpha2.OutputChunk) error {
			select {
			case chunks <- c.GetData():
				return nil
			case <-readCtx.Done():
				return readCtx.Err()
			}
		})
	}()

	buffering := req.TailBytes > 0
	var tail []byte
	idle := time.NewTimer(s.params.LogIdle)
	defer idle.Stop()
	if req.Follow && !buffering {
		idle.Stop()
	}
	flush := func() error {
		buffering = false
		data := tail
		tail = nil
		if len(data) == 0 {
			return nil
		}
		return out.Write(data)
	}
	for {
		select {
		case data := <-chunks:
			if buffering {
				tail = lastBytes(append(tail, data...), req.TailBytes)
			} else if err := out.Write(data); err != nil {
				return err
			}
			if !req.Follow || buffering {
				idle.Reset(s.params.LogIdle)
			}
		case <-idle.C:
			if buffering {
				if err := flush(); err != nil {
					return err
				}
			}
			if !req.Follow {
				return nil
			}
		case err := <-done:
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return runtimeFailure(err, "read")
			}
			return flush()
		}
	}
}

// lastBytes returns the last n bytes of b.
func lastBytes(b []byte, n int64) []byte {
	if int64(len(b)) <= n {
		return b
	}
	return append([]byte(nil), b[int64(len(b))-n:]...)
}

// read passes the output of an exec from offset after to sink until the
// record that ends it. An offset below the earliest mvmd holds restarts the
// read at that earliest.
func (s *service) read(ctx context.Context, machineID, execID string, after uint64, sink func(*v1alpha2.OutputChunk) error) error {
	for {
		_, err := v1alpha2.ReadExecOutput(ctx, s.runtime, machineID, execID, after, func(c *v1alpha2.OutputChunk) error {
			after = c.GetOffset() + uint64(len(c.GetData()))
			return sink(c)
		})
		if earliest, gone := v1alpha2.EarliestOffset(err); gone && earliest > after {
			after = earliest
			continue
		}
		return err
	}
}

func (s *service) Exec(ctx context.Context, req ExecRequest, out ExecOutput) error {
	m, id, err := s.machine(ctx, req.Target)
	if err != nil {
		return err
	}
	if m.Metadata.DeletionTimestamp != nil {
		return notRunning(nil, "the microvm %q is being deleted", req.Name)
	}
	execID, err := fresh("exec")
	if err != nil {
		return err
	}
	requestID, err := fresh("request")
	if err != nil {
		return err
	}
	// The call is not cut by a client that went away: the command may have started.
	call, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.params.CallTimeout)
	defer cancel()
	_, err = s.runtime.Exec(call, &v1alpha2.ExecRequest{
		ExecId: execID, RequestId: requestID, MachineId: id,
		Command: req.Command, Env: req.Env, WorkingDir: req.WorkingDir,
	})
	if err != nil {
		return runtimeFailure(err, "run the command")
	}
	out.Begin()
	rec, err := v1alpha2.ReadExecOutput(ctx, s.runtime, id, execID, 0, func(c *v1alpha2.OutputChunk) error {
		stream := Stdout
		if c.GetStream() == v1alpha2.OutputStream_OUTPUT_STREAM_STDERR {
			stream = Stderr
		}
		return out.Output(stream, c.GetData())
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var failed *failure
		if errors.As(err, &failed) {
			return err
		}
		return runtimeFailure(err, "read the output")
	}
	return out.Exit(rec)
}

// fresh returns an identifier for a call, never repeated.
func fresh(kind string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("name the %s: %w", kind, err)
	}
	return "gaon-" + hex.EncodeToString(b), nil
}
