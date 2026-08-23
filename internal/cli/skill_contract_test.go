package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestAgentSkillMatchesRuntimeCapabilities(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := Run([]string{"capabilities", "--json"}, IO{Out: &out, Err: &stderr}); code != 0 {
		t.Fatalf("capabilities exit=%d stderr=%s", code, stderr.String())
	}
	var capabilities struct {
		ProtocolVersion int            `json:"protocolVersion"`
		Commands        []string       `json:"commands"`
		ExitCodes       map[string]int `json:"exitCodes"`
		NativeServices  struct {
			StateDimensions map[string][]string `json:"stateDimensions"`
		} `json:"nativeServices"`
	}
	if err := json.Unmarshal(out.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join("..", "..", "skill", "use-tunlite", "SKILL.md")
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	contract := parseSkillContract(t, text)
	if contract["protocolVersion"] != strconv.Itoa(capabilities.ProtocolVersion) {
		t.Fatalf("Skill protocolVersion drifted: got %q want %d", contract["protocolVersion"], capabilities.ProtocolVersion)
	}

	commands := splitContractValues(contract["commands"])
	sort.Strings(commands)
	wantCommands := append([]string(nil), capabilities.Commands...)
	sort.Strings(wantCommands)
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("Skill commands drifted:\n got %v\nwant %v", commands, wantCommands)
	}

	wantExitCodes := make([]int, 0, len(capabilities.ExitCodes))
	for _, code := range capabilities.ExitCodes {
		wantExitCodes = append(wantExitCodes, code)
	}
	sort.Ints(wantExitCodes)
	wantExitCodes = uniqueInts(wantExitCodes)
	gotExitCodes := make([]int, 0)
	for _, value := range splitContractValues(contract["exit-codes"]) {
		code, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("invalid Skill exit code %q", value)
		}
		gotExitCodes = append(gotExitCodes, code)
	}
	sort.Ints(gotExitCodes)
	if !reflect.DeepEqual(gotExitCodes, wantExitCodes) {
		t.Fatalf("Skill exit codes drifted: got %v want %v", gotExitCodes, wantExitCodes)
	}

	for dimension, values := range capabilities.NativeServices.StateDimensions {
		got := splitContractValues(contract[dimension])
		if !reflect.DeepEqual(got, values) {
			t.Fatalf("Skill %s drifted: got %v want %v", dimension, got, values)
		}
	}

	known := make(map[string]bool, len(capabilities.Commands))
	for _, command := range capabilities.Commands {
		known[command] = true
	}
	examplePattern := regexp.MustCompile(`(?m)tunlite ([a-z][a-z-]*)`)
	for _, match := range examplePattern.FindAllStringSubmatch(text, -1) {
		if !known[match[1]] {
			t.Errorf("Skill example uses command %q which is absent from capabilities", match[1])
		}
	}
}

func TestAgentSkillContainsNoRemovedRuntimeContract(t *testing.T) {
	path := filepath.Join("..", "..", "skill", "use-tunlite", "SKILL.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(raw))
	if !strings.Contains(string(raw), "name: use-tunlite") {
		t.Error("Skill does not use the canonical use-tunlite name")
	}
	for _, forbidden := range []string{"name: ssh-tunnel", "install skill", "uninstall skill", "daemon", "ipc", "node.js", "npm", "install service", "退出码 | 6", "| 6 |"} {
		if strings.Contains(lower, strings.ToLower(forbidden)) {
			t.Errorf("Skill still contains removed contract %q", forbidden)
		}
	}
}

func parseSkillContract(t *testing.T, text string) map[string]string {
	t.Helper()
	pattern := regexp.MustCompile(`(?s)<!-- tunlite-contract\n(.*?)\n-->`)
	match := pattern.FindStringSubmatch(text)
	if len(match) != 2 {
		t.Fatal("Skill is missing tunlite-contract metadata")
	}
	result := make(map[string]string)
	for _, line := range strings.Split(match[1], "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			t.Fatalf("invalid Skill contract line %q", line)
		}
		result[key] = strings.TrimSpace(value)
	}
	return result
}

func splitContractValues(value string) []string {
	if value == "" {
		return []string{}
	}
	parts := strings.Split(value, ",")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
}

func uniqueInts(values []int) []int {
	result := make([]int, 0, len(values))
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
