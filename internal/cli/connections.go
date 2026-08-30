package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"

	"github.com/yuanyuanzijin/tunlite/internal/configstore"
	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
)

type connectionFlags struct {
	driver    string
	group     string
	noGroup   bool
	visited   map[string]bool
	driverArg []string
}

type condition = core.Condition

func add(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageError("usage: tunlite add <name> [--driver ssh] [--group group] [-p port] [-i key] [-J jump] -L/-R/-D ... user@host")
	}
	name := args[0]
	values, err := parseConnectionFlags("add", args[1:], false)
	if err != nil {
		return err
	}
	driver := values.driver
	if driver == "" {
		driver = "ssh"
	}
	if driver != "ssh" {
		return usageError(fmt.Sprintf("unknown driver %q (available: ssh)", driver))
	}
	config, err := sshdriver.ParseAddArguments(values.driverArg)
	if err != nil {
		return usageError(err.Error())
	}
	connection, err := newSSHConnection(name, config, values)
	if err != nil {
		return err
	}
	risks, err := connectionRiskConditions(ctx, connection, opts.NonInteractive)
	if err != nil {
		return err
	}
	store, err := connectionStore(opts)
	if err != nil {
		return err
	}
	if opts.DryRun {
		document, err := store.Load()
		if err != nil {
			return err
		}
		if _, existing := configstore.Find(&document, name); existing != nil {
			return conflictError(fmt.Sprintf("a connection named %q already exists", name))
		}
		conditions := endpointConflicts(&document, connection)
		serviceConditions, err := previewNewConnectionService(ctx, connection, store.Path(), opts)
		if err != nil {
			return err
		}
		conditions = append(conditions, serviceConditions...)
		conditions = append(conditions, risks...)
		renderCommandProgress(streams, opts, append([]string{"add", name}, args[1:]...))
		return renderConnectionMutationWithConditions(streams, opts, "add", "planned", connection, conditions)
	}
	document, err := store.Load()
	if err != nil {
		return err
	}
	if _, existing := configstore.Find(&document, name); existing != nil {
		return conflictError(fmt.Sprintf("a connection named %q already exists", name))
	}
	if _, err := previewNewConnectionService(ctx, connection, store.Path(), opts); err != nil {
		return err
	}
	renderCommandProgress(streams, opts, append([]string{"add", name}, args[1:]...))
	mutation, err := stageNewConnectionService(ctx, connection, store.Path(), opts)
	if err != nil {
		return err
	}
	defer mutation.finish()
	var conditions []condition
	if err := store.Update(ctx, func(document *configstore.Document) error {
		if _, existing := configstore.Find(document, name); existing != nil {
			return conflictError(fmt.Sprintf("a connection named %q already exists", name))
		}
		conditions = endpointConflicts(document, connection)
		document.Items = append(document.Items, connection)
		return nil
	}); err != nil {
		return mutation.rollbackAfter(classifyConcurrentChange(err))
	}
	if err := mutation.commit(); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		restoreErr := store.Update(recoveryCtx, func(document *configstore.Document) error {
			index, current := configstore.Find(document, name)
			if current == nil || !connectionsEqual(*current, connection) {
				return configstore.ErrChanged
			}
			document.Items = append(document.Items[:index], document.Items[index+1:]...)
			return nil
		})
		return recoveryBlockedError(err, errors.Join(restoreErr, mutation.rollback()), mutation.actions)
	}
	conditions = append(conditions, mutation.conditions...)
	conditions = append(conditions, risks...)
	return renderConnectionMutationWithConditions(streams, opts, "add", "applied", connection, conditions)
}

