package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	gatewayRebindProgressVersion             = 1
	gatewayRebindProgressPurpose             = "hostd/generated-ingress/rebind/progress/v1"
	gatewayRebindProgressFilenamePrefix      = "gateway-rebind-progress.v1.g"
	gatewayRebindProgressSequenceDigits      = 2
	maxGatewayRebindProgressBytes            = 32 << 10
	maxGatewayRebindFinalConfigProgressBytes = 256 << 10
	gatewayRebindProgressMaximumSequence     = 18
)

type gatewayRebindProgressPhase string

const (
	gatewayRebindProgressSuccessorIntent     gatewayRebindProgressPhase = "successor_intent"
	gatewayRebindProgressStageIntent         gatewayRebindProgressPhase = "stage_intent"
	gatewayRebindProgressStageConfigIntent   gatewayRebindProgressPhase = "stage_config_intent"
	gatewayRebindProgressStageConfigCopied   gatewayRebindProgressPhase = "stage_config_copied"
	gatewayRebindProgressStageStartIntent    gatewayRebindProgressPhase = "stage_start_intent"
	gatewayRebindProgressStageServing        gatewayRebindProgressPhase = "stage_serving"
	gatewayRebindProgressFinalConfigIntent   gatewayRebindProgressPhase = "final_config_intent"
	gatewayRebindProgressFinalConfigCopied   gatewayRebindProgressPhase = "final_config_copied"
	gatewayRebindProgressFinalHandoverIntent gatewayRebindProgressPhase = "final_handover_intent"
	gatewayRebindProgressFinalContainerBound gatewayRebindProgressPhase = "final_container_bound"
	gatewayRebindProgressCutoverIntent       gatewayRebindProgressPhase = "cutover_intent"
	gatewayRebindProgressSuccessorServing    gatewayRebindProgressPhase = "successor_serving"
	gatewayRebindProgressHandoverCommitted   gatewayRebindProgressPhase = "handover_committed"
	gatewayRebindProgressRollbackIntent      gatewayRebindProgressPhase = "rollback_intent"
	gatewayRebindProgressHandoverRolledBack  gatewayRebindProgressPhase = "handover_rolled_back"
)

// gatewayRebindProgressRecord is immutable history for the initial rebind.
// The first two records prepare only. Later records bind exact observed effect
// results; no record authorizes an administrator, releases a fence, writes
// SQLite, or publishes a route. Every effect path must freshly authenticate
// the pinned Docker image and topology while it holds its required locks and
// lease.
type gatewayRebindProgressRecord struct {
	Version               int                        `json:"version"`
	Purpose               string                     `json:"purpose"`
	Generation            uint64                     `json:"generation"`
	OperationID           string                     `json:"operationId"`
	Sequence              uint64                     `json:"sequence"`
	Phase                 gatewayRebindProgressPhase `json:"phase"`
	OccurredAt            string                     `json:"occurredAt"`
	ProtectedIntentDigest string                     `json:"protectedIntentDigest"`
	PreviousDigest        string                     `json:"previousDigest,omitempty"`
	Stage                 *gatewayRebindStageIntent  `json:"stage,omitempty"`
	Digest                string                     `json:"digest"`
	// Handover is appended after Digest so records one through twelve retain
	// their exact canonical JSON bytes and digests. It is populated only by the
	// private final-handover state machine.
	Handover *gatewayRebindFinalHandoverProgress `json:"handover,omitempty"`
	// TypedEffect is the v2-source physical result. It is a distinct union arm
	// appended after every v1 field so all legacy canonical bytes remain exact.
	TypedEffect *gatewayRebindTypedEffectProgress `json:"typedEffect,omitempty"`
	// TypedRollback is written only after a typed forward prefix. Its intent
	// record precedes every compensation effect and its completion record binds
	// the exact restored predecessor and absent successor inventory.
	TypedRollback *gatewayRebindTypedRollbackProgress `json:"typedRollback,omitempty"`
}

type gatewayRebindTypedEffectProgress struct {
	Version             int                                         `json:"version"`
	Purpose             string                                      `json:"purpose"`
	ImageID             string                                      `json:"imageId"`
	StagePlanDigest     string                                      `json:"stagePlanDigest"`
	Network             *gatewayRebindStageNetworkBinding           `json:"network,omitempty"`
	ConfigVolume        *gatewayRebindStageConfigVolumeBinding      `json:"configVolume,omitempty"`
	DataVolume          *gatewayRebindStageDataVolumeBinding        `json:"dataVolume,omitempty"`
	StageContainer      *gatewayRebindStageContainerBinding         `json:"stageContainer,omitempty"`
	StageConfigIntent   *gatewayRebindStageConfigIntentBinding      `json:"stageConfigIntent,omitempty"`
	StageConfigCopy     *gatewayRebindStageConfigCopyBinding        `json:"stageConfigCopy,omitempty"`
	StageStartIntent    *gatewayRebindStageStartIntentBinding       `json:"stageStartIntent,omitempty"`
	StageServing        *gatewayRebindStageServingBinding           `json:"stageServing,omitempty"`
	FinalConfigIntent   *gatewayRebindTypedFinalConfigIntentBinding `json:"finalConfigIntent,omitempty"`
	FinalConfigCopy     *gatewayRebindFinalConfigCopyBinding        `json:"finalConfigCopy,omitempty"`
	HandoverIntent      *gatewayRebindTypedHandoverIntent           `json:"handoverIntent,omitempty"`
	ApplicationNetworks []gatewayRebindHandoverApplicationNetwork   `json:"applicationNetworks,omitempty"`
	FinalContainer      *gatewayRebindFinalContainerBinding         `json:"finalContainer,omitempty"`
	CutoverIntent       *gatewayRebindTypedCutoverIntent            `json:"cutoverIntent,omitempty"`
	SuccessorServing    *gatewayRebindFinalHandoverServingProof     `json:"successorServing,omitempty"`
	Resources           *gatewayRebindFinalHandoverResourceBindings `json:"resources,omitempty"`
	PhysicalProof       *gatewayRebindFinalHandoverTerminalProof    `json:"physicalProof,omitempty"`
	Digest              string                                      `json:"digest"`
}

type gatewayRebindTypedRollbackOwnedResources struct {
	ImageID             string                                    `json:"imageId"`
	IngressNetwork      *gatewayRebindStageNetworkBinding         `json:"ingressNetwork,omitempty"`
	ConfigVolume        *gatewayRebindStageConfigVolumeBinding    `json:"configVolume,omitempty"`
	DataVolume          *gatewayRebindStageDataVolumeBinding      `json:"dataVolume,omitempty"`
	StageContainer      *gatewayRebindStageContainerBinding       `json:"stageContainer,omitempty"`
	FinalContainer      *gatewayRebindFinalContainerBinding       `json:"finalContainer,omitempty"`
	ApplicationNetworks []gatewayRebindHandoverApplicationNetwork `json:"applicationNetworks,omitempty"`
}

type gatewayRebindTypedRollbackProgress struct {
	Version                 int                                      `json:"version"`
	Purpose                 string                                   `json:"purpose"`
	FromSequence            uint64                                   `json:"fromSequence"`
	FromPhase               gatewayRebindProgressPhase               `json:"fromPhase"`
	FromDigest              string                                   `json:"fromDigest"`
	PlanDigest              string                                   `json:"planDigest"`
	Owned                   gatewayRebindTypedRollbackOwnedResources `json:"owned"`
	PredecessorRoutesDigest string                                   `json:"predecessorRoutesDigest"`
	AdoptionProofDigest     string                                   `json:"adoptionProofDigest,omitempty"`
	PhysicalProof           *gatewayRebindFinalHandoverTerminalProof `json:"physicalProof,omitempty"`
	Digest                  string                                   `json:"digest"`
}

const (
	gatewayRebindTypedEffectVersion   = 1
	gatewayRebindTypedEffectPurpose   = "hostd/generated-ingress/rebind/typed-effect/v1"
	gatewayRebindTypedRollbackVersion = 1
	gatewayRebindTypedRollbackPurpose = "hostd/generated-ingress/rebind/typed-rollback/v1"
)

// gatewayRebindStageIntent keeps the approved Caddy content digest distinct
// from the observed Docker image object ID. The latter is merely a pinned
// observation in this storage slice: validation proves its syntax and binding,
// never its current Docker provenance.
type gatewayRebindStageIntent struct {
	Identity                 gatewayRebindSuccessorIdentity      `json:"identity"`
	ApprovedCaddyImageDigest string                              `json:"approvedCaddyImageDigest"`
	ObservedDockerImageID    string                              `json:"observedDockerImageId"`
	NetworkPlan              gatewayRebindSuccessorIntentNetwork `json:"networkPlan"`
	NetworkPlanDigest        string                              `json:"networkPlanDigest"`
	NetworkTopologyDigest    string                              `json:"networkTopologyDigest"`
	// Network is populated only after the exact successor ingress network has
	// been observed under the effect locks. Sequence two must keep it empty;
	// sequence three may bind it once and every replay must be exact.
	Network *gatewayRebindStageNetworkBinding `json:"network,omitempty"`
	// ConfigVolume is populated only after sequence three has bound and
	// reattested the successor network. Sequence four binds the exact Docker
	// volume identity once; it does not authorize a container or route effect.
	ConfigVolume *gatewayRebindStageConfigVolumeBinding `json:"configVolume,omitempty"`
	// DataVolume is populated only after sequence four has bound and
	// reattested the config volume. Sequence five binds the exact Docker
	// volume identity once; it does not authorize a container or route effect.
	DataVolume *gatewayRebindStageDataVolumeBinding `json:"dataVolume,omitempty"`
	// StageContainer is populated only after sequence five has bound and
	// reattested the data volume. Sequence six binds the exact stopped Docker
	// container once; it does not authorize start, config copy, or route effects.
	StageContainer *gatewayRebindStageContainerBinding `json:"stageContainer,omitempty"`
	// StageConfigIntent is populated only after sequence six has reattested the
	// exact stopped container and proved its config volume empty. Sequence seven
	// pins exact deterministic bytes and their destination before any config copy.
	StageConfigIntent *gatewayRebindStageConfigIntentBinding `json:"stageConfigIntent,omitempty"`
	// StageConfigCopy is populated only after sequence seven has been reattested
	// and the exact stage config has been observed in the stopped container. It
	// is a receipt for the copy or exact adoption; it authorizes no start or
	// route effect.
	StageConfigCopy *gatewayRebindStageConfigCopyBinding `json:"stageConfigCopy,omitempty"`
	// StageStartIntent is populated only after sequence eight has reattested
	// the exact copied config in the stopped successor. Sequence nine pins the
	// precise future start contract; it does not start the container, persist a
	// free-port observation, publish a route, or write SQLite. Keep this last so
	// the v1 JSON bytes and digests of sequences one through eight are stable.
	StageStartIntent *gatewayRebindStageStartIntentBinding `json:"stageStartIntent,omitempty"`
	// StageServing is populated only after sequence nine has been reattested and
	// the exact stage has been proved serving through both the selected host
	// address and its container endpoint. Keep this last so sequences one through
	// nine retain their canonical JSON bytes and digests.
	StageServing *gatewayRebindStageServingBinding `json:"stageServing,omitempty"`
	// FinalConfigIntent is populated only after sequence ten has freshly
	// reattested the serving probe-and-404 stage. Sequence eleven pins the
	// complete successor application configuration before any copy or reload.
	// Preserve this field position so sequences one through ten retain their
	// canonical bytes.
	FinalConfigIntent *gatewayRebindFinalConfigIntentBinding `json:"finalConfigIntent,omitempty"`
	// FinalConfigCopy is populated only after sequence eleven has been freshly
	// reattested and the exact final configuration has been observed alongside
	// the still-live stage configuration. It is a copy receipt and authorizes no
	// reload, route publication, application probe or database effect. Keep this
	// last so sequences one through eleven retain their canonical bytes.
	FinalConfigCopy *gatewayRebindFinalConfigCopyBinding `json:"finalConfigCopy,omitempty"`
}

