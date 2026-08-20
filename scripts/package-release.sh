#!/bin/sh
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: scripts/package-release.sh <v1.0.0-preview.N|v1.0.0> [output-directory]" >&2
  exit 2
fi

GO_BINARY="${GO:-go}"
exec "$GO_BINARY" run ./tools/package "$1" "${2:-dist}"
