package cli

import "testing"

func assertReleaseProviderProcessesExited(t *testing.T, _ map[int]struct{}) {
	t.Helper()
	// The release/native-service E2E is explicitly unsupported on Windows and
	// returns before it can create or record a provider process. Unix uses
	// signal 0 for the post-cleanup assertion in release_process_unix_test.go.
}
