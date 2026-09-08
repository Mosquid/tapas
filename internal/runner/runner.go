// Package runner executes one child process with resolved credentials present
// only in that child's environment, and redacts those values from the child's
// standard output and standard error.
package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sort"
	"syscall"
	"time"
)

const placeholder = "[redacted]"

// Binding names one environment variable and the value delivered under it.
type Binding struct {
	Variable string
	Value    string
}

// Redactor removes exact secret values from a byte stream. It holds back the
// bytes that could still begin a match, so a value split across two writes is
// still removed. It does not decode base64, JSON escapes, or other
// transformations of the value, and it never sees output the child writes
// directly to a terminal, a file, or a network destination.
type Redactor struct {
	sink    io.Writer
	secrets [][]byte
	hold    int
	pending []byte
}

func NewRedactor(sink io.Writer, values []string) *Redactor {
	r := &Redactor{sink: sink}
	for _, v := range values {
		if v != "" {
			r.secrets = append(r.secrets, []byte(v))
		}
	}
	// Replace the longest values first, so a value that contains another is not
	// partially rewritten before its own match is found.
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
	for _, s := range r.secrets {
		if len(s)-1 > r.hold {
			r.hold = len(s) - 1
		}
	}
	return r
}

func (r *Redactor) scrub(b []byte) []byte {
	for _, s := range r.secrets {
		b = bytes.ReplaceAll(b, s, []byte(placeholder))
	}
	return b
}

func (r *Redactor) Write(p []byte) (int, error) {
	if len(r.secrets) == 0 {
		return r.sink.Write(p)
	}
	r.pending = r.scrub(append(r.pending, p...))
	if len(r.pending) > r.hold {
		flush := len(r.pending) - r.hold
		if _, e := r.sink.Write(r.pending[:flush]); e != nil {
			return 0, e
		}
		r.pending = r.pending[:copy(r.pending, r.pending[flush:])]
	}
	return len(p), nil
}

// Flush writes the held-back tail. Call it once the child has exited.
func (r *Redactor) Flush() error {
	if len(r.pending) == 0 {
		return nil
	}
	tail := r.scrub(r.pending)
	r.pending = nil
	_, e := r.sink.Write(tail)
	return e
}

// Exec runs argv with the bindings added to the current environment, streams
// redacted output, and returns the child's exit status. Values are passed only
// through the environment of this one child, never as arguments.
func Exec(ctx context.Context, argv []string, bindings []Binding, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return 0, errors.New("provide a command after --")
	}
	program, e := exec.LookPath(argv[0])
	if e != nil {
		return 0, errors.New("command not found: " + argv[0])
	}
	values := make([]string, 0, len(bindings))
	environment := os.Environ()
	for _, b := range bindings {
		environment = append(environment, b.Variable+"="+b.Value)
		values = append(values, b.Value)
	}
	outputRedactor := NewRedactor(stdout, values)
	errorRedactor := NewRedactor(stderr, values)
	cmd := exec.CommandContext(ctx, program, argv[1:]...)
	cmd.Env = environment
	cmd.Stdin = os.Stdin
	cmd.Stdout = outputRedactor
	cmd.Stderr = errorRedactor
	// Cancellation asks the child to terminate; WaitDelay then forces the kill
	// and stops waiting on inherited pipes.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	e = cmd.Run()
	flushError := outputRedactor.Flush()
	if flushError == nil {
		flushError = errorRedactor.Flush()
	}
	var exit *exec.ExitError
	if errors.As(e, &exit) {
		if flushError != nil {
			return 0, flushError
		}
		if code := exit.ExitCode(); code >= 0 {
			return code, nil
		}
		return 128, nil
	}
	if e != nil {
		return 0, errors.New("command did not complete")
	}
	return 0, flushError
}
