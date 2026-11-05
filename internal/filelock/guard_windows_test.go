package filelock

import (
	"os"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

func holdTestGuard(t *testing.T, path string) func() {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{}); err != nil {
		file.Close()
		t.Fatal(err)
	}
	var once sync.Once
	return func() { once.Do(func() { file.Close() }) }
}
