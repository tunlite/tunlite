//go:build windows

package cli

import "os"

func foregroundTerminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
