# Stop the hub started by test\setup.ps1.
$ErrorActionPreference = 'SilentlyContinue'
$data = Join-Path (Resolve-Path (Join-Path $PSScriptRoot '..')).Path 'test\data'
$pidFile = Join-Path $data 'hub.pid'
if (Test-Path $pidFile) {
    $procId = [int](Get-Content $pidFile)
    Stop-Process -Id $procId -Force
    Remove-Item $pidFile -Force
    Write-Host "stopped pid $procId"
} else {
    Get-Process onebotnoa -ErrorAction SilentlyContinue | Stop-Process -Force
    Write-Host "no pid file; stopped any onebotnoa process"
}
