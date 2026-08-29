package ssh

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseAddArgumentsMatchesSafeOpenSSHSyntax(t *testing.T) {
	got, err := ParseAddArguments([]string{
		"-N", "-T", "-p", "2222", "-i", "~/.ssh/work_ed25519",
		"-J", "jump@bastion:2200",
		"-L", "15432:localhost:5432",
		"-R", "19000:localhost:3000",
		"-D", "11080",
		"-o", "Compression=yes",
		"deploy@app01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "deploy@app01" || got.Port != 2222 || got.IdentityFile != "~/.ssh/work_ed25519" {
		t.Fatalf("unexpected target config: %#v", got)
	}
	if !reflect.DeepEqual(got.Jump, []string{"jump@bastion:2200"}) {
		t.Fatalf("unexpected jump: %#v", got.Jump)
	}
	wantTypes := []string{"local", "remote", "dynamic"}
	for index, want := range wantTypes {
		if got.Forwards[index].Type != want {
			t.Fatalf("forward order changed: %#v", got.Forwards)
		}
	}
	if !reflect.DeepEqual(got.SSHOptions, []string{"Compression=yes"}) {
		t.Fatalf("unexpected SSH options: %#v", got.SSHOptions)
	}
}

func TestParseAddArgumentsAcceptsNativeAttachedShortValues(t *testing.T) {
	got, err := ParseAddArguments([]string{"-p2222", "-D1080", "deploy@app01"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != 2222 || len(got.Forwards) != 1 || got.Forwards[0].SrcPort != 1080 {
		t.Fatalf("unexpected config: %#v", got)
	}
}

func TestParseAddArgumentsCanonicalizesBracketedIPv6Target(t *testing.T) {
	withoutPort, err := ParseAddArguments([]string{"-D", "1080", "ops@[2001:db8::10]"})
	if err != nil {
		t.Fatal(err)
	}
	if withoutPort.Host != "ops@2001:db8::10" || withoutPort.Port != 22 {
		t.Fatalf("unexpected IPv6 config without port: %#v", withoutPort)
	}

	withPort, err := ParseAddArguments([]string{"-D", "1081", "ops@[2001:db8::10]:2200"})
	if err != nil {
		t.Fatal(err)
	}
	if withPort.Host != "ops@2001:db8::10" || withPort.Port != 2200 {
		t.Fatalf("unexpected IPv6 config with port: %#v", withPort)
	}

	if _, err := ParseAddArguments([]string{"-p", "2222", "-D", "1082", "ops@[2001:db8::10]:2200"}); err == nil {
		t.Fatal("IPv6 target port conflicted silently with -p")
	}
}

func TestParseSetArgumentsProducesIdempotentListReplacements(t *testing.T) {
	patch, err := ParseSetArguments([]string{
		"-L", "15432:localhost:5432", "-D", "11080",
		"-o", "Compression=yes", "deploy@app02",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !patch.HostSet || patch.Host != "deploy@app02" || !patch.ForwardsSet || !patch.SSHOptionsSet {
		t.Fatalf("unexpected patch: %#v", patch)
	}
	if len(patch.Forwards) != 2 || len(patch.SSHOptions) != 1 {
		t.Fatalf("lists were not captured as complete replacements: %#v", patch)
	}
	before := Config{
		Host: "old", Port: 22,
		Forwards:   []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 9999}},
		SSHOptions: []string{"LogLevel=ERROR"},
	}
	after := ApplyPatch(before, patch)
	if len(after.Forwards) != 2 || !reflect.DeepEqual(after.SSHOptions, []string{"Compression=yes"}) {
		t.Fatalf("patch appended instead of replacing: %#v", after)
	}
}

func TestParseArgumentsRejectsUnsafeOrAmbiguousSurface(t *testing.T) {
	tests := [][]string{
		{"--to", "deploy@app01", "-D", "1080"},
		{"--ssh", "deploy@app01", "-D", "1080"},
		{"-F", "~/.ssh/config", "-D", "1080", "deploy@app01"},
		{"-f", "-D", "1080", "deploy@app01"},
		{"-M", "-D", "1080", "deploy@app01"},
		{"-S", "/tmp/socket", "-D", "1080", "deploy@app01"},
		{"-t", "-D", "1080", "deploy@app01"},
		{"-W", "host:80", "deploy@app01"},
		{"-J", "", "-D", "1080", "deploy@app01"},
		{"-J", "user@-oProxyCommand=id 127.0.0.1", "-D", "1080", "deploy@app01"},
		{"-D", "1080", "deploy@app01", "uname"},
		{"-D", "1080", "user@host name"},
		{"deploy@app01", "-D", "1080"},
		{"-D", "1080", "deploy@app01:2222"},
		{"-D", "1080", "one@host", "two@host"},
	}
	for _, args := range tests {
		if _, err := ParseAddArguments(args); err == nil {
			t.Errorf("unsafe or ambiguous argv was accepted: %q", strings.Join(args, " "))
		}
	}
}

func TestParseSetArgumentsAllowsOptionsWithoutRepeatingTarget(t *testing.T) {
	patch, err := ParseSetArguments([]string{"-p", "2222", "-D", "1080"})
	if err != nil {
		t.Fatal(err)
	}
	if patch.HostSet || !patch.PortSet || !patch.ForwardsSet {
		t.Fatalf("unexpected patch: %#v", patch)
	}
}

func TestParseSetArgumentsClearsOptionalSSHFields(t *testing.T) {
	patch, err := ParseSetArguments([]string{
		"--clear", "identity", "--clear=jump", "--clear", "options",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !patch.IdentitySet || !patch.JumpSet || !patch.SSHOptionsSet {
		t.Fatalf("clear fields were not represented as an explicit patch: %#v", patch)
	}
	before := Config{
		Host: "deploy@app01", Port: 22, IdentityFile: "~/.ssh/work",
		Jump:       []string{"jump@bastion"},
		Forwards:   []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1080}},
		SSHOptions: []string{"Compression=yes"},
	}
	after := ApplyPatch(before, patch)
	if after.IdentityFile != "" || len(after.Jump) != 0 || len(after.SSHOptions) != 0 {
		t.Fatalf("optional SSH fields were not cleared: %#v", after)
	}
	if after.Host != before.Host || !reflect.DeepEqual(after.Forwards, before.Forwards) {
		t.Fatalf("clear changed unrelated SSH fields: %#v", after)
	}
}

func TestParseSetArgumentsRejectsInvalidOrConflictingClears(t *testing.T) {
	tests := [][]string{
		{"--clear"},
		{"--clear", ""},
		{"--clear", "host"},
		{"--clear", "identity", "--clear", "identity"},
		{"--clear", "identity", "-i", "key"},
		{"-i", "key", "--clear", "identity"},
		{"--clear", "jump", "-J", "jump@host"},
		{"-J", "jump@host", "--clear", "jump"},
		{"--clear", "options", "-o", "Compression=yes"},
		{"-o", "Compression=yes", "--clear", "options"},
	}
	for _, args := range tests {
		if _, err := ParseSetArguments(args); err == nil {
			t.Errorf("invalid clear arguments were accepted: %q", strings.Join(args, " "))
		}
	}
	if _, err := ParseAddArguments([]string{"--clear", "identity", "-D", "1080", "deploy@app01"}); err == nil {
		t.Fatal("add accepted set-only --clear")
	}
}
