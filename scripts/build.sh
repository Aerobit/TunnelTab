#!/usr/bin/env bash
# Builds the portable TunnelTab folder and zip for Windows x64 and Linux x64.
#
#   ./scripts/build.sh            version from `git describe`, or "dev"
#   ./scripts/build.sh 1.2.3      explicit version
#
# Output:
#   dist/tunneltab/               the portable folder
#   dist/tunneltab-<version>.zip  the same folder, zipped for release
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
VERSION="${VERSION#v}"
OUT="dist/tunneltab"
LDFLAGS="-s -w -X main.version=${VERSION}"

echo "Building TunnelTab ${VERSION}"
rm -rf dist
mkdir -p "$OUT"

# CGO off: fully static binaries that run on any Windows 10+/Linux x64 machine.
# -H windowsgui: no console window flashes up when the .exe is double-clicked.
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags "${LDFLAGS} -H windowsgui" -o "$OUT/tunneltab.exe" ./cmd/tunneltab
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "${LDFLAGS}" -o "$OUT/tunneltab-linux-amd64" ./cmd/tunneltab

sed "s/{{VERSION}}/${VERSION}/g" packaging/README.txt > "$OUT/README.txt"

go run ./scripts/mkzip "$OUT" "dist/tunneltab-${VERSION}.zip"

echo "Done:"
ls -l "$OUT" "dist/tunneltab-${VERSION}.zip"
