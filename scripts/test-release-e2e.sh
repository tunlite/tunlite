#!/bin/sh
# Build the macOS release artifact, then exercise that exact binary through
# real OpenSSH, launchd, all forwarding modes, traffic, and crash recovery.
set -eu

if [ "$(uname -s)" != Darwin ]; then
  echo "tunlite: release E2E currently requires macOS; Linux systemd is a separate hardware gate" >&2
  exit 1
fi
if [ "$#" -gt 1 ]; then
  echo "usage: scripts/test-release-e2e.sh [v1.0.0-preview.N|v1.0.0]" >&2
  exit 2
fi

version="${1:-v1.0.0-preview.1}"
plain_version="${version#v}"
temporary="$(mktemp -d)"
trap 'rm -rf "$temporary"' EXIT HUP INT TERM

scripts/package-release.sh "$version" "$temporary/dist"
scripts/verify-release.sh "$version" "$temporary/dist"
tar -xzf "$temporary/dist/tunlite_${plain_version}_macos.tar.gz" -C "$temporary" tunlite

TUNLITE_REAL_RELEASE_E2E=1 \
TUNLITE_RELEASE_BINARY="$temporary/tunlite" \
TUNLITE_EXPECTED_RELEASE_VERSION="$plain_version" \
go test ./internal/cli -run '^TestReleaseBinaryRealOpenSSHNativeServiceTraffic$' -count=1 -v
