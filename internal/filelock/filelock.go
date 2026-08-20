package filelock

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const malformedGrace = 30 * time.Second

func Acquire(ctx context.Context, path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	owner, err := ownerRecord()
	if err != nil {
		return nil, err
	}
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, writeErr := file.Write(owner); writeErr != nil {
				_ = file.Close()
				removeIfOwner(path, owner)
				return nil, writeErr
			}
			if closeErr := file.Close(); closeErr != nil {
				removeIfOwner(path, owner)
				return nil, closeErr
			}
			return func() { removeIfOwner(path, owner) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		reclaimed, reclaimErr := reclaimDeadOwner(path)
		if reclaimErr != nil {
			return nil, reclaimErr
		}
		if reclaimed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Active reports whether a lock is currently owned by a live process. It is a
// read-only observation: stale locks are ignored and later reclaimed by
// Acquire.
func Active(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return false
	}
	pid, err := strconv.Atoi(fields[0])
	return err == nil && pid > 0 && processAlive(pid)
}

// Held reports whether a lock file must still be treated as owned. Dead
// owners are reclaimed; malformed recent files remain held for the grace
// period so coordination never guesses that an ambiguous lock is safe.
func Held(path string) (bool, error) {
	reclaimed, err := reclaimDeadOwner(path)
	if err != nil {
		return false, err
	}
	return !reclaimed, nil
}

func ownerRecord() ([]byte, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, fmt.Errorf("generate lock owner token: %w", err)
	}
	return []byte(fmt.Sprintf("%d %s\n", os.Getpid(), hex.EncodeToString(token))), nil
}

func reclaimDeadOwner(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 2 {
		pid, parseErr := strconv.Atoi(fields[0])
		if parseErr == nil && pid > 0 {
			if processAlive(pid) {
				return false, nil
			}
			return removeExact(path, raw)
		}
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if time.Since(info.ModTime()) <= malformedGrace {
		return false, nil
	}
	return removeExact(path, raw)
}

func removeExact(path string, expected []byte) (bool, error) {
	current, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func removeIfOwner(path string, owner []byte) {
	_, _ = removeExact(path, owner)
}
