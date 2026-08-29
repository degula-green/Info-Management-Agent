$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$corePath = Join-Path $projectRoot "services\core"
$ragPath = Join-Path $projectRoot "services\rag"
$webPath = Join-Path $projectRoot "apps\web"
$nginxPath = Join-Path $projectRoot "gateway\nginx"
$nginxRuntime = "C:\info-agent-nginx"
$ragRuntime = Join-Path $ragPath ".runtime\python"
$ragRequirements = Join-Path $ragPath "requirements.txt"
$ragRequirementsStamp = Join-Path $ragRuntime ".requirements.sha256"

function Find-Tool([string]$Name, [string[]]$Candidates = @()) {
    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if ($command) { return $command.Source }
    foreach ($candidate in $Candidates) { if (Test-Path $candidate) { return $candidate } }
    return $null
}

function Start-ServiceWindow([string]$Title, [string]$WorkingDirectory, [string]$Command) {
    $safeTitle = $Title.Replace("'", "''")
    $safeDir = $WorkingDirectory.Replace("'", "''")
    $script = "`$Host.UI.RawUI.WindowTitle = '$safeTitle'; Set-Location -LiteralPath '$safeDir'; $Command"
    $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($script))
    if (Get-Command wt.exe -ErrorAction SilentlyContinue) {
        Start-Process wt.exe -ArgumentList @(
            '-w', '0', 'new-tab',
            'powershell.exe', '-NoLogo', '-NoExit', '-ExecutionPolicy', 'Bypass',
            '-EncodedCommand', $encoded
        ) | Out-Null
    } else {
        Start-Process powershell.exe -ArgumentList @(
            '-NoLogo', '-NoExit', '-ExecutionPolicy', 'Bypass', '-EncodedCommand', $encoded
        ) | Out-Null
    }
}

function Import-EnvFile([string]$Path) {
    if (-not (Test-Path $Path)) { return }
    foreach ($line in Get-Content -LiteralPath $Path) {
        $text = $line.Trim()
        if ($text -and -not $text.StartsWith('#') -and $text.Contains('=')) {
            $parts = $text.Split('=', 2)
            [Environment]::SetEnvironmentVariable($parts[0].Trim(), $parts[1].Trim().Trim('"').Trim("'"), 'Process')
        }
    }
}

$go = Find-Tool 'go' @('C:\Program Files\Go\bin\go.exe')
$npm = Find-Tool 'npm'
$pythonCandidates = @('C:\Program Files\Python311\python.exe')
if ($env:USERPROFILE) {
    $pythonCandidates += Join-Path $env:USERPROFILE 'AppData\Local\Programs\Python\Python314\python.exe'
}
$python = Find-Tool 'python' $pythonCandidates
$uvCandidates = @()
if ($env:USERPROFILE) {
    $uvCandidates += Join-Path $env:USERPROFILE '.local\bin\uv.exe'
    $uvCandidates += Get-ChildItem -Path (Join-Path $env:USERPROFILE 'AppData\Roaming\Python') -Filter 'uv.exe' -File -Recurse -ErrorAction SilentlyContinue |
        Select-Object -ExpandProperty FullName
}
if ($env:APPDATA) {
    $pythonUserRoot = Join-Path $env:APPDATA 'Python'
    if (Test-Path $pythonUserRoot) {
        $uvCandidates += Get-ChildItem -Path $pythonUserRoot -Filter 'uv.exe' -File -Recurse -ErrorAction SilentlyContinue |
            Select-Object -ExpandProperty FullName
    }
}
if ($python) {
    $pythonUserScripts = & $python -c "import sysconfig; print(sysconfig.get_path('scripts', 'nt_user') or '')" 2>$null
    if ($pythonUserScripts) {
        $uvCandidates += Join-Path $pythonUserScripts.Trim() 'uv.exe'
    }
}
$uv = Find-Tool 'uv' $uvCandidates
$nginx = Find-Tool 'nginx'

