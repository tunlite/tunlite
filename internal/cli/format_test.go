package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
	"github.com/yuanyuanzijin/tunlite/internal/service"
)

func fixtureStatusRow(t *testing.T) statusRow {
	t.Helper()
	raw, err := json.Marshal(sshdriver.Config{
		Host: "me@example.com", Port: 2222,
		Forwards: []sshdriver.Forward{{Type: "local", Bind: "127.0.0.1", SrcPort: 5432, DestHost: "localhost", DestPort: 5432}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return statusRow{
		connection: core.Connection{
			ConfigVersion: core.ConfigVersion, Kind: core.Kind, Metadata: core.Metadata{Name: "db"},
			Spec: core.ConnectionSpec{Driver: sshdriver.ID, Config: raw},
		},
		state: service.State{
			Connection: "db", ServiceState: service.ServiceRegistered, Mode: service.ModeBackground,
			ProcessState: service.ProcessRunning, Health: service.HealthHealthy,
			ConsistencyState: service.ConsistencyConsistent, Platform: map[string]any{"pid": "42"},
			Conditions: []core.Condition{},
		},
	}
}

func TestStatusTableRestoresAlignedHumanOutputAndColor(t *testing.T) {
	lines := renderStatusTable([]statusRow{fixtureStatusRow(t)}, true, languageEnglish)
	text := strings.Join(lines, "\n")
	for _, column := range []string{"NAME", "STATE", "RUNS AS", "ON LOGIN", "PID", "UP", "RESTARTS", "HOST", "ROUTE"} {
		if !strings.Contains(text, column) {
			t.Fatalf("missing %s column:\n%s", column, text)
		}
	}
	if strings.Contains(text, "TYPE") || strings.Contains(text, "MODE") {
		t.Fatalf("missing table columns:\n%s", text)
	}
	if !strings.Contains(text, "\x1b[32m● running") {
		t.Fatalf("running state is not green:\n%q", text)
	}
	if !strings.Contains(text, "127.0.0.1:5432 → localhost:5432") || !strings.Contains(text, "42") {
		t.Fatalf("missing route or pid:\n%s", text)
	}
}

func TestStatusTableHasNoANSIWhenColorDisabled(t *testing.T) {
	text := strings.Join(renderStatusTable([]statusRow{fixtureStatusRow(t)}, false, languageEnglish), "\n")
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("plain output leaked ANSI: %q", text)
	}
}

func TestStatusDetailIncludesConnectionAndRuntimeDimensions(t *testing.T) {
	text := strings.Join(renderStatusDetail(fixtureStatusRow(t), false, languageEnglish), "\n")
	for _, expected := range []string{"db", "state", "running", "runs as", "background", "on login", "listen", "target", "restarts"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("detail missing %q:\n%s", expected, text)
		}
	}
}

func TestChineseStatusUsesPlainLanguageAndKeepsOperationalColumns(t *testing.T) {
	text := strings.Join(renderStatusTable([]statusRow{fixtureStatusRow(t)}, false, languageChinese), "\n")
	for _, expected := range []string{"名称", "状态", "正常运行", "运行位置", "系统后台", "登录自启", "PID", "已运行", "重启次数", "SSH 目标", "转发"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Chinese status missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "MODE") || strings.Contains(text, "TYPE") {
		t.Fatalf("Chinese status exposed internal columns:\n%s", text)
	}
}

func TestLanguageResolutionSupportsFlagEnvironmentAndSystemLocale(t *testing.T) {
	t.Setenv("TUNLITE_LANG", "zh_CN.UTF-8")
	lang, err := resolveLanguage("")
	if err != nil || lang != languageChinese {
		t.Fatalf("environment language=%q err=%v", lang, err)
	}
	lang, err = resolveLanguage("en-US")
	if err != nil || lang != languageEnglish {
		t.Fatalf("explicit language=%q err=%v", lang, err)
	}
	if _, err := resolveLanguage("fr"); err == nil {
		t.Fatal("unsupported explicit language was accepted")
	}
}

func TestChineseHelpExplainsLifecycleWithoutChangingCommands(t *testing.T) {
	var out, stderr bytes.Buffer
	code := RunContext(context.Background(), []string{"help", "--lang", "zh-CN"}, IO{Out: &out, Err: &stderr})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("help exit=%d stdout=%s stderr=%s", code, out.String(), stderr.String())
	}
	for _, expected := range []string{"运行方式", "start", "系统后台", "enable", "登录自启", "disable", "关闭登录自启"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("Chinese help missing %q:\n%s", expected, out.String())
		}
	}
}

