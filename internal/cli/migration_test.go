package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuanyuanzijin/tunlite/internal/configstore"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
	"github.com/yuanyuanzijin/tunlite/internal/service"
)

func TestMigrateLegacyConfigAndRollbackByteForByte(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	configPath := filepath.Join(home, "config", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{
  "version": 1,
  "settings": {"keepalive":{"intervalSec":9}},
  "tunnels": [{
    "name": "db", "host": "me@example.com", "port": 2222,
    "identityFile": "~/.ssh/id_ed25519", "jump": "me@bastion:2200",
    "tags": ["work", "db"],
    "forwards": [{"type":"local","bind":"127.0.0.1","srcPort":5432,"destHost":"localhost","destPort":5432}],
    "sshOptions": ["-o", "Compression=yes"], "enabled": true, "autoSetupKey": true
  }]
}`)
	if err := os.WriteFile(configPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, stderr := invoke(t, "migrate", "--dry-run", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"outcome":"planned"`) {
		t.Fatalf("dry-run exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	unchanged, _ := os.ReadFile(configPath)
	if string(unchanged) != string(legacy) {
		t.Fatal("dry-run changed the legacy config")
	}

	code, out, stderr = invoke(t, "migrate", "--json", "--non-interactive")
	if code != 0 || stderr != "" {
		t.Fatalf("migrate exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var result struct {
		Outcome     string `json:"outcome"`
		Result      string `json:"result"`
		BackupPath  string `json:"backupPath"`
		JournalPath string `json:"journalPath"`
		Connections int    `json:"connections"`
		Conditions  []struct {
			Code string `json:"code"`
		} `json:"conditions"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "applied" || result.Result != "migrated" || result.Connections != 1 || result.BackupPath == "" || result.JournalPath == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	backup, err := os.ReadFile(result.BackupPath)
	if err != nil || string(backup) != string(legacy) {
		t.Fatalf("backup is not byte-for-byte: err=%v", err)
	}
	document, err := configstore.New(configPath).Load()
	if err != nil || len(document.Items) != 1 {
		t.Fatalf("load migrated config: items=%d err=%v", len(document.Items), err)
	}
	connection := document.Items[0]
	if connection.Metadata.Labels["tag.tunlite.dev/work"] != "true" || connection.Spec.Lifecycle != "persistent" {
		t.Fatalf("unexpected migrated connection: %#v", connection)
	}
	sshConfig, err := sshdriver.DecodeConfig(connection.Spec.Config)
	if err != nil {
		t.Fatal(err)
	}
	if len(sshConfig.SSHOptions) != 1 || sshConfig.SSHOptions[0] != "Compression=yes" || len(sshConfig.Jump) != 1 {
		t.Fatalf("legacy SSH fields were not normalized: %#v", sshConfig)
	}

	code, out, stderr = invoke(t, "migrate", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"result":"already-migrated"`) {
		t.Fatalf("repeat migrate exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, stderr = invoke(t, "migrate", "--rollback", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"result":"rolled-back"`) {
		t.Fatalf("rollback exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	restored, err := os.ReadFile(configPath)
	if err != nil || string(restored) != string(legacy) {
		t.Fatalf("rollback was not byte-for-byte: err=%v", err)
	}
}

func TestMigrationRollbackRefusesConfigDrift(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	configPath := filepath.Join(home, "config", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"version":1,"settings":{},"tunnels":[{"name":"socks","host":"me@host","forwards":[{"type":"dynamic","srcPort":1080}],"sshOptions":[],"enabled":false}]}`)
	if err := os.WriteFile(configPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := invoke(t, "migrate", "--json"); code != 0 {
		t.Fatalf("migrate exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if code, out, stderr := invoke(t, "add", "new", "--to", "me@host", "-D", "1081", "--json"); code != 0 {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	before, _ := os.ReadFile(configPath)
	code, out, _ := invoke(t, "migrate", "--rollback", "--json")
	if code != 1 || !strings.Contains(out, `"code":"migration_drifted"`) {
		t.Fatalf("rollback exit=%d stdout=%s", code, out)
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Fatal("refused rollback changed the config")
	}
}

func TestMigrateRejectsUnsafeLegacySSHOptionsWithoutWriting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	configPath := filepath.Join(home, "config", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"version":1,"settings":{},"tunnels":[{"name":"bad","host":"me@host","forwards":[{"type":"dynamic","srcPort":1080}],"sshOptions":["ProxyCommand","touch /tmp/x"]}]}`)
	if err := os.WriteFile(configPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, _ := invoke(t, "migrate", "--json")
	if code == 0 {
		t.Fatal("unsafe legacy option was migrated")
	}
	after, _ := os.ReadFile(configPath)
	if string(after) != string(legacy) {
		t.Fatal("failed migration changed the config")
	}
}

func TestMigrationRollbackRefusesEnabledNativeServices(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	configPath := filepath.Join(home, "config", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"version":1,"settings":{},"tunnels":[{"name":"socks","host":"me@host","forwards":[{"type":"dynamic","srcPort":1080}],"enabled":true}]}`)
	if err := os.WriteFile(configPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := invoke(t, "migrate", "--json"); code != 0 {
		t.Fatalf("migrate exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	records := service.NewStore(filepath.Join(home, "state", "services"))
	if err := records.Write(context.Background(), service.Record{
		APIVersion: service.RecordVersion, Connection: "socks", Adapter: "test",
		DesiredState: service.DesiredEnabled,
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(configPath)
	code, out, _ := invoke(t, "migrate", "--rollback", "--json")
	if code != 1 || !strings.Contains(out, `"code":"migration_services_enabled"`) || !strings.Contains(out, `"type":"disable_connections"`) {
		t.Fatalf("rollback exit=%d stdout=%s", code, out)
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Fatal("rollback with an enabled service changed the config")
	}
}
