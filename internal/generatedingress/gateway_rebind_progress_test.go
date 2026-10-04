package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type legacyGatewayRebindStageIntentV1 struct {
	Identity                 gatewayRebindSuccessorIdentity      `json:"identity"`
	ApprovedCaddyImageDigest string                              `json:"approvedCaddyImageDigest"`
	ObservedDockerImageID    string                              `json:"observedDockerImageId"`
	NetworkPlan              gatewayRebindSuccessorIntentNetwork `json:"networkPlan"`
	NetworkPlanDigest        string                              `json:"networkPlanDigest"`
	NetworkTopologyDigest    string                              `json:"networkTopologyDigest"`
	Network                  *gatewayRebindStageNetworkBinding   `json:"network,omitempty"`
}

type legacyGatewayRebindProgressRecordV1 struct {
	Version               int                               `json:"version"`
	Purpose               string                            `json:"purpose"`
	Generation            uint64                            `json:"generation"`
	OperationID           string                            `json:"operationId"`
	Sequence              uint64                            `json:"sequence"`
	Phase                 gatewayRebindProgressPhase        `json:"phase"`
	OccurredAt            string                            `json:"occurredAt"`
	ProtectedIntentDigest string                            `json:"protectedIntentDigest"`
	PreviousDigest        string                            `json:"previousDigest,omitempty"`
	Stage                 *legacyGatewayRebindStageIntentV1 `json:"stage,omitempty"`
	Digest                string                            `json:"digest"`
}

type preDataGatewayRebindStageIntent struct {
	Identity                 gatewayRebindSuccessorIdentity         `json:"identity"`
	ApprovedCaddyImageDigest string                                 `json:"approvedCaddyImageDigest"`
	ObservedDockerImageID    string                                 `json:"observedDockerImageId"`
	NetworkPlan              gatewayRebindSuccessorIntentNetwork    `json:"networkPlan"`
	NetworkPlanDigest        string                                 `json:"networkPlanDigest"`
	NetworkTopologyDigest    string                                 `json:"networkTopologyDigest"`
	Network                  *gatewayRebindStageNetworkBinding      `json:"network,omitempty"`
	ConfigVolume             *gatewayRebindStageConfigVolumeBinding `json:"configVolume,omitempty"`
}

type preDataGatewayRebindProgressRecord struct {
	Version               int                              `json:"version"`
	Purpose               string                           `json:"purpose"`
	Generation            uint64                           `json:"generation"`
	OperationID           string                           `json:"operationId"`
	Sequence              uint64                           `json:"sequence"`
	Phase                 gatewayRebindProgressPhase       `json:"phase"`
	OccurredAt            string                           `json:"occurredAt"`
	ProtectedIntentDigest string                           `json:"protectedIntentDigest"`
	PreviousDigest        string                           `json:"previousDigest,omitempty"`
	Stage                 *preDataGatewayRebindStageIntent `json:"stage,omitempty"`
	Digest                string                           `json:"digest"`
}

type preContainerGatewayRebindStageIntent struct {
	Identity                 gatewayRebindSuccessorIdentity         `json:"identity"`
	ApprovedCaddyImageDigest string                                 `json:"approvedCaddyImageDigest"`
	ObservedDockerImageID    string                                 `json:"observedDockerImageId"`
	NetworkPlan              gatewayRebindSuccessorIntentNetwork    `json:"networkPlan"`
	NetworkPlanDigest        string                                 `json:"networkPlanDigest"`
	NetworkTopologyDigest    string                                 `json:"networkTopologyDigest"`
	Network                  *gatewayRebindStageNetworkBinding      `json:"network,omitempty"`
	ConfigVolume             *gatewayRebindStageConfigVolumeBinding `json:"configVolume,omitempty"`
	DataVolume               *gatewayRebindStageDataVolumeBinding   `json:"dataVolume,omitempty"`
}

type preContainerGatewayRebindProgressRecord struct {
	Version               int                                   `json:"version"`
	Purpose               string                                `json:"purpose"`
	Generation            uint64                                `json:"generation"`
	OperationID           string                                `json:"operationId"`
	Sequence              uint64                                `json:"sequence"`
	Phase                 gatewayRebindProgressPhase            `json:"phase"`
	OccurredAt            string                                `json:"occurredAt"`
	ProtectedIntentDigest string                                `json:"protectedIntentDigest"`
	PreviousDigest        string                                `json:"previousDigest,omitempty"`
	Stage                 *preContainerGatewayRebindStageIntent `json:"stage,omitempty"`
	Digest                string                                `json:"digest"`
}

