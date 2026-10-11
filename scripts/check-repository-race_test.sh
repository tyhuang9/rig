#!/usr/bin/env bash
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
runner="$root/scripts/check-repository-race.sh"
temp="$(mktemp -d)"
trap 'rm -rf "$temp"' EXIT

packages="$temp/packages"
args="$temp/args"
mkdir -p "$temp/bin"
cat > "$temp/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == list ]]; then
  cat "$RIG_REPOSITORY_RACE_PACKAGES"
  if [[ "${RIG_REPOSITORY_RACE_FAIL_LIST:-}" == 1 ]]; then
    exit 31
  fi
  exit 0
fi
if [[ "$1" != test ]]; then
  exit 97
fi
printf '%s\n' "$*" >> "$RIG_REPOSITORY_RACE_ARGS"
parent=FuzzFuturePackage
for argument in "$@"; do
  case "$argument" in
    -run) parent=TestDeploymentFuture ;;
    -skip) parent=ExampleFutureFallback ;;
  esac
done
if [[ "${RIG_REPOSITORY_RACE_EMPTY_PARENT:-}" == 1 ]]; then
  parent=TestMain
fi
if [[ "${RIG_REPOSITORY_RACE_BARE_EXAMPLE:-}" == 1 ]]; then
  parent=Example
fi
if [[ "${RIG_REPOSITORY_RACE_CHILD_ONLY:-}" == 1 ]]; then
  parent=TestFuturePackage/seed
fi
printf '{"Action":"pass","Test":"%s"}\n' "$parent"
if [[ "${RIG_REPOSITORY_RACE_NO_PACKAGE_PASS:-}" != 1 ]]; then
  printf '{"Action":"pass","Package":"github.com/hostd/hostd/internal/future"}\n'
fi
if [[ "${RIG_REPOSITORY_RACE_FAIL_TEST:-}" == 1 ]]; then
  exit 32
fi
EOF
chmod +x "$temp/bin/go"

cat > "$temp/bin/tee" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${RIG_REPOSITORY_RACE_FAIL_TEE:-}" == 1 ]]; then
  cat > "$1"
  exit 33
fi
exec "$RIG_REPOSITORY_RACE_SYSTEM_TEE" "$@"
EOF
chmod +x "$temp/bin/tee"

cat > "$packages" <<'EOF'
github.com/hostd/hostd/cmd/hostd
github.com/hostd/hostd/internal/appaccess
github.com/hostd/hostd/internal/appconfig
github.com/hostd/hostd/internal/controller
github.com/hostd/hostd/internal/database
github.com/hostd/hostd/internal/generatedingress
github.com/hostd/hostd/internal/generatedrecovery
github.com/hostd/hostd/internal/jobs
github.com/hostd/hostd/internal/future
EOF

run() {
  PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$packages" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" bash "$runner" "$@" > /dev/null
}

system_tee="$(command -v tee)"

for suite in repository-remainder hostd appaccess appconfig database generatedrecovery jobs controller-lifecycle controller-remainder; do
  run "$suite"
done

[[ $(wc -l < "$args") -eq 9 ]]
grep -Fx -- 'test -json -race -count=1 -timeout=40m github.com/hostd/hostd/internal/future' "$args" > /dev/null
for package in \
  github.com/hostd/hostd/cmd/hostd \
  github.com/hostd/hostd/internal/appaccess \
  github.com/hostd/hostd/internal/appconfig \
  github.com/hostd/hostd/internal/database \
  github.com/hostd/hostd/internal/generatedrecovery \
  github.com/hostd/hostd/internal/jobs; do
  grep -Fx -- "test -json -race -count=1 -timeout=40m $package" "$args" > /dev/null
done
grep -F -- '-run ^Test(Deployment|Runtime|Latest|Reviewed|Prior|Release|Secret|Local|Resume|Generated) github.com/hostd/hostd/internal/controller' "$args" > /dev/null
grep -F -- '-skip ^Test(Deployment|Runtime|Latest|Reviewed|Prior|Release|Secret|Local|Resume|Generated) github.com/hostd/hostd/internal/controller' "$args" > /dev/null
if grep -F -- 'github.com/hostd/hostd/internal/generatedingress' "$args" > /dev/null; then
  printf 'ingress appeared in a repository race command\n' >&2
  exit 1
fi
if run unknown; then
  printf 'unknown suite succeeded\n' >&2
  exit 1
fi
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$packages" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" bash "$runner" > /dev/null 2>&1; then
  printf 'missing suite argument succeeded\n' >&2
  exit 1
fi
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$packages" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" bash "$runner" hostd extra > /dev/null 2>&1; then
  printf 'extra argument succeeded\n' >&2
  exit 1
fi
cp "$packages" "$temp/duplicate"
printf '%s\n' 'github.com/hostd/hostd/internal/future' >> "$temp/duplicate"
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$temp/duplicate" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" bash "$runner" jobs > /dev/null 2>&1; then
  printf 'duplicate package inventory succeeded\n' >&2
  exit 1
fi
grep -v -Fx 'github.com/hostd/hostd/internal/jobs' "$packages" > "$temp/missing"
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$temp/missing" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" bash "$runner" jobs > /dev/null 2>&1; then
  printf 'missing designated package succeeded\n' >&2
  exit 1
fi
cp "$packages" "$temp/malformed"
printf '%s\n' 'invalid package' >> "$temp/malformed"
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$temp/malformed" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" bash "$runner" jobs > /dev/null 2>&1; then
  printf 'malformed inventory succeeded\n' >&2
  exit 1
fi
designated_only="$temp/designated-only"
grep -v -Fx 'github.com/hostd/hostd/internal/future' "$packages" > "$designated_only"
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$designated_only" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" bash "$runner" repository-remainder > /dev/null 2>&1; then
  printf 'empty repository remainder succeeded\n' >&2
  exit 1
fi
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$packages" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" RIG_REPOSITORY_RACE_EMPTY_PARENT=1 bash "$runner" hostd > /dev/null 2>&1; then
  printf 'TestMain-only selection succeeded\n' >&2
  exit 1
fi
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$packages" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" RIG_REPOSITORY_RACE_CHILD_ONLY=1 bash "$runner" hostd > /dev/null 2>&1; then
  printf 'child-only selection succeeded\n' >&2
  exit 1
fi
PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$packages" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" RIG_REPOSITORY_RACE_BARE_EXAMPLE=1 bash "$runner" hostd > /dev/null
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$packages" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" RIG_REPOSITORY_RACE_NO_PACKAGE_PASS=1 bash "$runner" hostd > /dev/null 2>&1; then
  printf 'missing package pass succeeded\n' >&2
  exit 1
fi
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$packages" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" RIG_REPOSITORY_RACE_FAIL_LIST=1 bash "$runner" hostd > /dev/null 2>&1; then
  printf 'package discovery failure succeeded\n' >&2
  exit 1
fi
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$packages" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" RIG_REPOSITORY_RACE_FAIL_TEST=1 bash "$runner" hostd > /dev/null 2>&1; then
  printf 'go test failure succeeded\n' >&2
  exit 1
fi
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$packages" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" RIG_REPOSITORY_RACE_FAIL_TEE=1 bash "$runner" hostd > /dev/null 2>&1; then
  printf 'tee failure succeeded\n' >&2
  exit 1
fi
