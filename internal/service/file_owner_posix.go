//go:build !windows

package service

import (
	"os"
	"syscall"
)

type fileSecurityMetadata struct {
	UID   uint32
	GID   uint32
	Dev   uint64
	Ino   uint64
	Nlink uint64
}

func securityMetadata(info os.FileInfo) (fileSecurityMetadata, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileSecurityMetadata{}, false
	}
	return fileSecurityMetadata{
		UID: stat.Uid, GID: stat.Gid, Dev: uint64(stat.Dev), Ino: uint64(stat.Ino), Nlink: uint64(stat.Nlink),
	}, true
}
