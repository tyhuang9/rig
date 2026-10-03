package generatedingress

import (
	"context"
	"errors"
	"os"
	"sort"
	"testing"
)

func TestClassifyGatewayV2RollbackRetirementAcceptsOnlyExactPartialCleanup(t *testing.T) {
	source, state, journal, observation := gatewayV2RollbackRetirementIdentityFixture(t)

	got, ok := classifyGatewayV2RollbackRetirement(source, state, journal, observation)
	if !ok || got != (gatewayV2RollbackRetirementObservation{IngressNetworkPresent: true, ConfigVolumePresent: true, DataVolumePresent: true}) {
		t.Fatalf("full idle infrastructure = %+v, ok=%t", got, ok)
	}

	observation.IngressFound = false
	observation.IngressNetwork = caddyNetworkInspection{}
	observation.IngressNetworkID = ""
	observation.OwnedNetworks = []string{}
	got, ok = classifyGatewayV2RollbackRetirement(source, state, journal, observation)
	if !ok || got != (gatewayV2RollbackRetirementObservation{ConfigVolumePresent: true, DataVolumePresent: true}) {
		t.Fatalf("network-removed partial cleanup = %+v, ok=%t", got, ok)
	}

	observation.ConfigVolumeFound = false
	observation.ConfigVolume = volumeInspection{}
	observation.ConfigVolumeIdentity = gatewayV1VolumeIdentity{}
	observation.OwnedVolumes = []string{state.Identity.DataVolume}
	got, ok = classifyGatewayV2RollbackRetirement(source, state, journal, observation)
	if !ok || got != (gatewayV2RollbackRetirementObservation{DataVolumePresent: true}) {
		t.Fatalf("single-volume partial cleanup = %+v, ok=%t", got, ok)
	}

	observation.DataVolumeFound = false
	observation.DataVolume = volumeInspection{}
	observation.DataVolumeIdentity = gatewayV1VolumeIdentity{}
	observation.OwnedVolumes = []string{}
	got, ok = classifyGatewayV2RollbackRetirement(source, state, journal, observation)
	if !ok || !got.complete() {
		t.Fatalf("complete cleanup = %+v, ok=%t", got, ok)
	}
}

func TestValidOwnedNameSetAcceptsEmptyDockerInventoryWithoutAcceptingExtras(t *testing.T) {
	if !validOwnedNameSet(nil) || !validOwnedNameSet([]string{}) {
		t.Fatal("empty Docker inventory was rejected")
	}
	if validOwnedNameSet([]string{"unexpected"}) || validOwnedNameSet([]string{}, "expected") {
		t.Fatal("incorrect Docker inventory was accepted")
	}
	if !validOwnedNameSet([]string{"alpha", "beta"}, "beta", "alpha") ||
		validOwnedNameSet([]string{"beta", "alpha"}, "beta", "alpha") ||
		validOwnedNameSet([]string{"alpha", "alpha"}, "beta", "alpha") {
		t.Fatal("owned inventory did not require the exact sorted names")
	}
}

func TestClassifyGatewayV2RollbackRetirementRejectsReplacementResources(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gatewayV2DockerObservation, gatewayV2RouteState)
	}{
		{name: "network immutable id", mutate: func(value *gatewayV2DockerObservation, _ gatewayV2RouteState) {
			value.IngressNetworkID = "sha256:" + repeatGatewayTestHex("9")
		}},
		{name: "network labels", mutate: func(value *gatewayV2DockerObservation, _ gatewayV2RouteState) {
			value.IngressNetwork.Labels[gatewayV2OperationLabelKey] = "77777777-7777-4777-8777-777777777777"
		}},
		{name: "config mountpoint", mutate: func(value *gatewayV2DockerObservation, _ gatewayV2RouteState) {
			value.ConfigVolumeIdentity.Mountpoint += "-replacement"
		}},
		{name: "data creation time", mutate: func(value *gatewayV2DockerObservation, _ gatewayV2RouteState) {
			value.DataVolumeIdentity.CreatedAt = "2026-09-30T00:00:00Z"
		}},
		{name: "extra owned volume", mutate: func(value *gatewayV2DockerObservation, _ gatewayV2RouteState) {
			value.OwnedVolumes = append(value.OwnedVolumes, "rig-generated-caddy-replacement")
			sort.Strings(value.OwnedVolumes)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, state, journal, observation := gatewayV2RollbackRetirementIdentityFixture(t)
			test.mutate(&observation, state)
			if got, ok := classifyGatewayV2RollbackRetirement(source, state, journal, observation); ok {
				t.Fatalf("replacement was accepted: %+v", got)
			}
		})
	}
}

