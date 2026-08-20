package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/service"
)

func TestRealOpenSSHAllForwardTypes(t *testing.T) {
	if os.Getenv("TUNLITE_REAL_SSH") != "1" {
		t.Skip("set TUNLITE_REAL_SSH=1 to run the real OpenSSH integration test")
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client is not installed")
	}
	server := startRealSSHD(t)
	temp := t.TempDir()

	const marker = "tunlite-real-openssh-ok"
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, marker)
	}))
	defer target.Close()
	targetURL, _ := url.Parse(target.URL)
	targetHost, targetPortText, _ := net.SplitHostPort(targetURL.Host)
	localPort := reserveTCPPort(t)
	remotePort := reserveTCPPort(t)
	socksPort := reserveTCPPort(t)

	t.Setenv("TUNLITE_SSH", sshPath)
	t.Setenv("TUNLITE_HOME", filepath.Join(temp, "tunlite-home"))
	adapter := newFakeServiceAdapter(filepath.Join(temp, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	code, out, addErr := invoke(t,
		"add", "integration", "--to", fmt.Sprintf("%s@127.0.0.1:%d", server.username, server.port), "-i", server.clientKey,
		"-L", fmt.Sprintf("127.0.0.1:%d:%s:%s", localPort, targetHost, targetPortText),
		"-R", fmt.Sprintf("127.0.0.1:%d:%s:%s", remotePort, targetHost, targetPortText),
		"-D", fmt.Sprintf("127.0.0.1:%d", socksPort),
		"--ssh-opt", "UserKnownHostsFile=/dev/null",
	)
	if code != 0 {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, addErr)
	}
	runCtx, stopRun := context.WithCancel(context.Background())
	stdout, stderr := &lockedBuffer{}, &lockedBuffer{}
	runDone := make(chan int, 1)
	go func() {
		runDone <- runContext(runCtx, []string{
			"run", "integration", "--json", "--non-interactive",
		}, IO{Out: stdout, Err: stderr}, func() (service.Adapter, error) { return adapter, nil })
	}()

	verifyHTTPForward(t, fmt.Sprintf("http://127.0.0.1:%d", localPort), marker, 10*time.Second)
	verifyHTTPForward(t, fmt.Sprintf("http://127.0.0.1:%d", remotePort), marker, 10*time.Second)
	verifySOCKSForward(t, fmt.Sprintf("127.0.0.1:%d", socksPort), targetHost, targetPortText, marker, 10*time.Second)
	stopRun()
	select {
	case code := <-runDone:
		if code != 0 {
			t.Fatalf("tunlite run exit=%d\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tunlite run did not stop")
	}
}

func TestReleaseBinaryRealOpenSSHNativeServiceTraffic(t *testing.T) {
	if os.Getenv("TUNLITE_REAL_RELEASE_E2E") != "1" {
		t.Skip("set TUNLITE_REAL_RELEASE_E2E=1 and TUNLITE_RELEASE_BINARY to run the release/native-service E2E")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native services are unavailable on this platform")
	}
	releaseBinary := os.Getenv("TUNLITE_RELEASE_BINARY")
	if releaseBinary == "" || !filepath.IsAbs(releaseBinary) {
		t.Fatal("TUNLITE_RELEASE_BINARY must be an absolute path")
	}
	if info, err := os.Stat(releaseBinary); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("release binary is not executable: info=%#v err=%v", info, err)
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client is not installed")
	}
	server := startRealSSHD(t)
	root := t.TempDir()
	t.Setenv("TUNLITE_HOME", filepath.Join(root, "tunlite-home"))
	t.Setenv("TUNLITE_SSH", sshPath)

	versionOutput := invokeReleaseBinary(t, releaseBinary, "version", "--json")
	expectedVersion := os.Getenv("TUNLITE_EXPECTED_RELEASE_VERSION")
	if expectedVersion != "" && !strings.Contains(versionOutput, `"version":"`+expectedVersion+`"`) {
		t.Fatalf("release binary reported wrong version: %s", versionOutput)
	}

	const marker = "tunlite-release-native-e2e-ok"
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, marker)
	}))
	defer target.Close()
	targetURL, _ := url.Parse(target.URL)
	targetHost, targetPortText, _ := net.SplitHostPort(targetURL.Host)
	localPort := reserveTCPPort(t)
	remotePort := reserveTCPPort(t)
	socksPort := reserveTCPPort(t)
	name := "it-release-e2e"
	assertReleaseServiceNameAvailable(t, name)

	invokeReleaseBinary(t, releaseBinary,
		"add", name, "--to", fmt.Sprintf("%s@127.0.0.1:%d", server.username, server.port), "-i", server.clientKey,
		"-L", fmt.Sprintf("127.0.0.1:%d:%s:%s", localPort, targetHost, targetPortText),
		"-R", fmt.Sprintf("127.0.0.1:%d:%s:%s", remotePort, targetHost, targetPortText),
		"-D", fmt.Sprintf("127.0.0.1:%d", socksPort),
		"--ssh-opt", "UserKnownHostsFile=/dev/null", "--json", "--non-interactive",
	)
	removed := false
	t.Cleanup(func() {
		if removed {
			return
		}
		bestEffortReleaseCommand(releaseBinary, "disable", name, "--json", "--non-interactive")
		bestEffortReleaseCommand(releaseBinary, "remove", name, "--json", "--non-interactive")
	})

	invokeReleaseBinary(t, releaseBinary, "start", name, "--json", "--non-interactive")
	background := releaseServiceState(t, releaseBinary, name)
	assertReleaseServiceState(t, background, service.ModeBackground, service.ProcessRunning)
	verifyReleaseTraffic(t, localPort, remotePort, socksPort, targetHost, targetPortText, marker)

	backgroundPID := releaseServicePID(t, background)
	invokeReleaseBinary(t, releaseBinary, "enable", name, "--json", "--non-interactive")
	persistent := releaseServiceState(t, releaseBinary, name)
	assertReleaseServiceState(t, persistent, service.ModePersistent, service.ProcessRunning)
	if persistentPID := releaseServicePID(t, persistent); persistentPID != backgroundPID {
		t.Fatalf("enable restarted provider: background pid=%d persistent pid=%d", backgroundPID, persistentPID)
	}
	verifyReleaseTraffic(t, localPort, remotePort, socksPort, targetHost, targetPortText, marker)

	assertReleaseSSHProcess(t, backgroundPID, server.clientKey)
	process, err := os.FindProcess(backgroundPID)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatalf("kill isolated release E2E provider: %v", err)
	}
	restarted := waitForReleaseServiceRestart(t, releaseBinary, name, backgroundPID, 10*time.Second)
	assertReleaseServiceState(t, restarted, service.ModePersistent, service.ProcessRunning)
	verifyReleaseTraffic(t, localPort, remotePort, socksPort, targetHost, targetPortText, marker)

	invokeReleaseBinary(t, releaseBinary, "disable", name, "--json", "--non-interactive")
	stopped := releaseServiceState(t, releaseBinary, name)
	assertReleaseServiceState(t, stopped, service.ModeStopped, service.ProcessInactive)
	invokeReleaseBinary(t, releaseBinary, "remove", name, "--json", "--non-interactive")
	removed = true
}

