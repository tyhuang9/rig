package generatedingress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

func cloneGatewayRebindTypedProgressForTest(t *testing.T, value gatewayRebindProgressRecord) gatewayRebindProgressRecord {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result gatewayRebindProgressRecord
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func rehashGatewayRebindTypedProgressForTest(t *testing.T, value *gatewayRebindProgressRecord) {
	t.Helper()
	var err error
	value.TypedEffect.Digest = ""
	value.TypedEffect.Digest, err = gatewayRebindTypedEffectDigest(*value.TypedEffect)
	if err != nil {
		t.Fatal(err)
	}
	value.Digest = ""
	value.Digest, err = gatewayRebindProgressDigest(*value)
	if err != nil {
		t.Fatal(err)
	}
}

func installGatewayRebindTypedAttemptForTerminal(t *testing.T, fixture gatewayCurrentStateFixture,
	checkpoint gatewayRebindPredecessorCheckpoint, intent gatewayRebindProtectedIntentV2,
	resources gatewayRebindFinalHandoverResourceBindings, proof gatewayRebindFinalHandoverTerminalProof,
) gatewayRebindProgressRecord {
	t.Helper()
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(fixture.dataRoot, checkpoint.Generation, checkpoint.OperationID)
	if err != nil || checkpointStore.installExact(checkpoint) != nil {
		t.Fatalf("install typed checkpoint: %v", err)
	}
	intentStore, err := newGatewayRebindProtectedIntentV2Store(fixture.dataRoot, intent.Generation, intent.OperationID)
	if err != nil || intentStore.installExact(intent) != nil {
		t.Fatalf("install typed intent: %v", err)
	}
	progress, err := newGatewayRebindSuccessorIntentProgressV2(intent, time.Unix(4, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	progressStore, err := newGatewayRebindProgressStore(fixture.dataRoot, progress.Generation, progress.OperationID, progress.Sequence)
	if err != nil || progressStore.installExact(context.Background(), progress) != nil {
		t.Fatalf("install typed progress: %v", err)
	}
	records, finalResources, finalProof := gatewayRebindTypedCompleteProgressFixture(t, fixture, checkpoint, intent, progress)
	if !reflect.DeepEqual(finalResources, resources) {
		t.Fatal("typed resource fixture disagrees with requested resources")
	}
	_ = proof
	_ = finalProof
	for _, record := range records[1:] {
		store, storeErr := newGatewayRebindProgressStore(fixture.dataRoot, record.Generation, record.OperationID, record.Sequence)
		if storeErr != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install typed progress %d: %v", record.Sequence, storeErr)
		}
	}
	return records[len(records)-1]
}

func gatewayRebindTypedCompleteProgressFixture(t *testing.T, fixture gatewayCurrentStateFixture,
	checkpoint gatewayRebindPredecessorCheckpoint, intent gatewayRebindProtectedIntentV2,
	first gatewayRebindProgressRecord,
) ([]gatewayRebindProgressRecord, gatewayRebindFinalHandoverResourceBindings,
	gatewayRebindFinalHandoverTerminalProof,
) {
	t.Helper()
	resources := fixture.receipt.Resources
	var err error
	resources.IngressNetwork, err = gatewayRebindTypedStageNetworkBindingFor(intent, resources.IngressNetwork.ID)
	if err != nil {
		t.Fatal(err)
	}
	resources.ConfigVolume, err = gatewayRebindTypedStageConfigVolumeBindingFor(intent,
		resources.ConfigVolume.Mountpoint, resources.ConfigVolume.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	resources.DataVolume, err = gatewayRebindTypedStageDataVolumeBindingFor(intent,
		resources.DataVolume.Mountpoint, resources.DataVolume.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	stagePlanDigest, err := gatewayRebindTypedStagePlanDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	effect := gatewayRebindTypedEffectProgress{ImageID: resources.ImageID, StagePlanDigest: stagePlanDigest}
	heads := gatewayRebindTypedProgressFixtureHeads(t, checkpoint, intent)
	terminalProof := fixture.receipt.PhysicalProof
	records := []gatewayRebindProgressRecord{first}
	firstAt, err := parseGatewayRebindProgressTime(first.OccurredAt)
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(2); sequence <= 17; sequence++ {
		switch sequence {
		case 3:
			network := resources.IngressNetwork
			effect.Network = &network
		case 4:
			volume := resources.ConfigVolume
			effect.ConfigVolume = &volume
		case 5:
			volume := resources.DataVolume
			effect.DataVolume = &volume
		case 6:
			container, buildErr := gatewayRebindTypedStageContainerBindingFor(intent, effect,
				resources.StageContainer.ID)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			resources.StageContainer = container
			effect.StageContainer = &container
		case 7:
			value, buildErr := gatewayRebindTypedStageConfigIntentFor(intent, records[len(records)-1], effect)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			effect.StageConfigIntent = &value
		case 8:
			value, buildErr := gatewayRebindTypedStageConfigCopyFor(intent, records[len(records)-1], effect)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			effect.StageConfigCopy = &value
		case 9:
			value, buildErr := gatewayRebindTypedStageStartIntentFor(intent, records[len(records)-1], effect)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			effect.StageStartIntent = &value
		case 10:
			value, buildErr := gatewayRebindTypedStageServingFor(intent, records[len(records)-1], effect,
				resources.StageContainer.ID)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			effect.StageServing = &value
		case 11:
			value, buildErr := gatewayRebindTypedFinalConfigIntentFor(intent, checkpoint,
				records[len(records)-1], effect, heads)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			effect.FinalConfigIntent = &value
		case 12:
			value, buildErr := gatewayRebindTypedFinalConfigCopyFor(intent, records[len(records)-1], effect)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			effect.FinalConfigCopy = &value
		case 13:
			resources.ApplicationNetworks = gatewayRebindTypedFixtureApplicationNetworks(t,
				effect.FinalConfigIntent.RoutePlan, resources.ApplicationNetworks)
			localHostPort := fixture.history.Progress[12].Record.Handover.Plan.LocalHostPort
			value, buildErr := gatewayRebindTypedHandoverIntentFor(intent, records[len(records)-1], effect,
				localHostPort, terminalProof.Observation.PredecessorObservationDigest, true,
				resources.ApplicationNetworks)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			effect.HandoverIntent = &value
			effect.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), resources.ApplicationNetworks...)
		case 14:
			container, buildErr := gatewayRebindTypedFinalContainerBindingFor(intent, effect,
				effect.HandoverIntent.Plan, resources.FinalContainer.ID)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			resources.FinalContainer = &container
			effect.FinalContainer = &container
		case 15:
			value, buildErr := gatewayRebindTypedCutoverIntentFor(intent, records[len(records)-1], effect,
				terminalProof.Observation.Digest)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			effect.CutoverIntent = &value
		case 16:
			observation := terminalProof.Observation
			observation.ConfigDigest = effect.HandoverIntent.Plan.FinalConfigDigest
			observation.RoutesDigest = effect.HandoverIntent.Plan.RoutePlanDigest
			observation.FinalID = effect.FinalContainer.ID
			observation.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil),
				effect.HandoverIntent.Plan.ApplicationNetworks...)
			observation.Digest = ""
			observation.Digest, err = gatewayRebindFinalHandoverObservationDigest(observation)
			if err != nil {
				t.Fatal(err)
			}
			terminalProof.Observation = observation
			value, buildErr := gatewayRebindTypedSuccessorServingFor(records[len(records)-1], effect, observation)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			effect.SuccessorServing = &value
		case 17:
			resources.Digest = ""
			resources.Digest, err = gatewayRebindFinalHandoverResourcesDigest(resources)
			if err != nil {
				t.Fatal(err)
			}
			proof := terminalProof
			proof.PriorProgressDigest = records[len(records)-1].Digest
			proof.Observation.FinalID = resources.FinalContainer.ID
			proof.Observation.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), resources.ApplicationNetworks...)
			proof.Digest = ""
			proof.Digest, err = gatewayRebindFinalHandoverOutcomeDigest(proof)
			if err != nil {
				t.Fatal(err)
			}
			resourcesCopy, proofCopy := resources, proof
			effect.Resources, effect.PhysicalProof = &resourcesCopy, &proofCopy
		}
		record, recordErr := newGatewayRebindTypedEffectProgressV2(intent, checkpoint, records, effect,
			firstAt.Add(time.Duration(sequence-1)*time.Nanosecond))
		if recordErr != nil {
			if sequence == 17 {
				candidate := effect
				candidate.Version, candidate.Purpose, candidate.Digest = gatewayRebindTypedEffectVersion,
					gatewayRebindTypedEffectPurpose, ""
				candidate.Digest, _ = gatewayRebindTypedEffectDigest(candidate)
				t.Fatalf("build typed progress %d: %v shape=%t semantic=%t physical=%t resources=%t proof=%t appnets=%#v proofnets=%#v", sequence, recordErr,
					validGatewayRebindTypedEffectProgress(candidate, sequence),
					gatewayRebindTypedEffectSemanticMatch(candidate, intent, records[len(records)-1]),
					gatewayRebindTypedPhysicalOutcomeMatches(intent.Identity, records[len(records)-1].Digest,
						*candidate.Resources, *candidate.PhysicalProof),
					validGatewayRebindFinalHandoverResourceBindingsValue(*candidate.Resources),
					validGatewayRebindFinalHandoverOutcomeValue(*candidate.PhysicalProof),
					candidate.Resources.ApplicationNetworks, candidate.PhysicalProof.Observation.ApplicationNetworks)
			}
			t.Fatalf("build typed progress %d: %v", sequence, recordErr)
		}
		records = append(records, record)
		effect = *record.TypedEffect
	}
	return records, resources, *records[len(records)-1].TypedEffect.PhysicalProof
}

