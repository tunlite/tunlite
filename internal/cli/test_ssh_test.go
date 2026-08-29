package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

const testSSHProcess = "TUNLITE_TEST_SSH_PROCESS"

var portableTestSSH string

// TestMain lets tests use the already-built test executable as a portable SSH
// fixture. Child executions exit successfully before testing parses the SSH
// argv, while the parent test process continues through the normal suite.
func TestMain(m *testing.M) {
	if os.Getenv(testSSHProcess) == "1" {
		os.Exit(0)
	}

	directory, err := os.MkdirTemp("", "tunlite-test-ssh-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "prepare portable SSH test fixture:", err)
		os.Exit(2)
	}
	portableTestSSH = filepath.Join(directory, "ssh")
	executable, err := os.Executable()
	if err == nil {
		err = os.Link(executable, portableTestSSH)
	}
	if err != nil {
		var raw []byte
		raw, err = os.ReadFile(executable)
		if err == nil {
			err = os.WriteFile(portableTestSSH, raw, 0o700)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "prepare portable SSH test fixture:", err)
		_ = os.RemoveAll(directory)
		os.Exit(2)
	}

	code := m.Run()
	if err := os.RemoveAll(directory); err != nil && code == 0 {
		fmt.Fprintln(os.Stderr, "clean portable SSH test fixture:", err)
		code = 2
	}
	os.Exit(code)
}

func setTestSSH(t *testing.T) string {
	t.Helper()
	if portableTestSSH == "" {
		t.Fatal("portable SSH test executable is not initialized")
	}
	if runtime.GOOS == "windows" {
		// An empty effective PATHEXT makes LookPath accept the extensionless PE
		// fixture. Its basename remains "ssh", matching native-plan import rules.
		t.Setenv("PATHEXT", ";")
	}
	t.Setenv("TUNLITE_SSH", portableTestSSH)
	t.Setenv(testSSHProcess, "1")
	return portableTestSSH
}

func TestPortableSSHFixtureResolvesAndExecutes(t *testing.T) {
	path := setTestSSH(t)
	resolved, err := exec.LookPath(path)
	if err != nil {
		t.Fatalf("resolve portable SSH fixture: %v", err)
	}
	if filepath.Base(resolved) != "ssh" {
		t.Fatalf("portable SSH fixture lost canonical basename: %q", resolved)
	}
	if output, err := exec.Command(resolved, "-F", "none", "-N", "--", "test@example.com").CombinedOutput(); err != nil {
		t.Fatalf("execute portable SSH fixture: %v\n%s", err, output)
	}
}
