#!/usr/bin/env bash
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
runner="$root/scripts/check-generated-runtime-race.sh"
temp_parent="$(cd -- "${TMPDIR:-/tmp}" && pwd -P)"
temp="$(mktemp -d "$temp_parent/rig-ingress-selector.XXXXXXXX")"
cleanup() {
  [[ "$(dirname -- "$temp")" == "$temp_parent" && "$(basename -- "$temp")" == rig-ingress-selector.* ]] || return 1
  rm -rf -- "$temp"
}
trap cleanup EXIT
mkdir "$temp/bin"

cat > "$temp/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" > "$RIG_INGRESS_SELECTOR_ARGS"
case "${RIG_INGRESS_SELECTOR_OUTPUT:-parent}" in
  parent) printf '{"Action":"pass","Test":"TestSelectorControl"}\n' ;;
  child) printf '{"Action":"pass","Test":"TestSelectorControl/child"}\n' ;;
  empty) : ;;
esac
printf '{"Action":"pass","Package":"github.com/hostd/hostd/internal/generatedingress"}\n'
if [[ "${RIG_INGRESS_SELECTOR_FAIL_GO:-}" == 1 ]]; then exit 32; fi
EOF
cat > "$temp/bin/tee" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${RIG_INGRESS_SELECTOR_FAIL_TEE:-}" == 1 ]]; then cat > "$1"; exit 33; fi
exec "$RIG_INGRESS_SELECTOR_SYSTEM_TEE" "$@"
EOF
chmod +x "$temp/bin/go" "$temp/bin/tee"
system_tee="$(command -v tee)"
run() {
  PATH="$temp/bin:$PATH" RIG_INGRESS_SELECTOR_ARGS="$temp/args" \
    RIG_INGRESS_SELECTOR_SYSTEM_TEE="$system_tee" bash "$runner" "$@" > /dev/null
}

composition='^TestGatewayRebindConcreteComposition'
startup='^TestGatewayRebindConcreteComposition(ReconstructsFreshA0|RecoversUnboundCreate)'
transferred='^TestGatewayRebindConcreteCompositionTransferred'
second='^TestGatewayRebindConcreteCompositionSecondGeneration'
legacy='^TestGatewayRebindConcreteCompositionLegacySource'
typed='^TestGatewayRebindTyped'
withdrawal='^TestGatewayRebindTyped(Rollback|ForwardOnly)'
completed='^TestGatewayRebindTyped(Completed|Committed|Recovery|Undurable)'
handover='^TestGatewayRebindTypedHandover'
typed_suites=(rebind-typed-withdrawal rebind-typed-completed rebind-typed-handover rebind-typed-runtime)
composition_suites=(rebind-composition-native rebind-composition-startup rebind-composition-transferred rebind-composition-second-generation rebind-composition-legacy-source)
suites=("${typed_suites[@]}" "${composition_suites[@]}")
runtime_packages=(
  ./internal/projectanalysis
  ./internal/deploymentplans
  ./internal/generatedimage
  ./internal/generatedimagestate
  ./internal/generatedmigration
  ./internal/generatedruntime
  ./internal/generatedexecutor
  ./internal/generatedruntimestate
  ./internal/generatedrecovery
  ./internal/autodeploy
)
runtime_package_suites=(runtime-packages-core runtime-packages-executor runtime-packages-state runtime-packages-recovery)
ingress_suites=(
  gateway-v2
  rebind-history
  rebind-startup-runtime
  rebind-recovery
  rebind-recovery-guards
  rebind-current
  rebind-config
  rebind-resources
  rebind-handover
  rebind-handover-rollback
  rebind-typed-withdrawal
  rebind-typed-completed
  rebind-typed-handover
  rebind-typed-runtime
  rebind-composition-native
  rebind-composition-startup
  rebind-composition-transferred
  rebind-composition-second-generation
  rebind-composition-legacy-source
  current-lan
  current-lan-recovery
  current-runtime
  current-serving-emergency
  current-serving-predecessor
  current-serving
  ingress-remainder
)
runtime_packages_for_suite() {
  case "$1" in
    runtime-packages) printf '%s\n' "${runtime_packages[@]}" ;;
    runtime-packages-core) printf '%s\n' "${runtime_packages[@]:0:6}" "${runtime_packages[9]}" ;;
    runtime-packages-executor) printf '%s\n' "${runtime_packages[6]}" ;;
    runtime-packages-state) printf '%s\n' "${runtime_packages[7]}" ;;
    runtime-packages-recovery) printf '%s\n' "${runtime_packages[8]}" ;;
    *) printf 'unknown runtime package suite: %s\n' "$1" >&2; return 1 ;;
  esac
}

LC_ALL=C printf '%s\n' "${runtime_packages[@]}" | LC_ALL=C sort > "$temp/runtime-packages-expected"
for suite in "${runtime_package_suites[@]}"; do
  runtime_packages_for_suite "$suite"
done | LC_ALL=C sort > "$temp/runtime-packages-actual"
cmp "$temp/runtime-packages-expected" "$temp/runtime-packages-actual"
if LC_ALL=C sort "$temp/runtime-packages-actual" | uniq -d | grep -q .; then
  printf 'runtime package split contains duplicate packages\n' >&2
  exit 1
fi

