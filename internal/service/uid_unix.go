//go:build !windows

package service

import "os"

func currentUID() int { return os.Getuid() }