func gatewayRebindTypedFixtureApplicationNetworks(t *testing.T, plan gatewayRebindTypedFinalConfigRoutePlan,
	available []gatewayRebindHandoverApplicationNetwork,
) []gatewayRebindHandoverApplicationNetwork {
	t.Helper()
	routes := make(map[string]routeRecord, len(plan.Routes))
	for _, binding := range plan.Routes {
		routes[binding.AppID] = binding.Route
	}
	owners, valid := gatewayRouteNetworkOwners(routes)
	if !valid {
		t.Fatal("typed fixture route network ownership is invalid")
	}
	byName := make(map[string]string, len(available))
	for _, network := range available {
		byName[network.Name] = network.ID
	}
	names := make([]string, 0, len(owners))
	for name := range owners {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil
	}
	result := make([]gatewayRebindHandoverApplicationNetwork, 0, len(names))
	for _, name := range names {
		id, exists := byName[name]
		if !exists {
			t.Fatalf("typed fixture lacks application network %s", name)
		}
		result = append(result, gatewayRebindHandoverApplicationNetwork{Name: name, ID: id})
	}
	return result
}

func gatewayRebindTypedProgressFixtureHeads(t *testing.T, checkpoint gatewayRebindPredecessorCheckpoint,
	intent gatewayRebindProtectedIntentV2,
) []appaccess.GatewayRebindRuntimeHead {
	t.Helper()
	return append([]appaccess.GatewayRebindRuntimeHead(nil), intent.RuntimeHeads...)
}

func gatewayRebindTypedTerminalPhysicalFixture(t *testing.T, fixture gatewayCurrentStateFixture,
	intent gatewayRebindProtectedIntentV2, previous gatewayRebindProgressRecord,
) (gatewayRebindFinalHandoverResourceBindings, gatewayRebindFinalHandoverTerminalProof) {
	t.Helper()
	resources := fixture.receipt.Resources
	resources.ConfigVolume.Name = intent.Identity.ConfigVolume
	resources.DataVolume.Name = intent.Identity.DataVolume
	resources.Digest = ""
	var err error
	resources.Digest, err = gatewayRebindFinalHandoverResourcesDigest(resources)
	if err != nil {
		t.Fatal(err)
	}
	proof := fixture.receipt.PhysicalProof
	proof.PriorProgressDigest = previous.Digest
	proof.Observation.FinalID = resources.FinalContainer.ID
	proof.Observation.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), resources.ApplicationNetworks...)
	proof.Digest = ""
	proof.Digest, err = gatewayRebindFinalHandoverOutcomeDigest(proof)
	if err != nil || !gatewayRebindTypedPhysicalOutcomeMatches(intent.Identity, previous.Digest, resources, proof) {
		t.Fatalf("typed physical fixture: %v", err)
	}
	return resources, proof
}

