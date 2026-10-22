#requires -Version 7
<#
.SYNOPSIS
  Builds Pennyframe, packages it for Scoop and points bucket/pennyframe.json at the new zip.
.EXAMPLE
  ./scripts/release.ps1 -Version 0.2.0
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version,
    [string]$Repo = 'Kollefranz/pennyframe'
)
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)

if (-not (Get-Command wails -ErrorAction SilentlyContinue)) {
    throw 'wails CLI not found. Install it: go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0'
}

go test ./...
if ($LASTEXITCODE) { throw 'tests failed' }

wails build -s -clean -platform windows/amd64 -ldflags "-X main.version=$Version"
if ($LASTEXITCODE) { throw 'wails build failed' }

$zipName = "pennyframe-$Version-windows-amd64.zip"
$dist = Join-Path (Get-Location) 'dist'
New-Item -ItemType Directory -Force $dist | Out-Null
$zip = Join-Path $dist $zipName
Remove-Item $zip -ErrorAction SilentlyContinue
Compress-Archive -Path 'build/bin/pennyframe.exe' -DestinationPath $zip

$hash = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLowerInvariant()
[IO.File]::WriteAllText("$zip.sha256", $hash + "`n")

# Targeted edits keep the manifest's formatting and its PowerShell snippets intact.
$manifestPath = 'bucket/pennyframe.json'
$text = [IO.File]::ReadAllText((Resolve-Path $manifestPath))
$url = "https://github.com/$Repo/releases/download/v$Version/$zipName"
$text = [regex]::new('("version"\s*:\s*")[^"]*(")').Replace($text, "`${1}$Version`${2}", 1)
$text = [regex]::new('("url"\s*:\s*")https://[^"]*(")').Replace($text, "`${1}$url`${2}", 1)
$text = [regex]::new('("hash"\s*:\s*")[^"]*(")').Replace($text, "`${1}$hash`${2}", 1)
[IO.File]::WriteAllText((Resolve-Path $manifestPath), $text)
$null = $text | ConvertFrom-Json # fail loudly if an edit broke the JSON

Write-Host "Built $zipName"
Write-Host "sha256 $hash"
Write-Host "Manifest updated: $manifestPath"
