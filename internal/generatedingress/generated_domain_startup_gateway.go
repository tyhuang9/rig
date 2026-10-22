package generatedingress

import (
	"errors"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/deployments"
)

func classifyGeneratedDomainStartup(database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot, docker generatedDomainDockerStartupSnapshot,
) (generatedDomainStartupClassification, error) {
	refused := generatedDomainStartupClassification{}
	if protected.ambiguous || docker.ambiguous {
		refused.reason = generatedDomainStartupReasonAmbiguous
		return refused, nil
	}
	recoveries, validRecoveries := generatedDomainStartupRecoveries(database, protected, docker)
	if !validRecoveries {
		refused.reason = generatedDomainStartupReasonAmbiguous
		return refused, nil
	}
	if len(recoveries) != 0 {
		if docker.cold {
			refused.reason = generatedDomainStartupReasonColdAccounting
			return refused, nil
		}
		if !docker.recoveryAccounted {
			refused.reason = generatedDomainStartupReasonAmbiguous
			return refused, nil
		}
		return generatedDomainStartupClassification{disposition: generatedDomainStartupRecoveryRequired,
			reason: generatedDomainStartupReasonRecovery, recovery: recoveries}, nil
	}
	if generatedDomainStartupDatabaseCold(database) || docker.cold {
		refused.reason = generatedDomainStartupReasonColdAccounting
		return refused, nil
	}
	if generatedDomainStartupDatabaseEffectEmpty(database) && protected.effectEmpty && docker.effectEmpty {
		return generatedDomainStartupClassification{disposition: generatedDomainStartupEmpty,
			reason: generatedDomainStartupReasonEmpty}, nil
	}
	if generatedDomainStartupStableGateway(database, protected, docker) {
		return generatedDomainStartupClassification{disposition: generatedDomainStartupStable,
			reason: generatedDomainStartupReasonStable}, nil
	}
	refused.reason = generatedDomainStartupReasonAmbiguous
	return refused, nil
}

func generatedDomainStartupObservationGateway(stable generatedDomainGatewayExpectation,
	recovery generatedDomainRecoveryDockerExpectation,
) generatedDomainGatewayExpectation {
	if recovery.gateway.kind != generatedDomainGatewayNone {
		return recovery.gateway
	}
	return stable
}

func resolveGeneratedDomainRecoveryDockerExpectation(database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot,
) generatedDomainRecoveryDockerExpectation {
	recoveries, valid := generatedDomainStartupRecoveries(database, protected, generatedDomainDockerStartupSnapshot{})
	if !valid || len(recoveries) == 0 {
		return generatedDomainRecoveryDockerExpectation{}
	}
	var result generatedDomainRecoveryDockerExpectation
	for _, recovery := range recoveries {
		var candidate generatedDomainRecoveryDockerExpectation
		var ok bool
		switch recovery.kind {
		case generatedDomainStartupRecoveryUpgrade:
			candidate, ok = generatedDomainUpgradeRecoveryDockerExpectation(protected, recovery.operationID)
		case generatedDomainStartupRecoveryGrant, generatedDomainStartupRecoveryDisable,
			generatedDomainStartupRecoveryBatch:
			candidate, ok = generatedDomainCurrentRecoveryDockerExpectation(database, protected)
		case generatedDomainStartupRecoveryRebind:
			candidate, ok = generatedDomainRebindRecoveryDockerExpectation(database, protected, recovery.operationID)
		}
		if !ok {
			return generatedDomainRecoveryDockerExpectation{}
		}
		candidate.recoveries = nil
		if result.kind == generatedDomainRecoveryDockerNone {
			result = candidate
		} else if !reflect.DeepEqual(result, candidate) {
			return generatedDomainRecoveryDockerExpectation{}
		}
	}
	result.recoveries = canonicalGeneratedDomainStartupRecoveries(recoveries)
	return result
}

func generatedDomainUpgradeRecoveryDockerExpectation(protected generatedDomainProtectedStartupSnapshot,
	operationID string,
) (generatedDomainRecoveryDockerExpectation, bool) {
	if protected.nativeV1 == nil || len(protected.nativeV1.Active) != 0 {
		return generatedDomainRecoveryDockerExpectation{}, false
	}
	var selected *gatewayUpgradeGenerationSelection
	for index := range protected.upgrade.generations {
		generation := protected.upgrade.generations[index]
		if generation.operationID != operationID || generation.Aborted || generation.Retired {
			continue
		}
		if selected != nil {
			return generatedDomainRecoveryDockerExpectation{}, false
		}
		copy := generation
		selected = &copy
	}
	if selected == nil || !validGatewayTopologyInputs(*protected.nativeV1, selected.State, selected.Journal) ||
		len(selected.State.Apps) != 0 {
		return generatedDomainRecoveryDockerExpectation{}, false
	}
	base := generatedDomainNativeV1DockerDescriptors()
	addInfrastructure := func(values []generatedDomainRecoveryDockerDescriptor) ([]generatedDomainRecoveryDockerDescriptor, bool) {
		result := append([]generatedDomainRecoveryDockerDescriptor(nil), values...)
		resources := selected.Journal.Resources
		if resources.IngressNetworkID != "" {
			result = append(result, generatedDomainGatewayV2NetworkDescriptor(selected.State, selected.Journal))
		}
		configValid, configPresent := validGatewayV2VolumeResourceBinding(resources.ConfigVolume)
		dataValid, dataPresent := validGatewayV2VolumeResourceBinding(resources.DataVolume)
		if !configValid || !dataValid {
			return nil, false
		}
		if configPresent {
			result = append(result, generatedDomainGatewayV2VolumeDescriptor(selected.State, selected.Journal,
				gatewayV2ConfigVolumeRole, resources.ConfigVolume))
		}
		if dataPresent {
			result = append(result, generatedDomainGatewayV2VolumeDescriptor(selected.State, selected.Journal,
				gatewayV2DataVolumeRole, resources.DataVolume))
		}
		return result, true
	}
	withInfrastructure, ok := addInfrastructure(base)
	if !ok {
		return generatedDomainRecoveryDockerExpectation{}, false
	}
	withStage := append(append([]generatedDomainRecoveryDockerDescriptor(nil), withInfrastructure...),
		generatedDomainGatewayV2ContainerDescriptor(selected.State, selected.Journal, gatewayV2StageContainerRole,
			selected.Journal.Resources.StageContainerID))
	withFinal := append(append([]generatedDomainRecoveryDockerDescriptor(nil), withInfrastructure...),
		generatedDomainGatewayV2ContainerDescriptor(selected.State, selected.Journal, gatewayV2FinalContainerRole,
			selected.Journal.Resources.FinalContainerID))
	var alternatives [][]generatedDomainRecoveryDockerDescriptor
	switch selected.Journal.Phase {
	case gatewayPhasePrepared:
		alternatives = [][]generatedDomainRecoveryDockerDescriptor{base}
	case gatewayPhaseStageIntent:
		if selected.Journal.Resources.StageContainerID == "" {
			alternatives = [][]generatedDomainRecoveryDockerDescriptor{withInfrastructure}
		} else if len(withInfrastructure) == len(base)+3 {
			alternatives = [][]generatedDomainRecoveryDockerDescriptor{withStage}
		}
	case gatewayPhaseStaged:
		if selected.Journal.Resources.StageContainerID != "" && len(withInfrastructure) == len(base)+3 {
			alternatives = [][]generatedDomainRecoveryDockerDescriptor{withStage}
		}
	case gatewayPhaseTransferIntent, gatewayPhaseRollbackIntent:
		if len(withInfrastructure) == len(base)+3 {
			alternatives = append(alternatives, withInfrastructure)
			if selected.Journal.Resources.StageContainerID != "" {
				alternatives = append(alternatives, withStage)
			}
			if selected.Journal.Resources.FinalContainerID != "" {
				alternatives = append(alternatives, withFinal)
			}
		}
	case gatewayPhaseV2Serving:
		if selected.Journal.Resources.FinalContainerID != "" && len(withInfrastructure) == len(base)+3 {
			alternatives = [][]generatedDomainRecoveryDockerDescriptor{withFinal}
		}
	}
	if len(alternatives) == 0 {
		return generatedDomainRecoveryDockerExpectation{}, false
	}
	source := cloneRouteState(*protected.nativeV1)
	return generatedDomainRecoveryDockerExpectation{kind: generatedDomainRecoveryDockerUpgrade,
		alternatives: alternatives, imageID: selected.Journal.Resources.ImageID, source: &source, upgrade: selected}, true
}

