package ssh

import (
	"fmt"
	"strconv"
	"strings"
)

// ConfigPatch is the structured result of parsing SSH arguments for `set`.
// A Set field distinguishes an omitted option from an explicit replacement.
type ConfigPatch struct {
	HostSet       bool
	Host          string
	PortSet       bool
	Port          int
	IdentitySet   bool
	IdentityFile  string
	JumpSet       bool
	Jump          []string
	ForwardsSet   bool
	Forwards      []Forward
	SSHOptionsSet bool
	SSHOptions    []string
}

func (patch ConfigPatch) Empty() bool {
	return !patch.HostSet && !patch.PortSet && !patch.IdentitySet && !patch.JumpSet &&
		!patch.ForwardsSet && !patch.SSHOptionsSet
}

// ParseAddArguments parses Tunlite's deliberately small, safe OpenSSH argv
// subset. The common path can therefore be copied almost mechanically from an
// `ssh -N ... user@host` command without exposing commands or config loaders.
func ParseAddArguments(args []string) (Config, error) {
	patch, err := parseArguments(args, false)
	if err != nil {
		return Config{}, err
	}
	if !patch.HostSet {
		return Config{}, fmt.Errorf("SSH target is required (for example, user@host)")
	}
	config := Config{
		Host: patch.Host, Port: patch.Port, IdentityFile: patch.IdentityFile,
		Jump: patch.Jump, Forwards: patch.Forwards, SSHOptions: patch.SSHOptions,
	}
	if err := Validate(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

// ParseSetArguments parses the same SSH surface as add, but returns a patch.
// Supplying any forward or -o option replaces that complete list so repeated
// automation is idempotent rather than append-only.
func ParseSetArguments(args []string) (ConfigPatch, error) {
	return parseArguments(args, true)
}

func ApplyPatch(config Config, patch ConfigPatch) Config {
	if patch.HostSet {
		config.Host = patch.Host
	}
	if patch.PortSet {
		config.Port = patch.Port
	}
	if patch.IdentitySet {
		config.IdentityFile = patch.IdentityFile
	}
	if patch.JumpSet {
		config.Jump = append([]string(nil), patch.Jump...)
	}
	if patch.ForwardsSet {
		config.Forwards = append([]Forward(nil), patch.Forwards...)
	}
	if patch.SSHOptionsSet {
		config.SSHOptions = append([]string(nil), patch.SSHOptions...)
	}
	return config
}

func parseArguments(args []string, patchMode bool) (ConfigPatch, error) {
	var result ConfigPatch
	positionalOnly := false
	seen := map[string]bool{}

	valueFor := func(index *int, argument, short string) (string, error) {
		if argument != short {
			return strings.TrimPrefix(argument, short), nil
		}
		if *index+1 >= len(args) {
			return "", fmt.Errorf("%s requires a value", short)
		}
		(*index)++
		return args[*index], nil
	}
	setOnce := func(name string) error {
		if seen[name] {
			return fmt.Errorf("SSH option %s may only be specified once", name)
		}
		seen[name] = true
		return nil
	}

	for index := 0; index < len(args); index++ {
		argument := args[index]
		if result.HostSet {
			return ConfigPatch{}, fmt.Errorf("SSH target must be the final Driver argument; remote commands are not supported (unexpected %q)", argument)
		}
		if !positionalOnly && argument == "--" {
			positionalOnly = true
			continue
		}
		if !positionalOnly && (argument == "-N" || argument == "-T") {
			continue
		}
		if !positionalOnly && strings.HasPrefix(argument, "-") {
			if len(argument) < 2 {
				return ConfigPatch{}, fmt.Errorf("invalid SSH argument %q", argument)
			}
			short := argument[:2]
			switch short {
			case "-p":
				if err := setOnce("-p"); err != nil {
					return ConfigPatch{}, err
				}
				value, err := valueFor(&index, argument, short)
				if err != nil {
					return ConfigPatch{}, err
				}
				port, err := strconv.Atoi(value)
				if err != nil || !validPort(port) {
					return ConfigPatch{}, fmt.Errorf("invalid SSH port %q", value)
				}
				result.PortSet, result.Port = true, port
			case "-i":
				if err := setOnce("-i"); err != nil {
					return ConfigPatch{}, err
				}
				value, err := valueFor(&index, argument, short)
				if err != nil {
					return ConfigPatch{}, err
				}
				if value == "" {
					return ConfigPatch{}, fmt.Errorf("-i requires a non-empty identity file")
				}
				result.IdentitySet, result.IdentityFile = true, value
			case "-J":
				if err := setOnce("-J"); err != nil {
					return ConfigPatch{}, err
				}
				value, err := valueFor(&index, argument, short)
				if err != nil {
					return ConfigPatch{}, err
				}
				jump, err := ParseJump(value)
				if err != nil {
					return ConfigPatch{}, err
				}
				result.JumpSet, result.Jump = true, jump
			case "-L", "-R", "-D":
				value, err := valueFor(&index, argument, short)
				if err != nil {
					return ConfigPatch{}, err
				}
				forward, err := ParseForward(short, value)
				if err != nil {
					return ConfigPatch{}, err
				}
				result.ForwardsSet = true
				result.Forwards = append(result.Forwards, forward)
			case "-o":
				value, err := valueFor(&index, argument, short)
				if err != nil {
					return ConfigPatch{}, err
				}
				if err := validateSSHOption(value); err != nil {
					return ConfigPatch{}, err
				}
				result.SSHOptionsSet = true
				result.SSHOptions = append(result.SSHOptions, value)
			default:
				return ConfigPatch{}, fmt.Errorf("unsupported SSH argument %q", argument)
			}
			continue
		}

		host, port, err := ParseTarget(argument)
		if err != nil {
			return ConfigPatch{}, err
		}
		if host != strings.TrimSpace(argument) {
			return ConfigPatch{}, fmt.Errorf("SSH target must not contain a port; use -p %d %s", port, host)
		}
		result.HostSet, result.Host = true, host
	}

	if !patchMode {
		if result.Port == 0 {
			result.Port = 22
		}
		if !result.ForwardsSet {
			return ConfigPatch{}, fmt.Errorf("add needs at least one forward (-L / -R / -D)")
		}
	}
	if err := validatePatchLists(result); err != nil {
		return ConfigPatch{}, err
	}
	return result, nil
}

func validatePatchLists(patch ConfigPatch) error {
	config := Config{
		Host: "validation.invalid", Port: 22, IdentityFile: patch.IdentityFile,
		Jump: patch.Jump, Forwards: patch.Forwards, SSHOptions: patch.SSHOptions,
	}
	if patch.HostSet {
		config.Host = patch.Host
	}
	if patch.PortSet {
		config.Port = patch.Port
	}
	if !patch.ForwardsSet {
		config.Forwards = []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1}}
	}
	if err := Validate(config); err != nil {
		return err
	}
	return nil
}
