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
	driver := NewWithLookup(func(string) (string, error) { return "ssh", nil })

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
	if got.Mode != "direct" || got.Exec.Program != "ssh" || got.Lifecycle.Restart != "always" {
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
	got, err := NewWithLookup(func(string) (string, error) { return "ssh", nil }).Plan(context.Background(), connection, core.PlanOptions{})
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
	_, err := NewWithLookup(func(string) (string, error) { return "ssh", nil }).Plan(context.Background(), connection, core.PlanOptions{})
	if err == nil {
		t.Fatal("unsafe SSH option was accepted")
	}
}

func TestNonInteractiveControlsBatchMode(t *testing.T) {
	config := Config{Host: "me@host", Forwards: []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1080}}}
	raw, _ := json.Marshal(config)
	connection := core.Connection{ConfigVersion: core.ConfigVersion, Kind: core.Kind, Metadata: core.Metadata{Name: "batch"}, Spec: core.ConnectionSpec{Driver: ID, Config: raw}}
	driver := NewWithLookup(func(string) (string, error) { return "ssh", nil })

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
		"Compression=yes", "LogLevel=VERBOSE",
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
		"SetEnv=TOKEN=secret", "setenv=TOKEN=secret",
		"ProxyCommand=helper %h %p", "LocalCommand=touch /tmp/pwned", "PermitLocalCommand=yes",
		"RemoteCommand=uname", "KnownHostsCommand=helper", "Include=other.conf",
		"LocalForward=8080 localhost:80", "RemoteForward=9000 localhost:80", "DynamicForward=1080",
		"ClearAllForwardings=yes", "BatchMode=no", "ExitOnForwardFailure=no",
		"ServerAliveInterval=0", "ServerAliveCountMax=99", "TCPKeepAlive=no",
		"ConnectTimeout=0", "StrictHostKeyChecking=no", "ProxyJump=bastion",
		"IdentityFile=/tmp/other", "IdentitiesOnly=no", "ControlMaster=auto",
		"PKCS11Provider=/tmp/provider.so", "ForwardAgent=yes", "RequestTTY=yes",
		"Hostname=actual.example", "User=other", "HostKeyAlias=alias",
		"CanonicalDomains=example.com", "CanonicalizeFallbackLocal=yes", "CanonicalizeHostname=yes",
		"CanonicalizeMaxDots=1", "CanonicalizePermittedCNAMEs=*.example.com:*.internal",
		"NoHostAuthenticationForLocalhost=yes",
	}
	for _, option := range rejected {
		config := base
		config.SSHOptions = []string{option}
		if err := Validate(config); err == nil {
			t.Errorf("unsafe or managed option %q was accepted", option)
		}
	}
}

func TestValidateSSHOptionsRejectsKnownHostsPathOverrides(t *testing.T) {
	base := Config{Host: "me@host", Forwards: []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1080}}}
	rejected := []string{
		"UserKnownHostsFile=/tmp/known_hosts",
		"GlobalKnownHostsFile=/etc/ssh/ssh_known_hosts",
		"UserKnownHostsFile=/dev/null",
		"UserKnownHostsFile=none",
		`UserKnownHostsFile=n""one`,
		`UserKnownHostsFile=/dev/nu""ll`,
	}
	for _, option := range rejected {
		config := base
		config.SSHOptions = []string{option}
		if err := Validate(config); err == nil {
			t.Errorf("known-hosts path override %q was accepted", option)
		}
	}
}

func TestParseTargetAcceptsStrictDNSIPv4AndBracketedIPv6(t *testing.T) {
	tests := []struct {
		value string
		host  string
		port  int
	}{
		{value: "deploy@app01", host: "deploy@app01", port: 22},
		{value: "app01.internal.", host: "app01.internal.", port: 22},
		{value: "deploy@app01.internal:2222", host: "deploy@app01.internal", port: 2222},
		{value: "192.0.2.10", host: "192.0.2.10", port: 22},
		{value: "ops@[2001:db8::10]", host: "ops@2001:db8::10", port: 22},
		{value: "ops@[2001:db8::10]:2200", host: "ops@2001:db8::10", port: 2200},
	}
	for _, test := range tests {
		host, port, err := ParseTarget(test.value)
		if err != nil {
			t.Errorf("safe target %q was rejected: %v", test.value, err)
			continue
		}
		if host != test.host || port != test.port {
			t.Errorf("target %q parsed as host=%q port=%d, want host=%q port=%d", test.value, host, port, test.host, test.port)
		}
	}

	hops, err := ParseJump("jump@bastion:2200,ops@[2001:db8::20]:2222,192.0.2.20")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"jump@bastion:2200", "ops@[2001:db8::20]:2222", "192.0.2.20"}
	if !reflect.DeepEqual(hops, want) {
		t.Fatalf("multi-hop jump changed: got=%#v want=%#v", hops, want)
	}
}