func generatedDomainCurrentRecoveryDockerExpectation(database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot,
) (generatedDomainRecoveryDockerExpectation, bool) {
	gateway, ok := generatedDomainRecoveryGatewayExpectation(database, protected)
	if !ok {
		return generatedDomainRecoveryDockerExpectation{}, false
	}
	descriptors, imageID, ok := generatedDomainGatewayDockerDescriptors(gateway)
	if !ok {
		return generatedDomainRecoveryDockerExpectation{}, false
	}
	return generatedDomainRecoveryDockerExpectation{kind: generatedDomainRecoveryDockerCurrent,
		alternatives: [][]generatedDomainRecoveryDockerDescriptor{descriptors}, imageID: imageID, gateway: gateway}, true
}

func generatedDomainRecoveryGatewayExpectation(database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot,
) (generatedDomainGatewayExpectation, bool) {
	candidates := generatedDomainCommittedAndCurrentGatewayCandidates(database, protected, false)
	var valid bool
	candidates, valid = generatedDomainCollapseCurrentV2Sources(candidates)
	if !valid {
		return generatedDomainGatewayExpectation{}, false
	}
	if len(candidates) != 1 {
		return generatedDomainGatewayExpectation{}, false
	}
	result := candidates[0]
	result.names = generatedDomainGatewayExpectedNames(result)
	ids, err := generatedDomainGatewayExpectedIDs(result)
	if err != nil {
		return generatedDomainGatewayExpectation{}, false
	}
	result.ids = ids
	return result, true
}

func generatedDomainRebindRecoveryDockerExpectation(database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot, operationID string,
) (generatedDomainRecoveryDockerExpectation, bool) {
	gateway, ok := generatedDomainRecoveryGatewayExpectation(database, protected)
	if !ok {
		return generatedDomainRecoveryDockerExpectation{}, false
	}
	base, predecessorImage, ok := generatedDomainGatewayDockerDescriptors(gateway)
	if !ok {
		return generatedDomainRecoveryDockerExpectation{}, false
	}
	var intent *gatewayRebindProtectedIntentV2
	for index := range protected.rebind.IntentsV2 {
		candidate := protected.rebind.IntentsV2[index].Intent
		if candidate.OperationID == operationID {
			if intent != nil {
				return generatedDomainRecoveryDockerExpectation{}, false
			}
			copy := candidate
			intent = &copy
		}
	}
	records := make([]gatewayRebindProgressRecord, 0)
	for _, selection := range protected.rebind.Progress {
		if selection.Record.OperationID == operationID {
			records = append(records, selection.Record)
		}
	}
	result := generatedDomainRecoveryDockerExpectation{kind: generatedDomainRecoveryDockerRebind,
		gateway: gateway, alternatives: [][]generatedDomainRecoveryDockerDescriptor{base}, imageID: predecessorImage}
	if intent == nil {
		return result, len(records) == 0
	}
	result.rebindIntent = intent
	if len(records) == 0 {
		return generatedDomainRecoveryDockerExpectation{}, false
	}
	last := records[len(records)-1]
	if last.Phase == gatewayRebindProgressHandoverCommitted {
		return generatedDomainRecoveryDockerExpectation{}, false
	}
	if last.Phase == gatewayRebindProgressHandoverRolledBack {
		return result, last.TypedRollback != nil && last.TypedRollback.PhysicalProof != nil
	}
	var effect gatewayRebindTypedEffectProgress
	if last.Phase == gatewayRebindProgressRollbackIntent {
		if last.TypedRollback == nil || !validGatewayRebindTypedRollbackProgress(*last.TypedRollback) {
			return generatedDomainRecoveryDockerExpectation{}, false
		}
		var effectOK bool
		effect, effectOK = generatedDomainTypedRollbackEffect(*intent, *last.TypedRollback, records)
		if !effectOK {
			return generatedDomainRecoveryDockerExpectation{}, false
		}
		if last.TypedRollback.FromSequence == 1 {
			return result, reflect.DeepEqual(last.TypedRollback.Owned,
				gatewayRebindTypedRollbackOwnedResources{})
		}
	} else {
		if last.Sequence == 1 {
			return result, true
		}
		if last.TypedEffect == nil || !validGatewayRebindTypedEffectProgress(*last.TypedEffect, last.Sequence) {
			return generatedDomainRecoveryDockerExpectation{}, false
		}
		effect = *last.TypedEffect
	}
	if len(effect.ApplicationNetworks) != 0 {
		return generatedDomainRecoveryDockerExpectation{}, false
	}
	successor, ok := generatedDomainTypedRebindDockerDescriptors(*intent, effect)
	if !ok {
		return generatedDomainRecoveryDockerExpectation{}, false
	}
	combined := append(append([]generatedDomainRecoveryDockerDescriptor(nil), base...), successor...)
	effectCopy := effect
	result.alternatives = [][]generatedDomainRecoveryDockerDescriptor{combined}
	result.imageID = effect.ImageID
	result.rebindEffect = &effectCopy
	return result, true
}

func generatedDomainTypedRollbackEffect(intent gatewayRebindProtectedIntentV2,
	rollback gatewayRebindTypedRollbackProgress, records []gatewayRebindProgressRecord,
) (gatewayRebindTypedEffectProgress, bool) {
	if !validGatewayRebindTypedRollbackProgress(rollback) ||
		!validGatewayRebindTypedRollbackOwned(rollback.Owned, intent.Identity) {
		return gatewayRebindTypedEffectProgress{}, false
	}
	var forward *gatewayRebindProgressRecord
	for index := range records {
		record := records[index]
		if record.Sequence != rollback.FromSequence {
			continue
		}
		if forward != nil || record.Phase != rollback.FromPhase || record.Digest != rollback.FromDigest ||
			record.TypedRollback != nil || (record.Sequence == 1) != (record.TypedEffect == nil) {
			return gatewayRebindTypedEffectProgress{}, false
		}
		copy := record
		forward = &copy
	}
	if forward == nil {
		return gatewayRebindTypedEffectProgress{}, false
	}
	bound := gatewayRebindTypedRollbackOwnedResources{}
	if forward.TypedEffect != nil {
		bound = gatewayRebindTypedRollbackOwnedFromEffect(*forward.TypedEffect)
	}
	adopted := !reflect.DeepEqual(bound, rollback.Owned)
	if !gatewayRebindTypedRollbackOwnedContains(bound, rollback.Owned, rollback.FromSequence) ||
		(adopted && !validSHA256(rollback.AdoptionProofDigest)) ||
		(!adopted && rollback.AdoptionProofDigest != "") {
		return gatewayRebindTypedEffectProgress{}, false
	}
	if forward.TypedEffect == nil {
		return gatewayRebindTypedEffectProgress{}, reflect.DeepEqual(rollback.Owned,
			gatewayRebindTypedRollbackOwnedResources{})
	}
	effect := *forward.TypedEffect
	if adopted {
		effect.ImageID = rollback.Owned.ImageID
		effect.Network = rollback.Owned.IngressNetwork
		effect.ConfigVolume = rollback.Owned.ConfigVolume
		effect.DataVolume = rollback.Owned.DataVolume
		effect.StageContainer = rollback.Owned.StageContainer
		effect.FinalContainer = rollback.Owned.FinalContainer
		effect.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil),
			rollback.Owned.ApplicationNetworks...)
	}
	planDigest, err := gatewayRebindTypedStagePlanDigest(intent)
	if err != nil || effect.StagePlanDigest != planDigest || !validSHA256(effect.ImageID) ||
		!reflect.DeepEqual(gatewayRebindTypedRollbackOwnedFromEffect(effect), rollback.Owned) {
		return gatewayRebindTypedEffectProgress{}, false
	}
	if _, ok := generatedDomainTypedRebindDockerDescriptors(intent, effect); !ok {
		return gatewayRebindTypedEffectProgress{}, false
	}
	return effect, true
}

