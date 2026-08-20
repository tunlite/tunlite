package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/service"
)

func TestRealLegacy0103ImportSwitchAndRollback(t *testing.T) {
	if os.Getenv("TUNLITE_REAL_MIGRATION_E2E") != "1" {
		t.Skip("set TUNLITE_REAL_MIGRATION_E2E=1 with release and v0.10.3 binaries to run migration E2E")
	}
	releaseBinary := requireExecutablePath(t, "TUNLITE_RELEASE_BINARY")
	legacyBinary := requireExecutablePath(t, "TUNLITE_LEGACY_BINARY")
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required to execute the frozen v0.10.3 tag")
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client is not installed")
	}
	server := startRealSSHD(t)
	root := t.TempDir()
	t.Setenv("TUNLITE_HOME", filepath.Join(root, "shared-home"))
	// Unix-domain sockets have a short kernel path limit on macOS. Go's test
	// directory is intentionally descriptive and can exceed it, so keep this
	// process-unique test socket directly under /tmp.
	legacySocket := filepath.Join("/tmp", fmt.Sprintf("tunlite-migration-%d.sock", os.Getpid()))
	t.Setenv("TUNLITE_SOCKET", legacySocket)
	t.Cleanup(func() { _ = os.Remove(legacySocket) })
	t.Setenv("TUNLITE_SSH", sshPath)
	t.Setenv("TUNLITE_SERVICE_LABEL", "io.github.yuanyuanzijin.tunlite.migration-e2e")
	t.Setenv("TUNLITE_LAUNCH_AGENTS_DIR", filepath.Join(root, "legacy-launch-agents"))
	t.Setenv("TUNLITE_FAKE_AUTOSTART", "1")

	if version := strings.TrimSpace(invokeLegacyBinary(t, nodePath, legacyBinary, "version")); version != "0.10.3" {
		t.Fatalf("legacy tag reported version %q, want 0.10.3", version)
	}
	expectedVersion := os.Getenv("TUNLITE_EXPECTED_RELEASE_VERSION")
	versionOutput := invokeReleaseBinary(t, releaseBinary, "version", "--json")
	if expectedVersion != "" && !strings.Contains(versionOutput, `"version":"`+expectedVersion+`"`) {
		t.Fatalf("release binary reported wrong version: %s", versionOutput)
	}

	const name = "it-migration-e2e"
	assertReleaseServiceNameAvailable(t, name)
	const marker = "tunlite-migration-switch-rollback-ok"
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(marker))
	}))
	defer target.Close()
	targetURL, _ := url.Parse(target.URL)
	targetHost, targetPortText, _ := net.SplitHostPort(targetURL.Host)
	localPort := reserveTCPPort(t)

	invokeLegacyBinary(t, nodePath, legacyBinary,
		"add", name, "--to", fmt.Sprintf("%s@127.0.0.1:%d", server.username, server.port),
		"-i", server.clientKey,
		"-L", fmt.Sprintf("127.0.0.1:%d:%s:%s", localPort, targetHost, targetPortText),
		"--ssh-opt=-o", "--ssh-opt=UserKnownHostsFile=/dev/null", "--no-auto-key", "--disabled", "--json",
	)
	removed := false
	t.Cleanup(func() {
		if !removed {
			bestEffortReleaseCommand(releaseBinary, "disable", name, "--json", "--non-interactive")
			bestEffortReleaseCommand(releaseBinary, "remove", name, "--json", "--non-interactive")
		}
		cleanupLegacyRuntime(nodePath, legacyBinary, name)
	})

	invokeLegacyBinary(t, nodePath, legacyBinary, "enable", name, "--json")
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", localPort)
	verifyHTTPForward(t, endpoint, marker, 10*time.Second)

	legacyConfig := filepath.Join(os.Getenv("TUNLITE_HOME"), "config", "config.json")
	beforeImport, err := os.ReadFile(legacyConfig)
	if err != nil {
		t.Fatal(err)
	}
	v1Config := filepath.Join(os.Getenv("TUNLITE_HOME"), "config", "connections.json")
	preview := invokeReleaseBinary(t, releaseBinary, "import", legacyConfig, "--dry-run", "--json", "--non-interactive")
	if !strings.Contains(preview, `"dryRun":true`) || !strings.Contains(preview, `"outcome":"planned"`) {
		t.Fatalf("unexpected migration preview: %s", preview)
	}
	if _, err := os.Stat(v1Config); !os.IsNotExist(err) {
		t.Fatalf("dry-run created V1 config: %v", err)
	}
	verifyHTTPForward(t, endpoint, marker, 5*time.Second)

	imported := invokeReleaseBinary(t, releaseBinary, "import", legacyConfig, "--json", "--non-interactive")
	if !strings.Contains(imported, `"outcome":"applied"`) || !strings.Contains(imported, `"connection":"`+name+`"`) {
		t.Fatalf("unexpected migration result: %s", imported)
	}
	afterImport, err := os.ReadFile(legacyConfig)
	if err != nil || !bytes.Equal(afterImport, beforeImport) {
		t.Fatalf("V1 import changed legacy config: err=%v", err)
	}
	importedState := releaseServiceState(t, releaseBinary, name)
	assertReleaseServiceState(t, importedState, service.ModeStopped, service.ProcessInactive)
	verifyHTTPForward(t, endpoint, marker, 5*time.Second)

	invokeLegacyBinary(t, nodePath, legacyBinary, "disable", name, "--json")
	waitTCPUnavailable(t, fmt.Sprintf("127.0.0.1:%d", localPort), 5*time.Second)
	invokeReleaseBinary(t, releaseBinary, "start", name, "--json", "--non-interactive")
	v1Running := releaseServiceState(t, releaseBinary, name)
	assertReleaseServiceState(t, v1Running, service.ModeBackground, service.ProcessRunning)
	verifyHTTPForward(t, endpoint, marker, 10*time.Second)

	invokeReleaseBinary(t, releaseBinary, "stop", name, "--json", "--non-interactive")
	waitTCPUnavailable(t, fmt.Sprintf("127.0.0.1:%d", localPort), 5*time.Second)
	invokeLegacyBinary(t, nodePath, legacyBinary, "enable", name, "--json")
	verifyHTTPForward(t, endpoint, marker, 10*time.Second)

	invokeLegacyBinary(t, nodePath, legacyBinary, "disable", name, "--json")
	waitTCPUnavailable(t, fmt.Sprintf("127.0.0.1:%d", localPort), 5*time.Second)
	invokeReleaseBinary(t, releaseBinary, "remove", name, "--json", "--non-interactive")
	removed = true
	stopLegacyDaemon(t, nodePath, legacyBinary)
}

