package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	lines := renderStatusTable([]statusRow{fixtureStatusRow(t)}, true)
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "NAME") || !strings.Contains(text, "STATE") || !strings.Contains(text, "HOST") || !strings.Contains(text, "ROUTE") {
		t.Fatalf("missing table columns:\n%s", text)
	}
	if !strings.Contains(text, "\x1b[32m● healthy") {
		t.Fatalf("healthy state is not green:\n%q", text)
	}
	if !strings.Contains(text, "127.0.0.1:5432 → localhost:5432") || !strings.Contains(text, "42") {
		t.Fatalf("missing route or pid:\n%s", text)
	}
}

func TestStatusTableHasNoANSIWhenColorDisabled(t *testing.T) {
	text := strings.Join(renderStatusTable([]statusRow{fixtureStatusRow(t)}, false), "\n")
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("plain output leaked ANSI: %q", text)
	}
}

func TestStatusDetailIncludesConnectionAndRuntimeDimensions(t *testing.T) {
	text := strings.Join(renderStatusDetail(fixtureStatusRow(t), false), "\n")
	for _, expected := range []string{"db", "state", "healthy", "mode", "background", "process", "running", "listen", "target"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("detail missing %q:\n%s", expected, text)
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
	renderMonitor(&out, []statusRow{fixtureStatusRow(t)}, 0, false, nil, true, []string{"work"}, time.Second, "")
	text := out.String()
	if !strings.Contains(text, "tunlite monitor") || !strings.Contains(text, "tag work") || !strings.Contains(text, "q quit") {
		t.Fatalf("incomplete monitor frame: %q", text)
	}
	if !strings.Contains(text, ansiReverse) {
		t.Fatalf("selected monitor row is not highlighted: %q", text)
	}
}

func TestMonitorDetailRendersRecentLogs(t *testing.T) {
	var out bytes.Buffer
	renderMonitor(&out, []statusRow{fixtureStatusRow(t)}, 0, true, []string{"connected", "stderr: warning"}, false, nil, time.Second, "")
	text := out.String()
	if !strings.Contains(text, "LOGS · latest 20 lines") || !strings.Contains(text, "connected") || !strings.Contains(text, "stderr: warning") {
		t.Fatalf("monitor detail did not render logs: %q", text)
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