func TestDoctorHumanOutputRestoresGlyphsColorsAndSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	t.Setenv("NO_COLOR", "")
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "--to", "me@example.com", "-D", "15432"); code != 0 {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	color := true
	var out, stderr bytes.Buffer
	code := runContext(context.Background(), []string{"doctor", "db", "--offline"}, IO{Out: &out, Err: &stderr, Color: &color}, func() (service.Adapter, error) { return adapter, nil })
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("doctor exit=%d stdout=%s stderr=%s", code, out.String(), stderr.String())
	}
	if !strings.Contains(out.String(), "\x1b[32m✓") || !strings.Contains(out.String(), "checks:") {
		t.Fatalf("doctor did not render colored checks and summary: %q", out.String())
	}
}

func TestMonitorFrameUsesOnePublicMonitorView(t *testing.T) {
	var out bytes.Buffer
	renderMonitor(&out, []statusRow{fixtureStatusRow(t)}, 0, false, nil, true, []string{"work"}, time.Second, "", nil, nil, false, languageEnglish)
	text := out.String()
	if !strings.Contains(text, "tunlite monitor") || !strings.Contains(text, "v"+Version) || !strings.Contains(text, "filter tag=work") || !strings.Contains(text, "Esc/q quit") {
		t.Fatalf("incomplete monitor frame: %q", text)
	}
	if !strings.Contains(text, "connections 1 total") || !strings.Contains(text, "NAME") || !strings.Contains(text, "STATE") {
		t.Fatalf("monitor frame is missing top-style summary or table: %q", text)
	}
	if !strings.Contains(text, ansiReverse) {
		t.Fatalf("selected monitor row is not highlighted: %q", text)
	}
}

func TestMonitorDetailRendersRecentLogs(t *testing.T) {
	var out bytes.Buffer
	renderMonitor(&out, []statusRow{fixtureStatusRow(t)}, 0, true, []string{"connected", "stderr: warning"}, false, nil, time.Second, "", nil, nil, false, languageEnglish)
	text := out.String()
	if !strings.Contains(text, "recent logs · latest 20 lines") || !strings.Contains(text, "connected") || !strings.Contains(text, "stderr: warning") || !strings.Contains(text, "←/Esc back") {
		t.Fatalf("monitor detail did not render logs: %q", text)
	}
}

func TestMonitorRendersTransitionsAndDestructiveConfirmation(t *testing.T) {
	row := fixtureStatusRow(t)
	row.transition = "stopping"
	transition := &monitorTransition{name: "db", label: "stopping"}
	var out bytes.Buffer
	renderMonitor(&out, []statusRow{row}, 0, false, nil, false, nil, time.Second, "", nil, transition, false, languageEnglish)
	if text := out.String(); !strings.Contains(text, "◌ stopping") || !strings.Contains(text, "stopping db…") {
		t.Fatalf("monitor did not render an in-flight stop: %q", text)
	}

	out.Reset()
	confirmation := &monitorConfirmation{key: monitorRestart, name: "db"}
	renderMonitor(&out, []statusRow{fixtureStatusRow(t)}, 0, false, nil, false, nil, time.Second, "", confirmation, nil, false, languageEnglish)
	if text := out.String(); !strings.Contains(text, `Restart "db"?`) || !strings.Contains(text, "y confirms; any other key cancels") {
		t.Fatalf("monitor did not render restart confirmation: %q", text)
	}
}

func TestMonitorPersistentStopAndRestartExplainTheirDifferentEffects(t *testing.T) {
	persistentStop := monitorConfirmationText(monitorConfirmation{key: monitorStop, name: "db", persistent: true}, languageChinese)
	if !strings.Contains(persistentStop, "关闭登录自启") || !strings.Contains(persistentStop, "重新登录后不会自动恢复") {
		t.Fatalf("persistent stop warning is incomplete: %q", persistentStop)
	}
	restart := monitorConfirmationText(monitorConfirmation{key: monitorRestart, name: "db"}, languageChinese)
	if !strings.Contains(restart, "连接会短暂中断") || !strings.Contains(restart, "登录自启设置不变") {
		t.Fatalf("restart warning is incomplete: %q", restart)
	}
}

func TestNarrowMonitorKeepsPIDUptimeAndRestartCount(t *testing.T) {
	text := strings.Join(renderMonitorTable([]statusRow{fixtureStatusRow(t)}, 0, 0, 1, 60, false, languageEnglish), "\n")
	for _, expected := range []string{"NAME", "STATE", "PID", "UP", "RESTARTS"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("narrow monitor dropped %s:\n%s", expected, text)
		}
	}
	for _, hidden := range []string{"HOST", "ROUTE", "RUNS AS", "ON LOGIN"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("narrow monitor kept lower-priority %s:\n%s", hidden, text)
		}
	}
}