func requireExecutablePath(t *testing.T, name string) string {
	t.Helper()
	path := os.Getenv(name)
	if path == "" || !filepath.IsAbs(path) {
		t.Fatalf("%s must be an absolute path", name)
	}
	if info, err := os.Stat(path); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("%s is not executable: info=%#v err=%v", name, info, err)
	}
	return path
}

func invokeLegacyBinary(t *testing.T, node, binary string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, append([]string{binary}, args...)...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("legacy command %q failed: %v\nstdout=%s\nstderr=%s", args, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func cleanupLegacyRuntime(node, binary, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, node, binary, "disable", name, "--json").Run()
	_ = exec.CommandContext(ctx, node, binary, "daemon", "stop", "--json").Run()
}

func stopLegacyDaemon(t *testing.T, node, binary string) {
	t.Helper()
	invokeLegacyBinary(t, node, binary, "daemon", "stop", "--json")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output := invokeLegacyBinary(t, node, binary, "daemon", "status", "--json")
		var status struct {
			Running bool `json:"running"`
		}
		if err := json.Unmarshal([]byte(output), &status); err == nil && !status.Running {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("legacy daemon did not stop")
}

func waitTCPUnavailable(t *testing.T, address string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err != nil {
			return
		}
		_ = connection.Close()
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to stop accepting traffic", address)
}
