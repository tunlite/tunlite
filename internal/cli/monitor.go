package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/core"
	"github.com/yuanyuanzijin/tunlite/internal/service"
	"golang.org/x/term"
)

type monitorKey int

const (
	monitorRefresh monitorKey = iota
	monitorUp
	monitorDown
	monitorEnterDetail
	monitorBack
	monitorEscape
	monitorStart
	monitorStop
	monitorRestart
	monitorConfirm
	monitorCancel
	monitorQuit
)

const monitorEscapeDelay = 35 * time.Millisecond

type monitorConfirmation struct {
	key        monitorKey
	name       string
	persistent bool
}

type monitorTransition struct {
	name  string
	label string
}

type monitorMutationResult struct {
	name    string
	message string
}

func monitor(ctx context.Context, args []string, streams IO, opts options) error {
	flags := flag.NewFlagSet("monitor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	intervalText := "1"
	var group string
	var legacyTags stringsFlag
	flags.StringVar(&intervalText, "interval", "1", "refresh interval in seconds")
	flags.StringVar(&group, "group", "", "filter by group")
	flags.Var(&legacyTags, "tag", "deprecated compatibility alias for --group")
	if err := flags.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError(fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}
	if opts.JSON {
		return usageError("monitor is an interactive terminal view; use status --json for machine-readable state")
	}
	if opts.DryRun {
		return usageError("monitor does not support --dry-run; preview lifecycle changes with start, stop, or restart --dry-run")
	}
	if !streamsAreInteractive(streams) {
		return usageError("monitor requires an interactive terminal; use status --json in scripts")
	}
	seconds, err := strconv.ParseFloat(intervalText, 64)
	if err != nil || seconds < 0.25 || seconds > 60 {
		return usageError("monitor interval must be between 0.25 and 60 seconds")
	}
	groupSet := false
	legacyTagSet := false
	flags.Visit(func(item *flag.Flag) {
		groupSet = groupSet || item.Name == "group"
		legacyTagSet = legacyTagSet || item.Name == "tag"
	})
	if groupSet && legacyTagSet {
		return usageError("use --group, not --group and legacy --tag together")
	}
	if groupSet {
		group, err = parseGroup(group)
		if err != nil {
			return err
		}
	} else if legacyTagSet {
		group, err = parseLegacyGroup(legacyTags)
		if err != nil {
			return err
		}
	}
	interval := time.Duration(seconds * float64(time.Second))
	color := outputUsesColor(streams, opts)
	monitorCtx, cancelMonitor := context.WithCancel(ctx)
	defer cancelMonitor()

	restore, err := makeMonitorRaw(streams.In)
	if err != nil {
		return err
	}
	defer restore()
	fmt.Fprint(streams.Out, "\x1b[?1049h\x1b[?25l\x1b[2J\x1b[H")
	defer fmt.Fprint(streams.Out, "\x1b[?25h\x1b[0m\x1b[?1049l")

	keys := make(chan monitorKey, 8)
	go readMonitorKeys(streams.In, keys)
	mutations := make(chan monitorMutationResult, 1)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	selected := 0
	detail := false
	flash := ""
	var confirmation *monitorConfirmation
	var transition *monitorTransition
	var rows []statusRow
	var logs []string
	render := func(loading bool) {
		visible := append([]statusRow(nil), rows...)
		if transition != nil {
			for index := range visible {
				if visible[index].connection.Metadata.Name == transition.name {
					visible[index].transition = transition.label
				}
			}
		}
		renderMonitor(streams.Out, visible, selected, detail, logs, color, group, interval, flash, confirmation, transition, loading, opts.Language)
	}
	refresh := func() error {
		fresh, refreshErr := collectMonitorRows(monitorCtx, group, opts)
		if refreshErr != nil {
			return refreshErr
		}
		rows = fresh
		if len(rows) == 0 {
			selected = 0
			detail = false
		} else if selected >= len(rows) {
			selected = len(rows) - 1
		}
		logs = nil
		if detail && len(rows) > 0 {
			var logsErr error
			logs, logsErr = collectMonitorLogs(monitorCtx, rows[selected], opts)
			if logsErr != nil {
				flash = "logs: " + logsErr.Error()
			}
		}
		render(false)
		flash = ""
		return nil
	}
	render(true)
	if err := refresh(); err != nil {
		return err
	}
	beginMutation := func(key monitorKey) {
		if transition != nil || len(rows) == 0 {
			return
		}
		row := rows[selected]
		command, label, planErr := monitorMutationPlan(key, row)
		if planErr != nil {
			flash = planErr.Error()
			render(false)
			flash = ""
			return
		}
		transition = &monitorTransition{name: row.connection.Metadata.Name, label: label}
		render(false)
		go func() {
			message := executeMonitorMutation(monitorCtx, command, row, opts)
			mutations <- monitorMutationResult{name: row.connection.Metadata.Name, message: message}
		}()
	}
	for {
		select {
		case <-monitorCtx.Done():
			return nil
		case <-ticker.C:
			if err := refresh(); err != nil {
				flash = err.Error()
				render(false)
				flash = ""
			}
		case result := <-mutations:
			if transition != nil && transition.name == result.name {
				transition = nil
			}
			flash = result.message
			if err := refresh(); err != nil {
				flash = err.Error()
				render(false)
				flash = ""
			}
		case key := <-keys:
			if confirmation != nil {
				pending := confirmation
				confirmation = nil
				if key == monitorConfirm {
					beginMutation(pending.key)
				} else {
					flash = localized(opts.Language, "operation cancelled", "操作已取消")
					render(false)
					flash = ""
				}
				continue
			}
			switch key {
			case monitorQuit:
				return nil
			case monitorEscape:
				if detail {
					detail = false
					logs = nil
				} else {
					return nil
				}
			case monitorBack:
				if detail {
					detail = false
					logs = nil
				}
			case monitorUp:
				if !detail && selected > 0 {
					selected--
				}
			case monitorDown:
				if !detail && selected+1 < len(rows) {
					selected++
				}
			case monitorEnterDetail:
				if !detail && len(rows) > 0 {
					detail = true
				}
			case monitorStart:
				beginMutation(key)
				continue
			case monitorStop, monitorRestart:
				if transition != nil {
					flash = transition.label + " " + transition.name + " is already in progress"
					break
				}
				if len(rows) > 0 {
					_, _, planErr := monitorMutationPlan(key, rows[selected])
					if planErr != nil {
						flash = planErr.Error()
					} else {
						confirmation = &monitorConfirmation{
							key: key, name: rows[selected].connection.Metadata.Name,
							persistent: key == monitorStop && rows[selected].state.Mode == service.ModePersistent,
						}
					}
				}
			}
			if detail && key == monitorEnterDetail {
				if err := refresh(); err != nil {
					flash = err.Error()
					render(false)
					flash = ""
				}
			} else {
				render(false)
				flash = ""
			}
		}
	}
}

func makeMonitorRaw(input io.Reader) (func(), error) {
	file, ok := input.(*os.File)
	if !ok {
		return func() {}, nil
	}
	fd := int(file.Fd())
	previous, err := term.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("enter monitor terminal mode: %w", err)
	}
	return func() { _ = term.Restore(fd, previous) }, nil
}

