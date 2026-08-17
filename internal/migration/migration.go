package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/configstore"
	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
)

var (
	ErrDrifted     = errors.New("configuration changed after migration")
	ErrNoMigration = errors.New("no migration journal exists for this configuration")
	validLegacyTag = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

type Report struct {
	Outcome       string           `json:"outcome"`
	Result        string           `json:"result"`
	Operation     string           `json:"operation"`
	SourceVersion string           `json:"sourceVersion,omitempty"`
	Path          string           `json:"path"`
	BackupPath    string           `json:"backupPath,omitempty"`
	JournalPath   string           `json:"journalPath,omitempty"`
	Connections   int              `json:"connections"`
	DryRun        bool             `json:"dryRun"`
	Conditions    []core.Condition `json:"conditions"`
}

type Journal struct {
	Version        int       `json:"version"`
	Status         string    `json:"status"`
	ConfigPath     string    `json:"configPath"`
	BackupPath     string    `json:"backupPath"`
	SourceVersion  string    `json:"sourceVersion"`
	BeforeSHA256   string    `json:"beforeSHA256"`
	MigratedSHA256 string    `json:"migratedSHA256"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type legacyConfig struct {
	Version  int             `json:"version"`
	Settings json.RawMessage `json:"settings"`
	Tunnels  []legacyTunnel  `json:"tunnels"`
}

type legacyTunnel struct {
	Name         string          `json:"name"`
	Host         string          `json:"host"`
	Port         int             `json:"port"`
	IdentityFile *string         `json:"identityFile"`
	Jump         json.RawMessage `json:"jump"`
	Tags         json.RawMessage `json:"tags"`
	Forwards     []legacyForward `json:"forwards"`
	SSHOptions   []string        `json:"sshOptions"`
	Enabled      *bool           `json:"enabled"`
	AutoSetupKey *bool           `json:"autoSetupKey"`
}

type legacyForward struct {
	Type     string `json:"type"`
	Bind     string `json:"bind"`
	SrcPort  int    `json:"srcPort"`
	DestHost string `json:"destHost"`
	DestPort int    `json:"destPort"`
}

func Migrate(ctx context.Context, configPath, stateDir string, dryRun bool) (Report, error) {
	configPath, err := filepath.Abs(configPath)
	if err != nil {
		return Report{}, err
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return Report{}, fmt.Errorf("read legacy config %s: %w", configPath, err)
	}
	journalPath := journalFile(stateDir, configPath)
	if isV1(raw) {
		return finishInterruptedJournal(configPath, journalPath, raw, dryRun)
	}
	legacy, err := decodeLegacy(raw)
	if err != nil {
		return Report{}, err
	}
	document, conditions, err := convert(legacy)
	if err != nil {
		return Report{}, err
	}
	replacement, err := configstore.Encode(document)
	if err != nil {
		return Report{}, fmt.Errorf("encode migrated config: %w", err)
	}
	beforeHash := digest(raw)
	afterHash := digest(replacement)
	backupPath := fmt.Sprintf("%s.v0-sha256-%s.bak", configPath, beforeHash[:12])
	report := Report{
		Outcome: "planned", Result: "migration", Operation: "migrate", SourceVersion: "0.x/config-v1",
		Path: configPath, BackupPath: backupPath, JournalPath: journalPath,
		Connections: len(document.Items), DryRun: dryRun, Conditions: conditions,
	}
	if dryRun {
		return report, nil
	}
	journal := Journal{
		Version: 1, Status: "prepared", ConfigPath: configPath, BackupPath: backupPath,
		SourceVersion: report.SourceVersion, BeforeSHA256: beforeHash,
		MigratedSHA256: afterHash, UpdatedAt: time.Now().UTC(),
	}
	prepare := func() error {
		if err := preserveBackup(backupPath, raw); err != nil {
			return err
		}
		return writeJournal(journalPath, journal)
	}
	if err := configstore.New(configPath).ReplaceRawIfUnchanged(ctx, raw, replacement, prepare); err != nil {
		if errors.Is(err, configstore.ErrChanged) {
			return Report{}, ErrDrifted
		}
		return Report{}, err
	}
	journal.Status = "applied"
	journal.UpdatedAt = time.Now().UTC()
	if err := writeJournal(journalPath, journal); err != nil {
		return Report{}, fmt.Errorf("migration applied but journal finalization failed: %w", err)
	}
	report.Outcome = "applied"
	report.Result = "migrated"
	return report, nil
}

func Rollback(ctx context.Context, configPath, stateDir string, dryRun bool) (Report, error) {
	configPath, err := filepath.Abs(configPath)
	if err != nil {
		return Report{}, err
	}
	journalPath := journalFile(stateDir, configPath)
	journal, err := readJournal(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return Report{}, ErrNoMigration
	}
	if err != nil {
		return Report{}, err
	}
	if journal.Version != 1 || journal.ConfigPath != configPath {
		return Report{}, errors.New("migration journal does not belong to this configuration")
	}
	current, err := os.ReadFile(configPath)
	if err != nil {
		return Report{}, fmt.Errorf("read current config: %w", err)
	}
	backup, err := os.ReadFile(journal.BackupPath)
	if err != nil {
		return Report{}, fmt.Errorf("read migration backup: %w", err)
	}
	if digest(backup) != journal.BeforeSHA256 {
		return Report{}, errors.New("migration backup checksum does not match the journal")
	}
	report := Report{
		Outcome: "planned", Result: "rollback", Operation: "rollback", SourceVersion: journal.SourceVersion,
		Path: configPath, BackupPath: journal.BackupPath, JournalPath: journalPath,
		DryRun: dryRun, Conditions: []core.Condition{},
	}
	if digest(current) == journal.BeforeSHA256 {
		report.Outcome = "succeeded"
		report.Result = "already-rolled-back"
		if journal.Status != "rolled_back" && !dryRun {
			journal.Status = "rolled_back"
			journal.UpdatedAt = time.Now().UTC()
			if err := writeJournal(journalPath, journal); err != nil {
				return Report{}, err
			}
		}
		return report, nil
	}
	if digest(current) != journal.MigratedSHA256 {
		return Report{}, ErrDrifted
	}
	if dryRun {
		return report, nil
	}
	if err := configstore.New(configPath).ReplaceRawIfUnchanged(ctx, current, backup, nil); err != nil {
		if errors.Is(err, configstore.ErrChanged) {
			return Report{}, ErrDrifted
		}
		return Report{}, err
	}
	journal.Status = "rolled_back"
	journal.UpdatedAt = time.Now().UTC()
	if err := writeJournal(journalPath, journal); err != nil {
		return Report{}, fmt.Errorf("rollback applied but journal finalization failed: %w", err)
	}
	report.Outcome = "applied"
	report.Result = "rolled-back"
	return report, nil
}

func decodeLegacy(raw []byte) (legacyConfig, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var legacy legacyConfig
	if err := decoder.Decode(&legacy); err != nil {
		return legacyConfig{}, fmt.Errorf("decode legacy config: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return legacyConfig{}, fmt.Errorf("decode legacy config: %w", err)
	}
	if legacy.Version != 1 {
		return legacyConfig{}, fmt.Errorf("unsupported legacy config version %d", legacy.Version)
	}
	if legacy.Tunnels == nil {
		return legacyConfig{}, errors.New("legacy config tunnels must be an array")
	}
	if len(legacy.Settings) > 0 && string(legacy.Settings) != "null" {
		var settings map[string]json.RawMessage
		if err := json.Unmarshal(legacy.Settings, &settings); err != nil || settings == nil {
			return legacyConfig{}, errors.New("legacy config settings must be an object")
		}
	}
	return legacy, nil
}

func convert(legacy legacyConfig) (configstore.Document, []core.Condition, error) {
	document := configstore.Empty()
	conditions := make([]core.Condition, 0)
	if hasObjectValues(legacy.Settings) {
		conditions = append(conditions, core.Condition{
			Code: "legacy_settings_not_migrated", Severity: "warning",
			Message: "0.x 的全局守护进程、重试和通知设置不属于 v1 配置，未迁移",
			Actions: []core.Action{{Type: "review_migration_notes", Parameters: map[string]any{"section": "legacy-settings"}}},
		})
	}
	seen := make(map[string]struct{}, len(legacy.Tunnels))
	for index, tunnel := range legacy.Tunnels {
		if _, exists := seen[tunnel.Name]; exists {
			return configstore.Document{}, nil, fmt.Errorf("duplicate legacy tunnel name %q", tunnel.Name)
		}
		seen[tunnel.Name] = struct{}{}
		connection, err := convertTunnel(tunnel)
		if err != nil {
			return configstore.Document{}, nil, fmt.Errorf("migrate tunnel %d (%q): %w", index, tunnel.Name, err)
		}
		document.Items = append(document.Items, connection)
		enabled := tunnel.Enabled == nil || *tunnel.Enabled
		if enabled {
			conditions = append(conditions, core.Condition{
				Code: "legacy_enabled_not_started", Severity: "warning", Connection: tunnel.Name,
				Driver: sshdriver.ID, Message: "该连接在 0.x 中为启用状态；迁移不会安装或启动原生服务",
				Actions: []core.Action{{Type: "enable_connection", Parameters: map[string]any{"connection": tunnel.Name}}},
			})
		}
		if tunnel.AutoSetupKey != nil {
			conditions = append(conditions, core.Condition{
				Code: "legacy_auto_setup_key_not_migrated", Severity: "info", Connection: tunnel.Name,
				Driver: sshdriver.ID, Message: "v1 不自动向远端安装 SSH 密钥，autoSetupKey 已忽略",
				Actions: []core.Action{},
			})
		}
	}
	sort.Slice(conditions, func(i, j int) bool {
		if conditions[i].Connection == conditions[j].Connection {
			return conditions[i].Code < conditions[j].Code
		}
		return conditions[i].Connection < conditions[j].Connection
	})
	return document, conditions, nil
}

func convertTunnel(tunnel legacyTunnel) (core.Connection, error) {
	jump, err := decodeStringList(tunnel.Jump, "jump")
	if err != nil {
		return core.Connection{}, err
	}
	tags, err := decodeStringList(tunnel.Tags, "tags")
	if err != nil {
		return core.Connection{}, err
	}
	options, err := normalizeSSHOptions(tunnel.SSHOptions)
	if err != nil {
		return core.Connection{}, err
	}
	forwards := make([]sshdriver.Forward, 0, len(tunnel.Forwards))
	for _, forward := range tunnel.Forwards {
		bind := forward.Bind
		if bind == "" {
			bind = "127.0.0.1"
		}
		forwards = append(forwards, sshdriver.Forward{
			Type: forward.Type, Bind: bind, SrcPort: forward.SrcPort,
			DestHost: forward.DestHost, DestPort: forward.DestPort,
		})
	}
	identity := ""
	if tunnel.IdentityFile != nil {
		identity = *tunnel.IdentityFile
	}
	sshConfig := sshdriver.Config{
		Host: tunnel.Host, Port: tunnel.Port, IdentityFile: identity,
		Jump: jump, Forwards: forwards, SSHOptions: options,
	}
	if err := sshdriver.Validate(sshConfig); err != nil {
		return core.Connection{}, err
	}
	rawConfig, err := json.Marshal(sshConfig)
	if err != nil {
		return core.Connection{}, err
	}
	labels := make(map[string]string, len(tags))
	for _, tag := range tags {
		if !validLegacyTag.MatchString(tag) {
			return core.Connection{}, fmt.Errorf("invalid tag %q", tag)
		}
		labels["tag.tunlite.dev/"+tag] = "true"
	}
	connection := core.Connection{
		APIVersion: core.APIVersion, Kind: core.Kind,
		Metadata: core.Metadata{Name: tunnel.Name, Labels: labels},
		Spec:     core.ConnectionSpec{Driver: sshdriver.ID, Lifecycle: "persistent", ConfigVersion: 1, Config: rawConfig},
	}
	if err := core.ValidateConnection(connection); err != nil {
		return core.Connection{}, err
	}
	return connection, nil
}

func normalizeSSHOptions(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	for index := 0; index < len(values); index++ {
		value := strings.TrimSpace(values[index])
		if value == "-o" {
			index++
			if index >= len(values) {
				return nil, errors.New("legacy sshOptions ends with -o without a value")
			}
			value = strings.TrimSpace(values[index])
		} else if strings.HasPrefix(value, "-o ") {
			value = strings.TrimSpace(strings.TrimPrefix(value, "-o "))
		} else if strings.HasPrefix(value, "-o") && strings.Contains(value[2:], "=") {
			value = strings.TrimSpace(value[2:])
		}
		if value == "" || strings.HasPrefix(value, "-") || !strings.Contains(value, "=") {
			return nil, fmt.Errorf("legacy SSH option %q cannot be represented safely as Key=Value", values[index])
		}
		result = append(result, value)
	}
	return result, nil
}

func decodeStringList(raw json.RawMessage, field string) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `""` {
		return []string{}, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("legacy %s must be a string or string array", field)
		}
		list = []string{value}
	}
	result := make([]string, 0, len(list))
	for _, item := range list {
		for _, part := range strings.Split(item, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				result = append(result, trimmed)
			}
		}
	}
	return result, nil
}

func finishInterruptedJournal(configPath, journalPath string, raw []byte, dryRun bool) (Report, error) {
	store := configstore.New(configPath)
	document, err := store.Load()
	if err != nil {
		return Report{}, err
	}
	report := Report{
		Outcome: "succeeded", Result: "already-migrated", Operation: "migrate", Path: configPath,
		JournalPath: journalPath, Connections: len(document.Items), DryRun: dryRun,
		Conditions: []core.Condition{},
	}
	journal, err := readJournal(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return Report{}, err
	}
	if journal.ConfigPath != configPath || digest(raw) != journal.MigratedSHA256 {
		return report, nil
	}
	report.BackupPath = journal.BackupPath
	report.SourceVersion = journal.SourceVersion
	if journal.Status == "prepared" && !dryRun {
		journal.Status = "applied"
		journal.UpdatedAt = time.Now().UTC()
		if err := writeJournal(journalPath, journal); err != nil {
			return Report{}, err
		}
	}
	return report, nil
}

func isV1(raw []byte) bool {
	var probe struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
	}
	return json.Unmarshal(raw, &probe) == nil && probe.APIVersion == core.APIVersion && probe.Kind == configstore.Kind
}

func hasObjectValues(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal(raw, &value) == nil && len(value) > 0
}

func journalFile(stateDir, configPath string) string {
	sum := sha256.Sum256([]byte(configPath))
	return filepath.Join(stateDir, "migrations", "config-"+hex.EncodeToString(sum[:8])+".json")
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func preserveBackup(path string, raw []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, raw) {
			return nil
		}
		return errors.New("migration backup exists with different contents")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".migration-backup-*.tmp")
	if err != nil {
		return fmt.Errorf("create migration backup: %w", err)
	}
	temporaryPath := temporary.Name()
	cleaned := false
	defer func() {
		_ = temporary.Close()
		if !cleaned {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect migration backup: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		return fmt.Errorf("write migration backup: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync migration backup: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close migration backup: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return errors.New("migration backup appeared during migration")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install migration backup: %w", err)
	}
	cleaned = true
	return nil
}

func readJournal(path string) (Journal, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Journal{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var journal Journal
	if err := decoder.Decode(&journal); err != nil {
		return Journal{}, fmt.Errorf("decode migration journal: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return Journal{}, fmt.Errorf("decode migration journal: %w", err)
	}
	return journal, nil
}

func writeJournal(path string, journal Journal) error {
	raw, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeAtomic(path, raw)
}

func writeAtomic(path string, raw []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".journal-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleaned := false
	defer func() {
		_ = temporary.Close()
		if !cleaned {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	cleaned = true
	return nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("more than one JSON value")
}
