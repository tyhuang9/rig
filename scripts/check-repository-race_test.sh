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
run_pattern=''
skip_pattern=''
for ((index = 1; index <= $#; index++)); do
  case "${!index}" in
    -run)
      next_index=$((index + 1))
      run_pattern="${!next_index}"
      ;;
    -skip)
      next_index=$((index + 1))
      skip_pattern="${!next_index}"
      ;;
  esac
done
parent="${RIG_REPOSITORY_RACE_FAKE_PARENT:-}"
if [[ -z "$parent" ]]; then
  case "$run_pattern" in
    '^TestRunner('*) parent=TestRunnerCancellationAndProgressHaveOneTerminalOutcome ;;
    '^Test(Admitted|ScopedAdmission)') parent=TestAdmittedFuture ;;
    '^Test(CoordinatorJobRunsOwnerScopedGitHubComposeMaterialization|BlockingCompose|LegacyLocalSourceDraft|Compose)') parent=TestComposeFuture ;;
    '^Test(Deployment|Runtime|Latest|Reviewed|Prior|Release|Secret|Local|Resume|Generated)') parent=TestDeploymentFuture ;;
    '^Test(GuardPolicyGateRejectsMutableIdentityAndStageChanges|GuardPolicyGateUsesClosedPersistedStagePolicy|GuardComposeChecksUseSeparateClosedStagePolicies|GuardRouteActivationUsesExactWaitingHealthStage|GuardPolicyGateRefusesLaterRunningAuthorityAsStale|GuardPolicyGatePreservesDependencyCausesAndPostCallbackCancellation)$') parent=TestGuardPolicyGateRejectsMutableIdentityAndStageChanges ;;
    '^Test(GeneratedRuntimeCleanupOwnershipDoesNotRequireConfiguredNetwork|GeneratedNetworkBoundaryFailedCreateReinspectionPreservesCurrentCancellation|GeneratedRuntimeHealthWaitFailsClosed|GeneratedRuntimeCapacitySourceFailuresPreserveClosedDiagnostics|GeneratedNetworkBoundaryPreservesCancellationFromNilErrorInspectionResults|ReplacementReservationRejectsForeignRetentionAndReleasesUnattemptedAggregate|RetainedRuntimeRequiresOwnedNetworkForStoppedAndRunningTargets|RetainedRuntimeReservationCannotCrossEngineOrOperation|GeneratedNetworkBoundaryExistingExactNetworkRequiresFreshAuthority)$') parent=TestGeneratedRuntimeCleanupOwnershipDoesNotRequireConfiguredNetwork ;;
    '^Test(GeneratedDomainStartupSnapshotTxReturnsSortedServingPendingMigrationAndCandidateFacts|BeginCandidateTxRejectsForeignAndDriftedProvenance|GeneratedDomainStartupSnapshotTxRejectsMalformedOrCrossedStateWithoutPartialResult|RecordCandidateObservedFailureTxRejectsForeignStageAndPostRoute|RecordCandidateObservedFailureTxRejectsIncompleteOrDriftedRoster)$') parent=TestGeneratedDomainStartupSnapshotTxReturnsSortedServingPendingMigrationAndCandidateFacts ;;
    *)
      if [[ -n "$skip_pattern" ]]; then
        parent=ExampleFutureFallback
      else
        parent=FuzzFuturePackage
      fi
      ;;
  esac
fi
if [[ "${RIG_REPOSITORY_RACE_EMPTY_PARENT:-}" == 1 ]]; then
  parent=TestMain
fi
if [[ "${RIG_REPOSITORY_RACE_BARE_EXAMPLE:-}" == 1 ]]; then
  parent=Example
fi
if [[ "${RIG_REPOSITORY_RACE_CHILD_ONLY:-}" == 1 ]]; then
  parent=TestFuturePackage/seed
fi
selected=true
if [[ -n "$run_pattern" && ! "$parent" =~ $run_pattern ]]; then
  selected=false
fi
if [[ -n "$skip_pattern" && "$parent" =~ $skip_pattern ]]; then
  selected=false
fi
if [[ "$selected" == true ]]; then
  printf '{"Action":"pass","Test":"%s"}\n' "$parent"
fi
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
github.com/hostd/hostd/internal/deploymentexecution
github.com/hostd/hostd/internal/generatedruntime
github.com/hostd/hostd/internal/generatedruntimestate
github.com/hostd/hostd/internal/future
EOF

run() {
  PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$packages" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" bash "$runner" "$@" > /dev/null
}

system_tee="$(command -v tee)"

for suite in repository-remainder hostd appaccess appconfig database generatedrecovery jobs-atomic jobs-admission jobs-compose jobs-remainder deploymentexecution-heavy deploymentexecution-remainder generatedruntime-heavy generatedruntime-remainder generatedruntimestate-heavy generatedruntimestate-remainder controller-lifecycle controller-remainder; do
  run "$suite"
