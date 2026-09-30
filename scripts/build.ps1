# Build the single binary.
#   scripts\build.cmd [-Web] [-Target native|linux]
param(
    [switch]$Web,
    [ValidateSet('native', 'linux')][string]$Target = 'native'
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'env.ps1')

if ($Web) {
    & (Join-Path $PSScriptRoot 'build-web.ps1')
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

$binDir = Join-Path $script:RepoRoot 'bin'
New-Item -ItemType Directory -Force -Path $binDir | Out-Null

$ldflags = '-s -w -X main.version=dev'
$env:CGO_ENABLED = '0'
if ($Target -eq 'linux') {
    $env:GOOS = 'linux'
    $env:GOARCH = 'amd64'
    $out = Join-Path $binDir 'onebotnoa-linux-amd64'
} else {
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $out = Join-Path $binDir 'onebotnoa.exe'
}

Push-Location $script:RepoRoot
try {
    go build -trimpath -ldflags $ldflags -o $out ./cmd/onebotnoa
    $code = $LASTEXITCODE
} finally { Pop-Location }
if ($code -eq 0) { Write-Host "built: $out" }
exit $code
