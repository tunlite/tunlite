package cli

import (
	"context"
	"encoding/json"
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

const tagLabelPrefix = "tag.tunlite.dev/"

type connectionFlags struct {
	to           string
	identity     string
	jump         string
	local        stringsFlag
	remote       stringsFlag
	dynamic      stringsFlag
	sshOptions   stringsFlag
	tags         stringsFlag
	noTags       bool
	noSSHOptions bool
	visited      map[string]bool
}

func add(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageError("usage: tunlite add <name> --to user@host[:port] -L/-R/-D ...")
	}
	name := args[0]
	values, err := parseConnectionFlags("add", args[1:], false)
	if err != nil {
		return err
	}
	if values.to == "" {
		return usageError("add requires --to user@host[:port]")
	}
	connection, err := newSSHConnection(name, values)
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
		return renderConnectionMutation(streams, opts, "add", "planned", connection)
	}
	if err := store.Update(ctx, func(document *configstore.Document) error {
		if _, existing := configstore.Find(document, name); existing != nil {
			return conflictError(fmt.Sprintf("a connection named %q already exists", name))
		}
		document.Items = append(document.Items, connection)
		return nil
	}); err != nil {
		return err
	}
	return renderConnectionMutation(streams, opts, "add", "applied", connection)
}

func list(args []string, streams IO, opts options) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var rawTags stringsFlag
	flags.Var(&rawTags, "tag", "filter by tag")
	if err := flags.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError(fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}
	tags, err := parseTags(rawTags)
	if err != nil {
		return err
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
		if len(tags) == 0 || hasAnyTag(connection.Metadata.Labels, tags) {
			results = append(results, connection)
		}
	}
	if len(tags) > 0 && len(results) == 0 {
		return notFoundError(fmt.Sprintf("no connections tagged %s", quoteList(tags)))
	}
	if opts.JSON {
		return writeJSON(streams.Out, map[string]any{
			"contractVersion": ContractVersion,
			"outcome":         "succeeded",
			"results":         results,
		})
	}
	if len(results) == 0 {
		fmt.Fprintln(streams.Out, "no connections defined")
		return nil
	}
	for _, connection := range results {
		fmt.Fprintln(streams.Out, describeConnection(connection))
	}
	return nil
}

func set(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageError("usage: tunlite set <name> [--to ...] [-L/-R/-D ...]")
	}
	name := args[0]
	values, err := parseConnectionFlags("set", args[1:], true)
	if err != nil {
		return err
	}
	if len(values.visited) == 0 {
		return usageError("nothing to set")
	}
	if values.noTags && values.visited["tag"] {
		return usageError("use --tag or --no-tags, not both")
	}
	if values.noSSHOptions && values.visited["ssh-opt"] {
		return usageError("use --ssh-opt or --no-ssh-options, not both")
	}
	store, err := connectionStore(opts)
	if err != nil {
		return err
	}
	apply := func(document *configstore.Document) (core.Connection, error) {
		_, connection := configstore.Find(document, name)
		if connection == nil {
			return core.Connection{}, notFoundError(fmt.Sprintf("no such connection: %s", name))
		}
		updated, err := applyConnectionFlags(*connection, values)
		if err != nil {
			return core.Connection{}, err
		}
		*connection = updated
		return updated, nil
	}
	if opts.DryRun {
		document, err := store.Load()
		if err != nil {
			return err
		}
		updated, err := apply(&document)
		if err != nil {
			return err
		}
		return renderConnectionMutation(streams, opts, "set", "planned", updated)
	}
	var updated core.Connection
	if err := store.Update(ctx, func(document *configstore.Document) error {
		var err error
		updated, err = apply(document)
		return err
	}); err != nil {
		return err
	}
	return renderConnectionMutation(streams, opts, "set", "applied", updated)
}

func remove(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) != 1 {
		return usageError("usage: tunlite rm <name>")
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
		return renderConnectionMutation(streams, opts, "rm", "planned", removed)
	}
	var removed core.Connection
	if err := store.Update(ctx, func(document *configstore.Document) error {
		var err error
		removed, err = removeFrom(document)
		return err
	}); err != nil {
		return err
	}
	return renderConnectionMutation(streams, opts, "rm", "applied", removed)
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
		return renderConnectionMutation(streams, opts, "rename", "planned", updated)
	}
	var updated core.Connection
	if err := store.Update(ctx, func(document *configstore.Document) error {
		var err error
		updated, err = renameIn(document)
		return err
	}); err != nil {
		return err
	}
	return renderConnectionMutation(streams, opts, "rename", "applied", updated)
}

