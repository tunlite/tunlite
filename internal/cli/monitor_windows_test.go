//go:build windows

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestMonitorRejectsUnsupportedWindowsConsoleBeforeEnteringRawMode(t *testing.T) {
	terminal := true
	var output bytes.Buffer
	err := monitor(context.Background(), nil, IO{
		In:       bytes.NewBuffer(nil),
		Out:      &output,
		Err:      &output,
		Terminal: &terminal,
	}, options{})
	if err == nil || !strings.Contains(err.Error(), "monitor is not available on this platform") {
		t.Fatalf("monitor error=%v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("monitor changed the terminal before rejecting Windows: %q", output.String())
	}
}