func readMonitorKeys(input io.Reader, output chan<- monitorKey) {
	inputBytes := make(chan byte)
	go func() {
		defer close(inputBytes)
		buffer := make([]byte, 1)
		for {
			n, err := input.Read(buffer)
			if n == 1 {
				inputBytes <- buffer[0]
			}
			if err != nil {
				return
			}
		}
	}()
	for value := range inputBytes {
		if value != 0x1b {
			emitMonitorKey(value, output)
			continue
		}
		next, ok, timedOut := readMonitorByte(inputBytes, monitorEscapeDelay)
		if !ok || timedOut {
			output <- monitorEscape
			if !ok {
				return
			}
			continue
		}
		if next != '[' {
			output <- monitorEscape
			emitMonitorKey(next, output)
			continue
		}
		direction, ok, timedOut := readMonitorByte(inputBytes, monitorEscapeDelay)
		if !ok || timedOut {
			output <- monitorEscape
			if !ok {
				return
			}
			continue
		}
		switch direction {
		case 'A':
			output <- monitorUp
		case 'B':
			output <- monitorDown
		case 'C':
			output <- monitorEnterDetail
		case 'D':
			output <- monitorBack
		default:
			output <- monitorCancel
		}
	}
}

func readMonitorByte(input <-chan byte, timeout time.Duration) (byte, bool, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case value, ok := <-input:
		return value, ok, false
	case <-timer.C:
		return 0, true, true
	}
}

