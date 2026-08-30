//go:build !windows

package cli

import (
	"os"
	"syscall"
)

func foregroundTerminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