func list(args []string, streams IO, opts options) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var group string
	flags.StringVar(&group, "group", "", "filter by group")
	if err := flags.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError(fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}
	groupSet := false
	flags.Visit(func(item *flag.Flag) {
		groupSet = groupSet || item.Name == "group"
	})
	if groupSet {
		var err error
		group, err = parseGroup(group)
		if err != nil {
			return err
		}
	}
	store, err := connectionStore(opts)
	if err != nil {
		return err
	}
	document, err := store.Load()
	if err != nil {
		return err
	}
	results := make([]core.Connection, 0, len(document.Items))
	for _, connection := range document.Items {
		if !groupSet || connection.Metadata.Group == group {
			results = append(results, connection)
		}
	}
	if groupSet && len(results) == 0 {
		return notFoundError(fmt.Sprintf("no connections in group %q", group))
	}
	if opts.JSON {
		return writeResponse(streams.Out, map[string]any{
			"outcome": "succeeded",
			"results": results,
		})
	}
	if len(results) == 0 {
		fmt.Fprintln(streams.Out, localized(opts.Language,
			"No connections yet. Create one with: tunlite add <name> [SSH options] <destination>",
			"还没有连接。请使用 tunlite add <name> [SSH 选项] <destination> 创建。"))
		return nil
	}
	sort.Slice(results, func(i, j int) bool { return connectionLess(results[i], results[j]) })
	currentGroup := "\x00"
	for index, connection := range results {
		if connection.Metadata.Group != currentGroup {
			if index > 0 {
				fmt.Fprintln(streams.Out)
			}
			currentGroup = connection.Metadata.Group
			fmt.Fprintln(streams.Out, groupHeading(currentGroup, countConnectionsInGroup(results, currentGroup), opts.Language))
		}
		fmt.Fprintln(streams.Out, "  "+describeConnection(connection))
	}
	return nil
}

func set(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageError("usage: tunlite set <name> [-p port] [-i key] [-J jump] [-L/-R/-D ...] [-o Key=Value ...] [--clear identity|jump|options ...] [user@host] [--group group|--no-group]")
	}
	name := args[0]
	values, err := parseConnectionFlags("set", args[1:], true)
	if err != nil {
		return err
	}
	if values.noGroup && values.visited["group"] {
		return usageError("use a group value or clear the group, not both")
	}
	store, err := connectionStore(opts)
	if err != nil {
		return err
	}
	document, err := store.Load()
	if err != nil {
		return err
	}
	_, initial := configstore.Find(&document, name)
	if initial == nil {
		return notFoundError(fmt.Sprintf("no such connection: %s", name))
	}
	if initial.Spec.Driver != sshdriver.ID {
		return usageError(fmt.Sprintf("set arguments are not available for driver %q", initial.Spec.Driver))
	}
	patch, err := sshdriver.ParseSetArguments(values.driverArg)
	if err != nil {
		return usageError(err.Error())
	}
	if patch.Empty() && len(values.visited) == 0 {
		return usageError("nothing to set")
	}
	var conditions []condition
	apply := func(document *configstore.Document) (core.Connection, core.Connection, error) {
		_, connection := configstore.Find(document, name)
		if connection == nil {
			return core.Connection{}, core.Connection{}, notFoundError(fmt.Sprintf("no such connection: %s", name))
		}
		before := cloneConnection(*connection)
		updated, err := applyConnectionFlags(*connection, values, patch)
		if err != nil {
			return core.Connection{}, core.Connection{}, err
		}
		conditions = endpointConflicts(document, updated)
		*connection = updated
		return before, updated, nil
	}
	if opts.DryRun {
		before, updated, err := apply(&document)
		if err != nil {
			return err
		}
		conditions = append(conditions, setCollectionReplacementConditions(before, updated, patch)...)
		riskConditions, err := connectionRiskConditions(ctx, updated, opts.NonInteractive)
		if err != nil {
			return err
		}
		serviceConditions, err := previewUpdatedConnectionService(ctx, before, updated, store.Path(), opts)
		if err != nil {
			return err
		}
		conditions = append(conditions, serviceConditions...)
		conditions = append(conditions, riskConditions...)
		renderCommandProgress(streams, opts, append([]string{"set", name}, args[1:]...))
		return renderConnectionMutationWithConditions(streams, opts, "set", "planned", updated, conditions)
	}
	before, updated, err := apply(&document)
	if err != nil {
		return err
	}
	riskConditions, err := connectionRiskConditions(ctx, updated, opts.NonInteractive)
	if err != nil {
		return err
	}
	conditions = append(conditions, riskConditions...)
	conditions = append(conditions, setCollectionReplacementConditions(before, updated, patch)...)
	serviceConditions, err := previewUpdatedConnectionService(ctx, before, updated, store.Path(), opts)
	if err != nil {
		return err
	}
	conditions = append(conditions, serviceConditions...)
	renderCommandProgress(streams, opts, append([]string{"set", name}, args[1:]...))
	mutation, err := stageUpdatedConnectionService(ctx, before, updated, store.Path(), opts, streams)
	if err != nil {
		return err
	}
	defer mutation.finish()
	if err := store.Update(ctx, func(document *configstore.Document) error {
		_, current := configstore.Find(document, name)
		if current == nil {
			return notFoundError(fmt.Sprintf("no such connection: %s", name))
		}
		if !connectionsEqual(*current, before) {
			return fmt.Errorf("%w (%s)", configstore.ErrChanged, connectionDifference(*current, before))
		}
		*current = updated
		return nil
	}); err != nil {
		return mutation.rollbackAfter(classifyConcurrentChange(err))
	}
	if err := mutation.commit(); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		restoreErr := store.Update(recoveryCtx, func(document *configstore.Document) error {
			_, current := configstore.Find(document, name)
			if current == nil || !connectionsEqual(*current, updated) {
				return configstore.ErrChanged
			}
			*current = before
			return nil
		})
		return recoveryBlockedError(err, errors.Join(restoreErr, mutation.rollback()), mutation.actions)
	}
	conditions = append(conditions, mutation.conditions...)
	return renderConnectionMutationWithConditions(streams, opts, "set", "applied", updated, conditions)
}

