package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

// Test teardown is intentionally outside production recovery. It deletes only
// the terminal-bound disposable successor after reattesting protected, SQL,
// runtime-head and Docker ownership under the normal effect and gateway locks.
func cleanupLiveGatewayRebindRuntime(t *testing.T, fixture liveGatewayRebindRuntimeFixture) {
	t.Helper()
	if fixture.manager == nil || fixture.repository == nil || fixture.fixture == nil {
		t.Error("typed runtime cleanup lacks fixture authority; retaining resources")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, fixture.manager.options.WorkingDirectory)
	if err != nil {
		t.Error("typed runtime cleanup cannot acquire effects lease; retaining resources")
		return
	}
	defer func() {
		if releaseErr := releaseEffects(); releaseErr != nil {
			t.Error("typed runtime cleanup effects lease release failed")
		}
	}()
	releaseGateway, err := fixture.manager.lockGatewayRaw(ctx)
	if err != nil {
		t.Error("typed runtime cleanup cannot acquire gateway lock; retaining resources")
		return
	}
	defer func() {
		if releaseErr := releaseGateway(); releaseErr != nil {
			t.Error("typed runtime cleanup gateway lock release failed")
		}
	}()
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !liveGatewayRebindRuntimeHistoryMatchesFixture(history, fixture.inspection) {
		t.Error("typed runtime cleanup cannot identify an exact terminal; retaining resources")
		return
	}
	receipt := history.TerminalsV2[0].Receipt
	if receipt.Disposition == appaccess.GatewayRebindDispositionAbort {
		if !liveGatewayRebindRuntimeRollbackAbsent(ctx, fixture.manager, fixture.repository, history, receipt) {
			t.Error("typed runtime rollback cleanup found successor residue; retaining resources")
		}
		return
	}
	plan, err := newLiveGatewayRebindRuntimeCleanupPlan(ctx, fixture.manager, fixture.repository)
	if err != nil {
		t.Error("typed runtime cleanup lacks exact committed terminal authority; retaining resources")
		return
	}
	boundary, err := newLiveGatewayRebindRuntimeCleanupBoundary(ctx, fixture, plan)
	if err != nil {
		t.Errorf("typed runtime cleanup initial attestation refused: %v; retaining resources", err)
		return
	}
	if err := cleanupGatewayRebindRuntimeTerminal(ctx, &boundary, plan); err != nil {
		t.Errorf("typed runtime cleanup refused: %v; retaining resources", err)
	}
}

type liveGatewayRebindRuntimeCleanupBoundary struct {
	manager    *Manager
	repository *appaccess.Repository
	plan       gatewayRebindRuntimeCleanupPlan
	inspection GatewayRebindProposalInspection
	files      gatewayHistorySnapshot
	target     gatewayCurrentPhysicalTarget
	runtime    managerGatewayCurrentPhysicalRuntime
	endpoints  string
}

func liveGatewayRebindRuntimeHistoryMatchesFixture(history gatewayRebindProtectedIntentHistory, expected GatewayRebindProposalInspection) bool {
	if len(history.Intents) != 0 || len(history.Terminals) != 0 || len(history.IntentsV2) != 1 || len(history.Checkpoints) != 1 || len(history.TerminalsV2) != 1 {
		return false
	}
	intent, checkpoint, receipt := history.IntentsV2[0].Intent, history.Checkpoints[0].Checkpoint, history.TerminalsV2[0].Receipt
	return intent.OperationID == expected.Spec.OperationID && intent.Generation == expected.ProtectedGeneration &&
		reflect.DeepEqual(intent.Claim.Spec, expected.Spec) && reflect.DeepEqual(intent.Roster, expected.Roster) &&
		reflect.DeepEqual(intent.RuntimeHeads, expected.RuntimeHeads) && checkpoint.Digest == expected.PredecessorCheckpointDigest &&
		checkpoint.SourceStateVersion == expected.SourceStateVersion && checkpoint.SourceStateRevision == expected.SourceStateRevision &&
		checkpoint.SourceStateDigest == expected.SourceStateDigest &&
		gatewayRebindTerminalV2MatchesIntentHistory(receipt, intent, checkpoint, history.Progress)
}

