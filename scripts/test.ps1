# Quality gate: go vet + tests.
#   scripts\test.ps1          vet + test
#   scripts\test.ps1 -Race    vet + race test (needs a C toolchain on Windows)
param([switch]$Race)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'env.ps1')

Push-Location $script:RepoRoot
try {
    go vet ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    if ($Race) { go test -race ./... } else { go test ./... }
    $code = $LASTEXITCODE
} finally {
    Pop-Location
}
exit $code
