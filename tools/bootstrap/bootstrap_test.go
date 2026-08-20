package bootstrap_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	fakeUname = `#!/bin/sh
case "$1" in
  -s) printf '%s\n' "$TUNLITE_TEST_OS" ;;
  -m) printf '%s\n' "$TUNLITE_TEST_ARCH" ;;
  *) exit 2 ;;
esac
`
	fakeCurl = `#!/bin/sh
url=''
output=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
printf '%s\n' "$url" >> "$TUNLITE_TEST_CURL_LOG"
cp "$TUNLITE_TEST_RELEASE_DIR/${url##*/}" "$output"
`
	fakeTar = `#!/bin/sh
destination=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -C) destination="$2"; shift 2 ;;
    *) shift ;;
  esac
done
cp "$TUNLITE_TEST_BINARY" "$destination/tunlite"
`
	fakeBinary = `#!/bin/sh
[ "$1" = version ] || exit 64
[ "${TUNLITE_TEST_BINARY_FAIL:-0}" != 1 ] || exit 42
printf '%s\n' 'tunlite test version'
`
)

type fixture struct {
	root       string
	home       string
	fakeBin    string
	releaseDir string
	curlLog    string
	binary     string
	checksum   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bootstrap.sh is only supported on macOS and Linux")
	}
	root := t.TempDir()
	f := &fixture{
		root:       root,
		home:       filepath.Join(root, "home"),
		fakeBin:    filepath.Join(root, "fake-bin"),
		releaseDir: filepath.Join(root, "release"),
		curlLog:    filepath.Join(root, "curl.log"),
		binary:     filepath.Join(root, "release-binary"),
	}
	for _, path := range []string{f.home, f.fakeBin, f.releaseDir} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.writeExecutable(t, filepath.Join(f.fakeBin, "uname"), fakeUname)
	f.writeExecutable(t, filepath.Join(f.fakeBin, "curl"), fakeCurl)
	f.writeExecutable(t, filepath.Join(f.fakeBin, "tar"), fakeTar)
	f.writeExecutable(t, f.binary, fakeBinary)
	archive := []byte("fake archive used to exercise download verification\n")
	sum := fmt.Sprintf("%x", sha256.Sum256(archive))
	assets := []string{
		"tunlite_1.0.0-preview.1_macos.tar.gz",
		"tunlite_1.0.0-preview.1_linux_x64.tar.gz",
		"tunlite_1.0.0-preview.1_linux_arm64.tar.gz",
		"tunlite_1.0.0-preview.12_macos.tar.gz",
		"tunlite_1.0.0_macos.tar.gz",
	}
	var checksums strings.Builder
	for _, asset := range assets {
		if err := os.WriteFile(filepath.Join(f.releaseDir, asset), archive, 0o644); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&checksums, "%s  %s\n", sum, asset)
	}
	f.checksum = checksums.String()
	f.writeChecksums(t, f.checksum)
	return f
}

func (f *fixture) writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) writeChecksums(t *testing.T, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.releaseDir, "SHA256SUMS"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) run(t *testing.T, extraEnv map[string]string, args ...string) (string, error) {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/sh", append([]string{filepath.Join(repositoryRoot, "bootstrap.sh")}, args...)...)
	env := make([]string, 0, len(os.Environ())+10)
	for _, item := range os.Environ() {
		name := strings.SplitN(item, "=", 2)[0]
		if strings.HasPrefix(name, "TUNLITE_") || name == "HOME" || name == "PATH" || strings.HasPrefix(name, "XDG_") {
			continue
		}
		env = append(env, item)
	}
	values := map[string]string{
		"HOME":                     f.home,
		"PATH":                     f.fakeBin + string(os.PathListSeparator) + "/usr/bin:/bin:/usr/sbin:/sbin",
		"TUNLITE_RELEASE_BASE_URL": "https://release.test/download",
		"TUNLITE_TEST_ARCH":        "x86_64",
		"TUNLITE_TEST_BINARY":      f.binary,
		"TUNLITE_TEST_CURL_LOG":    f.curlLog,
		"TUNLITE_TEST_OS":          "Darwin",
		"TUNLITE_TEST_RELEASE_DIR": f.releaseDir,
	}
	for name, value := range extraEnv {
		values[name] = value
	}
	for name, value := range values {
		env = append(env, name+"="+value)
	}
	command.Env = env
	raw, runErr := command.CombinedOutput()
	return string(raw), runErr
}

