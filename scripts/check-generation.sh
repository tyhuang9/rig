#!/usr/bin/env sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cmp "$root/internal/database/migrations/001_foundation.sql" "$root/db/migrations/001_foundation.sql"
grep -q '^openapi: 3.1.0$' "$root/api/openapi.yaml"
