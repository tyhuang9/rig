#!/usr/bin/env sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
test -f "$root/web/dist/index.html" || { echo "web/dist is missing; run pnpm --dir web build first." >&2; exit 1; }
rm -rf "$root/internal/controller/ui"
mkdir -p "$root/internal/controller/ui"
cp -R "$root/web/dist/." "$root/internal/controller/ui/"