func emitMonitorKey(value byte, output chan<- monitorKey) {
	switch value {
	case 'k':
		output <- monitorUp
	case 'j':
		output <- monitorDown
	case '\r', '\n':
		output <- monitorEnterDetail
	case 's':
		output <- monitorStart
	case 'x':
		output <- monitorStop
	case 'r':
		output <- monitorRestart
	case 'y', 'Y':
		output <- monitorConfirm
	case 'q', 3:
		output <- monitorQuit
	default:
		output <- monitorCancel
	}
}

func collectMonitorRows(ctx context.Context, group string, opts options) ([]statusRow, error) {
	adapter, records, config, document, err := serviceDependencies(opts)
	if err != nil {
		return nil, err
	}
	targets := make([]core.Connection, 0)
	for _, connection := range document.Items {
		if group == "" || connection.Metadata.Group == group {
			targets = append(targets, connection)
		}
	}
	rows := make([]statusRow, 0, len(targets))
	results := collectStatusResults(ctx, adapter, records, targets, config.Path(), defaultStatusObservationBudget())
	for index, result := range results {
		if result.Error != nil {
			return nil, errors.New(result.Error.Message)
		}
		if result.State != nil {
			rows = append(rows, statusRow{connection: targets[index], state: *result.State})
		}
	}
	sortStatusRows(rows)
	return rows, nil
}

func collectMonitorLogs(ctx context.Context, row statusRow, opts options) ([]string, error) {
	adapter, records, config, _, err := serviceDependencies(opts)
	if err != nil {
		return nil, err
	}
	record, err := loadServiceRecord(records, row.connection, config.Path())
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	if err := adapter.Logs(ctx, record.Reference(), 20, false, service.Streams{Out: &stdout, Err: &stderr}); err != nil {
		return nil, err
	}
	stdoutLines := splitLogLines(stdout.String())
	stderrLines := splitLogLines(stderr.String())
	if len(stdoutLines)+len(stderrLines) > 20 && len(stderrLines) > 0 {
		stdoutLimit := minInt(10, len(stdoutLines))
		stdoutLines = stdoutLines[len(stdoutLines)-stdoutLimit:]
		stderrLimit := minInt(20-len(stdoutLines), len(stderrLines))
		stderrLines = stderrLines[len(stderrLines)-stderrLimit:]
	} else if len(stdoutLines) > 20 {
		stdoutLines = stdoutLines[len(stdoutLines)-20:]
	}
	lines := append([]string(nil), stdoutLines...)
	for _, line := range stderrLines {
		lines = append(lines, "stderr: "+line)
	}
	return lines, nil
}

func sortStatusRows(rows []statusRow) {
	sort.Slice(rows, func(i, j int) bool { return connectionLess(rows[i].connection, rows[j].connection) })
}