func gatewayRebindProgressFixture(t *testing.T) (gatewayRebindPredecessorFixture, gatewayRebindProtectedIntent) {
	t.Helper()
	fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
	selection, err := fixture.manager.resolveGatewayRebindProtectedIntentLocked(observation.result.RebindOperationID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, selection.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if err := selection.Store.installExact(intent); err != nil {
		t.Fatal(err)
	}
	return fixture, intent
}

func gatewayRebindProgressTimestamp(sequence uint64) time.Time {
	return time.Date(2026, time.October, 3, 12, 0, int(sequence), 0, time.UTC)
}

func gatewayRebindProgressStageObservation(intent gatewayRebindProtectedIntent,
	sequence uint64,
) gatewayRebindStageIntentObservation {
	return gatewayRebindStageIntentObservation{
		OccurredAt:            gatewayRebindProgressTimestamp(sequence),
		ObservedDockerImageID: strings.Repeat("b", 64),
		NetworkTopologyDigest: intent.NetworkObservationDigest,
	}
}

func legacyGatewayRebindProgressRecord(value gatewayRebindProgressRecord) legacyGatewayRebindProgressRecordV1 {
	legacy := legacyGatewayRebindProgressRecordV1{
		Version: value.Version, Purpose: value.Purpose, Generation: value.Generation,
		OperationID: value.OperationID, Sequence: value.Sequence, Phase: value.Phase,
		OccurredAt: value.OccurredAt, ProtectedIntentDigest: value.ProtectedIntentDigest,
		PreviousDigest: value.PreviousDigest, Digest: value.Digest,
	}
	if value.Stage != nil {
		legacy.Stage = &legacyGatewayRebindStageIntentV1{
			Identity: value.Stage.Identity, ApprovedCaddyImageDigest: value.Stage.ApprovedCaddyImageDigest,
			ObservedDockerImageID: value.Stage.ObservedDockerImageID, NetworkPlan: value.Stage.NetworkPlan,
			NetworkPlanDigest: value.Stage.NetworkPlanDigest, NetworkTopologyDigest: value.Stage.NetworkTopologyDigest,
			Network: value.Stage.Network,
		}
	}
	return legacy
}

func preDataGatewayRebindProgress(value gatewayRebindProgressRecord) preDataGatewayRebindProgressRecord {
	legacy := preDataGatewayRebindProgressRecord{
		Version: value.Version, Purpose: value.Purpose, Generation: value.Generation,
		OperationID: value.OperationID, Sequence: value.Sequence, Phase: value.Phase,
		OccurredAt: value.OccurredAt, ProtectedIntentDigest: value.ProtectedIntentDigest,
		PreviousDigest: value.PreviousDigest, Digest: value.Digest,
	}
	if value.Stage != nil {
		legacy.Stage = &preDataGatewayRebindStageIntent{
			Identity: value.Stage.Identity, ApprovedCaddyImageDigest: value.Stage.ApprovedCaddyImageDigest,
			ObservedDockerImageID: value.Stage.ObservedDockerImageID, NetworkPlan: value.Stage.NetworkPlan,
			NetworkPlanDigest: value.Stage.NetworkPlanDigest, NetworkTopologyDigest: value.Stage.NetworkTopologyDigest,
			Network: value.Stage.Network, ConfigVolume: value.Stage.ConfigVolume,
		}
	}
	return legacy
}

func preContainerGatewayRebindProgress(value gatewayRebindProgressRecord) preContainerGatewayRebindProgressRecord {
	legacy := preContainerGatewayRebindProgressRecord{
		Version: value.Version, Purpose: value.Purpose, Generation: value.Generation,
		OperationID: value.OperationID, Sequence: value.Sequence, Phase: value.Phase,
		OccurredAt: value.OccurredAt, ProtectedIntentDigest: value.ProtectedIntentDigest,
		PreviousDigest: value.PreviousDigest, Digest: value.Digest,
	}
	if value.Stage != nil {
		legacy.Stage = &preContainerGatewayRebindStageIntent{
			Identity: value.Stage.Identity, ApprovedCaddyImageDigest: value.Stage.ApprovedCaddyImageDigest,
			ObservedDockerImageID: value.Stage.ObservedDockerImageID, NetworkPlan: value.Stage.NetworkPlan,
			NetworkPlanDigest: value.Stage.NetworkPlanDigest, NetworkTopologyDigest: value.Stage.NetworkTopologyDigest,
			Network: value.Stage.Network, ConfigVolume: value.Stage.ConfigVolume, DataVolume: value.Stage.DataVolume,
		}
	}
	return legacy
}

func gatewayRebindProgressThroughConfig(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayRebindProtectedIntent, []gatewayRebindProgressRecord,
) {
	t.Helper()
	fixture, intent := gatewayRebindProgressFixture(t)
	first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
	if err != nil {
		t.Fatal(err)
	}
	second, err := newGatewayRebindStageIntentProgress(intent, first,
		gatewayRebindProgressStageObservation(intent, 2))
	if err != nil {
		t.Fatal(err)
	}
	networkOwnership, err := gatewayRebindStageNetworkOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	third, err := newGatewayRebindStageNetworkProgress(intent, second, strings.Repeat("a", 64),
		networkOwnership, gatewayRebindProgressTimestamp(3))
	if err != nil {
		t.Fatal(err)
	}
	configOwnership, err := gatewayRebindStageConfigVolumeOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	fourth, err := newGatewayRebindStageConfigVolumeProgress(intent, third,
		gatewayRebindStageConfigVolumeBinding{
			Name:       intent.Intent.Identity.ConfigVolume,
			Mountpoint: "/var/lib/docker/volumes/" + intent.Intent.Identity.ConfigVolume + "/_data",
			CreatedAt:  "2026-10-03T12:00:03Z", OwnershipDigest: configOwnership,
		}, gatewayRebindProgressTimestamp(4))
	if err != nil {
		t.Fatal(err)
	}
	return fixture, intent, []gatewayRebindProgressRecord{first, second, third, fourth}
}

func gatewayRebindProgressThroughData(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayRebindProtectedIntent, []gatewayRebindProgressRecord,
) {
	t.Helper()
	fixture, intent, records := gatewayRebindProgressThroughConfig(t)
	ownership, err := gatewayRebindStageDataVolumeOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	fifth, err := newGatewayRebindStageDataVolumeProgress(intent, records[3],
		gatewayRebindStageDataVolumeBinding{
			Name:       intent.Intent.Identity.DataVolume,
			Mountpoint: "/var/lib/docker/volumes/" + intent.Intent.Identity.DataVolume + "/_data",
			CreatedAt:  "2026-10-03T12:00:04Z", OwnershipDigest: ownership,
		}, gatewayRebindProgressTimestamp(5))
	if err != nil {
		t.Fatal(err)
	}
	return fixture, intent, append(records, fifth)
}

func gatewayRebindProgressThroughContainer(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayRebindProtectedIntent, []gatewayRebindProgressRecord,
) {
	t.Helper()
	fixture, intent, records := gatewayRebindProgressThroughData(t)
	stage := *records[4].Stage
	ownership, err := gatewayRebindStageContainerOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := gatewayRebindStageContainerConfigurationDigest(intent, stage)
	if err != nil {
		t.Fatal(err)
	}
	sixth, err := newGatewayRebindStageContainerProgress(intent, records[4], gatewayRebindStageContainerBinding{
		ID: strings.Repeat("c", 64), OwnershipDigest: ownership, ConfigurationDigest: configuration,
	}, gatewayRebindProgressTimestamp(6))
	if err != nil {
		t.Fatal(err)
	}
	return fixture, intent, append(records, sixth)
}

func TestGatewayRebindProgressSequenceSevenBindsExactStageConfigIntent(t *testing.T) {
	fixture, intent, records := gatewayRebindProgressThroughContainer(t)
	binding, err := gatewayRebindStageConfigIntentBindingFor(intent, records[5])
	if err != nil {
		t.Fatal(err)
	}
	seventh, err := newGatewayRebindStageConfigIntentProgress(intent, records[5], binding,
		gatewayRebindProgressTimestamp(7))
	if err != nil || !validGatewayRebindProgressRecord(seventh) ||
		seventh.Phase != gatewayRebindProgressStageConfigIntent || seventh.Stage == nil ||
		seventh.Stage.StageConfigIntent == nil || *seventh.Stage.StageConfigIntent != binding {
		t.Fatalf("sequence seven=%#v error=%v", seventh, err)
	}
	withoutConfigIntent := *seventh.Stage
	withoutConfigIntent.StageConfigIntent = nil
	if !reflect.DeepEqual(withoutConfigIntent, *records[5].Stage) {
		t.Fatal("sequence seven changed a sequence-six stage field")
	}
	for _, record := range append(records, seventh) {
		store, storeErr := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
			intent.Generation, intent.OperationID, record.Sequence)
		if storeErr != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install sequence %d: %v", record.Sequence, storeErr)
		}
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 7 || !reflect.DeepEqual(history.Progress[6].Record, seventh) {
		t.Fatalf("sequence-seven history=%#v error=%v", history.Progress, err)
	}
}

