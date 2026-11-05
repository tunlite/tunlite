package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
)

type ExecRunner struct{}

type commandOutput interface {
	io.Writer
	Bytes() []byte
}

const diagnosticTailLimit = 32 * 1024

type diagnosticTail struct{ data []byte }

func (b *diagnosticTail) Bytes() []byte { return b.data }

func (b *diagnosticTail) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= diagnosticTailLimit {
		p = p[len(p)-diagnosticTailLimit:]
		b.data = b.data[:0]
	} else if drop := len(b.data) + len(p) - diagnosticTailLimit; drop > 0 {
		copy(b.data, b.data[drop:])
		b.data = b.data[:len(b.data)-drop]
	}
	b.data = append(b.data, p...)
	return n, nil
}

type CommandError struct {
	Command string
	Args    []string
	Output  string
	Err     error
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("%s failed: %v", e.Command, e.Err)
}

func (e *CommandError) Unwrap() error { return e.Err }

func (ExecRunner) Run(ctx context.Context, command string, args []string, streams Streams) ([]byte, error) {
	process := exec.CommandContext(ctx, command, args...)
	var stdout, stderr commandOutput = &bytes.Buffer{}, &bytes.Buffer{}
	if streams.Streaming {
		stdout = &diagnosticTail{data: make([]byte, 0, diagnosticTailLimit)}
		stderr = &diagnosticTail{data: make([]byte, 0, diagnosticTailLimit)}
	}
	process.Stdin = streams.In
	process.Stdout = stdout
	process.Stderr = stderr
	if streams.Out != nil {
		process.Stdout = io.MultiWriter(stdout, streams.Out)
	}
	if streams.Err != nil {
		process.Stderr = io.MultiWriter(stderr, streams.Err)
	}
	err := process.Run()
	output := append(append([]byte(nil), stdout.Bytes()...), stderr.Bytes()...)
	if err != nil {
		return output, &CommandError{Command: command, Args: append([]string(nil), args...), Output: string(output), Err: err}
	}
	return output, nil
}