func setCollectionReplacementConditions(before, after core.Connection, patch sshdriver.ConfigPatch) []condition {
	if !patch.ForwardsSet && !patch.SSHOptionsSet {
		return []condition{}
	}
	beforeConfig, beforeErr := sshdriver.DecodeConfig(before.Spec.Config)
	afterConfig, afterErr := sshdriver.DecodeConfig(after.Spec.Config)
	if beforeErr != nil || afterErr != nil {
		return []condition{}
	}
	result := make([]condition, 0, 2)
	if patch.ForwardsSet {
		result = append(result, condition{
			Code: "forwards_collection_replaced", Severity: "warning", Connection: before.Metadata.Name,
			Message: fmt.Sprintf("the complete forwards collection will be replaced (%d before, %d after); omitted forwards will be removed", len(beforeConfig.Forwards), len(afterConfig.Forwards)),
			Actions: []core.Action{},
		})
	}
	if patch.SSHOptionsSet {
		result = append(result, condition{
			Code: "ssh_options_collection_replaced", Severity: "warning", Connection: before.Metadata.Name,
			Message: fmt.Sprintf("the complete SSH options collection will be replaced (%d before, %d after); omitted options will be removed", len(beforeConfig.SSHOptions), len(afterConfig.SSHOptions)),
			Actions: []core.Action{},
		})
	}
	return result
}

