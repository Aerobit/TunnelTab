#!/usr/bin/env bash
# Builds the portable TunnelTab folder and zip for Windows x64 and Linux x64.
#
#   ./scripts/build.sh            version from `git describe`, or "dev"
#   ./scripts/build.sh 1.2.3      explicit version
#
# Output:
#   dist/tunneltab/               the portable folder
#   dist/tunneltab-<version>.zip  the same folder, zipped for release
#   dist/SHA256SUMS.txt           checksum of the zip
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
VERSION="${VERSION#v}"
# Windows file properties need a numeric x.y.z version; dev builds get 0.0.0.
NUMVER="${VERSION%%[-+]*}"
[[ "$NUMVER" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || NUMVER="0.0.0"

OUT="dist/tunneltab"
LDFLAGS="-s -w -X main.version=${VERSION}"
WINRES="github.com/tc-hib/go-winres@v0.3.3"
SYSO="cmd/tunneltab/rsrc_windows_amd64.syso"

echo "Building TunnelTab ${VERSION}"
rm -rf dist
mkdir -p "$OUT"
trap 'rm -f "$SYSO"' EXIT

# Icon, version details and manifest for tunneltab.exe (Properties → Details).
# go build picks up the generated .syso automatically for Windows builds.
go run "$WINRES" simply --arch amd64 --out cmd/tunneltab/rsrc --manifest gui \
  --icon packaging/icon.png \
  --product-name "TunnelTab" \
  --file-description "TunnelTab - portable SSH terminal and web-UI launcher" \
  --product-version "$NUMVER" --file-version "$NUMVER" \
  --copyright "Copyright (c) 2026 Aerobit. MIT License." \
  --original-filename "tunneltab.exe"

# CGO off: fully static binaries that run on any Windows 10+/Linux x64 machine.
# -H windowsgui: no console window flashes up when the .exe is double-clicked.
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags "${LDFLAGS} -H windowsgui" -o "$OUT/tunneltab.exe" ./cmd/tunneltab
rm -f "$SYSO"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "${LDFLAGS}" -o "$OUT/tunneltab-linux-amd64" ./cmd/tunneltab

# Text files, with Windows line endings so every Windows viewer shows them.
crlf() { sed 's/\r*$/\r/'; }
sed "s/{{VERSION}}/${VERSION}/g" packaging/README.txt | crlf > "$OUT/README.txt"
crlf < LICENSE > "$OUT/LICENSE.txt"
go run ./scripts/notices | crlf > "$OUT/THIRD_PARTY_NOTICES.txt"

go run ./scripts/mkzip "$OUT" "dist/tunneltab-${VERSION}.zip"
(cd dist && sha256sum "tunneltab-${VERSION}.zip" > SHA256SUMS.txt)

echo "Done:"
ls -l "$OUT" dist/*.zip dist/SHA256SUMS.txt
