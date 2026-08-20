package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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

func displayState(value service.State) stateStyle {
	switch {
	case value.ConsistencyState == service.ConsistencyBlocked:
		return stateStyle{glyph: "⊘", color: ansiRed, label: "blocked"}
	case value.ConsistencyState == service.ConsistencyDrifted:
		return stateStyle{glyph: "!", color: ansiYellow, label: "drifted"}
	case value.ConsistencyState == service.ConsistencyMissing:
		return stateStyle{glyph: "✕", color: ansiRed, label: "missing"}
	case value.ProcessState == service.ProcessFailed:
		return stateStyle{glyph: "✕", color: ansiRed, label: "failed"}
	case value.Health == service.HealthUnhealthy:
		return stateStyle{glyph: "✕", color: ansiRed, label: "unhealthy"}
	case value.ProcessState == service.ProcessStarting:
		return stateStyle{glyph: "◌", color: ansiYellow, label: "starting"}
	case value.ProcessState == service.ProcessRunning && value.Health == service.HealthHealthy:
		return stateStyle{glyph: "●", color: ansiGreen, label: "healthy"}
	case value.ProcessState == service.ProcessRunning:
		return stateStyle{glyph: "●", color: ansiGreen, label: "running"}
	case value.Mode == service.ModeForeground:
		return stateStyle{glyph: "●", color: ansiGreen, label: "foreground"}
	default:
		return stateStyle{glyph: "○", color: ansiDim, label: "stopped"}
	}
}

func renderStatusTable(rows []statusRow, color bool) []string {
	return renderStatusTableSelected(rows, color, -1)
}

func renderStatusTableSelected(rows []statusRow, color bool, selected int) []string {
	columns := []tableColumn{
		{header: "NAME", max: 24, value: func(row statusRow) string { return row.connection.Metadata.Name }},
		{header: "STATE", max: 16, value: func(row statusRow) string {
			style := displayState(row.state)
			return style.glyph + " " + style.label
		}},
		{header: "HOST", max: 36, value: statusHost},
		{header: "TYPE", max: 12, value: statusTypes},
		{header: "ROUTE", max: 42, value: statusRoutes},
		{header: "MODE", max: 12, value: func(row statusRow) string { return row.state.Mode }},
		{header: "PID", max: 10, value: statusPID},
		{header: "UP", max: 10, value: statusUptime},
		{header: "RESTARTS", max: 8, value: statusRestarts},
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
			if column.header == "STATE" {
				cells[index] = colorize(cells[index], displayState(row.state).color, color)
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
		runes := []rune(value)
		if width <= 1 {
			return "…"
		}
		value = string(runes[:width-1]) + "…"
	}
	return value + strings.Repeat(" ", width-visibleWidth(value))
}

func visibleWidth(value string) int { return utf8.RuneCountInString(value) }

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

func statusTypes(row statusRow) string {
	config, err := sshdriver.DecodeConfig(row.connection.Spec.Config)
	if err != nil {
		return "—"
	}
	seen := map[string]bool{}
	var values []string
	for _, forward := range config.Forwards {
		if !seen[forward.Type] {
			seen[forward.Type] = true
			values = append(values, forward.Type)
		}
	}
	return strings.Join(values, ",")
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

func renderStatusDetail(row statusRow, color bool) []string {
	style := displayState(row.state)
	lines := []string{row.connection.Metadata.Name}
	appendRow := func(label, value, shade string) {
		lines = append(lines, fmt.Sprintf("  %-12s %s", label, colorize(value, shade, color)))
	}
	appendRow("state", style.glyph+" "+style.label, style.color)
	appendRow("mode", row.state.Mode, "")
	appendRow("process", row.state.ProcessState, "")
	appendRow("health", row.state.Health, "")
	appendRow("consistency", row.state.ConsistencyState, "")
	appendRow("service", row.state.ServiceState, "")
	appendRow("host", statusHost(row), "")
	appendRow("pid", statusPID(row), "")
	appendRow("up", statusUptime(row), "")
	appendRow("restarts", statusRestarts(row), "")
	if config, err := sshdriver.DecodeConfig(row.connection.Spec.Config); err == nil {
		if len(config.Jump) > 0 {
			appendRow("jump", strings.Join(config.Jump, ", "), "")
		}
		for _, forward := range config.Forwards {
			appendRow("type", forward.Type, "")
			switch forward.Type {
			case "remote":
				appendRow("listen", fmt.Sprintf("%s:%d (on server)", defaultBind(forward.Bind), forward.SrcPort), "")
				appendRow("target", fmt.Sprintf("%s:%d (on this machine)", forward.DestHost, forward.DestPort), "")
			case "dynamic":
				appendRow("listen", fmt.Sprintf("%s:%d (local SOCKS)", defaultBind(forward.Bind), forward.SrcPort), "")
			default:
				appendRow("listen", fmt.Sprintf("%s:%d (local)", defaultBind(forward.Bind), forward.SrcPort), "")
				appendRow("target", fmt.Sprintf("%s:%d (reachable from server)", forward.DestHost, forward.DestPort), "")
			}
		}
		if config.IdentityFile != "" {
			appendRow("identity", config.IdentityFile, "")
		}
		if len(config.SSHOptions) > 0 {
			appendRow("options", strings.Join(config.SSHOptions, " "), "")
		}
	}
	if tags := row.connection.Metadata.Tags; len(tags) > 0 {
		appendRow("tags", strings.Join(tags, ", "), "")
	}
	for _, condition := range row.state.Conditions {
		shade := ansiYellow
		if condition.Severity == "error" {
			shade = ansiRed
		}
		appendRow(condition.Severity, condition.Message, shade)
	}
	return lines
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
	sort.Slice(rows, func(i, j int) bool { return rows[i].connection.Metadata.Name < rows[j].connection.Metadata.Name })
	return rows
}
