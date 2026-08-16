$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$state = Join-Path $root '.hostd-dev'
if (-not (Test-Path $state)) { Write-Output 'No local runtime state to scan.'; exit 0 }
$forbidden = @('fixture-secret-value')
foreach ($value in $forbidden) {
  $hits = Get-ChildItem -LiteralPath $state -Recurse -File -Exclude '*.png','*.exe' | Select-String -SimpleMatch -Pattern $value
  if ($hits) { throw "Potential secret fixture leaked into repository: $value" }
}
Write-Output 'No known fixture auth or secret values found in tracked source.'
