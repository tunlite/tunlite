package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

type language string

const (
	languageEnglish language = "en"
	languageChinese language = "zh-CN"
)

func resolveLanguage(explicit string) (language, error) {
	if explicit != "" {
		return parseLanguage(explicit)
	}
	if value := os.Getenv("TUNLITE_LANG"); value != "" {
		return parseLanguage(value)
	}
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(key); value != "" {
			if lang, ok := knownLanguage(value); ok {
				return lang, nil
			}
		}
	}
	return languageEnglish, nil
}

func parseLanguage(value string) (language, error) {
	if lang, ok := knownLanguage(value); ok {
		return lang, nil
	}
	return "", fmt.Errorf("unsupported language %q (supported: en, zh-CN)", value)
}

func knownLanguage(value string) (language, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	if index := strings.IndexByte(normalized, '.'); index >= 0 {
		normalized = normalized[:index]
	}
	if index := strings.IndexByte(normalized, '@'); index >= 0 {
		normalized = normalized[:index]
	}
	switch {
	case normalized == "en" || strings.HasPrefix(normalized, "en-"):
		return languageEnglish, true
	case normalized == "zh" || strings.HasPrefix(normalized, "zh-"):
		return languageChinese, true
	default:
		return "", false
	}
}

func isChinese(lang language) bool { return lang == languageChinese }

func localized(lang language, english, chinese string) string {
	if isChinese(lang) {
		return chinese
	}
	return english
}

var unknownCommandPattern = regexp.MustCompile(`^unknown command ("[^"]+")(?:; did you mean ("[^"]+")\?)?$`)

func localizeHumanError(kind, message string, lang language) string {
	if !isChinese(lang) {
		return message
	}
	if matches := unknownCommandPattern.FindStringSubmatch(message); matches != nil {
		result := "未知命令 " + matches[1]
		if matches[2] != "" {
			result += "；你是否想输入 " + matches[2] + "？"
		}
		return result
	}
	for _, replacement := range []struct{ from, to string }{
		{"usage: ", "用法："},
		{"unexpected arguments: ", "未识别的参数："},
		{"no such connection: ", "找不到连接："},
		{"unsupported language ", "不支持的语言 "},
		{"monitor requires an interactive terminal; use status --json in scripts", "monitor 需要交互式终端；脚本中请使用 status --json"},
		{"monitor is an interactive terminal view; use status --json for machine-readable state", "monitor 是交互式终端界面；机器读取状态请使用 status --json"},
		{"monitor does not support --dry-run; preview lifecycle changes with start, stop, or restart --dry-run", "monitor 不支持 --dry-run；请用 start、stop 或 restart --dry-run 预演变更"},
	} {
		if strings.HasPrefix(message, replacement.from) {
			return replacement.to + strings.TrimPrefix(message, replacement.from)
		}
	}
	prefix := map[string]string{
		"invalid_arguments": "参数错误",
		"not_found":         "未找到",
		"already_exists":    "已存在",
		"state_conflict":    "当前状态不允许此操作",
		"capability_error":  "当前系统不支持此操作",
		"internal_error":    "操作失败",
	}[kind]
	if prefix == "" {
		prefix = "操作失败"
	}
	return prefix + "：" + message
}