func generatedDomainNativeV1DockerDescriptors() []generatedDomainRecoveryDockerDescriptor {
	return []generatedDomainRecoveryDockerDescriptor{
		{kind: "container", name: caddyContainerName, labels: map[string]string{
			gatewayV2ManagedLabelKey: gatewayV2ManagedContainerLabel, gatewayV2IdentityLabelKey: gatewayV1IdentityVersion,
			gatewayV2ListenerIsolationKey: gatewayV2ListenerIsolationVersion}},
		{kind: "network", name: caddyNetworkName, labels: map[string]string{
			gatewayV2ManagedLabelKey: gatewayV2ManagedNetworkLabel, gatewayV2IdentityLabelKey: gatewayV1IdentityVersion},
			data: generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}},
		{kind: "volume", name: caddyVolumeName, labels: map[string]string{
			gatewayV2ManagedLabelKey: gatewayV2ManagedContainerLabel, gatewayV2IdentityLabelKey: gatewayV1IdentityVersion},
			data: generatedDomainDockerObjectData{driver: "local", scope: "local", options: map[string]string{}}},
	}
}

func generatedDomainGatewayV2NetworkDescriptor(state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) generatedDomainRecoveryDockerDescriptor {
	return generatedDomainRecoveryDockerDescriptor{kind: "network", id: journal.Resources.IngressNetworkID,
		name:   state.Identity.IngressNetwork,
		labels: gatewayV2ResourceLabels(state, journal, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole, false),
		data:   generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}}
}

func generatedDomainGatewayV2VolumeDescriptor(state gatewayV2RouteState, journal gatewayMigrationJournal,
	role string, binding gatewayV2VolumeResourceBinding,
) generatedDomainRecoveryDockerDescriptor {
	name := state.Identity.ConfigVolume
	if role == gatewayV2DataVolumeRole {
		name = state.Identity.DataVolume
	}
	return generatedDomainRecoveryDockerDescriptor{kind: "volume", name: name,
		labels: gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, role, false),
		data: generatedDomainDockerObjectData{driver: "local", scope: "local", options: map[string]string{},
			mountpoint: binding.Mountpoint, createdAt: binding.CreatedAt}}
}

func generatedDomainGatewayV2ContainerDescriptor(state gatewayV2RouteState, journal gatewayMigrationJournal,
	role, id string,
) generatedDomainRecoveryDockerDescriptor {
	name := state.Identity.StageContainer
	if role == gatewayV2FinalContainerRole {
		name = state.Identity.FinalContainer
	}
	return generatedDomainRecoveryDockerDescriptor{kind: "container", id: id, name: name,
		labels: gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, role, true)}
}

func generatedDomainGatewayDockerDescriptors(value generatedDomainGatewayExpectation) (
	[]generatedDomainRecoveryDockerDescriptor, string, bool,
) {
	switch value.kind {
	case generatedDomainGatewayNativeV1:
		if value.nativeV1 == nil || len(value.nativeV1.Active) != 0 {
			return nil, "", false
		}
		return generatedDomainNativeV1DockerDescriptors(), "", true
	case generatedDomainGatewayNativeV2:
		if value.nativeV2 == nil || value.journal == nil || len(value.nativeV2.Apps) != 0 {
			return nil, "", false
		}
		state, journal := *value.nativeV2, *value.journal
		values := []generatedDomainRecoveryDockerDescriptor{
			generatedDomainGatewayV2ContainerDescriptor(state, journal, gatewayV2FinalContainerRole, journal.Resources.FinalContainerID),
			generatedDomainGatewayV2NetworkDescriptor(state, journal),
			generatedDomainGatewayV2VolumeDescriptor(state, journal, gatewayV2ConfigVolumeRole, journal.Resources.ConfigVolume),
			generatedDomainGatewayV2VolumeDescriptor(state, journal, gatewayV2DataVolumeRole, journal.Resources.DataVolume),
		}
		return values, journal.Resources.ImageID, true
	case generatedDomainGatewayCurrentV1, generatedDomainGatewayCurrentV2:
		return generatedDomainCurrentGatewayDockerDescriptors(value)
	default:
		return nil, "", false
	}
}

func generatedDomainCurrentGatewayDockerDescriptors(value generatedDomainGatewayExpectation) (
	[]generatedDomainRecoveryDockerDescriptor, string, bool,
) {
	if value.currentV2 != nil && value.currentV2.Lineage.Kind == appaccess.GatewayRebindSourceGatewayUpgrade {
		if value.terminal != nil || value.nativeV1 == nil || value.nativeV2 == nil || value.journal == nil ||
			value.upgrade == nil || len(value.currentV2.Apps) != 0 || len(value.nativeV2.Apps) != 0 ||
			!generatedDomainCurrentV2UpgradeExpectationValid(value) {
			return nil, "", false
		}
		state, journal := *value.nativeV2, *value.journal
		values := []generatedDomainRecoveryDockerDescriptor{
			generatedDomainGatewayV2ContainerDescriptor(state, journal, gatewayV2FinalContainerRole,
				journal.Resources.FinalContainerID),
			generatedDomainGatewayV2NetworkDescriptor(state, journal),
			generatedDomainGatewayV2VolumeDescriptor(state, journal, gatewayV2ConfigVolumeRole,
				journal.Resources.ConfigVolume),
			generatedDomainGatewayV2VolumeDescriptor(state, journal, gatewayV2DataVolumeRole,
				journal.Resources.DataVolume),
		}
		return values, journal.Resources.ImageID, true
	}
	if value.terminal == nil {
		return nil, "", false
	}
	terminal := *value.terminal
	var lineage appaccess.GatewayCurrentLineageRef
	var network gatewayV2NetworkPlan
	var identity gatewayCurrentIdentity
	var appCount int
	if value.currentV1 != nil {
		lineage, network, identity, appCount = value.currentV1.Lineage, value.currentV1.Network,
			value.currentV1.Identity, len(value.currentV1.Apps)
	} else if value.currentV2 != nil {
		lineage, network, identity, appCount = value.currentV2.Lineage, value.currentV2.Network,
			value.currentV2.Identity, len(value.currentV2.Apps)
	} else {
		return nil, "", false
	}
	if appCount != 0 || identity.Rebind == nil || terminal.Resources.FinalContainer == nil ||
		len(terminal.SuccessorIdentity.Digest) < 12 {
		return nil, "", false
	}
	facts := gatewayFinalOwnershipFacts{Lineage: lineage, Terminal: terminal, Network: network}
	final := identity.Rebind
	values := []generatedDomainRecoveryDockerDescriptor{
		{kind: "container", id: terminal.Resources.FinalContainer.ID, name: final.FinalContainer,
			labels: gatewayFinalOwnershipLabels(facts, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole)},
		{kind: "network", id: terminal.Resources.IngressNetwork.ID, name: final.IngressNetwork,
			labels: gatewayFinalOwnershipLabels(facts, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole),
			data: generatedDomainDockerObjectData{driver: "bridge", scope: "local",
				options: map[string]string{gatewayRebindBridgeNameOptionKey: "rig" + terminal.SuccessorIdentity.Digest[:12]}}},
		{kind: "volume", name: final.ConfigVolume,
			labels: gatewayFinalOwnershipLabels(facts, gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole),
			data: generatedDomainDockerObjectData{driver: "local", scope: "local", options: map[string]string{},
				mountpoint: terminal.Resources.ConfigVolume.Mountpoint, createdAt: terminal.Resources.ConfigVolume.CreatedAt}},
		{kind: "volume", name: final.DataVolume,
			labels: gatewayFinalOwnershipLabels(facts, gatewayV2ManagedContainerLabel, gatewayV2DataVolumeRole),
			data: generatedDomainDockerObjectData{driver: "local", scope: "local", options: map[string]string{},
				mountpoint: terminal.Resources.DataVolume.Mountpoint, createdAt: terminal.Resources.DataVolume.CreatedAt}},
	}
	return values, terminal.Resources.ImageID, true
}