func renderMonitor(
	out io.Writer,
	rows []statusRow,
	selected int,
	detail bool,
	logs []string,
	color bool,
	group string,
	interval time.Duration,
	flash string,
	confirmation *monitorConfirmation,
	transition *monitorTransition,
	loading bool,
	lang language,
) {
	width, height := monitorDimensions(out)
	if selected < 0 || selected >= len(rows) {
		selected = 0
	}
	view := localized(lang, "connections", "连接")
	if detail && len(rows) > 0 {
		view = rows[selected].connection.Metadata.Name + " · " + localized(lang, "detail", "详情")
	}
	left := fmt.Sprintf("tunlite monitor  v%s  ·  %s", Version, view)
	right := fmt.Sprintf("%s  %s %s", time.Now().Format("15:04:05"), localized(lang, "refresh", "刷新"), interval)
	title := monitorTitle(left, right, width)
	lines := []string{colorize(title, ansiReverse, color)}
	separator := strings.Repeat("─", width)

	if detail && len(rows) > 0 {
		lines = append(lines, separator)
		bodyCapacity := maxInt(1, height-4)
		body := renderMonitorDetail(rows[selected], logs, bodyCapacity, width, lang)
		lines = append(lines, body...)
		for len(lines) < height-2 {
			lines = append(lines, strings.Repeat(" ", width))
		}
	} else {
		running, changing, problem, stopped := monitorCounts(rows)
		summary := fmt.Sprintf(localized(lang,
			"connections %d total   ● %d running   ◌ %d changing   ✕ %d problem   ○ %d stopped",
			"连接 %d 个   ● %d 运行   ◌ %d 变更中   ✕ %d 异常   ○ %d 停止"),
			len(rows), running, changing, problem, stopped)
		if width < 80 {
			summary = fmt.Sprintf(localized(lang,
				"all %d   ● run %d   ◌ change %d   ✕ bad %d   ○ stop %d",
				"共 %d  ● 运行 %d  ◌ 变更 %d  ✕ 异常 %d  ○ 停止 %d"),
				len(rows), running, changing, problem, stopped)
		}
		if group != "" {
			summary += "   " + localized(lang, "filter group=", "筛选 分组=") + group
		}
		lines = append(lines, fitCell(summary, width), separator)
		bodyCapacity := maxInt(1, height-5)
		if loading {
			lines = append(lines, fitCell(localized(lang, "  Loading connection status…", "  正在读取连接状态…"), width))
		} else if len(rows) == 0 {
			lines = append(lines, fitCell(localized(lang, "  No matching connections.", "  没有符合条件的连接。"), width))
		} else {
			start, end := monitorGroupedViewport(rows, selected, maxInt(1, bodyCapacity-1))
			lines = append(lines, renderMonitorTable(rows, selected, start, end, width, color, lang)...)
		}
		for len(lines) < height-2 {
			lines = append(lines, strings.Repeat(" ", width))
		}
	}

	lines = append(lines, separator)
	footer := localized(lang,
		" ↑/↓ select   Enter/→ details   s start   x stop   r restart   Esc/q quit",
		" ↑/↓ 选择   Enter/→ 详情   s 启动   x 停止   r 重启   Esc/q 退出")
	if detail {
		footer = localized(lang,
			" ←/Esc back   s start   x stop   r restart   q quit",
			" ←/Esc 返回   s 启动   x 停止   r 重启   q 退出")
	} else if width < 80 {
		footer = localized(lang,
			" ↑/↓ select  → detail  s start  x stop  r restart  Esc quit",
			" ↑/↓ 选择  → 详情  s 启动  x 停止  r 重启  Esc 退出")
	}
	shade := ""
	if confirmation != nil {
		footer = monitorConfirmationText(*confirmation, lang)
		shade = ansiYellow
	} else if transition != nil {
		footer = fmt.Sprintf(" %s %s…", localizedTransition(transition.label, lang), transition.name)
		shade = ansiYellow
	} else if flash != "" {
		footer = " " + flash
		shade = ansiYellow
	}
	footer = fitCell(footer, width)
	lines = append(lines, colorize(footer, shade, color))
	fmt.Fprint(out, "\x1b[H", strings.Join(lines, "\x1b[K\r\n"), "\x1b[K\x1b[J")
}

func monitorDimensions(out io.Writer) (int, int) {
	width, height := 120, 30
	if file, ok := out.(*os.File); ok {
		if columns, rows, err := term.GetSize(int(file.Fd())); err == nil {
			width, height = columns, rows
		}
	}
	if width < 24 {
		width = 24
	}
	if height < 8 {
		height = 8
	}
	return width, height
}

func monitorTitle(left, right string, width int) string {
	if visibleWidth(left)+visibleWidth(right)+2 > width {
		return fitCell(left, width)
	}
	return left + strings.Repeat(" ", width-visibleWidth(left)-visibleWidth(right)) + right
}

func monitorCounts(rows []statusRow) (running, changing, problem, stopped int) {
	for _, row := range rows {
		switch {
		case row.transition != "" || row.state.ProcessState == service.ProcessStarting:
			changing++
		case row.state.ConsistencyState != service.ConsistencyConsistent || row.state.ProcessState == service.ProcessFailed || row.state.Health == service.HealthUnhealthy:
			problem++
		case row.state.ProcessState == service.ProcessRunning || row.state.Mode == service.ModeForeground:
			running++
		default:
			stopped++
		}
	}
	return running, changing, problem, stopped
}

func monitorViewport(count, selected, capacity int) (int, int) {
	if capacity <= 0 || count <= capacity {
		return 0, minInt(count, capacity)
	}
	start := selected - capacity/2
	if start < 0 {
		start = 0
	}
	if start+capacity > count {
		start = count - capacity
	}
	return start, start + capacity
}

func monitorGroupedViewport(rows []statusRow, selected, capacity int) (int, int) {
	if len(rows) == 0 || capacity <= 0 {
		return 0, 0
	}
	if selected < 0 || selected >= len(rows) {
		selected = 0
	}
	start, end := selected, selected+1
	for {
		grew := false
		if end < len(rows) && monitorGroupedLineCount(rows, start, end+1) <= capacity {
			end++
			grew = true
		}
		if start > 0 && monitorGroupedLineCount(rows, start-1, end) <= capacity {
			start--
			grew = true
		}
		if !grew {
			break
		}
	}
	return start, end
}

