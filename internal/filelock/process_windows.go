//go:build windows

package filelock

import (
	"errors"
	"syscall"
)

func processAlive(pid int) bool {
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err == nil {
		_ = syscall.CloseHandle(handle)
		return true
	}
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
}
