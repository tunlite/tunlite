package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yuanyuanzijin/tunlite/internal/configstore"
	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
	"github.com/yuanyuanzijin/tunlite/internal/service"
)

func TestUnsupportedDryRunCommandsFailBeforeDispatch(t *testing.T) {
	tests := [][]string{
		{"check", "me@example.com", "--dry-run"},
		{"doctor", "--dry-run"},
		{"logs", "db", "--follow", "--dry-run"},
		{"status", "--dry-run"},
		{"list", "--dry-run"},
		{"validate", "--dry-run"},
		{"capabilities", "--dry-run"},
		{"help", "--dry-run"},
		{"version", "--dry-run"},
	}
	for _, args := range tests {
		t.Run(args[0], func(t *testing.T) {
			called := 0
			var out, stderr bytes.Buffer
			code := runContextWithOptions(context.Background(), args, IO{Out: &out, Err: &stderr}, options{
				serviceAdapter: func() (service.Adapter, error) {
					called++
					return nil, errors.New("must not be called")
				},
			})
			if code != 2 || called != 0 || !strings.Contains(stderr.String(), "does not support --dry-run") {
				t.Fatalf("args=%v exit=%d adapterCalls=%d stdout=%s stderr=%s", args, code, called, out.String(), stderr.String())
			}
		})
	}
}

func TestDocumentDryRunRequiresARealOutputFile(t *testing.T) {
	t.Setenv("TUNLITE_HOME", t.TempDir())
	for _, args := range [][]string{
		{"export", "--dry-run"}, {"export", "--output", "-", "--dry-run"},
		{"schema", "--dry-run"}, {"schema", "--output", "-", "--dry-run"},
	} {
		code, out, stderr := invoke(t, args...)
		if code != 2 || !strings.Contains(out+stderr, "requires --output <file>") {
			t.Fatalf("args=%v exit=%d stdout=%s stderr=%s", args, code, out, stderr)
		}
	}
	for _, command := range []string{"export", "schema"} {
		path := filepath.Join(t.TempDir(), command+".json")
		code, out, stderr := invoke(t, command, "--output", path, "--dry-run", "--json")
		if code != 0 || stderr != "" || !strings.Contains(out, `"outcome":"planned"`) {
			t.Fatalf("command=%s exit=%d stdout=%s stderr=%s", command, code, out, stderr)
		}
	}
}