func monitorGroupedLineCount(rows []statusRow, start, end int) int {
	if start >= end {
		return 0
	}
	lines := end - start
	for index := start; index < end; index++ {
		if index == start || rows[index-1].connection.Metadata.Group != rows[index].connection.Metadata.Group {
			lines++
		}
	}
	return lines
}

func renderMonitorTable(rows []statusRow, selected, start, end, width int, color bool, lang language) []string {
	columns := []tableColumn{
		{header: localized(lang, "NAME", "名称"), max: 24, value: func(row statusRow) string { return row.connection.Metadata.Name }},
		{header: localized(lang, "STATE", "状态"), max: 16, value: func(row statusRow) string {
			style := displayStatusRow(row, lang)
			return style.glyph + " " + style.label
		}},
		{header: localized(lang, "RUNS AS", "运行位置"), max: 12, value: func(row statusRow) string { return statusRunsAs(row, lang) }},
		{header: localized(lang, "ON LOGIN", "登录自启"), max: 10, value: func(row statusRow) string { return statusOnLogin(row, lang) }},
		{header: "PID", max: 7, value: statusPID},
		{header: localized(lang, "UP", "已运行"), max: 8, value: statusUptime},
		{header: localized(lang, "RESTARTS", "重启次数"), max: 8, value: statusRestarts},
		{header: localized(lang, "HOST", "SSH 目标"), max: 28, value: statusHost},
		{header: localized(lang, "ROUTE", "转发"), max: 38, value: statusRoutes},
	}
	if width < 120 {
		columns = columns[:len(columns)-1]
	}
	if width < 100 {
		columns = columns[:len(columns)-1]
	}
	if width < 82 {
		columns = []tableColumn{columns[0], columns[1], columns[4], columns[5], columns[6]}
	}
	widths := make([]int, len(columns))
	for index, column := range columns {
		widths[index] = visibleWidth(column.header)
		for _, row := range rows {
			cellWidth := visibleWidth(column.value(row))
			if column.max > 0 && cellWidth > column.max {
				cellWidth = column.max
			}
			if cellWidth > widths[index] {
				widths[index] = cellWidth
			}
		}
	}
	available := width - 2 - (len(columns) - 1)
	for tableWidth(widths) > available {
		changed := false
		for _, columnIndex := range []int{len(columns) - 1, len(columns) - 2, 0, 1, 2, 3, 5, 6, 4} {
			if columnIndex >= 0 && columnIndex < len(columns) {
				if widths[columnIndex] > visibleWidth(columns[columnIndex].header) {
					widths[columnIndex]--
					changed = true
				}
			}
			if tableWidth(widths) <= available {
				break
			}
		}
		if !changed {
			break
		}
	}
	headerCells := make([]string, len(columns))
	for index, column := range columns {
		headerCells[index] = fitCell(column.header, widths[index])
	}
	lines := []string{colorize("  "+strings.Join(headerCells, " "), ansiDim, color)}
	for rowIndex := start; rowIndex < end; rowIndex++ {
		row := rows[rowIndex]
		if rowIndex == start || rows[rowIndex-1].connection.Metadata.Group != row.connection.Metadata.Group {
			heading := fitCell(monitorGroupHeading(rows, row.connection.Metadata.Group, lang), width)
			lines = append(lines, colorize(heading, ansiDim, color))
		}
		cells := make([]string, len(columns))
		for index, column := range columns {
			cells[index] = fitCell(column.value(row), widths[index])
			if index == 1 && rowIndex != selected {
				cells[index] = colorize(cells[index], displayStatusRow(row, lang).color, color)
			}
		}
		marker := "  "
		if rowIndex == selected {
			marker = "> "
		}
		line := marker + strings.Join(cells, " ")
		if rowIndex == selected {
			line = colorize(line, ansiReverse, color)
		}
		lines = append(lines, line)
	}
	return lines
}

func monitorGroupHeading(rows []statusRow, group string, lang language) string {
	groupRows := make([]statusRow, 0)
	for _, row := range rows {
		if row.connection.Metadata.Group == group {
			groupRows = append(groupRows, row)
		}
	}
	running, changing, problem, stopped := monitorCounts(groupRows)
	if isChinese(lang) {
		return fmt.Sprintf("  ── 分组 %s · %d 个  ● %d  ◌ %d  ✕ %d  ○ %d",
			groupLabel(group, lang), len(groupRows), running, changing, problem, stopped)
	}
	return fmt.Sprintf("  ── Group %s · %d  ● %d  ◌ %d  ✕ %d  ○ %d",
		groupLabel(group, lang), len(groupRows), running, changing, problem, stopped)
}

