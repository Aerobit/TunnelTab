# Builds the portable TunnelTab folder and zip for Windows x64 and Linux x64.
#
#   ./scripts/build.ps1            version from `git describe`, or "dev"
#   ./scripts/build.ps1 1.2.3      explicit version
#
# Output:
#   dist/tunneltab/               the portable folder
#   dist/tunneltab-<version>.zip  the same folder, zipped for release
#   dist/SHA256SUMS.txt           checksum of the zip
param([string]$Version = "")

$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

if (-not $Version) {
    $Version = (git describe --tags --always --dirty 2>$null)
    if ($LASTEXITCODE -ne 0 -or -not $Version) { $Version = "dev" }
}
$Version = $Version.TrimStart("v")
# Windows file properties need a numeric x.y.z version; dev builds get 0.0.0.
$NumVer = ($Version -split "[-+]")[0]
if ($NumVer -notmatch '^\d+\.\d+\.\d+$') { $NumVer = "0.0.0" }

$Out = "dist/tunneltab"
$LdFlags = "-s -w -X main.version=$Version"
$WinRes = "github.com/tc-hib/go-winres@v0.3.3"
$Syso = "cmd/tunneltab/rsrc_windows_amd64.syso"

function Invoke-Checked([string]$What, [scriptblock]$Block) {
    & $Block
    if ($LASTEXITCODE -ne 0) { throw "$What failed" }
}

function Build([string]$Os, [string]$Output, [string]$ExtraLdFlags) {
    $env:CGO_ENABLED = "0"; $env:GOOS = $Os; $env:GOARCH = "amd64"
    Invoke-Checked "build for $Os" { go build -trimpath -ldflags "$LdFlags $ExtraLdFlags" -o $Output ./cmd/tunneltab }
}

# Writes text with Windows line endings (UTF-8, no BOM).
function Write-Crlf([string]$Path, [string]$Text) {
    $crlf = ($Text -replace "`r?`n", "`r`n")
    [System.IO.File]::WriteAllText((Join-Path (Get-Location) $Path), $crlf, (New-Object System.Text.UTF8Encoding $false))
}

Write-Host "Building TunnelTab $Version"
if (Test-Path dist) { Remove-Item -Recurse -Force dist }
New-Item -ItemType Directory -Force $Out | Out-Null

try {
    # Icon, version details and manifest for tunneltab.exe (Properties → Details).
    Invoke-Checked "go-winres" {
        go run $WinRes simply --arch amd64 --out cmd/tunneltab/rsrc --manifest gui `
            --icon packaging/icon.png `
            --product-name "TunnelTab" `
            --file-description "TunnelTab - portable SSH terminal and web-UI launcher" `
            --product-version $NumVer --file-version $NumVer `
            --copyright "Copyright (c) 2026 Aerobit. MIT License." `
            --original-filename "tunneltab.exe"
    }
    # -H windowsgui: no console window flashes up when the .exe is double-clicked.
    Build "windows" "$Out/tunneltab.exe" "-H windowsgui"
    Remove-Item $Syso -ErrorAction SilentlyContinue
    Build "linux" "$Out/tunneltab-linux-amd64" ""
} finally {
    Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
    Remove-Item $Syso -ErrorAction SilentlyContinue
}

Write-Crlf "$Out/README.txt" ((Get-Content packaging/README.txt -Raw).Replace("{{VERSION}}", $Version))
Write-Crlf "$Out/LICENSE.txt" (Get-Content LICENSE -Raw)
$notices = Invoke-Checked "notices" { go run ./scripts/notices }
Write-Crlf "$Out/THIRD_PARTY_NOTICES.txt" (($notices -join "`n") + "`n")

Invoke-Checked "packaging" { go run ./scripts/mkzip $Out "dist/tunneltab-$Version.zip" }
$hash = (Get-FileHash "dist/tunneltab-$Version.zip" -Algorithm SHA256).Hash.ToLower()
# Plain LF line endings: checksum tools on every system accept them.
[System.IO.File]::WriteAllText((Join-Path (Get-Location) "dist/SHA256SUMS.txt"), "$hash  tunneltab-$Version.zip`n", (New-Object System.Text.UTF8Encoding $false))

Write-Host "Done:"
Get-ChildItem $Out, "dist/tunneltab-$Version.zip", "dist/SHA256SUMS.txt"