func TestGatewayV2CurrentV1RetirementBindingsPermitLegitimateRouteChange(t *testing.T) {
	source, state, journal, _ := gatewayV2RollbackRetirementIdentityFixture(t)
	changed := source
	changed.Active = map[string]routeRecord{}

	proofState, proofJournal, err := gatewayV2CurrentV1RetirementBindings(changed, state, journal)
	if err != nil {
		t.Fatal(err)
	}
	changedDigest, err := canonicalDigest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if proofState.SourceV1StateDigest != changedDigest || proofJournal.Source.StateDigest != changedDigest ||
		len(proofState.Apps) != 0 || proofJournal.Source.IdentityDigest != "" ||
		proofState.OperationID != state.OperationID || proofState.Identity != state.Identity ||
		proofState.Network != state.Network || proofJournal.Resources != journal.Resources ||
		!journalMatchesInitialV2State(proofJournal, proofState) {
		t.Fatalf("current-v1 proof binding drifted: state=%+v journal=%+v", proofState, proofJournal)
	}
}

func TestFinalizeGatewayV2RollbackResumesAfterAmbiguousPartialCleanup(t *testing.T) {
	manager, store, request, sourceDriver, stageDriver, _, _ := gatewayV2CoordinatorTestFixture(t)
	state, journal := prepareRolledBackForRetirementTest(t, manager, store, request, sourceDriver, stageDriver)
	sourceDriver.retirementObservation = gatewayV2RollbackRetirementObservation{
		IngressNetworkPresent: true, ConfigVolumePresent: true, DataVolumePresent: true,
	}
	sourceDriver.retirementNetworkErrorAfterEffect = errors.New("injected ambiguous Docker response")

	if err := finalizeGatewayV2RollbackForTest(t, manager, store, state, journal); err == nil {
		t.Fatal("ambiguous network removal unexpectedly finalized")
	}
	if sourceDriver.retirementRemoveNetworkCalls != 1 || sourceDriver.retirementRemoveConfigCalls != 0 ||
		sourceDriver.retirementRemoveDataCalls != 0 || sourceDriver.retirementObservation.IngressNetworkPresent {
		t.Fatalf("ambiguous cleanup state: %+v", sourceDriver)
	}
	if _, err := store.loadRollbackRetirementReceipt(state, journal); err == nil {
		t.Fatal("receipt was written after ambiguous Docker response")
	}

	sourceDriver.retirementNetworkErrorAfterEffect = nil
	if err := finalizeGatewayV2RollbackForTest(t, manager, store, state, journal); err != nil {
		t.Fatal(err)
	}
	if sourceDriver.retirementRemoveNetworkCalls != 1 || sourceDriver.retirementRemoveConfigCalls != 1 ||
		sourceDriver.retirementRemoveDataCalls != 1 || !sourceDriver.retirementObservation.complete() {
		t.Fatalf("resumed cleanup state: %+v", sourceDriver)
	}
	if _, err := store.loadRollbackRetirementReceipt(state, journal); err != nil {
		t.Fatalf("retirement receipt: %v", err)
	}
}

func TestFinalizeGatewayV2RollbackDoesNotDeleteReplacementResource(t *testing.T) {
	manager, store, request, sourceDriver, stageDriver, _, _ := gatewayV2CoordinatorTestFixture(t)
	state, journal := prepareRolledBackForRetirementTest(t, manager, store, request, sourceDriver, stageDriver)
	sourceDriver.retirementObservation = gatewayV2RollbackRetirementObservation{IngressNetworkPresent: true}
	sourceDriver.retirementRemoveNetworkError = errors.New("replacement immutable id")

	if err := finalizeGatewayV2RollbackForTest(t, manager, store, state, journal); err == nil {
		t.Fatal("replacement resource unexpectedly finalized")
	}
	if sourceDriver.retirementRemoveNetworkCalls != 1 || !sourceDriver.retirementObservation.IngressNetworkPresent ||
		sourceDriver.retirementRemoveConfigCalls != 0 || sourceDriver.retirementRemoveDataCalls != 0 {
		t.Fatalf("replacement resource was mutated: %+v", sourceDriver)
	}
	if _, err := store.loadRollbackRetirementReceipt(state, journal); err == nil {
		t.Fatal("receipt was written with replacement resource present")
	}
}