func generatedDomainTypedRebindDockerDescriptors(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress,
) ([]generatedDomainRecoveryDockerDescriptor, bool) {
	result := make([]generatedDomainRecoveryDockerDescriptor, 0, 5)
	if effect.Network != nil {
		bridge, err := gatewayRebindTypedStageBridgeName(intent)
		if err != nil || !gatewayRebindTypedStageNetworkBindingMatches(intent, *effect.Network) {
			return nil, false
		}
		result = append(result, generatedDomainRecoveryDockerDescriptor{kind: "network", id: effect.Network.ID,
			name:   intent.Identity.IngressNetwork,
			labels: gatewayRebindTypedStageResourceLabels(intent, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole),
			data: generatedDomainDockerObjectData{driver: "bridge", scope: "local",
				options: map[string]string{gatewayRebindBridgeNameOptionKey: bridge}}})
	}
	if effect.ConfigVolume != nil {
		if !gatewayRebindTypedStageConfigVolumeBindingMatches(intent, *effect.ConfigVolume) {
			return nil, false
		}
		result = append(result, generatedDomainRecoveryDockerDescriptor{kind: "volume", name: intent.Identity.ConfigVolume,
			labels: gatewayRebindTypedStageResourceLabels(intent, gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole),
			data: generatedDomainDockerObjectData{driver: "local", scope: "local", options: map[string]string{},
				mountpoint: effect.ConfigVolume.Mountpoint, createdAt: effect.ConfigVolume.CreatedAt}})
	}
	if effect.DataVolume != nil {
		if !gatewayRebindTypedStageDataVolumeBindingMatches(intent, *effect.DataVolume) {
			return nil, false
		}
		result = append(result, generatedDomainRecoveryDockerDescriptor{kind: "volume", name: intent.Identity.DataVolume,
			labels: gatewayRebindTypedStageResourceLabels(intent, gatewayV2ManagedContainerLabel, gatewayV2DataVolumeRole),
			data: generatedDomainDockerObjectData{driver: "local", scope: "local", options: map[string]string{},
				mountpoint: effect.DataVolume.Mountpoint, createdAt: effect.DataVolume.CreatedAt}})
	}
	if effect.StageContainer != nil {
		if !gatewayRebindTypedStageContainerBindingMatches(intent, effect, *effect.StageContainer) {
			return nil, false
		}
		result = append(result, generatedDomainRecoveryDockerDescriptor{kind: "container", id: effect.StageContainer.ID,
			name:   intent.Identity.StageContainer,
			labels: gatewayRebindTypedStageResourceLabels(intent, gatewayV2ManagedContainerLabel, gatewayV2StageContainerRole)})
	}
	if effect.FinalContainer != nil {
		if effect.HandoverIntent == nil {
			return nil, false
		}
		expected, err := gatewayRebindTypedFinalContainerBindingFor(intent, effect,
			effect.HandoverIntent.Plan, effect.FinalContainer.ID)
		if err != nil || !reflect.DeepEqual(expected, *effect.FinalContainer) {
			return nil, false
		}
		result = append(result, generatedDomainRecoveryDockerDescriptor{kind: "container", id: effect.FinalContainer.ID,
			name:   intent.Identity.FinalContainer,
			labels: gatewayRebindTypedStageResourceLabels(intent, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole)})
	}
	return result, true
}

func resolveGeneratedDomainDockerCensus(protected generatedDomainProtectedStartupSnapshot) (
	generatedDomainGatewayExpectation, error,
) {
	result := generatedDomainGatewayExpectation{names: map[string]map[string]struct{}{
		"container": {}, "network": {}, "volume": {}, "image": {},
	}, ids: map[string]map[string]struct{}{"container": {}, "network": {}, "image": {}}}
	addName := func(kind, name string) {
		if name != "" {
			result.names[kind][name] = struct{}{}
		}
	}
	addID := func(kind, id string) bool {
		if id == "" {
			return true
		}
		normalized := normalizeID(id)
		if !generatedDomainCanonicalDockerID(normalized, false) {
			return false
		}
		result.ids[kind][normalized] = struct{}{}
		return true
	}
	addV1 := func() {
		addName("container", caddyContainerName)
		addName("network", caddyNetworkName)
		addName("volume", caddyVolumeName)
	}
	addV2Identity := func(identity gatewayV2Identity) {
		addName("container", identity.StageContainer)
		addName("container", identity.FinalContainer)
		addName("network", identity.IngressNetwork)
		addName("volume", identity.ConfigVolume)
		addName("volume", identity.DataVolume)
	}
	addRebindIdentity := func(identity gatewayRebindSuccessorIdentity) {
		addName("container", identity.StageContainer)
		addName("container", identity.FinalContainer)
		addName("network", identity.IngressNetwork)
		addName("volume", identity.ConfigVolume)
		addName("volume", identity.DataVolume)
	}
	addResources := func(resources gatewayV2ResourceBindings) bool {
		return addID("image", resources.ImageID) && addID("network", resources.IngressNetworkID) &&
			addID("container", resources.StageContainerID) && addID("container", resources.FinalContainerID)
	}
	addTerminalResources := func(resources gatewayRebindFinalHandoverResourceBindings) bool {
		if !addID("image", resources.ImageID) || !addID("network", resources.IngressNetwork.ID) {
			return false
		}
		return resources.FinalContainer == nil || addID("container", resources.FinalContainer.ID)
	}
	if protected.nativeV1 != nil || len(protected.upgrade.generations) != 0 {
		addV1()
	}
	for _, generation := range protected.upgrade.generations {
		addV2Identity(generation.State.Identity)
		if !addResources(generation.Journal.Resources) {
			return generatedDomainGatewayExpectation{}, errGeneratedDomainStartupGatewayInvalid
		}
	}
	for _, state := range protected.currentV1 {
		if state.Identity.Upgrade != nil {
			addV2Identity(*state.Identity.Upgrade)
		}
		if state.Identity.Rebind != nil {
			addRebindIdentity(*state.Identity.Rebind)
		}
	}
	for _, entry := range protected.currentV2.files {
		state := entry.State
		if state.Identity.Upgrade != nil {
			addV2Identity(*state.Identity.Upgrade)
		}
		if state.Identity.Rebind != nil {
			addRebindIdentity(*state.Identity.Rebind)
		}
	}
	for _, intent := range protected.rebind.Intents {
		addRebindIdentity(intent.Intent.Intent.Identity)
	}
	for _, intent := range protected.rebind.IntentsV2 {
		addRebindIdentity(intent.Intent.Identity)
	}
	for _, terminal := range protected.terminals {
		addRebindIdentity(terminal.SuccessorIdentity)
		if !addTerminalResources(terminal.Resources) {
			return generatedDomainGatewayExpectation{}, errGeneratedDomainStartupGatewayInvalid
		}
	}
	return result, nil
}

func generatedDomainStartupDatabaseCold(value deployments.GeneratedDomainStartupSnapshot) bool {
	return len(value.Runtime.ActiveHeads) != 0 || len(value.Runtime.Deployments) != 0 ||
		len(value.Runtime.Components) != 0 || len(value.Runtime.CandidateIntents) != 0 ||
		len(value.GeneratedDeployments) != 0 || len(value.Artifacts) != 0 ||
		value.LifecycleOperationCount != 0 || len(value.UnresolvedLifecycleApps) != 0 ||
		len(value.Gateway.RuntimeHeads) != 0 || len(value.Gateway.RuntimeComponents) != 0
}

func generatedDomainStartupDatabaseEffectEmpty(value deployments.GeneratedDomainStartupSnapshot) bool {
	if generatedDomainStartupDatabaseCold(value) || len(value.Gateway.Upgrades.Claims) != 0 ||
		len(value.Gateway.Grants.Claims) != 0 || len(value.Gateway.Disables.Claims) != 0 ||
		len(value.Gateway.Rebind.History) != 0 || value.Gateway.Rebind.Active != nil ||
		value.Gateway.Rebind.CurrentSource != nil || value.Gateway.Rebind.CurrentProfile != nil ||
		value.Gateway.ActiveRebindApprovalAuthority != nil {
		return false
	}
	return value.Gateway.Upgrades.CurrentProfile == nil
}

