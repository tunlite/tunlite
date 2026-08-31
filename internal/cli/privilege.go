package cli

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/tunlite/tunlite/internal/service"
)

type privilegeAuthorizer interface {
	Validate(context.Context, IO, language, privilegeRequest) error
}

type privilegeRequest struct {
	connections      int
	userToSystem     int
	systemToUser     int
	installsToSystem int
}

type privilegeCommandRunner interface {
	Run(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error
}

type execPrivilegeCommandRunner struct{}

func (execPrivilegeCommandRunner) Run(
	ctx context.Context,
	program string,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	command := exec.CommandContext(ctx, program, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

type sudoPrivilegeAuthorizer struct {
	runner privilegeCommandRunner
}

func newSudoPrivilegeAuthorizer() privilegeAuthorizer {
	return sudoPrivilegeAuthorizer{runner: execPrivilegeCommandRunner{}}
}

func (authorizer sudoPrivilegeAuthorizer) Validate(ctx context.Context, streams IO, lang language, request privilegeRequest) error {
	if !privilegePromptAvailable(streams) {
		return &privilegeRequiredError{message: localized(lang,
			"Administrator authorization is required, but no interactive terminal is available. Run this command in an interactive terminal, or pre-authorize sudo and retry with --non-interactive.",
			"此操作需要管理员授权，但当前没有可交互终端。请在交互式终端中运行，或预先完成 sudo 授权后使用 --non-interactive 重试。",
		)}
	}
	fmt.Fprintln(streams.Err, privilegeRequestMessage(lang, request))
	if err := authorizer.runner.Run(ctx, service.MacOSSudoProgram, []string{"-v"}, streams.In, io.Discard, streams.Err); err != nil {
		return &privilegeRequiredError{
			message: localized(lang,
				"Administrator authorization was cancelled or failed; no service scope changes were started.",
				"管理员授权已取消或失败；尚未开始任何服务范围变更。",
			),
			cause: err,
		}
	}
	return nil
}

func privilegeRequestMessage(lang language, request privilegeRequest) string {
	parts := make([]string, 0, 3)
	if request.userToSystem > 0 {
		parts = append(parts, fmt.Sprintf("%d USER→SYSTEM", request.userToSystem))
	}
	if request.systemToUser > 0 {
		parts = append(parts, fmt.Sprintf("%d SYSTEM→USER", request.systemToUser))
	}
	if request.installsToSystem > 0 {
		parts = append(parts, localized(lang,
			fmt.Sprintf("%d NEW→SYSTEM", request.installsToSystem),
			fmt.Sprintf("%d 个新服务→SYSTEM", request.installsToSystem),
		))
	}
	directions := strings.Join(parts, localized(lang, ", ", "、"))
	if directions == "" {
		directions = localized(lang, "system-scope change", "system 范围变更")
	}
	if isChinese(lang) {
		return fmt.Sprintf("本批有 %d 个连接需要管理员权限（%s），正在请求授权；sudo 可能会提示输入密码…", request.connections, directions)
	}
	return fmt.Sprintf("Administrator authorization is required for %d connection(s) (%s). Requesting it now; sudo may prompt for your password…", request.connections, directions)
}

func privilegePromptAvailable(streams IO) bool {
	if streams.Terminal != nil {
		return *streams.Terminal
	}
	return readerIsTerminal(streams.In) && writerIsTerminal(streams.Err)
}

type privilegeRequiredError struct {
	message string
	cause   error
}

func (err *privilegeRequiredError) Error() string { return err.message }
func (err *privilegeRequiredError) Unwrap() error { return err.cause }
func (err *privilegeRequiredError) Code() string  { return "privilege_required" }
