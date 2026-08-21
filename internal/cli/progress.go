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
	verb, ok := map[string]string{
		"run":      "Preparing",
		"add":      "Adding",
		"set":      "Updating",
		"remove":   "Removing",
		"rename":   "Renaming",
		"import":   "Importing",
		"check":    "Checking SSH for",
		"doctor":   "Running diagnostics for",
		"monitor":  "Opening monitor",
		"start":    "Starting",
		"stop":     "Stopping",
		"enable":   "Enabling",
		"disable":  "Disabling",
		"restart":  "Restarting",
		"status":   "Checking status for",
		"logs":     "Reading logs for",
		"manage":   "Managing",
		"unmanage": "Unmanaging",
		"resolve":  "Resolving",
	}[command]
	if !ok {
		return
	}
	if opts.DryRun {
		verb = "Planning " + strings.ToLower(verb)
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
	return writerIsTerminal(streams.Err) || writerIsTerminal(streams.Out)
}
