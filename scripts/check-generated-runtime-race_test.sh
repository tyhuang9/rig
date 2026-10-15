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
suites=(rebind-composition-native rebind-composition-startup rebind-composition-transferred rebind-composition-second-generation rebind-composition-legacy-source)

for mode in default race plain; do
  for suite in "${suites[@]}"; do
    if [[ "$mode" == default ]]; then run "$suite"; else run "$suite" "$mode"; fi
    expected=(test -json)
    if [[ "$mode" != plain ]]; then expected+=(-race); fi
    expected+=(-count=1 -timeout=32m -run)
    case "$suite" in
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

# Missing or duplicate composition rows must fail; each platform runs the same
# complete parent groups through the shared selector.
printf '%s\n' "${suites[@]}" | LC_ALL=C sort > "$temp/expected-matrix"
for workflow in generated-runtime-lifecycle-ci.yml github-deployment-ci.yml windows-controller-ci.yml; do
  sed -n 's/^          - \(rebind-composition-[a-z-]*\)\r\{0,1\}$/\1/p' "$root/.github/workflows/$workflow" | LC_ALL=C sort > "$temp/matrix"
  cmp "$temp/expected-matrix" "$temp/matrix"
done

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
for output in empty child; do
  export RIG_INGRESS_SELECTOR_OUTPUT="$output"
  must_fail run rebind-composition-startup
done
unset RIG_INGRESS_SELECTOR_OUTPUT
export RIG_INGRESS_SELECTOR_FAIL_GO=1
must_fail run rebind-composition-transferred
unset RIG_INGRESS_SELECTOR_FAIL_GO
export RIG_INGRESS_SELECTOR_FAIL_TEE=1
must_fail run rebind-composition-native
unset RIG_INGRESS_SELECTOR_FAIL_TEE
printf 'Ingress selector contract passed: 15 invocations, three matrices, eight refusal/failure cases.\n'
