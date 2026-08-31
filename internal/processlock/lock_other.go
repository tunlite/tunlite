//go:build !darwin && !linux

package processlock

import (
	"context"

	"github.com/tunlite/tunlite/internal/filelock"
)

// Windows is not in the v1 support matrix. Keep unsupported targets compiling
// until their native Adapter and process-lock implementation are validated.
func Acquire(ctx context.Context, path string) (func(), error) {
	return filelock.Acquire(ctx, path)
}

func Active(path string) bool { return filelock.Active(path) }
