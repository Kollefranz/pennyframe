#requires -Version 7
<#
.SYNOPSIS
  Points the Scoop manifest and the Homebrew cask at the release files in dist/.
.EXAMPLE
  ./scripts/update-manifests.ps1 -Version 0.3.0
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version,
    [string]$Repo = 'Kollefranz/pennyframe'
)
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)

function Get-Sha([string]$path) { (Get-FileHash $path -Algorithm SHA256).Hash.ToLowerInvariant() }

# Targeted edits keep each file's formatting (and Scoop's PowerShell snippets) intact.
function Edit-First([string]$text, [string]$pattern, [string]$value) {
    [regex]::new($pattern).Replace($text, ('${1}' + $value + '${2}'), 1)
}

# ---- Scoop (Windows zip)
$winZip = "dist/pennyframe-$Version-windows-amd64.zip"
if (Test-Path $winZip) {
    $path = Resolve-Path 'bucket/pennyframe.json'
    $text = [IO.File]::ReadAllText($path)
    $url = "https://github.com/$Repo/releases/download/v$Version/$(Split-Path $winZip -Leaf)"
    $text = Edit-First $text '("version"\s*:\s*")[^"]*(")' $Version
    $text = Edit-First $text '("url"\s*:\s*")https://[^"]*(")' $url
    $text = Edit-First $text '("hash"\s*:\s*")[^"]*(")' (Get-Sha $winZip)
    $null = $text | ConvertFrom-Json # fail loudly if an edit broke the JSON
    [IO.File]::WriteAllText($path, $text)
    Write-Host "Updated bucket/pennyframe.json to $Version"
} else { Write-Warning "$winZip missing; Scoop manifest not updated" }

# ---- Homebrew (macOS zip)
$macZip = "dist/pennyframe-$Version-macos-universal.zip"
if (Test-Path $macZip) {
    $path = Resolve-Path 'Casks/pennyframe.rb'
    $text = [IO.File]::ReadAllText($path)
    $text = Edit-First $text '(version\s+")[^"]*(")' $Version
    $text = Edit-First $text '(sha256\s+")[^"]*(")' (Get-Sha $macZip)
    [IO.File]::WriteAllText($path, $text)
    Write-Host "Updated Casks/pennyframe.rb to $Version"
} else { Write-Warning "$macZip missing; Homebrew cask not updated" }

# ---- checksums published next to the assets
foreach ($f in Get-ChildItem dist -File | Where-Object { $_.Name -notlike '*.sha256' }) {
    [IO.File]::WriteAllText("$($f.FullName).sha256", (Get-Sha $f.FullName) + "`n")
}