func remove(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) != 1 {
		return usageError("usage: tunlite remove <name>")
	}
	name := args[0]
	store, err := connectionStore(opts)
	if err != nil {
		return err
	}
	removeFrom := func(document *configstore.Document) (core.Connection, error) {
		index, connection := configstore.Find(document, name)
		if connection == nil {
			return core.Connection{}, notFoundError(fmt.Sprintf("no such connection: %s", name))
		}
		removed := *connection
		remaining := make([]core.Connection, 0, len(document.Items)-1)
		remaining = append(remaining, document.Items[:index]...)
		remaining = append(remaining, document.Items[index+1:]...)
		document.Items = remaining
		return removed, nil
	}
	if opts.DryRun {
		document, err := store.Load()
		if err != nil {
			return err
		}
		removed, err := removeFrom(&document)
		if err != nil {
			return err
		}
		if err := checkConnectionRemovable(ctx, removed, store.Path(), opts); err != nil {
			return err
		}
		renderCommandProgress(streams, opts, []string{"remove", name})
		return renderConnectionMutation(streams, opts, "remove", "planned", removed)
	}
	document, err := store.Load()
	if err != nil {
		return err
	}
	_, current := configstore.Find(&document, name)
	if current == nil {
		return notFoundError(fmt.Sprintf("no such connection: %s", name))
	}
	removed := cloneConnection(*current)
	if err := checkConnectionRemovable(ctx, removed, store.Path(), opts); err != nil {
		return err
	}
	renderCommandProgress(streams, opts, []string{"remove", name})
	mutation, err := stageRemovedConnectionService(ctx, removed, store.Path(), opts)
	if err != nil {
		return err
	}
	defer mutation.finish()
	// Commit the removable service state before deleting the declaration. If
	// either this commit or the following config update fails, the staged
	// mutation can still restore the native service and its binding.
	if err := mutation.commit(); err != nil {
		return mutation.rollbackAfter(err)
	}
	if err := store.Update(ctx, func(document *configstore.Document) error {
		_, fresh := configstore.Find(document, name)
		if fresh == nil {
			return notFoundError(fmt.Sprintf("no such connection: %s", name))
		}
		if !connectionsEqual(*fresh, removed) {
			return configstore.ErrChanged
		}
		_, err := removeFrom(document)
		return err
	}); err != nil {
		return mutation.rollbackAfter(classifyConcurrentChange(err))
	}
	return renderConnectionMutation(streams, opts, "remove", "applied", removed)
}

func rename(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) != 2 {
		return usageError("usage: tunlite rename <old> <new>")
	}
	oldName, newName := args[0], args[1]
	store, err := connectionStore(opts)
	if err != nil {
		return err
	}
	renameIn := func(document *configstore.Document) (core.Connection, error) {
		_, connection := configstore.Find(document, oldName)
		if connection == nil {
			return core.Connection{}, notFoundError(fmt.Sprintf("no such connection: %s", oldName))
		}
		if oldName != newName {
			if _, existing := configstore.Find(document, newName); existing != nil {
				return core.Connection{}, conflictError(fmt.Sprintf("a connection named %q already exists", newName))
			}
		}
		updated := cloneConnection(*connection)
		updated.Metadata.Name = newName
		if err := core.ValidateConnection(updated); err != nil {
			return core.Connection{}, usageError(err.Error())
		}
		*connection = updated
		return updated, nil
	}
	if opts.DryRun {
		document, err := store.Load()
		if err != nil {
			return err
		}
		updated, err := renameIn(&document)
		if err != nil {
			return err
		}
		if err := previewRenamedConnectionService(oldName, newName, store.Path(), opts); err != nil {
			return err
		}
		identity, err := loadRenameIdentity(oldName)
		if err != nil {
			return err
		}
		renderCommandProgress(streams, opts, []string{"rename", oldName, newName})
		return renderRenameResult(streams, opts, "planned", updated, identity)
	}
	document, err := store.Load()
	if err != nil {
		return err
	}
	_, beforePtr := configstore.Find(&document, oldName)
	if beforePtr == nil {
		return notFoundError(fmt.Sprintf("no such connection: %s", oldName))
	}
	before := cloneConnection(*beforePtr)
	_, updated, err := renameInWithBefore(&document, oldName, newName)
	if err != nil {
		return err
	}
	if err := previewRenamedConnectionService(oldName, newName, store.Path(), opts); err != nil {
		return err
	}
	identity, err := loadRenameIdentity(oldName)
	if err != nil {
		return err
	}
	renderCommandProgress(streams, opts, []string{"rename", oldName, newName})
	mutation, err := stageRenamedConnectionService(ctx, oldName, newName, store.Path(), opts)
	if err != nil {
		return err
	}
	defer mutation.finish()
	// Move the service binding first. The mutation retains enough state to
	// restore the old binding if the declaration update loses a race or fails.
	if err := mutation.commit(); err != nil {
		return mutation.rollbackAfter(err)
	}
	if err := store.Update(ctx, func(document *configstore.Document) error {
		_, current := configstore.Find(document, oldName)
		if current == nil || !connectionsEqual(*current, before) {
			return configstore.ErrChanged
		}
		_, value, err := renameInWithBefore(document, oldName, newName)
		updated = value
		return err
	}); err != nil {
		return mutation.rollbackAfter(classifyConcurrentChange(err))
	}
	return renderRenameResult(streams, opts, "applied", updated, identity)
}

