#!/usr/bin/env sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cmp "$root/internal/database/migrations/001_foundation.sql" "$root/db/migrations/001_foundation.sql"
cd "$root"
go run ./cmd/openapi-gen -check
go test ./internal/controller -run '^TestOpenAPIContractMatchesRegisteredRoutes$' -count=1
