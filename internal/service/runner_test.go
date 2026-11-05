package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServiceRunnerOutputHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-2] != "--runner-helper" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	chunk := bytes.Repeat([]byte("x"), 32*1024)
	for i := 0; i < 512 || mode == "cancel"; i++ {
		if _, err := os.Stdout.Write(chunk); err != nil {
			os.Exit(2)
		}
		if _, err := os.Stderr.Write(chunk); err != nil {
			os.Exit(2)
		}
	}
	io.WriteString(os.Stdout, "stdout-tail\n")
	io.WriteString(os.Stderr, "stderr-tail\n")
	if mode == "failure" {
		os.Exit(7)
	}
	os.Exit(0)
}

type outputCaptureRunner struct {
	mode   string
	output []byte
	err    error
}

func (r *outputCaptureRunner) Run(ctx context.Context, _ string, _ []string, streams Streams) ([]byte, error) {
	r.output, r.err = (ExecRunner{}).Run(ctx, os.Args[0], []string{"-test.run=^TestServiceRunnerOutputHelper$", "--", "--runner-helper", r.mode}, streams)
	return r.output, r.err
}

type streamMemoryProbe struct {
	mu                sync.Mutex
	bytes, nextSample uint64
	baseline, peak    uint64
	cancel            context.CancelFunc
}

func (w *streamMemoryProbe) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.bytes += uint64(len(p))
	if w.bytes >= w.nextSample {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		if stats.HeapAlloc > w.peak {
			w.peak = stats.HeapAlloc
		}
		w.nextSample = w.bytes + 1024*1024
	}
	if w.cancel != nil && w.bytes >= 1024*1024 {
		w.cancel()
	}
	return len(p), nil
}

func TestNativeLogFollowRetainsBoundedDiagnosticTail(t *testing.T) {
	for _, platform := range []string{"launchd", "systemd"} {
		t.Run(platform, func(t *testing.T) {
			runner := &outputCaptureRunner{mode: "failure"}
			var adapter Adapter = NewSystemd(t.TempDir(), runner)
			reference := Reference{Connection: "stream", Scope: ScopeUser}
			if platform == "launchd" {
				path := filepath.Join(t.TempDir(), "provider.log")
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				reference.LogPaths = []string{path}
				adapter = NewLaunchd(t.TempDir(), t.TempDir(), 501, runner)
			}
			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			probe := &streamMemoryProbe{baseline: before.HeapAlloc}
			err := adapter.Logs(context.Background(), reference, 100, true, Streams{Out: probe, Err: probe})
			var commandErr *CommandError
			if !errors.As(err, &commandErr) {
				t.Fatalf("missing ordinary command error: %v", err)
			}
			if probe.bytes != 32*1024*1024+24 {
				t.Fatalf("stream dropped data: %d bytes", probe.bytes)
			}
			if len(runner.output) > 64*1024 || len(commandErr.Output) > 64*1024 {
				t.Errorf("follow retained %d output bytes and %d error bytes after 32 MiB stream", len(runner.output), len(commandErr.Output))
			}
			if probe.peak > probe.baseline+8*1024*1024 {
				t.Errorf("follow heap grew by %d bytes during sustained output", probe.peak-probe.baseline)
			}
			for _, tail := range []string{"stdout-tail\n", "stderr-tail\n"} {
				if !strings.Contains(commandErr.Output, tail) {
					t.Errorf("missing diagnostic tail %q", tail)
				}
			}
		})
	}
}

func TestExecRunnerPreservesOrdinaryCommandDiagnostics(t *testing.T) {
	runner := &outputCaptureRunner{mode: "failure"}
	output, err := runner.Run(context.Background(), "unused", nil, Streams{})
	var commandErr *CommandError
	if !errors.As(err, &commandErr) || len(output) != 32*1024*1024+24 || commandErr.Output != string(output) {
		t.Fatalf("ordinary command diagnostics were lost: bytes=%d err=%v", len(output), err)
	}
}

func TestLogFollowCancellationStopsStreamingProcess(t *testing.T) {
	runner := &outputCaptureRunner{mode: "cancel"}
	adapter := NewSystemd(t.TempDir(), runner)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	probe := &streamMemoryProbe{cancel: cancel}
	started := time.Now()
	err := adapter.Logs(ctx, Reference{Connection: "stream"}, 100, true, Streams{Out: probe, Err: probe})
	if err == nil || ctx.Err() != context.Canceled || time.Since(started) > 3*time.Second {
		t.Fatalf("stream did not stop on cancellation: err=%v ctx=%v elapsed=%v", err, ctx.Err(), time.Since(started))
	}
}