type realSSHD struct {
	username  string
	port      int
	clientKey string
}

func startRealSSHD(t *testing.T) realSSHD {
	t.Helper()
	sshdPath, err := exec.LookPath("sshd")
	if err != nil {
		if _, statErr := os.Stat("/usr/sbin/sshd"); statErr != nil {
			t.Skip("OpenSSH server is not installed")
		}
		sshdPath = "/usr/sbin/sshd"
	}
	keygenPath, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen is not installed")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	hostKey := filepath.Join(temp, "host_key")
	clientKey := filepath.Join(temp, "client_key")
	generateKey(t, keygenPath, hostKey)
	generateKey(t, keygenPath, clientKey)
	publicKey, err := os.ReadFile(clientKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	authorizedKeys := filepath.Join(temp, "authorized_keys")
	if err := os.WriteFile(authorizedKeys, publicKey, 0o600); err != nil {
		t.Fatal(err)
	}
	sshdPort := reserveTCPPort(t)
	configPath := filepath.Join(temp, "sshd_config")
	config := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
AuthorizedKeysFile %s
PidFile %s
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
StrictModes no
UsePAM no
AllowTcpForwarding yes
GatewayPorts clientspecified
LogLevel VERBOSE
AllowUsers %s
`, sshdPort, hostKey, authorizedKeys, filepath.Join(temp, "sshd.pid"), current.Username)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	validate := exec.Command(sshdPath, "-t", "-f", configPath)
	if output, err := validate.CombinedOutput(); err != nil {
		t.Fatalf("sshd config is invalid: %v\n%s", err, output)
	}
	sshdCtx, stopSSHD := context.WithCancel(context.Background())
	sshdLogs := &lockedBuffer{}
	sshd := exec.CommandContext(sshdCtx, sshdPath, "-D", "-e", "-f", configPath)
	sshd.Stdout, sshd.Stderr = sshdLogs, sshdLogs
	if err := sshd.Start(); err != nil {
		t.Fatal(err)
	}
	sshdDone := make(chan error, 1)
	go func() { sshdDone <- sshd.Wait() }()
	t.Cleanup(func() {
		stopSSHD()
		select {
		case <-sshdDone:
		case <-time.After(5 * time.Second):
			t.Errorf("sshd did not stop\n%s", sshdLogs.String())
		}
	})
	if err := waitTCP(fmt.Sprintf("127.0.0.1:%d", sshdPort), 5*time.Second); err != nil {
		t.Fatalf("sshd did not start: %v\n%s", err, sshdLogs.String())
	}
	return realSSHD{username: current.Username, port: sshdPort, clientKey: clientKey}
}

func invokeReleaseBinary(t *testing.T, binary string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("release command %q failed: %v\nstdout=%s\nstderr=%s", args, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func assertReleaseServiceNameAvailable(t *testing.T, name string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	label := "dev.tunlite.connection." + name
	plist := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	if _, err := os.Stat(plist); err == nil {
		t.Fatalf("refusing to use occupied release E2E service name: %s exists", plist)
	} else if !os.IsNotExist(err) {
		t.Fatalf("inspect release E2E service path: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	if output, err := exec.CommandContext(ctx, "launchctl", "print", target).CombinedOutput(); err == nil {
		t.Fatalf("refusing to use occupied release E2E launchd label %s:\n%s", target, output)
	}
}

func bestEffortReleaseCommand(binary string, args ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, binary, args...).Run()
}

func releaseServiceState(t *testing.T, binary, name string) service.State {
	t.Helper()
	output := invokeReleaseBinary(t, binary, "status", name, "--json", "--non-interactive")
	var response struct {
		Results []serviceItemResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode release status: %v\n%s", err, output)
	}
	if len(response.Results) != 1 || response.Results[0].Error != nil || response.Results[0].State == nil {
		t.Fatalf("unexpected release status: %#v", response.Results)
	}
	return *response.Results[0].State
}

func assertReleaseServiceState(t *testing.T, state service.State, mode, process string) {
	t.Helper()
	if state.Mode != mode || state.ProcessState != process || state.ConsistencyState != service.ConsistencyConsistent {
		t.Fatalf("unexpected release service state: %#v", state)
	}
}

func releaseServicePID(t *testing.T, state service.State) int {
	t.Helper()
	pid, ok := releaseServicePIDValue(state)
	if !ok {
		key := "mainPID"
		if runtime.GOOS == "darwin" {
			key = "pid"
		}
		t.Fatalf("release state has no valid %s: %#v", key, state.Platform)
	}
	return pid
}

func releaseServicePIDValue(state service.State) (int, bool) {
	key := "mainPID"
	if runtime.GOOS == "darwin" {
		key = "pid"
	}
	value, ok := state.Platform[key].(string)
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(value)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func assertReleaseSSHProcess(t *testing.T, pid int, clientKey string) {
	t.Helper()
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").CombinedOutput()
	if err != nil || !strings.Contains(string(output), clientKey) {
		t.Fatalf("refusing to kill unverified process pid=%d: err=%v command=%q", pid, err, output)
	}
}

func waitForReleaseServiceRestart(t *testing.T, binary, name string, previousPID int, timeout time.Duration) service.State {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last service.State
	for time.Now().Before(deadline) {
		last = releaseServiceState(t, binary, name)
		if last.ProcessState == service.ProcessRunning {
			if pid, ok := releaseServicePIDValue(last); ok && pid != previousPID {
				return last
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("release service did not restart after pid %d: %#v", previousPID, last)
	return service.State{}
}

func verifyReleaseTraffic(t *testing.T, localPort, remotePort, socksPort int, targetHost, targetPortText, marker string) {
	t.Helper()
	verifyHTTPForward(t, fmt.Sprintf("http://127.0.0.1:%d", localPort), marker, 10*time.Second)
	verifyHTTPForward(t, fmt.Sprintf("http://127.0.0.1:%d", remotePort), marker, 10*time.Second)
	verifySOCKSForward(t, fmt.Sprintf("127.0.0.1:%d", socksPort), targetHost, targetPortText, marker, 10*time.Second)
}

func generateKey(t *testing.T, keygen, path string) {
	t.Helper()
	command := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-f", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate key: %v\n%s", err, output)
	}
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func waitTCP(address string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", address)
}

func verifyHTTPForward(t *testing.T, endpoint, marker string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	var lastErr error
	for time.Now().Before(deadline) {
		response, err := client.Get(endpoint)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK && strings.Contains(string(body), marker) {
				return
			}
			lastErr = fmt.Errorf("status=%d body=%q readErr=%v", response.StatusCode, body, readErr)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("HTTP forward %s failed: %v", endpoint, lastErr)
}

func verifySOCKSForward(t *testing.T, proxy, targetHost, targetPortText, marker string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := socksHTTP(proxy, targetHost, targetPortText, marker); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("SOCKS forward %s failed: %v", proxy, lastErr)
}

func socksHTTP(proxy, targetHost, targetPortText, marker string) error {
	connection, err := net.DialTimeout("tcp", proxy, 500*time.Millisecond)
	if err != nil {
		return err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := connection.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(connection, greeting); err != nil || !bytes.Equal(greeting, []byte{5, 0}) {
		return fmt.Errorf("SOCKS greeting: %v %v", greeting, err)
	}
	port, _ := strconv.Atoi(targetPortText)
	ip := net.ParseIP(targetHost).To4()
	if ip == nil {
		return fmt.Errorf("target is not IPv4: %s", targetHost)
	}
	request := []byte{5, 1, 0, 1, ip[0], ip[1], ip[2], ip[3], byte(port >> 8), byte(port)}
	if _, err := connection.Write(request); err != nil {
		return err
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return err
	}
	if header[1] != 0 {
		return fmt.Errorf("SOCKS connect reply=%d", header[1])
	}
	addressLength := 0
	switch header[3] {
	case 1:
		addressLength = 4
	case 4:
		addressLength = 16
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(connection, length); err != nil {
			return err
		}
		addressLength = int(length[0])
	default:
		return fmt.Errorf("SOCKS address type=%d", header[3])
	}
	if _, err := io.CopyN(io.Discard, connection, int64(addressLength+2)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(connection, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", targetHost); err != nil {
		return err
	}
	response, err := io.ReadAll(bufio.NewReader(connection))
	if err != nil {
		return err
	}
	if !bytes.Contains(response, []byte(marker)) {
		return fmt.Errorf("marker missing from SOCKS response: %q", response)
	}
	return nil
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (buffer *lockedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buf.Write(value)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buf.String()
}
