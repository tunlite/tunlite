package ssh

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/yuanyuanzijin/tunlite/internal/core"
)

func TestDriverBuildsDeterministicArgvPlan(t *testing.T) {
	config := Config{
		Host: "me@example.com", Port: 2222, IdentityFile: "/keys/id",
		Jump: []string{"bastion", "jump@host:2200"},
		Forwards: []Forward{
			{Type: "local", Bind: "127.0.0.1", SrcPort: 8080, DestHost: "localhost", DestPort: 80},
			{Type: "remote", Bind: "127.0.0.1", SrcPort: 9000, DestHost: "localhost", DestPort: 3000},
			{Type: "dynamic", Bind: "0.0.0.0", SrcPort: 1080},
		},
		SSHOptions: []string{"Compression=yes"},
	}
	raw, _ := json.Marshal(config)
	connection := core.Connection{
		ConfigVersion: core.ConfigVersion, Kind: core.Kind, Metadata: core.Metadata{Name: "example"},
		Spec: core.ConnectionSpec{Driver: ID, Config: raw},
	}
	driver := NewWithLookup(func(string) (string, error) { return "/usr/bin/ssh", nil })

	got, err := driver.Plan(context.Background(), connection, core.PlanOptions{NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	wantTail := []string{
		"-i", "/keys/id", "-o", "IdentitiesOnly=yes", "-p", "2222",
		"-J", "bastion,jump@host:2200",
		"-L", "127.0.0.1:8080:localhost:80",
		"-R", "127.0.0.1:9000:localhost:3000",
		"-D", "0.0.0.0:1080", "-o", "Compression=yes", "--", "me@example.com",
	}
	if !reflect.DeepEqual(got.Exec.Args[len(got.Exec.Args)-len(wantTail):], wantTail) {
		t.Fatalf("unexpected argv tail:\n got: %#v\nwant: %#v", got.Exec.Args, wantTail)
	}
	if got.Mode != "direct" || got.Exec.Program != "/usr/bin/ssh" || got.Lifecycle.Restart != "on-failure" {
		t.Fatalf("unexpected execution plan: %#v", got)
	}
	if !containsPair(got.Exec.Args, "-F", "none") {
		t.Fatalf("managed SSH plan can still read ambient config: %#v", got.Exec.Args)
	}
	if len(got.HealthChecks) != 2 {
		t.Fatalf("local and dynamic forwards should have health checks: %#v", got.HealthChecks)
	}
}

func TestParseForwardMatchesOpenSSHForms(t *testing.T) {
	tests := []struct {
		flag string
		spec string
		want Forward
	}{
		{"-L", "8080:localhost:80", Forward{Type: "local", Bind: "127.0.0.1", SrcPort: 8080, DestHost: "localhost", DestPort: 80}},
		{"-R", "0.0.0.0:9000:localhost:3000", Forward{Type: "remote", Bind: "0.0.0.0", SrcPort: 9000, DestHost: "localhost", DestPort: 3000}},
		{"-D", "1080", Forward{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1080}},
		{"-L", "[::1]:8080:[2001:db8::1]:80", Forward{Type: "local", Bind: "::1", SrcPort: 8080, DestHost: "2001:db8::1", DestPort: 80}},
	}
	for _, test := range tests {
		got, err := ParseForward(test.flag, test.spec)
		if err != nil {
			t.Fatalf("%s %s: %v", test.flag, test.spec, err)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%s %s: got %#v, want %#v", test.flag, test.spec, got, test.want)
		}
	}
}

func TestPlanTerminatesSSHOptionParsingBeforeHost(t *testing.T) {
	config := Config{Host: "me@host", Forwards: []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1080}}}
	raw, _ := json.Marshal(config)
	connection := core.Connection{ConfigVersion: core.ConfigVersion, Kind: core.Kind, Metadata: core.Metadata{Name: "safe"}, Spec: core.ConnectionSpec{Driver: ID, Config: raw}}
	got, err := NewWithLookup(func(string) (string, error) { return "/usr/bin/ssh", nil }).Plan(context.Background(), connection, core.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	last := got.Exec.Args[len(got.Exec.Args)-2:]
	if !reflect.DeepEqual(last, []string{"--", "me@host"}) {
		t.Fatalf("unsafe argv ending: %#v", last)
	}
}

func TestPlanRejectsSSHOptionsThatCanTerminateOptionParsing(t *testing.T) {
	config := Config{
		Host:       "me@host",
		Forwards:   []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1080}},
		SSHOptions: []string{"--"},
	}
	raw, _ := json.Marshal(config)
	connection := core.Connection{ConfigVersion: core.ConfigVersion, Kind: core.Kind, Metadata: core.Metadata{Name: "safe"}, Spec: core.ConnectionSpec{Driver: ID, Config: raw}}
	_, err := NewWithLookup(func(string) (string, error) { return "/usr/bin/ssh", nil }).Plan(context.Background(), connection, core.PlanOptions{})
	if err == nil {
		t.Fatal("unsafe SSH option was accepted")
	}
}