type gatewayRebindStageNetworkBinding struct {
	ID              string `json:"id"`
	OwnershipDigest string `json:"ownershipDigest"`
}

type gatewayRebindStageConfigVolumeBinding struct {
	Name            string `json:"name"`
	Mountpoint      string `json:"mountpoint"`
	CreatedAt       string `json:"createdAt"`
	OwnershipDigest string `json:"ownershipDigest"`
}

type gatewayRebindStageDataVolumeBinding struct {
	Name            string `json:"name"`
	Mountpoint      string `json:"mountpoint"`
	CreatedAt       string `json:"createdAt"`
	OwnershipDigest string `json:"ownershipDigest"`
}

type gatewayRebindStageContainerBinding struct {
	ID                  string `json:"id"`
	OwnershipDigest     string `json:"ownershipDigest"`
	ConfigurationDigest string `json:"configurationDigest"`
}

type gatewayRebindStageConfigIntentBinding struct {
	ContentDigest              string                                `json:"contentDigest"`
	ContentLength              int64                                 `json:"contentLength"`
	Destination                string                                `json:"destination"`
	StageContainer             gatewayRebindStageContainerBinding    `json:"stageContainer"`
	ConfigVolume               gatewayRebindStageConfigVolumeBinding `json:"configVolume"`
	ProtectedIntentDigest      string                                `json:"protectedIntentDigest"`
	ProtectedPredecessorDigest string                                `json:"protectedPredecessorDigest"`
	PriorProgressDigest        string                                `json:"priorProgressDigest"`
}

type gatewayRebindStageConfigCopyBinding struct {
	StageConfigIntent   gatewayRebindStageConfigIntentBinding `json:"stageConfigIntent"`
	PriorProgressDigest string                                `json:"priorProgressDigest"`
}

// gatewayRebindStageStartIntentBinding deliberately records the complete
// future start contract, rather than a free-standing digest. Docker endpoint
// identity and runtime proof are unavailable while the stage is stopped and
// must be observed by the later start receipt.
type gatewayRebindStageStartIntentBinding struct {
	Version                    int                                 `json:"version"`
	StageConfigCopy            gatewayRebindStageConfigCopyBinding `json:"stageConfigCopy"`
	StageContainer             gatewayRebindStageContainerBinding  `json:"stageContainer"`
	Network                    gatewayRebindStageNetworkBinding    `json:"network"`
	InterfaceID                string                              `json:"interfaceId"`
	SelectedIPv4               string                              `json:"selectedIpv4"`
	PortStart                  uint16                              `json:"portStart"`
	PortEnd                    uint16                              `json:"portEnd"`
	ContainerIPv4              string                              `json:"containerIpv4"`
	ProbeToken                 string                              `json:"probeToken"`
	StartEffectDigest          string                              `json:"startEffectDigest"`
	ProtectedIntentDigest      string                              `json:"protectedIntentDigest"`
	ProtectedPredecessorDigest string                              `json:"protectedPredecessorDigest"`
	PriorProgressDigest        string                              `json:"priorProgressDigest"`
}

// gatewayRebindStageServingBinding records the unpredictable Docker endpoint
// identity together with deterministic runtime projections. Network and HTTP
// proofs are re-observed on every replay; the receipt is never permission to
// trust a stale listener.
type gatewayRebindStageServingBinding struct {
	Version                     int                                  `json:"version"`
	StageStartIntent            gatewayRebindStageStartIntentBinding `json:"stageStartIntent"`
	EndpointID                  string                               `json:"endpointId"`
	EffectivePortBindingsDigest string                               `json:"effectivePortBindingsDigest"`
	ConfigDigest                string                               `json:"configDigest"`
	ProtectedIntentDigest       string                               `json:"protectedIntentDigest"`
	PriorProgressDigest         string                               `json:"priorProgressDigest"`
}

// gatewayRebindStageIntentObservation is deliberately explicit so callers
// supply both the clock value and observed Docker identity. This slice does
// not generate time, IDs, or topology observations on its own.
type gatewayRebindStageIntentObservation struct {
	OccurredAt            time.Time
	ObservedDockerImageID string
	NetworkTopologyDigest string
}

type gatewayRebindProgressStore struct {
	directory   *stateStore
	dataRoot    string
	generation  uint64
	operationID string
	sequence    uint64
	path        string
	purpose     string
}

type gatewayRebindProgressSelection struct {
	Store      *gatewayRebindProgressStore
	Generation uint64
	Sequence   uint64
	Record     gatewayRebindProgressRecord
	Existing   bool
}

func newGatewayRebindSuccessorIntentProgress(intent gatewayRebindProtectedIntent,
	occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind successor progress input")
	}
	value := gatewayRebindProgressRecord{
		Version:    gatewayRebindProgressVersion,
		Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 1, Phase: gatewayRebindProgressSuccessorIntent,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	digest, err := gatewayRebindProgressDigest(value)
	if err != nil {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind successor progress input")
	}
	value.Digest = digest
	if !validGatewayRebindProgressRecord(value) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind successor progress input")
	}
	return value, nil
}

func newGatewayRebindSuccessorIntentProgressV2(intent gatewayRebindProtectedIntentV2,
	occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntentV2(intent) || !validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress typed rebind successor progress input")
	}
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 1, Phase: gatewayRebindProgressSuccessorIntent,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	digest, err := gatewayRebindProgressDigest(value)
	if err != nil {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress typed rebind successor progress input")
	}
	value.Digest = digest
	if !gatewayRebindProgressMatchesIntentV2(value, intent, nil) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress typed rebind successor progress input")
	}
	return value, nil
}

func newGatewayRebindTypedEffectProgressV2(intent gatewayRebindProtectedIntentV2,
	checkpoint gatewayRebindPredecessorCheckpoint, previous []gatewayRebindProgressRecord,
	effect gatewayRebindTypedEffectProgress, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	invalid := errors.New("invalid generated ingress typed effect progress input")
	if !validGatewayRebindProtectedIntentV2(intent) || !validGatewayRebindPredecessorCheckpoint(checkpoint) ||
		checkpoint.sourceRef() != intent.Predecessor || len(previous) == 0 || len(previous) >= 17 ||
		!validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, invalid
	}
	last := previous[len(previous)-1]
	lastAt, err := parseGatewayRebindProgressTime(last.OccurredAt)
	if err != nil || !occurredAt.After(lastAt) {
		return gatewayRebindProgressRecord{}, invalid
	}
	effect.Version, effect.Purpose, effect.Digest = gatewayRebindTypedEffectVersion, gatewayRebindTypedEffectPurpose, ""
	effect.Digest, err = gatewayRebindTypedEffectDigest(effect)
	if err != nil {
		return gatewayRebindProgressRecord{}, invalid
	}
	sequence := uint64(len(previous) + 1)
	phase, ok := gatewayRebindTypedEffectPhase(sequence)
	if !ok {
		return gatewayRebindProgressRecord{}, invalid
	}
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: sequence, Phase: phase, OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano),
		ProtectedIntentDigest: intent.Digest, PreviousDigest: last.Digest, TypedEffect: &effect,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	selections := make([]gatewayRebindProgressSelection, len(previous))
	for index := range previous {
		selections[index] = gatewayRebindProgressSelection{Generation: previous[index].Generation,
			Sequence: previous[index].Sequence, Record: previous[index], Existing: true}
	}
	if err != nil || !gatewayRebindProgressMatchesIntentV2(value, intent, selections) ||
		!gatewayRebindTypedProgressMatchesCheckpoint(intent, checkpoint, append(previous, value)) {
		return gatewayRebindProgressRecord{}, invalid
	}
	return value, nil
}

func gatewayRebindTypedEffectDigest(value gatewayRebindTypedEffectProgress) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayRebindTypedRollbackDigest(value gatewayRebindTypedRollbackProgress) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayRebindTypedRollbackPlanDigest(intent gatewayRebindProtectedIntentV2,
	value gatewayRebindTypedRollbackProgress,
) (string, error) {
	return canonicalDigest(struct {
		Purpose                 string                                   `json:"purpose"`
		ProtectedIntentDigest   string                                   `json:"protectedIntentDigest"`
		Predecessor             appaccess.GatewayRebindSourceRef         `json:"predecessor"`
		FromSequence            uint64                                   `json:"fromSequence"`
		FromPhase               gatewayRebindProgressPhase               `json:"fromPhase"`
		FromDigest              string                                   `json:"fromDigest"`
		Owned                   gatewayRebindTypedRollbackOwnedResources `json:"owned"`
		PredecessorRoutesDigest string                                   `json:"predecessorRoutesDigest"`
		AdoptionProofDigest     string                                   `json:"adoptionProofDigest,omitempty"`
	}{"hostd/generated-ingress/rebind/typed-rollback-plan/v1", intent.Digest, intent.Predecessor,
		value.FromSequence, value.FromPhase, value.FromDigest, value.Owned,
		value.PredecessorRoutesDigest, value.AdoptionProofDigest})
}

func gatewayRebindTypedRollbackOwnedFromEffect(value gatewayRebindTypedEffectProgress) gatewayRebindTypedRollbackOwnedResources {
	result := gatewayRebindTypedRollbackOwnedResources{ImageID: value.ImageID}
	if value.Network != nil {
		copy := *value.Network
		result.IngressNetwork = &copy
	}
	if value.ConfigVolume != nil {
		copy := *value.ConfigVolume
		result.ConfigVolume = &copy
	}
	if value.DataVolume != nil {
		copy := *value.DataVolume
		result.DataVolume = &copy
	}
	if value.StageContainer != nil {
		copy := *value.StageContainer
		result.StageContainer = &copy
	}
	if value.FinalContainer != nil {
		copy := *value.FinalContainer
		result.FinalContainer = &copy
	}
	result.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), value.ApplicationNetworks...)
	return result
}

func validGatewayRebindTypedRollbackOwned(value gatewayRebindTypedRollbackOwnedResources,
	identity gatewayRebindSuccessorIdentity,
) bool {
	return validGatewayRebindTypedRollbackOwnedShape(value) && validGatewayRebindSuccessorIdentityValue(identity) &&
		(value.ConfigVolume == nil || value.ConfigVolume.Name == identity.ConfigVolume) &&
		(value.DataVolume == nil || value.DataVolume.Name == identity.DataVolume)
}

func validGatewayRebindTypedRollbackOwnedShape(value gatewayRebindTypedRollbackOwnedResources) bool {
	if value.ImageID == "" {
		return value.IngressNetwork == nil && value.ConfigVolume == nil && value.DataVolume == nil &&
			value.StageContainer == nil && value.FinalContainer == nil && len(value.ApplicationNetworks) == 0
	}
	if !validSHA256(value.ImageID) ||
		value.ConfigVolume != nil && value.IngressNetwork == nil ||
		value.DataVolume != nil && value.ConfigVolume == nil ||
		value.StageContainer != nil && value.DataVolume == nil ||
		value.FinalContainer != nil && value.StageContainer == nil {
		return false
	}
	if value.IngressNetwork != nil && (!validContainerID(value.IngressNetwork.ID) ||
		normalizeID(value.IngressNetwork.ID) != value.IngressNetwork.ID || !validSHA256(value.IngressNetwork.OwnershipDigest)) {
		return false
	}
	if value.ConfigVolume != nil && !validGatewayRebindStageConfigVolumeBindingValue(*value.ConfigVolume) {
		return false
	}
	if value.DataVolume != nil && !validGatewayRebindStageDataVolumeBindingValue(*value.DataVolume) {
		return false
	}
	if value.StageContainer != nil && !validGatewayRebindStageContainerBindingValue(*value.StageContainer) {
		return false
	}
	if value.FinalContainer != nil && (!validGatewayRebindFinalContainerBindingValue(*value.FinalContainer) ||
		value.StageContainer == nil || value.FinalContainer.ID == value.StageContainer.ID) {
		return false
	}
	return validGatewayRebindFinalHandoverApplicationNetworkValues(value.ApplicationNetworks)
}