type renameIdentity struct {
	NativeIdentity string `json:"nativeIdentity"`
	ServiceKey     string `json:"serviceKey"`
	UnitName       string `json:"unitName"`
}

func loadRenameIdentity(connection string) (renameIdentity, error) {
	records, err := serviceRecordStore()
	if err != nil {
		return renameIdentity{}, err
	}
	record, exists, err := records.Load(connection)
	if err != nil || !exists {
		return renameIdentity{}, err
	}
	serviceKey := record.ServiceKey
	if serviceKey == "" {
		serviceKey = record.Connection
	}
	nativeIdentity := record.UnitName
	if nativeIdentity == "" {
		nativeIdentity = serviceKey
	}
	return renameIdentity{NativeIdentity: nativeIdentity, ServiceKey: serviceKey, UnitName: record.UnitName}, nil
}

func renderRenameResult(streams IO, opts options, outcome string, connection core.Connection, identity renameIdentity) error {
	if opts.JSON {
		return writeResponse(streams.Out, map[string]any{
			"outcome": outcome, "operation": "rename", "connection": connection,
			"connectionName": connection.Metadata.Name, "nativeIdentity": identity.NativeIdentity,
			"serviceKey": identity.ServiceKey, "unitName": identity.UnitName,
			"conditions": []condition{}, "dryRun": opts.DryRun,
		})
	}
	verb := localized(opts.Language, "renamed", "已重命名")
	if opts.DryRun {
		verb = localized(opts.Language, "would be renamed", "将重命名")
	}
	fmt.Fprintf(streams.Out, "%s %q\n", verb, connection.Metadata.Name)
	fmt.Fprintf(streams.Out, "  %s  %s\n", localized(opts.Language, "Connection name", "Connection 名称"), connection.Metadata.Name)
	fmt.Fprintf(streams.Out, "  %s  %s\n", localized(opts.Language, "Native identity", "原生服务标识"), displayIdentityValue(identity.NativeIdentity))
	fmt.Fprintf(streams.Out, "  %s  %s\n", localized(opts.Language, "Service key", "Service Key"), displayIdentityValue(identity.ServiceKey))
	fmt.Fprintf(streams.Out, "  %s  %s\n", localized(opts.Language, "Unit name", "Unit 名称"), displayIdentityValue(identity.UnitName))
	fmt.Fprintln(streams.Out, localized(opts.Language,
		"  Native identity is unchanged, so the running service is not interrupted.",
		"  原生服务标识保持不变，因此不会中断正在运行的服务。"))
	return nil
}

func displayIdentityValue(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func renameInWithBefore(document *configstore.Document, oldName, newName string) (core.Connection, core.Connection, error) {
	_, connection := configstore.Find(document, oldName)
	if connection == nil {
		return core.Connection{}, core.Connection{}, notFoundError(fmt.Sprintf("no such connection: %s", oldName))
	}
	before := cloneConnection(*connection)
	if oldName != newName {
		if _, existing := configstore.Find(document, newName); existing != nil {
			return core.Connection{}, core.Connection{}, conflictError(fmt.Sprintf("a connection named %q already exists", newName))
		}
	}
	updated := cloneConnection(*connection)
	updated.Metadata.Name = newName
	if err := core.ValidateConnection(updated); err != nil {
		return core.Connection{}, core.Connection{}, usageError(err.Error())
	}
	*connection = updated
	return before, updated, nil
}

func parseConnectionFlags(command string, args []string, allowClear bool) (connectionFlags, error) {
	values := connectionFlags{visited: make(map[string]bool), driverArg: make([]string, 0, len(args))}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		value := func(name string) (string, error) {
			if prefix := "--" + name + "="; strings.HasPrefix(argument, prefix) {
				return strings.TrimPrefix(argument, prefix), nil
			}
			if index+1 >= len(args) {
				return "", usageError(fmt.Sprintf("--%s requires a value", name))
			}
			index++
			return args[index], nil
		}
		switch {
		case argument == "--driver" || strings.HasPrefix(argument, "--driver="):
			if command != "add" {
				return connectionFlags{}, usageError("set cannot change a Connection driver; create a new Connection instead")
			}
			if values.visited["driver"] {
				return connectionFlags{}, usageError("--driver may only be specified once")
			}
			selected, err := value("driver")
			if err != nil {
				return connectionFlags{}, err
			}
			if selected == "" {
				return connectionFlags{}, usageError("--driver requires a value")
			}
			values.driver, values.visited["driver"] = selected, true
		case argument == "--group" || strings.HasPrefix(argument, "--group="):
			if values.visited["group"] {
				return connectionFlags{}, usageError("--group may only be specified once")
			}
			group, err := value("group")
			if err != nil {
				return connectionFlags{}, err
			}
			values.group, values.visited["group"] = group, true
		case argument == "--no-group":
			if !allowClear {
				return connectionFlags{}, usageError("--no-group is only available with set")
			}
			if values.noGroup {
				return connectionFlags{}, usageError("--no-group may only be specified once")
			}
			values.noGroup, values.visited["no-group"] = true, true
		default:
			values.driverArg = append(values.driverArg, argument)
		}
	}
	return values, nil
}