for mode in default race plain; do
  # Keep the legacy selector and every hosted complementary package selector
  # in both the race and Windows plain modes.
  for suite in runtime-packages "${runtime_package_suites[@]}"; do
    if [[ "$mode" == default ]]; then run "$suite"; else run "$suite" "$mode"; fi
    expected=(test -json)
    if [[ "$mode" != plain ]]; then expected+=(-race); fi
    expected+=(-count=1 -timeout=32m)
    mapfile -t package_args < <(runtime_packages_for_suite "$suite")
    expected+=("${package_args[@]}")
    printf '%s\n' "${expected[@]}" > "$temp/expected"
    cmp "$temp/expected" "$temp/args"
  done

  for suite in "${suites[@]}"; do
    if [[ "$mode" == default ]]; then run "$suite"; else run "$suite" "$mode"; fi
    expected=(test -json)
    if [[ "$mode" != plain ]]; then expected+=(-race); fi
    expected+=(-count=1 -timeout=32m -run)
    case "$suite" in
      rebind-typed-withdrawal) expected+=("$withdrawal") ;;
      rebind-typed-completed) expected+=("$completed") ;;
      rebind-typed-handover) expected+=("$handover") ;;
      rebind-typed-runtime) expected+=("$typed" -skip "$withdrawal|$completed|$handover") ;;
      rebind-composition-native) expected+=("$composition" -skip "$second|$legacy|$startup|$transferred") ;;
      rebind-composition-startup) expected+=("$startup") ;;
      rebind-composition-transferred) expected+=("$transferred") ;;
      rebind-composition-second-generation) expected+=("$second") ;;
      rebind-composition-legacy-source) expected+=("$legacy") ;;
    esac
    expected+=(./internal/generatedingress)
    printf '%s\n' "${expected[@]}" > "$temp/expected"
    cmp "$temp/expected" "$temp/args"
  done
done

# The generated-runtime workflow selects each exact package batch once. The
# two ingress-only workflows retain the complete matching ingress list.
workflow_matrix() {
  awk -v job="$2" '
    { sub(/\r$/, "") }
    $0 == "  " job ":" { in_job = 1; next }
    in_job && $0 == "    steps:" { exit }
    in_job && $0 ~ /^          - / {
      sub(/^[[:space:]]*- /, "")
      print
    }
  ' "$1"
}
assert_matrix() {
  local workflow="$1"
  local job="$2"
  local expected_file="$3"
  workflow_matrix "$root/.github/workflows/$workflow" "$job" > "$temp/matrix"
  cmp "$expected_file" "$temp/matrix"
}
workflow_job_has_line() {
  local workflow="$1"
  local job="$2"
  local expected="$3"
  awk -v job="$job" -v expected="$expected" '
    { sub(/\r$/, "") }
    $0 == "  " job ":" { in_job = 1; next }
    in_job && /^  [[:alnum:]_-]+:$/ { exit }
    in_job && $0 == expected { found = 1 }
    END { exit(found ? 0 : 1) }
  ' "$root/.github/workflows/$workflow"
}
printf '%s\n' "${runtime_package_suites[@]}" "${ingress_suites[@]}" > "$temp/generated-runtime-matrix"
printf '%s\n' "${ingress_suites[@]}" > "$temp/ingress-matrix"
assert_matrix generated-runtime-lifecycle-ci.yml generated-runtime-race "$temp/generated-runtime-matrix"
assert_matrix github-deployment-ci.yml ingress-race "$temp/ingress-matrix"
assert_matrix windows-controller-ci.yml windows-ingress "$temp/ingress-matrix"

# Retain the aggregate gates and the 34-minute hosted step deadline while the
# runner keeps the race and 32-minute Go timeout assertions above.
workflow_job_has_line generated-runtime-lifecycle-ci.yml generated-runtime-race-gate '    needs: generated-runtime-race'
workflow_job_has_line generated-runtime-lifecycle-ci.yml generated-runtime-race '        timeout-minutes: 34'
workflow_job_has_line github-deployment-ci.yml postgres-race-gate '    needs: [postgres-race, repository-race, ingress-race]'
workflow_job_has_line github-deployment-ci.yml ingress-race '        timeout-minutes: 34'
workflow_job_has_line windows-controller-ci.yml windows-controller '    needs: [windows-controller-core, windows-ingress]'
workflow_job_has_line windows-controller-ci.yml windows-ingress '        timeout-minutes: 34'

must_fail() {
  if "$@" > /dev/null 2>&1; then
    printf 'invalid invocation or failed execution satisfied the selector gate\n' >&2
    exit 1
  fi
}
must_fail run
must_fail run unknown
must_fail run rebind-composition-native invalid
must_fail run rebind-composition-native race extra
must_fail run rebind-typed-handover invalid
must_fail run rebind-typed-runtime race extra
for output in empty child; do
  export RIG_INGRESS_SELECTOR_OUTPUT="$output"
  must_fail run runtime-packages
  must_fail run rebind-composition-startup
  must_fail run rebind-typed-handover
  must_fail run rebind-typed-runtime
done
unset RIG_INGRESS_SELECTOR_OUTPUT
export RIG_INGRESS_SELECTOR_FAIL_GO=1
must_fail run rebind-composition-transferred
unset RIG_INGRESS_SELECTOR_FAIL_GO
export RIG_INGRESS_SELECTOR_FAIL_TEE=1
must_fail run rebind-composition-native
unset RIG_INGRESS_SELECTOR_FAIL_TEE
printf 'Generated runtime selector contract passed: 42 invocations, three matrices, sixteen refusal/failure cases.\n'