func TestGatewayRebindProgressHistoryRejectsForgedSequenceSeven(t *testing.T) {
	fixture, intent, records := gatewayRebindProgressThroughContainer(t)
	for _, record := range records {
		store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
			intent.Generation, intent.OperationID, record.Sequence)
		if err != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install sequence %d: %v", record.Sequence, err)
		}
	}
	binding, err := gatewayRebindStageConfigIntentBindingFor(intent, records[5])
	if err != nil {
		t.Fatal(err)
	}
	seventh, err := newGatewayRebindStageConfigIntentProgress(intent, records[5], binding,
		gatewayRebindProgressTimestamp(7))
	if err != nil {
		t.Fatal(err)
	}
	forgedStage := *seventh.Stage
	forgedBinding := *forgedStage.StageConfigIntent
	forgedBinding.ContentDigest = strings.Repeat("f", 64)
	forgedStage.StageConfigIntent = &forgedBinding
	seventh.Stage = &forgedStage
	seventh.Digest, err = gatewayRebindProgressDigest(seventh)
	if err != nil || !validGatewayRebindProgressRecord(seventh) {
		t.Fatalf("forged sequence seven is not structurally valid: %v", err)
	}
	store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
		intent.Generation, intent.OperationID, 7)
	if err != nil {
		t.Fatal(err)
	}
	state := gatewayUpgradeStateStore{directory: store.directory}
	if err := state.writeExact(store.path, store.purpose, seventh, false, maxGatewayRebindProgressBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
		t.Fatal("scanner accepted forged sequence-seven progress")
	}
}

func TestGatewayRebindProgressSequenceSevenRejectsInvalidOrStaleInputs(t *testing.T) {
	_, intent, records := gatewayRebindProgressThroughContainer(t)
	binding, err := gatewayRebindStageConfigIntentBindingFor(intent, records[5])
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		previous gatewayRebindProgressRecord
		binding  gatewayRebindStageConfigIntentBinding
		at       time.Time
	}{
		{name: "exact", previous: records[5], binding: binding, at: gatewayRebindProgressTimestamp(7)},
		{name: "stale predecessor", previous: records[4], binding: binding, at: gatewayRebindProgressTimestamp(7)},
		{name: "same time", previous: records[5], binding: binding, at: gatewayRebindProgressTimestamp(6)},
		{name: "wrong destination", previous: records[5], binding: func() gatewayRebindStageConfigIntentBinding {
			value := binding
			value.Destination = "/config/active.json"
			return value
		}(), at: gatewayRebindProgressTimestamp(7)},
		{name: "wrong prior progress digest", previous: records[5], binding: func() gatewayRebindStageConfigIntentBinding {
			value := binding
			value.PriorProgressDigest = strings.Repeat("f", 64)
			return value
		}(), at: gatewayRebindProgressTimestamp(7)},
		{name: "wrong protected predecessor digest", previous: records[5], binding: func() gatewayRebindStageConfigIntentBinding {
			value := binding
			value.ProtectedPredecessorDigest = strings.Repeat("f", 64)
			return value
		}(), at: gatewayRebindProgressTimestamp(7)},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := newGatewayRebindStageConfigIntentProgress(intent, test.previous, test.binding, test.at)
			if test.name == "exact" {
				if err != nil || !validGatewayRebindProgressRecord(value) {
					t.Fatalf("exact sequence seven rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("invalid sequence seven accepted: %#v", value)
			}
		})
	}
}

func TestGatewayRebindProgressSequenceSixBindsExactStoppedStageContainer(t *testing.T) {
	fixture, intent, records := gatewayRebindProgressThroughData(t)
	stage := *records[4].Stage
	ownership, err := gatewayRebindStageContainerOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := gatewayRebindStageContainerConfigurationDigest(intent, stage)
	if err != nil {
		t.Fatal(err)
	}
	binding := gatewayRebindStageContainerBinding{
		ID: strings.Repeat("c", 64), OwnershipDigest: ownership, ConfigurationDigest: configuration,
	}
	sixth, err := newGatewayRebindStageContainerProgress(intent, records[4], binding,
		gatewayRebindProgressTimestamp(6))
	if err != nil || !validGatewayRebindProgressRecord(sixth) || sixth.Stage == nil ||
		sixth.Stage.StageContainer == nil || *sixth.Stage.StageContainer != binding {
		t.Fatalf("sequence six=%#v error=%v", sixth, err)
	}
	withoutContainer := *sixth.Stage
	withoutContainer.StageContainer = nil
	if !reflect.DeepEqual(withoutContainer, *records[4].Stage) {
		t.Fatal("sequence six changed a sequence-five stage field")
	}
	for _, record := range append(records, sixth) {
		store, storeErr := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
			intent.Generation, intent.OperationID, record.Sequence)
		if storeErr != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install sequence %d: %v", record.Sequence, storeErr)
		}
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 6 || !reflect.DeepEqual(history.Progress[5].Record, sixth) {
		t.Fatalf("sequence-six history=%#v error=%v", history.Progress, err)
	}
}

