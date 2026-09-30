# Builds the portable TunnelTab folder and zip for Windows x64 and Linux x64.
#
#   .\scripts\build.ps1            version from `git describe`, or "dev"
#   .\scripts\build.ps1 1.2.3      explicit version
#
# Output:
#   dist\tunneltab\               the portable folder
#   dist\tunneltab-<version>.zip  the same folder, zipped for release
param([string]$Version = "")

$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

if (-not $Version) {
    $Version = (git describe --tags --always --dirty 2>$null)
    if ($LASTEXITCODE -ne 0 -or -not $Version) { $Version = "dev" }
}
$Version = $Version.TrimStart("v")
$Out = "dist\tunneltab"
$LdFlags = "-s -w -X main.version=$Version"

Write-Host "Building TunnelTab $Version"
if (Test-Path dist) { Remove-Item -Recurse -Force dist }
New-Item -ItemType Directory -Force $Out | Out-Null

function Build([string]$Os, [string]$Output, [string]$ExtraLdFlags) {
    $env:CGO_ENABLED = "0"; $env:GOOS = $Os; $env:GOARCH = "amd64"
    go build -trimpath -ldflags "$LdFlags $ExtraLdFlags" -o $Output ./cmd/tunneltab
    if ($LASTEXITCODE -ne 0) { throw "build failed for $Os" }
}

try {
    # -H windowsgui: no console window flashes up when the .exe is double-clicked.
    Build "windows" "$Out\tunneltab.exe" "-H windowsgui"
    Build "linux" "$Out\tunneltab-linux-amd64" ""
} finally {
    Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
}

(Get-Content packaging\README.txt -Raw).Replace("{{VERSION}}", $Version) |
    Set-Content -NoNewline -Encoding utf8 "$Out\README.txt"

go run ./scripts/mkzip $Out "dist\tunneltab-$Version.zip"
if ($LASTEXITCODE -ne 0) { throw "packaging failed" }

Write-Host "Done:"
Get-ChildItem $Out, "dist\tunneltab-$Version.zip"
