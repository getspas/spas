$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$logs = Join-Path $env:RUNNER_TEMP 'spas-native-tests'
New-Item -ItemType Directory -Path $logs -Force | Out-Null
$timing = [ordered]@{
  commit = $env:GITHUB_SHA
  startUtc = [DateTimeOffset]::UtcNow.ToString('o')
  endUtc = $null
  exitCode = $null
}
$timing | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath (Join-Path $logs 'command.json')
$testExit = 1
try {
  go test -count=1 -json -timeout 30m ./... 2> (Join-Path $logs 'stderr.log') |
    Tee-Object -FilePath (Join-Path $logs 'tests.jsonl')
  $testExit = $LASTEXITCODE
}
finally {
  $timing.endUtc = [DateTimeOffset]::UtcNow.ToString('o')
  $timing.exitCode = $testExit
  $timing | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath (Join-Path $logs 'command.json')
}
exit $testExit
