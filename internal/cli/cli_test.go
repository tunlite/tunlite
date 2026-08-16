package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRunDryRunProducesVersionedExecutionPlan(t *testing.T) {
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	var out, stderr bytes.Buffer
	code := Run([]string{
		"run", "--to", "me@example.com:2222", "-L", "8080:localhost:80", "-D", "1080",
		"--name", "staging", "--json", "--non-interactive", "--dry-run",
	}, IO{Out: &out, Err: &stderr})
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s, stdout=%s", code, stderr.String(), out.String())
	}
	var result struct {
		ContractVersion string `json:"contractVersion"`
		Outcome         string `json:"outcome"`
		DryRun          bool   `json:"dryRun"`
		Plan            struct {
			Mode string `json:"mode"`
			Exec struct {
				Program string   `json:"program"`
				Args    []string `json:"args"`
			} `json:"exec"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if result.ContractVersion != ContractVersion || result.Outcome != "planned" || !result.DryRun {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Plan.Mode != "direct" || result.Plan.Exec.Program != "/usr/bin/ssh" {
		t.Fatalf("unexpected plan: %#v", result.Plan)
	}
	last := result.Plan.Exec.Args[len(result.Plan.Exec.Args)-2:]
	if last[0] != "--" || last[1] != "me@example.com" {
		t.Fatalf("unsafe argv ending: %#v", last)
	}
}

func TestRunForegroundStartsAndStopsProvider(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test provider is a POSIX executable script")
	}
	temp := t.TempDir()
	marker := filepath.Join(temp, "started")
	provider := filepath.Join(temp, "fake-ssh")
	script := []byte("#!/bin/sh\n: > \"$TUNLITE_TEST_MARKER\"\ntrap 'exit 0' TERM INT\nwhile :; do :; done\n")
	if err := os.WriteFile(provider, script, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNLITE_SSH", provider)
	t.Setenv("TUNLITE_TEST_MARKER", marker)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- RunContext(ctx, []string{
			"run", "--to", "me@example.com", "-D", "1080", "--json", "--non-interactive",
		}, IO{Out: &out, Err: &stderr})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := os.Stat(marker)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("provider did not start; stderr=%s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d, stderr=%s, stdout=%s", code, stderr.String(), out.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("foreground provider did not stop after cancellation")
	}

	decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
	var started, stopped map[string]any
	if err := decoder.Decode(&started); err != nil {
		t.Fatalf("missing starting event: %v\n%s", err, out.String())
	}
	if err := decoder.Decode(&stopped); err != nil {
		t.Fatalf("missing stopped event: %v\n%s", err, out.String())
	}
	if started["outcome"] != "starting" || stopped["outcome"] != "stopped" {
		t.Fatalf("unexpected events: %#v %#v", started, stopped)
	}
}

func TestRunRejectsMissingForwardWithUsageExit(t *testing.T) {
	var out, stderr bytes.Buffer
	code := Run([]string{"run", "--to", "me@example.com", "--json"}, IO{Out: &out, Err: &stderr})
	if code != 2 {
		t.Fatalf("got exit %d, want 2", code)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("usage error was not JSON: %v", err)
	}
	if result["contractVersion"] != ContractVersion {
		t.Fatalf("missing contract version: %#v", result)
	}
}
