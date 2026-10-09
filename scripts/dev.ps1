# Run the hub in development mode.
#   scripts\dev.ps1          backend only (serves the embedded/placeholder page)
#   scripts\dev.ps1 -Web     backend + Vite dev server on :5173 (hot reload)
param([switch]$Web)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'env.ps1')

$vite = $null
if ($Web) {
    $vite = Start-Process -PassThru -FilePath 'npm.cmd' -ArgumentList 'run', 'dev' -WorkingDirectory (Join-Path $script:RepoRoot 'web')
    Write-Host "vite dev server started (pid $($vite.Id)) - open http://127.0.0.1:5173"
}

Push-Location $script:RepoRoot
try {
    go run ./cmd/onebotnoa serve -config ./config.yaml
} finally {
    Pop-Location
    if ($vite) { Stop-Process -Id $vite.Id -Force -ErrorAction SilentlyContinue }
}
