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
	"github.com/yuanyuanzijin/tunlite/internal/service"
)

func TestRunDryRunProducesVersionedExecutionPlan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "staging", "--to", "me@example.com:2222", "-L", "8080:localhost:80", "-D", "1080"); code != 0 {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var out, stderr bytes.Buffer
	code := runContext(context.Background(), []string{
		"run", "staging", "--json", "--non-interactive", "--dry-run",
	}, IO{Out: &out, Err: &stderr}, func() (service.Adapter, error) { return adapter, nil })
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s, stdout=%s", code, stderr.String(), out.String())
	}
	var result struct {
		ProtocolVersion int    `json:"protocolVersion"`
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
	if result.ProtocolVersion != ProtocolVersion || result.Outcome != "planned" || !result.DryRun {
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

func TestRunDoesNotBlockAcceptedPublishDeclaration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/true")
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))
	if code, out, stderr := invoke(t, "add", "publish", "--to", "me@example.com", "-R", "19000:localhost:3000"); code != 0 {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, stderr := invoke(t,
		"run", "publish",
		"--json", "--non-interactive",
	)
	if code != 0 || stderr != "" {
		t.Fatalf("run exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if strings.Contains(out, "approval_required") || !strings.Contains(out, `"outcome":"starting"`) || !strings.Contains(out, `"outcome":"stopped"`) {
		t.Fatalf("run was blocked or omitted lifecycle events: %s", out)
	}
}

func TestRunDryRunReportsPublicListenRisk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))
	if code, out, stderr := invoke(t, "add", "public", "--to", "me@example.com", "-D", "0.0.0.0:1080"); code != 0 {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, stderr := invoke(t,
		"run", "public",
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
	if result.Outcome != "planned" || len(result.Conditions) != 1 || result.Conditions[0].Code != "network_exposure" {
		t.Fatalf("unexpected plan conditions: %#v", result)
	}
	actions := result.Conditions[0].Actions
	if len(actions) != 0 {
		t.Fatalf("risk warning unexpectedly required an action: %#v", actions)
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
	if result["protocolVersion"] != float64(ProtocolVersion) || result["version"] != Version {
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
	if _, exists := schemaDocument["protocolVersion"]; exists {
		t.Fatalf("raw schema was wrapped in a CLI response: %#v", schemaDocument)
	}
	code, out, stderr = invoke(t, "schema", "connection", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("schema JSON exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var schemaResponse struct {
		ProtocolVersion int            `json:"protocolVersion"`
		Outcome         string         `json:"outcome"`
		Schema          map[string]any `json:"schema"`
	}
	if err := json.Unmarshal([]byte(out), &schemaResponse); err != nil {
		t.Fatal(err)
	}
	if schemaResponse.ProtocolVersion != ProtocolVersion || schemaResponse.Outcome != "succeeded" ||
		schemaResponse.Schema["$id"] != "https://tunlite.dev/schemas/connection-v1.schema.json" {
		t.Fatalf("unexpected schema response: %#v", schemaResponse)
	}
	schemaPath := filepath.Join(t.TempDir(), "connection.schema.json")
	code, out, stderr = invoke(t, "schema", "connection", "--output", schemaPath, "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"protocolVersion":1`) {
		t.Fatalf("schema output exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	writtenSchema, err := os.ReadFile(schemaPath)
	if err != nil || !bytes.Contains(writtenSchema, []byte(`"$id": "https://tunlite.dev/schemas/connection-v1.schema.json"`)) {
		t.Fatalf("schema output was not raw: %s err=%v", writtenSchema, err)
	}
	code, out, stderr = invoke(t, "capabilities", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("capabilities exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var capabilities map[string]any
	if err := json.Unmarshal([]byte(out), &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities["protocolVersion"] != float64(ProtocolVersion) {
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
		NativeServices struct {
			Available bool `json:"available"`
		} `json:"nativeServices"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.NativeServices.Available {
		t.Fatal("capabilities claimed a native service manager was available with an empty PATH")
	}
}

func TestConnectionCRUDUsesVersionedStoreAndJSONContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))

	code, out, stderr := invoke(t, "add", "db", "--to", "me@example.com:2222",
		"-L", "5432:localhost:5432", "--group", "work", "--json")
	if code != 0 {
		t.Fatalf("add exit %d, stdout=%s stderr=%s", code, out, stderr)
	}
	var added struct {
		ProtocolVersion int             `json:"protocolVersion"`
		Outcome         string          `json:"outcome"`
		Connection      core.Connection `json:"connection"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil {
		t.Fatal(err)
	}
	if added.ProtocolVersion != ProtocolVersion || added.Outcome != "applied" || added.Connection.Metadata.Name != "db" {
		t.Fatalf("unexpected add result: %#v", added)
	}

	code, out, stderr = invoke(t, "list", "--group", "work", "--json")
	if code != 0 {
		t.Fatalf("list exit %d, stdout=%s stderr=%s", code, out, stderr)
	}
	var listed struct {
		ProtocolVersion int               `json:"protocolVersion"`
		Results         []core.Connection `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.ProtocolVersion != ProtocolVersion || len(listed.Results) != 1 {
		t.Fatalf("unexpected list result: %#v", listed)
	}

	code, out, stderr = invoke(t, "set", "db", "--to", "deploy@db.internal", "-D", "1080",
		"--no-group", "--ssh-opt", "Compression=yes", "--json")
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
	if updated.Connection.Metadata.Group != "" {
		t.Fatalf("group was not cleared: %q", updated.Connection.Metadata.Group)
	}

	code, _, stderr = invoke(t, "rename", "db", "database")
	if code != 0 {
		t.Fatalf("rename exit %d, stderr=%s", code, stderr)
	}
	code, out, stderr = invoke(t, "remove", "database", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("dry-run remove exit %d, stdout=%s stderr=%s", code, out, stderr)
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
	code, _, stderr = invoke(t, "remove", "database")
	if code != 0 {
		t.Fatalf("remove exit %d, stderr=%s", code, stderr)
	}
	code, out, _ = invoke(t, "remove", "database", "--json")
	if code != 3 || !strings.Contains(out, `"code":"not_found"`) {
		t.Fatalf("unexpected not-found result: exit=%d stdout=%s", code, out)
	}
}

func TestHumanListAndStatusAreGroupedByBusinessOwnership(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))

	for _, args := range [][]string{
		{"add", "prod-db", "--to", "me@example.com", "-D", "1081", "--group", "prod"},
		{"add", "prod-api", "--to", "me@example.com", "-D", "1082", "--group", "prod"},
		{"add", "personal", "--to", "me@example.com", "-D", "1083"},
	} {
		if code, out, stderr := invoke(t, args...); code != 0 {
			t.Fatalf("%v exit=%d stdout=%s stderr=%s", args, code, out, stderr)
		}
	}

	for _, args := range [][]string{{"list"}, {"status"}} {
		code, out, stderr := invoke(t, args...)
		if code != 0 || stderr != "" {
			t.Fatalf("%v exit=%d stdout=%s stderr=%s", args, code, out, stderr)
		}
		prod := strings.Index(out, "Group prod · 2 connections")
		ungrouped := strings.Index(out, "Group Ungrouped · 1 connection")
		if prod < 0 || ungrouped < 0 || prod > ungrouped {
			t.Fatalf("%v did not group named ownership before ungrouped connections:\n%s", args, out)
		}
	}

	code, out, stderr := invoke(t, "status", "--group", "prod", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"connection":"prod-db"`) ||
		!strings.Contains(out, `"connection":"prod-api"`) || strings.Contains(out, `"connection":"personal"`) {
		t.Fatalf("group selection exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestAddDryRunDoesNotCreateConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	code, out, stderr := invoke(t, "add", "preview", "--to", "me@example.com", "-D", "1080", "--dry-run", "--json")
	if code != 0 || !strings.Contains(out, `"outcome":"planned"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	path := filepath.Join(home, "config", "connections.json")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created local state: %v", err)
	}
}

func TestLegacyConfigRequiresExplicitImportWithoutChangingIt(t *testing.T) {
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
	if code != 1 || !strings.Contains(out, `"code":"legacy_import_required"`) ||
		!strings.Contains(out, `"type":"import_legacy_config"`) ||
		!strings.Contains(out, `tunlite import`) ||
		!strings.Contains(out, `"type":"review_upgrade_guide"`) {
		t.Fatalf("exit=%d stdout=%s", code, out)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("legacy config was overwritten")
	}
	if _, err := os.Stat(filepath.Join(home, "config", "connections.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only list created v1 config: %v", err)
	}
}

func TestImportAcceptsFrozenZeroXConfigWithoutChangingSourceOrRuntime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))
	legacyPath := filepath.Join(home, "config", "config.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{
  "version": 1,
  "settings": {"keepalive":{"intervalSec":9}},
  "tunnels": [{
    "name": "db", "host": "me@example.com", "port": 2222,
    "identityFile": "~/.ssh/id_ed25519", "jump": "me@bastion:2200",
		"tags": ["work"],
    "forwards": [{"type":"local","bind":"127.0.0.1","srcPort":5432,"destHost":"localhost","destPort":5432}],
    "sshOptions": ["-o", "Compression=yes"], "enabled": true, "autoSetupKey": true
  }]
}`)
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, stderr := invoke(t, "import", legacyPath, "--json", "--non-interactive")
	if code != 0 || stderr != "" || !strings.Contains(out, `"sourceFormat":"tunlite-0.x"`) ||
		!strings.Contains(out, `"code":"legacy_settings_ignored"`) ||
		!strings.Contains(out, `"code":"legacy_running_state_ignored"`) {
		t.Fatalf("import exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	unchanged, err := os.ReadFile(legacyPath)
	if err != nil || !bytes.Equal(unchanged, legacy) {
		t.Fatalf("legacy source changed: err=%v", err)
	}
	document, err := configstore.New(filepath.Join(home, "config", "connections.json")).Load()
	if err != nil || len(document.Items) != 1 {
		t.Fatalf("V1 config items=%d err=%v", len(document.Items), err)
	}
	connection := document.Items[0]
	if connection.Metadata.Group != "work" {
		t.Fatalf("legacy tag was not imported as a group: %q", connection.Metadata.Group)
	}
	sshConfig, err := sshdriver.DecodeConfig(connection.Spec.Config)
	if err != nil || sshConfig.Port != 2222 || len(sshConfig.Jump) != 1 ||
		len(sshConfig.SSHOptions) != 1 || sshConfig.SSHOptions[0] != "Compression=yes" {
		t.Fatalf("legacy SSH fields were not imported: config=%#v err=%v", sshConfig, err)
	}
	if code, out, stderr = invoke(t, "status", "db", "--json"); code != 0 || stderr != "" || !strings.Contains(out, `"mode":"stopped"`) {
		t.Fatalf("imported connection was not stopped: exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestImportRealZeroTenThreeExportFixtureIsReadOnlyAndStopped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	legacyPath := filepath.Join("..", "..", "testdata", "legacy", "tunlite-0.10.3-export.json")
	original, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(home, "config", "connections.json")

	code, out, stderr := invoke(t, "import", legacyPath, "--dry-run", "--json", "--non-interactive")
	if code != 0 || stderr != "" || !strings.Contains(out, `"sourceFormat":"tunlite-0.x"`) ||
		!strings.Contains(out, `"dryRun":true`) {
		t.Fatalf("dry-run exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if _, err := os.Stat(targetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created V1 config: %v", err)
	}
	if len(adapter.artifacts) != 0 {
		t.Fatalf("dry-run created native artifacts: %#v", adapter.artifacts)
	}

	code, out, stderr = invoke(t, "import", legacyPath, "--json", "--non-interactive")
	if code != 0 || stderr != "" || !strings.Contains(out, `"outcome":"applied"`) {
		t.Fatalf("import exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	after, err := os.ReadFile(legacyPath)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("real 0.10.3 fixture changed: err=%v", err)
	}
	document, err := configstore.New(targetPath).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Items) != 4 {
		t.Fatalf("imported %d connections, want 4", len(document.Items))
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"rev", "socks", "multi", "web-9000"} {
		record, exists, err := records.Load(name)
		if err != nil || !exists || record.Mode != service.ModeStopped {
			t.Fatalf("%s record=%#v exists=%t err=%v", name, record, exists, err)
		}
		observation := adapter.observed[name]
		if !observation.Installed || observation.ProcessState != service.ProcessInactive || observation.AutoStart {
			t.Fatalf("%s was not imported as a stopped service: %#v", name, observation)
		}
	}
}

func TestGlobalConfigFlagIsNotSupported(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))
	path := filepath.Join(t.TempDir(), "project", "tunlite.json")
	code, out, stderr := invoke(t, "add", "project-db", "--to", "me@example.com", "-D", "1080", "--config", path, "--json")
	if code != 2 || !strings.Contains(out, `"code":"invalid_arguments"`) {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, stderr = invoke(t, "version", "--config", path, "--json")
	if code != 2 || !strings.Contains(out, `"code":"invalid_arguments"`) {
		t.Fatalf("version exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestNamedConnectionRunBuildsForegroundPlan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))
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
		} `json:"connection"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "planned" || result.Connection.Metadata.Name != "named-db" {
		t.Fatalf("unexpected named run plan: %#v", result)
	}
}

func TestEndpointConflictIsStructuredWarning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))
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
	t.Setenv("TUNLITE_HOME", filepath.Join(root, "source-home"))
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(root, "source-native")))
	target := filepath.Join(root, "target-home", "config", "connections.json")
	project := filepath.Join(root, "project.json")

	for _, item := range []struct {
		name string
		port string
	}{{"a", "1080"}, {"b", "1081"}} {
		code, out, stderr := invoke(t, "add", item.name, "--to", "me@example.com", "-D", item.port, "--json")
		if code != 0 {
			t.Fatalf("source add exit=%d stdout=%s stderr=%s", code, out, stderr)
		}
	}
	code, out, stderr := invoke(t, "export", "--output", project, "--json")
	if code != 0 {
		t.Fatalf("export exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	document, err := configstore.New(project).Load()
	if err != nil || len(document.Items) != 2 {
		t.Fatalf("exported document invalid: items=%d err=%v", len(document.Items), err)
	}
	code, out, stderr = invoke(t, "export", "--output", "-")
	if code != 0 || stderr != "" {
		t.Fatalf("raw export exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var rawExport map[string]any
	if err := json.Unmarshal([]byte(out), &rawExport); err != nil {
		t.Fatal(err)
	}
	if rawExport["configVersion"] != float64(core.ConfigVersion) || rawExport["protocolVersion"] != nil {
		t.Fatalf("raw export used the wrong envelope: %#v", rawExport)
	}
	code, out, stderr = invoke(t, "export", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("JSON export exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var exportResponse struct {
		ProtocolVersion int                  `json:"protocolVersion"`
		Document        configstore.Document `json:"document"`
	}
	if err := json.Unmarshal([]byte(out), &exportResponse); err != nil {
		t.Fatal(err)
	}
	if exportResponse.ProtocolVersion != ProtocolVersion || exportResponse.Document.ConfigVersion != core.ConfigVersion {
		t.Fatalf("JSON export used the wrong envelopes: %#v", exportResponse)
	}
	code, out, stderr = invoke(t, "validate", project, "--json")
	if code != 0 || !strings.Contains(out, `"outcome":"succeeded"`) {
		t.Fatalf("validate exit=%d stdout=%s stderr=%s", code, out, stderr)
	}

	t.Setenv("TUNLITE_HOME", filepath.Join(root, "target-home"))
	invoke = useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(root, "target-native")))
	code, _, stderr = invoke(t, "add", "a", "--to", "old@example.com", "-D", "2080")
	if code != 0 {
		t.Fatalf("target add exit=%d stderr=%s", code, stderr)
	}
	code, out, stderr = invoke(t, "import", project, "--json")
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
	for _, result := range imported.Results {
		if result.Outcome == "failed" && (result.RiskLevel == "" || result.Actions == nil) {
			t.Fatalf("failed import item is missing its machine error contract: %#v", result)
		}
	}
	targetDocument, err := configstore.New(target).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(targetDocument.Items) != 2 {
		t.Fatalf("partial import did not preserve successes: %#v", targetDocument.Items)
	}

	code, out, stderr = invoke(t, "import", project, "--force", "--dry-run", "--json")
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
		ConfigVersion: core.ConfigVersion,
		Kind:          configstore.Kind,
		Items: []core.Connection{{
			ConfigVersion: core.ConfigVersion,
			Kind:          core.Kind,
			Metadata:      core.Metadata{Name: "broken"},
			Spec:          core.ConnectionSpec{Driver: sshdriver.ID, Config: rawConfig},
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
	home := filepath.Join(root, "target-home")
	t.Setenv("TUNLITE_HOME", home)
	target := filepath.Join(home, "config", "connections.json")
	document := configstore.Document{
		ConfigVersion: core.ConfigVersion,
		Kind:          configstore.Kind,
		Items: []core.Connection{{
			ConfigVersion: core.ConfigVersion, Kind: core.Kind, Metadata: core.Metadata{Name: "broken"},
			Spec: core.ConnectionSpec{
				Driver: sshdriver.ID,
				Config: json.RawMessage(`{"host":"me@example.com","forwards":[]}`),
			},
		}},
	}
	if err := configstore.New(source).Write(context.Background(), document, false); err != nil {
		t.Fatal(err)
	}
	code, out, _ := invoke(t, "import", source, "--json")
	if code != 1 || !strings.Contains(out, `"outcome":"failed"`) {
		t.Fatalf("exit=%d stdout=%s", code, out)
	}
	var response struct {
		Results []itemResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].RiskLevel != "low" || response.Results[0].Actions == nil {
		t.Fatalf("failed import item is not structured: %#v", response.Results)
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
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
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
	t.Setenv("TUNLITE_HOME", temp)
	adapter := newFakeServiceAdapter(filepath.Join(temp, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "foreground", "--to", "me@example.com", "-D", "1080"); code != 0 {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runContext(ctx, []string{
			"run", "foreground", "--json", "--non-interactive",
		}, IO{Out: &out, Err: &stderr}, func() (service.Adapter, error) { return adapter, nil })
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
	if result["protocolVersion"] != float64(ProtocolVersion) {
		t.Fatalf("missing contract version: %#v", result)
	}
}
