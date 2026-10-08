#!/usr/bin/env bash
set -euo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
suite="${1:?provide the required race suite}"
filter=()
# These complementary filters retain every ingress test and its full subtest
# tree. Both workflows use this command so their partitions cannot drift.
gateway_v2_pattern='^TestGatewayV2'
rebind_pattern='^Test(GatewayRebind|InspectGatewayRebind)'
rebind_effects_pattern='^Test(GatewayRebind(Stage|Final|Successor)|InspectGatewayRebindSuccessor)'
rebind_config_pattern='^TestGatewayRebind(StageConfig|FinalConfig)'
# Keep the concrete lifecycle and retained-current cases out of the older
# history/remainder batches. Each fallback excludes the complete family so new
# cases remain selected exactly once without extending this list of names.
rebind_typed_pattern='^TestGatewayRebindTyped'
rebind_withdrawal_pattern='^TestGatewayRebindTyped(Rollback|ForwardOnly)'
rebind_completed_pattern='^TestGatewayRebindTyped(Completed|Committed|Recovery|Undurable)'
rebind_composition_pattern='^TestGatewayRebindConcreteComposition'
rebind_second_generation_pattern='^TestGatewayRebindConcreteCompositionSecondGeneration'
rebind_legacy_source_pattern='^TestGatewayRebindConcreteCompositionLegacySource'
current_pattern='^Test(ManagedGatewayCurrent|GatewayCurrent|InspectGatewayCurrent)'
current_lan_pattern='^Test(ManagedGatewayCurrentLAN|GatewayCurrentLAN)'
case "$suite" in
  runtime-packages)
    packages=(
      ./internal/projectanalysis
      ./internal/deploymentplans
      ./internal/generatedimage
      ./internal/generatedruntime
      ./internal/generatedexecutor
      ./internal/generatedruntimestate
      ./internal/generatedrecovery
      ./internal/autodeploy
    )
    ;;
  gateway-v2)
    packages=(./internal/generatedingress)
    filter=(-run "$gateway_v2_pattern")
    ;;
  rebind-history)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_pattern" -skip "$rebind_effects_pattern|$rebind_typed_pattern|$rebind_composition_pattern")
    ;;
  rebind-config)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_config_pattern")
    ;;
  rebind-resources)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_effects_pattern" -skip "$rebind_config_pattern")
    ;;
  rebind-typed-withdrawal)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_withdrawal_pattern")
    ;;
  rebind-typed-completed)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_completed_pattern")
    ;;
  rebind-typed-runtime)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_typed_pattern" -skip "$rebind_withdrawal_pattern|$rebind_completed_pattern")
    ;;
  rebind-composition-native)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_composition_pattern" -skip "$rebind_second_generation_pattern|$rebind_legacy_source_pattern")
    ;;
  rebind-composition-second-generation)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_second_generation_pattern")
    ;;
  rebind-composition-legacy-source)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_legacy_source_pattern")
    ;;
  current-lan)
    packages=(./internal/generatedingress)
    filter=(-run "$current_lan_pattern")
    ;;
  current-serving)
    packages=(./internal/generatedingress)
    filter=(-run "$current_pattern" -skip "$current_lan_pattern")
    ;;
  ingress-remainder)
    packages=(./internal/generatedingress)
    filter=(-skip "$gateway_v2_pattern|$rebind_pattern|$current_pattern")
    ;;
  *) printf 'Unknown race suite: %s\n' "$suite" >&2; exit 1 ;;
esac
output="$(mktemp)"
trap 'rm -f "$output"' EXIT
go test -json -race -count=1 -timeout=32m "${filter[@]}" "${packages[@]}" | tee "$output"
# An empty selection must not satisfy a required race check. pipefail above
# preserves every test, race, compilation and output-capture failure.
jq -se 'any(.[]; .Action == "pass" and ((.Test // "") | test("^Test[^/]+$")))' "$output" > /dev/null
