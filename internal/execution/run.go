package execution

import (
	"context"
	"io"
	"os/exec"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

func Run(ctx context.Context, executionPlan plan.ExecutionPlan, streams IO) error {
	if err := plan.Validate(executionPlan); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	command := exec.Command(executionPlan.Exec.Program, executionPlan.Exec.Args...)
	command.Stdin = streams.In
	command.Stdout = streams.Out
	command.Stderr = streams.Err
	if executionPlan.Exec.WorkingDirectory != nil {
		command.Dir = *executionPlan.Exec.WorkingDirectory
	}
	if len(executionPlan.Exec.Environment) > 0 {
		command.Env = command.Environ()
		for key, value := range executionPlan.Exec.Environment {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	if err := command.Start(); err != nil {
		return err
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()

	select {
	case err := <-waited:
		return err
	case <-ctx.Done():
		_ = terminateProcess(command.Process)
	}

	timer := time.NewTimer(time.Duration(executionPlan.Lifecycle.StopTimeoutSeconds) * time.Second)
	defer timer.Stop()
	select {
	case <-waited:
		return ctx.Err()
	case <-timer.C:
		_ = command.Process.Kill()
		<-waited
		return ctx.Err()
	}
}