func newSSHConnection(name string, config sshdriver.Config, values connectionFlags) (core.Connection, error) {
	group, err := connectionGroup(values)
	if err != nil {
		return core.Connection{}, err
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return core.Connection{}, err
	}
	connection := core.Connection{
		ConfigVersion: core.ConfigVersion,
		Kind:          core.Kind,
		Metadata:      core.Metadata{Name: name, Group: group},
		Spec: core.ConnectionSpec{
			Driver: sshdriver.ID, Config: raw,
		},
	}
	if err := core.ValidateConnection(connection); err != nil {
		return core.Connection{}, usageError(err.Error())
	}
	return connection, nil
}

func applyConnectionFlags(connection core.Connection, values connectionFlags, patch sshdriver.ConfigPatch) (core.Connection, error) {
	if connection.Spec.Driver != sshdriver.ID {
		return core.Connection{}, usageError(fmt.Sprintf("set arguments are not available for driver %q", connection.Spec.Driver))
	}
	updated := cloneConnection(connection)
	config, err := sshdriver.DecodeConfig(updated.Spec.Config)
	if err != nil {
		return core.Connection{}, err
	}
	config = sshdriver.ApplyPatch(config, patch)
	if values.visited["group"] {
		group, err := connectionGroup(values)
		if err != nil {
			return core.Connection{}, err
		}
		updated.Metadata.Group = group
	} else if values.noGroup {
		updated.Metadata.Group = ""
	}
	if err := sshdriver.Validate(config); err != nil {
		return core.Connection{}, usageError(err.Error())
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return core.Connection{}, err
	}
	updated.Spec.Config = raw
	return updated, nil
}

func parseGroup(value string) (string, error) {
	group := strings.TrimSpace(value)
	if group == "" || len(group) > 63 {
		return "", usageError(fmt.Sprintf("invalid group %q", value))
	}
	for _, char := range group {
		if !(unicode.IsLetter(char) || unicode.IsDigit(char) || char == '.' || char == '_' || char == '-') || char > unicode.MaxASCII {
			return "", usageError(fmt.Sprintf("invalid group %q (use ASCII letters, digits, . _ -)", group))
		}
	}
	return group, nil
}

func connectionGroup(values connectionFlags) (string, error) {
	if values.visited["group"] {
		return parseGroup(values.group)
	}
	return "", nil
}

func connectionRiskConditions(ctx context.Context, connection core.Connection, nonInteractive bool) ([]condition, error) {
	registry, err := core.NewRegistry(sshdriver.New())
	if err != nil {
		return nil, err
	}
	executionPlan, err := core.NewPlanner(registry).Plan(ctx, connection, core.PlanOptions{NonInteractive: nonInteractive})
	if err != nil {
		return nil, err
	}
	return planRiskConditions(connection, executionPlan), nil
}

func renderConnectionMutation(streams IO, opts options, operation, outcome string, connection core.Connection) error {
	return renderConnectionMutationWithConditions(streams, opts, operation, outcome, connection, nil)
}

