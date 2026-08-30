$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
function Import-EnvFile([string]$Path) {
    foreach ($line in Get-Content -LiteralPath $Path) {
        $text = $line.Trim()
        if ($text -and -not $text.StartsWith('#') -and $text.Contains('=')) {
            $parts = $text.Split('=', 2)
            [Environment]::SetEnvironmentVariable($parts[0].Trim(), $parts[1].Trim().Trim('"').Trim("'"), 'Process')
        }
    }
}
Import-EnvFile (Join-Path $root 'services\core\.env')
$env:PYTHONPATH = $root
$owner = (Get-NetTCPConnection -LocalPort 8100 -State Listen -ErrorAction SilentlyContinue).OwningProcess
if ($owner) { Stop-Process -Id $owner -Force; Start-Sleep -Seconds 2 }
$python = (Get-Command python).Source
Start-Process $python -ArgumentList @('-m','uvicorn','services.collectors.wechat.service:app','--host','127.0.0.1','--port','8100') -WorkingDirectory $root -WindowStyle Normal
Start-Sleep -Seconds 5
Invoke-RestMethod 'http://127.0.0.1:8100/status' | ConvertTo-Json -Depth 4
