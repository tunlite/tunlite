#!/bin/sh
# Verify the documented v0.10.3 -> V1 import, per-connection switch, and
# rollback against real OpenSSH traffic without using the working-tree Node code.
set -eu

if [ "$(uname -s)" != Darwin ]; then
  echo "tunlite: migration E2E currently requires macOS; Linux systemd is a separate hardware gate" >&2
  exit 1
fi
if [ "$#" -gt 1 ]; then
  echo "usage: scripts/test-migration-e2e.sh [v1.0.0-preview.N|v1.0.0]" >&2
  exit 2
fi

version="${1:-v1.0.0-preview.1}"
plain_version="${version#v}"
temporary="$(mktemp -d)"
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
mkdir -p "$temporary/legacy"

git archive --format=tar v0.10.3 | tar -xf - -C "$temporary/legacy"
scripts/package-release.sh "$version" "$temporary/dist"
scripts/verify-release.sh "$version" "$temporary/dist"
tar -xzf "$temporary/dist/tunlite_${plain_version}_macos.tar.gz" -C "$temporary" tunlite

TUNLITE_REAL_MIGRATION_E2E=1 \
TUNLITE_RELEASE_BINARY="$temporary/tunlite" \
TUNLITE_LEGACY_BINARY="$temporary/legacy/bin/tunlite.js" \
TUNLITE_EXPECTED_RELEASE_VERSION="$plain_version" \
go test ./internal/cli -run '^TestRealLegacy0103ImportSwitchAndRollback$' -count=1 -v