func generatedDomainStartupRecoveries(database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot, docker generatedDomainDockerStartupSnapshot,
) ([]generatedDomainStartupRecovery, bool) {
	if docker.ambiguous {
		return nil, false
	}
	result := make([]generatedDomainStartupRecovery, 0)
	upgradeClaims := generatedDomainUpgradeStartupClaims(database.Gateway.Upgrades)
	claimSet, err := validateGatewayV2StartupClaims(upgradeClaims)
	if err != nil {
		return nil, false
	}
	matchedUpgrades := make(map[string]struct{})
	for _, generation := range protected.upgrade.generations {
		if generation.Aborted || generation.Retired || generation.Journal.Phase == gatewayPhaseCommitted {
			continue
		}
		claim, exists := claimSet.byOperation[generation.operationID]
		localHostPort := uint16(0)
		if generation.Existing {
			localHostPort = generation.Journal.Source.LocalHostPort
		}
		if !exists || !gatewayV2StartupClaimIsNonterminal(claim.State) ||
			!gatewayV2StartupClaimMatchesSelection(claim, generation, localHostPort) {
			return nil, false
		}
		result = append(result, generatedDomainStartupRecovery{kind: generatedDomainStartupRecoveryUpgrade,
			operationID: generation.operationID})
		matchedUpgrades[generation.operationID] = struct{}{}
	}
	for operationID, claim := range claimSet.byOperation {
		if gatewayV2StartupClaimIsNonterminal(claim.State) {
			if _, matched := matchedUpgrades[operationID]; !matched {
				return nil, false
			}
		}
	}

	grantClaims := GatewayLANGrantStartupClaims(database.Gateway.Grants)
	disableClaims := GatewayLANDisableStartupClaims(database.Gateway.Disables)
	lanClaims, err := validateGatewayV2LANAccessStartupClaims(grantClaims, disableClaims)
	if err != nil {
		return nil, false
	}
	lanStateMatched := false
	if protected.upgrade.committed && generatedDomainUpgradeDatabaseCurrent(database, protected.upgrade) {
		lanStateMatched = true
		values, valid := generatedDomainNativeV2LANRecoveries(protected.upgrade.state, lanClaims)
		if !valid {
			return nil, false
		}
		result = append(result, values...)
	}
	for _, state := range protected.currentV1 {
		if !generatedDomainCurrentDatabaseMatches(database, state.Lineage, state.Profile, state.TransferManifestDigest) {
			continue
		}
		lanStateMatched = true
		values, valid := generatedDomainCurrentLANRecoveries(database, state, lanClaims)
		if !valid {
			return nil, false
		}
		result = append(result, values...)
	}
	if !lanStateMatched && generatedDomainLANClaimsRequireRecovery(lanClaims) {
		return nil, false
	}

	if active := database.Gateway.Rebind.Active; active != nil {
		if !protected.hasRebind || database.Gateway.Rebind.Phase == "" {
			return nil, false
		}
		evidence, _, complete, projectionErr := gatewayRebindProtectedRecoveryProjection(protected.rebind, *active)
		operationID := generatedDomainRebindEntryOperationID(*active)
		if projectionErr != nil || !complete || operationID == "" || evidence.OperationID != operationID {
			return nil, false
		}
		result = append(result, generatedDomainStartupRecovery{kind: generatedDomainStartupRecoveryRebind,
			operationID: operationID})
	} else if !generatedDomainCompletedRebindHistoryMatchesCurrent(database, protected) {
		return nil, false
	}
	return canonicalGeneratedDomainStartupRecoveries(result), true
}

func generatedDomainUpgradeStartupClaims(snapshot appaccess.GatewayUpgradeStartupSnapshot) []GatewayV2StartupClaim {
	result := make([]GatewayV2StartupClaim, 0, len(snapshot.Claims))
	for _, entry := range snapshot.Claims {
		result = append(result, GatewayV2StartupClaim{Request: GatewayV2UpgradeRequest{
			OperationID: entry.Claim.OperationID,
			Profile: GatewayV2ProfileBinding{RevisionID: entry.Profile.ID,
				RevisionNumber: entry.Profile.RevisionNumber, SelectedIPv4: entry.Profile.Spec.SelectedIPv4,
				InterfaceID: entry.Profile.Spec.InterfaceID, PortStart: entry.Profile.Spec.PortStart,
				PortEnd: entry.Profile.Spec.PortEnd, SpecDigest: entry.Profile.SpecDigest},
			ApprovedBy: entry.Claim.ApprovedBy, ApprovedActionDigest: entry.ApprovedActionDigest,
		}, State: entry.Claim.State, StateSequence: entry.Claim.StateSequence})
	}
	return result
}

func generatedDomainNativeV2LANRecoveries(state gatewayV2RouteState,
	claims gatewayV2LANAccessStartupClaims,
) ([]generatedDomainStartupRecovery, bool) {
	if state.LANRecovery != nil {
		if state.Pending != nil || !gatewayV2LANRecoveryCensusMatchesHead(state, claims) {
			return nil, false
		}
		return []generatedDomainStartupRecovery{{kind: generatedDomainStartupRecoveryBatch,
			operationID: state.OperationID}}, true
	}
	if state.Pending == nil {
		return nil, !generatedDomainLANClaimsRequireRecovery(claims)
	}
	switch state.Pending.Kind {
	case gatewayV2PendingLANGrant, gatewayV2PendingLANWithdrawal:
		request, err := gatewayV2LANPendingRequest(*state.Pending)
		claim, exists := claims.grants.byAttempt[request.AttemptID]
		if err != nil || !exists || claim.Request != request {
			return nil, false
		}
		return []generatedDomainStartupRecovery{{kind: generatedDomainStartupRecoveryGrant,
			operationID: request.AttemptID}}, true
	case gatewayV2PendingLANDisable:
		if state.Pending.Disable == nil {
			return nil, false
		}
		request := *state.Pending.Disable
		claim, exists := claims.disables[request.OperationID]
		if !exists || !reflect.DeepEqual(claim.Request, request) || claim.ClearAcknowledged {
			return nil, false
		}
		return []generatedDomainStartupRecovery{{kind: generatedDomainStartupRecoveryDisable,
			operationID: request.OperationID}}, true
	default:
		return nil, false
	}
}

func generatedDomainCurrentLANRecoveries(database deployments.GeneratedDomainStartupSnapshot,
	state gatewayCurrentRouteState, claims gatewayV2LANAccessStartupClaims,
) ([]generatedDomainStartupRecovery, bool) {
	if state.LANRecovery != nil {
		if state.Pending != nil || !gatewayCurrentLANRecoveryCensusMatchesHead(
			state, claims, database.Gateway.Rebind.CurrentTransfers) {
			return nil, false
		}
		return []generatedDomainStartupRecovery{{kind: generatedDomainStartupRecoveryBatch,
			operationID: state.Lineage.OperationID}}, true
	}
	inspection, err := gatewayCurrentLANStartupCensus(state, claims)
	if err != nil {
		return nil, false
	}
	if inspection.Disposition == GatewayV2LANStartupNormal {
		return nil, true
	}
	if inspection.Disposition != GatewayV2LANStartupRecoveryOnly {
		return nil, false
	}
	values := inspection.Recoveries
	if len(values) == 0 {
		values = []GatewayV2LANAccessStartupRecovery{{Kind: inspection.RecoveryKind,
			OperationID: inspection.OperationID, AppID: inspection.AppID}}
	}
	result := make([]generatedDomainStartupRecovery, 0, len(values))
	for _, value := range values {
		switch value.Kind {
		case GatewayV2LANRecoveryGrant:
			result = append(result, generatedDomainStartupRecovery{kind: generatedDomainStartupRecoveryGrant,
				operationID: value.OperationID})
		case GatewayV2LANRecoveryDisable:
			result = append(result, generatedDomainStartupRecovery{kind: generatedDomainStartupRecoveryDisable,
				operationID: value.OperationID})
		default:
			return nil, false
		}
	}
	return result, true
}

func generatedDomainLANClaimsRequireRecovery(claims gatewayV2LANAccessStartupClaims) bool {
	for _, claim := range claims.grants.byAttempt {
		if claim.State != appaccess.AppAccessGrantCommitted && claim.State != appaccess.AppAccessGrantRolledBack {
			return true
		}
	}
	for _, claim := range claims.disables {
		if claim.State != appaccess.AppAccessDisableCommitted || !claim.ClearAcknowledged {
			return true
		}
	}
	return false
}

func generatedDomainRebindEntryOperationID(value appaccess.GatewayRebindHistoryEntry) string {
	switch {
	case value.Claim.SpecVersion == 1 && value.Claim.Legacy != nil && value.Claim.V2 == nil:
		return value.Claim.Legacy.Spec.OperationID
	case value.Claim.SpecVersion == appaccess.GatewayRebindSpecVersionV2 && value.Claim.Legacy == nil && value.Claim.V2 != nil:
		return value.Claim.V2.Spec.OperationID
	default:
		return ""
	}
}