func parseConnectionFlags(command string, args []string, allowClear bool) (connectionFlags, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var values connectionFlags
	flags.StringVar(&values.to, "to", "", "SSH target")
	flags.StringVar(&values.identity, "i", "", "identity file")
	flags.StringVar(&values.jump, "jump", "", "comma-separated jump hosts")
	flags.Var(&values.local, "L", "local forward")
	flags.Var(&values.remote, "R", "remote forward")
	flags.Var(&values.dynamic, "D", "dynamic forward")
	flags.Var(&values.sshOptions, "ssh-opt", "OpenSSH Key=Value option")
	flags.Var(&values.tags, "tag", "connection tag")
	if allowClear {
		flags.BoolVar(&values.noTags, "no-tags", false, "clear tags")
		flags.BoolVar(&values.noSSHOptions, "no-ssh-options", false, "clear SSH options")
	}
	if err := flags.Parse(args); err != nil {
		return connectionFlags{}, usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return connectionFlags{}, usageError(fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}
	values.visited = make(map[string]bool)
	flags.Visit(func(item *flag.Flag) { values.visited[item.Name] = true })
	return values, nil
}

func newSSHConnection(name string, values connectionFlags) (core.Connection, error) {
	host, port, err := sshdriver.ParseTarget(values.to)
	if err != nil {
		return core.Connection{}, usageError(err.Error())
	}
	forwards, err := parseForwards(values)
	if err != nil {
		return core.Connection{}, err
	}
	if len(forwards) == 0 {
		return core.Connection{}, usageError("add needs at least one forward (-L / -R / -D)")
	}
	jump, err := sshdriver.ParseJump(values.jump)
	if err != nil {
		return core.Connection{}, usageError(err.Error())
	}
	tags, err := parseTags(values.tags)
	if err != nil {
		return core.Connection{}, err
	}
	config := sshdriver.Config{
		Host: host, Port: port, IdentityFile: values.identity, Jump: jump,
		Forwards: forwards, SSHOptions: append([]string(nil), values.sshOptions...),
	}
	if err := sshdriver.Validate(config); err != nil {
		return core.Connection{}, usageError(err.Error())
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return core.Connection{}, err
	}
	connection := core.Connection{
		APIVersion: core.APIVersion,
		Kind:       core.Kind,
		Metadata:   core.Metadata{Name: name, Labels: labelsForTags(tags)},
		Spec: core.ConnectionSpec{
			Driver: sshdriver.ID, Lifecycle: "persistent", ConfigVersion: 1, Config: raw,
		},
	}
	if err := core.ValidateConnection(connection); err != nil {
		return core.Connection{}, usageError(err.Error())
	}
	return connection, nil
}

func applyConnectionFlags(connection core.Connection, values connectionFlags) (core.Connection, error) {
	if connection.Spec.Driver != sshdriver.ID {
		return core.Connection{}, usageError(fmt.Sprintf("set flags are not available for driver %q", connection.Spec.Driver))
	}
	if connection.Spec.ConfigVersion != 1 {
		return core.Connection{}, usageError(fmt.Sprintf("unsupported SSH configVersion %d", connection.Spec.ConfigVersion))
	}
	updated := cloneConnection(connection)
	config, err := sshdriver.DecodeConfig(updated.Spec.Config)
	if err != nil {
		return core.Connection{}, err
	}
	if values.visited["to"] {
		host, port, err := sshdriver.ParseTarget(values.to)
		if err != nil {
			return core.Connection{}, usageError(err.Error())
		}
		config.Host, config.Port = host, port
	}
	if values.visited["i"] {
		config.IdentityFile = values.identity
	}
	if values.visited["jump"] {
		jump, err := sshdriver.ParseJump(values.jump)
		if err != nil {
			return core.Connection{}, usageError(err.Error())
		}
		config.Jump = jump
	}
	if values.visited["ssh-opt"] {
		config.SSHOptions = append([]string(nil), values.sshOptions...)
	} else if values.noSSHOptions {
		config.SSHOptions = []string{}
	}
	if values.visited["L"] || values.visited["R"] || values.visited["D"] {
		forwards, err := parseForwards(values)
		if err != nil {
			return core.Connection{}, err
		}
		config.Forwards = forwards
	}
	if values.visited["tag"] {
		tags, err := parseTags(values.tags)
		if err != nil {
			return core.Connection{}, err
		}
		updated.Metadata.Labels = replaceTags(updated.Metadata.Labels, tags)
	} else if values.noTags {
		updated.Metadata.Labels = replaceTags(updated.Metadata.Labels, nil)
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

func parseForwards(values connectionFlags) ([]sshdriver.Forward, error) {
	forwards := make([]sshdriver.Forward, 0, len(values.local)+len(values.remote)+len(values.dynamic))
	for _, item := range []struct {
		flag   string
		values []string
	}{{"-L", values.local}, {"-R", values.remote}, {"-D", values.dynamic}} {
		for _, value := range item.values {
			forward, err := sshdriver.ParseForward(item.flag, value)
			if err != nil {
				return nil, usageError(err.Error())
			}
			forwards = append(forwards, forward)
		}
	}
	return forwards, nil
}

func parseTags(values []string) ([]string, error) {
	seen := make(map[string]struct{})
	var tags []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			tag := strings.TrimSpace(part)
			if tag == "" {
				continue
			}
			if len(tag) > 63 {
				return nil, usageError(fmt.Sprintf("invalid tag %q", tag))
			}
			for _, char := range tag {
				if !(unicode.IsLetter(char) || unicode.IsDigit(char) || char == '.' || char == '_' || char == '-') || char > unicode.MaxASCII {
					return nil, usageError(fmt.Sprintf("invalid tag %q (use ASCII letters, digits, . _ -)", tag))
				}
			}
			if _, exists := seen[tag]; !exists {
				seen[tag] = struct{}{}
				tags = append(tags, tag)
			}
		}
	}
	return tags, nil
}

func labelsForTags(tags []string) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	labels := make(map[string]string, len(tags))
	for _, tag := range tags {
		labels[tagLabelPrefix+tag] = "true"
	}
	return labels
}

