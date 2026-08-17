package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
)

func PlanDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func installArtifact(path string, content []byte) ([]byte, bool, error) {
	previous, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if err := replaceFile(path, content, 0o600); err != nil {
		return nil, false, err
	}
	return previous, existed, nil
}

func restoreArtifact(path string, previous []byte, existed bool) error {
	if existed {
		return replaceFile(path, previous, 0o600)
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