func TestFinalizeGatewayV2RollbackReceiptReplayRequiresFreshCurrentV1Proof(t *testing.T) {
	manager, store, request, sourceDriver, stageDriver, _, _ := gatewayV2CoordinatorTestFixture(t)
	state, journal := prepareRolledBackForRetirementTest(t, manager, store, request, sourceDriver, stageDriver)
	if err := finalizeGatewayV2RollbackForTest(t, manager, store, state, journal); err != nil {
		t.Fatal(err)
	}
	initialObservations := sourceDriver.retirementObserveCalls
	sourceDriver.retirementObserveError = errors.New("current v1 stopped or drifted")
	if err := finalizeGatewayV2RollbackForTest(t, manager, store, state, journal); err == nil {
		t.Fatal("receipt replay accepted stopped or drifted current v1")
	}
	if sourceDriver.retirementObserveCalls != initialObservations+1 || sourceDriver.retirementRemoveNetworkCalls != 0 ||
		sourceDriver.retirementRemoveConfigCalls != 0 || sourceDriver.retirementRemoveDataCalls != 0 {
		t.Fatalf("receipt replay did not remain read-only: %+v", sourceDriver)
	}
	sourceDriver.retirementObserveError = nil
	if err := finalizeGatewayV2RollbackForTest(t, manager, store, state, journal); err != nil {
		t.Fatalf("fresh current v1 proof did not replay receipt: %v", err)
	}
}

func TestFinalizeGatewayV2RollbackDoesNotInstallReceiptAfterWriteFailure(t *testing.T) {
	manager, store, request, sourceDriver, stageDriver, _, _ := gatewayV2CoordinatorTestFixture(t)
	state, journal := prepareRolledBackForRetirementTest(t, manager, store, request, sourceDriver, stageDriver)
	originalWriteNew := upgradeProtectedWriteNew
	upgradeProtectedWriteNew = func(path, purpose string, body []byte) error {
		if purpose == store.receiptPurpose {
			return errors.New("injected receipt write failure")
		}
		return originalWriteNew(path, purpose, body)
	}
	t.Cleanup(func() { upgradeProtectedWriteNew = originalWriteNew })

	if err := finalizeGatewayV2RollbackForTest(t, manager, store, state, journal); err == nil {
		t.Fatal("receipt write failure unexpectedly finalized")
	}
	if _, err := os.Lstat(store.receiptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt exists after failed write: %v", err)
	}
}

func TestFinalizeGatewayV2RollbackIsIdempotentAfterCurrentV1RouteChange(t *testing.T) {
	manager, store, request, sourceDriver, stageDriver, _, _ := gatewayV2CoordinatorTestFixture(t)
	_, _ = prepareRolledBackForRetirementTest(t, manager, store, request, sourceDriver, stageDriver)
	if err := manager.FinalizeGatewayV2Rollback(context.Background(), request.OperationID); err != nil {
		t.Fatal(err)
	}
	firstObservations := sourceDriver.retirementObserveCalls
	current, err := manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	current.Active = map[string]routeRecord{}
	if err := manager.store.save(current); err != nil {
		t.Fatal(err)
	}
	if err := manager.FinalizeGatewayV2Rollback(context.Background(), request.OperationID); err != nil {
		t.Fatalf("idempotent retirement after current v1 route change: %v", err)
	}
	if sourceDriver.retirementObserveCalls != firstObservations+1 || sourceDriver.retirementRemoveNetworkCalls != 0 ||
		sourceDriver.retirementRemoveConfigCalls != 0 || sourceDriver.retirementRemoveDataCalls != 0 {
		t.Fatalf("idempotent replay was not a fresh read-only proof: %+v", sourceDriver)
	}
}