func validGatewayRebindTypedRollbackProgress(value gatewayRebindTypedRollbackProgress) bool {
	expectedPhase, phaseOK := gatewayRebindTypedForwardPhase(value.FromSequence)
	if value.Version != gatewayRebindTypedRollbackVersion || value.Purpose != gatewayRebindTypedRollbackPurpose ||
		value.FromSequence < 1 || value.FromSequence > 16 || !phaseOK || value.FromPhase != expectedPhase ||
		!validSHA256(value.FromDigest) || !validSHA256(value.PlanDigest) ||
		!validGatewayRebindTypedRollbackOwnedShape(value.Owned) || !validSHA256(value.PredecessorRoutesDigest) ||
		(value.AdoptionProofDigest != "" && !validSHA256(value.AdoptionProofDigest)) || !validSHA256(value.Digest) {
		return false
	}
	if value.PhysicalProof != nil && !validGatewayRebindFinalHandoverOutcomeValue(*value.PhysicalProof) {
		return false
	}
	digest, err := gatewayRebindTypedRollbackDigest(value)
	return err == nil && digest == value.Digest
}

func gatewayRebindTypedRollbackOwnedContains(bound, planned gatewayRebindTypedRollbackOwnedResources,
	fromSequence uint64,
) bool {
	if bound.ImageID != planned.ImageID || bound.IngressNetwork != nil && !reflect.DeepEqual(bound.IngressNetwork, planned.IngressNetwork) ||
		bound.ConfigVolume != nil && !reflect.DeepEqual(bound.ConfigVolume, planned.ConfigVolume) ||
		bound.DataVolume != nil && !reflect.DeepEqual(bound.DataVolume, planned.DataVolume) ||
		bound.StageContainer != nil && !reflect.DeepEqual(bound.StageContainer, planned.StageContainer) ||
		bound.FinalContainer != nil && !reflect.DeepEqual(bound.FinalContainer, planned.FinalContainer) ||
		!reflect.DeepEqual(bound.ApplicationNetworks, planned.ApplicationNetworks) {
		return false
	}
	if reflect.DeepEqual(bound, planned) {
		return true
	}
	// Only the one effect authorized by the immediately retained creation
	// intent may be adopted after a lost acknowledgment. Future resources and
	// foreign extras remain fenced even when their individual hashes are valid.
	projection := planned
	switch fromSequence {
	case 2:
		if bound.IngressNetwork != nil || planned.IngressNetwork == nil {
			return false
		}
		projection.IngressNetwork = nil
	case 3:
		if bound.ConfigVolume != nil || planned.ConfigVolume == nil {
			return false
		}
		projection.ConfigVolume = nil
	case 4:
		if bound.DataVolume != nil || planned.DataVolume == nil {
			return false
		}
		projection.DataVolume = nil
	case 5:
		if bound.StageContainer != nil || planned.StageContainer == nil {
			return false
		}
		projection.StageContainer = nil
	case 13:
		if bound.FinalContainer != nil || planned.FinalContainer == nil {
			return false
		}
		projection.FinalContainer = nil
	default:
		return false
	}
	return reflect.DeepEqual(bound, projection)
}

func validGatewayRebindTypedRollbackPhysicalProof(value gatewayRebindFinalHandoverTerminalProof,
	rollback gatewayRebindTypedRollbackProgress, priorProgressDigest string,
) bool {
	observation := value.Observation
	return validGatewayRebindFinalHandoverOutcomeValue(value) && value.Kind == gatewayRebindFinalHandoverOutcomeAbort &&
		value.PriorProgressDigest == priorProgressDigest && observation.Stage == gatewayRebindHandoverContainerAbsent &&
		observation.Final == gatewayRebindHandoverContainerAbsent && observation.FinalID == "" &&
		observation.PredecessorRunning && observation.PredecessorAddress == gatewayRebindPredecessorAddressPresent &&
		!observation.ConfigVolumePresent && !observation.DataVolumePresent && !observation.IngressNetworkPresent &&
		len(observation.ApplicationNetworks) == 0 && observation.ConfigDigest == "" && observation.RoutesDigest == "" &&
		observation.PredecessorRoutesDigest == rollback.PredecessorRoutesDigest && observation.PredecessorStopDigest == ""
}

func newGatewayRebindTypedRollbackIntentV2(intent gatewayRebindProtectedIntentV2,
	previous []gatewayRebindProgressRecord, owned gatewayRebindTypedRollbackOwnedResources,
	predecessorRoutesDigest, adoptionProofDigest string, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	invalid := errors.New("invalid generated ingress typed rollback intent")
	if !validGatewayRebindProtectedIntentV2(intent) || len(previous) < 1 || len(previous) >= 17 ||
		!validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, invalid
	}
	last := previous[len(previous)-1]
	if last.TypedRollback != nil || last.Sequence < 1 || last.Sequence > 16 ||
		(last.Sequence == 1) != (last.TypedEffect == nil) ||
		!validGatewayRebindTypedRollbackOwned(owned, intent.Identity) || !validSHA256(predecessorRoutesDigest) {
		return gatewayRebindProgressRecord{}, invalid
	}
	bound := gatewayRebindTypedRollbackOwnedResources{}
	if last.TypedEffect != nil {
		bound = gatewayRebindTypedRollbackOwnedFromEffect(*last.TypedEffect)
	}
	if !gatewayRebindTypedRollbackOwnedContains(bound, owned, last.Sequence) ||
		(!reflect.DeepEqual(bound, owned) && !validSHA256(adoptionProofDigest)) ||
		(reflect.DeepEqual(bound, owned) && adoptionProofDigest != "") {
		return gatewayRebindProgressRecord{}, invalid
	}
	lastAt, err := parseGatewayRebindProgressTime(last.OccurredAt)
	if err != nil || !occurredAt.After(lastAt) {
		return gatewayRebindProgressRecord{}, invalid
	}
	rollback := gatewayRebindTypedRollbackProgress{
		Version: gatewayRebindTypedRollbackVersion, Purpose: gatewayRebindTypedRollbackPurpose,
		FromSequence: last.Sequence, FromPhase: last.Phase, FromDigest: last.Digest,
		Owned: owned, PredecessorRoutesDigest: predecessorRoutesDigest, AdoptionProofDigest: adoptionProofDigest,
	}
	rollback.PlanDigest, err = gatewayRebindTypedRollbackPlanDigest(intent, rollback)
	if err != nil {
		return gatewayRebindProgressRecord{}, invalid
	}
	rollback.Digest, err = gatewayRebindTypedRollbackDigest(rollback)
	if err != nil {
		return gatewayRebindProgressRecord{}, invalid
	}
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: last.Sequence + 1, Phase: gatewayRebindProgressRollbackIntent,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: last.Digest, TypedRollback: &rollback,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	selections := make([]gatewayRebindProgressSelection, len(previous))
	for index := range previous {
		selections[index] = gatewayRebindProgressSelection{Generation: previous[index].Generation,
			Sequence: previous[index].Sequence, Record: previous[index], Existing: true}
	}
	if err != nil || !gatewayRebindProgressMatchesIntentV2(value, intent, selections) {
		return gatewayRebindProgressRecord{}, invalid
	}
	return value, nil
}

func newGatewayRebindTypedRollbackCompleteV2(intent gatewayRebindProtectedIntentV2,
	previous []gatewayRebindProgressRecord, proof gatewayRebindFinalHandoverTerminalProof, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	invalid := errors.New("invalid generated ingress typed rollback completion")
	if !validGatewayRebindProtectedIntentV2(intent) || len(previous) < 2 || len(previous) >= 18 ||
		!validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, invalid
	}
	last := previous[len(previous)-1]
	if last.Phase != gatewayRebindProgressRollbackIntent || last.TypedRollback == nil || last.TypedRollback.PhysicalProof != nil {
		return gatewayRebindProgressRecord{}, invalid
	}
	lastAt, err := parseGatewayRebindProgressTime(last.OccurredAt)
	rollback := *last.TypedRollback
	if err != nil || !occurredAt.After(lastAt) || !validGatewayRebindTypedRollbackPhysicalProof(proof, rollback, last.Digest) {
		return gatewayRebindProgressRecord{}, invalid
	}
	proofCopy := proof
	rollback.PhysicalProof = &proofCopy
	rollback.Digest, err = gatewayRebindTypedRollbackDigest(rollback)
	if err != nil {
		return gatewayRebindProgressRecord{}, invalid
	}
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: last.Sequence + 1, Phase: gatewayRebindProgressHandoverRolledBack,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: last.Digest, TypedRollback: &rollback,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	selections := make([]gatewayRebindProgressSelection, len(previous))
	for index := range previous {
		selections[index] = gatewayRebindProgressSelection{Generation: previous[index].Generation,
			Sequence: previous[index].Sequence, Record: previous[index], Existing: true}
	}
	if err != nil || !gatewayRebindProgressMatchesIntentV2(value, intent, selections) {
		return gatewayRebindProgressRecord{}, invalid
	}
	return value, nil
}

func gatewayRebindTypedStagePlanDigest(intent gatewayRebindProtectedIntentV2) (string, error) {
	return canonicalDigest(struct {
		Purpose     string                              `json:"purpose"`
		Intent      string                              `json:"protectedIntentDigest"`
		Checkpoint  string                              `json:"predecessorCheckpointDigest"`
		Source      string                              `json:"sourceStateDigest"`
		Identity    gatewayRebindSuccessorIdentity      `json:"identity"`
		Network     gatewayRebindSuccessorIntentNetwork `json:"network"`
		NetworkHash string                              `json:"networkDigest"`
	}{"hostd/generated-ingress/rebind/typed-stage-plan/v1", intent.Digest,
		intent.Predecessor.PredecessorCheckpointDigest, intent.Predecessor.SourceStateDigest,
		intent.Identity, intent.Network, intent.NetworkDigest})
}

func gatewayRebindTypedEffectPhase(sequence uint64) (gatewayRebindProgressPhase, bool) {
	phases := [...]gatewayRebindProgressPhase{
		gatewayRebindProgressStageIntent, gatewayRebindProgressStageIntent,
		gatewayRebindProgressStageIntent, gatewayRebindProgressStageIntent, gatewayRebindProgressStageIntent,
		gatewayRebindProgressStageConfigIntent, gatewayRebindProgressStageConfigCopied,
		gatewayRebindProgressStageStartIntent, gatewayRebindProgressStageServing,
		gatewayRebindProgressFinalConfigIntent, gatewayRebindProgressFinalConfigCopied,
		gatewayRebindProgressFinalHandoverIntent, gatewayRebindProgressFinalContainerBound,
		gatewayRebindProgressCutoverIntent, gatewayRebindProgressSuccessorServing,
		gatewayRebindProgressHandoverCommitted,
	}
	if sequence < 2 || sequence > 17 {
		return "", false
	}
	return phases[sequence-2], true
}

func gatewayRebindTypedForwardPhase(sequence uint64) (gatewayRebindProgressPhase, bool) {
	if sequence == 1 {
		return gatewayRebindProgressSuccessorIntent, true
	}
	return gatewayRebindTypedEffectPhase(sequence)
}

