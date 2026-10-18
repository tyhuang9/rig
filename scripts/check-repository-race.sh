#!/usr/bin/env bash
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

if [[ "$#" -ne 1 ]]; then
  printf 'Expected exactly one repository race suite.\n' >&2
  exit 1
fi
suite="$1"

module='github.com/hostd/hostd'
ingress_package="$module/internal/generatedingress"
controller_package="$module/internal/controller"
jobs_package="$module/internal/jobs"
controller_lifecycle_pattern='^Test(Deployment|Runtime|Latest|Reviewed|Prior|Release|Secret|Local|Resume|Generated)'
jobs_atomic_pattern='^TestRunner(CancellationAndProgress|CompletionAndCancellation)HaveOneTerminalOutcome$'
jobs_admission_pattern='^Test(Admitted|ScopedAdmission)'
jobs_compose_pattern='^Test(CoordinatorJobRunsOwnerScopedGitHubComposeMaterialization|BlockingCompose|LegacyLocalSourceDraft|Compose)'
# The exact atomic parent is excluded as an exact match. The admission and
# Compose families retain their existing prefix semantics so future parents in
# those families remain in their designated lanes. Deriving this union from
# the three positive selectors keeps the catch-all partition in lockstep.
jobs_remainder_skip_pattern="(${jobs_atomic_pattern}|${jobs_admission_pattern}|${jobs_compose_pattern})"
heavy_packages=(
  "$module/cmd/hostd"
  "$module/internal/appaccess"
  "$module/internal/appconfig"
  "$module/internal/database"
  "$module/internal/generatedrecovery"
  "$jobs_package"
)
designated_packages=("$ingress_package" "$controller_package" "${heavy_packages[@]}")

package_list="$(mktemp)"
output="$(mktemp)"
trap 'rm -f "$package_list" "$output"' EXIT

go list ./... > "$package_list"
mapfile -t all_packages < "$package_list"
if (( ${#all_packages[@]} == 0 )); then
  printf 'Go package discovery returned no packages.\n' >&2
  exit 1
fi

declare -A package_counts=()
for package in "${all_packages[@]}"; do
  if [[ ! "$package" =~ ^github\.com/hostd/hostd(/[A-Za-z0-9._-]+)*$ ]]; then
    printf 'Malformed Go package inventory entry: %s\n' "$package" >&2
    exit 1
  fi
  if [[ ${package_counts["$package"]:-0} -ne 0 ]]; then
    printf 'Duplicate Go package inventory entry: %s\n' "$package" >&2
    exit 1
  fi
  package_counts["$package"]=1
done
for package in "${designated_packages[@]}"; do
  if [[ ${package_counts["$package"]:-0} -ne 1 ]]; then
    printf 'Required Go package must appear exactly once: %s\n' "$package" >&2
    exit 1
  fi
done

packages=()
filters=()
case "$suite" in
  repository-remainder)
    for package in "${all_packages[@]}"; do
      selected=true
      for designated in "${designated_packages[@]}"; do
        if [[ "$package" == "$designated" ]]; then
          selected=false
          break
        fi
      done
      if [[ "$selected" == true ]]; then
        packages+=("$package")
      fi
    done
    ;;
  hostd)
    packages=("$module/cmd/hostd")
    ;;
  appaccess)
    packages=("$module/internal/appaccess")
    ;;
  appconfig)
    packages=("$module/internal/appconfig")
    ;;
  database)
    packages=("$module/internal/database")
    ;;
  generatedrecovery)
    packages=("$module/internal/generatedrecovery")
    ;;
  jobs-atomic)
    packages=("$jobs_package")
    filters=(-run "$jobs_atomic_pattern")
    ;;
  jobs-admission)
    packages=("$jobs_package")
    filters=(-run "$jobs_admission_pattern")
    ;;
  jobs-compose)
    packages=("$jobs_package")
    filters=(-run "$jobs_compose_pattern")
    ;;
  jobs-remainder)
    packages=("$jobs_package")
    filters=(-skip "$jobs_remainder_skip_pattern")
    ;;
  controller-lifecycle)
    packages=("$controller_package")
    filters=(-run "$controller_lifecycle_pattern")
    ;;
  controller-remainder)
    packages=("$controller_package")
    filters=(-skip "$controller_lifecycle_pattern")
    ;;
  *)
    printf 'Unknown repository race suite: %s\n' "$suite" >&2
    exit 1
    ;;
esac

if (( ${#packages[@]} == 0 )); then
  printf 'Repository race suite selected no packages: %s\n' "$suite" >&2
  exit 1
fi

go test -json -race -count=1 -timeout=40m "${filters[@]}" "${packages[@]}" | tee "$output"
# A successful command that selects no runnable Test, Example, or Fuzz parent
# must fail the required lane. The package pass separately proves the selected
# package test binary completed.
jq -se '
  any(.[]; .Action == "pass" and (.Test? != "TestMain") and ((.Test // "") | test("^(Test|Example|Fuzz)[^/]*$")))
  and any(.[]; .Action == "pass" and (.Package? != null) and (.Test? == null))
' "$output" > /dev/null
