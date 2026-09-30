# Build the WebUI into internal/webui/dist (the directory go:embed packages).
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'env.ps1')

$dist = Join-Path $script:RepoRoot 'internal\webui\dist'
New-Item -ItemType Directory -Force -Path $dist | Out-Null
Get-ChildItem -Path $dist -Recurse -Force |
    Where-Object { $_.Name -ne '.gitkeep' } |
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue

$webDir = Join-Path $script:RepoRoot 'web'
Push-Location $webDir
try {
    if (-not (Test-Path (Join-Path $webDir 'node_modules'))) {
        Write-Host 'installing web dependencies (npm install)...'
        npm install --no-fund --no-audit
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }
    npm run build
    $code = $LASTEXITCODE
} finally { Pop-Location }
exit $code
