$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$source = Join-Path $root 'web/dist'
$target = Join-Path $root 'internal/controller/ui'
Push-Location $root
try {
  & pnpm --dir web build
  if ($LASTEXITCODE -ne 0) { throw 'Dashboard production build failed.' }
} finally { Pop-Location }
$sourceFiles = Get-ChildItem -LiteralPath $source -Recurse -File | ForEach-Object { $_.FullName.Substring($source.Length + 1) } | Sort-Object
$targetFiles = Get-ChildItem -LiteralPath $target -Recurse -File | ForEach-Object { $_.FullName.Substring($target.Length + 1) } | Sort-Object
if (Compare-Object $sourceFiles $targetFiles) { throw 'Embedded asset file set drifted; run scripts/embed-web.ps1.' }
foreach ($relative in $sourceFiles) {
  $sourceHash = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $source $relative)).Hash
  $targetHash = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $target $relative)).Hash
  if ($sourceHash -ne $targetHash) { throw "Embedded asset drift: $relative" }
}
Write-Output 'Embedded dashboard assets match the deterministic production build.'
