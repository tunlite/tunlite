//go:build !windows

package filelock

import (
	"os"
	"sync"
	"syscall"
	"testing"
)

func holdTestGuard(t *testing.T, path string) func() {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		t.Fatal(err)
	}
	var once sync.Once
	return func() { once.Do(func() { file.Close() }) }
}
