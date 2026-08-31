package ssh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tunlite/tunlite/internal/core"
)

const (
	probeHelperBehavior   = "TUNLITE_PROBE_HELPER_BEHAVIOR"
	probeHelperChild      = "TUNLITE_PROBE_HELPER_CHILD"
	probeHelperMarker     = "TUNLITE_PROBE_HELPER_MARKER"
	probeHelperReady      = "TUNLITE_PROBE_HELPER_READY"
	probeImmediateFailure = "immediate-auth-failure"
	probeHoldStderr       = "hold-inherited-stderr"
	probeAuthAndHold      = "authenticate-and-hold-inherited-stderr"
)

func TestMain(m *testing.M) {
	if os.Getenv(probeHelperChild) == "1" {
		fmt.Fprintln(os.Stdout, "ready")
		waitForProbeHelperTermination()
		os.Exit(0)
	}
	switch os.Getenv(probeHelperBehavior) {
	case probeImmediateFailure:
		fmt.Fprintln(os.Stderr, "Permission denied (publickey).")
		os.Exit(255)
	case probeHoldStderr, probeAuthAndHold:
		runInheritedStderrProbeHelper()
		os.Exit(0)
	default:
		os.Exit(m.Run())
	}
}

func runInheritedStderrProbeHelper() {
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "create descendant readiness pipe:", err)
		os.Exit(2)
	}
	descendant := exec.Command(os.Args[0])
	descendant.Env = append(os.Environ(), probeHelperChild+"=1")
	descendant.Stdout = readyWriter
	descendant.Stderr = os.Stderr
	if err := descendant.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start stderr-holding descendant:", err)
		os.Exit(2)
	}
	_ = readyWriter.Close()
	marker := os.Getenv(probeHelperMarker)
	if err := os.WriteFile(marker, []byte(strconv.Itoa(descendant.Process.Pid)), 0o600); err != nil {
		_ = descendant.Process.Kill()
		fmt.Fprintln(os.Stderr, "write descendant pid marker:", err)
		os.Exit(2)
	}
	ready := make([]byte, len("ready\n"))
	if _, err := io.ReadFull(readyReader, ready); err != nil || string(ready) != "ready\n" {
		_ = descendant.Process.Kill()
		fmt.Fprintf(os.Stderr, "wait for stderr-holding descendant: ready=%q err=%v\n", string(ready), err)
		os.Exit(2)
	}
	_ = readyReader.Close()
	connection, err := net.Dial("tcp", os.Getenv(probeHelperReady))
	if err != nil {
		_ = descendant.Process.Kill()
		fmt.Fprintln(os.Stderr, "signal probe helper readiness:", err)
		os.Exit(2)
	}
	_ = connection.Close()
	if os.Getenv(probeHelperBehavior) == probeAuthAndHold {
		fmt.Fprintln(os.Stderr, "Authenticated to host using publickey.")
	}
	waitForProbeHelperTermination()
}

func waitForProbeHelperTermination() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	<-signals
}

