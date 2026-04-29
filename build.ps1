#requires -Version 5.0
# Cross-build gdrive-migrate for Windows / Linux / macOS.
# Usage:  .\build.ps1             - build all targets into .\dist
#         .\build.ps1 -Target win - build Windows only

param(
    [ValidateSet('all','win','linux','mac','mac-arm')]
    [string]$Target = 'all',
    [string]$Version = '0.1.0'
)

$ErrorActionPreference = 'Stop'

$ldflags = "-s -w -X main.version=$Version"

$dist = Join-Path $PSScriptRoot 'dist'
New-Item -ItemType Directory -Force -Path $dist | Out-Null

$env:CGO_ENABLED = '0'

function Build($os, $arch, $out) {
    Write-Host "→ Building $os/$arch -> $out"
    $env:GOOS   = $os
    $env:GOARCH = $arch
    & go build -ldflags $ldflags -o (Join-Path $dist $out) .
    if ($LASTEXITCODE -ne 0) { throw "build failed for $os/$arch" }
}

if ($Target -in 'all','win')     { Build 'windows' 'amd64' 'gdrive-migrate.exe' }
if ($Target -in 'all','linux')   { Build 'linux'   'amd64' 'gdrive-migrate-linux' }
if ($Target -in 'all','mac')     { Build 'darwin'  'amd64' 'gdrive-migrate-mac-intel' }
if ($Target -in 'all','mac-arm') { Build 'darwin'  'arm64' 'gdrive-migrate-mac-arm64' }

Write-Host ""
Write-Host "Done. Binaries in: $dist"
Get-ChildItem $dist | Format-Table Name, @{N='Size';E={'{0:N1} MB' -f ($_.Length/1MB)}}
