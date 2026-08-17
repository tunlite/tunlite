#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: scripts/package-release.sh <v1.0.0-preview.N|v1.0.0>" >&2
  exit 2
fi

GO_BINARY="${GO:-go}"
exec "$GO_BINARY" run ./tools/package "$1" dist
