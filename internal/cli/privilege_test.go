package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/tunlite/tunlite/internal/service"
)

type fakePrivilegeCommandRunner struct {
	calls   int
	program string
	args    []string
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	err     error
}

func (runner *fakePrivilegeCommandRunner) Run(
	_ context.Context,
	program string,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	runner.calls++
	runner.program = program
	runner.args = append([]string(nil), args...)
	runner.stdin = stdin
	runner.stdout = stdout
	runner.stderr = stderr
	return runner.err
}

func TestSudoPrivilegeAuthorizerUsesExactInteractiveCommandAndStreams(t *testing.T) {
	runner := &fakePrivilegeCommandRunner{}
	authorizer := sudoPrivilegeAuthorizer{runner: runner}
	terminal := true
	stdin := strings.NewReader("not-read-by-tunlite")
	var stderr bytes.Buffer
	err := authorizer.Validate(context.Background(), IO{In: stdin, Out: io.Discard, Err: &stderr, Terminal: &terminal}, languageEnglish,
		privilegeRequest{connections: 2, userToSystem: 1, systemToUser: 1})
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 1 || runner.program != service.MacOSSudoProgram || !reflect.DeepEqual(runner.args, []string{"-v"}) {
		t.Fatalf("calls=%d command=%q args=%v", runner.calls, runner.program, runner.args)
	}
	if runner.stdin != stdin || runner.stderr != &stderr || runner.stdout != io.Discard {
		t.Fatalf("sudo streams were not inherited safely: stdin=%T stdout=%T stderr=%T", runner.stdin, runner.stdout, runner.stderr)
	}
	if !strings.Contains(stderr.String(), "Administrator authorization is required") || !strings.Contains(stderr.String(), "Requesting it now") {
		t.Fatalf("missing authorization progress: %q", stderr.String())
	}
	for _, expected := range []string{"2 connection(s)", "1 USER→SYSTEM", "1 SYSTEM→USER"} {
		if !strings.Contains(stderr.String(), expected) {
			t.Fatalf("authorization summary missing %q: %q", expected, stderr.String())
		}
	}
}

func TestSudoPrivilegeAuthorizerNoTTYFailsWithoutRunningCommand(t *testing.T) {
	runner := &fakePrivilegeCommandRunner{}
	authorizer := sudoPrivilegeAuthorizer{runner: runner}
	terminal := false
	err := authorizer.Validate(context.Background(), IO{In: strings.NewReader(""), Err: io.Discard, Terminal: &terminal}, languageEnglish, privilegeRequest{connections: 1})
	var privilege *privilegeRequiredError
	if !errors.As(err, &privilege) || privilege.Code() != "privilege_required" || runner.calls != 0 {
		t.Fatalf("err=%v runner calls=%d", err, runner.calls)
	}
	if !strings.Contains(err.Error(), "interactive terminal") || !strings.Contains(err.Error(), "--non-interactive") {
		t.Fatalf("unhelpful no-TTY error: %v", err)
	}
}

func TestSudoPrivilegeAuthorizerFailureIsTypedAndLocalized(t *testing.T) {
	runner := &fakePrivilegeCommandRunner{err: errors.New("exit status 1")}
	authorizer := sudoPrivilegeAuthorizer{runner: runner}
	terminal := true
	var stderr bytes.Buffer
	err := authorizer.Validate(context.Background(), IO{In: strings.NewReader(""), Err: &stderr, Terminal: &terminal}, languageChinese,
		privilegeRequest{connections: 2, userToSystem: 2})
	var privilege *privilegeRequiredError
	if !errors.As(err, &privilege) || privilege.Code() != "privilege_required" || runner.calls != 1 {
		t.Fatalf("err=%v runner calls=%d", err, runner.calls)
	}
	if !strings.Contains(stderr.String(), "本批有 2 个连接") || !strings.Contains(stderr.String(), "2 USER→SYSTEM") ||
		!strings.Contains(stderr.String(), "正在请求授权") || !strings.Contains(err.Error(), "尚未开始任何服务范围变更") {
		t.Fatalf("stderr=%q err=%v", stderr.String(), err)
	}
}
