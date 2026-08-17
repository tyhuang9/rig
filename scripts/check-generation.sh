#!/usr/bin/env sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
embedded="$root/internal/database/migrations"
public="$root/db/migrations"
for migration in "$embedded"/*.sql; do
  name=${migration##*/}
  test -f "$public/$name"
  cmp "$migration" "$public/$name"
done
for migration in "$public"/*.sql; do
  name=${migration##*/}
  test -f "$embedded/$name"
done
cd "$root"
go run ./cmd/openapi-gen -check
go test ./internal/controller -run '^TestOpenAPIContractMatchesRegisteredRoutes$' -count=1