func TestGatewayRebindProgressOptionalStageContainerPreservesSequenceOneThroughFiveBytesAndDigests(t *testing.T) {
	_, _, records := gatewayRebindProgressThroughData(t)
	for _, record := range records {
		actualBytes, marshalErr := json.Marshal(record)
		legacy := preContainerGatewayRebindProgress(record)
		legacyBytes, legacyErr := json.Marshal(legacy)
		if marshalErr != nil || legacyErr != nil || !reflect.DeepEqual(actualBytes, legacyBytes) {
			t.Fatalf("sequence %d bytes changed: actual=%s legacy=%s errors=%v/%v",
				record.Sequence, actualBytes, legacyBytes, marshalErr, legacyErr)
		}
		legacy.Digest = ""
		legacyDigest, digestErr := canonicalDigest(legacy)
		if digestErr != nil || legacyDigest != record.Digest {
			t.Fatalf("sequence %d digest changed: actual=%s legacy=%s error=%v",
				record.Sequence, record.Digest, legacyDigest, digestErr)
		}
	}
}

func TestGatewayRebindProgressHistoryRejectsForgedSequenceSix(t *testing.T) {
	fixture, intent, records := gatewayRebindProgressThroughData(t)
	stage := *records[4].Stage
	ownership, err := gatewayRebindStageContainerOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := gatewayRebindStageContainerConfigurationDigest(intent, stage)
	if err != nil {
		t.Fatal(err)
	}
	sixth, err := newGatewayRebindStageContainerProgress(intent, records[4],
		gatewayRebindStageContainerBinding{ID: strings.Repeat("c", 64), OwnershipDigest: ownership,
			ConfigurationDigest: configuration}, gatewayRebindProgressTimestamp(6))
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		store, storeErr := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
			intent.Generation, intent.OperationID, record.Sequence)
		if storeErr != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install sequence %d: %v", record.Sequence, storeErr)
		}
	}
	forgedStage := *sixth.Stage
	forgedBinding := *forgedStage.StageContainer
	forgedBinding.ConfigurationDigest = strings.Repeat("f", 64)
	forgedStage.StageContainer = &forgedBinding
	sixth.Stage = &forgedStage
	sixth.Digest, err = gatewayRebindProgressDigest(sixth)
	if err != nil || !validGatewayRebindProgressRecord(sixth) {
		t.Fatalf("forged sequence six is not structurally valid: %v", err)
	}
	store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
		intent.Generation, intent.OperationID, 6)
	if err != nil {
		t.Fatal(err)
	}
	state := gatewayUpgradeStateStore{directory: store.directory}
	if err := state.writeExact(store.path, store.purpose, sixth, false, maxGatewayRebindProgressBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
		t.Fatal("scanner accepted forged sequence-six progress")
	}
}

func TestGatewayRebindProgressSequenceSixRejectsInvalidOrStaleInputs(t *testing.T) {
	_, intent, records := gatewayRebindProgressThroughData(t)
	stage := *records[4].Stage
	ownership, err := gatewayRebindStageContainerOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := gatewayRebindStageContainerConfigurationDigest(intent, stage)
	if err != nil {
		t.Fatal(err)
	}
	valid := gatewayRebindStageContainerBinding{ID: strings.Repeat("c", 64), OwnershipDigest: ownership,
		ConfigurationDigest: configuration}
	for _, test := range []struct {
		name     string
		previous gatewayRebindProgressRecord
		binding  gatewayRebindStageContainerBinding
		at       time.Time
	}{
		{name: "sequence four predecessor", previous: records[3], binding: valid, at: gatewayRebindProgressTimestamp(6)},
		{name: "equal timestamp", previous: records[4], binding: valid, at: gatewayRebindProgressTimestamp(5)},
		{name: "wrong container id", previous: records[4], binding: func() gatewayRebindStageContainerBinding { value := valid; value.ID = "bad"; return value }(), at: gatewayRebindProgressTimestamp(6)},
		{name: "wrong ownership", previous: records[4], binding: func() gatewayRebindStageContainerBinding {
			value := valid
			value.OwnershipDigest = strings.Repeat("f", 64)
			return value
		}(), at: gatewayRebindProgressTimestamp(6)},
		{name: "wrong configuration", previous: records[4], binding: func() gatewayRebindStageContainerBinding {
			value := valid
			value.ConfigurationDigest = strings.Repeat("f", 64)
			return value
		}(), at: gatewayRebindProgressTimestamp(6)},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, makeErr := newGatewayRebindStageContainerProgress(intent, test.previous, test.binding, test.at)
			if makeErr == nil || !reflect.DeepEqual(value, gatewayRebindProgressRecord{}) {
				t.Fatalf("invalid sequence-six input accepted: %#v error=%v", value, makeErr)
			}
		})
	}
}

func TestGatewayRebindProgressSequenceFourBindsExactConfigVolume(t *testing.T) {
	fixture, intent := gatewayRebindProgressFixture(t)
	first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
	if err != nil {
		t.Fatal(err)
	}
	second, err := newGatewayRebindStageIntentProgress(intent, first,
		gatewayRebindProgressStageObservation(intent, 2))
	if err != nil {
		t.Fatal(err)
	}
	networkOwnership, err := gatewayRebindStageNetworkOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	third, err := newGatewayRebindStageNetworkProgress(intent, second, strings.Repeat("a", 64),
		networkOwnership, gatewayRebindProgressTimestamp(3))
	if err != nil {
		t.Fatal(err)
	}
	volumeOwnership, err := gatewayRebindStageConfigVolumeOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	binding := gatewayRebindStageConfigVolumeBinding{
		Name:       intent.Intent.Identity.ConfigVolume,
		Mountpoint: "/var/lib/docker/volumes/" + intent.Intent.Identity.ConfigVolume + "/_data",
		CreatedAt:  "2026-10-03T12:00:03Z", OwnershipDigest: volumeOwnership,
	}
	fourth, err := newGatewayRebindStageConfigVolumeProgress(intent, third, binding,
		gatewayRebindProgressTimestamp(4))
	if err != nil || !validGatewayRebindProgressRecord(fourth) || fourth.Stage == nil ||
		fourth.Stage.ConfigVolume == nil || *fourth.Stage.ConfigVolume != binding {
		t.Fatalf("sequence four=%#v error=%v", fourth, err)
	}
	stageWithoutConfig := *fourth.Stage
	stageWithoutConfig.ConfigVolume = nil
	if !reflect.DeepEqual(stageWithoutConfig, *third.Stage) {
		t.Fatal("sequence four changed a sequence-three stage field")
	}
	for _, record := range []gatewayRebindProgressRecord{first, second, third, fourth} {
		store, storeErr := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
			intent.Generation, intent.OperationID, record.Sequence)
		if storeErr != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install sequence %d: %v", record.Sequence, storeErr)
		}
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 4 || !reflect.DeepEqual(history.Progress[3].Record, fourth) {
		t.Fatalf("sequence-four history=%#v error=%v", history.Progress, err)
	}
}