func TestFinalizeGatewayV2RollbackDoesNotSucceedWhenGatewayLockReleaseFails(t *testing.T) {
	manager, store, request, sourceDriver, stageDriver, _, osLockHeld := gatewayV2CoordinatorTestFixture(t)
	state, journal := prepareRolledBackForRetirementTest(t, manager, store, request, sourceDriver, stageDriver)
	successfulAcquire := managerAcquireGatewayOSLock
	managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
		if *osLockHeld {
			t.Fatal("gateway OS lock acquired twice")
		}
		*osLockHeld = true
		return func() error {
			*osLockHeld = false
			return errors.New("injected gateway lock release failure")
		}, nil
	}

	if err := manager.FinalizeGatewayV2Rollback(context.Background(), request.OperationID); !IsCode(err, DiagnosticRouteUnresolved) || *osLockHeld {
		t.Fatalf("lock release failure = %v, lockHeld=%t", err, *osLockHeld)
	}
	if _, err := store.loadRollbackRetirementReceipt(state, journal); err != nil {
		t.Fatalf("durable receipt missing after ambiguous lock release: %v", err)
	}
	managerAcquireGatewayOSLock = successfulAcquire
	if err := manager.FinalizeGatewayV2Rollback(context.Background(), request.OperationID); err != nil {
		t.Fatalf("fresh idempotent retry after lock release failure: %v", err)
	}
}

func gatewayV2RollbackRetirementIdentityFixture(t *testing.T) (routeState, gatewayV2RouteState, gatewayMigrationJournal, gatewayV2DockerObservation) {
	t.Helper()
	source, state, journal := gatewayV2IdentityTestState(t)
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Phase = gatewayPhaseRolledBack
	if !validGatewayMigrationJournal(journal) {
		t.Fatal("invalid rolled-back journal fixture")
	}
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1Only)
	observation.ConfigVolume = gatewayV2IdentityTestVolume(state, journal, state.Identity.ConfigVolume, gatewayV2ConfigVolumeRole)
	observation.ConfigVolumeIdentity = gatewayV1VolumeIdentity{
		Mountpoint: journal.Resources.ConfigVolume.Mountpoint, CreatedAt: journal.Resources.ConfigVolume.CreatedAt,
	}
	observation.ConfigVolumeFound = true
	observation.DataVolume = gatewayV2IdentityTestVolume(state, journal, state.Identity.DataVolume, gatewayV2DataVolumeRole)
	observation.DataVolumeIdentity = gatewayV1VolumeIdentity{
		Mountpoint: journal.Resources.DataVolume.Mountpoint, CreatedAt: journal.Resources.DataVolume.CreatedAt,
	}
	observation.DataVolumeFound = true
	observation.IngressNetwork = gatewayV2IdentityTestNetwork(state, journal, "", "")
	observation.IngressNetworkID = "sha256:" + journal.Resources.IngressNetworkID
	observation.IngressFound = true
	observation.OwnedVolumes = []string{state.Identity.ConfigVolume, state.Identity.DataVolume}
	sort.Strings(observation.OwnedVolumes)
	observation.OwnedNetworks = []string{state.Identity.IngressNetwork}
	return source, state, journal, observation
}

func prepareRolledBackForRetirementTest(t *testing.T, manager *Manager, store *gatewayUpgradeStateStore,
	request GatewayV2UpgradeRequest, sourceDriver *fakeGatewayV2CoordinatorDriver, stageDriver *fakeGatewayV2UpgradeDriver,
) (gatewayV2RouteState, gatewayMigrationJournal) {
	t.Helper()
	preparation, err := gatewayV2UpgradePreparation(request)
	if err != nil {
		t.Fatal(err)
	}
	preparation.LocalHostPort = manager.options.HostPort
	preparation.Network = sourceDriver.plan
	preparation.SourceIdentityDigest = sourceDriver.digest
	source, err := manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	state, journal, err := prepareGatewayV2State(source, preparation)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGatewayV2ProtectedState(store, state, journal); err != nil {
		t.Fatal(err)
	}
	stageDriver.failAt = "selected_preflight"
	if err := manager.StageGatewayV2(context.Background(), request.OperationID); err == nil {
		t.Fatal("stage failure unexpectedly succeeded")
	}
	state, journal, err = store.loadBoundUpgrade(request.OperationID)
	if err != nil || journal.Phase != gatewayPhaseRolledBack {
		t.Fatalf("rolled-back pair: journal=%+v err=%v", journal, err)
	}
	return state, journal
}

func finalizeGatewayV2RollbackForTest(t *testing.T, manager *Manager, store *gatewayUpgradeStateStore,
	state gatewayV2RouteState, journal gatewayMigrationJournal,
) error {
	t.Helper()
	release, err := manager.lockGateway(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result := manager.finalizeGatewayV2RollbackLocked(context.Background(), store, state, journal)
	if releaseErr := release(); releaseErr != nil && result == nil {
		result = releaseErr
	}
	return result
}

func repeatGatewayTestHex(value string) string {
	if len(value) != 1 {
		panic("test hex must be one character")
	}
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result
}
