#!/bin/sh
# Build a macOS release artifact, or reuse an explicitly supplied release
# directory, then exercise that exact archive through real OpenSSH, launchd,
# all forwarding modes, traffic, and crash recovery.
set -eu

if [ "$#" -gt 2 ]; then
  echo "usage: scripts/test-release-e2e.sh [v1.0.0-preview.N|v1.0.0] [release-directory]" >&2
  exit 2
fi
if [ "$(uname -s)" != Darwin ]; then
  echo "tunlite: release E2E currently requires macOS; Linux systemd is a separate hardware gate" >&2
  exit 1
fi

version="${1:-v1.0.0-preview.1}"
plain_version="${version#v}"

if [ "$#" -eq 2 ]; then
  release_directory="$2"
  if [ ! -d "$release_directory" ]; then
    echo "tunlite: supplied release directory does not exist or is not a directory: $release_directory" >&2
    exit 1
  fi
  use_existing_release=1
  echo "tunlite: testing existing release artifacts in $release_directory"
else
  use_existing_release=0
fi

temporary="$(mktemp -d)"
trap 'rm -rf "$temporary"' EXIT HUP INT TERM

if [ "$use_existing_release" -eq 0 ]; then
  release_directory="$temporary/dist"
  echo "tunlite: building release artifacts for native E2E"
  scripts/package-release.sh "$version" "$release_directory"
fi

scripts/verify-release.sh "$version" "$release_directory"
tar -xzf "$release_directory/tunlite_${plain_version}_macos.tar.gz" -C "$temporary" tunlite

TUNLITE_REAL_RELEASE_E2E=1 \
TUNLITE_RELEASE_BINARY="$temporary/tunlite" \
TUNLITE_EXPECTED_RELEASE_VERSION="$plain_version" \
go test ./internal/cli -run '^TestReleaseBinaryRealOpenSSHNativeServiceTraffic$' -count=1 -v
