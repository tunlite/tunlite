package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
	"github.com/yuanyuanzijin/tunlite/internal/service"
)

func TestRealNativeManageAndUnmanageDoNotInterruptProvider(t *testing.T) {
	if os.Getenv("TUNLITE_REAL_SERVICE") != "1" {
		t.Skip("set TUNLITE_REAL_SERVICE=1 to run the real ownership integration test")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native services are unavailable on this platform")
	}
	root := t.TempDir()
	t.Setenv("TUNLITE_HOME", filepath.Join(root, "tunlite-home"))
	fakeSSH := createNativeOwnershipSSH(t, root)
	t.Setenv("TUNLITE_SSH", fakeSSH)

	adapter, err := service.NewCurrent(nil)
	if err != nil {
		t.Fatal(err)
	}
	nativeName := "it-ownership"
	raw, err := json.Marshal(sshdriver.Config{
		Host: "integration@example.com",
		Forwards: []sshdriver.Forward{{
			Type: "remote", Bind: "127.0.0.1", SrcPort: 39001,
			DestHost: "127.0.0.1", DestPort: 39002,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	connection := core.Connection{
		ConfigVersion: core.ConfigVersion, Kind: core.Kind,
		Metadata: core.Metadata{Name: nativeName},
		Spec:     core.ConnectionSpec{Driver: sshdriver.ID, Config: raw},
	}
	executionPlan, err := sshdriver.New().Plan(context.Background(), connection, core.PlanOptions{NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := adapter.Build(nativeName, executionPlan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = adapter.Remove(cleanupCtx, artifact.Reference())
		for _, path := range artifact.LogPaths {
			_ = os.Truncate(path, 0)
		}
	})
	for _, path := range artifact.LogPaths {
		_ = os.Truncate(path, 0)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := adapter.Materialize(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(ctx, artifact.Reference()); err != nil {
		t.Fatal(err)
	}
	before := waitForNativeOwnershipProcess(t, ctx, adapter, artifact.Reference())
	beforeIdentity := nativeProcessIdentity(t, adapter.Name(), before)
	if before.AutoStart {
		t.Fatalf("source service was not background-only: %#v", before)
	}

	invoke := func(t *testing.T, args ...string) (int, string, string) {
		t.Helper()
		var stdout, stderr strings.Builder
		code := runContext(ctx, args, IO{Out: &stdout, Err: &stderr}, func() (service.Adapter, error) { return adapter, nil })
		return code, stdout.String(), stderr.String()
	}
	managedName := "managed-native"
	code, out, stderr := invoke(t, "manage", artifact.UnitName, "--name", managedName, "--json", "--non-interactive")
	if code != 0 || stderr != "" || !strings.Contains(out, `"operation":"manage"`) {
		t.Fatalf("manage exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	afterManage := waitForNativeOwnershipProcess(t, ctx, adapter, artifact.Reference())
	if got := nativeProcessIdentity(t, adapter.Name(), afterManage); got != beforeIdentity || afterManage.AutoStart {
		t.Fatalf("manage interrupted or changed the native service: before=%#v after=%#v", before, afterManage)
	}
	code, out, stderr = invoke(t, "status", managedName, "--json", "--non-interactive")
	if code != 0 || stderr != "" || !strings.Contains(out, `"mode":"background"`) ||
		!strings.Contains(out, `"consistencyState":"consistent"`) {
		t.Fatalf("managed status exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, stderr = invoke(t, "unmanage", managedName, "--json", "--non-interactive")
	if code != 0 || stderr != "" || !strings.Contains(out, `"operation":"unmanage"`) {
		t.Fatalf("unmanage exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	afterUnmanage := waitForNativeOwnershipProcess(t, ctx, adapter, artifact.Reference())
	if got := nativeProcessIdentity(t, adapter.Name(), afterUnmanage); got != beforeIdentity || afterUnmanage.AutoStart || !afterUnmanage.Installed {
		t.Fatalf("unmanage interrupted or removed the native service: before=%#v after=%#v", before, afterUnmanage)
	}
	code, out, stderr = invoke(t, "list", "--json", "--non-interactive")
	if code != 0 || stderr != "" || !strings.Contains(out, `"results":[]`) {
		t.Fatalf("unmanaged declaration remained: exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func createNativeOwnershipSSH(t *testing.T, fallbackDir string) string {
	t.Helper()
	destinationDir := fallbackDir
	var err error
	if runtime.GOOS == "darwin" {
		cacheDir, cacheErr := os.UserCacheDir()
		if cacheErr != nil {
			t.Fatal(cacheErr)
		}
		destinationDir, err = os.MkdirTemp(cacheDir, "tunlite-ownership-*")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(destinationDir) })
	}
	path := filepath.Join(destinationDir, "ssh")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Clean(path)
	t.Cleanup(func() { _ = os.Remove(path) })
	if _, err := file.WriteString("#!/bin/sh\nexec /bin/sleep 30\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Chmod(0o700); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForNativeOwnershipProcess(t *testing.T, ctx context.Context, adapter service.Adapter, reference service.Reference) service.Observation {
	t.Helper()
	for {
		observation, err := adapter.Inspect(ctx, reference)
		if err == nil && observation.ProcessState == service.ProcessRunning {
			return observation
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for native ownership provider: observation=%#v err=%v", observation, err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func nativeProcessIdentity(t *testing.T, adapter string, observation service.Observation) string {
	t.Helper()
	key := "mainPID"
	if adapter == "launchd" {
		key = "pid"
	}
	value, ok := observation.Platform[key].(string)
	if !ok || value == "" || value == "0" {
		t.Fatalf("native observation has no %s: %#v", key, observation.Platform)
	}
	return value
}