func gatewayRebindTypedRollbackProofFixture(t *testing.T, fixture gatewayCurrentStateFixture,
	predecessorRoutesDigest, priorProgressDigest string,
) gatewayRebindFinalHandoverTerminalProof {
	t.Helper()
	proof := fixture.receipt.PhysicalProof
	proof.Kind = gatewayRebindFinalHandoverOutcomeAbort
	proof.PriorProgressDigest = priorProgressDigest
	proof.Observation.Stage = gatewayRebindHandoverContainerAbsent
	proof.Observation.Final = gatewayRebindHandoverContainerAbsent
	proof.Observation.FinalID = ""
	proof.Observation.PredecessorRunning = true
	proof.Observation.PredecessorAddress = gatewayRebindPredecessorAddressPresent
	proof.Observation.ConfigVolumePresent = false
	proof.Observation.DataVolumePresent = false
	proof.Observation.IngressNetworkPresent = false
	proof.Observation.ApplicationNetworks = nil
	proof.Observation.ConfigDigest = ""
	proof.Observation.RoutesDigest = ""
	proof.Observation.PredecessorRoutesDigest = predecessorRoutesDigest
	proof.Observation.PredecessorStopDigest = ""
	proof.Observation.Digest = ""
	var err error
	proof.Observation.Digest, err = gatewayRebindFinalHandoverObservationDigest(proof.Observation)
	if err != nil {
		t.Fatal(err)
	}
	proof.Digest = ""
	proof.Digest, err = gatewayRebindFinalHandoverOutcomeDigest(proof)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func TestGatewayRebindTypedProgressReplayRejectsRehashedSemanticMismatches(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	checkpoint, intent := gatewayRebindAttemptTypedFixture(t, fixture)
	first, err := newGatewayRebindSuccessorIntentProgressV2(intent, time.Unix(4, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	records, _, _ := gatewayRebindTypedCompleteProgressFixture(t, fixture, checkpoint, intent, first)

	check := func(t *testing.T, sequence uint64, mutate func(*gatewayRebindProgressRecord)) {
		t.Helper()
		changed := cloneGatewayRebindTypedProgressForTest(t, records[sequence-1])
		mutate(&changed)
		rehashGatewayRebindTypedProgressForTest(t, &changed)
		previous := make([]gatewayRebindProgressSelection, sequence-1)
		for index := range previous {
			previous[index] = gatewayRebindProgressSelection{Generation: records[index].Generation,
				Sequence: records[index].Sequence, Record: records[index], Existing: true}
		}
		if gatewayRebindProgressMatchesIntentV2(changed, intent, previous) {
			t.Fatal("typed replay matcher accepted a rehashed semantic mismatch")
		}
	}

	t.Run("cross-generation predecessor", func(t *testing.T) {
		check(t, 11, func(record *gatewayRebindProgressRecord) {
			binding := record.TypedEffect.FinalConfigIntent
			binding.RoutePlan.Predecessor.Lineage.ProtectedGeneration++
			binding.RoutePlan.Digest, _ = gatewayRebindTypedFinalConfigRoutePlanDigest(binding.RoutePlan)
			binding.Digest, _ = gatewayRebindTypedFinalConfigIntentDigest(*binding)
		})
	})
	t.Run("network ownership", func(t *testing.T) {
		check(t, 3, func(record *gatewayRebindProgressRecord) {
			record.TypedEffect.Network.OwnershipDigest = strings.Repeat("0", 64)
			if record.TypedEffect.Network.OwnershipDigest == records[2].TypedEffect.Network.OwnershipDigest {
				record.TypedEffect.Network.OwnershipDigest = strings.Repeat("f", 64)
			}
		})
	})
	t.Run("stage create command", func(t *testing.T) {
		check(t, 6, func(record *gatewayRebindProgressRecord) {
			record.TypedEffect.StageContainer.ConfigurationDigest = strings.Repeat("0", 64)
			if record.TypedEffect.StageContainer.ConfigurationDigest == records[5].TypedEffect.StageContainer.ConfigurationDigest {
				record.TypedEffect.StageContainer.ConfigurationDigest = strings.Repeat("f", 64)
			}
		})
	})
	t.Run("stage copy target", func(t *testing.T) {
		check(t, 7, func(record *gatewayRebindProgressRecord) {
			record.TypedEffect.StageConfigIntent.Destination = "/config/other.json"
		})
	})
	t.Run("probe token", func(t *testing.T) {
		check(t, 9, func(record *gatewayRebindProgressRecord) {
			binding := record.TypedEffect.StageStartIntent
			binding.ProbeToken = strings.Repeat("f", 64)
			if binding.ProbeToken == records[8].TypedEffect.StageStartIntent.ProbeToken {
				binding.ProbeToken = strings.Repeat("e", 64)
			}
			binding.StartEffectDigest, _ = gatewayRebindStageStartEffectDigest(*binding)
		})
	})
	t.Run("route plan", func(t *testing.T) {
		check(t, 11, func(record *gatewayRebindProgressRecord) {
			binding := record.TypedEffect.FinalConfigIntent
			binding.RoutePlan.Routes[0].Route.Endpoints[0].InternalPort++
			route := &binding.RoutePlan.Routes[0]
			route.SourceBindingDigest, _ = gatewayRebindTypedFinalConfigSourceBindingDigest(*route)
			binding.RoutePlan.RouteMapDigest, _ = gatewayRebindTypedFinalConfigRouteMapDigest(binding.RoutePlan.Routes)
			binding.RoutePlan.Digest, _ = gatewayRebindTypedFinalConfigRoutePlanDigest(binding.RoutePlan)
			binding.Digest, _ = gatewayRebindTypedFinalConfigIntentDigest(*binding)
		})
	})
	t.Run("final create command", func(t *testing.T) {
		check(t, 13, func(record *gatewayRebindProgressRecord) {
			binding := record.TypedEffect.HandoverIntent
			binding.Plan.FinalConfigurationDigest = strings.Repeat("0", 64)
			if binding.Plan.FinalConfigurationDigest == records[12].TypedEffect.HandoverIntent.Plan.FinalConfigurationDigest {
				binding.Plan.FinalConfigurationDigest = strings.Repeat("f", 64)
			}
			binding.Plan.Digest, _ = gatewayRebindFinalHandoverPlanDigest(binding.Plan)
			binding.Digest, _ = gatewayRebindTypedHandoverIntentDigest(*binding)
		})
	})
	t.Run("application network census", func(t *testing.T) {
		if len(records[12].TypedEffect.ApplicationNetworks) == 0 {
			t.Fatal("typed fixture has no application network census")
		}
		check(t, 13, func(record *gatewayRebindProgressRecord) {
			networks := append([]gatewayRebindHandoverApplicationNetwork(nil),
				record.TypedEffect.ApplicationNetworks[:len(record.TypedEffect.ApplicationNetworks)-1]...)
			record.TypedEffect.ApplicationNetworks = networks
			binding := record.TypedEffect.HandoverIntent
			binding.Plan.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), networks...)
			binding.Plan.Digest, _ = gatewayRebindFinalHandoverPlanDigest(binding.Plan)
			binding.Digest, _ = gatewayRebindTypedHandoverIntentDigest(*binding)
		})
	})
	t.Run("self consistent route plan still disagrees with checkpoint", func(t *testing.T) {
		changed := cloneGatewayRebindTypedProgressForTest(t, records[10])
		binding := changed.TypedEffect.FinalConfigIntent
		binding.RoutePlan.Routes[0].Route.Endpoints[0].InternalPort++
		route := &binding.RoutePlan.Routes[0]
		var err error
		route.SourceBindingDigest, err = gatewayRebindTypedFinalConfigSourceBindingDigest(*route)
		if err != nil {
			t.Fatal(err)
		}
		binding.RoutePlan.RouteMapDigest, err = gatewayRebindTypedFinalConfigRouteMapDigest(binding.RoutePlan.Routes)
		if err != nil {
			t.Fatal(err)
		}
		binding.RoutePlan.Digest, err = gatewayRebindTypedFinalConfigRoutePlanDigest(binding.RoutePlan)
		if err != nil {
			t.Fatal(err)
		}
		body, err := gatewayRebindTypedFinalConfigBytesForPlan(intent, binding.RoutePlan)
		if err != nil {
			t.Fatal(err)
		}
		defer clear(body)
		digest := sha256.Sum256(body)
		binding.ContentDigest = hex.EncodeToString(digest[:])
		binding.ContentLength = int64(len(body))
		binding.Digest, err = gatewayRebindTypedFinalConfigIntentDigest(*binding)
		if err != nil {
			t.Fatal(err)
		}
		rehashGatewayRebindTypedProgressForTest(t, &changed)
		previous := make([]gatewayRebindProgressSelection, 10)
		for index := range previous {
			previous[index] = gatewayRebindProgressSelection{Generation: records[index].Generation,
				Sequence: records[index].Sequence, Record: records[index], Existing: true}
		}
		if !gatewayRebindProgressMatchesIntentV2(changed, intent, previous) {
			t.Fatal("self-consistent rehashed route plan did not reach the frozen-checkpoint validation boundary")
		}
		prefix := append([]gatewayRebindProgressRecord(nil), records[:10]...)
		prefix = append(prefix, changed)
		if gatewayRebindTypedProgressMatchesCheckpoint(intent, checkpoint, prefix) {
			t.Fatal("frozen checkpoint accepted a self-consistent rehashed route plan")
		}
	})
}

func TestGatewayRebindTypedProgressAcceptsCanonicalZeroRuntimeHeads(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	state := fixture.baseline
	state.Apps = map[string]gatewayCurrentAppRoute{}
	state.Pending = nil
	state.LANRecovery = nil
	var err error
	state.TransferManifestDigest, err = appaccess.GatewayRebindTransferManifestDigest(nil)
	if err != nil {
		t.Fatal(err)
	}
	state.Digest = ""
	state.Digest, err = gatewayCurrentRouteStateDigest(state)
	if err != nil || !validGatewayCurrentRouteState(state) {
		t.Fatalf("zero-head current state: %v", err)
	}
	operationID := "11111111-1111-4111-8111-111111111111"
	checkpoint, err := newGatewayRebindPredecessorCheckpoint(state.Lineage.ProtectedGeneration+1,
		operationID, state.Lineage, nil, &state)
	if err != nil {
		t.Fatal(err)
	}
	profileSpec := appaccess.GatewayProfileSpec{SelectedIPv4: state.Profile.SelectedIPv4,
		InterfaceID: state.Profile.InterfaceID, PortStart: state.Profile.PortStart, PortEnd: state.Profile.PortEnd}
	intent := gatewayRebindAttemptTypedIntentForCheckpoint(t, checkpoint, nil,
		"22222222-2222-4222-8222-222222222222", state.Profile.RevisionNumber+1,
		"33333333-3333-4333-8333-333333333333", profileSpec, fixture.intent.NetworkObservation)
	if len(intent.RuntimeHeads) != 0 {
		t.Fatalf("fixture did not retain canonical empty runtime heads: %#v", intent.RuntimeHeads)
	}
	plan, err := gatewayRebindTypedFinalConfigRoutePlanFor(intent, checkpoint, []appaccess.GatewayRebindRuntimeHead{})
	if err != nil || len(plan.Routes) != 0 || plan.RuntimeHeadsDigest != intent.Claim.Spec.RuntimeHeadsDigest {
		t.Fatalf("zero-head route plan: %#v error=%v", plan, err)
	}
	body, err := gatewayRebindTypedFinalConfigBytes(intent, checkpoint, plan)
	if err != nil || len(body) == 0 {
		t.Fatalf("zero-head final config: bytes=%d error=%v", len(body), err)
	}
	clear(body)
	first, err := newGatewayRebindSuccessorIntentProgressV2(intent, time.Unix(4, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	records, resources, proof := gatewayRebindTypedCompleteProgressFixture(t, fixture, checkpoint, intent, first)
	if len(records) != 17 || records[10].TypedEffect == nil ||
		records[10].TypedEffect.FinalConfigIntent == nil ||
		len(records[10].TypedEffect.FinalConfigIntent.RoutePlan.Routes) != 0 ||
		!gatewayRebindTypedProgressMatchesCheckpoint(intent, checkpoint, records) {
		t.Fatal("zero-head typed progress was not retained as a complete canonical prefix")
	}
	selections := make([]gatewayRebindProgressSelection, len(records))
	for index := range records {
		selections[index] = gatewayRebindProgressSelection{Generation: records[index].Generation,
			Sequence: records[index].Sequence, Record: records[index], Existing: true}
	}
	commit, err := newGatewayRebindCommitTerminalV2(intent, records[len(records)-1], resources, proof,
		time.Unix(21, 0).UTC())
	if err != nil || !gatewayRebindTerminalV2MatchesIntentHistory(commit, intent, checkpoint, selections) ||
		commit.RosterEntryDigests == nil {
		t.Fatalf("zero-roster commit terminal: %#v error=%v", commit, err)
	}
	transfers, err := newGatewayRebindTransfersV2(intent, commit, checkpoint)
	if err != nil || transfers == nil || len(transfers) != 0 {
		t.Fatalf("zero-roster transfers: %#v error=%v", transfers, err)
	}
	baseline, err := newGatewayCurrentRouteBaselineFromV2Terminal(intent, commit, checkpoint, transfers)
	if err != nil || len(baseline.Apps) != 0 || !validGatewayCurrentRouteState(baseline) {
		t.Fatalf("zero-roster current baseline: %#v error=%v", baseline, err)
	}
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(fixture.dataRoot,
		checkpoint.Generation, checkpoint.OperationID)
	if err != nil || checkpointStore.installExact(checkpoint) != nil {
		t.Fatalf("install zero-census checkpoint: %v", err)
	}
	intentStore, err := newGatewayRebindProtectedIntentV2Store(fixture.dataRoot, intent.Generation, intent.OperationID)
	if err != nil || intentStore.installExact(intent) != nil {
		t.Fatalf("install zero-census intent: %v", err)
	}
	for _, record := range records {
		store, storeErr := newGatewayRebindProgressStore(fixture.dataRoot, record.Generation,
			record.OperationID, record.Sequence)
		if storeErr != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install zero-census progress %d: %v", record.Sequence, storeErr)
		}
	}
	terminalStore, err := newGatewayRebindTerminalStoreV2(fixture.dataRoot, commit.Generation, commit.OperationID)
	if err != nil || terminalStore.installExact(context.Background(), commit) != nil {
		t.Fatalf("install zero-census terminal: %v", err)
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.TerminalsV2) != 1 ||
		!reflect.DeepEqual(history.TerminalsV2[0].Receipt, commit) {
		t.Fatalf("scan zero-census terminal: terminals=%d error=%v", len(history.TerminalsV2), err)
	}
	currentStore, err := newGatewayCurrentRouteStateStore(fixture.dataRoot, baseline.Lineage)
	if err != nil || currentStore.installBaseline(baseline) != nil {
		t.Fatalf("install zero-census current baseline: %v", err)
	}
	loaded, err := currentStore.load()
	if err != nil || !reflect.DeepEqual(loaded, baseline) {
		t.Fatalf("read zero-census current baseline: %#v error=%v", loaded, err)
	}
	noEffectProof := gatewayRebindNoEffectAbortProof{
		Version: gatewayRebindNoEffectProofVersion, Purpose: gatewayRebindNoEffectProofPurpose,
		Generation: checkpoint.Generation, OperationID: checkpoint.OperationID,
		ClaimRequestDigest: intent.Claim.RequestDigest, PredecessorCheckpointDigest: checkpoint.Digest,
		SourceStateDigest: checkpoint.SourceStateDigest, SuccessorIdentityDigest: intent.Identity.Digest,
		ObservationDigest: strings.Repeat("a", 64), CreatedAt: time.Unix(4, 0).UTC().Format(time.RFC3339Nano),
	}
	noEffectProof.Digest, err = gatewayRebindNoEffectAbortProofDigest(noEffectProof)
	if err != nil {
		t.Fatal(err)
	}
	noEffect, err := newGatewayRebindNoEffectAbortTerminalV2(intent.Claim,
		[]appaccess.GatewayRebindRosterEntryV2{}, []appaccess.GatewayRebindRuntimeHead{}, checkpoint,
		noEffectProof, time.Unix(5, 0).UTC())
	if err != nil || noEffect.PreparedDatabaseDigest != intent.DatabaseDigest {
		t.Fatalf("canonical empty no-effect database digest=%s want=%s error=%v",
			noEffect.PreparedDatabaseDigest, intent.DatabaseDigest, err)
	}
	changedHeads := []appaccess.GatewayRebindRuntimeHead{{AppID: "loopback-only", DeploymentID: uuid.NewString(),
		ReleaseID: uuid.NewString(), Slot: "blue", Generation: 1, UpdatedAt: time.Unix(1, 0).UTC()}}
	if _, err := newGatewayRebindNoEffectAbortTerminalV2(intent.Claim, nil, changedHeads, checkpoint,
		noEffectProof, time.Unix(5, 0).UTC()); err == nil {
		t.Fatal("no-effect terminal accepted a runtime-head census outside the retained claim")
	}
	routesDigest, err := gatewayRebindTypedCheckpointRoutesDigest(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	rollbackIntent, err := newGatewayRebindTypedRollbackIntentV2(intent,
		[]gatewayRebindProgressRecord{first}, gatewayRebindTypedRollbackOwnedResources{}, routesDigest, "",
		time.Unix(5, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	rollbackProof := gatewayRebindTypedRollbackProofFixture(t, fixture, routesDigest, rollbackIntent.Digest)
	rollbackComplete, err := newGatewayRebindTypedRollbackCompleteV2(intent,
		[]gatewayRebindProgressRecord{first, rollbackIntent}, rollbackProof, time.Unix(6, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := newGatewayRebindRollbackTerminalV2(intent, rollbackComplete, checkpoint, time.Unix(7, 0).UTC())
	rollbackSelections := []gatewayRebindProgressSelection{
		{Generation: first.Generation, Sequence: first.Sequence, Record: first, Existing: true},
		{Generation: rollbackIntent.Generation, Sequence: rollbackIntent.Sequence, Record: rollbackIntent, Existing: true},
		{Generation: rollbackComplete.Generation, Sequence: rollbackComplete.Sequence, Record: rollbackComplete, Existing: true},
	}
	if err != nil || !gatewayRebindTerminalV2MatchesIntentHistory(rollback, intent, checkpoint, rollbackSelections) ||
		rollback.RosterEntryDigests == nil {
		t.Fatalf("zero-roster rollback terminal: %#v error=%v", rollback, err)
	}
}

func TestGatewayRebindTypedHandoverBindsRetainedPredecessorLocalPort(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	checkpoint, intent := gatewayRebindAttemptTypedFixture(t, fixture)
	first, err := newGatewayRebindSuccessorIntentProgressV2(intent, time.Unix(4, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	records, _, _ := gatewayRebindTypedCompleteProgressFixture(t, fixture, checkpoint, intent, first)
	selections := make([]gatewayRebindProgressSelection, 13)
	for index := range selections {
		selections[index] = gatewayRebindProgressSelection{Generation: records[index].Generation,
			Sequence: records[index].Sequence, Record: records[index], Existing: true}
	}
	if !gatewayRebindTypedProgressMatchesPredecessorLocalPort(fixture.history, intent, selections) {
		t.Fatal("exact retained predecessor local port was rejected")
	}
	prior := records[11]
	effect := *prior.TypedEffect
	port := records[12].TypedEffect.HandoverIntent.Plan.LocalHostPort + 1
	if port == 0 {
		port = records[12].TypedEffect.HandoverIntent.Plan.LocalHostPort - 1
	}
	binding, err := gatewayRebindTypedHandoverIntentFor(intent, prior, effect, port,
		records[12].TypedEffect.HandoverIntent.Plan.PredecessorObservationDigest,
		records[12].TypedEffect.HandoverIntent.Plan.PredecessorInitiallyRunning,
		records[12].TypedEffect.HandoverIntent.Plan.ApplicationNetworks)
	if err != nil {
		t.Fatal(err)
	}
	effect.HandoverIntent = &binding
	effect.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), binding.Plan.ApplicationNetworks...)
	changed, err := newGatewayRebindTypedEffectProgressV2(intent, checkpoint, records[:12], effect,
		time.Unix(16, 0).UTC())
	if err != nil {
		t.Fatalf("build self-consistent changed local-port progress: %v", err)
	}
	selections[12] = gatewayRebindProgressSelection{Generation: changed.Generation,
		Sequence: changed.Sequence, Record: changed, Existing: true}
	if gatewayRebindTypedProgressMatchesPredecessorLocalPort(fixture.history, intent, selections) {
		t.Fatal("self-consistent handover accepted a local port from outside retained predecessor history")
	}
}

func TestGatewayRebindTypedCommitTerminalScansAndSelectsActualLineage(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	checkpoint, intent := gatewayRebindAttemptTypedFixture(t, fixture)
	// The typed physical result binds every retained physical boundary and the
	// final proof to sequence sixteen rather than reusing any v1 journal proof.
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(fixture.dataRoot, checkpoint.Generation, checkpoint.OperationID)
	if err != nil || checkpointStore.installExact(checkpoint) != nil {
		t.Fatalf("install typed checkpoint: %v", err)
	}
	intentStore, err := newGatewayRebindProtectedIntentV2Store(fixture.dataRoot, intent.Generation, intent.OperationID)
	if err != nil || intentStore.installExact(intent) != nil {
		t.Fatalf("install typed intent: %v", err)
	}
	first, err := newGatewayRebindSuccessorIntentProgressV2(intent, time.Unix(4, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	firstStore, _ := newGatewayRebindProgressStore(fixture.dataRoot, first.Generation, first.OperationID, first.Sequence)
	if err := firstStore.installExact(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	records, resources, proof := gatewayRebindTypedCompleteProgressFixture(t, fixture, checkpoint, intent, first)
	for _, record := range records[1:] {
		progressStore, storeErr := newGatewayRebindProgressStore(fixture.dataRoot, record.Generation, record.OperationID, record.Sequence)
		if storeErr != nil || progressStore.installExact(context.Background(), record) != nil {
			t.Fatalf("install typed progress %d: %v", record.Sequence, storeErr)
		}
	}
	progress := records[len(records)-1]
	receipt, err := newGatewayRebindCommitTerminalV2(intent, progress, resources,
		proof, time.Unix(21, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindTerminalStoreV2(fixture.dataRoot, receipt.Generation, receipt.OperationID)
	if err != nil || store.installExact(context.Background(), receipt) != nil {
		t.Fatalf("install typed terminal: %v", err)
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Terminals) != 1 || len(history.TerminalsV2) != 1 ||
		!reflect.DeepEqual(history.TerminalsV2[0].Receipt, receipt) {
		t.Fatalf("scan typed terminal: v1=%d v2=%d error=%v", len(history.Terminals), len(history.TerminalsV2), err)
	}
	typedProgress := make([]gatewayRebindProgressSelection, 0, 17)
	for _, retained := range history.Progress {
		if retained.Generation == intent.Generation {
			typedProgress = append(typedProgress, retained)
		}
	}
	if len(typedProgress) != 17 || !gatewayRebindTerminalV2MatchesIntentHistory(receipt, intent, checkpoint, typedProgress) {
		t.Fatalf("complete typed progress prefix mismatch: records=%d", len(typedProgress))
	}
	changedResources := receipt
	resourcesCopy := *receipt.Resources
	resourcesCopy.ImageID = strings.Repeat("0", 64)
	if resourcesCopy.ImageID == receipt.Resources.ImageID {
		resourcesCopy.ImageID = strings.Repeat("f", 64)
	}
	resourcesCopy.Digest, err = gatewayRebindFinalHandoverResourcesDigest(resourcesCopy)
	if err != nil {
		t.Fatal(err)
	}
	changedResources.Resources = &resourcesCopy
	changedResources.Digest, err = gatewayRebindTerminalDigestV2(changedResources)
	if err != nil || !validGatewayRebindTerminalReceiptV2(changedResources) ||
		gatewayRebindTerminalV2MatchesIntentHistory(changedResources, intent, checkpoint, typedProgress) {
		t.Fatalf("rehashed alternate terminal resources were not isolated from retained progress: %v", err)
	}
	changedProof := receipt
	proofCopy := *receipt.PhysicalProof
	proofCopy.Observation.ConfigDigest = strings.Repeat("0", 64)
	if proofCopy.Observation.ConfigDigest == receipt.PhysicalProof.Observation.ConfigDigest {
		proofCopy.Observation.ConfigDigest = strings.Repeat("f", 64)
	}
	proofCopy.Observation.Digest, err = gatewayRebindFinalHandoverObservationDigest(proofCopy.Observation)
	if err != nil {
		t.Fatal(err)
	}
	proofCopy.Digest, err = gatewayRebindFinalHandoverOutcomeDigest(proofCopy)
	if err != nil {
		t.Fatal(err)
	}
	changedProof.PhysicalProof = &proofCopy
	changedProof.Digest, err = gatewayRebindTerminalDigestV2(changedProof)
	if err != nil || !validGatewayRebindTerminalReceiptV2(changedProof) ||
		gatewayRebindTerminalV2MatchesIntentHistory(changedProof, intent, checkpoint, typedProgress) {
		t.Fatalf("rehashed alternate terminal proof was not isolated from retained progress: %v", err)
	}
	if gatewayRebindProgressMatchesIntentV2(typedProgress[16].Record, intent, typedProgress[:15]) {
		t.Fatal("typed progress matcher accepted a missing sequence sixteen")
	}
	lineage, err := gatewayRebindCurrentLineageV2(receipt)
	if err != nil || lineage.OperationID != intent.OperationID || lineage.TerminalReceiptDigest != receipt.Digest ||
		lineage.ProtectedIntentDigest != intent.Digest || lineage.ProtectedJournalDigest != "" {
		t.Fatalf("typed lineage mismatch: %#v error=%v", lineage, err)
	}
	view, err := newGatewayRebindAttemptTerminalViewV2(receipt)
	if err != nil || !gatewayRebindAttemptTerminalMatchesLineage(view, lineage) || view.LegacyReceipt != nil || view.TypedReceipt == nil {
		t.Fatalf("typed terminal union mismatch: %#v error=%v", view, err)
	}
	changedView := view
	changedView.Resources = view.Resources
	changedFinal := *view.Resources.FinalContainer
	changedFinal.ID = strings.Repeat("a", 64)
	if changedFinal.ID == view.Resources.FinalContainer.ID {
		changedFinal.ID = strings.Repeat("b", 64)
	}
	changedView.Resources.FinalContainer = &changedFinal
	changedView.Resources.Digest, err = gatewayRebindFinalHandoverResourcesDigest(changedView.Resources)
	if err != nil || gatewayRebindAttemptTerminalMatchesLineage(changedView, lineage) {
		t.Fatalf("rehashed typed terminal summary accepted: %v", err)
	}
	mixedView := view
	mixedView.LegacyReceipt = &fixture.receipt
	if gatewayRebindAttemptTerminalMatchesLineage(mixedView, lineage) {
		t.Fatal("mixed typed and legacy terminal summary accepted")
	}
	selectedView, err := gatewayCurrentSelectionTerminalView(gatewayCurrentSelection{
		Kind: gatewayCurrentSelectionRebind, Lineage: lineage, Terminal: &view,
	})
	if err != nil || !reflect.DeepEqual(selectedView, view) {
		t.Fatalf("typed current terminal adapter mismatch: %#v error=%v", selectedView, err)
	}
	transfers, err := newGatewayRebindTransfersV2(intent, receipt, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := newGatewayCurrentRouteBaselineFromV2Terminal(intent, receipt, checkpoint, transfers)
	if err != nil || len(baseline.Apps) != len(checkpoint.CurrentState.Apps) || baseline.Lineage != lineage || baseline.Revision != 1 {
		t.Fatalf("typed baseline mismatch: apps=%d error=%v", len(baseline.Apps), err)
	}
	currentStore, err := newGatewayCurrentRouteStateStore(fixture.dataRoot, lineage)
	if err != nil || currentStore.installBaseline(baseline) != nil {
		t.Fatalf("install typed baseline: %v", err)
	}
	loaded, err := currentStore.load()
	if err != nil || !reflect.DeepEqual(loaded, baseline) {
		t.Fatalf("load typed baseline: %v", err)
	}
}

func TestGatewayRebindTypedNoEffectAbortRequiresCheckpointWithoutIntent(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	checkpoint, intent := gatewayRebindAttemptTypedFixture(t, fixture)
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(fixture.dataRoot, checkpoint.Generation, checkpoint.OperationID)
	if err != nil || checkpointStore.installExact(checkpoint) != nil {
		t.Fatalf("install typed checkpoint: %v", err)
	}
	proof := gatewayRebindNoEffectAbortProof{
		Version: gatewayRebindNoEffectProofVersion, Purpose: gatewayRebindNoEffectProofPurpose,
		Generation: checkpoint.Generation, OperationID: checkpoint.OperationID,
		ClaimRequestDigest: intent.Claim.RequestDigest, PredecessorCheckpointDigest: checkpoint.Digest,
		SourceStateDigest: checkpoint.SourceStateDigest, SuccessorIdentityDigest: intent.Identity.Digest,
		ObservationDigest: strings.Repeat("a", 64), CreatedAt: time.Unix(4, 0).UTC().Format(time.RFC3339Nano),
	}
	proof.Digest, err = gatewayRebindNoEffectAbortProofDigest(proof)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := newGatewayRebindNoEffectAbortTerminalV2(intent.Claim, intent.Roster, intent.RuntimeHeads,
		checkpoint, proof, time.Unix(5, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindTerminalStoreV2(fixture.dataRoot, receipt.Generation, receipt.OperationID)
	if err != nil || store.installExact(context.Background(), receipt) != nil {
		t.Fatalf("install no-effect terminal: %v", err)
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	typedProgress := 0
	for _, record := range history.Progress {
		if record.Generation == checkpoint.Generation {
			typedProgress++
		}
	}
	if err != nil || len(history.TerminalsV2) != 1 || len(history.IntentsV2) != 0 || typedProgress != 0 ||
		history.TerminalsV2[0].Receipt.Disposition != appaccess.GatewayRebindDispositionAbort {
		t.Fatalf("scan no-effect abort: intents=%d progress=%d terminals=%d error=%v",
			len(history.IntentsV2), typedProgress, len(history.TerminalsV2), err)
	}
	intentStore, err := newGatewayRebindProtectedIntentV2Store(fixture.dataRoot, intent.Generation, intent.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := intentStore.installExact(intent); err == nil {
		t.Fatal("typed intent was installed after an immutable no-effect abort")
	}
	if _, err := intentStore.load(); err == nil {
		t.Fatal("typed intent file was created before terminal conflict refusal")
	}
	nonIncreasing := receipt
	nonIncreasing.CreatedAt = proof.CreatedAt
	nonIncreasing.Digest, _ = gatewayRebindTerminalDigestV2(nonIncreasing)
	if !validGatewayRebindTerminalReceiptV2(nonIncreasing) ||
		gatewayRebindTerminalV2MatchesNoIntentHistory(nonIncreasing, checkpoint) {
		t.Fatal("no-effect terminal/proof time ordering was not enforced by retained-history matching")
	}
	changed := receipt
	changed.Disposition = appaccess.GatewayRebindDispositionCommit
	changed.Digest, _ = gatewayRebindTerminalDigestV2(changed)
	if validGatewayRebindTerminalReceiptV2(changed) {
		t.Fatal("no-effect abort proof was accepted as commit")
	}

	conflictFixture := newGatewayCurrentStateFixture(t)
	conflictCheckpoint, conflictIntent := gatewayRebindAttemptTypedFixture(t, conflictFixture)
	conflictCheckpointStore, _ := newGatewayRebindPredecessorCheckpointStore(conflictFixture.dataRoot,
		conflictCheckpoint.Generation, conflictCheckpoint.OperationID)
	if err := conflictCheckpointStore.installExact(conflictCheckpoint); err != nil {
		t.Fatal(err)
	}
	conflictIntentStore, _ := newGatewayRebindProtectedIntentV2Store(conflictFixture.dataRoot,
		conflictIntent.Generation, conflictIntent.OperationID)
	if err := conflictIntentStore.installExact(conflictIntent); err != nil {
		t.Fatal(err)
	}
	conflictProof := proof
	conflictProof.OperationID = conflictIntent.OperationID
	conflictProof.Generation = conflictIntent.Generation
	conflictProof.ClaimRequestDigest = conflictIntent.Claim.RequestDigest
	conflictProof.PredecessorCheckpointDigest = conflictCheckpoint.Digest
	conflictProof.SourceStateDigest = conflictCheckpoint.SourceStateDigest
	conflictProof.SuccessorIdentityDigest = conflictIntent.Identity.Digest
	conflictProof.Digest, _ = gatewayRebindNoEffectAbortProofDigest(conflictProof)
	conflictReceipt, err := newGatewayRebindNoEffectAbortTerminalV2(conflictIntent.Claim, conflictIntent.Roster,
		conflictIntent.RuntimeHeads, conflictCheckpoint, conflictProof, time.Unix(5, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	conflictStore, _ := newGatewayRebindTerminalStoreV2(conflictFixture.dataRoot,
		conflictReceipt.Generation, conflictReceipt.OperationID)
	if err := conflictStore.installExact(context.Background(), conflictReceipt); err == nil {
		t.Fatal("no-intent terminal was installed beside a retained typed intent")
	}
	if _, err := conflictStore.load(); err == nil {
		t.Fatal("conflicting no-intent terminal file was created before refusal")
	}
}

func TestGatewayRebindTypedRollbackTerminalBindsAdoptedOwnedResource(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	checkpoint, intent := gatewayRebindAttemptTypedFixture(t, fixture)
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(fixture.dataRoot, checkpoint.Generation, checkpoint.OperationID)
	if err != nil || checkpointStore.installExact(checkpoint) != nil {
		t.Fatalf("install typed checkpoint: %v", err)
	}
	intentStore, err := newGatewayRebindProtectedIntentV2Store(fixture.dataRoot, intent.Generation, intent.OperationID)
	if err != nil || intentStore.installExact(intent) != nil {
		t.Fatalf("install typed intent: %v", err)
	}
	first, err := newGatewayRebindSuccessorIntentProgressV2(intent, time.Unix(4, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	records, resources, _ := gatewayRebindTypedCompleteProgressFixture(t, fixture, checkpoint, intent, first)
	// Retain the forward prefix through handover intent. The final create then
	// succeeds without its sequence-fourteen acknowledgment, so rollback must
	// first adopt the exact configured container under that retained intent.
	forward := append([]gatewayRebindProgressRecord(nil), records[:13]...)
	for _, record := range forward {
		store, storeErr := newGatewayRebindProgressStore(fixture.dataRoot, record.Generation, record.OperationID, record.Sequence)
		if storeErr != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install typed forward progress %d: %v", record.Sequence, storeErr)
		}
	}
	routesDigest, err := gatewayRebindTypedCheckpointRoutesDigest(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	owned := gatewayRebindTypedRollbackOwnedFromEffect(*forward[len(forward)-1].TypedEffect)
	finalCopy := *resources.FinalContainer
	owned.FinalContainer = &finalCopy
	lastAt, err := parseGatewayRebindProgressTime(forward[len(forward)-1].OccurredAt)
	if err != nil {
		t.Fatal(err)
	}
	rollbackIntent, err := newGatewayRebindTypedRollbackIntentV2(intent, forward, owned, routesDigest,
		strings.Repeat("b", 64), lastAt.Add(time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	withoutAdoption := owned
	if _, err := newGatewayRebindTypedRollbackIntentV2(intent, forward, withoutAdoption, routesDigest, "",
		lastAt.Add(time.Nanosecond)); err == nil {
		t.Fatal("unacknowledged final create was accepted without exact adoption proof")
	}
	beforeHandover := append([]gatewayRebindProgressRecord(nil), records[:12]...)
	if _, err := newGatewayRebindTypedRollbackIntentV2(intent, beforeHandover, owned, routesDigest,
		strings.Repeat("b", 64), lastAt.Add(time.Nanosecond)); err == nil {
		t.Fatal("final container adoption was accepted before retained handover intent")
	}
	early := append([]gatewayRebindProgressRecord(nil), records[:2]...)
	futureOwned := gatewayRebindTypedRollbackOwnedFromEffect(*early[len(early)-1].TypedEffect)
	networkCopy, configCopy := resources.IngressNetwork, resources.ConfigVolume
	futureOwned.IngressNetwork, futureOwned.ConfigVolume = &networkCopy, &configCopy
	earlyAt, _ := parseGatewayRebindProgressTime(early[len(early)-1].OccurredAt)
	if _, err := newGatewayRebindTypedRollbackIntentV2(intent, early, futureOwned, routesDigest,
		strings.Repeat("c", 64), earlyAt.Add(time.Nanosecond)); err == nil {
		t.Fatal("network creation intent adopted an unauthorized future config volume")
	}
	firstAt, _ := parseGatewayRebindProgressTime(first.OccurredAt)
	postIntentRollback, err := newGatewayRebindTypedRollbackIntentV2(intent,
		[]gatewayRebindProgressRecord{first}, gatewayRebindTypedRollbackOwnedResources{}, routesDigest, "",
		firstAt.Add(time.Nanosecond))
	if err != nil {
		t.Fatalf("sequence-one post-intent rollback: %v", err)
	}
	postIntentProof := gatewayRebindTypedRollbackProofFixture(t, fixture, routesDigest, postIntentRollback.Digest)
	postIntentComplete, err := newGatewayRebindTypedRollbackCompleteV2(intent,
		[]gatewayRebindProgressRecord{first, postIntentRollback}, postIntentProof, firstAt.Add(2*time.Nanosecond))
	if err != nil {
		t.Fatalf("sequence-one rollback completion: %v", err)
	}
	postIntentTerminal, err := newGatewayRebindRollbackTerminalV2(intent, postIntentComplete, checkpoint,
		firstAt.Add(3*time.Nanosecond))
	postIntentProgress := []gatewayRebindProgressSelection{
		{Generation: first.Generation, Sequence: first.Sequence, Record: first},
		{Generation: postIntentRollback.Generation, Sequence: postIntentRollback.Sequence, Record: postIntentRollback},
		{Generation: postIntentComplete.Generation, Sequence: postIntentComplete.Sequence, Record: postIntentComplete},
	}
	if err != nil || !gatewayRebindTerminalV2MatchesIntentHistory(postIntentTerminal, intent, checkpoint, postIntentProgress) {
		t.Fatalf("sequence-one retained intent could not roll back safely: %v", err)
	}
	withRollbackIntent := append(append([]gatewayRebindProgressRecord(nil), forward...), rollbackIntent)
	proof := gatewayRebindTypedRollbackProofFixture(t, fixture, routesDigest, rollbackIntent.Digest)
	rollbackComplete, err := newGatewayRebindTypedRollbackCompleteV2(intent, withRollbackIntent, proof,
		lastAt.Add(2*time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []gatewayRebindProgressRecord{rollbackIntent, rollbackComplete} {
		store, storeErr := newGatewayRebindProgressStore(fixture.dataRoot, record.Generation, record.OperationID, record.Sequence)
		if storeErr != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install typed rollback progress %d: %v", record.Sequence, storeErr)
		}
	}
	all := append(withRollbackIntent, rollbackComplete)
	terminal, err := newGatewayRebindRollbackTerminalV2(intent, rollbackComplete, checkpoint,
		lastAt.Add(3*time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindTerminalStoreV2(fixture.dataRoot, terminal.Generation, terminal.OperationID)
	if err != nil || store.installExact(context.Background(), terminal) != nil ||
		store.installExact(context.Background(), terminal) != nil {
		t.Fatalf("install/replay typed rollback terminal: %v", err)
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	typedProgress := make([]gatewayRebindProgressSelection, 0, len(all))
	for _, selection := range history.Progress {
		if selection.Record.OperationID == intent.OperationID {
			typedProgress = append(typedProgress, selection)
		}
	}
	if err != nil || len(history.TerminalsV2) != 1 || len(typedProgress) != len(all) ||
		!gatewayRebindTerminalV2MatchesIntentHistory(terminal, intent, checkpoint, typedProgress) {
		t.Fatalf("typed rollback history mismatch: records=%d terminals=%d error=%v",
			len(typedProgress), len(history.TerminalsV2), err)
	}
	retained, err := gatewayRebindRetainedOperationInspectionV2(terminal)
	if err != nil || retained.Disposition != appaccess.GatewayRebindDispositionAbort ||
		retained.Resources.FinalContainerID != finalCopy.ID || retained.Resources.FinalContainerOwnershipDigest != finalCopy.OwnershipDigest {
		t.Fatalf("typed rollback retained ownership mismatch: %#v error=%v", retained, err)
	}
	wrongRemoval := proof
	wrongRemoval.Observation.IngressNetworkPresent = true
	wrongRemoval.Observation.Digest, _ = gatewayRebindFinalHandoverObservationDigest(wrongRemoval.Observation)
	wrongRemoval.Digest, _ = gatewayRebindFinalHandoverOutcomeDigest(wrongRemoval)
	if _, err := newGatewayRebindTypedRollbackCompleteV2(intent, withRollbackIntent, wrongRemoval,
		lastAt.Add(2*time.Nanosecond)); err == nil {
		t.Fatal("rollback completion accepted retained successor network")
	}
	if _, err := newGatewayRebindRollbackTerminalV2(intent, rollbackComplete, checkpoint,
		lastAt.Add(2*time.Nanosecond)); err == nil {
		t.Fatal("rollback terminal accepted a non-increasing timestamp")
	}
}