func TestGatewayRebindProgressSequenceFiveBindsExactDataVolume(t *testing.T) {
	fixture, intent, records := gatewayRebindProgressThroughConfig(t)
	dataOwnership, err := gatewayRebindStageDataVolumeOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	binding := gatewayRebindStageDataVolumeBinding{
		Name:       intent.Intent.Identity.DataVolume,
		Mountpoint: "/var/lib/docker/volumes/" + intent.Intent.Identity.DataVolume + "/_data",
		CreatedAt:  "2026-10-03T12:00:04Z", OwnershipDigest: dataOwnership,
	}
	fifth, err := newGatewayRebindStageDataVolumeProgress(intent, records[3], binding,
		gatewayRebindProgressTimestamp(5))
	if err != nil || !validGatewayRebindProgressRecord(fifth) || fifth.Stage == nil ||
		fifth.Stage.DataVolume == nil || *fifth.Stage.DataVolume != binding {
		t.Fatalf("sequence five=%#v error=%v", fifth, err)
	}
	stageWithoutData := *fifth.Stage
	stageWithoutData.DataVolume = nil
	if !reflect.DeepEqual(stageWithoutData, *records[3].Stage) {
		t.Fatal("sequence five changed a sequence-four stage field")
	}
	records = append(records, fifth)
	for _, record := range records {
		store, storeErr := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
			intent.Generation, intent.OperationID, record.Sequence)
		if storeErr != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install sequence %d: %v", record.Sequence, storeErr)
		}
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 5 || !reflect.DeepEqual(history.Progress[4].Record, fifth) {
		t.Fatalf("sequence-five history=%#v error=%v", history.Progress, err)
	}
}

func TestGatewayRebindProgressHistoryRejectsForgedSequenceFive(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindStageIntent)
	}{
		{name: "inherited image substitution", mutate: func(stage *gatewayRebindStageIntent) {
			stage.ObservedDockerImageID = strings.Repeat("d", 64)
		}},
		{name: "data volume identity substitution", mutate: func(stage *gatewayRebindStageIntent) {
			stage.DataVolume.Name += "-foreign"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, intent, records := gatewayRebindProgressThroughConfig(t)
			ownership, err := gatewayRebindStageDataVolumeOwnershipDigest(intent)
			if err != nil {
				t.Fatal(err)
			}
			fifth, err := newGatewayRebindStageDataVolumeProgress(intent, records[3],
				gatewayRebindStageDataVolumeBinding{
					Name:       intent.Intent.Identity.DataVolume,
					Mountpoint: "/var/lib/docker/volumes/" + intent.Intent.Identity.DataVolume + "/_data",
					CreatedAt:  "2026-10-03T12:00:04Z", OwnershipDigest: ownership,
				}, gatewayRebindProgressTimestamp(5))
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range records {
				store, storeErr := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
					intent.Generation, intent.OperationID, record.Sequence)
				if storeErr != nil {
					t.Fatal(storeErr)
				}
				if err := store.installExact(context.Background(), record); err != nil {
					t.Fatal(err)
				}
			}
			stage := *fifth.Stage
			binding := *stage.DataVolume
			stage.DataVolume = &binding
			fifth.Stage = &stage
			test.mutate(fifth.Stage)
			fifth.Digest, err = gatewayRebindProgressDigest(fifth)
			if err != nil || !validGatewayRebindProgressRecord(fifth) {
				t.Fatalf("forged sequence five is not structurally valid: %v", err)
			}
			store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
				intent.Generation, intent.OperationID, 5)
			if err != nil {
				t.Fatal(err)
			}
			state := gatewayUpgradeStateStore{directory: store.directory}
			if err := state.writeExact(store.path, store.purpose, fifth, false,
				maxGatewayRebindProgressBytes); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
				t.Fatal("scanner accepted forged sequence-five progress")
			}
		})
	}
}

func TestGatewayRebindProgressOptionalDataVolumePreservesSequenceOneThroughFourBytesAndDigests(t *testing.T) {
	_, _, records := gatewayRebindProgressThroughConfig(t)
	for _, record := range records {
		actualBytes, marshalErr := json.Marshal(record)
		legacy := preDataGatewayRebindProgress(record)
		legacyBytes, legacyErr := json.Marshal(legacy)
		if marshalErr != nil || legacyErr != nil || !reflect.DeepEqual(actualBytes, legacyBytes) {
			t.Fatalf("sequence %d bytes changed: actual=%s legacy=%s errors=%v/%v",
				record.Sequence, actualBytes, legacyBytes, marshalErr, legacyErr)
		}
		legacy.Digest = ""
		legacyDigest, digestErr := canonicalDigest(legacy)
		if digestErr != nil || legacyDigest != record.Digest {
			t.Fatalf("sequence %d digest changed: actual=%s legacy=%s error=%v",
				record.Sequence, record.Digest, legacyDigest, digestErr)
		}
	}
}

