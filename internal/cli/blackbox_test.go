package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuanyuanzijin/tunlite/internal/service"
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
			root := t.TempDir()
			t.Setenv("TUNLITE_HOME", filepath.Join(root, "home"))
			t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
			adapter := newFakeServiceAdapter(filepath.Join(root, "native"))
			for index, step := range workflow.Steps {
				args := append([]string(nil), step.Args...)
				var stdout, stderr bytes.Buffer
				code := runContext(context.Background(), args, IO{Out: &stdout, Err: &stderr}, func() (service.Adapter, error) { return adapter, nil })
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
