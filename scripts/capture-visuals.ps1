$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$screenshots = Join-Path $root 'artifacts/screenshots'
$env:HOSTD_SCREENSHOT_DIR = $screenshots
Push-Location (Join-Path $root 'web')
try {
  & pnpm build
  if ($LASTEXITCODE -ne 0) { throw 'Dashboard production build failed.' }
} finally { Pop-Location }
& powershell -ExecutionPolicy Bypass -File (Join-Path $root 'scripts/embed-web.ps1')
Push-Location (Join-Path $root 'web')
try {
  & pnpm e2e
  if ($LASTEXITCODE -ne 0) { throw 'Playwright verification failed.' }
} finally { Pop-Location }
