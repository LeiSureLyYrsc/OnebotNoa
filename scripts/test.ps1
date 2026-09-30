# Quality gate: go vet + tests.
#   scripts\test.cmd [-Race]
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test.ps1
# -Race needs cgo and a C toolchain (absent on a bare Windows box), so it is
# opt-in; CI on linux can always pass it.
param([switch]$Race)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'env.ps1')
Write-Host "go: $script:GoExe"
go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
if ($Race) { go test -race ./... } else { go test ./... }
exit $LASTEXITCODE
