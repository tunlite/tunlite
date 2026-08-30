package cli

import (
	"fmt"
	"strings"
)

func renderCommandProgress(streams IO, opts options, args []string) {
	if opts.JSON || len(args) == 0 || !terminalProgressEnabled(streams) {
		return
	}
	command := args[0]
	if command == "monitor" && opts.DryRun {
		return
	}
	target := ""
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		target = args[1]
	}
	if target == "" && len(args) == 1 {
		requiresSelection := map[string]bool{
			"add": true, "set": true, "remove": true, "rename": true, "import": true,
			"check": true, "logs": true, "unmanage": true, "resolve": true,
			"start": true, "stop": true, "enable": true, "disable": true, "restart": true,
		}
		if requiresSelection[command] {
			return
		}
	}
	verbs := map[string][2]string{
		"run": {"Preparing", "正在准备"}, "add": {"Adding", "正在添加"},
		"set": {"Updating", "正在更新"}, "remove": {"Removing", "正在删除"},
		"rename": {"Renaming", "正在重命名"}, "import": {"Importing", "正在导入"},
		"check": {"Checking SSH for", "正在检查 SSH"}, "doctor": {"Running diagnostics for", "正在诊断"},
		"monitor": {"Opening monitor", "正在打开监控"}, "start": {"Starting", "正在启动"},
		"stop": {"Stopping", "正在停止"}, "enable": {"Enabling", "正在开启并设置自动启动"},
		"disable": {"Disabling", "正在停止并关闭自动启动"}, "restart": {"Restarting", "正在重启"},
		"status": {"Checking status for", "正在检查状态"}, "logs": {"Reading logs for", "正在读取日志"},
		"manage": {"Managing", "正在接管"}, "unmanage": {"Unmanaging", "正在取消接管"},
		"resolve": {"Resolving", "正在处理"},
	}
	choices, ok := verbs[command]
	if !ok {
		return
	}
	verb := choices[0]
	if isChinese(opts.Language) {
		verb = choices[1]
	}
	if opts.DryRun {
		if isChinese(opts.Language) {
			verb = "正在预演：" + strings.TrimPrefix(verb, "正在")
		} else {
			verb = "Planning " + strings.ToLower(verb)
		}
	}
	if target == "" {
		verb = strings.TrimSuffix(verb, " for")
		fmt.Fprintf(streams.Err, "%s…\n", verb)
		return
	}
	fmt.Fprintf(streams.Err, "%s %s…\n", verb, target)
}

func terminalProgressEnabled(streams IO) bool {
	if streams.Terminal != nil {
		return *streams.Terminal
	}
	return writerIsTerminal(streams.Err)
}

func renderBatchProgress(streams IO, opts options, current, total int, english, chinese string) {
	if opts.JSON || !terminalProgressEnabled(streams) || total < 1 || current < 1 || current > total {
		return
	}
	message := localized(opts.Language, english, chinese)
	fmt.Fprintf(streams.Err, "[%d/%d] %s\n", current, total, message)
}

func renderStageProgress(streams IO, opts options, english, chinese string) {
	if opts.JSON || !terminalProgressEnabled(streams) {
		return
	}
	fmt.Fprintln(streams.Err, localized(opts.Language, english, chinese))
}
