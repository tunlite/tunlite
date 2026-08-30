package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"

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

// mergeEmbeddedPlan keeps the native service body authoritative for fields it
// can express. The embedded comment only restores provider-neutral metadata
// that launchd/systemd cannot encode, and cannot override what will execute.
func mergeEmbeddedPlan(native, embedded plan.ExecutionPlan) (plan.ExecutionPlan, error) {
	if native.Mode != embedded.Mode || !reflect.DeepEqual(native.Exec, embedded.Exec) || native.Lifecycle != embedded.Lifecycle {
		return plan.ExecutionPlan{}, errors.New("embedded execution plan conflicts with native service body")
	}
	native.Endpoints = append([]plan.Endpoint(nil), embedded.Endpoints...)
	native.HealthChecks = append([]plan.HealthCheck(nil), embedded.HealthChecks...)
	native.Redactions = append([]plan.Redaction(nil), embedded.Redactions...)
	if err := plan.Validate(native); err != nil {
		return plan.ExecutionPlan{}, err
	}
	return native, nil
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