func newLiveGatewayRebindRuntimeCleanupBoundary(ctx context.Context, fixture liveGatewayRebindRuntimeFixture, plan gatewayRebindRuntimeCleanupPlan) (liveGatewayRebindRuntimeCleanupBoundary, error) {
	b := liveGatewayRebindRuntimeCleanupBoundary{manager: fixture.manager, repository: fixture.repository, plan: plan, inspection: fixture.inspection}
	sql, err := b.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		return b, errors.New("read selected SQL authority")
	}
	selected, err := b.manager.selectGatewayCurrentLocked(ctx, sql)
	if err != nil || selected.State == nil || selected.State.Pending != nil || selected.State.LANRecovery != nil {
		return b, errors.New("select stable current state")
	}
	b.target, err = gatewayCurrentPhysicalTargetFor(selected, *selected.State, nil, nil)
	if err != nil {
		return b, errors.New("bind exact current physical target")
	}
	proof, err := b.manager.attestGatewayCurrentPhysicalLocked(ctx, selected)
	if err != nil || proof.Outcome != gatewayCurrentPhysicalStableServing || !gatewayCurrentPhysicalAttestationAtTarget(proof, b.target) {
		return b, errors.New("attest committed serving target")
	}
	driver := newManagedGatewayCurrentPhysicalDriver(b.manager).(managedGatewayCurrentPhysicalDriver)
	b.runtime = driver.runtime.(managerGatewayCurrentPhysicalRuntime)
	b.files, err = readGatewayHistorySnapshotMode(b.manager.store, true)
	if err != nil {
		return b, errors.New("pin complete protected history")
	}
	b.endpoints, err = liveGatewayRebindRuntimeApplicationProof(ctx, b.manager, b.target)
	if err != nil {
		return b, errors.New("pin application endpoints")
	}
	if _, err = b.Snapshot(ctx); err != nil {
		return b, err
	}
	return b, nil
}

func (b *liveGatewayRebindRuntimeCleanupBoundary) Snapshot(ctx context.Context) (gatewayRebindRuntimeCleanupSnapshot, error) {
	before, intent, receipt, err := liveGatewayRebindRuntimeCleanupAuthority(ctx, b.manager, b.repository)
	if err != nil || before != b.plan.Authority {
		return gatewayRebindRuntimeCleanupSnapshot{}, errors.New("typed runtime cleanup authority drift")
	}
	history, historyErr := b.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	files, filesErr := readGatewayHistorySnapshotMode(b.manager.store, true)
	if historyErr != nil || filesErr != nil || !liveGatewayRebindRuntimeHistoryMatchesFixture(history, b.inspection) || !sameGatewayHistorySnapshot(b.files, files) {
		return gatewayRebindRuntimeCleanupSnapshot{}, errors.New("typed runtime cleanup protected fixture changed")
	}
	value, err := liveGatewayRebindRuntimeCleanupDockerSnapshot(ctx, b, intent, receipt)
	if err != nil {
		return gatewayRebindRuntimeCleanupSnapshot{}, err
	}
	after, _, _, err := liveGatewayRebindRuntimeCleanupAuthority(ctx, b.manager, b.repository)
	afterFiles, afterFilesErr := readGatewayHistorySnapshotMode(b.manager.store, true)
	if err != nil || after != before || afterFilesErr != nil || !sameGatewayHistorySnapshot(files, afterFiles) || ctx.Err() != nil {
		return gatewayRebindRuntimeCleanupSnapshot{}, errors.New("typed runtime cleanup authority changed during Docker observation")
	}
	value.Authority = after
	return value, nil
}

func (b *liveGatewayRebindRuntimeCleanupBoundary) Effect(ctx context.Context, args ...string) error {
	if b == nil || b.manager == nil {
		return errors.New("typed runtime cleanup has no command boundary")
	}
	snapshot, err := b.Snapshot(ctx)
	if err != nil {
		return err
	}
	if !validGatewayRebindRuntimeCleanupSnapshot(snapshot, b.plan) {
		return errors.New("cleanup ownership changed at effect boundary")
	}
	var next []string
	switch {
	case snapshot.Final.Present && snapshot.Final.Running:
		next = []string{"container", "stop", "--time", "10", b.plan.FinalID}
	case snapshot.Final.Present:
		next = []string{"container", "rm", b.plan.FinalID}
	case snapshot.Config.Present:
		next = []string{"volume", "rm", b.plan.Config.Name}
	case snapshot.Data.Present:
		next = []string{"volume", "rm", b.plan.Data.Name}
	case snapshot.Ingress.Present:
		next = []string{"network", "rm", b.plan.Ingress.ID}
	}
	if len(next) == 0 || !reflect.DeepEqual(args, next) {
		return errors.New("cleanup command is not the next exact owned effect")
	}
	if reflect.DeepEqual(args, []string{"container", "stop", "--time", "10", b.plan.FinalID}) {
		owned, err := b.manager.gatewayCurrentOwnedStopTargetForStateLocked(b.target.State)
		if err != nil {
			return errors.New("bind guarded current withdrawal")
		}
		return b.manager.stopGatewayCurrentOwnedTargetLocked(ctx, owned)
	}
	return b.manager.runDiscard(ctx, b.manager.options.CommandTimeout, args...)
}