func renderConnectionMutationWithConditions(streams IO, opts options, operation, outcome string, connection core.Connection, conditions []condition) error {
	if conditions == nil {
		conditions = []condition{}
	}
	conditions = deduplicateConditions(conditions)
	if opts.JSON {
		return writeResponse(streams.Out, map[string]any{
			"outcome":    outcome,
			"operation":  operation,
			"connection": connection,
			"conditions": conditions,
			"dryRun":     opts.DryRun,
		})
	}
	verb := map[string]string{
		"add": "added", "set": "updated", "remove": "removed", "rename": "renamed",
		"manage": "managed", "unmanage": "unmanaged", "resolve": "resolved",
	}[operation]
	if isChinese(opts.Language) {
		verb = map[string]string{
			"add": "已添加", "set": "已更新", "remove": "已删除", "rename": "已重命名",
			"manage": "已接管", "unmanage": "已停止管理", "resolve": "已处理",
		}[operation]
		if opts.DryRun {
			verb = "将" + verb
		}
	} else if opts.DryRun {
		verb = "would be " + verb
	}
	fmt.Fprintf(streams.Out, "%s %q\n", verb, connection.Metadata.Name)
	for _, item := range conditions {
		fmt.Fprintf(streams.Err, "%s%s\n", localized(opts.Language, "warning: ", "警告："), localizedMutationCondition(item, opts.Language))
	}
	return nil
}

func deduplicateConditions(conditions []condition) []condition {
	if len(conditions) < 2 {
		return conditions
	}
	result := make([]condition, 0, len(conditions))
	seen := make(map[string]struct{}, len(conditions))
	for _, item := range conditions {
		identity, err := json.Marshal(item)
		if err != nil {
			// An action payload that cannot be represented in the public response
			// cannot be compared safely. Preserve it rather than risk hiding a
			// distinct condition.
			result = append(result, item)
			continue
		}
		key := string(identity)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	return result
}

func localizedMutationCondition(item condition, lang language) string {
	if !isChinese(lang) {
		return item.Message
	}
	switch item.Code {
	case "connection_restart_required":
		return "应用该配置时会重启正在运行的原生后台服务。"
	case "endpoint_conflict":
		if len(item.Actions) > 0 {
			endpoint := fmt.Sprint(item.Actions[0].Parameters["endpoint"])
			other := fmt.Sprint(item.Actions[0].Parameters["otherConnection"])
			return fmt.Sprintf("端点 %s 也由连接“%s”声明。", endpoint, other)
		}
	case "network_exposure":
		message := strings.TrimPrefix(item.Message, "connection ")
		message = strings.ReplaceAll(message, "publishes a remote endpoint", "会公开远程端点")
		message = strings.ReplaceAll(message, "listens outside the loopback interface", "会在非回环网络接口上监听")
		message = strings.ReplaceAll(message, " and ", "，并且")
		return "该连接" + message + "。"
	case "foreground_only":
		return "当前系统无法提供可用的原生后台服务；该连接只能使用 tunlite run。"
	}
	return "" + item.Message
}

func endpointConflicts(document *configstore.Document, candidate core.Connection) []condition {
	mine := endpointKeys(candidate)
	if len(mine) == 0 {
		return []condition{}
	}
	var conflicts []condition
	for _, other := range document.Items {
		if other.Metadata.Name == candidate.Metadata.Name {
			continue
		}
		for key := range endpointKeys(other) {
			if _, exists := mine[key]; !exists {
				continue
			}
			conflicts = append(conflicts, condition{
				Code:       "endpoint_conflict",
				Severity:   "warning",
				Message:    fmt.Sprintf("endpoint %s is also declared by connection %q", key, other.Metadata.Name),
				Connection: candidate.Metadata.Name,
				Driver:     candidate.Spec.Driver,
				Actions: []core.Action{{
					Type:       "change_forward",
					Parameters: map[string]any{"endpoint": key, "otherConnection": other.Metadata.Name},
				}},
			})
		}
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Message < conflicts[j].Message })
	return conflicts
}