func gatewayRebindTypedPhysicalOutcomeMatches(identity gatewayRebindSuccessorIdentity, previousDigest string,
	resources gatewayRebindFinalHandoverResourceBindings, proof gatewayRebindFinalHandoverTerminalProof,
) bool {
	if !validGatewayRebindSuccessorIdentityValue(identity) || !validSHA256(previousDigest) ||
		!validGatewayRebindFinalHandoverResourceBindingsValue(resources) ||
		!validGatewayRebindFinalHandoverOutcomeValue(proof) || proof.Kind != gatewayRebindFinalHandoverOutcomeCommit ||
		proof.PriorProgressDigest != previousDigest || resources.ConfigVolume.Name != identity.ConfigVolume ||
		resources.DataVolume.Name != identity.DataVolume || resources.FinalContainer == nil ||
		proof.Observation.Stage != gatewayRebindHandoverContainerAbsent ||
		proof.Observation.Final != gatewayRebindHandoverContainerRunning ||
		proof.Observation.FinalID != resources.FinalContainer.ID || proof.Observation.PredecessorRunning ||
		!proof.Observation.ConfigVolumePresent || !proof.Observation.DataVolumePresent ||
		!proof.Observation.IngressNetworkPresent || !validSHA256(proof.Observation.ConfigDigest) ||
		!validSHA256(proof.Observation.RoutesDigest) || !validSHA256(proof.Observation.PredecessorStopDigest) ||
		!reflect.DeepEqual(proof.Observation.ApplicationNetworks, resources.ApplicationNetworks) {
		return false
	}
	return true
}

func newGatewayRebindStageIntentProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, observation gatewayRebindStageIntentObservation,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) ||
		!validGatewayRebindProgressRecord(previous) || previous.Generation != intent.Generation ||
		previous.OperationID != intent.OperationID || previous.Sequence != 1 ||
		previous.Phase != gatewayRebindProgressSuccessorIntent || previous.ProtectedIntentDigest != intent.Digest ||
		previous.PreviousDigest != "" || previous.Stage != nil || !validGatewayRebindProgressTime(observation.OccurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage progress input")
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !observation.OccurredAt.After(previousAt) || !validSHA256(observation.ObservedDockerImageID) ||
		!validSHA256(observation.NetworkTopologyDigest) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage progress input")
	}
	stage := gatewayRebindStageIntent{
		Identity:                 intent.Intent.Identity,
		ApprovedCaddyImageDigest: intent.Intent.Identity.CaddyImageDigest,
		ObservedDockerImageID:    observation.ObservedDockerImageID,
		NetworkPlan:              intent.Intent.Network, NetworkPlanDigest: intent.Intent.NetworkDigest,
		NetworkTopologyDigest: observation.NetworkTopologyDigest,
	}
	value := gatewayRebindProgressRecord{
		Version:    gatewayRebindProgressVersion,
		Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 2, Phase: gatewayRebindProgressStageIntent,
		OccurredAt: observation.OccurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: previous.Digest, Stage: &stage,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	digest, err := gatewayRebindProgressDigest(value)
	if err != nil {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage progress input")
	}
	value.Digest = digest
	if !validGatewayRebindProgressRecord(value) || !gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage progress input")
	}
	return value, nil
}

func newGatewayRebindStageNetworkProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, networkID, ownershipDigest string, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Generation != intent.Generation || previous.OperationID != intent.OperationID ||
		previous.Sequence != 2 || previous.Phase != gatewayRebindProgressStageIntent ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil ||
		previous.Stage.Network != nil || !gatewayRebindStageIntentMatchesProtectedIntent(previous, intent) ||
		!validContainerID(networkID) || normalizeID(networkID) != networkID || !validSHA256(ownershipDigest) ||
		!validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage network progress input")
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage network progress input")
	}
	stage := *previous.Stage
	stage.Network = &gatewayRebindStageNetworkBinding{ID: networkID, OwnershipDigest: ownershipDigest}
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 3, Phase: gatewayRebindProgressStageIntent,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: previous.Digest, Stage: &stage,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	if err != nil || !validGatewayRebindProgressRecord(value) ||
		!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage network progress input")
	}
	return value, nil
}

func newGatewayRebindStageConfigVolumeProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, binding gatewayRebindStageConfigVolumeBinding, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Generation != intent.Generation || previous.OperationID != intent.OperationID ||
		previous.Sequence != 3 || previous.Phase != gatewayRebindProgressStageIntent ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil || previous.Stage.Network == nil ||
		previous.Stage.ConfigVolume != nil || !gatewayRebindStageIntentMatchesProtectedIntent(previous, intent) ||
		!validGatewayRebindStageConfigVolumeBinding(intent, binding) || !validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage config volume progress input")
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage config volume progress input")
	}
	stage := *previous.Stage
	stage.ConfigVolume = &binding
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 4, Phase: gatewayRebindProgressStageIntent,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: previous.Digest, Stage: &stage,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	if err != nil || !validGatewayRebindProgressRecord(value) ||
		!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage config volume progress input")
	}
	return value, nil
}

func newGatewayRebindStageDataVolumeProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, binding gatewayRebindStageDataVolumeBinding, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Generation != intent.Generation || previous.OperationID != intent.OperationID ||
		previous.Sequence != 4 || previous.Phase != gatewayRebindProgressStageIntent ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil || previous.Stage.Network == nil ||
		previous.Stage.ConfigVolume == nil || previous.Stage.DataVolume != nil ||
		!gatewayRebindStageIntentMatchesProtectedIntent(previous, intent) ||
		!validGatewayRebindStageDataVolumeBinding(intent, binding) || !validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage data volume progress input")
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage data volume progress input")
	}
	stage := *previous.Stage
	stage.DataVolume = &binding
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 5, Phase: gatewayRebindProgressStageIntent,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: previous.Digest, Stage: &stage,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	if err != nil || !validGatewayRebindProgressRecord(value) ||
		!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage data volume progress input")
	}
	return value, nil
}

func newGatewayRebindStageContainerProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, binding gatewayRebindStageContainerBinding, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	configurationDigest := ""
	if previous.Stage != nil {
		configurationDigest, _ = gatewayRebindStageContainerConfigurationDigest(intent, *previous.Stage)
	}
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Generation != intent.Generation || previous.OperationID != intent.OperationID ||
		previous.Sequence != 5 || previous.Phase != gatewayRebindProgressStageIntent ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil || previous.Stage.Network == nil ||
		previous.Stage.ConfigVolume == nil || previous.Stage.DataVolume == nil || previous.Stage.StageContainer != nil ||
		!gatewayRebindStageIntentMatchesProtectedIntent(previous, intent) || binding.ConfigurationDigest != configurationDigest ||
		!validGatewayRebindStageContainerBinding(intent, binding) || !validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage container progress input")
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage container progress input")
	}
	stage := *previous.Stage
	stage.StageContainer = &binding
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 6, Phase: gatewayRebindProgressStageIntent,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: previous.Digest, Stage: &stage,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	if err != nil || !validGatewayRebindProgressRecord(value) ||
		!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage container progress input")
	}
	return value, nil
}

func newGatewayRebindStageConfigIntentProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, binding gatewayRebindStageConfigIntentBinding, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Generation != intent.Generation || previous.OperationID != intent.OperationID ||
		previous.Sequence != 6 || previous.Phase != gatewayRebindProgressStageIntent ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil || previous.Stage.Network == nil ||
		previous.Stage.ConfigVolume == nil || previous.Stage.DataVolume == nil || previous.Stage.StageContainer == nil ||
		previous.Stage.StageConfigIntent != nil || !gatewayRebindStageIntentMatchesProtectedIntent(previous, intent) ||
		!validGatewayRebindStageConfigIntentBinding(intent, previous, binding) ||
		!validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage config intent progress input")
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage config intent progress input")
	}
	stage := *previous.Stage
	stage.StageConfigIntent = &binding
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 7, Phase: gatewayRebindProgressStageConfigIntent,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: previous.Digest, Stage: &stage,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	if err != nil || !validGatewayRebindProgressRecord(value) ||
		!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage config intent progress input")
	}
	return value, nil
}

func newGatewayRebindStageConfigCopyProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, binding gatewayRebindStageConfigCopyBinding, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Generation != intent.Generation || previous.OperationID != intent.OperationID ||
		previous.Sequence != 7 || previous.Phase != gatewayRebindProgressStageConfigIntent ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil || previous.Stage.Network == nil ||
		previous.Stage.ConfigVolume == nil || previous.Stage.DataVolume == nil || previous.Stage.StageContainer == nil ||
		previous.Stage.StageConfigIntent == nil || previous.Stage.StageConfigCopy != nil ||
		!gatewayRebindStageIntentMatchesProtectedIntent(previous, intent) ||
		!validGatewayRebindStageConfigCopyBinding(intent, previous, binding) ||
		!validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage config copy progress input")
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage config copy progress input")
	}
	stage := *previous.Stage
	stage.StageConfigCopy = &binding
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 8, Phase: gatewayRebindProgressStageConfigCopied,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: previous.Digest, Stage: &stage,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	if err != nil || !validGatewayRebindProgressRecord(value) ||
		!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage config copy progress input")
	}
	return value, nil
}

func newGatewayRebindStageStartIntentProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, binding gatewayRebindStageStartIntentBinding, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Generation != intent.Generation || previous.OperationID != intent.OperationID ||
		previous.Sequence != 8 || previous.Phase != gatewayRebindProgressStageConfigCopied ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil || previous.Stage.Network == nil ||
		previous.Stage.ConfigVolume == nil || previous.Stage.DataVolume == nil || previous.Stage.StageContainer == nil ||
		previous.Stage.StageConfigIntent == nil || previous.Stage.StageConfigCopy == nil ||
		previous.Stage.StageStartIntent != nil || !gatewayRebindStageIntentMatchesProtectedIntent(previous, intent) ||
		!validGatewayRebindStageStartIntentBinding(intent, previous, binding) ||
		!validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage start intent progress input")
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage start intent progress input")
	}
	stage := *previous.Stage
	stage.StageStartIntent = &binding
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 9, Phase: gatewayRebindProgressStageStartIntent,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: previous.Digest, Stage: &stage,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	if err != nil || !validGatewayRebindProgressRecord(value) ||
		!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage start intent progress input")
	}
	return value, nil
}

func newGatewayRebindStageServingProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, binding gatewayRebindStageServingBinding, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Generation != intent.Generation || previous.OperationID != intent.OperationID ||
		previous.Sequence != 9 || previous.Phase != gatewayRebindProgressStageStartIntent ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil || previous.Stage.Network == nil ||
		previous.Stage.ConfigVolume == nil || previous.Stage.DataVolume == nil || previous.Stage.StageContainer == nil ||
		previous.Stage.StageConfigIntent == nil || previous.Stage.StageConfigCopy == nil ||
		previous.Stage.StageStartIntent == nil || previous.Stage.StageServing != nil ||
		!gatewayRebindStageIntentMatchesProtectedIntent(previous, intent) ||
		!validGatewayRebindStageServingBinding(intent, previous, binding) ||
		!validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage serving progress input")
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage serving progress input")
	}
	stage := *previous.Stage
	stage.StageServing = &binding
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 10, Phase: gatewayRebindProgressStageServing,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: previous.Digest, Stage: &stage,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	if err != nil || !validGatewayRebindProgressRecord(value) ||
		!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind stage serving progress input")
	}
	return value, nil
}

func newGatewayRebindFinalConfigIntentProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, binding gatewayRebindFinalConfigIntentBinding, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Generation != intent.Generation || previous.OperationID != intent.OperationID ||
		previous.Sequence != 10 || previous.Phase != gatewayRebindProgressStageServing ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil ||
		previous.Stage.StageServing == nil || previous.Stage.FinalConfigIntent != nil ||
		!gatewayRebindStageIntentMatchesProtectedIntent(previous, intent) ||
		!validGatewayRebindFinalConfigIntentBinding(intent, previous, binding) ||
		!validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind final config intent progress input")
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind final config intent progress input")
	}
	stage := *previous.Stage
	stage.FinalConfigIntent = &binding
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 11, Phase: gatewayRebindProgressFinalConfigIntent,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: previous.Digest, Stage: &stage,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	if err != nil || !validGatewayRebindProgressRecord(value) ||
		!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind final config intent progress input")
	}
	return value, nil
}

func newGatewayRebindFinalConfigCopyProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, binding gatewayRebindFinalConfigCopyBinding, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Generation != intent.Generation || previous.OperationID != intent.OperationID ||
		previous.Sequence != 11 || previous.Phase != gatewayRebindProgressFinalConfigIntent ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil ||
		previous.Stage.StageServing == nil || previous.Stage.FinalConfigIntent == nil ||
		previous.Stage.FinalConfigCopy != nil ||
		!gatewayRebindStageIntentMatchesProtectedIntent(previous, intent) ||
		!validGatewayRebindFinalConfigCopyBinding(intent, previous, binding) ||
		!validGatewayRebindProgressTime(occurredAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind final config copy progress input")
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind final config copy progress input")
	}
	stage := *previous.Stage
	stage.FinalConfigCopy = &binding
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: 12, Phase: gatewayRebindProgressFinalConfigCopied,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), ProtectedIntentDigest: intent.Digest,
		PreviousDigest: previous.Digest, Stage: &stage,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	if err != nil || !validGatewayRebindProgressRecord(value) ||
		!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind final config copy progress input")
	}
	return value, nil
}

func gatewayRebindProgressDigest(value gatewayRebindProgressRecord) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func validGatewayRebindProgressRecord(value gatewayRebindProgressRecord) bool {
	if value.Version != gatewayRebindProgressVersion || value.Generation == 0 || value.Generation == math.MaxUint64 ||
		!validCanonicalUUID(value.OperationID) || value.Sequence == 0 || value.Sequence > gatewayRebindProgressMaximumSequence ||
		!validSHA256(value.ProtectedIntentDigest) || !validSHA256(value.Digest) ||
		(value.Sequence <= 12 && value.Handover != nil) {
		return false
	}
	_, expectedPurpose := gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	if value.Purpose != expectedPurpose {
		return false
	}
	if _, err := parseGatewayRebindProgressTime(value.OccurredAt); err != nil {
		return false
	}
	if value.TypedEffect != nil {
		expectedPhase, ok := gatewayRebindTypedEffectPhase(value.Sequence)
		if !ok || value.Phase != expectedPhase || !validSHA256(value.PreviousDigest) ||
			value.Stage != nil || value.Handover != nil || value.TypedRollback != nil ||
			!validGatewayRebindTypedEffectProgress(*value.TypedEffect, value.Sequence) {
			return false
		}
		digest, err := gatewayRebindProgressDigest(value)
		return err == nil && digest == value.Digest
	}
	if value.TypedRollback != nil {
		if (value.Phase != gatewayRebindProgressRollbackIntent && value.Phase != gatewayRebindProgressHandoverRolledBack) ||
			!validSHA256(value.PreviousDigest) || value.Stage != nil || value.Handover != nil ||
			!validGatewayRebindTypedRollbackProgress(*value.TypedRollback) ||
			(value.Phase == gatewayRebindProgressRollbackIntent) != (value.TypedRollback.PhysicalProof == nil) {
			return false
		}
		digest, err := gatewayRebindProgressDigest(value)
		return err == nil && digest == value.Digest
	}
	switch value.Phase {
	case gatewayRebindProgressSuccessorIntent:
		if value.Sequence != 1 || value.PreviousDigest != "" || value.Stage != nil {
			return false
		}
	case gatewayRebindProgressStageIntent:
		if (value.Sequence != 2 && value.Sequence != 3 && value.Sequence != 4 && value.Sequence != 5 && value.Sequence != 6) || !validSHA256(value.PreviousDigest) || value.Stage == nil ||
			!validGatewayRebindStageIntent(*value.Stage) {
			return false
		}
	case gatewayRebindProgressStageConfigIntent:
		if value.Sequence != 7 || !validSHA256(value.PreviousDigest) || value.Stage == nil ||
			!validGatewayRebindStageIntent(*value.Stage) {
			return false
		}
	case gatewayRebindProgressStageConfigCopied:
		if value.Sequence != 8 || !validSHA256(value.PreviousDigest) || value.Stage == nil ||
			!validGatewayRebindStageIntent(*value.Stage) {
			return false
		}
	case gatewayRebindProgressStageStartIntent:
		if value.Sequence != 9 || !validSHA256(value.PreviousDigest) || value.Stage == nil ||
			!validGatewayRebindStageIntent(*value.Stage) {
			return false
		}
	case gatewayRebindProgressStageServing:
		if value.Sequence != 10 || !validSHA256(value.PreviousDigest) || value.Stage == nil ||
			!validGatewayRebindStageIntent(*value.Stage) {
			return false
		}
	case gatewayRebindProgressFinalConfigIntent:
		if value.Sequence != 11 || !validSHA256(value.PreviousDigest) || value.Stage == nil ||
			!validGatewayRebindStageIntent(*value.Stage) {
			return false
		}
	case gatewayRebindProgressFinalConfigCopied:
		if value.Sequence != 12 || !validSHA256(value.PreviousDigest) || value.Stage == nil ||
			!validGatewayRebindStageIntent(*value.Stage) {
			return false
		}
	case gatewayRebindProgressFinalHandoverIntent, gatewayRebindProgressFinalContainerBound,
		gatewayRebindProgressCutoverIntent, gatewayRebindProgressSuccessorServing,
		gatewayRebindProgressHandoverCommitted, gatewayRebindProgressRollbackIntent,
		gatewayRebindProgressHandoverRolledBack:
		if value.Sequence < 13 || value.Sequence > gatewayRebindProgressMaximumSequence ||
			!validSHA256(value.PreviousDigest) || value.Stage == nil ||
			!validGatewayRebindStageIntent(*value.Stage) || value.Handover == nil ||
			!validGatewayRebindFinalHandoverProgressValue(*value.Handover) {
			return false
		}
	default:
		return false
	}
	digest, err := gatewayRebindProgressDigest(value)
	return err == nil && digest == value.Digest
}

func validGatewayRebindTypedEffectProgress(value gatewayRebindTypedEffectProgress, sequence uint64) bool {
	if value.Version != gatewayRebindTypedEffectVersion || value.Purpose != gatewayRebindTypedEffectPurpose ||
		!validSHA256(value.ImageID) || !validSHA256(value.StagePlanDigest) || !validSHA256(value.Digest) {
		return false
	}
	if (sequence >= 3) != (value.Network != nil) || sequence >= 3 && (!validContainerID(value.Network.ID) ||
		normalizeID(value.Network.ID) != value.Network.ID || !validSHA256(value.Network.OwnershipDigest)) {
		return false
	}
	if (sequence >= 4) != (value.ConfigVolume != nil) || sequence >= 4 && !validGatewayRebindStageConfigVolumeBindingValue(*value.ConfigVolume) {
		return false
	}
	if (sequence >= 5) != (value.DataVolume != nil) || sequence >= 5 && !validGatewayRebindStageDataVolumeBindingValue(*value.DataVolume) {
		return false
	}
	if (sequence >= 6) != (value.StageContainer != nil) || sequence >= 6 && !validGatewayRebindStageContainerBindingValue(*value.StageContainer) {
		return false
	}
	if (sequence >= 7) != (value.StageConfigIntent != nil) ||
		sequence >= 7 && !validGatewayRebindStageConfigIntentBindingValue(*value.StageConfigIntent) ||
		(sequence >= 8) != (value.StageConfigCopy != nil) ||
		sequence >= 8 && !validGatewayRebindStageConfigCopyBindingValue(*value.StageConfigCopy) ||
		(sequence >= 9) != (value.StageStartIntent != nil) ||
		sequence >= 9 && !validGatewayRebindStageStartIntentBindingValue(*value.StageStartIntent) ||
		(sequence >= 10) != (value.StageServing != nil) ||
		sequence >= 10 && !validGatewayRebindStageServingBindingValue(*value.StageServing) ||
		(sequence >= 11) != (value.FinalConfigIntent != nil) ||
		sequence >= 11 && !validGatewayRebindTypedFinalConfigIntentBindingValue(*value.FinalConfigIntent) ||
		(sequence >= 12) != (value.FinalConfigCopy != nil) ||
		sequence >= 12 && !validGatewayRebindFinalConfigCopyBindingValue(*value.FinalConfigCopy) ||
		(sequence >= 13) != (value.HandoverIntent != nil) ||
		sequence >= 13 && !validGatewayRebindTypedHandoverIntentValue(*value.HandoverIntent) ||
		(sequence >= 15) != (value.CutoverIntent != nil) ||
		sequence >= 15 && !validGatewayRebindTypedCutoverIntentValue(*value.CutoverIntent) ||
		(sequence >= 16) != (value.SuccessorServing != nil) ||
		sequence >= 16 && !validGatewayRebindFinalHandoverServingValue(*value.SuccessorServing) {
		return false
	}
	if sequence < 13 && len(value.ApplicationNetworks) != 0 ||
		sequence >= 13 && !validGatewayRebindFinalHandoverApplicationNetworkValues(value.ApplicationNetworks) {
		return false
	}
	if (sequence >= 14) != (value.FinalContainer != nil) || sequence >= 14 && (!validGatewayRebindFinalContainerBindingValue(*value.FinalContainer) ||
		value.StageContainer == nil || value.FinalContainer.ID == value.StageContainer.ID) {
		return false
	}
	if sequence == 17 {
		if value.Resources == nil || value.PhysicalProof == nil ||
			!validGatewayRebindFinalHandoverResourceBindingsValue(*value.Resources) ||
			!validGatewayRebindFinalHandoverOutcomeValue(*value.PhysicalProof) ||
			value.PhysicalProof.Kind != gatewayRebindFinalHandoverOutcomeCommit || value.Network == nil ||
			value.ConfigVolume == nil || value.DataVolume == nil || value.StageContainer == nil || value.FinalContainer == nil ||
			value.Resources.ImageID != value.ImageID || *value.Resources.FinalContainer != *value.FinalContainer ||
			value.Resources.IngressNetwork != *value.Network || value.Resources.ConfigVolume != *value.ConfigVolume ||
			value.Resources.DataVolume != *value.DataVolume || value.Resources.StageContainer != *value.StageContainer ||
			!reflect.DeepEqual(value.Resources.ApplicationNetworks, value.ApplicationNetworks) {
			return false
		}
	} else if value.Resources != nil || value.PhysicalProof != nil {
		return false
	}
	digest, err := gatewayRebindTypedEffectDigest(value)
	return err == nil && digest == value.Digest
}

func validGatewayRebindStageIntent(value gatewayRebindStageIntent) bool {
	identityDigest, err := gatewayRebindSuccessorIdentityDigest(value.Identity)
	return err == nil && identityDigest == value.Identity.Digest &&
		value.ApprovedCaddyImageDigest == value.Identity.CaddyImageDigest &&
		value.ApprovedCaddyImageDigest == gatewayV2CaddyImageDigest &&
		validSHA256(value.ObservedDockerImageID) && validGatewayV2NetworkPlan(gatewayV2NetworkPlan(value.NetworkPlan)) &&
		validSHA256(value.NetworkPlanDigest) && validSHA256(value.NetworkTopologyDigest) &&
		(value.Network == nil || (validContainerID(value.Network.ID) && normalizeID(value.Network.ID) == value.Network.ID &&
			validSHA256(value.Network.OwnershipDigest))) &&
		(value.ConfigVolume == nil || (value.Network != nil && validGatewayRebindStageConfigVolumeBindingValue(*value.ConfigVolume))) &&
		(value.DataVolume == nil || (value.ConfigVolume != nil && validGatewayRebindStageDataVolumeBindingValue(*value.DataVolume))) &&
		(value.StageContainer == nil || (value.DataVolume != nil && validGatewayRebindStageContainerBindingValue(*value.StageContainer))) &&
		(value.StageConfigIntent == nil || (value.StageContainer != nil && validGatewayRebindStageConfigIntentBindingValue(*value.StageConfigIntent))) &&
		(value.StageConfigCopy == nil || (value.StageConfigIntent != nil && validGatewayRebindStageConfigCopyBindingValue(*value.StageConfigCopy))) &&
		(value.StageStartIntent == nil || (value.StageConfigCopy != nil && validGatewayRebindStageStartIntentBindingValue(*value.StageStartIntent))) &&
		(value.StageServing == nil || (value.StageStartIntent != nil && validGatewayRebindStageServingBindingValue(*value.StageServing))) &&
		(value.FinalConfigIntent == nil || (value.StageServing != nil && validGatewayRebindFinalConfigIntentBindingValue(*value.FinalConfigIntent))) &&
		(value.FinalConfigCopy == nil || (value.FinalConfigIntent != nil && validGatewayRebindFinalConfigCopyBindingValue(*value.FinalConfigCopy)))
}

