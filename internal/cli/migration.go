package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yuanyuanzijin/tunlite/internal/configstore"
	"github.com/yuanyuanzijin/tunlite/internal/core"
	legacyMigration "github.com/yuanyuanzijin/tunlite/internal/migration"
	appPaths "github.com/yuanyuanzijin/tunlite/internal/paths"
	"github.com/yuanyuanzijin/tunlite/internal/service"
)

func migrateConfig(ctx context.Context, args []string, streams IO, opts options) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var rollback bool
	flags.BoolVar(&rollback, "rollback", false, "restore the byte-for-byte 0.x backup")
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
	stateDir, err := appPaths.StateDir()
	if err != nil {
		return err
	}
	var report legacyMigration.Report
	if rollback {
		if err := ensureRollbackServicesDisabled(store, stateDir); err != nil {
			return err
		}
		report, err = legacyMigration.Rollback(ctx, store.Path(), stateDir, opts.DryRun)
	} else {
		report, err = legacyMigration.Migrate(ctx, store.Path(), stateDir, opts.DryRun)
	}
	if err != nil {
		return err
	}
	if opts.JSON {
		return writeJSON(streams.Out, map[string]any{
			"contractVersion": ContractVersion,
			"outcome":         report.Outcome,
			"result":          report.Result,
			"operation":       report.Operation,
			"sourceVersion":   report.SourceVersion,
			"path":            report.Path,
			"backupPath":      report.BackupPath,
			"journalPath":     report.JournalPath,
			"connections":     report.Connections,
			"dryRun":          report.DryRun,
			"conditions":      report.Conditions,
		})
	}
	fmt.Fprintf(streams.Out, "%s: %s (%d connection(s))\n", report.Operation, report.Result, report.Connections)
	if report.BackupPath != "" {
		fmt.Fprintf(streams.Out, "backup: %s\n", report.BackupPath)
	}
	for _, condition := range report.Conditions {
		if condition.Connection == "" {
			fmt.Fprintf(streams.Out, "%s: %s\n", condition.Severity, condition.Message)
		} else {
			fmt.Fprintf(streams.Out, "%s: %s: %s\n", condition.Severity, condition.Connection, condition.Message)
		}
	}
	return nil
}

func ensureRollbackServicesDisabled(store *configstore.Store, stateDir string) error {
	document, err := store.Load()
	if err != nil {
		var legacy *configstore.LegacyConfigError
		if errors.As(err, &legacy) {
			return nil
		}
		return err
	}
	records := service.NewStore(filepath.Join(stateDir, "services"))
	enabled := make([]string, 0)
	for _, connection := range document.Items {
		record, exists, err := records.Load(connection.Metadata.Name)
		if err != nil {
			return err
		}
		if exists && record.DesiredState == service.DesiredEnabled {
			enabled = append(enabled, connection.Metadata.Name)
		}
	}
	if len(enabled) == 0 {
		return nil
	}
	sort.Strings(enabled)
	return &exitError{
		code: 1, kind: "migration_services_enabled",
		message:   "disable all v1 connections before rolling back the configuration",
		riskLevel: "high", actions: []core.Action{{
			Type: "disable_connections", Parameters: map[string]any{"connections": enabled},
		}},
	}
}
