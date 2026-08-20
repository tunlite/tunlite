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
	monitorToggleDetail
	monitorStart
	monitorStop
	monitorRestart
	monitorQuit
)

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

	restore, err := makeMonitorRaw(streams.In)
	if err != nil {
		return err
	}
	defer restore()
	fmt.Fprint(streams.Out, "\x1b[?25l\x1b[2J\x1b[H")
	defer fmt.Fprint(streams.Out, "\x1b[?25h\x1b[0m\n")

	keys := make(chan monitorKey, 8)
	go readMonitorKeys(streams.In, keys)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	selected := 0
	detail := false
	flash := ""
	var rows []statusRow
	var logs []string
	refresh := func() error {
		fresh, refreshErr := collectMonitorRows(ctx, tags, opts)
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
			logs, logsErr = collectMonitorLogs(ctx, rows[selected], opts)
			if logsErr != nil {
				flash = "logs: " + logsErr.Error()
			}
		}
		renderMonitor(streams.Out, rows, selected, detail, logs, color, tags, interval, flash)
		flash = ""
		return nil
	}
	if err := refresh(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := refresh(); err != nil {
				flash = err.Error()
				renderMonitor(streams.Out, rows, selected, detail, logs, color, tags, interval, flash)
			}
		case key := <-keys:
			switch key {
			case monitorQuit:
				return nil
			case monitorUp:
				if selected > 0 {
					selected--
				}
			case monitorDown:
				if selected+1 < len(rows) {
					selected++
				}
			case monitorToggleDetail:
				if len(rows) > 0 {
					detail = !detail
				}
			case monitorStart, monitorStop, monitorRestart:
				if len(rows) > 0 {
					flash = monitorMutation(ctx, key, rows[selected], opts)
				}
			}
			if err := refresh(); err != nil {
				flash = err.Error()
				renderMonitor(streams.Out, rows, selected, detail, logs, color, tags, interval, flash)
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
	buffer := make([]byte, 1)
	escape := 0
	for {
		if _, err := input.Read(buffer); err != nil {
			return
		}
		value := buffer[0]
		if escape == 1 {
			if value == '[' {
				escape = 2
				continue
			}
			escape = 0
		}
		if escape == 2 {
			escape = 0
			if value == 'A' {
				output <- monitorUp
			} else if value == 'B' {
				output <- monitorDown
			}
			continue
		}
		switch value {
		case 0x1b:
			escape = 1
		case 'k':
			output <- monitorUp
		case 'j':
			output <- monitorDown
		case '\r', '\n':
			output <- monitorToggleDetail
		case 's':
			output <- monitorStart
		case 'x':
			output <- monitorStop
		case 'r':
			output <- monitorRestart
		case 'q', 3:
			output <- monitorQuit
		}
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

func renderMonitor(out io.Writer, rows []statusRow, selected int, detail bool, logs []string, color bool, tags []string, interval time.Duration, flash string) {
	title := fmt.Sprintf("tunlite monitor · %d connection", len(rows))
	if len(rows) != 1 {
		title += "s"
	}
	if len(tags) > 0 {
		title += " · tag " + strings.Join(tags, ",")
	}
	title += fmt.Sprintf(" · refresh %s", interval)
	lines := []string{colorize(title, ansiGreen, color), ""}
	if len(rows) == 0 {
		lines = append(lines, "No matching connections.")
	} else if detail {
		lines = append(lines, renderStatusDetail(rows[selected], color)...)
		lines = append(lines, "", colorize("LOGS · latest 20 lines", ansiDim, color))
		if len(logs) == 0 {
			lines = append(lines, colorize("  No logs yet.", ansiDim, color))
		} else {
			for _, line := range logs {
				lines = append(lines, "  "+line)
			}
		}
	} else {
		lines = append(lines, renderStatusTableSelected(rows, color, selected)...)
	}
	lines = append(lines, "")
	if flash != "" {
		lines = append(lines, colorize(flash, ansiYellow, color))
	}
	lines = append(lines, "↑/↓ or j/k select   enter details   s start   x stop   r restart   q quit")
	fmt.Fprint(out, "\x1b[H", strings.Join(lines, "\x1b[K\r\n"), "\x1b[K\x1b[J")
}

func monitorMutation(ctx context.Context, key monitorKey, row statusRow, opts options) string {
	command := ""
	switch key {
	case monitorStart:
		if row.state.Mode != service.ModeStopped {
			return row.connection.Metadata.Name + " is already " + row.state.Mode
		}
		command = "start"
	case monitorStop:
		switch row.state.Mode {
		case service.ModeBackground:
			command = "stop"
		case service.ModePersistent:
			command = "disable"
		default:
			return row.connection.Metadata.Name + " is not running in the background"
		}
	case monitorRestart:
		if row.state.Mode != service.ModeBackground && row.state.Mode != service.ModePersistent {
			return row.connection.Metadata.Name + " is stopped; start it first"
		}
		command = "restart"
	}
	var stderr bytes.Buffer
	err := serviceCommand(ctx, command, []string{row.connection.Metadata.Name}, IO{Out: io.Discard, Err: &stderr}, opts)
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return message
	}
	past := map[string]string{"start": "started", "stop": "stopped", "disable": "disabled", "restart": "restarted"}
	return past[command] + " " + row.connection.Metadata.Name
}
