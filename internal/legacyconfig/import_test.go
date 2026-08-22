package legacyconfig

import (
	"strings"
	"testing"
)

func TestDecodeRejectsUnsafeLegacySSHOptions(t *testing.T) {
	raw := []byte(`{
  "version": 1,
  "settings": {},
  "tunnels": [{
    "name": "unsafe",
    "host": "example.com",
    "port": 22,
    "identityFile": null,
    "jump": [],
    "tags": [],
    "forwards": [{"type":"dynamic","bind":"127.0.0.1","srcPort":1080,"destHost":"","destPort":0}],
    "sshOptions": ["-o", "ProxyCommand=helper %h %p"],
    "enabled": false,
    "autoSetupKey": false
  }]
}`)
	_, _, err := Decode(raw)
	if err == nil || !strings.Contains(err.Error(), `tunnel 0 ("unsafe")`) || !strings.Contains(err.Error(), `ProxyCommand`) {
		t.Fatalf("unsafe legacy option was not rejected with its connection: %v", err)
	}
}

func TestDecodeRejectsMultipleLegacyTagsInsteadOfGuessingAGroup(t *testing.T) {
	raw := []byte(`{
  "version": 1,
  "settings": {},
  "tunnels": [{
    "name": "ambiguous",
    "host": "example.com",
    "port": 22,
    "identityFile": null,
    "jump": [],
    "tags": ["work", "database"],
    "forwards": [{"type":"dynamic","bind":"127.0.0.1","srcPort":1080,"destHost":"","destPort":0}],
    "sshOptions": [],
    "enabled": false,
    "autoSetupKey": false
  }]
}`)
	_, _, err := Decode(raw)
	if err == nil || !strings.Contains(err.Error(), "multiple legacy tags") || !strings.Contains(err.Error(), "choose one group") {
		t.Fatalf("ambiguous legacy tags error=%v", err)
	}
}