func TestLanguageNeverChangesJSONContract(t *testing.T) {
	var out, stderr bytes.Buffer
	code := RunContext(context.Background(), []string{"version", "--json", "--lang", "zh-CN"}, IO{Out: &out, Err: &stderr})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("version exit=%d stdout=%s stderr=%s", code, out.String(), stderr.String())
	}
	if !strings.Contains(out.String(), `"outcome":"succeeded"`) || !strings.Contains(out.String(), `"protocolVersion":1`) {
		t.Fatalf("localized JSON contract changed: %s", out.String())
	}
}

func TestMonitorKeysSupportDetailsBackAndStandaloneEscape(t *testing.T) {
	keys := make(chan monitorKey, 16)
	readMonitorKeys(bytes.NewBufferString("\x1b[A\x1b[B\x1b[C\x1b[D\r\x1b"), keys)
	got := make([]monitorKey, 0, len(keys))
	for len(keys) > 0 {
		got = append(got, <-keys)
	}
	want := []monitorKey{monitorUp, monitorDown, monitorEnterDetail, monitorBack, monitorEnterDetail, monitorEscape}
	if len(got) != len(want) {
		t.Fatalf("keys=%v want=%v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("keys=%v want=%v", got, want)
		}
	}
}

func TestMonitorStopAndRestartPlansRequireConfirmationBeforeExecution(t *testing.T) {
	row := fixtureStatusRow(t)
	command, transition, err := monitorMutationPlan(monitorStop, row)
	if err != nil || command != "stop" || transition != "stopping" {
		t.Fatalf("background stop plan command=%q transition=%q err=%v", command, transition, err)
	}
	row.state.Mode = service.ModePersistent
	command, transition, err = monitorMutationPlan(monitorStop, row)
	if err != nil || command != "disable" || transition != "stopping" {
		t.Fatalf("persistent stop plan command=%q transition=%q err=%v", command, transition, err)
	}
	command, transition, err = monitorMutationPlan(monitorRestart, row)
	if err != nil || command != "restart" || transition != "restarting" {
		t.Fatalf("restart plan command=%q transition=%q err=%v", command, transition, err)
	}
}

