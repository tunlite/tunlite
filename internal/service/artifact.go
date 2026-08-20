package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"

	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

const embeddedPlanPrefix = "Tunlite-ExecutionPlan: "

func PlanDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func ContentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func encodeEmbeddedPlan(value plan.ExecutionPlan) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(raw), nil
}

func decodeEmbeddedPlan(value string) (plan.ExecutionPlan, error) {
	raw, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return plan.ExecutionPlan{}, err
	}
	var result plan.ExecutionPlan
	if err := json.Unmarshal(raw, &result); err != nil {
		return plan.ExecutionPlan{}, err
	}
	if err := plan.Validate(result); err != nil {
		return plan.ExecutionPlan{}, err
	}
	return result, nil
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
