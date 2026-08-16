$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$canonical = Join-Path $root 'internal/database/migrations/001_foundation.sql'
$mirror = Join-Path $root 'db/migrations/001_foundation.sql'
if ((Get-FileHash $canonical).Hash -ne (Get-FileHash $mirror).Hash) { throw 'Migration mirror drift: copy internal/database/migrations/001_foundation.sql to db/migrations/001_foundation.sql.' }
Push-Location $root
try {
  & go test ./internal/controller -run '^TestOpenAPIContractMatchesRegisteredRoutes$' -count=1
  if ($LASTEXITCODE -ne 0) { throw 'OpenAPI route/schema drift check failed.' }
} finally { Pop-Location }
