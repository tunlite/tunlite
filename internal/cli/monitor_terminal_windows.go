//go:build windows

package cli

import (
	"os"
	"time"
)

const monitorPlatformSupported = false

// monitor rejects Windows before entering raw mode. Keep this stub so the
// terminal event loop remains platform-separated and cannot accidentally read
// from an unsupported console implementation.
func readMonitorTerminalByte(_ *os.File, _ time.Duration) (byte, bool) {
	return 0, false
}