func TestGlobalOptionsStopAtDoubleDashAndRejectRepeatedLanguage(t *testing.T) {
	cleaned, opts, err := extractGlobalOptions([]string{"add", "db", "--json", "--json", "--", "--dry-run", "--lang", "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"add", "db", "--", "--dry-run", "--lang", "zh-CN"}
	if !reflect.DeepEqual(cleaned, want) || !opts.JSON || opts.DryRun || opts.Language != languageEnglish {
		t.Fatalf("cleaned=%q opts=%+v", cleaned, opts)
	}
	if containsHelpFlag([]string{"--", "--help"}) {
		t.Fatal("--help after -- was treated as a global help request")
	}
	for _, args := range [][]string{
		{"version", "--lang", "en", "--lang", "en"},
		{"version", "--lang=en", "--lang=zh-CN"},
	} {
		if _, _, err := extractGlobalOptions(args); err == nil || !strings.Contains(err.Error(), "only be specified once") {
			t.Fatalf("repeated language was accepted: %v (%v)", args, err)
		}
	}
}

type countingReader struct{ calls int }

func (reader *countingReader) Read([]byte) (int, error) {
	reader.calls++
	return 0, io.EOF
}

func TestManageWithoutTargetRequiresTTYBeforeDiscoveryOrInput(t *testing.T) {
	for _, args := range [][]string{{"manage"}, {"manage", "--non-interactive"}} {
		input := &countingReader{}
		adapterCalls := 0
		var out, stderr bytes.Buffer
		code := runContextWithOptions(context.Background(), args, IO{In: input, Out: &out, Err: &stderr}, options{
			serviceAdapter: func() (service.Adapter, error) {
				adapterCalls++
				return nil, errors.New("must not be called")
			},
		})
		if code != 4 || input.calls != 0 || adapterCalls != 0 {
			t.Fatalf("args=%v exit=%d inputReads=%d adapterCalls=%d stdout=%s stderr=%s", args, code, input.calls, adapterCalls, out.String(), stderr.String())
		}
	}
	code, _, _ := invoke(t, "manage", "--unknown", "--non-interactive")
	if code != 2 {
		t.Fatalf("manage syntax error exit=%d, want 2", code)
	}
}

func TestManageRequiresVisibleStderrPromptBeforeReadingInput(t *testing.T) {
	input := &countingReader{}
	terminal, promptTerminal := true, false
	adapterCalls := 0
	var out, stderr bytes.Buffer
	code := runContextWithOptions(context.Background(), []string{"manage"}, IO{
		In: input, Out: &out, Err: &stderr, Terminal: &terminal, PromptTerminal: &promptTerminal,
	}, options{serviceAdapter: func() (service.Adapter, error) {
		adapterCalls++
		return nil, errors.New("must not be called")
	}})
	if code != 4 || input.calls != 0 || adapterCalls != 0 || !strings.Contains(stderr.String(), "interactive terminal") {
		t.Fatalf("exit=%d inputReads=%d adapterCalls=%d stdout=%s stderr=%s", code, input.calls, adapterCalls, out.String(), stderr.String())
	}
}

func TestSetEnvIsRejectedWithoutLeakingItsValue(t *testing.T) {
	const secret = "never-print-this-secret"
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	invokeFake := useFakeServiceAdapter(t, newFakeServiceAdapter(filepath.Join(home, "native")))

	code, out, stderr := invokeFake(t, "add", "bad", "-D", "1080", "-o", "SetEnv=TOKEN="+secret, "--json", "me@example.com")
	if code != 2 || strings.Contains(out+stderr, secret) || !strings.Contains(out, `"code":"invalid_arguments"`) {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if code, out, stderr = invokeFake(t, "add", "db", "-D", "1080", "me@example.com"); code != 0 {
		t.Fatalf("setup add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, stderr = invokeFake(t, "set", "db", "-o", "SetEnv=TOKEN="+secret, "--dry-run", "--json")
	if code != 2 || strings.Contains(out+stderr, secret) {
		t.Fatalf("set exit=%d stdout=%s stderr=%s", code, out, stderr)
	}

	invalid := core.Connection{
		ConfigVersion: core.ConfigVersion, Kind: core.Kind, Metadata: core.Metadata{Name: "imported"},
		Spec: core.ConnectionSpec{Driver: sshdriver.ID, Config: json.RawMessage(`{"host":"me@example.com","forwards":[{"type":"dynamic","bind":"127.0.0.1","srcPort":1081}],"sshOptions":["SetEnv=TOKEN=` + secret + `"]}`)},
	}
	source := filepath.Join(t.TempDir(), "connections.json")
	if err := configstore.New(source).Write(context.Background(), configstore.Document{
		ConfigVersion: core.ConfigVersion, Kind: configstore.Kind, Items: []core.Connection{invalid},
	}, false); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = invokeFake(t, "import", source, "--dry-run", "--json")
	if code != 1 || strings.Contains(out+stderr, secret) || !strings.Contains(out, "SetEnv") {
		t.Fatalf("import exit=%d stdout=%s stderr=%s", code, out, stderr)
	}

	store, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(document *configstore.Document) error {
		document.Items = append(document.Items, invalid)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = invokeFake(t, "run", "imported", "--dry-run", "--json")
	if code == 0 || strings.Contains(out+stderr, secret) || !strings.Contains(out, "SetEnv") {
		t.Fatalf("plan exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestCapabilitiesNamesExitCodeFourAsInteractionRequired(t *testing.T) {
	code, out, stderr := invoke(t, "capabilities", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var response struct {
		ExitCodes map[string]int `json:"exitCodes"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if response.ExitCodes["interactionRequired"] != 4 {
		t.Fatalf("exit codes=%v", response.ExitCodes)
	}
	if _, exists := response.ExitCodes["authenticationFailed"]; exists {
		t.Fatalf("legacy narrow exit code name remains: %v", response.ExitCodes)
	}
	validateCLIResponseSchema(t, []byte(out))
}

func TestConnectionMutationsReportProgressOnlyAfterValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	terminal := true
	invokeTTY := func(args ...string) (int, string, string) {
		var out, stderr bytes.Buffer
		code := runContext(context.Background(), args, IO{
			Out: &out, Err: &stderr, Terminal: &terminal,
		}, func() (service.Adapter, error) { return adapter, nil })
		return code, out.String(), stderr.String()
	}

	if code, _, stderr := invokeTTY("add"); code != 2 || strings.Contains(stderr, "Adding") {
		t.Fatalf("invalid add showed progress: exit=%d stderr=%q", code, stderr)
	}
	if code, out, stderr := invokeTTY("add", "db", "-D", "1080", "me@example.com"); code != 0 || !strings.Contains(stderr, "Adding db…") {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if code, out, stderr := invokeTTY("set", "db", "--group", "work"); code != 0 || !strings.Contains(stderr, "Updating db…") {
		t.Fatalf("set exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if code, out, stderr := invokeTTY("rename", "db", "renamed"); code != 0 || !strings.Contains(stderr, "Renaming db…") {
		t.Fatalf("rename exit=%d stdout=%s stderr=%s", code, out, stderr)
	}

	store, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "connections.json")
	if err := configstore.New(source).Write(context.Background(), document, false); err != nil {
		t.Fatal(err)
	}

	if code, out, stderr := invokeTTY("remove", "renamed"); code != 0 || !strings.Contains(stderr, "Removing renamed…") {
		t.Fatalf("remove exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if code, out, stderr := invokeTTY("import", source); code != 0 ||
		!strings.Contains(stderr, "Reading and validating import file ") || !strings.Contains(stderr, "Importing "+source+"…") {
		t.Fatalf("import exit=%d stdout=%s stderr=%s", code, out, stderr)
	}

	if code, out, stderr := invokeTTY("add", "json", "-D", "1081", "me@example.com", "--json"); code != 0 || stderr != "" || !strings.Contains(out, `"outcome":"applied"`) {
		t.Fatalf("JSON add progress leaked: exit=%d stdout=%s stderr=%q", code, out, stderr)
	}
	if code, out, stderr := invokeTTY("add", "cn", "-D", "1082", "me@example.com", "--lang", "zh-CN"); code != 0 || !strings.Contains(stderr, "正在添加 cn…") || !strings.Contains(out, "已添加") {
		t.Fatalf("Chinese add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestSetAndUnmanageHelpExplainDestructiveSemantics(t *testing.T) {
	tests := []struct {
		args []string
		want []string
	}{
		{[]string{"help", "set"}, []string{"entire forwarding list", "entire SSH options list", "briefly restart"}},
		{[]string{"help", "set", "--lang", "zh-CN"}, []string{"整体替换现有转发列表", "整体替换现有 SSH 选项", "短暂重启"}},
		{[]string{"help", "unmanage"}, []string{"native service remains installed", "autostart setting stay unchanged"}},
		{[]string{"help", "unmanage", "--lang", "zh-CN"}, []string{"原生后台服务保持安装", "自动启动设置不变"}},
	}
	for _, test := range tests {
		code, out, stderr := invoke(t, test.args...)
		if code != 0 || stderr != "" {
			t.Fatalf("args=%v exit=%d stdout=%s stderr=%s", test.args, code, out, stderr)
		}
		for _, expected := range test.want {
			if !strings.Contains(out, expected) {
				t.Fatalf("args=%v missing %q:\n%s", test.args, expected, out)
			}
		}
	}
	code, out, stderr := invoke(t, "help", "set", "--json", "--lang", "zh-CN")
	if code != 0 || stderr != "" || strings.Contains(out, "整体替换") || !strings.Contains(out, `"usage"`) {
		t.Fatalf("JSON help mixed human prose: exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestChineseDoctorAndUnmanageHumanOutputIsActionable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invokeFake := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invokeFake(t, "add", "db", "-D", "1080", "me@example.com"); code != 0 {
		t.Fatalf("setup add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if code, out, stderr := invokeFake(t, "doctor", "--offline", "--lang", "zh-CN"); code != 0 ||
		!strings.Contains(out, "配置有效") || !strings.Contains(out, "项检查") || !strings.Contains(out, "项通过") || strings.Contains(out, "checks:") {
		t.Fatalf("Chinese doctor exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if code, out, stderr := invokeFake(t, "unmanage", "db", "--lang", "zh-CN"); code != 0 ||
		!strings.Contains(out, "原生后台服务仍保持安装") || !strings.Contains(out, "自动启动设置不变") {
		t.Fatalf("Chinese unmanage exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestRenamePublishesPreservedNativeIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invokeFake := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invokeFake(t, "add", "old", "-D", "1080", "me@example.com"); code != 0 {
		t.Fatalf("setup add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	before, exists, err := records.Load("old")
	if err != nil || !exists {
		t.Fatalf("record exists=%t err=%v", exists, err)
	}
	serviceKey := before.ServiceKey
	if serviceKey == "" {
		serviceKey = before.Connection
	}
	if code, out, stderr := invokeFake(t, "rename", "old", "new"); code != 0 ||
		!strings.Contains(out, "Connection name  new") || !strings.Contains(out, "Native identity  "+before.UnitName) ||
		!strings.Contains(out, "Service key  "+serviceKey) || !strings.Contains(out, "not interrupted") {
		t.Fatalf("rename exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if code, out, stderr := invokeFake(t, "rename", "new", "future", "--dry-run", "--json"); code != 0 || stderr != "" ||
		!strings.Contains(out, `"connectionName":"future"`) || !strings.Contains(out, `"serviceKey":"`+serviceKey+`"`) ||
		!strings.Contains(out, `"unitName":"`+before.UnitName+`"`) || !strings.Contains(out, `"nativeIdentity":"`+before.UnitName+`"`) {
		t.Fatalf("JSON rename exit=%d stdout=%s stderr=%s", code, out, stderr)
	} else {
		validateCLIResponseSchema(t, []byte(out))
	}
	if code, out, stderr := invokeFake(t, "status", "new"); code != 0 ||
		!strings.Contains(out, "native identity") || !strings.Contains(out, before.UnitName) ||
		!strings.Contains(out, "service key") || !strings.Contains(out, serviceKey) {
		t.Fatalf("status exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	for _, test := range []struct {
		args []string
		want []string
	}{
		{[]string{"help", "rename"}, []string{"serviceKey", "unitName", "native identity", "not interrupted"}},
		{[]string{"help", "rename", "--lang", "zh-CN"}, []string{"serviceKey", "unitName", "原生服务标识保持不变", "不会中断"}},
	} {
		code, out, stderr := invokeFake(t, test.args...)
		if code != 0 || stderr != "" {
			t.Fatalf("help args=%v exit=%d stdout=%s stderr=%s", test.args, code, out, stderr)
		}
		for _, want := range test.want {
			if !strings.Contains(out, want) {
				t.Fatalf("help args=%v missing %q:\n%s", test.args, want, out)
			}
		}
	}
}

func TestStatusReportsProgressAfterSelectionAndKeepsJSONClean(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invokeFake := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invokeFake(t, "add", "db", "-D", "1080", "me@example.com"); code != 0 {
		t.Fatalf("setup add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	terminal := true
	invokeTTY := func(args ...string) (int, string, string) {
		var out, stderr bytes.Buffer
		code := runContext(context.Background(), args, IO{Out: &out, Err: &stderr, Terminal: &terminal},
			func() (service.Adapter, error) { return adapter, nil })
		return code, out.String(), stderr.String()
	}
	if code, out, stderr := invokeTTY("status", "missing"); code != 3 || strings.Contains(stderr, "Checking status") {
		t.Fatalf("invalid selection showed progress: exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if code, out, stderr := invokeTTY("status", "db"); code != 0 || !strings.Contains(stderr, "Checking status for db…") {
		t.Fatalf("status exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if code, out, stderr := invokeTTY("status", "db", "--json"); code != 0 || stderr != "" || !strings.Contains(out, `"operation":"status"`) {
		t.Fatalf("JSON status exit=%d stdout=%s stderr=%q", code, out, stderr)
	}
}