func TestGatewayRebindProgressSequenceFiveRejectsInvalidOrStaleInputs(t *testing.T) {
	_, intent, records := gatewayRebindProgressThroughConfig(t)
	ownership, err := gatewayRebindStageDataVolumeOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	valid := gatewayRebindStageDataVolumeBinding{
		Name:       intent.Intent.Identity.DataVolume,
		Mountpoint: "/var/lib/docker/volumes/" + intent.Intent.Identity.DataVolume + "/_data",
		CreatedAt:  "2026-10-03T12:00:04Z", OwnershipDigest: ownership,
	}
	for _, test := range []struct {
		name     string
		previous func() gatewayRebindProgressRecord
		binding  func() gatewayRebindStageDataVolumeBinding
		at       time.Time
	}{
		{name: "sequence three predecessor", previous: func() gatewayRebindProgressRecord { return records[2] }, binding: func() gatewayRebindStageDataVolumeBinding { return valid }, at: gatewayRebindProgressTimestamp(5)},
		{name: "equal timestamp", previous: func() gatewayRebindProgressRecord { return records[3] }, binding: func() gatewayRebindStageDataVolumeBinding { return valid }, at: gatewayRebindProgressTimestamp(4)},
		{name: "wrong name", previous: func() gatewayRebindProgressRecord { return records[3] }, binding: func() gatewayRebindStageDataVolumeBinding {
			value := valid
			value.Name = intent.Intent.Identity.ConfigVolume
			return value
		}, at: gatewayRebindProgressTimestamp(5)},
		{name: "wrong ownership", previous: func() gatewayRebindProgressRecord { return records[3] }, binding: func() gatewayRebindStageDataVolumeBinding {
			value := valid
			value.OwnershipDigest = strings.Repeat("f", 64)
			return value
		}, at: gatewayRebindProgressTimestamp(5)},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, makeErr := newGatewayRebindStageDataVolumeProgress(intent, test.previous(), test.binding(), test.at)
			if makeErr == nil || !reflect.DeepEqual(value, gatewayRebindProgressRecord{}) {
				t.Fatalf("invalid sequence-five input accepted: %#v error=%v", value, makeErr)
			}
		})
	}
}

func TestGatewayRebindProgressOptionalConfigVolumePreservesSequenceOneThroughThreeBytesAndDigests(t *testing.T) {
	_, intent := gatewayRebindProgressFixture(t)
	first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
	if err != nil {
		t.Fatal(err)
	}
	second, err := newGatewayRebindStageIntentProgress(intent, first,
		gatewayRebindProgressStageObservation(intent, 2))
	if err != nil {
		t.Fatal(err)
	}
	ownership, err := gatewayRebindStageNetworkOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	third, err := newGatewayRebindStageNetworkProgress(intent, second, strings.Repeat("a", 64),
		ownership, gatewayRebindProgressTimestamp(3))
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []gatewayRebindProgressRecord{first, second, third} {
		actualBytes, marshalErr := json.Marshal(record)
		legacy := legacyGatewayRebindProgressRecord(record)
		legacyBytes, legacyErr := json.Marshal(legacy)
		if marshalErr != nil || legacyErr != nil || !reflect.DeepEqual(actualBytes, legacyBytes) {
			t.Fatalf("sequence %d bytes changed: actual=%s legacy=%s errors=%v/%v",
				record.Sequence, actualBytes, legacyBytes, marshalErr, legacyErr)
		}
		legacy.Digest = ""
		legacyDigest, digestErr := canonicalDigest(legacy)
		if digestErr != nil || legacyDigest != record.Digest {
			t.Fatalf("sequence %d digest changed: actual=%s legacy=%s error=%v",
				record.Sequence, record.Digest, legacyDigest, digestErr)
		}
	}
}

func TestGatewayRebindProgressInstallsScansAndReplaysExactPreEffectRecords(t *testing.T) {
	fixture, intent := gatewayRebindProgressFixture(t)
	beforeDatabase, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeCommands := len(fixture.runner.commands)

	first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
	if err != nil {
		t.Fatal(err)
	}
	firstStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.installExact(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := firstStore.installExact(context.Background(), first); err != nil {
		t.Fatalf("exact first replay: %v", err)
	}
	loadedFirst, err := firstStore.load()
	if err != nil || !reflect.DeepEqual(loadedFirst, first) {
		t.Fatalf("first readback=%#v err=%v", loadedFirst, err)
	}

	second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindProgressStageObservation(intent, 2))
	if err != nil {
		t.Fatal(err)
	}
	if second.Stage == nil || second.Stage.ApprovedCaddyImageDigest != intent.Intent.Identity.CaddyImageDigest ||
		second.Stage.ObservedDockerImageID == second.Stage.ApprovedCaddyImageDigest {
		t.Fatalf("stage image bindings were conflated: %#v", second.Stage)
	}
	secondStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := secondStore.installExact(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := secondStore.installExact(context.Background(), second); err != nil {
		t.Fatalf("exact stage replay: %v", err)
	}

	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || len(history.Progress) != 2 ||
		!reflect.DeepEqual(history.Progress[0].Record, first) || !reflect.DeepEqual(history.Progress[1].Record, second) {
		t.Fatalf("progress history=%#v err=%v", history, err)
	}
	if _, err := readGatewayHistorySnapshot(fixture.manager.store); err == nil {
		t.Fatal("legacy history scanner accepted rebind progress")
	}
	if _, err := fixture.manager.scanGatewayUpgradeHistoryLocked(); err == nil {
		t.Fatal("legacy ownership scanner accepted rebind progress")
	}
	afterDatabase, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(afterDatabase, beforeDatabase) || len(fixture.runner.commands) != beforeCommands {
		t.Fatalf("pre-effect progress changed SQLite or Docker commands: database=%t commands=%d err=%v",
			reflect.DeepEqual(afterDatabase, beforeDatabase), len(fixture.runner.commands)-beforeCommands, err)
	}
}

func TestGatewayRebindProgressRejectsMissingIntentInvalidOrderingAndCancellationBeforeWrite(t *testing.T) {
	t.Run("missing installed protected intent", func(t *testing.T) {
		fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
		intent, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, predecessor.Generation+1)
		if err != nil {
			t.Fatal(err)
		}
		first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(context.Background(), first); err == nil {
			t.Fatal("progress without an installed protected intent was accepted")
		}
		if _, err := os.Stat(store.path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing-intent install created progress artifact: %v", err)
		}
	})

	t.Run("stage record without installed successor record", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindProgressStageObservation(intent, 2))
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(context.Background(), second); err == nil {
			t.Fatal("stage record without successor record was accepted")
		}
		if _, err := os.Stat(store.path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stage-before-successor install created progress artifact: %v", err)
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err != nil {
			t.Fatalf("rejected stage-before-successor changed protected history: %v", err)
		}
	})

	t.Run("cancelled before write", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := store.installExact(ctx, first); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled install error=%v", err)
		}
		if _, err := os.Stat(store.path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancelled install created progress artifact: %v", err)
		}
	})
}