func generatedDomainStartupStableGateway(database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot, docker generatedDomainDockerStartupSnapshot,
) bool {
	if !docker.gateway.exact || protected.nativeV1 != nil && protected.nativeV1.Pending != nil {
		return false
	}
	evidence := docker.gateway
	switch evidence.kind {
	case generatedDomainGatewayNativeV1:
		if !generatedDomainStartupDatabaseEffectEmpty(database) || protected.nativeV1 == nil || len(protected.upgrade.generations) != 0 ||
			len(protected.currentV1) != 0 || len(protected.currentV2.files) != 0 || len(protected.rebindArtifacts) != 0 {
			return false
		}
		digest, err := canonicalDigest(*protected.nativeV1)
		return err == nil && evidence.stateDigest == digest
	case generatedDomainGatewayNativeV2:
		if !protected.upgrade.committed || protected.upgrade.state.Pending != nil ||
			protected.upgrade.state.LANRecovery != nil || evidence.operationID != protected.upgrade.state.OperationID {
			return false
		}
		digest, err := canonicalDigest(protected.upgrade.state)
		return err == nil && evidence.stateDigest == digest && generatedDomainUpgradeDatabaseCurrent(database, protected.upgrade)
	case generatedDomainGatewayCurrentV1:
		for _, state := range protected.currentV1 {
			if state.Pending == nil && state.LANRecovery == nil && evidence.operationID == state.Lineage.OperationID &&
				evidence.protectedGeneration == state.Lineage.ProtectedGeneration && evidence.stateDigest == state.Digest &&
				generatedDomainCurrentDatabaseMatches(database, state.Lineage, state.Profile, state.TransferManifestDigest) {
				return true
			}
		}
	case generatedDomainGatewayCurrentV2:
		for _, entry := range protected.currentV2.files {
			state := entry.State
			if evidence.operationID == state.Lineage.OperationID &&
				evidence.protectedGeneration == state.Lineage.ProtectedGeneration && evidence.stateDigest == state.Digest &&
				generatedDomainCurrentDatabaseMatches(database, state.Lineage, state.Profile, state.TransferManifestDigest) {
				return true
			}
		}
	}
	return false
}

func generatedDomainUpgradeDatabaseCurrent(database deployments.GeneratedDomainStartupSnapshot,
	history gatewayUpgradeHistory,
) bool {
	if database.Gateway.Rebind.CurrentSource == nil || database.Gateway.Rebind.CurrentProfile == nil ||
		database.Gateway.Rebind.CurrentSource.Kind != appaccess.GatewayRebindSourceGatewayUpgrade ||
		database.Gateway.Rebind.CurrentSource.OperationID != history.state.OperationID {
		return false
	}
	for _, claim := range database.Gateway.Upgrades.Claims {
		if claim.Claim.OperationID == history.state.OperationID && claim.Claim.State == appaccess.GatewayProfileUpgradeCommitted &&
			claim.Profile.ID == history.state.Profile.RevisionID && claim.Profile.RevisionNumber == history.state.Profile.RevisionNumber &&
			claim.Profile.SpecDigest == history.state.Profile.SpecDigest {
			return true
		}
	}
	return false
}

// generatedDomainCommittedUpgradeGatewayCandidate binds the SQL-selected
// committed upgrade to its exact active protected generation and retained V1
// source. The source is carried with the candidate so a current V2 projection
// can prove its upgrade origin without inventing a rebind terminal.
func generatedDomainCommittedUpgradeGatewayCandidate(database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot, stableOnly bool,
) (generatedDomainGatewayExpectation, bool) {
	if protected.nativeV1 == nil || !protected.upgrade.committed ||
		stableOnly && (protected.upgrade.state.Pending != nil || protected.upgrade.state.LANRecovery != nil) ||
		!generatedDomainUpgradeDatabaseCurrent(database, protected.upgrade) {
		return generatedDomainGatewayExpectation{}, false
	}
	var selected *gatewayUpgradeGenerationSelection
	for index := range protected.upgrade.generations {
		candidate := protected.upgrade.generations[index]
		if candidate.Store != protected.upgrade.store || !candidate.Existing || candidate.PartialState ||
			candidate.Retired || candidate.Aborted || candidate.Journal.Phase != gatewayPhaseCommitted ||
			!reflect.DeepEqual(candidate.State, protected.upgrade.state) ||
			!reflect.DeepEqual(candidate.Journal, protected.upgrade.journal) {
			continue
		}
		if selected != nil {
			return generatedDomainGatewayExpectation{}, false
		}
		copy := candidate
		selected = &copy
	}
	if selected == nil || !validGatewayTopologyInputs(*protected.nativeV1, selected.State, selected.Journal) {
		return generatedDomainGatewayExpectation{}, false
	}
	if _, err := gatewayUpgradeCurrentLineage(*selected); err != nil {
		return generatedDomainGatewayExpectation{}, false
	}
	state := cloneGatewayV2RouteState(selected.State)
	journal := selected.Journal
	source := cloneRouteState(*protected.nativeV1)
	selection := *selected
	digest, err := canonicalDigest(state)
	if err != nil {
		return generatedDomainGatewayExpectation{}, false
	}
	return generatedDomainGatewayExpectation{kind: generatedDomainGatewayNativeV2,
		stateDigest: digest, nativeV1: &source, nativeV2: &state, journal: &journal, upgrade: &selection}, true
}

func generatedDomainCurrentDatabaseMatches(database deployments.GeneratedDomainStartupSnapshot,
	lineage appaccess.GatewayCurrentLineageRef, profile gatewayProfileBinding, transferDigest string,
) bool {
	snapshot := database.Gateway.Rebind
	if snapshot.CurrentSource == nil || snapshot.CurrentProfile == nil ||
		gatewayCurrentAuthority(lineage) != *snapshot.CurrentSource ||
		!gatewayCurrentProfileMatchesAuthority(snapshot.CurrentProfile, *snapshot.CurrentSource) ||
		snapshot.CurrentProfile.ID != profile.RevisionID || snapshot.CurrentProfile.RevisionNumber != profile.RevisionNumber ||
		snapshot.CurrentProfile.SpecDigest != profile.SpecDigest {
		return false
	}
	digest, err := appaccess.GatewayRebindTransferManifestDigest(snapshot.CurrentTransfers)
	return err == nil && digest == transferDigest
}

func generatedDomainCurrentGatewayCandidates(database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot, stableOnly bool,
) []generatedDomainGatewayExpectation {
	var result []generatedDomainGatewayExpectation
	for _, value := range protected.currentV1 {
		state := cloneGatewayCurrentRouteState(value)
		terminal, ok := protected.terminals[state.Lineage.OperationID]
		if !ok || stableOnly && (state.Pending != nil || state.LANRecovery != nil) ||
			!generatedDomainCurrentDatabaseMatches(database, state.Lineage, state.Profile, state.TransferManifestDigest) ||
			!gatewayRebindAttemptTerminalMatchesLineage(terminal, state.Lineage) {
			continue
		}
		terminalCopy := terminal
		result = append(result, generatedDomainGatewayExpectation{kind: generatedDomainGatewayCurrentV1,
			stateDigest: state.Digest, currentV1: &state, terminal: &terminalCopy})
	}
	for _, entry := range protected.currentV2.files {
		state := cloneGatewayCurrentRouteStateV2(entry.State)
		terminal, ok := protected.terminals[state.Lineage.OperationID]
		if !generatedDomainCurrentDatabaseMatches(database, state.Lineage, state.Profile, state.TransferManifestDigest) {
			continue
		}
		if state.Lineage.Kind == appaccess.GatewayRebindSourceGatewayRebind &&
			(!ok || !gatewayRebindAttemptTerminalMatchesLineage(terminal, state.Lineage)) {
			continue
		}
		terminalCopy := terminal
		var terminalPointer *gatewayRebindAttemptTerminalView
		if ok {
			terminalPointer = &terminalCopy
		}
		result = append(result, generatedDomainGatewayExpectation{kind: generatedDomainGatewayCurrentV2,
			stateDigest: state.Digest, currentV2: &state, terminal: terminalPointer})
	}
	return result
}

// generatedDomainCommittedAndCurrentGatewayCandidates is the common
// candidate boundary for the SQL-selected committed upgrade and its current
// projections. Keeping this assembly shared prevents a retained completed
// rebind history from changing which current source is considered.
func generatedDomainCommittedAndCurrentGatewayCandidates(database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot, stableOnly bool,
) []generatedDomainGatewayExpectation {
	var result []generatedDomainGatewayExpectation
	if candidate, ok := generatedDomainCommittedUpgradeGatewayCandidate(database, protected, stableOnly); ok {
		result = append(result, candidate)
	}
	return append(result, generatedDomainCurrentGatewayCandidates(database, protected, stableOnly)...)
}

