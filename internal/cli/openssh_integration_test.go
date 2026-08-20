package cli

import (
	"bufio"
	"bytes"
	"context"
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
	defer stopSSHD()
	sshdLogs := &lockedBuffer{}
	sshd := exec.CommandContext(sshdCtx, sshdPath, "-D", "-e", "-f", configPath)
	sshd.Stdout, sshd.Stderr = sshdLogs, sshdLogs
	if err := sshd.Start(); err != nil {
		t.Fatal(err)
	}
	sshdDone := make(chan error, 1)
	go func() { sshdDone <- sshd.Wait() }()
	if err := waitTCP(fmt.Sprintf("127.0.0.1:%d", sshdPort), 5*time.Second); err != nil {
		stopSSHD()
		<-sshdDone
		t.Fatalf("sshd did not start: %v\n%s", err, sshdLogs.String())
	}

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
		"add", "integration", "--to", fmt.Sprintf("%s@127.0.0.1:%d", current.Username, sshdPort), "-i", clientKey,
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
	stopSSHD()
	select {
	case <-sshdDone:
	case <-time.After(5 * time.Second):
		t.Fatal("sshd did not stop")
	}
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
