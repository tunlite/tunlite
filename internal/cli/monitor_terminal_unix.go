//go:build !windows

package cli

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const monitorPlatformSupported = true

func readMonitorTerminalByte(input *os.File, timeout time.Duration) (byte, bool) {
	timeoutMS := -1
	if timeout >= 0 {
		timeoutMS = int(timeout / time.Millisecond)
		if timeoutMS < 1 {
			timeoutMS = 1
		}
	}
	for {
		descriptors := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
		ready, err := unix.Poll(descriptors, timeoutMS)
		if err == unix.EINTR {
			continue
		}
		if err != nil || ready == 0 || descriptors[0].Revents&(unix.POLLIN|unix.POLLHUP) == 0 {
			return 0, false
		}
		buffer := []byte{0}
		if count, readErr := input.Read(buffer); count == 1 {
			return buffer[0], true
		} else if readErr != nil {
			return 0, false
		}
	}
}