func newLiveGatewayRebindRuntimeCleanupPlan(ctx context.Context, manager *Manager,
	repository *appaccess.Repository,
) (gatewayRebindRuntimeCleanupPlan, error) {
	authority, intent, receipt, err := liveGatewayRebindRuntimeCleanupAuthority(ctx, manager, repository)
	if err != nil || receipt.Resources == nil || receipt.Resources.FinalContainer == nil {
		return gatewayRebindRuntimeCleanupPlan{}, errors.New("invalid typed runtime cleanup terminal")
	}
	resources := *receipt.Resources
	return gatewayRebindRuntimeCleanupPlan{Authority: authority, FinalID: resources.FinalContainer.ID,
		ImageID: resources.ImageID, FinalLabels: gatewayRebindTypedStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole),
		Config: gatewayRebindRuntimeCleanupVolume{Name: resources.ConfigVolume.Name, CreatedAt: resources.ConfigVolume.CreatedAt,
			Mountpoint: resources.ConfigVolume.Mountpoint, Labels: gatewayRebindTypedStageResourceLabels(intent,
				gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole)},
		Data: gatewayRebindRuntimeCleanupVolume{Name: resources.DataVolume.Name, CreatedAt: resources.DataVolume.CreatedAt,
			Mountpoint: resources.DataVolume.Mountpoint, Labels: gatewayRebindTypedStageResourceLabels(intent,
				gatewayV2ManagedContainerLabel, gatewayV2DataVolumeRole)},
		Ingress: gatewayRebindRuntimeCleanupNetwork{ID: resources.IngressNetwork.ID,
			Labels: gatewayRebindTypedStageResourceLabels(intent, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole),
			IPAM:   []networkIPAM{{Subnet: intent.Network.Subnet, Gateway: intent.Network.GatewayIPv4}}},
	}, nil
}

