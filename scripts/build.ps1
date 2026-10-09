# Build the single binary into test\bin\.
#   scripts\build.ps1            native binary
#   scripts\build.ps1 -Web       build the WebUI first, then embed it
#   scripts\build.ps1 -Target linux
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

$outDir = Join-Path $script:RepoRoot 'test\bin'
New-Item -ItemType Directory -Force -Path $outDir | Out-Null

$env:CGO_ENABLED = '0'
if ($Target -eq 'linux') {
    $env:GOOS = 'linux'
    $env:GOARCH = 'amd64'
    $out = Join-Path $outDir 'onebotnoa-linux-amd64'
} else {
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $out = Join-Path $outDir 'onebotnoa.exe'
}

Push-Location $script:RepoRoot
try {
    go build -trimpath -ldflags '-s -w -X main.version=dev' -o $out ./cmd/onebotnoa
    $code = $LASTEXITCODE
} finally {
    Pop-Location
}
if ($code -eq 0) { Write-Host "built: $out" }
exit $code
