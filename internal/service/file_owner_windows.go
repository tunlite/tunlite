//go:build windows

package service

import "os"

type fileSecurityMetadata struct {
	UID   uint32
	GID   uint32
	Dev   uint64
	Ino   uint64
	Nlink uint64
}

func securityMetadata(os.FileInfo) (fileSecurityMetadata, bool) {
	return fileSecurityMetadata{}, false
}
