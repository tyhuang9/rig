#!/usr/bin/env sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
test -d "$root/.hostd-dev" || exit 0
! grep -R --exclude='*.png' --exclude='*.exe' -F 'fixture-secret-value' "$root/.hostd-dev"
