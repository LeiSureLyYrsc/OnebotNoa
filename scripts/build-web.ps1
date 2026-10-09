# Build the WebUI into internal\webui\dist (embedded by go:embed).
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'env.ps1')

Push-Location (Join-Path $script:RepoRoot 'web')
try {
    if (-not (Test-Path 'node_modules')) {
        npm install --no-fund --no-audit
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }
    npm run build
    $code = $LASTEXITCODE
} finally {
    Pop-Location
}
exit $code
