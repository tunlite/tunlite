package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type blackboxContract struct {
	ContractVersion string `json:"contractVersion"`
	SourceVersion   string `json:"sourceVersion"`
	Workflows       []struct {
		Name  string `json:"name"`
		Steps []struct {
			Args           []string `json:"args"`
			Exit           int      `json:"exit"`
			StdoutContains []string `json:"stdoutContains"`
		} `json:"steps"`
	} `json:"workflows"`
}

func TestSSHCLICompatibilityContract(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "contracts", "ssh-cli-v0.10.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var contract blackboxContract
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.ContractVersion != "tunlite.blackbox/v1" || contract.SourceVersion != "0.10.3" {
		t.Fatalf("unexpected contract metadata: %#v", contract)
	}
	for _, workflow := range contract.Workflows {
		t.Run(workflow.Name, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "config.json")
			for index, step := range workflow.Steps {
				args := append([]string(nil), step.Args...)
				args = append(args, "--config", config)
				var stdout, stderr bytes.Buffer
				code := RunContext(context.Background(), args, IO{Out: &stdout, Err: &stderr})
				if code != step.Exit {
					t.Fatalf("step %d %v: exit=%d want=%d stdout=%s stderr=%s", index, step.Args, code, step.Exit, stdout.String(), stderr.String())
				}
				for _, expected := range step.StdoutContains {
					if !strings.Contains(stdout.String(), expected) {
						t.Fatalf("step %d %v: stdout missing %q: %s", index, step.Args, expected, stdout.String())
					}
				}
			}
		})
	}
}