func TestCheckCapturesImmediateAuthenticationFailure(t *testing.T) {
	t.Setenv(probeHelperBehavior, probeImmediateFailure)
	report, err := newProbeTestDriver(t).Check(context.Background(), probeTestConnection(t), core.CheckOptions{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	assertProbeCondition(t, report, false, "ssh_authentication_failed")
}

func TestCheckTimeoutIsBoundedWhenDescendantHoldsStderr(t *testing.T) {
	listener, marker := configureInheritedStderrProbe(t, probeHoldStderr)
	driver := newProbeTestDriver(t)
	connection := probeTestConnection(t)
	started := time.Now()
	result := make(chan probeTestResult, 1)
	go func() {
		report, err := driver.Check(context.Background(), connection, core.CheckOptions{Timeout: time.Second})
		result <- probeTestResult{report: report, err: err}
	}()
	waitForProbeHelperReady(t, listener)
	completed := waitForProbeResult(t, result, 3*time.Second)
	assertProbeCondition(t, completed.report, false, "ssh_check_timeout")
	if completed.err != nil || time.Since(started) > 2*time.Second {
		t.Fatalf("timeout probe err=%v elapsed=%s", completed.err, time.Since(started))
	}
	assertProbeDescendantRecorded(t, marker)
}

func TestCheckParentCancellationIsBoundedWhenDescendantHoldsStderr(t *testing.T) {
	listener, marker := configureInheritedStderrProbe(t, probeHoldStderr)
	driver := newProbeTestDriver(t)
	connection := probeTestConnection(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan probeTestResult, 1)
	go func() {
		report, err := driver.Check(ctx, connection, core.CheckOptions{Timeout: 5 * time.Second})
		result <- probeTestResult{report: report, err: err}
	}()
	waitForProbeHelperReady(t, listener)
	started := time.Now()
	cancel()
	completed := waitForProbeResult(t, result, 2*time.Second)
	assertProbeCondition(t, completed.report, false, "ssh_check_timeout")
	if completed.err != nil || time.Since(started) > time.Second {
		t.Fatalf("cancelled probe err=%v elapsed=%s", completed.err, time.Since(started))
	}
	assertProbeDescendantRecorded(t, marker)
}

func TestCheckEarlyAuthenticationIsBoundedWhenDescendantHoldsStderr(t *testing.T) {
	listener, marker := configureInheritedStderrProbe(t, probeAuthAndHold)
	driver := newProbeTestDriver(t)
	connection := probeTestConnection(t)
	started := time.Now()
	result := make(chan probeTestResult, 1)
	go func() {
		report, err := driver.Check(context.Background(), connection, core.CheckOptions{Timeout: 5 * time.Second})
		result <- probeTestResult{report: report, err: err}
	}()
	waitForProbeHelperReady(t, listener)
	completed := waitForProbeResult(t, result, 2*time.Second)
	assertProbeCondition(t, completed.report, true, "ssh_authentication_succeeded")
	if completed.err != nil || time.Since(started) > 2*time.Second {
		t.Fatalf("authenticated probe err=%v elapsed=%s", completed.err, time.Since(started))
	}
	assertProbeDescendantRecorded(t, marker)
}

func TestProbeOutputObserverBoundsOutputAndMatchesAcrossWrites(t *testing.T) {
	authenticated := make(chan struct{}, 1)
	observer := newProbeOutputObserver(authenticated)
	_, _ = observer.Write([]byte(strings.Repeat("x", maxProbeOutputBytes+1024)))
	_, _ = observer.Write([]byte("\nAuthenti"))
	_, _ = observer.Write([]byte("cated to host using publickey.\n"))
	select {
	case <-authenticated:
	default:
		t.Fatal("split authentication diagnostic was not detected")
	}
	if len(observer.String()) != maxProbeOutputBytes {
		t.Fatalf("captured output length=%d want=%d", len(observer.String()), maxProbeOutputBytes)
	}
}

type probeTestResult struct {
	report core.CheckReport
	err    error
}

func newProbeTestDriver(t *testing.T) *Driver {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return NewWithLookup(func(string) (string, error) { return executable, nil })
}

func probeTestConnection(t *testing.T) core.Connection {
	t.Helper()
	raw, err := json.Marshal(Config{Host: "me@example.com", Forwards: []Forward{}})
	if err != nil {
		t.Fatal(err)
	}
	return core.Connection{
		ConfigVersion: core.ConfigVersion, Kind: core.Kind, Metadata: core.Metadata{Name: "probe"},
		Spec: core.ConnectionSpec{Driver: ID, Config: raw},
	}
}

func configureInheritedStderrProbe(t *testing.T, behavior string) (net.Listener, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if tcp, ok := listener.(*net.TCPListener); ok {
		if err := tcp.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			_ = listener.Close()
			t.Fatal(err)
		}
	}
	marker := filepath.Join(t.TempDir(), "descendant.pid")
	t.Setenv(probeHelperBehavior, behavior)
	t.Setenv(probeHelperMarker, marker)
	t.Setenv(probeHelperReady, listener.Addr().String())
	t.Cleanup(func() {
		_ = listener.Close()
		raw, err := os.ReadFile(marker)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			t.Errorf("read probe descendant marker: %v", err)
			return
		}
		pid, err := strconv.Atoi(string(raw))
		if err != nil {
			t.Errorf("parse probe descendant pid %q: %v", raw, err)
			return
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			t.Errorf("find probe descendant %d: %v", pid, err)
			return
		}
		if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill probe descendant %d: %v", pid, err)
		}
	})
	return listener, marker
}

func waitForProbeHelperReady(t *testing.T, listener net.Listener) {
	t.Helper()
	connection, err := listener.Accept()
	if err != nil {
		t.Fatalf("wait for probe helper readiness: %v", err)
	}
	_ = connection.Close()
	_ = listener.Close()
}

func waitForProbeResult(t *testing.T, result <-chan probeTestResult, timeout time.Duration) probeTestResult {
	t.Helper()
	select {
	case completed := <-result:
		return completed
	case <-time.After(timeout):
		t.Fatalf("probe did not return within %s", timeout)
		return probeTestResult{}
	}
}

func assertProbeCondition(t *testing.T, report core.CheckReport, healthy bool, code string) {
	t.Helper()
	if report.Healthy != healthy || len(report.Conditions) != 1 || report.Conditions[0].Code != code {
		t.Fatalf("probe report=%#v want healthy=%t code=%s", report, healthy, code)
	}
}

func assertProbeDescendantRecorded(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("probe descendant marker: %v", err)
	}
}