done

[[ $(wc -l < "$args") -eq 18 ]]
grep -Fx -- 'test -json -race -count=1 -timeout=40m github.com/hostd/hostd/internal/future' "$args" > /dev/null
for package in \
  github.com/hostd/hostd/cmd/hostd \
  github.com/hostd/hostd/internal/appaccess \
  github.com/hostd/hostd/internal/appconfig \
  github.com/hostd/hostd/internal/database \
  github.com/hostd/hostd/internal/generatedrecovery; do
  grep -Fx -- "test -json -race -count=1 -timeout=40m $package" "$args" > /dev/null
done
grep -Fx -- 'test -json -race -count=1 -timeout=40m -run ^TestRunner(CancellationAndProgress|CompletionAndCancellation)HaveOneTerminalOutcome$ github.com/hostd/hostd/internal/jobs' "$args" > /dev/null
grep -Fx -- 'test -json -race -count=1 -timeout=40m -run ^Test(Admitted|ScopedAdmission) github.com/hostd/hostd/internal/jobs' "$args" > /dev/null
grep -Fx -- 'test -json -race -count=1 -timeout=40m -run ^Test(CoordinatorJobRunsOwnerScopedGitHubComposeMaterialization|BlockingCompose|LegacyLocalSourceDraft|Compose) github.com/hostd/hostd/internal/jobs' "$args" > /dev/null
grep -Fx -- 'test -json -race -count=1 -timeout=40m -skip (^TestRunner(CancellationAndProgress|CompletionAndCancellation)HaveOneTerminalOutcome$|^Test(Admitted|ScopedAdmission)|^Test(CoordinatorJobRunsOwnerScopedGitHubComposeMaterialization|BlockingCompose|LegacyLocalSourceDraft|Compose)) github.com/hostd/hostd/internal/jobs' "$args" > /dev/null
grep -Fx -- 'test -json -race -count=1 -timeout=40m -run ^Test(GuardPolicyGateRejectsMutableIdentityAndStageChanges|GuardPolicyGateUsesClosedPersistedStagePolicy|GuardComposeChecksUseSeparateClosedStagePolicies|GuardRouteActivationUsesExactWaitingHealthStage|GuardPolicyGateRefusesLaterRunningAuthorityAsStale|GuardPolicyGatePreservesDependencyCausesAndPostCallbackCancellation)$ github.com/hostd/hostd/internal/deploymentexecution' "$args" > /dev/null
grep -Fx -- 'test -json -race -count=1 -timeout=40m -skip ^Test(GuardPolicyGateRejectsMutableIdentityAndStageChanges|GuardPolicyGateUsesClosedPersistedStagePolicy|GuardComposeChecksUseSeparateClosedStagePolicies|GuardRouteActivationUsesExactWaitingHealthStage|GuardPolicyGateRefusesLaterRunningAuthorityAsStale|GuardPolicyGatePreservesDependencyCausesAndPostCallbackCancellation)$ github.com/hostd/hostd/internal/deploymentexecution' "$args" > /dev/null
grep -Fx -- 'test -json -race -count=1 -timeout=40m -run ^Test(GeneratedRuntimeCleanupOwnershipDoesNotRequireConfiguredNetwork|GeneratedNetworkBoundaryFailedCreateReinspectionPreservesCurrentCancellation|GeneratedRuntimeHealthWaitFailsClosed|GeneratedRuntimeCapacitySourceFailuresPreserveClosedDiagnostics|GeneratedNetworkBoundaryPreservesCancellationFromNilErrorInspectionResults|ReplacementReservationRejectsForeignRetentionAndReleasesUnattemptedAggregate|RetainedRuntimeRequiresOwnedNetworkForStoppedAndRunningTargets|RetainedRuntimeReservationCannotCrossEngineOrOperation|GeneratedNetworkBoundaryExistingExactNetworkRequiresFreshAuthority)$ github.com/hostd/hostd/internal/generatedruntime' "$args" > /dev/null
grep -Fx -- 'test -json -race -count=1 -timeout=40m -skip ^Test(GeneratedRuntimeCleanupOwnershipDoesNotRequireConfiguredNetwork|GeneratedNetworkBoundaryFailedCreateReinspectionPreservesCurrentCancellation|GeneratedRuntimeHealthWaitFailsClosed|GeneratedRuntimeCapacitySourceFailuresPreserveClosedDiagnostics|GeneratedNetworkBoundaryPreservesCancellationFromNilErrorInspectionResults|ReplacementReservationRejectsForeignRetentionAndReleasesUnattemptedAggregate|RetainedRuntimeRequiresOwnedNetworkForStoppedAndRunningTargets|RetainedRuntimeReservationCannotCrossEngineOrOperation|GeneratedNetworkBoundaryExistingExactNetworkRequiresFreshAuthority)$ github.com/hostd/hostd/internal/generatedruntime' "$args" > /dev/null
grep -Fx -- 'test -json -race -count=1 -timeout=40m -run ^Test(GeneratedDomainStartupSnapshotTxReturnsSortedServingPendingMigrationAndCandidateFacts|BeginCandidateTxRejectsForeignAndDriftedProvenance|GeneratedDomainStartupSnapshotTxRejectsMalformedOrCrossedStateWithoutPartialResult|RecordCandidateObservedFailureTxRejectsForeignStageAndPostRoute|RecordCandidateObservedFailureTxRejectsIncompleteOrDriftedRoster)$ github.com/hostd/hostd/internal/generatedruntimestate' "$args" > /dev/null
grep -Fx -- 'test -json -race -count=1 -timeout=40m -skip ^Test(GeneratedDomainStartupSnapshotTxReturnsSortedServingPendingMigrationAndCandidateFacts|BeginCandidateTxRejectsForeignAndDriftedProvenance|GeneratedDomainStartupSnapshotTxRejectsMalformedOrCrossedStateWithoutPartialResult|RecordCandidateObservedFailureTxRejectsForeignStageAndPostRoute|RecordCandidateObservedFailureTxRejectsIncompleteOrDriftedRoster)$ github.com/hostd/hostd/internal/generatedruntimestate' "$args" > /dev/null
grep -F -- '-run ^Test(Deployment|Runtime|Latest|Reviewed|Prior|Release|Secret|Local|Resume|Generated) github.com/hostd/hostd/internal/controller' "$args" > /dev/null
grep -F -- '-skip ^Test(Deployment|Runtime|Latest|Reviewed|Prior|Release|Secret|Local|Resume|Generated) github.com/hostd/hostd/internal/controller' "$args" > /dev/null
if grep -F -- 'github.com/hostd/hostd/internal/generatedingress' "$args" > /dev/null; then
  printf 'ingress appeared in a repository race command\n' >&2
  exit 1
