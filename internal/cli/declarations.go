package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yuanyuanzijin/tunlite/internal/configstore"
	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
)

const exitPartial = 6

type itemResult struct {
	Connection string `json:"connection"`
	Outcome    string `json:"outcome"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
}

func exportConnections(ctx context.Context, args []string, streams IO, opts options) error {
	flags := flag.NewFlagSet("export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var output string
	var force bool
	flags.StringVar(&output, "output", "", "output file")
	flags.BoolVar(&force, "force", false, "replace output file")
	if err := flags.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError(fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}
	store, err := connectionStore(opts)
	if err != nil {
		return err
	}
	document, err := store.Load()
	if err != nil {
		return err
	}
	if output == "" {
		if opts.JSON {
			return writeJSON(streams.Out, map[string]any{
				"contractVersion": ContractVersion,
				"outcome":         "succeeded",
				"document":        document,
			})
		}
		return writeJSON(streams.Out, document)
	}
	absolute, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	if !force {
		if _, err := os.Stat(absolute); err == nil {
			return conflictError(fmt.Sprintf("config file already exists: %s (use --force to replace it)", absolute))
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if opts.DryRun {
		return renderDeclarationWrite(streams, opts, "planned", absolute, len(document.Items))
	}
	if err := configstore.New(absolute).Write(ctx, document, force); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return conflictError(err.Error() + " (use --force to replace it)")
		}
		return err
	}
	return renderDeclarationWrite(streams, opts, "applied", absolute, len(document.Items))
}

func importConnections(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageError("usage: tunlite import <file> [--force]")
	}
	sourceArgument := args[0]
	flags := flag.NewFlagSet("import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var force bool
	flags.BoolVar(&force, "force", false, "replace same-name declarations")
	if err := flags.Parse(args[1:]); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError(fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}
	sourcePath, err := filepath.Abs(sourceArgument)
	if err != nil {
		return err
	}
	if _, err := os.Stat(sourcePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return notFoundError(fmt.Sprintf("import file not found: %s", sourcePath))
		}
		return err
	}
	source, err := configstore.New(sourcePath).Load()
	if err != nil {
		return err
	}
	target, err := connectionStore(opts)
	if err != nil {
		return err
	}
	var results []itemResult
	noChanges := errors.New("import has no applicable changes")
	apply := func(document *configstore.Document) error {
		results = make([]itemResult, 0, len(source.Items))
		changed := false
		for _, imported := range source.Items {
			name := imported.Metadata.Name
			if err := validateKnownDriver(imported); err != nil {
				results = append(results, itemResult{Connection: name, Outcome: "failed", Code: "invalid_driver_config", Message: err.Error()})
				continue
			}
			index, existing := configstore.Find(document, name)
			if existing != nil && !force {
				results = append(results, itemResult{Connection: name, Outcome: "failed", Code: "already_exists", Message: "same-name connection already exists"})
				continue
			}
			copy := cloneConnection(imported)
			if existing == nil {
				document.Items = append(document.Items, copy)
				changed = true
				results = append(results, itemResult{Connection: name, Outcome: "added"})
			} else {
				document.Items[index] = copy
				changed = true
				results = append(results, itemResult{Connection: name, Outcome: "updated"})
			}
		}
		if !changed && !opts.DryRun {
			return noChanges
		}
		return nil
	}
	if opts.DryRun {
		document, err := target.Load()
		if err != nil {
			return err
		}
		if err := apply(&document); err != nil {
			return err
		}
	} else if err := target.Update(ctx, apply); err != nil && !errors.Is(err, noChanges) {
		return err
	}
	return renderImportResult(streams, opts, sourcePath, results)
}

func validateConnections(args []string, streams IO, opts options) error {
	if len(args) > 1 {
		return usageError("usage: tunlite validate [file]")
	}
	var store *configstore.Store
	if len(args) == 1 {
		path, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return notFoundError(fmt.Sprintf("validation file not found: %s", path))
			}
			return err
		}
		store = configstore.New(path)
	} else {
		var err error
		store, err = connectionStore(opts)
		if err != nil {
			return err
		}
	}
	document, err := store.Load()
	if err != nil {
		return err
	}
	conditions := make([]condition, 0)
	failed := false
	for _, connection := range document.Items {
		if err := validateKnownDriver(connection); err != nil {
			failed = true
			conditions = append(conditions, condition{
				Code: "invalid_driver_config", Severity: "error", Connection: connection.Metadata.Name,
				Driver: connection.Spec.Driver, Message: err.Error(), Actions: []core.Action{},
			})
		} else if connection.Spec.Driver != sshdriver.ID {
			conditions = append(conditions, condition{
				Code: "driver_not_installed", Severity: "warning", Connection: connection.Metadata.Name,
				Message: fmt.Sprintf("driver %q is not installed; declaration was not executed", connection.Spec.Driver),
				Driver:  connection.Spec.Driver,
				Actions: []core.Action{{Type: "inspect_driver", Parameters: map[string]any{"driver": connection.Spec.Driver}}},
			})
		}
	}
	sort.Slice(conditions, func(i, j int) bool {
		if conditions[i].Connection == conditions[j].Connection {
			return conditions[i].Code < conditions[j].Code
		}
		return conditions[i].Connection < conditions[j].Connection
	})
	outcome := "succeeded"
	if failed {
		outcome = "failed"
	}
	if opts.JSON {
		_ = writeJSON(streams.Out, map[string]any{
			"contractVersion": ContractVersion,
			"outcome":         outcome, "path": store.Path(), "connections": len(document.Items), "conditions": conditions,
		})
	} else {
		fmt.Fprintf(streams.Out, "%s: %d connection(s)\n", store.Path(), len(document.Items))
		for _, item := range conditions {
			fmt.Fprintf(streams.Out, "%s: %s: %s\n", item.Severity, item.Connection, item.Message)
		}
	}
	if failed {
		return renderedExit(1)
	}
	return nil
}

func validateKnownDriver(connection core.Connection) error {
	if connection.Spec.Driver != sshdriver.ID {
		return nil
	}
	if connection.Spec.ConfigVersion != 1 {
		return fmt.Errorf("connection %q uses unsupported SSH configVersion %d", connection.Metadata.Name, connection.Spec.ConfigVersion)
	}
	config, err := sshdriver.DecodeConfig(connection.Spec.Config)
	if err != nil {
		return fmt.Errorf("connection %q: %w", connection.Metadata.Name, err)
	}
	if err := sshdriver.Validate(config); err != nil {
		return fmt.Errorf("connection %q: %w", connection.Metadata.Name, err)
	}
	return nil
}

func renderDeclarationWrite(streams IO, opts options, outcome, path string, count int) error {
	if opts.JSON {
		return writeJSON(streams.Out, map[string]any{
			"contractVersion": ContractVersion, "outcome": outcome, "path": path,
			"connections": count, "dryRun": opts.DryRun,
		})
	}
	verb := "exported"
	if opts.DryRun {
		verb = "would export"
	}
	fmt.Fprintf(streams.Out, "%s %d connection(s) to %s\n", verb, count, path)
	return nil
}

func renderImportResult(streams IO, opts options, source string, results []itemResult) error {
	failed := 0
	applied := 0
	for _, result := range results {
		if result.Outcome == "failed" {
			failed++
		} else {
			applied++
		}
	}
	outcome := "applied"
	if opts.DryRun {
		outcome = "planned"
	}
	if failed > 0 {
		if applied > 0 {
			outcome = "partial"
		} else {
			outcome = "failed"
		}
	}
	if opts.JSON {
		_ = writeJSON(streams.Out, map[string]any{
			"contractVersion": ContractVersion, "outcome": outcome, "source": source,
			"results": results, "dryRun": opts.DryRun,
		})
	} else {
		for _, result := range results {
			fmt.Fprintf(streams.Out, "%s: %s", result.Connection, result.Outcome)
			if result.Message != "" {
				fmt.Fprintf(streams.Out, " (%s)", result.Message)
			}
			fmt.Fprintln(streams.Out)
		}
	}
	if failed > 0 {
		return renderedExit(exitPartial)
	}
	return nil
}
