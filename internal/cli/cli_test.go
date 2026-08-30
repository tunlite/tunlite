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
	sshPath := setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "staging", "-p", "2222", "-L", "8080:localhost:80", "-D", "1080", "me@example.com"); code != 0 {
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
	if result.Plan.Mode != "direct" || result.Plan.Exec.Program != sshPath {
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
	setTestSSH(t)
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))
	if code, out, stderr := invoke(t, "add", "publish", "-R", "19000:localhost:3000", "me@example.com"); code != 0 {
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
	setTestSSH(t)
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))
	if code, out, stderr := invoke(t, "add", "public", "-D", "0.0.0.0:1080", "me@example.com"); code != 0 {
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
	nativeServices, ok := capabilities["nativeServices"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities omitted native service metadata: %#v", capabilities)
	}
	scope, ok := nativeServices["scope"].(map[string]any)
	if !ok || scope["default"] != "user" {
		t.Fatalf("unexpected native service scope contract: %#v", scope)
	}
	if elevation := scope["nonInteractiveElevation"]; elevation != "external" && elevation != "unsupported" {
		t.Fatalf("unexpected non-interactive elevation contract: %#v", scope)
	}
	supported, ok := scope["supported"].([]any)
	if !ok || len(supported) == 0 || supported[0] != "user" {
		t.Fatalf("user service scope is not reported: %#v", scope)
	}
	systemSupported := false
	for _, value := range supported {
		systemSupported = systemSupported || value == "system"
	}
	if migration, ok := scope["migration"].(bool); !ok || migration != systemSupported {
		t.Fatalf("scope migration overclaims platform support: %#v", scope)
	}
	drivers, ok := capabilities["drivers"].([]any)
	if !ok || len(drivers) != 1 {
		t.Fatalf("capabilities omitted SSH driver metadata: %#v", capabilities)
	}
	sshCapabilities, ok := drivers[0].(map[string]any)
	if !ok {
		t.Fatalf("invalid SSH driver capabilities: %#v", drivers[0])
	}
	clearFields, ok := sshCapabilities["setClearFields"].([]any)
	if !ok || len(clearFields) != 3 || clearFields[0] != "identity" || clearFields[1] != "jump" || clearFields[2] != "options" {
		t.Fatalf("unexpected SSH set clear contract: %#v", clearFields)
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

func TestSetHelpDocumentsClearableSSHFields(t *testing.T) {
	code, out, stderr := invoke(t, "set", "--help")
	if code != 0 || stderr != "" || !strings.Contains(out, "--clear identity|jump|options") {
		t.Fatalf("set help omitted clear fields: exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestEnableHelpDocumentsScopeWithoutPlatformOverclaim(t *testing.T) {
	for _, args := range [][]string{{"enable", "--help"}, {"enable", "--help", "--lang", "zh-CN"}} {
		code, out, stderr := invoke(t, args...)
		if code != 0 || stderr != "" {
			t.Fatalf("args=%v exit=%d stdout=%s stderr=%s", args, code, out, stderr)
		}
		for _, expected := range []string{"--scope user|system", "capabilities"} {
			if !strings.Contains(out, expected) {
				t.Fatalf("args=%v missing %q: %s", args, expected, out)
			}
		}
	}
}

func TestConnectionCRUDUsesVersionedStoreAndJSONContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))

	code, out, stderr := invoke(t, "add", "db", "-p", "2222",
		"-L", "5432:localhost:5432", "--group", "work", "me@example.com", "--json")
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

	code, out, stderr = invoke(t, "set", "db", "-D", "1080",
		"--no-group", "-o", "Compression=yes", "deploy@db.internal", "--json")
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

func TestSetClearsOptionalSSHFieldsWithoutChangingRequiredFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))

	if code, out, stderr := invoke(t, "add", "db", "-i", "~/.ssh/work", "-J", "jump@bastion",
		"-D", "1080", "-o", "Compression=yes", "deploy@app01", "--json"); code != 0 {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, stderr := invoke(t, "set", "db", "--clear", "identity", "--clear=jump", "--clear", "options", "--json")
	if code != 0 {
		t.Fatalf("set --clear exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var result struct {
		Connection core.Connection `json:"connection"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	config, err := sshdriver.DecodeConfig(result.Connection.Spec.Config)
	if err != nil {
		t.Fatal(err)
	}
	if config.IdentityFile != "" || len(config.Jump) != 0 || len(config.SSHOptions) != 0 {
		t.Fatalf("optional SSH fields were not cleared: %#v", config)
	}
	if config.Host != "deploy@app01" || len(config.Forwards) != 1 || config.Forwards[0].SrcPort != 1080 {
		t.Fatalf("set --clear changed required or unrelated fields: %#v", config)
	}
}

func TestInvalidSetClearDoesNotChangeDeclaration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))
	if code, out, stderr := invoke(t, "add", "db", "-i", "key", "-D", "1080", "deploy@app01", "--json"); code != 0 {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	store, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	beforeRaw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := invoke(t, "set", "db", "--clear", "identity", "--clear", "host", "--json")
	if code != 2 || !strings.Contains(out, "unknown --clear field") {
		t.Fatalf("invalid clear returned exit=%d stdout=%s", code, out)
	}
	after, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	afterRaw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatalf("invalid set --clear rewrote the declaration file\nbefore=%s\nafter=%s", beforeRaw, afterRaw)
	}
	_, beforeConnection := configstore.Find(&before, "db")
	_, afterConnection := configstore.Find(&after, "db")
	if beforeConnection == nil || afterConnection == nil || !connectionsEqual(*beforeConnection, *afterConnection) {
		t.Fatalf("invalid set --clear changed declaration: before=%#v after=%#v", beforeConnection, afterConnection)
	}
}

func TestConnectionCommandsUseOneDriverSelectionContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))

	if code, out, stderr := invoke(t, "add", "db", "--driver", "ssh", "-D", "1080", "deploy@app01", "--json"); code != 0 {
		t.Fatalf("explicit default driver failed: exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if code, out, _ := invoke(t, "add", "future", "--driver", "kubernetes", "service/db", "--json"); code != 2 || !strings.Contains(out, `"code":"invalid_arguments"`) {
		t.Fatalf("unknown driver was not rejected deterministically: exit=%d stdout=%s", code, out)
	}
	if code, out, _ := invoke(t, "add", "internal-id", "--driver", sshdriver.ID, "-D", "1081", "deploy@app01", "--json"); code != 2 || !strings.Contains(out, `"code":"invalid_arguments"`) {
		t.Fatalf("internal driver ID became a second public spelling: exit=%d stdout=%s", code, out)
	}
	if code, out, _ := invoke(t, "set", "db", "--driver", "ssh", "--json"); code != 2 || !strings.Contains(out, "driver_change") && !strings.Contains(out, "cannot change") {
		t.Fatalf("set accepted a driver change surface: exit=%d stdout=%s", code, out)
	}
	if code, out, _ := invoke(t, "add", "legacy", "--to", "deploy@app01", "-D", "1082", "--json"); code != 2 || !strings.Contains(out, "unsupported SSH argument") {
		t.Fatalf("removed --to syntax was accepted: exit=%d stdout=%s", code, out)
	}
}

func TestHumanListAndStatusAreGroupedByBusinessOwnership(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))

	for _, args := range [][]string{
		{"add", "prod-db", "-D", "1081", "--group", "prod", "me@example.com"},
		{"add", "prod-api", "-D", "1082", "--group", "prod", "me@example.com"},
		{"add", "personal", "-D", "1083", "me@example.com"},
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
	code, out, stderr := invoke(t, "add", "preview", "-D", "1080", "--dry-run", "--json", "me@example.com")
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

func TestGlobalConfigFlagIsNotSupported(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))
	path := filepath.Join(t.TempDir(), "project", "tunlite.json")
	code, out, stderr := invoke(t, "add", "project-db", "-D", "1080", "--config", path, "--json", "me@example.com")
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
	setTestSSH(t)
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))
	code, _, stderr := invoke(t, "add", "named-db", "-L", "15432:localhost:5432", "me@example.com")
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
	setTestSSH(t)
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))
	code, _, stderr := invoke(t, "add", "first", "-L", "18080:localhost:80", "me@one")
	if code != 0 {
		t.Fatalf("first add exit %d, stderr=%s", code, stderr)
	}
	code, out, stderr := invoke(t, "add", "second", "-D", "18080", "--json", "me@two")
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
	setTestSSH(t)
	invoke := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(root, "source-native")))
	target := filepath.Join(root, "target-home", "config", "connections.json")
	project := filepath.Join(root, "project.json")

	for _, item := range []struct {
		name string
		port string
	}{{"a", "1080"}, {"b", "1081"}} {
		code, out, stderr := invoke(t, "add", item.name, "-D", item.port, "--json", "me@example.com")
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
	code, _, stderr = invoke(t, "add", "a", "-D", "2080", "old@example.com")
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
	setTestSSH(t)
	code, _, stderr := invoke(t, "add", "doctor-db", "-D", strconv.Itoa(port), "me@example.com")
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
	stoppedMarker := filepath.Join(temp, "stopped")
	provider, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNLITE_SSH", provider)
	t.Setenv(foregroundProviderMarker, marker)
	t.Setenv(foregroundProviderStopped, stoppedMarker)
	t.Setenv("TUNLITE_HOME", temp)
	adapter := newFakeServiceAdapter(filepath.Join(temp, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "foreground", "-D", "1080", "me@example.com"); code != 0 {
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
	if _, err := os.Stat(stoppedMarker); err != nil {
		t.Fatalf("foreground provider exited without observing termination: %v", err)
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

func TestRunRejectsAnonymousSSHArgumentsWithUsageExit(t *testing.T) {
	var out, stderr bytes.Buffer
	code := Run([]string{"run", "me@example.com", "-D", "1080", "--json"}, IO{Out: &out, Err: &stderr})
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