func (f *fixture) curlRequests(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(f.curlLog)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestBootstrapSelectsPlatformAsset(t *testing.T) {
	tests := []struct {
		name, os, arch, asset string
	}{
		{"macOS Intel", "Darwin", "x86_64", "tunlite_1.0.0-preview.1_macos.tar.gz"},
		{"macOS Apple Silicon", "Darwin", "arm64", "tunlite_1.0.0-preview.1_macos.tar.gz"},
		{"Linux x86_64", "Linux", "x86_64", "tunlite_1.0.0-preview.1_linux_x64.tar.gz"},
		{"Linux amd64", "Linux", "amd64", "tunlite_1.0.0-preview.1_linux_x64.tar.gz"},
		{"Linux arm64", "Linux", "arm64", "tunlite_1.0.0-preview.1_linux_arm64.tar.gz"},
		{"Linux aarch64", "Linux", "aarch64", "tunlite_1.0.0-preview.1_linux_arm64.tar.gz"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			installDir := filepath.Join(f.root, "install")
			output, err := f.run(t, map[string]string{"TUNLITE_TEST_OS": test.os, "TUNLITE_TEST_ARCH": test.arch}, "--install-dir", installDir)
			if err != nil {
				t.Fatalf("bootstrap failed: %v\n%s", err, output)
			}
			if !strings.Contains(f.curlRequests(t), "/"+test.asset+"\n") {
				t.Fatalf("wrong asset request:\n%s", f.curlRequests(t))
			}
			if _, err := os.Stat(filepath.Join(installDir, "tunlite")); err != nil {
				t.Fatalf("binary was not installed: %v", err)
			}
		})
	}
}