func liveGatewayRebindRuntimeCleanupAuthority(ctx context.Context, manager *Manager,
	repository *appaccess.Repository,
) (gatewayRebindRuntimeCleanupAuthority, gatewayRebindProtectedIntentV2, gatewayRebindTerminalReceiptV2, error) {
	if manager == nil || repository == nil {
		return gatewayRebindRuntimeCleanupAuthority{}, gatewayRebindProtectedIntentV2{}, gatewayRebindTerminalReceiptV2{}, errors.New("nil typed runtime cleanup authority")
	}
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 0 || len(history.Terminals) != 0 || len(history.IntentsV2) != 1 ||
		len(history.Checkpoints) != 1 || len(history.TerminalsV2) != 1 || len(history.Progress) != 17 {
		return gatewayRebindRuntimeCleanupAuthority{}, gatewayRebindProtectedIntentV2{}, gatewayRebindTerminalReceiptV2{}, errors.New("typed runtime cleanup history is not singular")
	}
	intent, checkpoint, receipt := history.IntentsV2[0].Intent, history.Checkpoints[0].Checkpoint, history.TerminalsV2[0].Receipt
	if receipt.Disposition != appaccess.GatewayRebindDispositionCommit || !gatewayRebindTerminalV2MatchesIntentHistory(receipt, intent, checkpoint, history.Progress) ||
		receipt.Resources == nil || receipt.Resources.FinalContainer == nil {
		return gatewayRebindRuntimeCleanupAuthority{}, gatewayRebindProtectedIntentV2{}, gatewayRebindTerminalReceiptV2{}, errors.New("typed runtime cleanup terminal is not committed")
	}
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	lineage, lineageErr := gatewayRebindCurrentLineageV2(receipt)
	current, currentErr := manager.inspectGatewayRebindCurrentLocked(ctx, repository)
	heads, headsErr := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || lineageErr != nil || currentErr != nil || headsErr != nil || snapshot.Active != nil || snapshot.CurrentSource == nil ||
		*snapshot.CurrentSource != gatewayCurrentAuthority(lineage) || current.SelectedCurrentAuthority != *snapshot.CurrentSource ||
		!current.FenceReleased || current.ActiveOperationID != "" || current.ActivePhase != "" || len(current.Retained) != 1 ||
		current.Retained[0].TerminalReceiptDigest != receipt.Digest {
		return gatewayRebindRuntimeCleanupAuthority{}, gatewayRebindProtectedIntentV2{}, gatewayRebindTerminalReceiptV2{}, errors.New("typed runtime cleanup SQL or current state is not bound")
	}
	protected, protectedErr := canonicalDigest(struct {
		Intent     gatewayRebindProtectedIntentV2
		Checkpoint gatewayRebindPredecessorCheckpoint
		Progress   []gatewayRebindProgressRecord
		Terminal   gatewayRebindTerminalReceiptV2
		Current    GatewayRebindCurrentInspection
	}{intent, checkpoint, selectedGatewayRebindProgress(history.Progress), receipt, current})
	sqlDigest, sqlErr := canonicalDigest(snapshot)
	headsDigest, headsDigestErr := canonicalDigest(heads)
	if protectedErr != nil || sqlErr != nil || headsDigestErr != nil {
		return gatewayRebindRuntimeCleanupAuthority{}, gatewayRebindProtectedIntentV2{}, gatewayRebindTerminalReceiptV2{}, errors.New("typed runtime cleanup cannot digest authority")
	}
	return gatewayRebindRuntimeCleanupAuthority{OperationID: receipt.OperationID, TerminalDigest: receipt.Digest,
		SQLDigest: sqlDigest, ProtectedDigest: protected, RuntimeHeadsDigest: headsDigest}, intent, receipt, nil
}

func selectedGatewayRebindProgress(values []gatewayRebindProgressSelection) []gatewayRebindProgressRecord {
	result := make([]gatewayRebindProgressRecord, len(values))
	for index := range values {
		result[index] = values[index].Record
	}
	return result
}

