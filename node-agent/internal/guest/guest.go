// Package guest serves a MicroVM's log and exec to the API server over
// mutual TLS (ADR 0043): the log of a Machine the agent holds, read from mvmd,
// and a command run in it.
package guest

import (
	"context"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Defaults of Params.
const (
	// DefaultLogIdle is how long without output a log read waits before it
	// takes the output held to be all there is. mvmd marks no such point, and
	// 500 ms is provisional: it is not measured.
	DefaultLogIdle = 500 * time.Millisecond
	// DefaultCallTimeout bounds each unary call to mvmd. It is below the
	// API server's wait for a response header, 30 s.
	DefaultCallTimeout = 20 * time.Second
)

// Params are the periods the Service keeps. A zero value is the default.
type Params struct {
	// LogIdle is the idle time that ends a read that does not follow, and
	// that ends the buffering of a tail.
	LogIdle time.Duration
	// CallTimeout bounds each unary call to mvmd.
	CallTimeout time.Duration
}

// Target names the Machine a request is for: a MicroVM at a binding epoch.
type Target struct {
	Name  string
	Epoch api.Epoch
	UID   api.UID
}

// LogRequest asks for the log of a Machine.
type LogRequest struct {
	Target
	// Follow keeps the read open until the process ends.
	Follow bool
	// TailBytes, when above zero, starts the read that many bytes before the
	// end of what is held.
	TailBytes int64
}

// ExecRequest asks for a command to run in a Machine.
type ExecRequest struct {
	Target
	Command    []string
	Env        map[string]string
	WorkingDir string
}

// Stream says which of a command's two outputs a piece of data is of.
type Stream string

// The streams of an exec's output.
const (
	Stdout Stream = "Stdout"
	Stderr Stream = "Stderr"
)

// LogOutput receives a log read. Begin is called once, before the first
// Write, when the read is known to be served: a failure before it is the
// response's status, and one after it can only cut the response.
type LogOutput interface {
	Begin()
	Write(data []byte) error
}

// ExecOutput receives an exec. Begin is called once, before anything else,
// when the command was handed to mvmd.
type ExecOutput interface {
	Begin()
	Output(stream Stream, data []byte) error
	Exit(rec *v1alpha2.ExecRecord) error
}

// Service reads logs and runs commands in the Machines the agent holds. A
// failure is an *api.APIError of reason NotRunning, Unavailable or Invalid,
// found with errors.As, that carries a message the caller may read; the cause
// is in the chain.
type Service interface {
	// Log writes the output of the Machine's entrypoint to out: the oldest
	// held byte on, or the last TailBytes of it. When Follow is false it ends
	// once the output has been idle for Params.LogIdle, when Follow is true
	// when the process ends, and either way when ctx ends. It is NotRunning
	// when the agent does not hold the Machine at the epoch and uid, or the
	// Machine is not Running.
	Log(ctx context.Context, req LogRequest, out LogOutput) error
	// Exec runs the command once in the Machine, writes its output to out and
	// ends with its record. It is NotRunning when the agent does not hold the
	// Machine at the epoch and uid, the MicroVM is being deleted or the
	// Machine is not Running, and never calls mvmd twice for one request. A
	// ctx that ends stops the read of the output and not the command.
	Exec(ctx context.Context, req ExecRequest, out ExecOutput) error
}
