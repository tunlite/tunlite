package ssh

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yuanyuanzijin/tunlite/internal/core"
)

func TestClassifyCheckFailure(t *testing.T) {
	tests := []struct {
		output string
		want   string
	}{
		{"Permission denied (publickey).", "ssh_authentication_failed"},
		{"Host key verification failed.", "ssh_host_key_untrusted"},
		{"Could not resolve hostname example.invalid", "ssh_dns_failed"},
		{"Connection refused", "ssh_unreachable"},
	}
	for _, test := range tests {
		code, _, _ := classifyCheckFailure("host", test.output, nil)
		if code != test.want {
			t.Fatalf("%q: got %q, want %q", test.output, code, test.want)
		}
	}
}

func TestCheckReportsMissingProvider(t *testing.T) {
	config := Config{Host: "me@host"}
	raw, _ := json.Marshal(config)
	connection := core.Connection{
		APIVersion: core.APIVersion, Kind: core.Kind, Metadata: core.Metadata{Name: "probe"},
		Spec: core.ConnectionSpec{Driver: ID, Lifecycle: "foreground", ConfigVersion: 1, Config: raw},
	}
	driver := NewWithLookup(func(string) (string, error) { return "", context.Canceled })
	report, err := driver.Check(context.Background(), connection, core.CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Healthy || report.Conditions[0].Code != "provider_missing" {
		t.Fatalf("unexpected report: %#v", report)
	}
}
