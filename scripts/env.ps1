# Shared dev environment for this repo. Dot-source it:
#   . "$PSScriptRoot\env.ps1"
#
# Why this exists:
#   * proxy.golang.org / goproxy.cn are unreachable here -> use a mirror.
#   * the file sandbox denies writes outside the workspace, so GOPATH, the
#     module cache, the build cache and the npm cache all live in .cache/.
#   * GOTMPDIR must exist before the first build or go fails early.
# It also puts the Go toolchain on PATH for this session, so scripts can simply
# call "go ..." (native commands avoid PowerShell parameter-binding surprises).

$script:RepoRoot = Split-Path $PSScriptRoot -Parent

if (Get-Command go -ErrorAction SilentlyContinue) {
    $script:GoExe = (Get-Command go).Source
} elseif (Test-Path 'C:\Program Files\Go\bin\go.exe') {
    $script:GoExe = 'C:\Program Files\Go\bin\go.exe'
} else {
    throw 'go toolchain not found: install Go or add it to PATH'
}

$goDir = Split-Path $script:GoExe -Parent
if (($env:PATH -split ';') -notcontains $goDir) {
    $env:PATH = $goDir + ';' + $env:PATH
}

$cacheRoot = Join-Path $script:RepoRoot '.cache'
$env:GOPATH     = Join-Path $cacheRoot 'go'
$env:GOMODCACHE = Join-Path $env:GOPATH 'pkg\mod'
$env:GOCACHE    = Join-Path $cacheRoot 'go-build'
$env:GOTMPDIR   = Join-Path $cacheRoot 'tmp'
$env:npm_config_cache = Join-Path $cacheRoot 'npm'
$env:GOPROXY    = 'https://goproxy.io,direct'
$env:GOSUMDB    = 'sum.golang.google.cn'

New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOTMPDIR, $env:npm_config_cache | Out-Null