// A current V2 artifact is an additive projection. It can replace only the
// exact retained source selected by its canonical origin descriptor. Keeping
// either an unmatched V2 artifact or more than one possible source ambiguous
// prevents a format preference from becoming authority.
func generatedDomainCollapseCurrentV2Sources(candidates []generatedDomainGatewayExpectation) (
	[]generatedDomainGatewayExpectation, bool,
) {
	remove := make(map[int]struct{})
	for currentIndex := range candidates {
		current := candidates[currentIndex]
		if current.kind != generatedDomainGatewayCurrentV2 || current.currentV2 == nil {
			continue
		}
		matches := 0
		matchedIndex := -1
		var matched generatedDomainGatewayExpectation
		for sourceIndex := range candidates {
			source := candidates[sourceIndex]
			selection := gatewayCurrentSelection{Lineage: current.currentV2.Lineage}
			switch {
			case current.currentV2.Lineage.Kind == appaccess.GatewayRebindSourceGatewayRebind &&
				source.kind == generatedDomainGatewayCurrentV1 && source.currentV1 != nil:
				selection.Kind, selection.State = gatewayCurrentSelectionRebind, source.currentV1
			case current.currentV2.Lineage.Kind == appaccess.GatewayRebindSourceGatewayUpgrade &&
				source.kind == generatedDomainGatewayNativeV2 && source.nativeV1 != nil &&
				source.nativeV2 != nil && source.journal != nil && source.upgrade != nil:
				selection.Kind, selection.Upgrade, selection.UpgradeSource =
					gatewayCurrentSelectionUpgrade, source.upgrade, source.nativeV1
			default:
				continue
			}
			expected, err := gatewayCurrentExpectedOriginV2(selection)
			if err != nil || !sameGatewayCurrentRouteOriginDescriptorV2(
				gatewayCurrentRouteOriginForStateV2(*current.currentV2), expected) ||
				!gatewayCurrentV2MatchesSelectedOrigin(*current.currentV2, selection) {
				continue
			}
			matches++
			matchedIndex = sourceIndex
			matched = source
		}
		if matches != 1 {
			return nil, false
		}
		if current.currentV2.Lineage.Kind == appaccess.GatewayRebindSourceGatewayUpgrade {
			source := cloneRouteState(*matched.nativeV1)
			state := cloneGatewayV2RouteState(*matched.nativeV2)
			journal, upgrade := *matched.journal, *matched.upgrade
			candidates[currentIndex].nativeV1, candidates[currentIndex].nativeV2 = &source, &state
			candidates[currentIndex].journal, candidates[currentIndex].upgrade = &journal, &upgrade
		}
		remove[matchedIndex] = struct{}{}
	}
	result := make([]generatedDomainGatewayExpectation, 0, len(candidates)-len(remove))
	for index := range candidates {
		if _, drop := remove[index]; !drop {
			result = append(result, candidates[index])
		}
	}
	return result, true
}

func generatedDomainCurrentV2UpgradeExpectationValid(value generatedDomainGatewayExpectation) bool {
	if value.kind != generatedDomainGatewayCurrentV2 || value.currentV2 == nil || value.nativeV1 == nil ||
		value.nativeV2 == nil || value.journal == nil || value.upgrade == nil || value.terminal != nil ||
		value.currentV2.Lineage.Kind != appaccess.GatewayRebindSourceGatewayUpgrade ||
		!reflect.DeepEqual(value.upgrade.State, *value.nativeV2) ||
		!reflect.DeepEqual(value.upgrade.Journal, *value.journal) ||
		!validGatewayTopologyInputs(*value.nativeV1, *value.nativeV2, *value.journal) {
		return false
	}
	lineage, err := gatewayUpgradeCurrentLineage(*value.upgrade)
	if err != nil || lineage != value.currentV2.Lineage {
		return false
	}
	selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionUpgrade, Lineage: lineage,
		Upgrade: value.upgrade, UpgradeSource: value.nativeV1}
	expected, err := gatewayCurrentExpectedOriginV2(selection)
	return err == nil && sameGatewayCurrentRouteOriginDescriptorV2(
		gatewayCurrentRouteOriginForStateV2(*value.currentV2), expected) &&
		gatewayCurrentV2MatchesSelectedOrigin(*value.currentV2, selection)
}

func generatedDomainCompletedRebindHistoryMatchesCurrent(database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot,
) bool {
	if !protected.hasRebind || len(protected.rebindArtifacts) == 0 {
		return true
	}
	if database.Gateway.Rebind.Active != nil || !gatewayRebindScannedHistoryTerminalComplete(protected.rebind) {
		return false
	}
	candidates := generatedDomainCommittedAndCurrentGatewayCandidates(database, protected, true)
	candidates, valid := generatedDomainCollapseCurrentV2Sources(candidates)
	if !valid || len(candidates) != 1 {
		return false
	}
	var lineage appaccess.GatewayCurrentLineageRef
	switch candidates[0].kind {
	case generatedDomainGatewayNativeV2:
		if candidates[0].upgrade == nil {
			return false
		}
		var err error
		lineage, err = gatewayUpgradeCurrentLineage(*candidates[0].upgrade)
		if err != nil {
			return false
		}
	case generatedDomainGatewayCurrentV1:
		lineage = candidates[0].currentV1.Lineage
	case generatedDomainGatewayCurrentV2:
		lineage = candidates[0].currentV2.Lineage
	default:
		return false
	}
	if lineage.Kind == appaccess.GatewayRebindSourceGatewayRebind {
		return gatewayCurrentAuthorityHasDatabaseCommit(database.Gateway.Rebind.History,
			database.Gateway.Rebind.CurrentDatabaseCommittedEvent, gatewayCurrentAuthority(lineage))
	}
	upgradeCurrent := candidates[0].kind == generatedDomainGatewayNativeV2 ||
		generatedDomainCurrentV2UpgradeExpectationValid(candidates[0])
	return lineage.Kind == appaccess.GatewayRebindSourceGatewayUpgrade && upgradeCurrent &&
		generatedDomainCompletedNativeAbortHistoryMatchesCurrent(database.Gateway.Rebind.History,
			protected.rebind, lineage)
}

func generatedDomainCompletedNativeAbortHistoryMatchesCurrent(database []appaccess.GatewayRebindHistoryEntry,
	protected gatewayRebindProtectedIntentHistory, lineage appaccess.GatewayCurrentLineageRef,
) bool {
	if lineage.Kind != appaccess.GatewayRebindSourceGatewayUpgrade || len(database) == 0 ||
		len(database) != len(protected.Terminals)+len(protected.TerminalsV2) {
		return false
	}
	entries := make(map[string]appaccess.GatewayRebindHistoryEntry, len(database))
	for _, entry := range database {
		operationID, ok := generatedDomainRebindHistoryOperationID(entry)
		if !ok || operationID == "" {
			return false
		}
		if _, duplicate := entries[operationID]; duplicate {
			return false
		}
		entries[operationID] = entry
	}
	matched := make(map[string]struct{}, len(entries))
	for _, terminal := range protected.Terminals {
		receipt := terminal.Receipt
		entry, ok := entries[receipt.OperationID]
		if !ok || !generatedDomainLegacyAbortTerminalMatchesHistory(receipt, entry, lineage) {
			return false
		}
		matched[receipt.OperationID] = struct{}{}
	}
	for _, terminal := range protected.TerminalsV2 {
		receipt := terminal.Receipt
		entry, ok := entries[receipt.OperationID]
		if !ok || receipt.Disposition != appaccess.GatewayRebindDispositionAbort ||
			receipt.Predecessor.Lineage != lineage || !gatewayRebindTerminalMatchesHistoryEntry(receipt, entry) ||
			!generatedDomainHistoryEntryHasAbortTerminal(entry, receipt.Digest) {
			return false
		}
		matched[receipt.OperationID] = struct{}{}
	}
	return len(matched) == len(entries)
}

func generatedDomainRebindHistoryOperationID(entry appaccess.GatewayRebindHistoryEntry) (string, bool) {
	switch {
	case entry.Claim.SpecVersion == 1 && entry.Claim.Legacy != nil && entry.Claim.V2 == nil:
		return entry.Claim.Legacy.Spec.OperationID, true
	case entry.Claim.SpecVersion == appaccess.GatewayRebindSpecVersionV2 && entry.Claim.Legacy == nil && entry.Claim.V2 != nil:
		return entry.Claim.V2.Spec.OperationID, true
	default:
		return "", false
	}
}