func TestNonInteractiveControlsBatchMode(t *testing.T) {
	config := Config{Host: "me@host", Forwards: []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1080}}}
	raw, _ := json.Marshal(config)
	connection := core.Connection{ConfigVersion: core.ConfigVersion, Kind: core.Kind, Metadata: core.Metadata{Name: "batch"}, Spec: core.ConnectionSpec{Driver: ID, Config: raw}}
	driver := NewWithLookup(func(string) (string, error) { return "/usr/bin/ssh", nil })

	interactive, err := driver.Plan(context.Background(), connection, core.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	nonInteractive, err := driver.Plan(context.Background(), connection, core.PlanOptions{NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	if containsPair(interactive.Exec.Args, "-o", "BatchMode=yes") {
		t.Fatal("interactive foreground plan unexpectedly disables prompts")
	}
	if !containsPair(nonInteractive.Exec.Args, "-o", "BatchMode=yes") {
		t.Fatal("non-interactive plan does not enforce BatchMode")
	}
}

func TestValidateSSHOptionsUsesConservativeAllowlist(t *testing.T) {
	base := Config{Host: "me@host", Forwards: []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1080}}}
	allowed := []string{
		"Compression=yes", "UserKnownHostsFile=/tmp/known hosts", "LogLevel=VERBOSE",
		"PreferredAuthentications=publickey", "Ciphers=chacha20-poly1305@openssh.com",
	}
	for _, option := range allowed {
		config := base
		config.SSHOptions = []string{option}
		if err := Validate(config); err != nil {
			t.Errorf("safe option %q was rejected: %v", option, err)
		}
	}

	rejected := []string{
		"ProxyCommand=helper %h %p", "LocalCommand=touch /tmp/pwned", "PermitLocalCommand=yes",
		"RemoteCommand=uname", "KnownHostsCommand=helper", "Include=other.conf",
		"LocalForward=8080 localhost:80", "RemoteForward=9000 localhost:80", "DynamicForward=1080",
		"ClearAllForwardings=yes", "BatchMode=no", "ExitOnForwardFailure=no",
		"ServerAliveInterval=0", "ServerAliveCountMax=99", "TCPKeepAlive=no",
		"ConnectTimeout=0", "StrictHostKeyChecking=no", "ProxyJump=bastion",
		"IdentityFile=/tmp/other", "IdentitiesOnly=no", "ControlMaster=auto",
		"PKCS11Provider=/tmp/provider.so", "ForwardAgent=yes", "RequestTTY=yes",
	}
	for _, option := range rejected {
		config := base
		config.SSHOptions = []string{option}
		if err := Validate(config); err == nil {
			t.Errorf("unsafe or managed option %q was accepted", option)
		}
	}
}