func validGatewayRebindStageConfigVolumeBindingValue(value gatewayRebindStageConfigVolumeBinding) bool {
	if value.Name == "" || strings.TrimSpace(value.Name) != value.Name || value.Mountpoint == "" ||
		strings.TrimSpace(value.Mountpoint) != value.Mountpoint || value.CreatedAt == "" ||
		strings.TrimSpace(value.CreatedAt) != value.CreatedAt || !validSHA256(value.OwnershipDigest) {
		return false
	}
	createdAt, err := time.Parse(time.RFC3339Nano, value.CreatedAt)
	return err == nil && !createdAt.IsZero()
}

func validGatewayRebindStageDataVolumeBindingValue(value gatewayRebindStageDataVolumeBinding) bool {
	if value.Name == "" || strings.TrimSpace(value.Name) != value.Name || value.Mountpoint == "" ||
		strings.TrimSpace(value.Mountpoint) != value.Mountpoint || value.CreatedAt == "" ||
		strings.TrimSpace(value.CreatedAt) != value.CreatedAt || !validSHA256(value.OwnershipDigest) {
		return false
	}
	createdAt, err := time.Parse(time.RFC3339Nano, value.CreatedAt)
	return err == nil && !createdAt.IsZero()
}

func validGatewayRebindStageContainerBindingValue(value gatewayRebindStageContainerBinding) bool {
	return validContainerID(value.ID) && normalizeID(value.ID) == value.ID &&
		validSHA256(value.OwnershipDigest) && validSHA256(value.ConfigurationDigest)
}

func validGatewayRebindStageConfigIntentBindingValue(value gatewayRebindStageConfigIntentBinding) bool {
	return validSHA256(value.ContentDigest) && value.ContentLength > 0 &&
		value.ContentLength <= gatewayV2MaxConfigBytes && value.Destination == "/config/"+gatewayV2StageConfigFilename &&
		validGatewayRebindStageContainerBindingValue(value.StageContainer) &&
		validGatewayRebindStageConfigVolumeBindingValue(value.ConfigVolume) &&
		validSHA256(value.ProtectedIntentDigest) && validSHA256(value.ProtectedPredecessorDigest) &&
		validSHA256(value.PriorProgressDigest)
}

func validGatewayRebindStageConfigCopyBindingValue(value gatewayRebindStageConfigCopyBinding) bool {
	return validGatewayRebindStageConfigIntentBindingValue(value.StageConfigIntent) &&
		validSHA256(value.PriorProgressDigest)
}

func gatewayRebindStageIntentMatchesProtectedIntent(value gatewayRebindProgressRecord,
	intent gatewayRebindProtectedIntent,
) bool {
	return value.Stage != nil && value.Generation == intent.Generation && value.OperationID == intent.OperationID &&
		value.ProtectedIntentDigest == intent.Digest && value.Stage.Identity == intent.Intent.Identity &&
		value.Stage.ApprovedCaddyImageDigest == intent.Intent.Identity.CaddyImageDigest &&
		value.Stage.NetworkPlan == intent.Intent.Network && value.Stage.NetworkPlanDigest == intent.Intent.NetworkDigest
}

func validGatewayRebindProgressTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, err := parseGatewayRebindProgressTime(value.UTC().Format(time.RFC3339Nano))
	return err == nil
}

func parseGatewayRebindProgressTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() || parsed.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("invalid generated ingress rebind progress timestamp")
	}
	return parsed.UTC(), nil
}

func newGatewayRebindProgressStore(dataRoot string, generation uint64, operationID string,
	sequence uint64,
) (*gatewayRebindProgressStore, error) {
	if generation == 0 || generation == math.MaxUint64 || !validCanonicalUUID(operationID) || sequence == 0 || sequence > gatewayRebindProgressMaximumSequence {
		return nil, errors.New("invalid generated ingress rebind progress store")
	}
	directory, err := newStateStore(dataRoot)
	if err != nil {
		return nil, err
	}
	name, purpose := gatewayRebindProgressName(generation, operationID, sequence)
	return &gatewayRebindProgressStore{
		directory: directory, dataRoot: dataRoot, generation: generation, operationID: operationID,
		sequence: sequence, path: filepath.Join(directory.root, name), purpose: purpose,
	}, nil
}

func gatewayRebindProgressName(generation uint64, operationID string, sequence uint64) (string, string) {
	token := fmt.Sprintf("%0*d.%s.s%0*d", gatewayV2GenerationDigits, generation, operationID,
		gatewayRebindProgressSequenceDigits, sequence)
	scope := fmt.Sprintf("generation/%0*d/%s/sequence/%0*d", gatewayV2GenerationDigits, generation,
		operationID, gatewayRebindProgressSequenceDigits, sequence)
	return gatewayRebindProgressFilenamePrefix + token + ".bundle", gatewayRebindProgressPurpose + "/" + scope
}