func TestBootstrapInstallsAtomicallyWithoutChangingShellFiles(t *testing.T) {
	f := newFixture(t)
	installDir := filepath.Join(f.root, "bin with spaces")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := []byte("old binary\n")
	destination := filepath.Join(installDir, "tunlite")
	if err := os.WriteFile(destination, old, 0o755); err != nil {
		t.Fatal(err)
	}
	shellFiles := []string{".bashrc", ".zshrc", ".profile"}
	for _, name := range shellFiles {
		if err := os.WriteFile(filepath.Join(f.home, name), []byte("sentinel "+name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := f.fakeBin + string(os.PathListSeparator) + installDir + string(os.PathListSeparator) + "/usr/bin:/bin:/usr/sbin:/sbin"
	output, err := f.run(t, map[string]string{"PATH": path}, "--install-dir", installDir)
	if err != nil {
		t.Fatalf("bootstrap failed: %v\n%s", err, output)
	}
	if strings.Contains(output, "加入 PATH") {
		t.Fatalf("reported PATH warning even though install directory is present:\n%s", output)
	}
	installed, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(f.binary)
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != string(expected) {
		t.Fatal("installed binary does not match verified staged binary")
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode = %o, want 755", info.Mode().Perm())
	}
	for _, name := range shellFiles {
		raw, err := os.ReadFile(filepath.Join(f.home, name))
		if err != nil || string(raw) != "sentinel "+name+"\n" {
			t.Fatalf("shell file %s changed: contents=%q err=%v", name, raw, err)
		}
	}
}

func TestBootstrapKeepsExistingBinaryWhenStagedBinaryFails(t *testing.T) {
	f := newFixture(t)
	installDir := filepath.Join(f.root, "install")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(installDir, "tunlite")
	old := []byte("known good binary\n")
	if err := os.WriteFile(destination, old, 0o755); err != nil {
		t.Fatal(err)
	}
	output, err := f.run(t, map[string]string{"TUNLITE_TEST_BINARY_FAIL": "1"}, "--install-dir", installDir)
	if err == nil {
		t.Fatalf("bootstrap accepted failing staged binary:\n%s", output)
	}
	raw, readErr := os.ReadFile(destination)
	if readErr != nil || string(raw) != string(old) {
		t.Fatalf("existing binary changed: contents=%q err=%v", raw, readErr)
	}
	staged, globErr := filepath.Glob(filepath.Join(installDir, ".tunlite-install-*"))
	if globErr != nil || len(staged) != 0 {
		t.Fatalf("staged files were not cleaned up: files=%v err=%v", staged, globErr)
	}
}

func TestBootstrapRejectsBadOrMissingChecksumBeforeInstall(t *testing.T) {
	tests := []struct {
		name, checksums, message string
	}{
		{"mismatch", strings.Repeat("0", 64) + "  tunlite_1.0.0-preview.1_macos.tar.gz\n", "checksum 不匹配"},
		{"missing", "", "SHA256SUMS 中没有"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			f.writeChecksums(t, test.checksums)
			installDir := filepath.Join(f.root, "install")
			output, err := f.run(t, nil, "--install-dir", installDir)
			if err == nil || !strings.Contains(output, test.message) {
				t.Fatalf("unexpected result: err=%v\n%s", err, output)
			}
			if _, statErr := os.Stat(filepath.Join(installDir, "tunlite")); !os.IsNotExist(statErr) {
				t.Fatalf("binary exists after checksum failure: %v", statErr)
			}
		})
	}
}

func TestBootstrapRefusesLegacyInstallationBeforeDownload(t *testing.T) {
	tests := []struct {
		name, relative string
	}{
		{"config", filepath.Join("config", "config.json")},
		{"manifest", filepath.Join("data", "install.json")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			legacyHome := filepath.Join(f.root, "legacy")
			legacyPath := filepath.Join(legacyHome, test.relative)
			if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(legacyPath, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := f.run(t, map[string]string{"TUNLITE_HOME": legacyHome})
			if err == nil || !strings.Contains(output, "不会直接覆盖旧版本") {
				t.Fatalf("legacy install was not rejected: err=%v\n%s", err, output)
			}
			if requests := f.curlRequests(t); requests != "" {
				t.Fatalf("download started before legacy refusal:\n%s", requests)
			}
		})
	}
}

func TestBootstrapRefusesLegacyInstallationInDefaultAndXDGPaths(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		path func(*fixture) string
	}{
		{
			name: "default config",
			path: func(f *fixture) string { return filepath.Join(f.home, ".config", "tunlite", "config.json") },
		},
		{
			name: "default state",
			path: func(f *fixture) string { return filepath.Join(f.home, ".local", "state", "tunlite", "install.json") },
		},
		{
			name: "XDG config",
			env:  map[string]string{"XDG_CONFIG_HOME": "CONFIG_HOME"},
			path: func(f *fixture) string { return filepath.Join(f.root, "xdg-config", "tunlite", "config.json") },
		},
		{
			name: "XDG state",
			env:  map[string]string{"XDG_STATE_HOME": "STATE_HOME"},
			path: func(f *fixture) string { return filepath.Join(f.root, "xdg-state", "tunlite", "install.json") },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			legacyPath := test.path(f)
			env := make(map[string]string, len(test.env))
			for name, value := range test.env {
				switch value {
				case "CONFIG_HOME":
					env[name] = filepath.Join(f.root, "xdg-config")
				case "STATE_HOME":
					env[name] = filepath.Join(f.root, "xdg-state")
				}
			}
			if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(legacyPath, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := f.run(t, env)
			if err == nil || !strings.Contains(output, "不会直接覆盖旧版本") {
				t.Fatalf("legacy install was not rejected: err=%v\n%s", err, output)
			}
			if requests := f.curlRequests(t); requests != "" {
				t.Fatalf("download started before legacy refusal:\n%s", requests)
			}
		})
	}
}

func TestBootstrapVersionAndArgumentValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"preview zero", []string{"--version", "v1.0.0-preview.0"}},
		{"preview leading zero", []string{"--version", "v1.0.0-preview.01"}},
		{"preview nonnumeric", []string{"--version", "v1.0.0-preview.x"}},
		{"unsupported stable", []string{"--version", "v1.1.0"}},
		{"missing version value", []string{"--version"}},
		{"missing directory value", []string{"--install-dir"}},
		{"unknown option", []string{"--wat"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			output, err := f.run(t, nil, test.args...)
			if err == nil {
				t.Fatalf("invalid arguments were accepted:\n%s", output)
			}
			if requests := f.curlRequests(t); requests != "" {
				t.Fatalf("download started before argument refusal:\n%s", requests)
			}
		})
	}
}

