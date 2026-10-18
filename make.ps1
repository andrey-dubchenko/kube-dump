<#
.SYNOPSIS
  kube-dump build script for Windows PowerShell (same targets as the Makefile).

.EXAMPLE
  .\make.ps1                build for this machine (kube-dump.exe, with the icon)
  .\make.ps1 windows        dist\windows-amd64\kube-dump.exe
  .\make.ps1 linux          dist\linux-amd64\: binary, desktop entry, icons, install.sh
  .\make.ps1 dist           both
  .\make.ps1 check          gofmt + go vet + tests
  .\make.ps1 icons          redraw assets\icon.ico and the PNG icons
  .\make.ps1 resources      recompile the Windows resources (needs windres from MinGW)
  .\make.ps1 clean
  .\make.ps1 build -Version 1.2.0
#>
param(
    [Parameter(Position = 0)]
    [ValidateSet('build', 'windows', 'linux', 'dist', 'test', 'vet', 'fmt', 'check', 'icons', 'resources', 'clean', 'help')]
    [string]$Target = 'build',
    [string]$Version = ''
)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$Name = 'kube-dump'
$Pkg = './cmd/kube-dump'
$Dist = 'dist'
if (-not $Version) {
    # git writes to stderr when there is no commit yet; that must not stop the script
    $saved = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $Version = (git describe --tags --always --dirty 2>$null | Select-Object -First 1)
    $ErrorActionPreference = $saved
    if (-not $Version) { $Version = 'dev' }
}
$LdFlags = "-s -w -X main.version=$Version"

function Invoke-Native {
    # run a native command and stop on a non-zero exit code
    & $args[0] $args[1..($args.Length - 1)]
    if ($LASTEXITCODE -ne 0) { throw "$($args -join ' ') failed with exit code $LASTEXITCODE" }
}

function Build-For([string]$Os, [string]$Output) {
    $oldOs, $oldArch = $env:GOOS, $env:GOARCH
    try {
        $env:GOOS = $Os
        $env:GOARCH = 'amd64'
        New-Item -ItemType Directory -Force -Path (Split-Path $Output) | Out-Null
        Invoke-Native go build -trimpath -ldflags $LdFlags -o $Output $Pkg
    }
    finally {
        $env:GOOS, $env:GOARCH = $oldOs, $oldArch
    }
}

function Build-Windows {
    Build-For 'windows' "$Dist/windows-amd64/$Name.exe"
    Copy-Item configuration.example.yml "$Dist/windows-amd64/"
}

function Build-Linux {
    $out = "$Dist/linux-amd64"
    Build-For 'linux' "$out/$Name"
    if (Test-Path "$out/icons") { Remove-Item -Recurse -Force "$out/icons" }
    Copy-Item -Recurse assets/icons "$out/icons"
    Copy-Item packaging/linux/kube-dump.desktop, packaging/linux/install.sh, configuration.example.yml $out
    Write-Host "linux build is in $out (on Linux: sh install.sh installs the binary, the desktop entry and the icons)"
}

function Test-Format {
    $bad = gofmt -l .
    if ($bad) { throw "gofmt needs to be run on:`n$($bad -join "`n")" }
}

switch ($Target) {
    'build' { Invoke-Native go build -trimpath -ldflags $LdFlags -o "$Name.exe" $Pkg }
    'windows' { Build-Windows }
    'linux' { Build-Linux }
    'dist' { Build-Windows; Build-Linux }
    'test' { Invoke-Native go test ./... }
    'vet' { Invoke-Native go vet ./... }
    'fmt' { Test-Format }
    'check' { Test-Format; Invoke-Native go vet ./...; Invoke-Native go test ./... }
    'icons' { Invoke-Native go run assets/genicon.go }
    'resources' {
        if (-not (Get-Command windres -ErrorAction SilentlyContinue)) {
            throw 'windres not found: install MinGW (for example MSYS2 mingw-w64-x86_64-binutils) and put it in PATH'
        }
        Push-Location assets
        try { Invoke-Native windres --use-temp-file -O coff -i kube-dump.rc -o ../cmd/kube-dump/resource_windows_amd64.syso }
        finally { Pop-Location }
    }
    'clean' { Remove-Item -Recurse -Force $Dist, "$Name.exe", $Name -ErrorAction SilentlyContinue }
    'help' { Get-Help $PSCommandPath -Examples }
}
