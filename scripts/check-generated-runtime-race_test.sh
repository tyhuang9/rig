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
  parent)
    run_pattern=''
    for ((index=1; index<=$#; index++)); do
      if [[ "${!index}" == -run && $((index + 1)) -le $# ]]; then
        next=$((index + 1))
        run_pattern="${!next}"
      fi
    done
    count=1
    case "$run_pattern" in
      *TestGeneratedDomainA0*) count=4 ;;
      *TestGeneratedDomainCold*) count=19 ;;
    esac
    for ((index=0; index<count; index++)); do
      printf '{"Action":"pass","Test":"TestSelectorControl%s"}\n' "$index"
    done
    ;;
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
rebind_pattern='^Test(GatewayRebind|InspectGatewayRebind)'
rebind_effects='^Test(GatewayRebind(Stage|Final|Successor)|InspectGatewayRebindSuccessor)'
rebind_current='^Test(GatewayRebind(Committed|Current)|InspectGatewayRebindCurrent)'
rebind_recovery='^TestGatewayRebind(ForwardConfirmation|Interactive|Reconcile|Recovery|Requested|RollbackConfirmation)'
rebind_startup_runtime='^TestGatewayRebind(Runtime|Startup)'
startup='^TestGatewayRebindConcreteComposition(ReconstructsFreshA0|RecoversUnboundCreate)'
transferred='^TestGatewayRebindConcreteCompositionTransferred'
second='^TestGatewayRebindConcreteCompositionSecondGeneration'
legacy='^TestGatewayRebindConcreteCompositionLegacySource'
typed='^TestGatewayRebindTyped'
withdrawal='^TestGatewayRebindTyped(Rollback|ForwardOnly)'
completed='^TestGatewayRebindTyped(Completed|Committed|Recovery|Undurable)'
handover='^TestGatewayRebindTypedHandover'
rebind_history_heavy='^Test(GatewayRebindCommitReplayRequiresUniqueExactReviewedHistory|GatewayRebindAbortedTerminalProofCoreIsFreshAndReadOnly|GatewayRebindRollbackTerminalObserverBindsCheckpointRoutesDigest|GatewayRebindHistoricalTerminalWithNewActiveReturnsUnavailableProofs|GatewayRebindAbortedTerminalExportedWrapperRejectsMissingPredecessorInventory|GatewayRebindTerminalAttestationWithdrawsOnlyRetainedOwner|GatewayRebindAdmissionCurrentPredecessorRejectsClosingConfigDrift)$'
domain='^TestGeneratedDomain'
domain_all_cold='^TestGeneratedDomainCold'
domain_retained='^TestGeneratedDomainCold.*Retained'
domain_startup='^TestGeneratedDomain(Startup|ProtectedStartup)'
domain_stable='^TestGeneratedDomainStartupClassifiesStableGateway'
domain_a0='^TestGeneratedDomainA0(CommittedV2NamespaceRequiresColdAccounting|CommittedV2NamespaceRequiresColdAccountingPart2|GatewayOnlyNamespaceRemainsExact|GatewayOnlyNamespaceRemainsExactPart2)'
domain_cold='^TestGeneratedDomainCold(ReconstructsExactTwoComponentCurrentRoster|ReconstructsExactTwoComponentCurrentRosterPart2|AcceptsDockerMergedCandidateContainerLabels|RejectsTwoComponentGroupAttestationDrift|RejectsTwoComponentGroupAttestationDriftPart2|RejectsPersistedBuilderNamespaceDrift|RejectsPersistedBuilderNamespaceDriftPart2|RejectsPersistedBuilderIdentityAndReadDrift|RejectsPersistedBuilderIdentityAndReadDriftPart2|RejectsPersistedBuilderPhysicalPolicyDrift|RejectsPersistedBuilderPhysicalPolicyDriftPart2|RejectsPersistedBuilderRequiredNetworkOptions|RejectsPersistedBuilderRequiredNetworkOptionsPart2|RejectsPersistedBuilderNetworkOptionCorrespondence|RejectsPersistedBuilderNetworkOptionCorrespondencePart2|RejectsPersistedBuilderFinalIdentityDrift|RejectsPersistedBuilderFinalIdentityDriftPart2|RejectsTwoComponentCensusDetails|RejectsTwoComponentCensusDetailsPart2)'
capacity='^TestGeneratedIngressCapacity'
gateway_operations='^Test(Prepare|Observe|Recover|Commit|Retire)Gateway'
old_remainder_skip='^TestGatewayV2|^Test(GatewayRebind|InspectGatewayRebind)|^Test(ManagedGatewayCurrent|GatewayCurrent|InspectGatewayCurrent)'
remainder_skip="$old_remainder_skip|$domain|$capacity|$gateway_operations"
typed_suites=(rebind-typed-withdrawal rebind-typed-completed rebind-typed-handover rebind-typed-runtime)
composition_suites=(rebind-composition-native rebind-composition-startup rebind-composition-transferred rebind-composition-second-generation rebind-composition-legacy-source)
history_suites=(rebind-history-heavy rebind-history)
remainder_suites=(domain-construction domain-cold-retained domain-cold-remainder domain-a0 domain-cold domain-startup-stable domain-startup ingress-capacity gateway-operations ingress-remainder)
suites=("${history_suites[@]}" "${typed_suites[@]}" "${composition_suites[@]}" "${remainder_suites[@]}")
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
  rebind-history-heavy
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
  domain-construction
  domain-cold-retained
  domain-cold-remainder
  domain-a0
  domain-cold
  domain-startup-stable
  domain-startup
  ingress-capacity
  gateway-operations
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
    expected+=(-count=1 -timeout=32m)
    if [[ "$suite" != ingress-remainder ]]; then expected+=(-run); fi
    case "$suite" in
      rebind-history-heavy) expected+=("$rebind_history_heavy") ;;
      rebind-history) expected+=("$rebind_pattern" -skip "$rebind_history_heavy|$rebind_effects|$typed|$composition|$rebind_current|$rebind_recovery|$rebind_startup_runtime") ;;
      rebind-typed-withdrawal) expected+=("$withdrawal") ;;
      rebind-typed-completed) expected+=("$completed") ;;
      rebind-typed-handover) expected+=("$handover") ;;
      rebind-typed-runtime) expected+=("$typed" -skip "$withdrawal|$completed|$handover") ;;
      rebind-composition-native) expected+=("$composition" -skip "$second|$legacy|$startup|$transferred") ;;
      rebind-composition-startup) expected+=("$startup") ;;
      rebind-composition-transferred) expected+=("$transferred") ;;
      rebind-composition-second-generation) expected+=("$second") ;;
      rebind-composition-legacy-source) expected+=("$legacy") ;;
      domain-construction) expected+=("$domain" -skip "$domain_startup|$domain_a0|$domain_all_cold") ;;
      domain-cold-retained) expected+=("$domain_retained" -skip "$domain_cold") ;;
      domain-cold-remainder) expected+=("$domain_all_cold" -skip "$domain_cold|$domain_retained") ;;
      domain-a0) expected+=("$domain_a0") ;;
      domain-cold) expected+=("$domain_cold") ;;
      domain-startup-stable) expected+=("$domain_stable") ;;
      domain-startup) expected+=("$domain_startup" -skip "$domain_stable") ;;
      ingress-capacity) expected+=("$capacity") ;;
      gateway-operations) expected+=("$gateway_operations") ;;
      ingress-remainder) expected+=(-skip "$remainder_skip") ;;
    esac
    expected+=(./internal/generatedingress)
    printf '%s\n' "${expected[@]}" > "$temp/expected"
    cmp "$temp/expected" "$temp/args"
  done
