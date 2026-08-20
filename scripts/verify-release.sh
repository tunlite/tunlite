#!/bin/sh
# Verify a packaged Tunlite release without trusting the build step that made it.
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: scripts/verify-release.sh <v1.0.0-preview.N|v1.0.0> [output-directory]" >&2
  exit 2
fi

version="$1"
output="${2:-dist}"
plain_version="${version#v}"
macos="tunlite_${plain_version}_macos.tar.gz"
linux_x64="tunlite_${plain_version}_linux_x64.tar.gz"
linux_arm64="tunlite_${plain_version}_linux_arm64.tar.gz"

expected_files="SHA256SUMS
$linux_arm64
$linux_x64
$macos"
actual_files="$(find "$output" -mindepth 1 -maxdepth 1 -exec basename {} \; | LC_ALL=C sort)"
if [ "$actual_files" != "$expected_files" ]; then
  echo "tunlite: release directory contains an unexpected file set" >&2
  printf 'expected:\n%s\nactual:\n%s\n' "$expected_files" "$actual_files" >&2
  exit 1
fi

expected_checksum_files="$linux_arm64
$linux_x64
$macos"
actual_checksum_files="$(awk '{ print $2 }' "$output/SHA256SUMS" | LC_ALL=C sort)"
if [ "$actual_checksum_files" != "$expected_checksum_files" ]; then
  echo "tunlite: SHA256SUMS does not cover the exact release archive set" >&2
  printf 'expected:\n%s\nactual:\n%s\n' "$expected_checksum_files" "$actual_checksum_files" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$output" && sha256sum --check SHA256SUMS)
elif command -v shasum >/dev/null 2>&1; then
  (cd "$output" && shasum -a 256 --check SHA256SUMS)
else
  echo "tunlite: release verification requires sha256sum or shasum" >&2
  exit 1
fi

for archive in "$linux_arm64" "$linux_x64" "$macos"; do
  entries="$(tar -tzf "$output/$archive")"
  if [ "$entries" != "tunlite
LICENSE
README.md" ]; then
    echo "tunlite: unexpected archive contents in $archive" >&2
    printf '%s\n' "$entries" >&2
    exit 1
  fi
done

temporary="$(mktemp -d)"
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
case "$(uname -s):$(uname -m)" in
  Darwin:x86_64|Darwin:arm64)
    native="$macos"
    command -v lipo >/dev/null 2>&1 || { echo "tunlite: lipo is required on macOS" >&2; exit 1; }
    ;;
  Linux:x86_64|Linux:amd64) native="$linux_x64" ;;
  Linux:arm64|Linux:aarch64) native="$linux_arm64" ;;
  *) echo "tunlite: unsupported verification platform $(uname -s)/$(uname -m)" >&2; exit 1 ;;
esac

tar -xzf "$output/$native" -C "$temporary" tunlite
if [ "$(uname -s)" = Darwin ]; then
  lipo "$temporary/tunlite" -verify_arch x86_64 arm64
fi
actual_version="$($temporary/tunlite version --json)"
expected_version="{\"outcome\":\"succeeded\",\"protocolVersion\":1,\"version\":\"$plain_version\"}"
if [ "$actual_version" != "$expected_version" ]; then
  echo "tunlite: packaged binary reported the wrong version" >&2
  printf 'expected: %s\nactual:   %s\n' "$expected_version" "$actual_version" >&2
  exit 1
fi

echo "tunlite: release artifacts verified ($version)"
