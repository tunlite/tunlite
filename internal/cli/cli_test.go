package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
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

func TestVersionJSONUsesMachineContract(t *testing.T) {
	code, out, stderr := invoke(t, "version", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result["contractVersion"] != ContractVersion || result["version"] != Version {
		t.Fatalf("unexpected version result: %#v", result)
	}
}

func TestConnectionCRUDUsesVersionedStoreAndJSONContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)

	code, out, stderr := invoke(t, "add", "db", "--to", "me@example.com:2222",
		"-L", "5432:localhost:5432", "--tag", "work,db", "--json")
	if code != 0 {
		t.Fatalf("add exit %d, stdout=%s stderr=%s", code, out, stderr)
	}
	var added struct {
		ContractVersion string          `json:"contractVersion"`
		Outcome         string          `json:"outcome"`
		Connection      core.Connection `json:"connection"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil {
		t.Fatal(err)
	}
	if added.ContractVersion != ContractVersion || added.Outcome != "applied" || added.Connection.Metadata.Name != "db" {
		t.Fatalf("unexpected add result: %#v", added)
	}

	code, out, stderr = invoke(t, "list", "--tag", "work", "--json")
	if code != 0 {
		t.Fatalf("list exit %d, stdout=%s stderr=%s", code, out, stderr)
	}
	var listed struct {
		ContractVersion string            `json:"contractVersion"`
		Results         []core.Connection `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.ContractVersion != ContractVersion || len(listed.Results) != 1 {
		t.Fatalf("unexpected list result: %#v", listed)
	}

	code, out, stderr = invoke(t, "set", "db", "--to", "deploy@db.internal", "-D", "1080",
		"--no-tags", "--ssh-opt", "Compression=yes", "--json")
	if code != 0 {
		t.Fatalf("set exit %d, stdout=%s stderr=%s", code, out, stderr)
	}
	var updated struct {
		Connection core.Connection `json:"connection"`
	}
	if err := json.Unmarshal([]byte(out), &updated); err != nil {
		t.Fatal(err)
	}
	var sshConfig sshdriver.Config
	if err := json.Unmarshal(updated.Connection.Spec.Config, &sshConfig); err != nil {
		t.Fatal(err)
	}
	if sshConfig.Host != "deploy@db.internal" || len(sshConfig.Forwards) != 1 || sshConfig.Forwards[0].Type != "dynamic" {
		t.Fatalf("unexpected SSH config: %#v", sshConfig)
	}
	if len(updated.Connection.Metadata.Labels) != 0 {
		t.Fatalf("tags were not cleared: %#v", updated.Connection.Metadata.Labels)
	}

	code, _, stderr = invoke(t, "rename", "db", "database")
	if code != 0 {
		t.Fatalf("rename exit %d, stderr=%s", code, stderr)
	}
	code, out, stderr = invoke(t, "rm", "database", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("dry-run rm exit %d, stdout=%s stderr=%s", code, out, stderr)
	}
	var planned map[string]any
	if err := json.Unmarshal([]byte(out), &planned); err != nil {
		t.Fatal(err)
	}
	if planned["outcome"] != "planned" || planned["dryRun"] != true {
		t.Fatalf("unexpected dry-run result: %#v", planned)
	}
	code, out, stderr = invoke(t, "list", "--json")
	if code != 0 || !strings.Contains(out, "database") {
		t.Fatalf("dry-run changed config: exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, _, stderr = invoke(t, "rm", "database")
	if code != 0 {
		t.Fatalf("rm exit %d, stderr=%s", code, stderr)
	}
	code, out, _ = invoke(t, "rm", "database", "--json")
	if code != 3 || !strings.Contains(out, `"code":"not_found"`) {
		t.Fatalf("unexpected not-found result: exit=%d stdout=%s", code, out)
	}
}

func TestAddDryRunDoesNotCreateConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	code, out, stderr := invoke(t, "add", "preview", "--to", "me@example.com", "-D", "1080", "--dry-run", "--json")
	if code != 0 || !strings.Contains(out, `"outcome":"planned"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	path := filepath.Join(home, "config", "config.json")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created config: %v", err)
	}
}

func TestLegacyConfigReturnsMigrationRequiredWithoutOverwrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	path := filepath.Join(home, "config", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"version":1,"tunnels":[]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := invoke(t, "list", "--json")
	if code != 1 || !strings.Contains(out, `"code":"migration_required"`) {
		t.Fatalf("exit=%d stdout=%s", code, out)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("legacy config was overwritten")
	}
}

func TestExplicitConfigFileSupportsProjectDeclarations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project", "tunlite.json")
	code, _, stderr := invoke(t, "add", "project-db", "--to", "me@example.com", "-D", "1080", "--config", path)
	if code != 0 {
		t.Fatalf("add exit %d, stderr=%s", code, stderr)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("explicit config was not created: %v", err)
	}
	code, out, stderr := invoke(t, "list", "--config="+path, "--json")
	if code != 0 || !strings.Contains(out, "project-db") {
		t.Fatalf("list exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func invoke(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, stderr bytes.Buffer
	code := RunContext(context.Background(), args, IO{Out: &out, Err: &stderr})
	return code, out.String(), stderr.String()
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