done

# Exercise the actual emitted filters against parent names, including future
# family members and names already owned by older partitions. Subtests inherit
# each complete parent; no slash selector may split those trees.
partition_suites=("${history_suites[@]}" "${remainder_suites[@]}")
for suite in "${partition_suites[@]}"; do
  run "$suite"
  cp "$temp/args" "$temp/args-$suite"
done
selected_by() {
  local name="$1" suite="$2" include='' exclude='' index
  local -a args
  mapfile -t args < "$temp/args-$suite"
  for ((index=0; index<${#args[@]}; index++)); do
    case "${args[index]}" in
      -run) include="${args[index+1]}" ;;
      -skip) exclude="${args[index+1]}" ;;
    esac
  done
  [[ "$include" != */* && "$exclude" != */* ]] || return 1
  [[ -z "$include" || "$name" =~ $include ]] && [[ -z "$exclude" || ! "$name" =~ $exclude ]]
}
while read -r name expected_suite; do
  actual=()
  for suite in "${partition_suites[@]}"; do
    if selected_by "$name" "$suite"; then actual+=("$suite"); fi
  done
  if [[ "$expected_suite" == none ]]; then
    [[ "${#actual[@]}" == 0 ]]
  else
    [[ "${#actual[@]}" == 1 && "${actual[0]}" == "$expected_suite" ]]
  fi
done <<'EOF'
TestGatewayRebindCommitReplayRequiresUniqueExactReviewedHistory rebind-history-heavy
TestGatewayRebindAbortedTerminalProofCoreIsFreshAndReadOnly rebind-history-heavy
TestGatewayRebindRollbackTerminalObserverBindsCheckpointRoutesDigest rebind-history-heavy
TestGatewayRebindHistoricalTerminalWithNewActiveReturnsUnavailableProofs rebind-history-heavy
TestGatewayRebindAbortedTerminalExportedWrapperRejectsMissingPredecessorInventory rebind-history-heavy
TestGatewayRebindTerminalAttestationWithdrawsOnlyRetainedOwner rebind-history-heavy
TestGatewayRebindAdmissionCurrentPredecessorRejectsClosingConfigDrift rebind-history-heavy
TestGatewayRebindHistoryFutureControl rebind-history
TestGatewayRebindTypedFutureControl none
TestGatewayRebindConcreteCompositionFutureControl none
TestInspectGatewayRebindFutureControl rebind-history
TestGeneratedDomainEmptyBootstrapEffectBoundaries domain-construction
TestGeneratedDomainConstructionReconstructsColdAccounting domain-construction
TestGeneratedDomainRegistryFutureControl domain-construction
TestGeneratedDomainColdBuilderContainerProjectionHonorsDockerTypedAndRawFallbackFields domain-cold-remainder
TestGeneratedDomainColdAcceptsCompleteRetainedV1UnderRebindOrigin domain-cold-retained
TestGeneratedDomainColdRejectsCompleteRetainedV1UnderRebindOrigin domain-cold-retained
TestGeneratedDomainColdRetainedGatewaySelectionTripleAndReadAttribution domain-cold-retained
TestGeneratedDomainColdRetainedGatewaySelectionAuthorityAndReaderAttribution domain-cold-retained
TestGeneratedDomainColdRetainedFutureControl domain-cold-retained
TestGeneratedDomainColdFutureRetainedControl domain-cold-retained
TestGeneratedDomainColdRetainedFutureControl/child domain-cold-retained
TestGeneratedDomainColdGroupCorrespondenceAttribution domain-cold-remainder
TestGeneratedDomainColdGroupCorrespondenceAttributionPart2 domain-cold-remainder
TestGeneratedDomainColdGroupStructuralAttribution domain-cold-remainder
TestGeneratedDomainColdRuntimeBoundary domain-cold-remainder
TestGeneratedDomainColdFutureControl domain-cold-remainder
TestGeneratedDomainColdFutureControl/child domain-cold-remainder
TestGeneratedDomainColdReconstructsExactTwoComponentCurrentRosterRetainedFuture domain-cold
TestGeneratedDomainA0CommittedV2NamespaceRequiresColdAccounting domain-a0
TestGeneratedDomainA0CommittedV2NamespaceRequiresColdAccountingPart2 domain-a0
TestGeneratedDomainA0GatewayOnlyNamespaceRemainsExact domain-a0
TestGeneratedDomainA0GatewayOnlyNamespaceRemainsExactPart2 domain-a0
TestGeneratedDomainColdReconstructsExactTwoComponentCurrentRoster domain-cold
TestGeneratedDomainColdReconstructsExactTwoComponentCurrentRosterPart2 domain-cold
TestGeneratedDomainColdAcceptsDockerMergedCandidateContainerLabels domain-cold
TestGeneratedDomainColdRejectsTwoComponentGroupAttestationDrift domain-cold
TestGeneratedDomainColdRejectsTwoComponentGroupAttestationDriftPart2 domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderNamespaceDrift domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderNamespaceDriftPart2 domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderIdentityAndReadDrift domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderIdentityAndReadDriftPart2 domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderPhysicalPolicyDrift domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderPhysicalPolicyDriftPart2 domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderRequiredNetworkOptions domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderRequiredNetworkOptionsPart2 domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderNetworkOptionCorrespondence domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderNetworkOptionCorrespondencePart2 domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderFinalIdentityDrift domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderFinalIdentityDriftPart2 domain-cold
TestGeneratedDomainColdRejectsTwoComponentCensusDetails domain-cold
TestGeneratedDomainColdRejectsTwoComponentCensusDetailsPart2 domain-cold
TestGeneratedDomainA0CommittedV2NamespaceRequiresColdAccounting/exact_two_app_v2_and_builder_namespace domain-a0
TestGeneratedDomainA0CommittedV2NamespaceRequiresColdAccounting/missing_second_app_frontend domain-a0
TestGeneratedDomainA0CommittedV2NamespaceRequiresColdAccounting/crossed_second_app_frontend domain-a0
TestGeneratedDomainA0CommittedV2NamespaceRequiresColdAccountingPart2/extra_selected_resource domain-a0
TestGeneratedDomainA0CommittedV2NamespaceRequiresColdAccountingPart2/unowned_prefixed_application_resource domain-a0
TestGeneratedDomainA0CommittedV2NamespaceRequiresColdAccountingPart2/crossed_persisted_builder_resource domain-a0
TestGeneratedDomainA0GatewayOnlyNamespaceRemainsExact/exact_gateway_only_namespace domain-a0
TestGeneratedDomainA0GatewayOnlyNamespaceRemainsExact/missing_gateway_object domain-a0
TestGeneratedDomainA0GatewayOnlyNamespaceRemainsExact/extra_gateway_object domain-a0
TestGeneratedDomainA0GatewayOnlyNamespaceRemainsExactPart2/crossed_gateway_expectation domain-a0
TestGeneratedDomainA0GatewayOnlyNamespaceRemainsExactPart2/malformed_gateway_expectation domain-a0
TestGeneratedDomainColdReconstructsExactTwoComponentCurrentRoster/exact_api_and_frontend_without_builder domain-cold
TestGeneratedDomainColdReconstructsExactTwoComponentCurrentRoster/exact_api_frontend_and_persisted_builder domain-cold
TestGeneratedDomainColdReconstructsExactTwoComponentCurrentRoster/writer_accepted_persisted_builder_network_options domain-cold
TestGeneratedDomainColdReconstructsExactTwoComponentCurrentRosterPart2/missing_frontend domain-cold
TestGeneratedDomainColdReconstructsExactTwoComponentCurrentRosterPart2/crossed_frontend domain-cold
TestGeneratedDomainColdAcceptsDockerMergedCandidateContainerLabels/docker_merged_authenticated_definition domain-cold
TestGeneratedDomainColdAcceptsDockerMergedCandidateContainerLabels/missing_authenticated_definition domain-cold
TestGeneratedDomainColdAcceptsDockerMergedCandidateContainerLabels/crossed_authenticated_definition domain-cold
TestGeneratedDomainColdRejectsTwoComponentGroupAttestationDrift/cold_group_image_attestation_failure domain-cold
TestGeneratedDomainColdRejectsTwoComponentGroupAttestationDrift/cold_group_container_attestation_failure domain-cold
TestGeneratedDomainColdRejectsTwoComponentGroupAttestationDrift/cold_group_network_attestation_failure domain-cold
TestGeneratedDomainColdRejectsTwoComponentGroupAttestationDriftPart2/nested_gateway_reason_remains_authoritative domain-cold
TestGeneratedDomainColdRejectsTwoComponentGroupAttestationDriftPart2/cold_group_gateway_attestation_failure domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderNamespaceDrift/orphan_persisted_builder_label domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderNamespaceDrift/missing_persisted_builder_network domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderNamespaceDrift/crossed_persisted_builder_identity domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderNamespaceDriftPart2/persisted_builder_identity_without_resources domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderNamespaceDriftPart2/cold_group_structural_fallback_remains_generic domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderIdentityAndReadDrift/persisted_builder_resources_without_identity domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderIdentityAndReadDrift/malformed_persisted_builder_identity domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderIdentityAndReadDrift/extra_persisted_builder_resource domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderIdentityAndReadDriftPart2/persisted_builder_container_read_failure domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderIdentityAndReadDriftPart2/persisted_builder_network_read_failure domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderPhysicalPolicyDrift/persisted_builder_image_read_failure domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderPhysicalPolicyDrift/invalid_persisted_builder_container_configuration domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderPhysicalPolicyDrift/stopped_persisted_builder_container domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderPhysicalPolicyDriftPart2/crossed_persisted_builder_image domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderPhysicalPolicyDriftPart2/extra_persisted_builder_network_member domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderRequiredNetworkOptions/missing_persisted_builder_census_required_network_option domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderRequiredNetworkOptions/true_persisted_builder_census_required_network_option domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderRequiredNetworkOptions/other_persisted_builder_census_required_network_option domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderRequiredNetworkOptionsPart2/missing_persisted_builder_detailed_required_network_option domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderRequiredNetworkOptionsPart2/true_persisted_builder_detailed_required_network_option domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderNetworkOptionCorrespondence/other_persisted_builder_detailed_required_network_option domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderNetworkOptionCorrespondence/crossed_persisted_builder_census_network_options domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderNetworkOptionCorrespondence/persisted_builder_network_option_added_between_reads domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderNetworkOptionCorrespondencePart2/persisted_builder_network_option_removed_between_reads domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderNetworkOptionCorrespondencePart2/persisted_builder_network_option_changed_between_reads domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderFinalIdentityDrift/crossed_persisted_builder_container_attachment domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderFinalIdentityDrift/labelled_persisted_builder_image domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderFinalIdentityDrift/persisted_builder_identity_changes_during_accounting domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderFinalIdentityDriftPart2/persisted_builder_identity_file_replaced_with_same_bytes domain-cold
TestGeneratedDomainColdRejectsPersistedBuilderFinalIdentityDriftPart2/persisted_builder_identity_reencoded_during_accounting domain-cold
TestGeneratedDomainColdRejectsTwoComponentCensusDetails/exact_rig_labels domain-cold
TestGeneratedDomainColdRejectsTwoComponentCensusDetails/application_network_shape domain-cold
TestGeneratedDomainColdRejectsTwoComponentCensusDetails/application_network_options domain-cold
TestGeneratedDomainColdRejectsTwoComponentCensusDetailsPart2/missing_frontend_image domain-cold
TestGeneratedDomainColdRejectsTwoComponentCensusDetailsPart2/missing_application_network domain-cold
TestGeneratedDomainStartupClassifiesStableGateway domain-startup-stable
TestGeneratedDomainStartupClassifiesStableGatewayFutureControl domain-startup-stable
TestGeneratedDomainStartupRequiresExactTypedRecovery domain-startup
TestGeneratedDomainProtectedStartupCoversEveryArtifactFamily domain-startup
TestGeneratedDomainStartupFutureControl domain-startup
TestGeneratedIngressCapacitySnapshotRefusesUnstableRebindCurrentAuthority ingress-capacity
TestGeneratedIngressCapacityFutureControl ingress-capacity
TestPrepareGatewayCurrentV2 gateway-operations
TestObserveGatewayRebindRollbackServingUsesRetainedAttemptWithoutEffects gateway-operations
TestRecoverGatewayRebindStartupReprovesRetainedNoEffectReceiptBeforeSQLRollback gateway-operations
TestCommitGatewayRebindClassifiesActiveReplayWithoutAdmission gateway-operations
TestRetireGatewayCurrentPredecessorsStartupUsesSelectedRebindAuthority gateway-operations
TestPrepareGatewayFutureControl gateway-operations
TestCandidateHeadSelfIsReadOnlyAndNeverInfersPredecessor ingress-remainder
TestUnrelatedIngressFutureControl ingress-remainder
TestGatewayV2FutureControl none
TestGatewayRebindFutureControl rebind-history
TestInspectGatewayRebindFutureControl rebind-history
TestManagedGatewayCurrentFutureControl none
TestGatewayCurrentFutureControl none
TestInspectGatewayCurrentFutureControl none
EOF

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
  for suite in "${partition_suites[@]}"; do must_fail run "$suite"; done
done
unset RIG_INGRESS_SELECTOR_OUTPUT
export RIG_INGRESS_SELECTOR_FAIL_GO=1
must_fail run rebind-composition-transferred
for suite in "${partition_suites[@]}"; do must_fail run "$suite"; done
unset RIG_INGRESS_SELECTOR_FAIL_GO
export RIG_INGRESS_SELECTOR_FAIL_TEE=1
must_fail run rebind-composition-native
for suite in "${partition_suites[@]}"; do must_fail run "$suite"; done
unset RIG_INGRESS_SELECTOR_FAIL_TEE
printf 'Generated runtime selector contract passed: argument, partition, three-matrix and refusal/failure controls.\n'
