#!/usr/bin/env bash
set -euo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
suite="${1:?provide the required test suite}"
mode="${2:-race}"
if [[ "$#" -gt 2 ]]; then
  printf 'Expected a suite and optional race/plain mode.\n' >&2
  exit 1
fi
# Linux gates retain race detection by default. Windows uses these exact
# complementary selections in plain mode, retaining all other test flags.
case "$mode" in
  race) race_flags=(-race) ;;
  plain) race_flags=() ;;
  *) printf 'Unknown test mode: %s\n' "$mode" >&2; exit 1 ;;
esac
filter=()
# These complementary filters retain every ingress test and its full subtest
# tree. All three workflows use this command so their partitions cannot drift.
gateway_v2_pattern='^TestGatewayV2'
rebind_pattern='^Test(GatewayRebind|InspectGatewayRebind)'
rebind_effects_pattern='^Test(GatewayRebind(Stage|Final|Successor)|InspectGatewayRebindSuccessor)'
rebind_config_pattern='^TestGatewayRebind(StageConfig|FinalConfig)'
rebind_current_pattern='^Test(GatewayRebind(Committed|Current)|InspectGatewayRebindCurrent)'
# The reviewed recovery cases are database-heavy; retain complete parent trees
# in a separate complementary group without extending the package deadline.
rebind_recovery_pattern='^TestGatewayRebind(ForwardConfirmation|Interactive|Reconcile|Recovery|Requested|RollbackConfirmation)'
rebind_recovery_guards_pattern='^TestGatewayRebind(ForwardConfirmation|Interactive|RollbackConfirmation)'
rebind_startup_runtime_pattern='^TestGatewayRebind(Runtime|Startup)'
rebind_handover_pattern='^TestGatewayRebindFinalHandover'
rebind_handover_rollback_pattern='^TestGatewayRebindFinalHandoverRollback'
# Keep the concrete lifecycle and retained-current cases out of the older
# history/remainder batches. Each fallback excludes the complete family so new
# cases remain selected exactly once without extending this list of names.
rebind_typed_pattern='^TestGatewayRebindTyped'
rebind_withdrawal_pattern='^TestGatewayRebindTyped(Rollback|ForwardOnly)'
rebind_completed_pattern='^TestGatewayRebindTyped(Completed|Committed|Recovery|Undurable)'
# Keep the complete typed Handover family in its own complementary group.
# Withdrawal and completed groups stay unchanged; typed-runtime retains every
# other typed-runtime parent without extending this list.
rebind_typed_handover_pattern='^TestGatewayRebindTypedHandover'
rebind_composition_pattern='^TestGatewayRebindConcreteComposition'
# Keep complete startup/adoption and transferred-lifecycle parent trees in
# complementary batches; none of these changes extends the test deadline.
rebind_composition_startup_pattern='^TestGatewayRebindConcreteComposition(ReconstructsFreshA0|RecoversUnboundCreate)'
rebind_composition_transferred_pattern='^TestGatewayRebindConcreteCompositionTransferred'
rebind_second_generation_pattern='^TestGatewayRebindConcreteCompositionSecondGeneration'
rebind_legacy_source_pattern='^TestGatewayRebindConcreteCompositionLegacySource'
current_pattern='^Test(ManagedGatewayCurrent|GatewayCurrent|InspectGatewayCurrent)'
current_lan_pattern='^Test(ManagedGatewayCurrentLAN|GatewayCurrentLAN)'
current_lan_recovery_pattern='^Test(ManagedGatewayCurrentLANRecovery|GatewayCurrentLANRecovery)'
current_runtime_pattern='^Test(ManagedGatewayCurrent|GatewayCurrent)(Physical|Serving)'
current_serving_emergency_pattern='^TestGatewayCurrent(Typed|Native|Startup|Terminal)Emergency'
current_serving_predecessor_pattern='^TestGatewayCurrentPredecessor'
case "$suite" in
  runtime-packages)
    packages=(
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
    ;;
  gateway-v2)
    packages=(./internal/generatedingress)
    filter=(-run "$gateway_v2_pattern")
    ;;
  rebind-history)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_pattern" -skip "$rebind_effects_pattern|$rebind_typed_pattern|$rebind_composition_pattern|$rebind_current_pattern|$rebind_recovery_pattern|$rebind_startup_runtime_pattern")
    ;;
  rebind-startup-runtime)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_startup_runtime_pattern")
    ;;
  rebind-recovery)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_recovery_pattern" -skip "$rebind_recovery_guards_pattern")
    ;;
  rebind-recovery-guards)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_recovery_guards_pattern")
    ;;
  rebind-current)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_current_pattern")
    ;;
  rebind-config)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_config_pattern")
    ;;
  rebind-resources)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_effects_pattern" -skip "$rebind_config_pattern|$rebind_handover_pattern")
    ;;
  rebind-handover)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_handover_pattern" -skip "$rebind_handover_rollback_pattern")
    ;;
  rebind-handover-rollback)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_handover_rollback_pattern")
    ;;
  rebind-typed-withdrawal)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_withdrawal_pattern")
    ;;
  rebind-typed-completed)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_completed_pattern")
    ;;
  rebind-typed-handover)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_typed_handover_pattern")
    ;;
  rebind-typed-runtime)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_typed_pattern" -skip "$rebind_withdrawal_pattern|$rebind_completed_pattern|$rebind_typed_handover_pattern")
    ;;
  rebind-composition-native)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_composition_pattern" -skip "$rebind_second_generation_pattern|$rebind_legacy_source_pattern|$rebind_composition_startup_pattern|$rebind_composition_transferred_pattern")
    ;;
  rebind-composition-startup)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_composition_startup_pattern")
    ;;
  rebind-composition-transferred)
    packages=(./internal/generatedingress)
    filter=(-run "$rebind_composition_transferred_pattern")
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
    filter=(-run "$current_lan_pattern" -skip "$current_lan_recovery_pattern")
    ;;
  current-lan-recovery)
    packages=(./internal/generatedingress)
    filter=(-run "$current_lan_recovery_pattern")
    ;;
  current-runtime)
    packages=(./internal/generatedingress)
    filter=(-run "$current_runtime_pattern")
    ;;
  current-serving-emergency)
    packages=(./internal/generatedingress)
    filter=(-run "$current_serving_emergency_pattern")
    ;;
  current-serving-predecessor)
    packages=(./internal/generatedingress)
    filter=(-run "$current_serving_predecessor_pattern")
    ;;
  current-serving)
    packages=(./internal/generatedingress)
    filter=(-run "$current_pattern" -skip "$current_lan_pattern|$current_runtime_pattern|$current_serving_emergency_pattern|$current_serving_predecessor_pattern")
    ;;
  ingress-remainder)
    packages=(./internal/generatedingress)
    filter=(-skip "$gateway_v2_pattern|$rebind_pattern|$current_pattern")
    ;;
  *) printf 'Unknown race suite: %s\n' "$suite" >&2; exit 1 ;;
esac
output="$(mktemp)"
trap 'rm -f "$output"' EXIT
go test -json "${race_flags[@]}" -count=1 -timeout=32m "${filter[@]}" "${packages[@]}" | tee "$output"
# An empty selection must not satisfy a required race check. pipefail above
# preserves every test, race, compilation and output-capture failure.
jq -se 'any(.[]; .Action == "pass" and ((.Test // "") | test("^Test[^/]+$")))' "$output" > /dev/null