func generatedDomainHistoryEntryHasAbortTerminal(entry appaccess.GatewayRebindHistoryEntry,
	receiptDigest string,
) bool {
	if len(entry.Commands) == 0 || !validSHA256(receiptDigest) {
		return false
	}
	last := entry.Commands[len(entry.Commands)-1]
	operationID, ok := generatedDomainRebindHistoryOperationID(entry)
	return ok && last.OperationID == operationID && last.NextState == appaccess.GatewayRebindRolledBack &&
		last.TerminalDisposition == appaccess.GatewayRebindDispositionAbort &&
		last.TerminalReceiptDigest == receiptDigest
}

func generatedDomainLegacyAbortTerminalMatchesHistory(receipt gatewayRebindFinalHandoverTerminalReceipt,
	entry appaccess.GatewayRebindHistoryEntry, lineage appaccess.GatewayCurrentLineageRef,
) bool {
	if receipt.Disposition != gatewayRebindFinalHandoverTerminalAbort ||
		!gatewayRebindLegacyPredecessorMatchesNativeLineage(receipt.Predecessor, lineage) ||
		entry.Claim.SpecVersion != 1 || entry.Claim.Legacy == nil || entry.Claim.V2 != nil ||
		!generatedDomainHistoryEntryHasAbortTerminal(entry, receipt.Digest) {
		return false
	}
	claim := entry.Claim.Legacy
	if receipt.OperationID != claim.Spec.OperationID || receipt.ClaimRequestDigest != claim.RequestDigest ||
		receipt.SuccessorProfile.RevisionID != claim.Spec.SuccessorProfileRevisionID ||
		receipt.SuccessorProfile.RevisionNumber != claim.Spec.SuccessorProfileRevisionNumber ||
		receipt.SuccessorProfile.OperationID != claim.Spec.SuccessorProfileOperationID ||
		receipt.SuccessorProfile.SpecDigest != claim.ConfigureApproval.SpecDigest ||
		receipt.RosterDigest != claim.Spec.RosterDigest || receipt.RosterCount != claim.Spec.RosterCount ||
		len(receipt.RosterEntryDigests) != len(entry.RosterV1) {
		return false
	}
	for index := range entry.RosterV1 {
		if receipt.RosterEntryDigests[index] != entry.RosterV1[index].EntryDigest {
			return false
		}
	}
	return true
}

var errGeneratedDomainStartupGatewayInvalid = errors.New("generated domain startup gateway evidence is invalid")

func resolveGeneratedDomainGatewayExpectation(database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot,
) (generatedDomainGatewayExpectation, error) {
	var candidates []generatedDomainGatewayExpectation
	if generatedDomainStartupDatabaseEffectEmpty(database) && protected.nativeV1 != nil && protected.nativeV1.Pending == nil && len(protected.upgrade.generations) == 0 &&
		len(protected.currentV1) == 0 && len(protected.currentV2.files) == 0 {
		state := cloneRouteState(*protected.nativeV1)
		digest, err := canonicalDigest(state)
		if err != nil {
			return generatedDomainGatewayExpectation{}, errGeneratedDomainStartupGatewayInvalid
		}
		candidates = append(candidates, generatedDomainGatewayExpectation{kind: generatedDomainGatewayNativeV1,
			stateDigest: digest, nativeV1: &state})
	}
	candidates = append(candidates,
		generatedDomainCommittedAndCurrentGatewayCandidates(database, protected, true)...)
	var valid bool
	candidates, valid = generatedDomainCollapseCurrentV2Sources(candidates)
	if !valid {
		return generatedDomainGatewayExpectation{}, errGeneratedDomainStartupGatewayInvalid
	}
	if len(candidates) == 0 {
		return generatedDomainGatewayExpectation{}, nil
	}
	if len(candidates) != 1 {
		return generatedDomainGatewayExpectation{}, errGeneratedDomainStartupGatewayInvalid
	}
	value := candidates[0]
	value.names = generatedDomainGatewayExpectedNames(value)
	ids, err := generatedDomainGatewayExpectedIDs(value)
	if err != nil {
		return generatedDomainGatewayExpectation{}, errGeneratedDomainStartupGatewayInvalid
	}
	value.ids = ids
	return value, nil
}

func generatedDomainGatewayExpectedNames(value generatedDomainGatewayExpectation) map[string]map[string]struct{} {
	result := map[string]map[string]struct{}{
		"container": {}, "network": {}, "volume": {}, "image": {},
	}
	add := func(kind, name string) {
		if name != "" {
			result[kind][name] = struct{}{}
		}
	}
	switch value.kind {
	case generatedDomainGatewayNativeV1:
		add("container", caddyContainerName)
		add("network", caddyNetworkName)
		add("volume", caddyVolumeName)
	case generatedDomainGatewayNativeV2:
		identity := value.nativeV2.Identity
		add("container", identity.FinalContainer)
		add("network", identity.IngressNetwork)
		add("volume", identity.ConfigVolume)
		add("volume", identity.DataVolume)
	case generatedDomainGatewayCurrentV1:
		identity := value.currentV1.Identity
		if identity.Rebind != nil {
			add("container", identity.Rebind.FinalContainer)
			add("network", identity.Rebind.IngressNetwork)
			add("volume", identity.Rebind.ConfigVolume)
			add("volume", identity.Rebind.DataVolume)
		} else if identity.Upgrade != nil {
			add("container", identity.Upgrade.FinalContainer)
			add("network", identity.Upgrade.IngressNetwork)
			add("volume", identity.Upgrade.ConfigVolume)
			add("volume", identity.Upgrade.DataVolume)
		}
	case generatedDomainGatewayCurrentV2:
		identity := value.currentV2.Identity
		if identity.Rebind != nil {
			add("container", identity.Rebind.FinalContainer)
			add("network", identity.Rebind.IngressNetwork)
			add("volume", identity.Rebind.ConfigVolume)
			add("volume", identity.Rebind.DataVolume)
		} else if identity.Upgrade != nil {
			add("container", identity.Upgrade.FinalContainer)
			add("network", identity.Upgrade.IngressNetwork)
			add("volume", identity.Upgrade.ConfigVolume)
			add("volume", identity.Upgrade.DataVolume)
		}
	}
	return result
}

func generatedDomainGatewayExpectedIDs(value generatedDomainGatewayExpectation) (map[string]map[string]struct{}, error) {
	result := map[string]map[string]struct{}{"container": {}, "network": {}, "image": {}}
	add := func(kind, id string) bool {
		if id != "" {
			normalized := normalizeID(id)
			if !generatedDomainCanonicalDockerID(normalized, false) {
				return false
			}
			result[kind][normalized] = struct{}{}
		}
		return true
	}
	switch value.kind {
	case generatedDomainGatewayNativeV2:
		if !add("container", value.journal.Resources.FinalContainerID) ||
			!add("network", value.journal.Resources.IngressNetworkID) || !add("image", value.journal.Resources.ImageID) {
			return nil, errGeneratedDomainStartupGatewayInvalid
		}
	case generatedDomainGatewayCurrentV1, generatedDomainGatewayCurrentV2:
		if value.kind == generatedDomainGatewayCurrentV2 && value.currentV2 != nil &&
			value.currentV2.Lineage.Kind == appaccess.GatewayRebindSourceGatewayUpgrade {
			if value.terminal != nil || value.journal == nil || value.nativeV2 == nil || value.nativeV1 == nil ||
				value.upgrade == nil || !add("container", value.journal.Resources.FinalContainerID) ||
				!add("network", value.journal.Resources.IngressNetworkID) ||
				!add("image", value.journal.Resources.ImageID) {
				return nil, errGeneratedDomainStartupGatewayInvalid
			}
			break
		}
		if value.terminal == nil {
			return nil, errGeneratedDomainStartupGatewayInvalid
		}
		resources := value.terminal.Resources
		if resources.FinalContainer != nil {
			if !add("container", resources.FinalContainer.ID) {
				return nil, errGeneratedDomainStartupGatewayInvalid
			}
		}
		if !add("network", resources.IngressNetwork.ID) || !add("image", resources.ImageID) {
			return nil, errGeneratedDomainStartupGatewayInvalid
		}
	}
	return result, nil
}
