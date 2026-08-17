param([string]$DataRoot = (Join-Path $PSScriptRoot '..\.hostd-dev'))
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
& go run ./cmd/hostd --data-root $DataRoot --fake-runtime