func TestGatewayRebindProgressRejectsInvalidStageObservation(t *testing.T) {
	_, intent := gatewayRebindProgressFixture(t)
	first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindStageIntentObservation)
	}{
		{"equal timestamp", func(value *gatewayRebindStageIntentObservation) {
			value.OccurredAt = gatewayRebindProgressTimestamp(1)
		}},
		{"backward timestamp", func(value *gatewayRebindStageIntentObservation) {
			value.OccurredAt = gatewayRebindProgressTimestamp(1).Add(-time.Second)
		}},
		{"malformed Docker image ID", func(value *gatewayRebindStageIntentObservation) {
			value.ObservedDockerImageID = "sha256:invalid"
		}},
		{"malformed network topology digest", func(value *gatewayRebindStageIntentObservation) {
			value.NetworkTopologyDigest = "invalid"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			observation := gatewayRebindProgressStageObservation(intent, 2)
			test.mutate(&observation)
			value, err := newGatewayRebindStageIntentProgress(intent, first, observation)
			if err == nil || !reflect.DeepEqual(value, gatewayRebindProgressRecord{}) {
				t.Fatalf("invalid stage observation was accepted: record=%#v err=%v", value, err)
			}
		})
	}
}

func TestGatewayRebindProgressCreateOnlyAmbiguityRequiresFreshStrictReplay(t *testing.T) {
	fixture, intent := gatewayRebindProgressFixture(t)
	first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
	if err != nil {
		t.Fatal(err)
	}
	original := upgradeProtectedWriteNew
	upgradeProtectedWriteNew = func(path, purpose string, body []byte) error {
		if err := original(path, purpose, body); err != nil {
			return err
		}
		return errors.New("injected post-install uncertainty")
	}
	t.Cleanup(func() { upgradeProtectedWriteNew = original })
	if err := store.installExact(context.Background(), first); err == nil {
		t.Fatal("ambiguous first progress install was reported as success")
	}
	upgradeProtectedWriteNew = original
	if err := store.installExact(context.Background(), first); err != nil {
		t.Fatalf("fresh strict exact replay after ambiguity: %v", err)
	}
}

func TestGatewayRebindProgressRejectsForgedBindingsBeforeAndDuringHistoryScan(t *testing.T) {
	fixture, intent := gatewayRebindProgressFixture(t)
	first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
	if err != nil {
		t.Fatal(err)
	}
	firstStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.installExact(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindProgressRecord)
		valid  bool
	}{
		{"wrong phase", func(value *gatewayRebindProgressRecord) { value.Phase = gatewayRebindProgressSuccessorIntent }, false},
		{"wrong scoped purpose", func(value *gatewayRebindProgressRecord) { value.Purpose = "wrong" }, false},
		{"forged protected digest", func(value *gatewayRebindProgressRecord) { value.ProtectedIntentDigest = strings.Repeat("d", 64) }, true},
		{"resource plan substitution", func(value *gatewayRebindProgressRecord) {
			value.Stage.NetworkPlan = gatewayRebindSuccessorIntentNetwork{
				Subnet: "10.242.0.16/28", GatewayIPv4: "10.242.0.17", ContainerIPv4: "10.242.0.18",
			}
		}, true},
		{"observed image is approved digest", func(value *gatewayRebindProgressRecord) {
			value.Stage.ObservedDockerImageID = value.Stage.ApprovedCaddyImageDigest
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			second, makeErr := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindProgressStageObservation(intent, 2))
			if makeErr != nil {
				t.Fatal(makeErr)
			}
			test.mutate(&second)
			second.Digest, makeErr = gatewayRebindProgressDigest(second)
			if makeErr != nil {
				t.Fatal(makeErr)
			}
			if validGatewayRebindProgressRecord(second) != test.valid {
				t.Fatalf("structural validity=%t want %t for %#v", validGatewayRebindProgressRecord(second), test.valid, second)
			}
			store, storeErr := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 2)
			if storeErr != nil {
				t.Fatal(storeErr)
			}
			if test.valid {
				if err := store.installExact(context.Background(), second); err == nil {
					t.Fatal("forged structural record was installed")
				}
				if _, err := os.Stat(store.path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("forged record created an artifact: %v", err)
				}
				return
			}
			if err := store.installExact(context.Background(), second); err == nil {
				t.Fatal("invalid record was installed")
			}
		})
	}
}

