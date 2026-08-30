//go:build darwin

package service

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMacOSInstallRejectsDevStdinAsRegularFileSource(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "definition.plist")
	command := exec.Command(installProgram, "-S", "-m", "0644", "/dev/stdin", destination)
	command.Stdin = bytes.NewBufferString("test definition\n")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("macOS install unexpectedly accepted /dev/stdin; output=%q", output)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 71 {
		t.Fatalf("macOS install /dev/stdin regression exit=%v output=%q", err, output)
	}
}