func TestValidateRejectsDuplicateSSHOptionCaseInsensitively(t *testing.T) {
	config := Config{
		Host:       "me@host",
		Forwards:   []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1080}},
		SSHOptions: []string{"Compression=yes", "compression=no"},
	}
	if err := Validate(config); err == nil {
		t.Fatal("duplicate SSH option was accepted")
	}
}

func TestDecodeConfigRejectsUnknownFields(t *testing.T) {
	_, err := DecodeConfig(json.RawMessage(`{"host":"me@host","forwards":[],"futureField":true}`))
	if err == nil {
		t.Fatal("unknown SSH config field was accepted")
	}
}

func TestPlanRejectsUnsupportedConfigVersion(t *testing.T) {
	config := Config{Host: "me@host", Forwards: []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1080}}}
	raw, _ := json.Marshal(config)
	connection := core.Connection{ConfigVersion: 2, Kind: core.Kind, Metadata: core.Metadata{Name: "future"}, Spec: core.ConnectionSpec{Driver: ID, Config: raw}}
	driver := NewWithLookup(func(string) (string, error) { return "/usr/bin/ssh", nil })
	registry, err := core.NewRegistry(driver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.NewPlanner(registry).Plan(context.Background(), connection, core.PlanOptions{}); err == nil {
		t.Fatal("unsupported Connection config version was accepted")
	}
}

func TestConfigFromPlanRoundTripsSupportedSSHArgv(t *testing.T) {
	raw, _ := json.Marshal(Config{
		Host: "me@example.com", Port: 2222, IdentityFile: "/tmp/id key",
		Jump:       []string{"jump@example.com"},
		Forwards:   []Forward{{Type: "local", Bind: "127.0.0.1", SrcPort: 15432, DestHost: "localhost", DestPort: 5432}},
		SSHOptions: []string{"Compression=yes"},
	})
	connection := core.Connection{
		ConfigVersion: core.ConfigVersion, Kind: core.Kind, Metadata: core.Metadata{Name: "roundtrip"},
		Spec: core.ConnectionSpec{Driver: ID, Config: raw},
	}
	value, err := NewWithLookup(func(string) (string, error) { return "/usr/bin/ssh", nil }).Plan(context.Background(), connection, core.PlanOptions{NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ConfigFromPlan(value)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "me@example.com" || got.Port != 2222 || got.IdentityFile != "/tmp/id key" || len(got.Forwards) != 1 || len(got.SSHOptions) != 1 {
		t.Fatalf("round-trip config=%#v", got)
	}
}

func TestConfigFromPlanRejectsAmbientSSHConfig(t *testing.T) {
	raw, _ := json.Marshal(Config{
		Host:     "me@example.com",
		Forwards: []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1080}},
	})
	connection := core.Connection{
		ConfigVersion: core.ConfigVersion, Kind: core.Kind, Metadata: core.Metadata{Name: "ambient"},
		Spec: core.ConnectionSpec{Driver: ID, Config: raw},
	}
	value, err := NewWithLookup(func(string) (string, error) { return "/usr/bin/ssh", nil }).Plan(context.Background(), connection, core.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	value.Exec.Args = value.Exec.Args[2:]
	if _, err := ConfigFromPlan(value); err == nil {
		t.Fatal("an OpenSSH plan that reads ambient config was accepted for management")
	}
}

func TestValidateRejectsDuplicateListeningEndpoint(t *testing.T) {
	config := Config{
		Host: "me@host",
		Forwards: []Forward{
			{Type: "local", Bind: "127.0.0.1", SrcPort: 1080, DestHost: "localhost", DestPort: 80},
			{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1080},
		},
	}
	if err := Validate(config); err == nil {
		t.Fatal("duplicate listening endpoint was accepted")
	}
}

func containsPair(values []string, first, second string) bool {
	for i := 0; i+1 < len(values); i++ {
		if values[i] == first && values[i+1] == second {
			return true
		}
	}
	return false
}