func endpointKeys(connection core.Connection) map[string]struct{} {
	if connection.Spec.Driver != sshdriver.ID {
		return nil
	}
	config, err := sshdriver.DecodeConfig(connection.Spec.Config)
	if err != nil {
		return nil
	}
	keys := make(map[string]struct{}, len(config.Forwards))
	for _, forward := range config.Forwards {
		bind := defaultBind(forward.Bind)
		key := fmt.Sprintf("L:%s:%d", bind, forward.SrcPort)
		if forward.Type == "remote" {
			key = fmt.Sprintf("R:%s:%s:%d", config.Host, bind, forward.SrcPort)
		}
		keys[key] = struct{}{}
	}
	return keys
}

func describeConnection(connection core.Connection) string {
	if connection.Spec.Driver != sshdriver.ID {
		return fmt.Sprintf("%s\t%s", connection.Metadata.Name, connection.Spec.Driver)
	}
	config, err := sshdriver.DecodeConfig(connection.Spec.Config)
	if err != nil {
		return fmt.Sprintf("%s\t%s\t(invalid config)", connection.Metadata.Name, connection.Spec.Driver)
	}
	parts := make([]string, 0, len(config.Forwards))
	for _, forward := range config.Forwards {
		switch forward.Type {
		case "dynamic":
			parts = append(parts, fmt.Sprintf("D:%s:%d", defaultBind(forward.Bind), forward.SrcPort))
		case "remote":
			parts = append(parts, fmt.Sprintf("R:%s:%d→%s:%d", defaultBind(forward.Bind), forward.SrcPort, forward.DestHost, forward.DestPort))
		default:
			parts = append(parts, fmt.Sprintf("L:%s:%d→%s:%d", defaultBind(forward.Bind), forward.SrcPort, forward.DestHost, forward.DestPort))
		}
	}
	return fmt.Sprintf("%s\t%s\t%s", connection.Metadata.Name, config.Host, strings.Join(parts, " "))
}

func connectionLess(left, right core.Connection) bool {
	leftGroup, rightGroup := left.Metadata.Group, right.Metadata.Group
	if leftGroup == rightGroup {
		return left.Metadata.Name < right.Metadata.Name
	}
	if leftGroup == "" {
		return false
	}
	if rightGroup == "" {
		return true
	}
	return leftGroup < rightGroup
}

func groupLabel(group string, lang language) string {
	if group == "" {
		return localized(lang, "Ungrouped", "未分组")
	}
	return group
}

func groupHeading(group string, count int, lang language) string {
	if isChinese(lang) {
		return fmt.Sprintf("分组 %s · %d 个连接", groupLabel(group, lang), count)
	}
	unit := "connections"
	if count == 1 {
		unit = "connection"
	}
	return fmt.Sprintf("Group %s · %d %s", groupLabel(group, lang), count, unit)
}

func countConnectionsInGroup(connections []core.Connection, group string) int {
	count := 0
	for _, connection := range connections {
		if connection.Metadata.Group == group {
			count++
		}
	}
	return count
}

func defaultBind(value string) string {
	if value == "" {
		return "127.0.0.1"
	}
	return value
}

func cloneConnection(connection core.Connection) core.Connection {
	connection.Spec.Config = append(json.RawMessage(nil), connection.Spec.Config...)
	return connection
}

func connectionsEqual(left, right core.Connection) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}

func connectionDifference(left, right core.Connection) string {
	var fields []string
	if left.ConfigVersion != right.ConfigVersion {
		fields = append(fields, "configVersion")
	}
	if left.Kind != right.Kind {
		fields = append(fields, "kind")
	}
	if left.Metadata.Name != right.Metadata.Name {
		fields = append(fields, "metadata.name")
	}
	if left.Metadata.Group != right.Metadata.Group {
		fields = append(fields, "metadata.group")
	}
	if left.Spec.Driver != right.Spec.Driver {
		fields = append(fields, "spec.driver")
	}
	if !bytes.Equal(left.Spec.Config, right.Spec.Config) {
		fields = append(fields, "spec.config")
	}
	if len(fields) == 0 {
		return "serialization"
	}
	return strings.Join(fields, ", ")
}
