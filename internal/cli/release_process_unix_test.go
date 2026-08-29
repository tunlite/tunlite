//go:build !windows

package cli

import (
	"errors"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
)

func assertReleaseProviderProcessesExited(t *testing.T, observedPIDs map[int]struct{}) {
	t.Helper()
	for pid := range observedPIDs {
		err := syscall.Kill(pid, 0)
		if err == nil || errors.Is(err, syscall.EPERM) {
			command, _ := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").CombinedOutput()
			t.Errorf("release cleanup left observed provider pid %d running: %q", pid, command)
		} else if !errors.Is(err, syscall.ESRCH) {
			t.Errorf("release cleanup could not verify provider pid %d exited: %v", pid, err)
		}
	}
}
