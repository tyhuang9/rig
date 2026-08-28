$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$canonicalDirectory = Join-Path $root 'internal/database/migrations'
$mirrorDirectory = Join-Path $root 'db/migrations'
$canonicalNames = @(Get-ChildItem $canonicalDirectory -Filter '*.sql' -File | Sort-Object Name | ForEach-Object Name)
$mirrorNames = @(Get-ChildItem $mirrorDirectory -Filter '*.sql' -File | Sort-Object Name | ForEach-Object Name)
if (($canonicalNames -join "`n") -ne ($mirrorNames -join "`n")) { throw 'Migration mirror drift: embedded and public migration file lists differ.' }
foreach ($name in $canonicalNames) {
  $canonical = Join-Path $canonicalDirectory $name
  $mirror = Join-Path $mirrorDirectory $name
  if ((Get-FileHash $canonical).Hash -ne (Get-FileHash $mirror).Hash) { throw "Migration mirror drift: $name differs." }
}
Push-Location $root
try {
  & go run ./cmd/openapi-gen -check
  if ($LASTEXITCODE -ne 0) { throw 'Generated OpenAPI Go/TypeScript artifacts drifted.' }
  & go test ./internal/controller -run '^TestOpenAPIContractMatchesRegisteredRoutes$' -count=1
  if ($LASTEXITCODE -ne 0) { throw 'OpenAPI route/schema drift check failed.' }
} finally { Pop-Location }
