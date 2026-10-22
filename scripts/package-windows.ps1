#requires -Version 7
<#
.SYNOPSIS
  Builds Pennyframe for Windows and packages it: a zip (for Scoop) and an installer (Inno Setup) in dist/.
.EXAMPLE
  ./scripts/package-windows.ps1 -Version 0.3.0
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version,
    # CI already ran the tests on the tagged commit.
    [switch]$SkipTests,
    [switch]$NoInstaller
)
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)

if (-not (Get-Command wails -ErrorAction SilentlyContinue)) {
    throw 'wails CLI not found. Install it: go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0'
}

if (-not $SkipTests) {
    go test ./...
    if ($LASTEXITCODE) { throw 'tests failed' }
}

wails build -s -clean -platform windows/amd64 -ldflags "-X main.version=$Version"
if ($LASTEXITCODE) { throw 'wails build failed' }

$dist = Join-Path (Get-Location) 'dist'
New-Item -ItemType Directory -Force $dist | Out-Null

$zip = Join-Path $dist "pennyframe-$Version-windows-amd64.zip"
Remove-Item $zip -ErrorAction SilentlyContinue
Compress-Archive -Path 'build/bin/pennyframe.exe' -DestinationPath $zip
Write-Host "Built $zip"

if (-not $NoInstaller) {
    $iscc = (Get-Command iscc -ErrorAction SilentlyContinue)?.Source
    if (-not $iscc) {
        $iscc = @("${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe", "$env:ProgramFiles\Inno Setup 6\ISCC.exe", "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe") |
            Where-Object { Test-Path $_ } | Select-Object -First 1
    }
    if (-not $iscc) { throw 'Inno Setup (ISCC.exe) not found. Install it (winget install JRSoftware.InnoSetup) or pass -NoInstaller.' }
    & $iscc "/DVersion=$Version" 'installer/pennyframe.iss'
    if ($LASTEXITCODE) { throw 'installer build failed' }
    Write-Host "Built installer in $dist"
}