func liveGatewayRebindRuntimeCleanupDockerSnapshot(ctx context.Context, boundary *liveGatewayRebindRuntimeCleanupBoundary,
	intent gatewayRebindProtectedIntentV2, receipt gatewayRebindTerminalReceiptV2,
) (gatewayRebindRuntimeCleanupSnapshot, error) {
	manager, finalID := boundary.manager, boundary.plan.FinalID
	if manager == nil || receipt.Resources == nil || receipt.Resources.FinalContainer == nil || finalID != receipt.Resources.FinalContainer.ID {
		return gatewayRebindRuntimeCleanupSnapshot{}, errors.New("typed runtime cleanup Docker plan is invalid")
	}
	_, _, stagePresent, err := manager.inspectNamedGatewayContainer(ctx, intent.Identity.StageContainer)
	if err != nil {
		return gatewayRebindRuntimeCleanupSnapshot{}, err
	}
	final, _, finalPresent, err := manager.inspectNamedGatewayContainer(ctx, intent.Identity.FinalContainer)
	if err != nil {
		return gatewayRebindRuntimeCleanupSnapshot{}, err
	}
	config, configIdentity, configPresent, err := manager.inspectNamedVolumeWithIdentity(ctx, intent.Identity.ConfigVolume)
	if err != nil {
		return gatewayRebindRuntimeCleanupSnapshot{}, err
	}
	data, dataIdentity, dataPresent, err := manager.inspectNamedVolumeWithIdentity(ctx, intent.Identity.DataVolume)
	if err != nil {
		return gatewayRebindRuntimeCleanupSnapshot{}, err
	}
	network, networkID, networkPresent, err := manager.inspectNamedGatewayNetwork(ctx, intent.Identity.IngressNetwork)
	if err != nil {
		return gatewayRebindRuntimeCleanupSnapshot{}, err
	}
	if stagePresent {
		return gatewayRebindRuntimeCleanupSnapshot{}, errors.New("unexpected stage container")
	}
	if _, _, present, inspectErr := manager.inspectNamedGatewayContainer(ctx, receipt.Resources.StageContainer.ID); inspectErr != nil || present {
		return gatewayRebindRuntimeCleanupSnapshot{}, errors.New("retained stage container ID is not absent")
	}
	if finalPresent {
		proof, proofErr := boundary.runtime.observe(ctx, boundary.target)
		want := gatewayCurrentPhysicalStableServing
		if !final.Running {
			want = gatewayCurrentPhysicalRecoveryStopped
		}
		if proofErr != nil || !validGatewayCurrentPhysicalAttestation(proof) || proof.Outcome != want ||
			!reflect.DeepEqual(proof.State, boundary.target.State) || proof.Lineage != boundary.target.Lineage ||
			!reflect.DeepEqual(proof.Terminal, boundary.target.Terminal) || !reflect.DeepEqual(proof.Identity, boundary.target.Identity) ||
			!reflect.DeepEqual(proof.Resources, boundary.target.Resources) || proof.Pending != nil || proof.LANRecovery != nil ||
			normalizeID(final.ID) != finalID {
			return gatewayRebindRuntimeCleanupSnapshot{}, errors.New("current final physical/config attestation changed")
		}
	} else if _, _, present, inspectErr := manager.inspectNamedGatewayContainer(ctx, finalID); inspectErr != nil || present {
		return gatewayRebindRuntimeCleanupSnapshot{}, errors.New("retained final container ID is not absent")
	}
	if configPresent && !boundary.runtime.validVolume(boundary.target, config, configIdentity, receipt.Resources.ConfigVolume, gatewayV2ConfigVolumeRole) {
		return gatewayRebindRuntimeCleanupSnapshot{}, errors.New("config volume binding changed")
	}
	if dataPresent && !boundary.runtime.validVolume(boundary.target, data, dataIdentity, receipt.Resources.DataVolume, gatewayV2DataVolumeRole) {
		return gatewayRebindRuntimeCleanupSnapshot{}, errors.New("data volume binding changed")
	}
	if networkPresent {
		if !boundary.runtime.validIngress(boundary.target, network, networkID) {
			return gatewayRebindRuntimeCleanupSnapshot{}, errors.New("ingress network binding changed")
		}
	} else if _, _, present, inspectErr := manager.inspectNamedGatewayNetwork(ctx, receipt.Resources.IngressNetwork.ID); inspectErr != nil || present {
		return gatewayRebindRuntimeCleanupSnapshot{}, errors.New("retained ingress network ID is not absent")
	}
	if err := liveGatewayRebindRuntimeExactCensus(ctx, manager, intent, finalPresent, configPresent, dataPresent, networkPresent); err != nil {
		return gatewayRebindRuntimeCleanupSnapshot{}, err
	}
	appFinal := final
	if !finalPresent {
		appFinal = caddyInspection{ID: finalID}
	}
	applications, appErr := boundary.runtime.applicationNetworkBindings(ctx, boundary.target.State, appFinal, true)
	endpoints, endpointErr := liveGatewayRebindRuntimeApplicationProof(ctx, manager, boundary.target)
	if appErr != nil || endpointErr != nil || !reflect.DeepEqual(applications, receipt.Resources.ApplicationNetworks) || endpoints != boundary.endpoints {
		return gatewayRebindRuntimeCleanupSnapshot{}, errors.New("application network or endpoint ownership changed")
	}
	value := gatewayRebindRuntimeCleanupSnapshot{StagePresent: stagePresent}
	if finalPresent {
		value.Final = gatewayRebindRuntimeCleanupContainer{Present: true, ID: normalizeID(final.ID), ImageID: normalizeID(final.Image),
			Running: final.Running, Labels: final.Labels}
	}
	if configPresent {
		value.Config = gatewayRebindRuntimeCleanupVolume{Present: true, Name: config.Name, CreatedAt: configIdentity.CreatedAt,
			Mountpoint: configIdentity.Mountpoint, Labels: config.Labels}
	}
	if dataPresent {
		value.Data = gatewayRebindRuntimeCleanupVolume{Present: true, Name: data.Name, CreatedAt: dataIdentity.CreatedAt,
			Mountpoint: dataIdentity.Mountpoint, Labels: data.Labels}
	}
	if networkPresent {
		value.Ingress = gatewayRebindRuntimeCleanupNetwork{Present: true, ID: networkID, Labels: network.Labels, IPAM: network.IPAM.Config}
	}
	if !finalPresent || !final.Running {
		value.Final.ListenerAbsent = boundary.runtime.listenerAbsent(ctx, boundary.target.State)
	}
	ownedConsumer := ""
	if finalPresent {
		ownedConsumer = finalID
	}
	value.Config.Consumers, err = liveGatewayRebindRuntimeVolumeConsumers(ctx, manager, intent.Identity.ConfigVolume, ownedConsumer)
	if err != nil {
		return gatewayRebindRuntimeCleanupSnapshot{}, err
	}
	value.Data.Consumers, err = liveGatewayRebindRuntimeVolumeConsumers(ctx, manager, intent.Identity.DataVolume, ownedConsumer)
	if err != nil {
		return gatewayRebindRuntimeCleanupSnapshot{}, err
	}
	for id := range network.Containers {
		if normalizeID(id) != ownedConsumer || !final.Running {
			value.Ingress.Members++
		}
	}
	return value, nil
}