func TestBootstrapFlagsOverrideEnvironmentAndAcceptPreviewNumber(t *testing.T) {
	f := newFixture(t)
	installDir := filepath.Join(f.root, "flag-install")
	envInstallDir := filepath.Join(f.root, "env-install")
	output, err := f.run(t, map[string]string{
		"TUNLITE_INSTALL_DIR": envInstallDir,
		"TUNLITE_VERSION":     "v1.0.0-preview.999",
	}, "--version", "v1.0.0-preview.12", "--install-dir", installDir)
	if err != nil {
		t.Fatalf("bootstrap failed: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(installDir, "tunlite")); err != nil {
		t.Fatalf("flag install directory was not used: %v", err)
	}
	if _, err := os.Stat(filepath.Join(envInstallDir, "tunlite")); !os.IsNotExist(err) {
		t.Fatalf("environment install directory unexpectedly used: %v", err)
	}
	if !strings.Contains(f.curlRequests(t), "tunlite_1.0.0-preview.12_macos.tar.gz") {
		t.Fatalf("flag version was not used:\n%s", f.curlRequests(t))
	}
}

func TestBootstrapAcceptsStableV1Version(t *testing.T) {
	f := newFixture(t)
	installDir := filepath.Join(f.root, "stable-install")
	output, err := f.run(t, nil, "--version", "v1.0.0", "--install-dir", installDir)
	if err != nil {
		t.Fatalf("bootstrap failed: %v\n%s", err, output)
	}
	if !strings.Contains(f.curlRequests(t), "tunlite_1.0.0_macos.tar.gz") {
		t.Fatalf("stable asset was not used:\n%s", f.curlRequests(t))
	}
}

func TestBootstrapReportsUnsupportedPlatforms(t *testing.T) {
	tests := []struct {
		name, os, arch, message string
	}{
		{"operating system", "FreeBSD", "amd64", "仅支持 macOS 和 Linux"},
		{"mac architecture", "Darwin", "ppc64", "不支持的 Mac CPU 架构"},
		{"linux architecture", "Linux", "riscv64", "不支持的 CPU 架构"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			output, err := f.run(t, map[string]string{"TUNLITE_TEST_OS": test.os, "TUNLITE_TEST_ARCH": test.arch})
			if err == nil || !strings.Contains(output, test.message) {
				t.Fatalf("unsupported platform was accepted: err=%v\n%s", err, output)
			}
			if requests := f.curlRequests(t); requests != "" {
				t.Fatalf("download started before platform refusal:\n%s", requests)
			}
		})
	}
}

func TestBootstrapUsesDefaultDirectoryAndReportsMissingPathEntry(t *testing.T) {
	f := newFixture(t)
	output, err := f.run(t, nil)
	if err != nil {
		t.Fatalf("bootstrap failed: %v\n%s", err, output)
	}
	destination := filepath.Join(f.home, ".local", "bin", "tunlite")
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("default destination was not used: %v", err)
	}
	if !strings.Contains(output, "请把 "+filepath.Dir(destination)+" 加入 PATH") {
		t.Fatalf("missing PATH guidance:\n%s", output)
	}
}
