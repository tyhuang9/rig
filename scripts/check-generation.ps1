$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$canonical = Join-Path $root 'internal/database/migrations/001_foundation.sql'
$mirror = Join-Path $root 'db/migrations/001_foundation.sql'
if ((Get-FileHash $canonical).Hash -ne (Get-FileHash $mirror).Hash) { throw 'Migration mirror drift: copy internal/database/migrations/001_foundation.sql to db/migrations/001_foundation.sql.' }
if (-not (Select-String -Path (Join-Path $root 'api/openapi.yaml') -Pattern 'openapi: 3.1.0' -Quiet)) { throw 'OpenAPI contract is missing its 3.1 declaration.' }
