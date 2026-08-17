#!/usr/bin/env sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
pnpm --dir web build
diff -qr "$root/web/dist" "$root/internal/controller/ui"
echo 'Embedded dashboard assets match the deterministic production build.'