// Pin every application endpoint and other network member across teardown.
// Only the already attested final gateway is omitted because its removal is
// this helper's purpose. The coordinator always observes unmodified Docker.
func liveGatewayRebindRuntimeApplicationProof(ctx context.Context, manager *Manager, target gatewayCurrentPhysicalTarget) (string, error) {
	routes := gatewayCurrentPhysicalRoutes(target.State)
	owners, ok := gatewayRouteNetworkOwners(routes)
	if !ok || target.Resources.FinalContainer == nil {
		return "", errors.New("invalid cleanup application proof target")
	}
	networks := make(map[string]caddyNetworkInspection, len(owners))
	for _, binding := range target.Resources.ApplicationNetworks {
		network, id, found, err := manager.inspectNamedGatewayNetwork(ctx, binding.Name)
		if err != nil || !found || id != binding.ID || owners[binding.Name] == "" || !validApplicationNetwork(network.identity(), owners[binding.Name]) {
			return "", errors.New("cleanup application network identity changed")
		}
		for member := range network.Containers {
			if normalizeID(member) == target.Resources.FinalContainer.ID {
				delete(network.Containers, member)
			}
		}
		networks[binding.Name] = network
	}
	return manager.inspectGatewayEndpointIdentitySnapshot(ctx, routes, networks)
}

func liveGatewayRebindRuntimeExactCensus(ctx context.Context, manager *Manager, intent gatewayRebindProtectedIntentV2, final, config, data, network bool) error {
	containers, err := manager.inspectGatewayRebindTypedOwnedNames(ctx, intent, "container", "ls", "--all")
	if err != nil {
		return errors.New("read owned container census")
	}
	volumes, err := manager.inspectGatewayRebindTypedOwnedNames(ctx, intent, "volume", "ls")
	if err != nil {
		return errors.New("read owned volume census")
	}
	networks, err := manager.inspectGatewayRebindTypedOwnedNames(ctx, intent, "network", "ls")
	if err != nil {
		return errors.New("read owned network census")
	}
	var wantContainers, wantVolumes, wantNetworks []string
	if final {
		wantContainers = append(wantContainers, intent.Identity.FinalContainer)
	}
	if config {
		wantVolumes = append(wantVolumes, intent.Identity.ConfigVolume)
	}
	if data {
		wantVolumes = append(wantVolumes, intent.Identity.DataVolume)
	}
	if network {
		wantNetworks = append(wantNetworks, intent.Identity.IngressNetwork)
	}
	if !validOwnedNameSet(containers, wantContainers...) || !validOwnedNameSet(volumes, wantVolumes...) || !validOwnedNameSet(networks, wantNetworks...) {
		return errors.New("unexpected operation-owned resource census")
	}
	return nil
}

func liveGatewayRebindRuntimeVolumeConsumers(ctx context.Context, manager *Manager, name, finalID string) (int, error) {
	result, err := manager.run(ctx, manager.options.CommandTimeout, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+name)
	if err != nil {
		return 0, err
	}
	ids, parseErr := parseGatewayV2DockerNetworkIDs(result.Stdout)
	truncated := result.StdoutTruncated || result.StderrTruncated
	clearResult(&result)
	if parseErr != nil || truncated {
		return 0, errors.New("uncertain volume consumer census")
	}
	consumers := 0
	for _, id := range ids {
		if normalizeID(id) != finalID {
			consumers++
		}
	}
	return consumers, nil
}