func replaceTags(labels map[string]string, tags []string) map[string]string {
	result := make(map[string]string)
	for key, value := range labels {
		if !strings.HasPrefix(key, tagLabelPrefix) {
			result[key] = value
		}
	}
	for key, value := range labelsForTags(tags) {
		result[key] = value
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func tagsFromLabels(labels map[string]string) []string {
	var tags []string
	for key := range labels {
		if tag, found := strings.CutPrefix(key, tagLabelPrefix); found {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags
}

func hasAnyTag(labels map[string]string, tags []string) bool {
	for _, tag := range tags {
		if labels[tagLabelPrefix+tag] == "true" {
			return true
		}
	}
	return false
}

func renderConnectionMutation(streams IO, opts options, operation, outcome string, connection core.Connection) error {
	if opts.JSON {
		return writeJSON(streams.Out, map[string]any{
			"contractVersion": ContractVersion,
			"outcome":         outcome,
			"operation":       operation,
			"connection":      connection,
			"dryRun":          opts.DryRun,
		})
	}
	verb := map[string]string{"add": "added", "set": "updated", "rm": "removed", "rename": "renamed"}[operation]
	if opts.DryRun {
		verb = "would be " + verb
	}
	fmt.Fprintf(streams.Out, "%s %q\n", verb, connection.Metadata.Name)
	return nil
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
	tagText := ""
	if tags := tagsFromLabels(connection.Metadata.Labels); len(tags) > 0 {
		tagText = " [" + strings.Join(tags, ",") + "]"
	}
	return fmt.Sprintf("%s\t%s\t%s%s", connection.Metadata.Name, config.Host, strings.Join(parts, " "), tagText)
}

func defaultBind(value string) string {
	if value == "" {
		return "127.0.0.1"
	}
	return value
}

func cloneConnection(connection core.Connection) core.Connection {
	connection.Spec.Config = append(json.RawMessage(nil), connection.Spec.Config...)
	if connection.Metadata.Labels != nil {
		connection.Metadata.Labels = make(map[string]string, len(connection.Metadata.Labels))
		for key, value := range connection.Metadata.Labels {
			connection.Metadata.Labels[key] = value
		}
	}
	return connection
}

func quoteList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = fmt.Sprintf("%q", value)
	}
	return strings.Join(quoted, ", ")
}
