package service

import (
	"errors"
	"os/user"
	"slices"
	"testing"
)

func TestCurrentLaunchdSystemOnlyCapabilitiesAreExplicit(t *testing.T) {
	runner := &fakeRunner{run: func(command string, args []string) ([]byte, error) {
		if command != "launchctl" || len(args) != 2 || args[0] != "print" {
			return nil, errors.New("unexpected probe")
		}
		if slices.Equal(args, []string{"print", "system"}) {
			return nil, nil
		}
		return nil, errors.New("gui domain unavailable")
	}}
	adapter, err := newCurrent("darwin", t.TempDir(), runner, func(string) (string, error) {
		return "/bin/launchctl", nil
	}, func() (*user.User, error) {
		return &user.User{Username: "alice"}, nil
	}, 501)
	if err != nil {
		t.Fatal(err)
	}
	caps := adapter.Capabilities()
	if caps.UserScope || !caps.SystemScope {
		t.Fatalf("system-only capabilities=%+v", caps)
	}
}

func TestCurrentLaunchdDoesNotAdvertiseSystemWithoutSafeUser(t *testing.T) {
	runner := &fakeRunner{run: func(string, []string) ([]byte, error) { return nil, nil }}
	adapter, err := newCurrent("darwin", t.TempDir(), runner, func(string) (string, error) {
		return "/bin/launchctl", nil
	}, func() (*user.User, error) {
		return nil, errors.New("identity unavailable")
	}, 501)
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Capabilities().SystemScope {
		t.Fatalf("system scope advertised without a safe UserName: %+v", adapter.Capabilities())
	}
}