fi
for package in \
  github.com/hostd/hostd/internal/deploymentexecution \
  github.com/hostd/hostd/internal/generatedruntime \
  github.com/hostd/hostd/internal/generatedruntimestate; do
  count=$(awk -v package="$package" '{ for (field = 1; field <= NF; field++) if ($field == package) count++ } END { print count + 0 }' "$args")
  [[ "$count" -eq 2 ]]
done
if run unknown; then
  printf 'unknown suite succeeded\n' >&2
  exit 1
fi
if run jobs; then
  printf 'retired jobs suite succeeded\n' >&2
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
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$temp/duplicate" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" bash "$runner" jobs-atomic > /dev/null 2>&1; then
  printf 'duplicate package inventory succeeded\n' >&2
  exit 1
fi
grep -v -Fx 'github.com/hostd/hostd/internal/jobs' "$packages" > "$temp/missing"
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$temp/missing" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" bash "$runner" jobs-atomic > /dev/null 2>&1; then
  printf 'missing designated package succeeded\n' >&2
  exit 1
fi
cp "$packages" "$temp/malformed"
printf '%s\n' 'invalid package' >> "$temp/malformed"
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$temp/malformed" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" bash "$runner" jobs-atomic > /dev/null 2>&1; then
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
if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$packages" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" RIG_REPOSITORY_RACE_FAKE_PARENT=TestFutureJob bash "$runner" jobs-atomic > /dev/null 2>&1; then
  printf 'empty jobs atomic lane succeeded\n' >&2
  exit 1
fi
for future_parent in TestFutureJob ExampleFutureJob FuzzFutureJob TestRunnerCancellationAndProgressHaveOneTerminalOutcomeExtra; do
  PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$packages" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" RIG_REPOSITORY_RACE_FAKE_PARENT="$future_parent" bash "$runner" jobs-remainder > /dev/null
done
for partitioned_parent in TestRunnerCancellationAndProgressHaveOneTerminalOutcome TestAdmittedFuture TestComposeFuture; do
  if PATH="$temp/bin:$PATH" RIG_REPOSITORY_RACE_PACKAGES="$packages" RIG_REPOSITORY_RACE_ARGS="$args" RIG_REPOSITORY_RACE_SYSTEM_TEE="$system_tee" RIG_REPOSITORY_RACE_FAKE_PARENT="$partitioned_parent" bash "$runner" jobs-remainder > /dev/null 2>&1; then
    printf 'jobs remainder accepted partitioned parent: %s\n' "$partitioned_parent" >&2
    exit 1
  fi
done
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
