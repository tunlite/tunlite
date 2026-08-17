package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/configstore"
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

func TestRunRequiresExplicitApprovalForPublish(t *testing.T) {
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	code, out, stderr := invoke(t,
		"run", "--to", "me@example.com", "-R", "19000:localhost:3000",
		"--json", "--non-interactive",
	)
	if code != 1 || stderr != "" {
		t.Fatalf("run exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var result struct {
		Error struct {
			Code             string        `json:"code"`
			RiskLevel        string        `json:"riskLevel"`
			RequiresApproval bool          `json:"requiresApproval"`
			Actions          []core.Action `json:"actions"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error.Code != "approval_required" || result.Error.RiskLevel != "high" || !result.Error.RequiresApproval {
		t.Fatalf("unexpected approval error: %#v", result.Error)
	}
	if len(result.Error.Actions) != 1 || result.Error.Actions[0].Type != "approve_connection" || result.Error.Actions[0].Parameters["operation"] != "run" {
		t.Fatalf("unexpected approval action: %#v", result.Error.Actions)
	}
}

func TestRunDryRunReportsPublicListenRisk(t *testing.T) {
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	code, out, stderr := invoke(t,
		"run", "--to", "me@example.com", "-D", "0.0.0.0:1080",
		"--dry-run", "--json", "--non-interactive",
	)
	if code != 0 || stderr != "" {
		t.Fatalf("run exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var result struct {
		Outcome    string           `json:"outcome"`
		Conditions []core.Condition `json:"conditions"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "planned" || len(result.Conditions) != 1 || result.Conditions[0].Code != "approval_required" {
		t.Fatalf("unexpected plan conditions: %#v", result)
	}
	actions := result.Conditions[0].Actions
	if len(actions) != 1 || actions[0].Parameters["operation"] != "run" || actions[0].Parameters["riskLevel"] != "high" {
		t.Fatalf("unexpected plan approval action: %#v", actions)
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

func TestSchemaAndCapabilitiesAreMachineQueryable(t *testing.T) {
	code, out, stderr := invoke(t, "schema", "connection")
	if code != 0 || stderr != "" {
		t.Fatalf("schema exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var schemaDocument map[string]any
	if err := json.Unmarshal([]byte(out), &schemaDocument); err != nil {
		t.Fatal(err)
	}
	if schemaDocument["$id"] != "https://tunlite.dev/schemas/connection-v1.schema.json" {
		t.Fatalf("unexpected schema: %#v", schemaDocument)
	}
	code, out, stderr = invoke(t, "capabilities", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("capabilities exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var capabilities map[string]any
	if err := json.Unmarshal([]byte(out), &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities["contractVersion"] != ContractVersion {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
}

func TestCapabilitiesReportMissingNativeServiceManager(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("this assertion covers platforms with a native service adapter")
	}
	t.Setenv("PATH", t.TempDir())
	code, out, stderr := invoke(t, "capabilities", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("capabilities exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var result struct {
		PersistentServices struct {
			Available bool `json:"available"`
		} `json:"persistentServices"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.PersistentServices.Available {
		t.Fatal("capabilities claimed a native service manager was available with an empty PATH")
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

func TestNamedConnectionRunBuildsForegroundPlan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	code, _, stderr := invoke(t, "add", "named-db", "--to", "me@example.com", "-L", "15432:localhost:5432")
	if code != 0 {
		t.Fatalf("add exit %d, stderr=%s", code, stderr)
	}
	code, out, stderr := invoke(t, "run", "named-db", "--dry-run", "--non-interactive", "--json")
	if code != 0 {
		t.Fatalf("run exit %d, stdout=%s stderr=%s", code, out, stderr)
	}
	var result struct {
		Outcome    string `json:"outcome"`
		Connection struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Lifecycle string `json:"lifecycle"`
			} `json:"spec"`
		} `json:"connection"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "planned" || result.Connection.Metadata.Name != "named-db" || result.Connection.Spec.Lifecycle != "foreground" {
		t.Fatalf("unexpected named run plan: %#v", result)
	}
}

func TestEndpointConflictIsStructuredWarning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	code, _, stderr := invoke(t, "add", "first", "--to", "me@one", "-L", "18080:localhost:80")
	if code != 0 {
		t.Fatalf("first add exit %d, stderr=%s", code, stderr)
	}
	code, out, stderr := invoke(t, "add", "second", "--to", "me@two", "-D", "18080", "--json")
	if code != 0 {
		t.Fatalf("second add exit %d, stdout=%s stderr=%s", code, out, stderr)
	}
	var result struct {
		Conditions []condition `json:"conditions"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Conditions) != 1 || result.Conditions[0].Code != "endpoint_conflict" || result.Conditions[0].Severity != "warning" {
		t.Fatalf("unexpected conditions: %#v", result.Conditions)
	}
}

func TestExportImportAndValidateDeclarations(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.json")
	target := filepath.Join(root, "target.json")
	project := filepath.Join(root, "project.json")

	for _, item := range []struct {
		name string
		port string
	}{{"a", "1080"}, {"b", "1081"}} {
		code, out, stderr := invoke(t, "add", item.name, "--to", "me@example.com", "-D", item.port, "--config", source, "--json")
		if code != 0 {
			t.Fatalf("source add exit=%d stdout=%s stderr=%s", code, out, stderr)
		}
	}
	code, out, stderr := invoke(t, "export", "--output", project, "--config", source, "--json")
	if code != 0 {
		t.Fatalf("export exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	document, err := configstore.New(project).Load()
	if err != nil || len(document.Items) != 2 {
		t.Fatalf("exported document invalid: items=%d err=%v", len(document.Items), err)
	}
	code, out, stderr = invoke(t, "validate", project, "--json")
	if code != 0 || !strings.Contains(out, `"outcome":"succeeded"`) {
		t.Fatalf("validate exit=%d stdout=%s stderr=%s", code, out, stderr)
	}

	code, _, stderr = invoke(t, "add", "a", "--to", "old@example.com", "-D", "2080", "--config", target)
	if code != 0 {
		t.Fatalf("target add exit=%d stderr=%s", code, stderr)
	}
	code, out, stderr = invoke(t, "import", project, "--config", target, "--json")
	if code != exitPartial {
		t.Fatalf("import exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var imported struct {
		Outcome string       `json:"outcome"`
		Results []itemResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Outcome != "partial" || len(imported.Results) != 2 {
		t.Fatalf("unexpected import result: %#v", imported)
	}
	targetDocument, err := configstore.New(target).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(targetDocument.Items) != 2 {
		t.Fatalf("partial import did not preserve successes: %#v", targetDocument.Items)
	}

	code, out, stderr = invoke(t, "import", project, "--force", "--dry-run", "--config", target, "--json")
	if code != 0 || !strings.Contains(out, `"dryRun":true`) {
		t.Fatalf("dry-run force import exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	_, existing := configstore.Find(&targetDocument, "a")
	before := append([]byte(nil), existing.Spec.Config...)
	afterDryRun, err := configstore.New(target).Load()
	if err != nil {
		t.Fatal(err)
	}
	_, existing = configstore.Find(&afterDryRun, "a")
	if !bytes.Equal(before, existing.Spec.Config) {
		t.Fatal("dry-run import changed target")
	}
}

func TestValidateReportsInvalidSSHConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.json")
	rawConfig := json.RawMessage(`{"host":"me@example.com","forwards":[]}`)
	document := configstore.Document{
		APIVersion: core.APIVersion,
		Kind:       configstore.Kind,
		Items: []core.Connection{{
			APIVersion: core.APIVersion,
			Kind:       core.Kind,
			Metadata:   core.Metadata{Name: "broken"},
			Spec:       core.ConnectionSpec{Driver: sshdriver.ID, Lifecycle: "persistent", ConfigVersion: 1, Config: rawConfig},
		}},
	}
	if err := configstore.New(path).Write(context.Background(), document, false); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := invoke(t, "validate", path, "--json")
	if code != 1 || !strings.Contains(out, `"code":"invalid_driver_config"`) {
		t.Fatalf("validate exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestImportMissingFileIsNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	code, out, _ := invoke(t, "import", path, "--json")
	if code != 3 || !strings.Contains(out, `"code":"not_found"`) {
		t.Fatalf("exit=%d stdout=%s", code, out)
	}
}

func TestFailedImportDoesNotCreateTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.json")
	target := filepath.Join(root, "target.json")
	document := configstore.Document{
		APIVersion: core.APIVersion,
		Kind:       configstore.Kind,
		Items: []core.Connection{{
			APIVersion: core.APIVersion, Kind: core.Kind, Metadata: core.Metadata{Name: "broken"},
			Spec: core.ConnectionSpec{
				Driver: sshdriver.ID, Lifecycle: "persistent", ConfigVersion: 1,
				Config: json.RawMessage(`{"host":"me@example.com","forwards":[]}`),
			},
		}},
	}
	if err := configstore.New(source).Write(context.Background(), document, false); err != nil {
		t.Fatal(err)
	}
	code, out, _ := invoke(t, "import", source, "--config", target, "--json")
	if code != exitPartial || !strings.Contains(out, `"outcome":"failed"`) {
		t.Fatalf("exit=%d stdout=%s", code, out)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed import created target: %v", err)
	}
}

func TestCheckReturnsStableAuthenticationExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test provider is a POSIX executable script")
	}
	provider := filepath.Join(t.TempDir(), "fake-ssh")
	script := []byte("#!/bin/sh\necho 'Permission denied (publickey).' >&2\nexit 255\n")
	if err := os.WriteFile(provider, script, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNLITE_HOME", t.TempDir())
	t.Setenv("TUNLITE_SSH", provider)
	code, out, stderr := invoke(t, "check", "me@example.com", "--timeout", "5", "--json", "--non-interactive")
	if code != 4 || stderr != "" || !strings.Contains(out, `"code":"ssh_authentication_failed"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestCheckAcceptsEarlyAuthenticationSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test provider is a POSIX executable script")
	}
	provider := filepath.Join(t.TempDir(), "fake-ssh")
	script := []byte("#!/bin/sh\necho 'Authenticated to host using publickey.' >&2\nwhile :; do :; done\n")
	if err := os.WriteFile(provider, script, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNLITE_HOME", t.TempDir())
	t.Setenv("TUNLITE_SSH", provider)
	started := time.Now()
	code, out, stderr := invoke(t, "check", "me@example.com", "--timeout", "5", "--json", "--non-interactive")
	if code != 0 || stderr != "" || !strings.Contains(out, `"code":"ssh_authentication_succeeded"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("check did not stop promptly after authentication: %s", time.Since(started))
	}
}

func TestCheckReportsMissingOpenSSH(t *testing.T) {
	t.Setenv("TUNLITE_HOME", t.TempDir())
	t.Setenv("TUNLITE_SSH", filepath.Join(t.TempDir(), "missing-ssh"))
	code, out, stderr := invoke(t, "check", "me@example.com", "--json")
	if code != 1 || stderr != "" || !strings.Contains(out, `"code":"provider_missing"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestDoctorOfflineChecksPlanAndEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	code, _, stderr := invoke(t, "add", "doctor-db", "--to", "me@example.com", "-D", strconv.Itoa(port))
	if code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, stderr)
	}
	code, out, stderr := invoke(t, "doctor", "doctor-db", "--offline", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"code":"endpoint_available"`) {
		t.Fatalf("doctor exit=%d stdout=%s stderr=%s", code, out, stderr)
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

	deadline := time.Now().Add(5 * time.Second)
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
	case <-time.After(5 * time.Second):
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