func liveGatewayRebindRuntimeRollbackAbsent(ctx context.Context, manager *Manager, repository *appaccess.Repository,
	history gatewayRebindProtectedIntentHistory, receipt gatewayRebindTerminalReceiptV2,
) bool {
	if manager == nil || repository == nil || len(history.IntentsV2) != 1 || len(history.Checkpoints) != 1 ||
		!gatewayRebindTerminalV2MatchesIntentHistory(receipt, history.IntentsV2[0].Intent, history.Checkpoints[0].Checkpoint, history.Progress) {
		return false
	}
	intent := history.IntentsV2[0].Intent
	files, filesErr := readGatewayHistorySnapshotMode(manager.store, true)
	heads, headsErr := repository.GatewayRebindRuntimeHeads(ctx)
	current, currentErr := manager.inspectGatewayRebindCurrentLocked(ctx, repository)
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || filesErr != nil || headsErr != nil || currentErr != nil || !reflect.DeepEqual(heads, intent.RuntimeHeads) ||
		snapshot.Active != nil || snapshot.CurrentSource == nil ||
		*snapshot.CurrentSource != gatewayCurrentAuthority(receipt.Predecessor.Lineage) || repository.CheckGatewayRebindFence(ctx) != nil {
		return false
	}
	_, _, stagePresent, stageErr := manager.inspectNamedGatewayContainer(ctx, intent.Identity.StageContainer)
	_, _, finalPresent, finalErr := manager.inspectNamedGatewayContainer(ctx, intent.Identity.FinalContainer)
	_, _, configPresent, configErr := manager.inspectNamedVolumeWithIdentity(ctx, intent.Identity.ConfigVolume)
	_, _, dataPresent, dataErr := manager.inspectNamedVolumeWithIdentity(ctx, intent.Identity.DataVolume)
	_, _, networkPresent, networkErr := manager.inspectNamedGatewayNetwork(ctx, intent.Identity.IngressNetwork)
	if stageErr != nil || finalErr != nil || configErr != nil || dataErr != nil || networkErr != nil ||
		stagePresent || finalPresent || configPresent || dataPresent || networkPresent ||
		liveGatewayRebindRuntimeExactCensus(ctx, manager, intent, false, false, false, false) != nil {
		return false
	}
	containerIDs, networkIDs := map[string]bool{}, map[string]bool{}
	for _, progress := range history.Progress {
		if effect := progress.Record.TypedEffect; effect != nil {
			if effect.StageContainer != nil {
				containerIDs[effect.StageContainer.ID] = true
			}
			if effect.FinalContainer != nil {
				containerIDs[effect.FinalContainer.ID] = true
			}
			if effect.Network != nil {
				networkIDs[effect.Network.ID] = true
			}
		}
	}
	for id := range containerIDs {
		if !validContainerID(id) {
			return false
		}
		if _, _, found, err := manager.inspectNamedGatewayContainer(ctx, id); err != nil || found {
			return false
		}
	}
	for id := range networkIDs {
		if !validContainerID(id) {
			return false
		}
		if _, _, found, err := manager.inspectNamedGatewayNetwork(ctx, id); err != nil || found {
			return false
		}
	}
	for port := intent.SuccessorProfile.PortStart; ; port++ {
		if !probeGatewayRebindHandoverListenerAbsent(ctx, intent.SuccessorProfile.SelectedIPv4, port) {
			return false
		}
		if port == intent.SuccessorProfile.PortEnd {
			break
		}
	}
	afterFiles, afterFilesErr := readGatewayHistorySnapshotMode(manager.store, true)
	afterSQL, afterSQLErr := repository.GatewayRebindRecoverySnapshot(ctx)
	afterHeads, afterHeadsErr := repository.GatewayRebindRuntimeHeads(ctx)
	afterCurrent, afterCurrentErr := manager.inspectGatewayRebindCurrentLocked(ctx, repository)
	return afterFilesErr == nil && afterSQLErr == nil && afterHeadsErr == nil && afterCurrentErr == nil && ctx.Err() == nil &&
		sameGatewayHistorySnapshot(files, afterFiles) && reflect.DeepEqual(snapshot, afterSQL) && reflect.DeepEqual(heads, afterHeads) &&
		reflect.DeepEqual(current, afterCurrent)
}

var _ gatewayRebindRuntimeCleanupBoundary = (*liveGatewayRebindRuntimeCleanupBoundary)(nil)