func TestPlanUsesBareIPv6HostAndStructuredPort(t *testing.T) {
	config := Config{
		Host:     "ops@2001:db8::10",
		Port:     2200,
		Jump:     []string{"jump@[2001:db8::20]:2222"},
		Forwards: []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1080}},
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	driver := NewWithLookup(func(string) (string, error) { return "ssh", nil })
	planned, err := driver.Plan(context.Background(), core.Connection{
		ConfigVersion: core.ConfigVersion,
		Kind:          core.Kind,
		Metadata:      core.Metadata{Name: "ipv6"},
		Spec:          core.ConnectionSpec{Driver: ID, Config: raw},
	}, core.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPair(planned.Exec.Args, "-p", "2200") || !containsPair(planned.Exec.Args, "-J", "jump@[2001:db8::20]:2222") {
		t.Fatalf("IPv6 plan lost structured port or ProxyJump brackets: %#v", planned.Exec.Args)
	}
	last := planned.Exec.Args[len(planned.Exec.Args)-2:]
	if !reflect.DeepEqual(last, []string{"--", "ops@2001:db8::10"}) {
		t.Fatalf("IPv6 main target was not canonicalized for OpenSSH: %#v", last)
	}
}

func TestParseTargetRejectsOptionAndCommandInjectionGrammar(t *testing.T) {
	rejected := []string{
		`user@-oProxyCommand=id 127.0.0.1`,
		`user@-oProxyCommand=id`,
		`user@host name`, "user@host\tname", `user@"host"`, `user@'host'`,
		"user@host;id", "user@host|id", "user@host&id", "user@$(id)", "user@`id`",
		`user@host/path`, `user@host\path`, "user@host,other", "one@two@host",
		"-option@host", "user@-host", "user name@host", "user'quote@host",
		".host", "host-", "host..internal", "999.999.999.999",
		"2001:db8::1", "user@[not-ipv6]", "user@[2001:db8::1]suffix",
	}
	for _, value := range rejected {
		if _, _, err := ParseTarget(value); err == nil {
			t.Errorf("unsafe SSH target %q was accepted", value)
		}
	}

	rejectedJumps := []string{
		"jump,,other", "jump, other", "jump ,other",
		`user@-oProxyCommand=id 127.0.0.1`,
		`safe,user@-oProxyCommand=id 127.0.0.1`,
	}
	for _, value := range rejectedJumps {
		if _, err := ParseJump(value); err == nil {
			t.Errorf("unsafe SSH jump %q was accepted", value)
		}
	}
}

func TestPlanNeverBuildsArgvForProxyJumpInjection(t *testing.T) {
	config := Config{
		Host:     "deploy@app01",
		Jump:     []string{`user@-oProxyCommand=id 127.0.0.1`},
		Forwards: []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1080}},
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	lookupCalled := false
	driver := NewWithLookup(func(string) (string, error) {
		lookupCalled = true
		return "ssh", nil
	})
	_, err = driver.Plan(context.Background(), core.Connection{
		ConfigVersion: core.ConfigVersion,
		Kind:          core.Kind,
		Metadata:      core.Metadata{Name: "injection"},
		Spec:          core.ConnectionSpec{Driver: ID, Config: raw},
	}, core.PlanOptions{})
	if err == nil {
		t.Fatal("ProxyJump injection reached argv planning")
	}
	if lookupCalled {
		t.Fatal("ProxyJump injection reached executable resolution")
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
	driver := NewWithLookup(func(string) (string, error) { return "ssh", nil })
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
	value, err := NewWithLookup(func(string) (string, error) { return "ssh", nil }).Plan(context.Background(), connection, core.PlanOptions{NonInteractive: true})
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
	value, err := NewWithLookup(func(string) (string, error) { return "ssh", nil }).Plan(context.Background(), connection, core.PlanOptions{})
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
