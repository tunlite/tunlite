// Package legacyconfig converts the frozen Tunlite 0.x declaration format
// into the current ConnectionList format. It does not read or write files and
// deliberately carries no migration, backup, rollback, or runtime behavior.
package legacyconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/yuanyuanzijin/tunlite/internal/configstore"
	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
)

var validTag = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type legacyDocument struct {
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

// Decode accepts only the frozen 0.x config/export envelope. The returned
// conditions describe old daemon-era fields that intentionally have no V1
// equivalent. The source bytes and the old runtime are never modified.
func Decode(raw []byte) (configstore.Document, []core.Condition, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var legacy legacyDocument
	if err := decoder.Decode(&legacy); err != nil {
		return configstore.Document{}, nil, fmt.Errorf("decode Tunlite 0.x config: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return configstore.Document{}, nil, fmt.Errorf("decode Tunlite 0.x config: %w", err)
	}
	if legacy.Version != 1 {
		return configstore.Document{}, nil, fmt.Errorf("unsupported Tunlite 0.x config version %d", legacy.Version)
	}
	if legacy.Tunnels == nil {
		return configstore.Document{}, nil, errors.New("tunlite 0.x config tunnels must be an array")
	}

	document := configstore.Empty()
	conditions := []core.Condition{}
	if hasObjectValues(legacy.Settings) {
		conditions = append(conditions, core.Condition{
			Code: "legacy_settings_ignored", Severity: "warning",
			Message: "0.x daemon settings and webhook configuration were not imported",
			Actions: []core.Action{},
		})
	}
	seen := make(map[string]struct{}, len(legacy.Tunnels))
	for index, tunnel := range legacy.Tunnels {
		if _, exists := seen[tunnel.Name]; exists {
			return configstore.Document{}, nil, fmt.Errorf("duplicate Tunlite 0.x tunnel name %q", tunnel.Name)
		}
		seen[tunnel.Name] = struct{}{}
		connection, err := convertTunnel(tunnel)
		if err != nil {
			return configstore.Document{}, nil, fmt.Errorf("import Tunlite 0.x tunnel %d (%q): %w", index, tunnel.Name, err)
		}
		document.Items = append(document.Items, connection)
		if tunnel.Enabled == nil || *tunnel.Enabled {
			conditions = append(conditions, core.Condition{
				Code: "legacy_running_state_ignored", Severity: "warning", Connection: tunnel.Name,
				Driver:  sshdriver.ID,
				Message: "the 0.x tunnel was enabled; V1 imported it in stopped mode to avoid running both versions on the same ports",
				Actions: []core.Action{{Type: "start_connection_after_legacy_shutdown", Parameters: map[string]any{"connection": tunnel.Name}}},
			})
		}
		if tunnel.AutoSetupKey != nil && *tunnel.AutoSetupKey {
			conditions = append(conditions, core.Condition{
				Code: "legacy_auto_setup_key_ignored", Severity: "info", Connection: tunnel.Name,
				Driver: sshdriver.ID, Message: "autoSetupKey was not imported; configure SSH keys with system OpenSSH tools",
				Actions: []core.Action{},
			})
		}
	}
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
	for _, tag := range tags {
		if !validTag.MatchString(tag) {
			return core.Connection{}, fmt.Errorf("invalid tag %q", tag)
		}
	}
	connection := core.Connection{
		ConfigVersion: core.ConfigVersion, Kind: core.Kind,
		Metadata: core.Metadata{Name: tunnel.Name, Tags: tags},
		Spec:     core.ConnectionSpec{Driver: sshdriver.ID, Config: rawConfig},
	}
	if err := core.ValidateConnection(connection); err != nil {
		return core.Connection{}, err
	}
	return connection, nil
}

func normalizeSSHOptions(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	for index := 0; index < len(values); index++ {
		original := values[index]
		value := strings.TrimSpace(original)
		if value == "-o" {
			index++
			if index >= len(values) {
				return nil, errors.New("SSH options end with -o without a value")
			}
			value = strings.TrimSpace(values[index])
		} else if strings.HasPrefix(value, "-o ") {
			value = strings.TrimSpace(strings.TrimPrefix(value, "-o "))
		} else if strings.HasPrefix(value, "-o") && strings.Contains(value[2:], "=") {
			value = strings.TrimSpace(value[2:])
		}
		if value == "" || strings.HasPrefix(value, "-") || !strings.Contains(value, "=") {
			return nil, fmt.Errorf("SSH option %q cannot be represented safely as Key=Value", original)
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
			return nil, fmt.Errorf("%s must be a string or string array", field)
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

func hasObjectValues(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal(raw, &value) == nil && len(value) > 0
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
