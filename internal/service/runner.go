package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
)

type ExecRunner struct{}

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
	var stdout, stderr bytes.Buffer
	process.Stdin = streams.In
	process.Stdout = &stdout
	process.Stderr = &stderr
	if streams.Out != nil {
		process.Stdout = io.MultiWriter(&stdout, streams.Out)
	}
	if streams.Err != nil {
		process.Stderr = io.MultiWriter(&stderr, streams.Err)
	}
	err := process.Run()
	output := append(append([]byte(nil), stdout.Bytes()...), stderr.Bytes()...)
	if err != nil {
		return output, &CommandError{Command: command, Args: append([]string(nil), args...), Output: string(output), Err: err}
	}
	return output, nil
}
