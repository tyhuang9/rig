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
    filter=(-run "$rebind_pattern" -skip "$rebind_effects_pattern")
    ;;
  rebind-config)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_config_pattern")
    ;;
  rebind-resources)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_effects_pattern" -skip "$rebind_config_pattern")
    ;;
  ingress-remainder)
    packages=(./internal/generatedingress)
    filter=(-skip "$gateway_v2_pattern|$rebind_pattern")
    ;;
  *) printf 'Unknown race suite: %s\n' "$suite" >&2; exit 1 ;;
esac
output="$(mktemp)"
trap 'rm -f "$output"' EXIT
go test -json -race -count=1 -timeout=32m "${filter[@]}" "${packages[@]}" | tee "$output"
# An empty selection must not satisfy a required race check. pipefail above
# preserves every test, race, compilation and output-capture failure.
jq -se 'any(.[]; .Action == "pass" and ((.Test // "") | test("^Test[^/]+$")))' "$output" > /dev/null