// installExact uses create-only storage, exact readback, and a fresh strict
// rebind history scan before it reports success. An ambiguous post-install
// error remains an error. A retry can succeed only if the newly observed,
// complete protected history proves this exact record.
func (s *gatewayRebindProgressStore) installExact(ctx context.Context, value gatewayRebindProgressRecord) error {
	if s == nil || s.directory == nil || ctx == nil || ctx.Err() != nil ||
		s.generation != value.Generation || s.operationID != value.OperationID || s.sequence != value.Sequence ||
		!validGatewayRebindProgressRecord(value) {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("invalid generated ingress rebind progress install")
	}
	manager := &Manager{store: s.directory, options: Options{DataRoot: s.dataRoot}}
	before, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !gatewayRebindProgressInstallPermitted(before, value) {
		return errors.New("generated ingress rebind progress install is not the current exact replay")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.writeExact(s.path, s.purpose, value, true, gatewayRebindProgressMaxBytes(value.Sequence)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return fmt.Errorf("generated ingress rebind progress history readback failed: %w", err)
	}
	for _, installed := range history.Progress {
		if installed.Generation == value.Generation && installed.Sequence == value.Sequence &&
			installed.Store != nil && installed.Store.path == s.path && reflect.DeepEqual(installed.Record, value) {
			return nil
		}
	}
	return errors.New("generated ingress rebind progress readback mismatch")
}

func gatewayRebindProgressInstallPermitted(history gatewayRebindProtectedIntentHistory,
	value gatewayRebindProgressRecord,
) bool {
	for _, selected := range history.IntentsV2 {
		if selected.Generation != value.Generation || selected.Intent.OperationID != value.OperationID ||
			selected.Intent.Digest != value.ProtectedIntentDigest {
			continue
		}
		previous := make([]gatewayRebindProgressSelection, 0, value.Sequence-1)
		for _, existing := range history.Progress {
			if existing.Generation != value.Generation {
				continue
			}
			if existing.Sequence == value.Sequence {
				return gatewayRebindProgressMatchesIntentV2(value, selected.Intent, previous) &&
					reflect.DeepEqual(existing.Record, value) &&
					gatewayRebindProgressMatchesCheckpointHistory(history, selected.Intent, previous, value)
			}
			if existing.Sequence < value.Sequence {
				previous = append(previous, existing)
			}
		}
		return value.Sequence == uint64(len(previous)+1) &&
			gatewayRebindProgressMatchesIntentV2(value, selected.Intent, previous) &&
			gatewayRebindProgressMatchesCheckpointHistory(history, selected.Intent, previous, value)
	}
	if len(history.Intents) != 1 || history.Intents[0].Generation != value.Generation ||
		history.Intents[0].Intent.OperationID != value.OperationID ||
		history.Intents[0].Intent.Digest != value.ProtectedIntentDigest {
		return false
	}
	previous := make([]gatewayRebindProgressSelection, 0, value.Sequence-1)
	for _, existing := range history.Progress {
		if existing.Generation != value.Generation {
			return false
		}
		if existing.Sequence == value.Sequence {
			if !gatewayRebindProgressMatchesIntent(value, history.Intents[0].Intent, previous) ||
				!gatewayRebindProgressMatchesPredecessor(history, value) ||
				!gatewayRebindProgressMatchesHandoverContext(history, value) {
				return false
			}
			return reflect.DeepEqual(existing.Record, value)
		}
		if existing.Sequence < value.Sequence {
			previous = append(previous, existing)
		}
	}
	return value.Sequence == uint64(len(history.Progress)+1) &&
		gatewayRebindProgressMatchesIntent(value, history.Intents[0].Intent, previous) &&
		gatewayRebindProgressMatchesPredecessor(history, value) &&
		gatewayRebindProgressMatchesHandoverContext(history, value)
}

func gatewayRebindProgressMatchesCheckpointHistory(history gatewayRebindProtectedIntentHistory,
	intent gatewayRebindProtectedIntentV2, previous []gatewayRebindProgressSelection,
	value gatewayRebindProgressRecord,
) bool {
	for _, selected := range history.Checkpoints {
		if selected.Generation != intent.Generation || selected.Checkpoint.OperationID != intent.OperationID {
			continue
		}
		records := make([]gatewayRebindProgressRecord, 0, len(previous)+1)
		for _, prior := range previous {
			records = append(records, prior.Record)
		}
		records = append(records, value)
		return gatewayRebindTypedProgressMatchesCheckpoint(intent, selected.Checkpoint, records)
	}
	return false
}

func scanGatewayRebindProgressForIntentV2(dataRoot string, intent gatewayRebindProtectedIntentV2,
	checkpoint gatewayRebindPredecessorCheckpoint, artifacts gatewayRebindHistoryGeneration,
) ([]gatewayRebindProgressSelection, error) {
	sequences := make([]uint64, 0, len(artifacts.progress))
	for sequence := range artifacts.progress {
		sequences = append(sequences, sequence)
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	result := make([]gatewayRebindProgressSelection, 0, len(sequences))
	for index, sequence := range sequences {
		if sequence != uint64(index+1) || sequence > gatewayRebindProgressMaximumSequence {
			return nil, errors.New("generated ingress typed rebind progress history has a sequence gap")
		}
		artifact := artifacts.progress[sequence]
		store, err := newGatewayRebindProgressStore(dataRoot, intent.Generation, intent.OperationID, sequence)
		if err != nil || store.path != artifact.path {
			return nil, errors.New("generated ingress typed rebind progress history path is invalid")
		}
		value, err := store.load()
		if err != nil || !gatewayRebindProgressMatchesIntentV2(value, intent, result) {
			return nil, errors.New("generated ingress typed rebind progress history is invalid")
		}
		result = append(result, gatewayRebindProgressSelection{Store: store, Generation: intent.Generation,
			Sequence: sequence, Record: value, Existing: true})
	}
	records := make([]gatewayRebindProgressRecord, len(result))
	for index := range result {
		records[index] = result[index].Record
	}
	if !gatewayRebindTypedProgressMatchesCheckpoint(intent, checkpoint, records) {
		return nil, errors.New("generated ingress typed rebind progress disagrees with predecessor checkpoint")
	}
	return result, nil
}

func gatewayRebindProgressMatchesIntentV2(value gatewayRebindProgressRecord,
	intent gatewayRebindProtectedIntentV2, previous []gatewayRebindProgressSelection,
) bool {
	if !validGatewayRebindProtectedIntentV2(intent) || !validGatewayRebindProgressRecord(value) ||
		value.Generation != intent.Generation || value.OperationID != intent.OperationID ||
		value.ProtectedIntentDigest != intent.Digest {
		return false
	}
	if value.Sequence == 1 {
		return value.Phase == gatewayRebindProgressSuccessorIntent && len(previous) == 0 &&
			value.PreviousDigest == "" && value.Stage == nil && value.Handover == nil &&
			value.TypedEffect == nil && value.TypedRollback == nil
	}
	if value.Sequence < 2 || value.Sequence > 18 || len(previous) != int(value.Sequence-1) {
		return false
	}
	prior := previous[len(previous)-1].Record
	priorAt, priorErr := parseGatewayRebindProgressTime(prior.OccurredAt)
	currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
	if priorErr != nil || currentErr != nil || !currentAt.After(priorAt) ||
		value.PreviousDigest != prior.Digest || prior.Sequence+1 != value.Sequence {
		return false
	}
	if value.TypedRollback != nil {
		return value.TypedEffect == nil && gatewayRebindTypedRollbackMatchesIntent(value, intent, previous)
	}
	if value.TypedEffect == nil || value.TypedRollback != nil || value.Sequence > 17 ||
		!gatewayRebindTypedEffectMatchesIntent(*value.TypedEffect, intent, prior) {
		return false
	}
	if value.Sequence == 2 {
		return prior.Sequence == 1 && prior.TypedEffect == nil
	}
	return prior.TypedEffect != nil && gatewayRebindTypedEffectExtends(*prior.TypedEffect, *value.TypedEffect, value.Sequence)
}

func gatewayRebindTypedRollbackMatchesIntent(value gatewayRebindProgressRecord,
	intent gatewayRebindProtectedIntentV2, previous []gatewayRebindProgressSelection,
) bool {
	rollback := value.TypedRollback
	if rollback == nil || !validGatewayRebindTypedRollbackProgress(*rollback) ||
		rollback.FromSequence >= uint64(len(previous)+1) || rollback.FromSequence < 1 {
		return false
	}
	forward := previous[rollback.FromSequence-1].Record
	if forward.Sequence != rollback.FromSequence || forward.TypedRollback != nil ||
		(forward.Sequence == 1) != (forward.TypedEffect == nil) ||
		forward.Phase != rollback.FromPhase || forward.Digest != rollback.FromDigest ||
		!validGatewayRebindTypedRollbackOwned(rollback.Owned, intent.Identity) {
		return false
	}
	bound := gatewayRebindTypedRollbackOwnedResources{}
	if forward.TypedEffect != nil {
		bound = gatewayRebindTypedRollbackOwnedFromEffect(*forward.TypedEffect)
	}
	if !gatewayRebindTypedRollbackOwnedContains(bound, rollback.Owned, rollback.FromSequence) ||
		(!reflect.DeepEqual(bound, rollback.Owned) && !validSHA256(rollback.AdoptionProofDigest)) ||
		(reflect.DeepEqual(bound, rollback.Owned) && rollback.AdoptionProofDigest != "") {
		return false
	}
	planDigest, err := gatewayRebindTypedRollbackPlanDigest(intent, *rollback)
	if err != nil || planDigest != rollback.PlanDigest {
		return false
	}
	switch value.Phase {
	case gatewayRebindProgressRollbackIntent:
		prior := previous[len(previous)-1].Record
		return rollback.PhysicalProof == nil && prior.Sequence == rollback.FromSequence && prior.Digest == rollback.FromDigest &&
			value.Sequence == rollback.FromSequence+1
	case gatewayRebindProgressHandoverRolledBack:
		prior := previous[len(previous)-1].Record
		if prior.Phase != gatewayRebindProgressRollbackIntent || prior.TypedRollback == nil ||
			prior.Sequence != rollback.FromSequence+1 || value.Sequence != rollback.FromSequence+2 ||
			rollback.PhysicalProof == nil || !validGatewayRebindTypedRollbackPhysicalProof(*rollback.PhysicalProof, *rollback, prior.Digest) {
			return false
		}
		prefix := *rollback
		prefix.PhysicalProof = nil
		prefix.Digest = prior.TypedRollback.Digest
		return reflect.DeepEqual(prefix, *prior.TypedRollback)
	default:
		return false
	}
}

func gatewayRebindTypedEffectMatchesIntent(value gatewayRebindTypedEffectProgress,
	intent gatewayRebindProtectedIntentV2, prior gatewayRebindProgressRecord,
) bool {
	planDigest, planErr := gatewayRebindTypedStagePlanDigest(intent)
	if planErr != nil || value.StagePlanDigest != planDigest ||
		!validGatewayRebindTypedEffectProgress(value, prior.Sequence+1) ||
		value.ConfigVolume != nil && value.ConfigVolume.Name != intent.Identity.ConfigVolume ||
		value.DataVolume != nil && value.DataVolume.Name != intent.Identity.DataVolume ||
		!gatewayRebindTypedEffectSemanticMatch(value, intent, prior) {
		return false
	}
	if prior.Sequence+1 == 17 {
		return value.Resources != nil && value.PhysicalProof != nil &&
			gatewayRebindTypedPhysicalOutcomeMatches(intent.Identity, prior.Digest, *value.Resources, *value.PhysicalProof)
	}
	return true
}

func gatewayRebindTypedEffectExtends(previous, current gatewayRebindTypedEffectProgress, sequence uint64) bool {
	prefix := current
	switch sequence {
	case 3:
		prefix.Network = nil
	case 4:
		prefix.ConfigVolume = nil
	case 5:
		prefix.DataVolume = nil
	case 6:
		prefix.StageContainer = nil
	case 7:
		prefix.StageConfigIntent = nil
	case 8:
		prefix.StageConfigCopy = nil
	case 9:
		prefix.StageStartIntent = nil
	case 10:
		prefix.StageServing = nil
	case 11:
		prefix.FinalConfigIntent = nil
	case 12:
		prefix.FinalConfigCopy = nil
	case 13:
		prefix.HandoverIntent, prefix.ApplicationNetworks = nil, nil
	case 14:
		prefix.FinalContainer = nil
	case 15:
		prefix.CutoverIntent = nil
	case 16:
		prefix.SuccessorServing = nil
	case 17:
		prefix.Resources, prefix.PhysicalProof = nil, nil
	default:
		return false
	}
	prefix.Digest = previous.Digest
	return reflect.DeepEqual(prefix, previous)
}

func gatewayRebindProgressMatchesHandoverContext(history gatewayRebindProtectedIntentHistory,
	value gatewayRebindProgressRecord,
) bool {
	if value.Sequence <= 12 {
		return true
	}
	if len(history.Intents) != 1 || len(history.Progress) < 12 ||
		history.Progress[11].Record.Sequence != 12 || value.Handover == nil || value.Handover.Plan == nil {
		return false
	}
	contextValue := gatewayRebindFinalHandoverContext{
		Intent: history.Intents[0].Intent, SequenceTwelve: history.Progress[11].Record,
		Predecessor: history.Predecessor, Source: history.Source, Phase: value.Phase,
		Plan: value.Handover.Plan, Final: value.Handover.Final,
	}
	return validGatewayRebindFinalHandoverProgressContext(contextValue, value)
}

func gatewayRebindProgressMatchesPredecessor(history gatewayRebindProtectedIntentHistory,
	value gatewayRebindProgressRecord,
) bool {
	if value.Sequence < 11 {
		return true
	}
	return value.Stage != nil && value.Stage.FinalConfigIntent != nil &&
		gatewayRebindFinalConfigRoutePlanMatchesPredecessor(history.Intents[0].Intent,
			history.Predecessor, value.Stage.FinalConfigIntent.RoutePlan)
}

func (s *gatewayRebindProgressStore) load() (gatewayRebindProgressRecord, error) {
	if s == nil || s.directory == nil {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind progress store")
	}
	var value gatewayRebindProgressRecord
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.readStrict(s.path, s.purpose, gatewayRebindProgressMaxBytes(s.sequence), &value); err != nil ||
		value.Generation != s.generation || value.OperationID != s.operationID || value.Sequence != s.sequence ||
		!validGatewayRebindProgressRecord(value) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind progress")
	}
	return value, nil
}

func scanGatewayRebindProgressForIntent(dataRoot string, intent gatewayRebindProtectedIntent,
	predecessor gatewayUpgradeGenerationSelection, artifacts gatewayRebindHistoryGeneration,
) ([]gatewayRebindProgressSelection, error) {
	sequences := make([]uint64, 0, len(artifacts.progress))
	for sequence := range artifacts.progress {
		sequences = append(sequences, sequence)
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	result := make([]gatewayRebindProgressSelection, 0, len(sequences))
	for index, sequence := range sequences {
		if sequence != uint64(index+1) || sequence > gatewayRebindProgressMaximumSequence {
			return nil, errors.New("generated ingress rebind progress history has a sequence gap")
		}
		artifact := artifacts.progress[sequence]
		store, err := newGatewayRebindProgressStore(dataRoot, intent.Generation, intent.OperationID, sequence)
		if err != nil || store.path != artifact.path {
			return nil, errors.New("generated ingress rebind progress history path is invalid")
		}
		value, err := store.load()
		if err != nil || !gatewayRebindProgressMatchesIntent(value, intent, result) ||
			(value.Sequence >= 11 && (value.Stage == nil || value.Stage.FinalConfigIntent == nil ||
				!gatewayRebindFinalConfigRoutePlanMatchesPredecessor(intent, predecessor,
					value.Stage.FinalConfigIntent.RoutePlan))) {
			return nil, errors.New("generated ingress rebind progress history is invalid")
		}
		result = append(result, gatewayRebindProgressSelection{
			Store: store, Generation: intent.Generation, Sequence: sequence, Record: value, Existing: true,
		})
	}
	return result, nil
}

func gatewayRebindProgressMatchesIntent(value gatewayRebindProgressRecord, intent gatewayRebindProtectedIntent,
	previous []gatewayRebindProgressSelection,
) bool {
	if !validGatewayRebindProgressRecord(value) || value.Generation != intent.Generation ||
		value.OperationID != intent.OperationID || value.ProtectedIntentDigest != intent.Digest {
		return false
	}
	switch value.Sequence {
	case 1:
		return value.Phase == gatewayRebindProgressSuccessorIntent && value.PreviousDigest == "" && value.Stage == nil
	case 2:
		if value.Phase != gatewayRebindProgressStageIntent || len(previous) != 1 ||
			value.PreviousDigest != previous[0].Record.Digest || !gatewayRebindStageIntentMatchesProtectedIntent(value, intent) {
			return false
		}
		previousAt, err := parseGatewayRebindProgressTime(previous[0].Record.OccurredAt)
		currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
		return err == nil && currentErr == nil && currentAt.After(previousAt) && value.Stage.Network == nil
	case 3:
		if value.Phase != gatewayRebindProgressStageIntent || len(previous) != 2 ||
			value.PreviousDigest != previous[1].Record.Digest || value.Stage == nil || value.Stage.Network == nil ||
			!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) ||
			previous[1].Record.Stage == nil || previous[1].Record.Stage.Network != nil {
			return false
		}
		ownershipDigest, digestErr := gatewayRebindStageNetworkOwnershipDigest(intent)
		if digestErr != nil || value.Stage.Network.OwnershipDigest != ownershipDigest {
			return false
		}
		stageWithoutNetwork := *value.Stage
		stageWithoutNetwork.Network = nil
		if !reflect.DeepEqual(stageWithoutNetwork, *previous[1].Record.Stage) {
			return false
		}
		previousAt, err := parseGatewayRebindProgressTime(previous[1].Record.OccurredAt)
		currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
		return err == nil && currentErr == nil && currentAt.After(previousAt)
	case 4:
		if value.Phase != gatewayRebindProgressStageIntent || len(previous) != 3 ||
			value.PreviousDigest != previous[2].Record.Digest || value.Stage == nil || value.Stage.Network == nil ||
			value.Stage.ConfigVolume == nil || !gatewayRebindStageIntentMatchesProtectedIntent(value, intent) ||
			previous[2].Record.Stage == nil || previous[2].Record.Stage.Network == nil ||
			previous[2].Record.Stage.ConfigVolume != nil ||
			!validGatewayRebindStageConfigVolumeBinding(intent, *value.Stage.ConfigVolume) {
			return false
		}
		stageWithoutConfigVolume := *value.Stage
		stageWithoutConfigVolume.ConfigVolume = nil
		if !reflect.DeepEqual(stageWithoutConfigVolume, *previous[2].Record.Stage) {
			return false
		}
		previousAt, err := parseGatewayRebindProgressTime(previous[2].Record.OccurredAt)
		currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
		return err == nil && currentErr == nil && currentAt.After(previousAt)
	case 5:
		if value.Phase != gatewayRebindProgressStageIntent || len(previous) != 4 ||
			value.PreviousDigest != previous[3].Record.Digest || value.Stage == nil || value.Stage.Network == nil ||
			value.Stage.ConfigVolume == nil || value.Stage.DataVolume == nil ||
			!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) ||
			previous[3].Record.Stage == nil || previous[3].Record.Stage.Network == nil ||
			previous[3].Record.Stage.ConfigVolume == nil || previous[3].Record.Stage.DataVolume != nil ||
			!validGatewayRebindStageDataVolumeBinding(intent, *value.Stage.DataVolume) {
			return false
		}
		stageWithoutDataVolume := *value.Stage
		stageWithoutDataVolume.DataVolume = nil
		if !reflect.DeepEqual(stageWithoutDataVolume, *previous[3].Record.Stage) {
			return false
		}
		previousAt, err := parseGatewayRebindProgressTime(previous[3].Record.OccurredAt)
		currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
		return err == nil && currentErr == nil && currentAt.After(previousAt)
	case 6:
		if value.Phase != gatewayRebindProgressStageIntent || len(previous) != 5 ||
			value.PreviousDigest != previous[4].Record.Digest || value.Stage == nil || value.Stage.Network == nil ||
			value.Stage.ConfigVolume == nil || value.Stage.DataVolume == nil || value.Stage.StageContainer == nil ||
			!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) ||
			previous[4].Record.Stage == nil || previous[4].Record.Stage.Network == nil ||
			previous[4].Record.Stage.ConfigVolume == nil || previous[4].Record.Stage.DataVolume == nil ||
			previous[4].Record.Stage.StageContainer != nil ||
			!validGatewayRebindStageContainerBinding(intent, *value.Stage.StageContainer) {
			return false
		}
		configurationDigest, digestErr := gatewayRebindStageContainerConfigurationDigest(intent, *previous[4].Record.Stage)
		if digestErr != nil || value.Stage.StageContainer.ConfigurationDigest != configurationDigest {
			return false
		}
		stageWithoutContainer := *value.Stage
		stageWithoutContainer.StageContainer = nil
		if !reflect.DeepEqual(stageWithoutContainer, *previous[4].Record.Stage) {
			return false
		}
		previousAt, err := parseGatewayRebindProgressTime(previous[4].Record.OccurredAt)
		currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
		return err == nil && currentErr == nil && currentAt.After(previousAt)
	case 7:
		if value.Phase != gatewayRebindProgressStageConfigIntent || len(previous) != 6 ||
			value.PreviousDigest != previous[5].Record.Digest || value.Stage == nil || value.Stage.Network == nil ||
			value.Stage.ConfigVolume == nil || value.Stage.DataVolume == nil || value.Stage.StageContainer == nil ||
			value.Stage.StageConfigIntent == nil || !gatewayRebindStageIntentMatchesProtectedIntent(value, intent) ||
			previous[5].Record.Stage == nil || previous[5].Record.Stage.Network == nil ||
			previous[5].Record.Stage.ConfigVolume == nil || previous[5].Record.Stage.DataVolume == nil ||
			previous[5].Record.Stage.StageContainer == nil || previous[5].Record.Stage.StageConfigIntent != nil ||
			!validGatewayRebindStageConfigIntentBinding(intent, previous[5].Record, *value.Stage.StageConfigIntent) {
			return false
		}
		stageWithoutConfigIntent := *value.Stage
		stageWithoutConfigIntent.StageConfigIntent = nil
		if !reflect.DeepEqual(stageWithoutConfigIntent, *previous[5].Record.Stage) {
			return false
		}
		previousAt, err := parseGatewayRebindProgressTime(previous[5].Record.OccurredAt)
		currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
		return err == nil && currentErr == nil && currentAt.After(previousAt)
	case 8:
		if value.Phase != gatewayRebindProgressStageConfigCopied || len(previous) != 7 ||
			value.PreviousDigest != previous[6].Record.Digest || value.Stage == nil || value.Stage.Network == nil ||
			value.Stage.ConfigVolume == nil || value.Stage.DataVolume == nil || value.Stage.StageContainer == nil ||
			value.Stage.StageConfigIntent == nil || value.Stage.StageConfigCopy == nil ||
			!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) ||
			previous[6].Record.Stage == nil || previous[6].Record.Stage.Network == nil ||
			previous[6].Record.Stage.ConfigVolume == nil || previous[6].Record.Stage.DataVolume == nil ||
			previous[6].Record.Stage.StageContainer == nil || previous[6].Record.Stage.StageConfigIntent == nil ||
			previous[6].Record.Stage.StageConfigCopy != nil ||
			!validGatewayRebindStageConfigCopyBinding(intent, previous[6].Record, *value.Stage.StageConfigCopy) {
			return false
		}
		stageWithoutConfigCopy := *value.Stage
		stageWithoutConfigCopy.StageConfigCopy = nil
		if !reflect.DeepEqual(stageWithoutConfigCopy, *previous[6].Record.Stage) {
			return false
		}
		previousAt, err := parseGatewayRebindProgressTime(previous[6].Record.OccurredAt)
		currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
		return err == nil && currentErr == nil && currentAt.After(previousAt)
	case 9:
		if value.Phase != gatewayRebindProgressStageStartIntent || len(previous) != 8 ||
			value.PreviousDigest != previous[7].Record.Digest || value.Stage == nil || value.Stage.Network == nil ||
			value.Stage.ConfigVolume == nil || value.Stage.DataVolume == nil || value.Stage.StageContainer == nil ||
			value.Stage.StageConfigIntent == nil || value.Stage.StageConfigCopy == nil ||
			value.Stage.StageStartIntent == nil || !gatewayRebindStageIntentMatchesProtectedIntent(value, intent) ||
			previous[7].Record.Stage == nil || previous[7].Record.Stage.Network == nil ||
			previous[7].Record.Stage.ConfigVolume == nil || previous[7].Record.Stage.DataVolume == nil ||
			previous[7].Record.Stage.StageContainer == nil || previous[7].Record.Stage.StageConfigIntent == nil ||
			previous[7].Record.Stage.StageConfigCopy == nil || previous[7].Record.Stage.StageStartIntent != nil ||
			!validGatewayRebindStageStartIntentBinding(intent, previous[7].Record, *value.Stage.StageStartIntent) {
			return false
		}
		stageWithoutStart := *value.Stage
		stageWithoutStart.StageStartIntent = nil
		if !reflect.DeepEqual(stageWithoutStart, *previous[7].Record.Stage) {
			return false
		}
		previousAt, err := parseGatewayRebindProgressTime(previous[7].Record.OccurredAt)
		currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
		return err == nil && currentErr == nil && currentAt.After(previousAt)
	case 10:
		if value.Phase != gatewayRebindProgressStageServing || len(previous) != 9 ||
			value.PreviousDigest != previous[8].Record.Digest || value.Stage == nil || value.Stage.Network == nil ||
			value.Stage.ConfigVolume == nil || value.Stage.DataVolume == nil || value.Stage.StageContainer == nil ||
			value.Stage.StageConfigIntent == nil || value.Stage.StageConfigCopy == nil ||
			value.Stage.StageStartIntent == nil || value.Stage.StageServing == nil ||
			!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) ||
			previous[8].Record.Stage == nil || previous[8].Record.Stage.StageStartIntent == nil ||
			previous[8].Record.Stage.StageServing != nil ||
			!validGatewayRebindStageServingBinding(intent, previous[8].Record, *value.Stage.StageServing) {
			return false
		}
		stageWithoutServing := *value.Stage
		stageWithoutServing.StageServing = nil
		if !reflect.DeepEqual(stageWithoutServing, *previous[8].Record.Stage) {
			return false
		}
		previousAt, err := parseGatewayRebindProgressTime(previous[8].Record.OccurredAt)
		currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
		return err == nil && currentErr == nil && currentAt.After(previousAt)
	case 11:
		if value.Phase != gatewayRebindProgressFinalConfigIntent || len(previous) != 10 ||
			value.PreviousDigest != previous[9].Record.Digest || value.Stage == nil ||
			value.Stage.StageServing == nil || value.Stage.FinalConfigIntent == nil ||
			!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) ||
			previous[9].Record.Stage == nil || previous[9].Record.Stage.StageServing == nil ||
			previous[9].Record.Stage.FinalConfigIntent != nil ||
			!validGatewayRebindFinalConfigIntentBinding(intent, previous[9].Record, *value.Stage.FinalConfigIntent) {
			return false
		}
		stageWithoutFinalConfig := *value.Stage
		stageWithoutFinalConfig.FinalConfigIntent = nil
		if !reflect.DeepEqual(stageWithoutFinalConfig, *previous[9].Record.Stage) {
			return false
		}
		previousAt, err := parseGatewayRebindProgressTime(previous[9].Record.OccurredAt)
		currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
		return err == nil && currentErr == nil && currentAt.After(previousAt)
	case 12:
		if value.Phase != gatewayRebindProgressFinalConfigCopied || len(previous) != 11 ||
			value.PreviousDigest != previous[10].Record.Digest || value.Stage == nil ||
			value.Stage.StageServing == nil || value.Stage.FinalConfigIntent == nil ||
			value.Stage.FinalConfigCopy == nil ||
			!gatewayRebindStageIntentMatchesProtectedIntent(value, intent) ||
			previous[10].Record.Stage == nil || previous[10].Record.Stage.StageServing == nil ||
			previous[10].Record.Stage.FinalConfigIntent == nil ||
			previous[10].Record.Stage.FinalConfigCopy != nil ||
			!validGatewayRebindFinalConfigCopyBinding(intent, previous[10].Record, *value.Stage.FinalConfigCopy) {
			return false
		}
		stageWithoutFinalConfigCopy := *value.Stage
		stageWithoutFinalConfigCopy.FinalConfigCopy = nil
		if !reflect.DeepEqual(stageWithoutFinalConfigCopy, *previous[10].Record.Stage) {
			return false
		}
		previousAt, err := parseGatewayRebindProgressTime(previous[10].Record.OccurredAt)
		currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
		return err == nil && currentErr == nil && currentAt.After(previousAt)
	default:
		return value.Sequence >= 13 &&
			gatewayRebindFinalHandoverProgressMatchesIntent(value, intent, previous)
	}
}

func gatewayRebindProgressMaxBytes(sequence uint64) int {
	if sequence >= 11 && sequence <= gatewayRebindProgressMaximumSequence {
		return maxGatewayRebindFinalConfigProgressBytes
	}
	return maxGatewayRebindProgressBytes
}
