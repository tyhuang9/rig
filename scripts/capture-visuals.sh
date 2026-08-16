#!/usr/bin/env sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
export HOSTD_SCREENSHOT_DIR="$root/artifacts/screenshots"
cd "$root"
pnpm --dir web build
"$root/scripts/embed-web.sh"
pnpm --dir web e2e