func TestMonitorStopAndRestartExecuteOnlyAfterYConfirmation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "--to", "me@example.com", "-D", "1080"); code != 0 {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if code, out, stderr := invoke(t, "start", "db"); code != 0 {
		t.Fatalf("start exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	adapter.stopCalls = nil
	adapter.restartCalls = nil

	input, writer := io.Pipe()
	defer writer.Close()
	output := &lockedBuffer{}
	terminal := true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- monitor(ctx, []string{"--interval", "60"}, IO{In: input, Out: output, Err: output, Terminal: &terminal}, options{
			serviceAdapter: func() (service.Adapter, error) { return adapter, nil },
		})
	}()
	waitForMonitorOutput(t, output, 0, "tunlite monitor")

	offset := len(output.String())
	_, _ = writer.Write([]byte("r"))
	waitForMonitorOutput(t, output, offset, `Restart "db"?`)
	if len(adapter.restartCalls) != 0 {
		t.Fatalf("restart ran before confirmation: %v", adapter.restartCalls)
	}
	offset = len(output.String())
	_, _ = writer.Write([]byte("n"))
	waitForMonitorOutput(t, output, offset, "operation cancelled")
	if len(adapter.restartCalls) != 0 {
		t.Fatalf("cancelled restart ran: %v", adapter.restartCalls)
	}

	offset = len(output.String())
	_, _ = writer.Write([]byte("r"))
	waitForMonitorOutput(t, output, offset, `Restart "db"?`)
	offset = len(output.String())
	_, _ = writer.Write([]byte("y"))
	waitForMonitorOutput(t, output, offset, "restarted db")
	if len(adapter.restartCalls) != 1 || adapter.restartCalls[0] != "db" {
		t.Fatalf("confirmed restart calls=%v", adapter.restartCalls)
	}

	offset = len(output.String())
	_, _ = writer.Write([]byte("x"))
	waitForMonitorOutput(t, output, offset, `Stop "db"?`)
	if len(adapter.stopCalls) != 0 {
		t.Fatalf("stop ran before confirmation: %v", adapter.stopCalls)
	}
	offset = len(output.String())
	_, _ = writer.Write([]byte("y"))
	waitForMonitorOutput(t, output, offset, "stopped db")
	if len(adapter.stopCalls) != 1 || adapter.stopCalls[0] != "db" {
		t.Fatalf("confirmed stop calls=%v", adapter.stopCalls)
	}

	_, _ = writer.Write([]byte("q"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("monitor did not exit after q")
	}
	if text := output.String(); !strings.Contains(text, "\x1b[?1049h") || !strings.Contains(text, "\x1b[?1049l") {
		t.Fatalf("monitor did not enter and restore the alternate screen: %q", text)
	}
}

func waitForMonitorOutput(t *testing.T, output *lockedBuffer, offset int, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		text := output.String()
		if offset <= len(text) && strings.Contains(text[offset:], want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("monitor output after byte %d never contained %q: %q", offset, want, output.String())
}

func TestInteractiveLongOperationsAnnounceProgressWithoutPollutingJSON(t *testing.T) {
	terminal := true
	var stderr bytes.Buffer
	renderCommandProgress(IO{Err: &stderr, Terminal: &terminal}, options{}, []string{"start", "db"})
	if stderr.String() != "Starting db…\n" {
		t.Fatalf("progress=%q", stderr.String())
	}
	stderr.Reset()
	renderCommandProgress(IO{Err: &stderr, Terminal: &terminal}, options{}, []string{"status"})
	if stderr.String() != "Checking status…\n" {
		t.Fatalf("status progress=%q", stderr.String())
	}
	stderr.Reset()
	renderCommandProgress(IO{Err: &stderr, Terminal: &terminal}, options{}, []string{"start"})
	if stderr.Len() != 0 {
		t.Fatalf("invalid command showed progress before usage validation: %q", stderr.String())
	}
	stderr.Reset()
	renderCommandProgress(IO{Err: &stderr, Terminal: &terminal}, options{JSON: true}, []string{"start", "db"})
	if stderr.Len() != 0 {
		t.Fatalf("JSON progress leaked to stderr: %q", stderr.String())
	}
}

func TestMonitorDetailReadsLogsOnlyForSelectedConnection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "--to", "me@example.com", "-D", "1080"); code != 0 {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	opts := options{serviceAdapter: func() (service.Adapter, error) { return adapter, nil }}
	rows, err := collectMonitorRows(context.Background(), nil, opts)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
	logs, err := collectMonitorLogs(context.Background(), rows[0], opts)
	if err != nil || len(logs) != 1 || logs[0] != "db: provider log" {
		t.Fatalf("logs=%#v err=%v", logs, err)
	}
}

func TestMonitorRejectsNonInteractiveJSONAndDryRunUse(t *testing.T) {
	for _, args := range [][]string{{"monitor"}, {"monitor", "--json"}, {"monitor", "--dry-run"}} {
		var out, stderr bytes.Buffer
		code := RunContext(context.Background(), args, IO{Out: &out, Err: &stderr})
		message := out.String() + stderr.String()
		if code != 2 || (!strings.Contains(message, "status --json") && !strings.Contains(message, "does not support --dry-run")) {
			t.Fatalf("args=%v exit=%d stdout=%s stderr=%s", args, code, out.String(), stderr.String())
		}
	}
}

func TestEveryPublicCommandHasSuccessfulHelp(t *testing.T) {
	for _, command := range publicCommands {
		var out, stderr bytes.Buffer
		code := RunContext(context.Background(), []string{command, "--help"}, IO{Out: &out, Err: &stderr})
		want := "usage: tunlite " + command
		if command == "help" {
			want = "usage: tunlite <command>"
		}
		if code != 0 || !strings.Contains(out.String(), want) {
			t.Fatalf("command=%s exit=%d stdout=%s stderr=%s", command, code, out.String(), stderr.String())
		}
	}
}

func TestProductDirectionDocumentsExactlyThePublicCommands(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "PROJECT_DIRECTION.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "## 9. CLI 契约")
	end := strings.Index(text, "## 10.")
	if start < 0 || end <= start {
		t.Fatal("PROJECT_DIRECTION.md is missing the authoritative CLI section")
	}
	pattern := regexp.MustCompile("(?m)^\\| `([a-z][a-z-]*)` \\| `tunlite ")
	documented := map[string]bool{}
	for _, match := range pattern.FindAllStringSubmatch(text[start:end], -1) {
		documented[match[1]] = true
	}
	if len(documented) != len(publicCommands) {
		t.Fatalf("documented %d commands, runtime exposes %d: %#v", len(documented), len(publicCommands), documented)
	}
	for _, command := range publicCommands {
		if !documented[command] {
			t.Fatalf("public command %q is missing from PROJECT_DIRECTION.md", command)
		}
	}
	for command := range documented {
		if !containsString(publicCommands, command) {
			t.Fatalf("PROJECT_DIRECTION.md promises non-public command %q", command)
		}
	}
}

func TestMistypedAndRetiredCommandsSuggestThePublicCommand(t *testing.T) {
	for input, want := range map[string]string{"stauts": "status", "rm": "remove", "mon": "monitor"} {
		var out, stderr bytes.Buffer
		code := RunContext(context.Background(), []string{input}, IO{Out: &out, Err: &stderr})
		if code != 2 || !strings.Contains(out.String()+stderr.String(), `did you mean "`+want+`"`) {
			t.Fatalf("input=%s exit=%d stdout=%s stderr=%s", input, code, out.String(), stderr.String())
		}
	}
}