func tableWidth(widths []int) int {
	total := maxInt(0, len(widths)-1)
	for _, width := range widths {
		total += width
	}
	return total
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func renderMonitorDetail(row statusRow, logs []string, capacity, width int, lang language) []string {
	info := renderStatusDetail(row, false, lang)
	lines := make([]string, 0, capacity)
	for _, line := range info {
		if len(lines) >= capacity {
			break
		}
		lines = append(lines, fitCell(line, width))
	}
	if len(lines) < capacity {
		label := localized(lang, " recent logs · latest 20 lines ", " 最近日志 · 最新 20 行 ")
		left := maxInt(0, (width-visibleWidth(label))/2)
		divider := strings.Repeat("─", left) + label
		divider += strings.Repeat("─", maxInt(0, width-visibleWidth(divider)))
		lines = append(lines, divider)
	}
	remaining := capacity - len(lines)
	if remaining <= 0 {
		return lines
	}
	if len(logs) == 0 {
		lines = append(lines, fitCell(localized(lang, "  No logs yet.", "  暂无日志。"), width))
		return lines
	}
	start := maxInt(0, len(logs)-remaining)
	for _, line := range logs[start:] {
		lines = append(lines, fitCell("  "+line, width))
	}
	return lines
}

func monitorMutationPlan(key monitorKey, row statusRow) (command, transition string, err error) {
	switch key {
	case monitorStart:
		if row.state.Mode != service.ModeStopped {
			return "", "", fmt.Errorf("%s is already %s", row.connection.Metadata.Name, row.state.Mode)
		}
		return "start", "starting", nil
	case monitorStop:
		switch row.state.Mode {
		case service.ModeBackground:
			return "stop", "stopping", nil
		case service.ModePersistent:
			return "disable", "stopping", nil
		default:
			return "", "", fmt.Errorf("%s is not running in the background", row.connection.Metadata.Name)
		}
	case monitorRestart:
		if row.state.Mode != service.ModeBackground && row.state.Mode != service.ModePersistent {
			return "", "", fmt.Errorf("%s is stopped; start it first", row.connection.Metadata.Name)
		}
		return "restart", "restarting", nil
	}
	return "", "", errors.New("unsupported monitor operation")
}

func executeMonitorMutation(ctx context.Context, command string, row statusRow, opts options) string {
	var stderr bytes.Buffer
	terminal := false
	err := serviceCommand(ctx, command, []string{row.connection.Metadata.Name}, IO{Out: io.Discard, Err: &stderr, Terminal: &terminal}, opts)
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return message
	}
	past := map[string]string{
		"start":   localized(opts.Language, "started", "已启动"),
		"stop":    localized(opts.Language, "stopped", "已停止"),
		"disable": localized(opts.Language, "stopped and disabled on login", "已停止并关闭登录自启"),
		"restart": localized(opts.Language, "restarted", "已重启"),
	}
	return past[command] + " " + row.connection.Metadata.Name
}

func localizedTransition(value string, lang language) string {
	switch value {
	case "starting":
		return localized(lang, "starting", "正在启动")
	case "stopping":
		return localized(lang, "stopping", "正在停止")
	case "restarting":
		return localized(lang, "restarting", "正在重启")
	default:
		return value
	}
}

func monitorConfirmationText(confirmation monitorConfirmation, lang language) string {
	if confirmation.key == monitorRestart {
		return fmt.Sprintf(localized(lang,
			" Restart %q? Connection will briefly drop; on-login stays unchanged. y confirms; any other key cancels",
			" 重启“%s”？连接会短暂中断，登录自启设置不变。按 y 确认，其他任意键取消"), confirmation.name)
	}
	if confirmation.persistent {
		return fmt.Sprintf(localized(lang,
			" Stop %q and turn off on-login? It will not return after login. y confirms; any other key cancels",
			" 停止“%s”并关闭登录自启？重新登录后不会自动恢复。按 y 确认，其他任意键取消"), confirmation.name)
	}
	return fmt.Sprintf(localized(lang,
		" Stop %q? y confirms; any other key cancels",
		" 停止“%s”？按 y 确认，其他任意键取消"), confirmation.name)
}
