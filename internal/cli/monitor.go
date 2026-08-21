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
	key  monitorKey
	name string
	verb string
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
	var rawTags stringsFlag
	flags.StringVar(&intervalText, "interval", "1", "refresh interval in seconds")
	flags.Var(&rawTags, "tag", "filter by tag")
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
	tags, err := parseTags(rawTags)
	if err != nil {
		return err
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
		renderMonitor(streams.Out, visible, selected, detail, logs, color, tags, interval, flash, confirmation, transition, loading)
	}
	refresh := func() error {
		fresh, refreshErr := collectMonitorRows(monitorCtx, tags, opts)
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
					flash = pending.verb + " cancelled"
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
						verb := map[monitorKey]string{monitorStop: "stop", monitorRestart: "restart"}[key]
						confirmation = &monitorConfirmation{key: key, name: rows[selected].connection.Metadata.Name, verb: verb}
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

func collectMonitorRows(ctx context.Context, tags []string, opts options) ([]statusRow, error) {
	adapter, records, config, document, err := serviceDependencies(opts)
	if err != nil {
		return nil, err
	}
	targets := make([]core.Connection, 0)
	for _, connection := range document.Items {
		if len(tags) == 0 || hasAllTags(connection.Metadata.Tags, tags) {
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
	sort.Slice(rows, func(i, j int) bool { return rows[i].connection.Metadata.Name < rows[j].connection.Metadata.Name })
}

func renderMonitor(
	out io.Writer,
	rows []statusRow,
	selected int,
	detail bool,
	logs []string,
	color bool,
	tags []string,
	interval time.Duration,
	flash string,
	confirmation *monitorConfirmation,
	transition *monitorTransition,
	loading bool,
) {
	width, height := monitorDimensions(out)
	if selected < 0 || selected >= len(rows) {
		selected = 0
	}
	view := "connections"
	if detail && len(rows) > 0 {
		view = rows[selected].connection.Metadata.Name + " · detail"
	}
	left := fmt.Sprintf("tunlite monitor  v%s  ·  %s", Version, view)
	right := fmt.Sprintf("%s  refresh %s", time.Now().Format("15:04:05"), interval)
	title := monitorTitle(left, right, width)
	lines := []string{colorize(title, ansiReverse, color)}
	separator := strings.Repeat("─", width)

	if detail && len(rows) > 0 {
		lines = append(lines, separator)
		bodyCapacity := maxInt(1, height-4)
		body := renderMonitorDetail(rows[selected], logs, bodyCapacity, width)
		lines = append(lines, body...)
		for len(lines) < height-2 {
			lines = append(lines, strings.Repeat(" ", width))
		}
	} else {
		running, changing, problem, stopped := monitorCounts(rows)
		summary := fmt.Sprintf("connections %d total   ● %d running   ◌ %d changing   ✕ %d problem   ○ %d stopped",
			len(rows), running, changing, problem, stopped)
		if width < 80 {
			summary = fmt.Sprintf("all %d   ● run %d   ◌ change %d   ✕ bad %d   ○ stop %d",
				len(rows), running, changing, problem, stopped)
		}
		if len(tags) > 0 {
			summary += "   filter tag=" + strings.Join(tags, ",")
		}
		lines = append(lines, fitCell(summary, width), separator)
		bodyCapacity := maxInt(1, height-5)
		if loading {
			lines = append(lines, fitCell("  Loading connection status…", width))
		} else if len(rows) == 0 {
			lines = append(lines, fitCell("  No matching connections.", width))
		} else {
			start, end := monitorViewport(len(rows), selected, maxInt(0, bodyCapacity-1))
			lines = append(lines, renderMonitorTable(rows, selected, start, end, width, color)...)
		}
		for len(lines) < height-2 {
			lines = append(lines, strings.Repeat(" ", width))
		}
	}

	lines = append(lines, separator)
	footer := " ↑/↓ select   Enter/→ details   s start   x stop   r restart   Esc/q quit"
	if detail {
		footer = " ←/Esc back   s start   x stop   r restart   q quit"
	} else if width < 80 {
		footer = " ↑/↓ select  → detail  s start  x stop  r restart  Esc quit"
	}
	shade := ""
	if confirmation != nil {
		footer = fmt.Sprintf(" %s %q?  y confirm · any other key cancels", confirmation.verb, confirmation.name)
		shade = ansiYellow
	} else if transition != nil {
		footer = fmt.Sprintf(" %s %s…", transition.label, transition.name)
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
		style := displayStatusRow(row)
		switch style.label {
		case "starting", "stopping", "restarting":
			changing++
		case "blocked", "drifted", "missing", "failed", "unhealthy":
			problem++
		case "healthy", "running", "foreground":
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

func renderMonitorTable(rows []statusRow, selected, start, end, width int, color bool) []string {
	columns := []tableColumn{
		{header: "NAME", max: 24, value: func(row statusRow) string { return row.connection.Metadata.Name }},
		{header: "STATE", max: 12, value: func(row statusRow) string {
			style := displayStatusRow(row)
			return style.glyph + " " + style.label
		}},
		{header: "HOST", max: 28, value: statusHost},
		{header: "TYPE", max: 8, value: statusTypes},
		{header: "ROUTE", max: 38, value: statusRoutes},
		{header: "MODE", max: 10, value: func(row statusRow) string { return row.state.Mode }},
		{header: "PID", max: 7, value: statusPID},
		{header: "UP", max: 8, value: statusUptime},
		{header: "RESTARTS", max: 8, value: statusRestarts},
	}
	if width < 100 {
		columns = []tableColumn{columns[0], columns[1], columns[2], columns[5], columns[6], columns[7]}
	}
	if width < 72 {
		columns = []tableColumn{columns[0], columns[1], columns[3], columns[4]}
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
		for _, header := range []string{"ROUTE", "HOST", "NAME", "STATE", "MODE", "TYPE", "UP", "RESTARTS", "PID"} {
			for index, column := range columns {
				if column.header == header && widths[index] > visibleWidth(column.header) {
					widths[index]--
					changed = true
					break
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
		cells := make([]string, len(columns))
		for index, column := range columns {
			cells[index] = fitCell(column.value(row), widths[index])
			if column.header == "STATE" && rowIndex != selected {
				cells[index] = colorize(cells[index], displayStatusRow(row).color, color)
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

func renderMonitorDetail(row statusRow, logs []string, capacity, width int) []string {
	info := renderStatusDetail(row, false)
	lines := make([]string, 0, capacity)
	for _, line := range info {
		if len(lines) >= capacity {
			break
		}
		lines = append(lines, fitCell(line, width))
	}
	if len(lines) < capacity {
		label := " recent logs · latest 20 lines "
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
		lines = append(lines, fitCell("  No logs yet.", width))
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
	past := map[string]string{"start": "started", "stop": "stopped", "disable": "stopped", "restart": "restarted"}
	return past[command] + " " + row.connection.Metadata.Name
}
