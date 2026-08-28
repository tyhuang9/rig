param([string]$DataRoot = (Join-Path $PSScriptRoot '..\.hostd-dev'))
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
& go run ./cmd/hostd serve --data-root $DataRoot --fake-runtime