if (-not $go) { throw "Go not found. Install Go first." }
if (-not $npm) { throw "npm not found. Install Node.js first." }
if (-not $python) { throw "Python not found. Install Python 3.11 first." }

if (-not (Test-Path (Join-Path $webPath 'node_modules'))) {
    Write-Host 'Installing frontend dependencies...'
    Push-Location $webPath
    try {
        & $npm ci
        if ($LASTEXITCODE -ne 0) { throw "npm ci failed with exit code $LASTEXITCODE." }
    } finally {
        Pop-Location
    }
}

$requirementsHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $ragRequirements).Hash
$installedHash = if (Test-Path $ragRequirementsStamp) {
    (Get-Content -Raw -LiteralPath $ragRequirementsStamp).Trim()
} else {
    ''
}
if (-not (Test-Path (Join-Path $ragRuntime 'uvicorn')) -or $installedHash -ne $requirementsHash) {
    New-Item -ItemType Directory -Force -Path $ragRuntime | Out-Null
    if ($uv) {
        Write-Host 'Installing RAG dependencies with uv...'
        & $uv pip install --python $python --target $ragRuntime --link-mode copy --upgrade --requirement $ragRequirements
        if ($LASTEXITCODE -ne 0) { throw "uv pip install failed with exit code $LASTEXITCODE." }
    } else {
        Write-Warning 'uv not found; installing RAG dependencies with Python pip.'
        & $python -m pip install --target $ragRuntime --upgrade --requirement $ragRequirements
        if ($LASTEXITCODE -ne 0) { throw "pip install failed with exit code $LASTEXITCODE." }
    }
    Set-Content -LiteralPath $ragRequirementsStamp -Value $requirementsHash -Encoding ascii
}

Import-EnvFile (Join-Path $corePath '.env')
Import-EnvFile (Join-Path $ragPath '.env')

Start-ServiceWindow 'info-agent core :8080' $corePath "& '$go' run ./cmd/server"
Start-ServiceWindow 'info-agent rag :8000' $ragPath "`$env:PYTHONPATH = '$ragRuntime'; & '$python' -m uvicorn app.main:app --host 0.0.0.0 --port 8000"
Start-ServiceWindow 'info-agent web :5173' $webPath "& '$npm' run dev -- --host 0.0.0.0"

if ($nginx) {
    New-Item -ItemType Directory -Force -Path (Join-Path $nginxRuntime 'conf.d') | Out-Null
    New-Item -ItemType Directory -Force -Path (Join-Path $nginxRuntime 'logs') | Out-Null
    New-Item -ItemType Directory -Force -Path (Join-Path $nginxRuntime 'temp\client_body_temp'), (Join-Path $nginxRuntime 'temp\proxy_temp'), (Join-Path $nginxRuntime 'temp\fastcgi_temp'), (Join-Path $nginxRuntime 'temp\uwsgi_temp'), (Join-Path $nginxRuntime 'temp\scgi_temp') | Out-Null
    Copy-Item (Join-Path $nginxPath 'nginx.conf') (Join-Path $nginxRuntime 'nginx.conf') -Force
    Copy-Item (Join-Path $nginxPath 'conf.d\default.conf') (Join-Path $nginxRuntime 'conf.d\default.conf') -Force
    Start-ServiceWindow 'info-agent nginx :80' $nginxRuntime "& '$nginx' -p '$nginxRuntime' -c nginx.conf -g 'daemon off;'"
    Write-Host 'Gateway started on http://localhost:80'
} else {
    Write-Warning 'nginx not found. Core, RAG and Web were started; gateway was skipped.'
    Write-Host 'Install nginx and add it to PATH, then run this file again.'
}

Write-Host 'Core: http://localhost:8080/health'
Write-Host 'RAG:  http://localhost:8000/health'
Write-Host 'Web:  http://localhost:5173'
Read-Host 'Press Enter to close this launcher'