func TestGatewayRebindProgressHistoryRejectsNamespaceTamperingAndChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindStageIntent)
	}{
		{name: "sequence three image substitution", mutate: func(stage *gatewayRebindStageIntent) {
			stage.ObservedDockerImageID = strings.Repeat("d", 64)
		}},
		{name: "sequence three topology substitution", mutate: func(stage *gatewayRebindStageIntent) {
			stage.NetworkTopologyDigest = strings.Repeat("d", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, intent := gatewayRebindProgressFixture(t)
			first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
			if err != nil {
				t.Fatal(err)
			}
			firstStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
				intent.Generation, intent.OperationID, 1)
			if err != nil || firstStore.installExact(context.Background(), first) != nil {
				t.Fatalf("install sequence one: %v", err)
			}
			second, err := newGatewayRebindStageIntentProgress(intent, first,
				gatewayRebindProgressStageObservation(intent, 2))
			if err != nil {
				t.Fatal(err)
			}
			secondStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
				intent.Generation, intent.OperationID, 2)
			if err != nil || secondStore.installExact(context.Background(), second) != nil {
				t.Fatalf("install sequence two: %v", err)
			}
			ownership, err := gatewayRebindStageNetworkOwnershipDigest(intent)
			if err != nil {
				t.Fatal(err)
			}
			third, err := newGatewayRebindStageNetworkProgress(intent, second,
				strings.Repeat("a", 64), ownership, gatewayRebindProgressTimestamp(3))
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(third.Stage)
			third.Digest, err = gatewayRebindProgressDigest(third)
			if err != nil || !validGatewayRebindProgressRecord(third) {
				t.Fatalf("forged sequence three is not structurally valid: %v", err)
			}
			thirdStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
				intent.Generation, intent.OperationID, 3)
			if err != nil {
				t.Fatal(err)
			}
			state := gatewayUpgradeStateStore{directory: thirdStore.directory}
			if err := state.writeExact(thirdStore.path, thirdStore.purpose, third, false,
				maxGatewayRebindProgressBytes); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
				t.Fatal("scanner accepted sequence three with a substituted sequence two stage field")
			}
		})
	}

	t.Run("persisted stage without successor sequence", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		name, _ := gatewayRebindProgressName(intent.Generation, intent.OperationID, 2)
		if err := os.WriteFile(filepath.Join(fixture.manager.store.root, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil ||
			!strings.Contains(err.Error(), "sequence gap") {
			t.Fatalf("persisted sequence two without one was not rejected as a gap: %v", err)
		}
	})

	t.Run("unsupported future progress sequence", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		firstStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := firstStore.installExact(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindProgressStageObservation(intent, 2))
		if err != nil {
			t.Fatal(err)
		}
		secondStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err := secondStore.installExact(context.Background(), second); err != nil {
			t.Fatal(err)
		}
		name, _ := gatewayRebindProgressName(intent.Generation, intent.OperationID, 7)
		if err := os.WriteFile(filepath.Join(fixture.manager.store.root, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil ||
			!strings.Contains(err.Error(), "sequence gap") {
			t.Fatalf("unsupported seventh progress sequence was accepted: %v", err)
		}
	})

	t.Run("same generation file collision", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		name, _ := gatewayRebindProgressName(intent.Generation, uuid.NewString(), 1)
		path := filepath.Join(fixture.manager.store.root, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
			t.Fatal("different-operation progress collision was accepted")
		}
		if _, err := readGatewayHistorySnapshot(fixture.manager.store); err == nil {
			t.Fatal("legacy scanner accepted progress collision")
		}
	})

	t.Run("self-consistent persisted field substitution", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		firstStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := firstStore.installExact(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindProgressStageObservation(intent, 2))
		if err != nil {
			t.Fatal(err)
		}
		secondStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err := secondStore.installExact(context.Background(), second); err != nil {
			t.Fatal(err)
		}
		forged := second
		forged.Stage = &gatewayRebindStageIntent{
			Identity:                 second.Stage.Identity,
			ApprovedCaddyImageDigest: second.Stage.ApprovedCaddyImageDigest,
			ObservedDockerImageID:    second.Stage.ObservedDockerImageID,
			NetworkPlan: gatewayRebindSuccessorIntentNetwork{
				Subnet: "10.242.0.16/28", GatewayIPv4: "10.242.0.17", ContainerIPv4: "10.242.0.18",
			},
			NetworkPlanDigest:     second.Stage.NetworkPlanDigest,
			NetworkTopologyDigest: second.Stage.NetworkTopologyDigest,
		}
		forged.Digest, err = gatewayRebindProgressDigest(forged)
		if err != nil || !validGatewayRebindProgressRecord(forged) {
			t.Fatalf("forged record was not structurally self-consistent: %v", err)
		}
		state := gatewayUpgradeStateStore{directory: secondStore.directory}
		if err := state.writeExact(secondStore.path, secondStore.purpose, forged, false, maxGatewayRebindProgressBytes); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
			t.Fatal("rebind-aware scan accepted recomputed resource-plan substitution")
		}
	})

	t.Run("unknown malformed and case-folded namespace", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		name, _ := gatewayRebindProgressName(intent.Generation, intent.OperationID, 1)
		for _, candidate := range []string{
			"gateway-rebind-progress.v1.unknown.bundle",
			gatewayRebindProgressFilenamePrefix + "not-a-generation." + intent.OperationID + ".s01.bundle",
			"Gateway-rebind-progress.v1.g" + strings.TrimPrefix(name, gatewayRebindProgressFilenamePrefix),
		} {
			if err := os.WriteFile(filepath.Join(fixture.manager.store.root, candidate), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
				t.Fatalf("rebind-aware scanner accepted %q", candidate)
			}
			if err := os.Remove(filepath.Join(fixture.manager.store.root, candidate)); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("changed artifact during scan", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		checkpoint := func() {
			file, openErr := os.OpenFile(store.path, os.O_RDWR, 0)
			if openErr != nil {
				t.Fatal(openErr)
			}
			byteValue := []byte{0}
			if _, readErr := file.ReadAt(byteValue, 0); readErr != nil {
				_ = file.Close()
				t.Fatal(readErr)
			}
			byteValue[0] ^= 0xff
			if _, writeErr := file.WriteAt(byteValue, 0); writeErr != nil {
				_ = file.Close()
				t.Fatal(writeErr)
			}
			if closeErr := file.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(checkpoint); err == nil {
			t.Fatal("changed progress artifact was accepted")
		}
	})

	t.Run("same-content replacement during scan", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		originalInfo, err := os.Stat(store.path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(store.path)
		if err != nil {
			t.Fatal(err)
		}
		replacementPath := filepath.Join(fixture.manager.store.root, "progress-replacement.tmp")
		if err := os.WriteFile(replacementPath, body, 0o600); err != nil {
			t.Fatal(err)
		}
		replacementInfo, err := os.Stat(replacementPath)
		if err != nil {
			t.Fatal(err)
		}
		if os.SameFile(originalInfo, replacementInfo) {
			t.Fatal("replacement unexpectedly shares the original file identity")
		}
		checkpointCalls := 0
		checkpoint := func() {
			checkpointCalls++
			if err := os.Remove(store.path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacementPath, store.path); err != nil {
				t.Fatal(err)
			}
			installedInfo, err := os.Stat(store.path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(replacementInfo, installedInfo) {
				t.Fatal("renamed replacement lost its file identity")
			}
			if loaded, err := store.load(); err != nil || !reflect.DeepEqual(loaded, first) {
				t.Fatalf("same-content replacement failed exact protected readback: %#v %v", loaded, err)
			}
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(checkpoint); err == nil {
			t.Fatal("same-content replacement of a retained progress artifact was accepted")
		}
		if checkpointCalls != 1 {
			t.Fatalf("checkpoint calls=%d, want one", checkpointCalls)
		}
	})

	t.Run("generation overflow", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		if _, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, math.MaxUint64,
			uuid.NewString(), 1); err == nil {
			t.Fatal("maximum generation was accepted for progress storage")
		}
	})
}
