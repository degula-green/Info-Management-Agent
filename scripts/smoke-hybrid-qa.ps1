param(
  [string]$BaseUrl = 'http://127.0.0.1:8082',
  [string]$Question = '系统如何记录消息来源？'
)

$ErrorActionPreference = 'Stop'
$token = $env:INFO_AGENT_JWT
if ([string]::IsNullOrWhiteSpace($token)) {
  throw 'Set INFO_AGENT_JWT to a short-lived local JWT before running this smoke test.'
}

$payload = @{
  question = $Question
  top_k = 8
  platforms = @()
  conversation_ids = @()
} | ConvertTo-Json -Compress

$headers = @{ Authorization = "Bearer $token"; Accept = 'text/event-stream' }
$response = Invoke-WebRequest -Uri ($BaseUrl.TrimEnd('/') + '/api/qa/ask') -Method Post -Headers $headers -ContentType 'application/json' -Body $payload -TimeoutSec 90
if ($response.StatusCode -ne 200) { throw "QA endpoint returned HTTP $($response.StatusCode)" }

$events = [regex]::Matches($response.Content, '(?m)^event:\s*([^\r\n]+)') | ForEach-Object { $_.Groups[1].Value }
$required = @('meta', 'delta', 'done')
foreach ($name in $required) {
  if ($events -notcontains $name) { throw "Missing SSE event: $name" }
}
if ($events[-1] -ne 'done') { throw 'SSE stream did not finish with done.' }
Write-Output ("QA smoke passed: " + (($events -join ', ')))
