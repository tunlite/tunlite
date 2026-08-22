package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
	"github.com/yuanyuanzijin/tunlite/internal/service"
	"golang.org/x/term"
)

const (
	ansiReset   = "\x1b[0m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiRed     = "\x1b[31m"
	ansiDim     = "\x1b[2m"
	ansiReverse = "\x1b[7m"
)

type stateStyle struct {
	glyph string
	color string
	label string
}

type tableColumn struct {
	header string
	value  func(statusRow) string
	max    int
}

type statusRow struct {
	connection core.Connection
	state      service.State
	transition string
}

func outputUsesColor(streams IO, opts options) bool {
	if opts.JSON || os.Getenv("NO_COLOR") != "" {
		return false
	}
	if streams.Color != nil {
		return *streams.Color
	}
	return writerIsTerminal(streams.Out)
}

func streamsAreInteractive(streams IO) bool {
	if streams.Terminal != nil {
		return *streams.Terminal
	}
	return readerIsTerminal(streams.In) && writerIsTerminal(streams.Out)
}

func writerIsTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func readerIsTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func colorize(value, color string, enabled bool) string {
	if !enabled || color == "" {
		return value
	}
	return color + value + ansiReset
}

func displayState(value service.State, lang language) stateStyle {
	switch {
	case value.ConsistencyState == service.ConsistencyBlocked:
		return stateStyle{glyph: "⊘", color: ansiRed, label: localized(lang, "needs attention", "需要处理")}
	case value.ConsistencyState == service.ConsistencyDrifted:
		return stateStyle{glyph: "!", color: ansiYellow, label: localized(lang, "needs attention", "需要处理")}
	case value.ConsistencyState == service.ConsistencyMissing:
		return stateStyle{glyph: "✕", color: ansiRed, label: localized(lang, "needs attention", "需要处理")}
	case value.ProcessState == service.ProcessFailed:
		return stateStyle{glyph: "✕", color: ansiRed, label: localized(lang, "unhealthy", "运行异常")}
	case value.Health == service.HealthUnhealthy:
		return stateStyle{glyph: "✕", color: ansiRed, label: localized(lang, "unhealthy", "运行异常")}
	case value.ProcessState == service.ProcessStarting:
		return stateStyle{glyph: "◌", color: ansiYellow, label: localized(lang, "starting", "启动中")}
	case value.ProcessState == service.ProcessRunning && value.Health == service.HealthHealthy:
		return stateStyle{glyph: "●", color: ansiGreen, label: localized(lang, "running", "正常运行")}
	case value.ProcessState == service.ProcessRunning:
		return stateStyle{glyph: "●", color: ansiGreen, label: localized(lang, "running", "正常运行")}
	case value.Mode == service.ModeForeground:
		return stateStyle{glyph: "●", color: ansiGreen, label: localized(lang, "running", "正常运行")}
	default:
		return stateStyle{glyph: "○", color: ansiDim, label: localized(lang, "stopped", "已停止")}
	}
}

func displayStatusRow(row statusRow, lang language) stateStyle {
	switch row.transition {
	case "starting":
		return stateStyle{glyph: "◌", color: ansiYellow, label: localized(lang, "starting", "启动中")}
	case "stopping":
		return stateStyle{glyph: "◌", color: ansiYellow, label: localized(lang, "stopping", "停止中")}
	case "restarting":
		return stateStyle{glyph: "◌", color: ansiYellow, label: localized(lang, "restarting", "重启中")}
	default:
		return displayState(row.state, lang)
	}
}

func renderStatusTable(rows []statusRow, color bool, lang language) []string {
	if len(rows) == 0 {
		return renderStatusTableSelected(rows, color, -1, lang)
	}
	lines := make([]string, 0, len(rows)*2)
	for start := 0; start < len(rows); {
		end := start + 1
		group := rows[start].connection.Metadata.Group
		for end < len(rows) && rows[end].connection.Metadata.Group == group {
			end++
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, colorize(groupHeading(group, end-start, lang), ansiReverse, color))
		lines = append(lines, renderStatusTableSelected(rows[start:end], color, -1, lang)...)
		start = end
	}
	return lines
}

func renderStatusTableSelected(rows []statusRow, color bool, selected int, lang language) []string {
	columns := []tableColumn{
		{header: localized(lang, "NAME", "名称"), max: 24, value: func(row statusRow) string { return row.connection.Metadata.Name }},
		{header: localized(lang, "STATE", "状态"), max: 16, value: func(row statusRow) string {
			style := displayStatusRow(row, lang)
			return style.glyph + " " + style.label
		}},
		{header: localized(lang, "RUNS AS", "运行位置"), max: 14, value: func(row statusRow) string { return statusRunsAs(row, lang) }},
		{header: localized(lang, "ON LOGIN", "登录自启"), max: 10, value: func(row statusRow) string { return statusOnLogin(row, lang) }},
		{header: "PID", max: 10, value: statusPID},
		{header: localized(lang, "UP", "已运行"), max: 10, value: statusUptime},
		{header: localized(lang, "RESTARTS", "重启次数"), max: 8, value: statusRestarts},
		{header: localized(lang, "HOST", "SSH 目标"), max: 36, value: statusHost},
		{header: localized(lang, "ROUTE", "转发"), max: 42, value: statusRoutes},
	}
	widths := make([]int, len(columns))
	for index, column := range columns {
		widths[index] = visibleWidth(column.header)
		for _, row := range rows {
			width := visibleWidth(column.value(row))
			if column.max > 0 && width > column.max {
				width = column.max
			}
			if width > widths[index] {
				widths[index] = width
			}
		}
	}
	lines := []string{colorize(joinTableCells(columns, widths, func(column tableColumn) string { return column.header }), ansiDim, color)}
	for rowIndex, row := range rows {
		cells := make([]string, len(columns))
		for index, column := range columns {
			cells[index] = fitCell(column.value(row), widths[index])
			if index == 1 {
				cells[index] = colorize(cells[index], displayStatusRow(row, lang).color, color)
			}
		}
		line := strings.Join(cells, "  ")
		if rowIndex == selected {
			line = colorize(line, ansiReverse, color)
		}
		lines = append(lines, line)
		if selected >= 0 {
			continue
		}
		for _, condition := range row.state.Conditions {
			if condition.Severity == "error" || condition.Severity == "warning" {
				lines = append(lines, colorize("  ↳ "+condition.Message, ansiDim, color))
				break
			}
		}
	}
	return lines
}

func joinTableCells(columns []tableColumn, widths []int, value func(tableColumn) string) string {
	cells := make([]string, len(columns))
	for index, column := range columns {
		cells[index] = fitCell(value(column), widths[index])
	}
	return strings.Join(cells, "  ")
}

func fitCell(value string, width int) string {
	if visibleWidth(value) > width {
		if width <= 1 {
			return "…"
		}
		limit := width - 1
		used := 0
		var fitted strings.Builder
		for _, current := range value {
			currentWidth := 1
			if runeIsWide(current) {
				currentWidth = 2
			}
			if used+currentWidth > limit {
				break
			}
			fitted.WriteRune(current)
			used += currentWidth
		}
		value = fitted.String() + "…"
	}
	return value + strings.Repeat(" ", width-visibleWidth(value))
}

func visibleWidth(value string) int {
	width := 0
	for _, current := range value {
		if runeIsWide(current) {
			width += 2
		} else {
			width++
		}
	}
	return width
}

func runeIsWide(value rune) bool {
	return value >= 0x1100 && (value <= 0x115f || value == 0x2329 || value == 0x232a ||
		(value >= 0x2e80 && value <= 0xa4cf) || (value >= 0xac00 && value <= 0xd7a3) ||
		(value >= 0xf900 && value <= 0xfaff) || (value >= 0xfe10 && value <= 0xfe19) ||
		(value >= 0xfe30 && value <= 0xfe6f) || (value >= 0xff00 && value <= 0xff60) ||
		(value >= 0xffe0 && value <= 0xffe6) || (value >= 0x1f300 && value <= 0x1faff) ||
		(value >= 0x20000 && value <= 0x3fffd))
}

func statusHost(row statusRow) string {
	config, err := sshdriver.DecodeConfig(row.connection.Spec.Config)
	if err != nil {
		return row.connection.Spec.Driver
	}
	if config.Port != 0 && config.Port != 22 {
		return config.Host + ":" + strconv.Itoa(config.Port)
	}
	return config.Host
}

func statusRoutes(row statusRow) string {
	config, err := sshdriver.DecodeConfig(row.connection.Spec.Config)
	if err != nil {
		return "—"
	}
	values := make([]string, 0, len(config.Forwards))
	for _, forward := range config.Forwards {
		switch forward.Type {
		case "remote":
			values = append(values, fmt.Sprintf("%s:%d ← %s:%d", forward.DestHost, forward.DestPort, defaultBind(forward.Bind), forward.SrcPort))
		case "dynamic":
			values = append(values, fmt.Sprintf("%s:%d SOCKS", defaultBind(forward.Bind), forward.SrcPort))
		default:
			values = append(values, fmt.Sprintf("%s:%d → %s:%d", defaultBind(forward.Bind), forward.SrcPort, forward.DestHost, forward.DestPort))
		}
	}
	return strings.Join(values, ", ")
}

func statusRunsAs(row statusRow, lang language) string {
	switch row.state.Mode {
	case service.ModeForeground:
		return localized(lang, "terminal", "当前终端")
	case service.ModeBackground, service.ModePersistent:
		return localized(lang, "background", "系统后台")
	default:
		return "—"
	}
}

func statusOnLogin(row statusRow, lang language) string {
	if row.state.Mode == service.ModePersistent {
		return localized(lang, "yes", "是")
	}
	return localized(lang, "no", "否")
}

func statusPID(row statusRow) string {
	if row.state.Platform == nil {
		return "—"
	}
	for _, key := range []string{"pid", "mainPID"} {
		if value, exists := row.state.Platform[key]; exists && fmt.Sprint(value) != "" && fmt.Sprint(value) != "0" {
			return fmt.Sprint(value)
		}
	}
	return "—"
}

func statusUptime(row statusRow) string {
	if row.state.ProcessState != service.ProcessRunning || row.state.Platform == nil {
		return "—"
	}
	raw := fmt.Sprint(row.state.Platform["activeEnterTimestamp"])
	if raw == "" || raw == "<nil>" {
		return "—"
	}
	for _, layout := range []string{time.RFC3339, "Mon 2006-01-02 15:04:05 MST"} {
		started, err := time.Parse(layout, raw)
		if err == nil {
			return formatDuration(time.Since(started))
		}
	}
	return "—"
}

func statusRestarts(row statusRow) string {
	if row.state.Platform == nil {
		return "0"
	}
	if value := fmt.Sprint(row.state.Platform["restarts"]); value != "" && value != "<nil>" {
		return value
	}
	if value := fmt.Sprint(row.state.Platform["runs"]); value != "" && value != "<nil>" {
		count, err := strconv.Atoi(value)
		if err == nil && count > 0 {
			return strconv.Itoa(count - 1)
		}
	}
	return "0"
}

func formatDuration(value time.Duration) string {
	if value < 0 {
		return "—"
	}
	if value >= 24*time.Hour {
		return fmt.Sprintf("%dd%dh", int(value/(24*time.Hour)), int(value%(24*time.Hour)/time.Hour))
	}
	if value >= time.Hour {
		return fmt.Sprintf("%dh%dm", int(value/time.Hour), int(value%time.Hour/time.Minute))
	}
	if value >= time.Minute {
		return fmt.Sprintf("%dm%ds", int(value/time.Minute), int(value%time.Minute/time.Second))
	}
	return fmt.Sprintf("%ds", int(value/time.Second))
}

func renderStatusDetail(row statusRow, color bool, lang language) []string {
	style := displayStatusRow(row, lang)
	lines := []string{row.connection.Metadata.Name}
	appendRow := func(label, value, shade string) {
		lines = append(lines, "  "+fitCell(label, 12)+" "+colorize(value, shade, color))
	}
	appendRow(localized(lang, "state", "状态"), style.glyph+" "+style.label, style.color)
	appendRow(localized(lang, "runs as", "运行位置"), statusRunsAs(row, lang), "")
	appendRow(localized(lang, "on login", "登录自启"), statusOnLogin(row, lang), "")
	appendRow("PID", statusPID(row), "")
	appendRow(localized(lang, "up", "已运行"), statusUptime(row), "")
	appendRow(localized(lang, "restarts", "重启次数"), statusRestarts(row), "")
	appendRow(localized(lang, "SSH host", "SSH 目标"), statusHost(row), "")
	if config, err := sshdriver.DecodeConfig(row.connection.Spec.Config); err == nil {
		if len(config.Jump) > 0 {
			appendRow(localized(lang, "jump", "跳板"), strings.Join(config.Jump, ", "), "")
		}
		for _, forward := range config.Forwards {
			appendRow(localized(lang, "forward", "转发类型"), localizedForwardType(forward.Type, lang), "")
			switch forward.Type {
			case "remote":
				appendRow(localized(lang, "listen", "监听"), fmt.Sprintf(localized(lang, "%s:%d (on server)", "%s:%d（远程服务器）"), defaultBind(forward.Bind), forward.SrcPort), "")
				appendRow(localized(lang, "target", "目标"), fmt.Sprintf(localized(lang, "%s:%d (on this machine)", "%s:%d（本机）"), forward.DestHost, forward.DestPort), "")
			case "dynamic":
				appendRow(localized(lang, "listen", "监听"), fmt.Sprintf(localized(lang, "%s:%d (local SOCKS)", "%s:%d（本机 SOCKS）"), defaultBind(forward.Bind), forward.SrcPort), "")
			default:
				appendRow(localized(lang, "listen", "监听"), fmt.Sprintf(localized(lang, "%s:%d (local)", "%s:%d（本机）"), defaultBind(forward.Bind), forward.SrcPort), "")
				appendRow(localized(lang, "target", "目标"), fmt.Sprintf(localized(lang, "%s:%d (reachable from server)", "%s:%d（由服务器访问）"), forward.DestHost, forward.DestPort), "")
			}
		}
		if config.IdentityFile != "" {
			appendRow(localized(lang, "identity", "密钥"), config.IdentityFile, "")
		}
		if len(config.SSHOptions) > 0 {
			appendRow(localized(lang, "options", "SSH 选项"), strings.Join(config.SSHOptions, " "), "")
		}
	}
	appendRow(localized(lang, "group", "分组"), groupLabel(row.connection.Metadata.Group, lang), "")
	for _, condition := range row.state.Conditions {
		shade := ansiYellow
		if condition.Severity == "error" {
			shade = ansiRed
		}
		appendRow(condition.Severity, condition.Message, shade)
	}
	return lines
}

func localizedForwardType(value string, lang language) string {
	if !isChinese(lang) {
		return value
	}
	switch value {
	case "local":
		return "本地转发"
	case "remote":
		return "远程转发"
	case "dynamic":
		return "动态转发"
	default:
		return value
	}
}

func doctorStyle(severity string) (string, string) {
	switch severity {
	case "error":
		return "✕", ansiRed
	case "warning":
		return "!", ansiYellow
	default:
		return "✓", ansiGreen
	}
}

func sortedStatusRows(results []serviceItemResult) []statusRow {
	rows := make([]statusRow, 0, len(results))
	for _, result := range results {
		if result.State == nil || result.Declaration == nil {
			continue
		}
		rows = append(rows, statusRow{connection: *result.Declaration, state: *result.State})
	}
	sort.Slice(rows, func(i, j int) bool { return connectionLess(rows[i].connection, rows[j].connection) })
	return rows
}
