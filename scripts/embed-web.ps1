$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$source = Join-Path $root 'web/dist'
$target = Join-Path $root 'internal/controller/ui'
if (-not (Test-Path (Join-Path $source 'index.html'))) { throw 'web/dist is missing; run pnpm --dir web build first.' }
if (Test-Path $target) { Remove-Item -LiteralPath $target -Recurse -Force }
New-Item -ItemType Directory -Force -Path $target | Out-Null
Get-ChildItem -LiteralPath $source -Force | Copy-Item -Destination $target -Recurse -Force
