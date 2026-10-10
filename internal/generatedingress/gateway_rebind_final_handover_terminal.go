package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"time"
)

const (
	gatewayRebindFinalHandoverTerminalVersion              = 1
	gatewayRebindFinalHandoverTerminalPurpose              = "hostd/generated-ingress/rebind/terminal/v1"
	gatewayRebindFinalHandoverTerminalFilenamePrefix       = "gateway-rebind-terminal.v1.g"
	maxGatewayRebindFinalHandoverTerminalBytes             = 256 << 10
	gatewayRebindPreparedClaimStateSequence          int64 = 1
)

type gatewayRebindFinalHandoverTerminalDisposition string

const (
	gatewayRebindFinalHandoverTerminalCommit gatewayRebindFinalHandoverTerminalDisposition = "commit"
	gatewayRebindFinalHandoverTerminalAbort  gatewayRebindFinalHandoverTerminalDisposition = "abort"
)

type gatewayRebindFinalHandoverResourceBindings struct {
	ImageID             string                                    `json:"imageId"`
	IngressNetwork      gatewayRebindStageNetworkBinding          `json:"ingressNetwork"`
	ConfigVolume        gatewayRebindStageConfigVolumeBinding     `json:"configVolume"`
	DataVolume          gatewayRebindStageDataVolumeBinding       `json:"dataVolume"`
	StageContainer      gatewayRebindStageContainerBinding        `json:"stageContainer"`
	FinalContainer      *gatewayRebindFinalContainerBinding       `json:"finalContainer,omitempty"`
	ApplicationNetworks []gatewayRebindHandoverApplicationNetwork `json:"applicationNetworks"`
	Digest              string                                    `json:"digest"`
}

// gatewayRebindFinalHandoverTerminalReceipt is protected unit-one evidence.
// It deliberately does not commit SQLite, advance the current predecessor, or
// synthesize a migration-026 journal. A later cross-store transaction must
// match this immutable evidence before it may establish successor lineage.
type gatewayRebindFinalHandoverTerminalReceipt struct {
	Version                    int                                           `json:"version"`
	Purpose                    string                                        `json:"purpose"`
	Generation                 uint64                                        `json:"generation"`
	OperationID                string                                        `json:"operationId"`
	Disposition                gatewayRebindFinalHandoverTerminalDisposition `json:"disposition"`
	ProtectedIntentDigest      string                                        `json:"protectedIntentDigest"`
	ClaimRequestDigest         string                                        `json:"claimRequestDigest"`
	PreparedDatabaseDigest     string                                        `json:"preparedDatabaseDigest"`
	PreparedClaimStateSequence int64                                         `json:"preparedClaimStateSequence"`
	TerminalProgressSequence   uint64                                        `json:"terminalProgressSequence"`
	TerminalProgressPhase      gatewayRebindProgressPhase                    `json:"terminalProgressPhase"`
	TerminalProgressDigest     string                                        `json:"terminalProgressDigest"`
	SequenceTwelveDigest       string                                        `json:"sequenceTwelveDigest"`
	PlanDigest                 string                                        `json:"planDigest"`
	FinalBindingDigest         string                                        `json:"finalBindingDigest,omitempty"`
	Predecessor                gatewayRebindSuccessorSourceBinding           `json:"predecessor"`
	SuccessorProfile           gatewayRebindSuccessorIntentProfile           `json:"successorProfile"`
	SuccessorIdentity          gatewayRebindSuccessorIdentity                `json:"successorIdentity"`
	RosterDigest               string                                        `json:"rosterDigest"`
	RosterCount                int64                                         `json:"rosterCount"`
	RosterEntryDigests         []string                                      `json:"rosterEntryDigests"`
	RoutePlanDigest            string                                        `json:"routePlanDigest"`
	FinalConfigDigest          string                                        `json:"finalConfigDigest"`
	Resources                  gatewayRebindFinalHandoverResourceBindings    `json:"resources"`
	PhysicalProof              gatewayRebindFinalHandoverTerminalProof       `json:"physicalProof"`
	CreatedAt                  string                                        `json:"createdAt"`
	Digest                     string                                        `json:"digest"`
}

type gatewayRebindFinalHandoverTerminalStore struct {
	directory   *stateStore
	dataRoot    string
	generation  uint64
	operationID string
	path        string
	purpose     string
}

type gatewayRebindFinalHandoverTerminalSelection struct {
	Store      *gatewayRebindFinalHandoverTerminalStore
	Generation uint64
	Receipt    gatewayRebindFinalHandoverTerminalReceipt
	Existing   bool
}

func gatewayRebindFinalHandoverResourcesDigest(value gatewayRebindFinalHandoverResourceBindings) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayRebindFinalHandoverTerminalDigest(value gatewayRebindFinalHandoverTerminalReceipt) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayRebindFinalHandoverTerminalReceiptFor(history gatewayRebindProtectedIntentHistory,
	createdAt time.Time,
) (gatewayRebindFinalHandoverTerminalReceipt, error) {
	invalid := errors.New("invalid generated ingress rebind terminal receipt input")
	if len(history.Intents) != 1 || len(history.Progress) < 15 || len(history.Progress) > 18 ||
		!validGatewayRebindProgressTime(createdAt) || len(history.Terminals) != 0 {
		return gatewayRebindFinalHandoverTerminalReceipt{}, invalid
	}
	intent := history.Intents[0].Intent
	last := history.Progress[len(history.Progress)-1].Record
	sequenceTwelve := history.Progress[11].Record
	if _, _, err := gatewayRebindEffectBoundaryProgressDigest(intent, history.Progress); err != nil ||
		!gatewayRebindProtectedIntentMatchesPredecessor(intent, history.Predecessor) {
		return gatewayRebindFinalHandoverTerminalReceipt{}, invalid
	}
	if last.Handover == nil || last.Handover.Plan == nil || last.Handover.Outcome == nil ||
		sequenceTwelve.Sequence != 12 || sequenceTwelve.Stage == nil ||
		sequenceTwelve.Stage.FinalConfigIntent == nil || sequenceTwelve.Stage.StageContainer == nil ||
		sequenceTwelve.Stage.Network == nil || sequenceTwelve.Stage.ConfigVolume == nil ||
		sequenceTwelve.Stage.DataVolume == nil {
		return gatewayRebindFinalHandoverTerminalReceipt{}, invalid
	}
	handoverContext := gatewayRebindFinalHandoverContext{
		Intent: intent, SequenceTwelve: sequenceTwelve, Predecessor: history.Predecessor, Source: history.Source,
		Phase: last.Phase, Plan: last.Handover.Plan, Final: last.Handover.Final,
	}
	if !validGatewayRebindFinalHandoverProgressContext(handoverContext, last) {
		return gatewayRebindFinalHandoverTerminalReceipt{}, invalid
	}
	lastAt, err := parseGatewayRebindProgressTime(last.OccurredAt)
	if err != nil || !createdAt.After(lastAt) {
		return gatewayRebindFinalHandoverTerminalReceipt{}, invalid
	}
	var disposition gatewayRebindFinalHandoverTerminalDisposition
	switch {
	case last.Phase == gatewayRebindProgressHandoverCommitted &&
		last.Handover.Outcome.Kind == gatewayRebindFinalHandoverOutcomeCommit && last.Handover.Final != nil:
		disposition = gatewayRebindFinalHandoverTerminalCommit
	case last.Phase == gatewayRebindProgressHandoverRolledBack &&
		last.Handover.Outcome.Kind == gatewayRebindFinalHandoverOutcomeAbort && last.Handover.Rollback != nil:
		disposition = gatewayRebindFinalHandoverTerminalAbort
	default:
		return gatewayRebindFinalHandoverTerminalReceipt{}, invalid
	}
	plan := last.Handover.Plan
	resources := gatewayRebindFinalHandoverResourceBindings{
		ImageID: sequenceTwelve.Stage.ObservedDockerImageID, IngressNetwork: *sequenceTwelve.Stage.Network,
		ConfigVolume: *sequenceTwelve.Stage.ConfigVolume, DataVolume: *sequenceTwelve.Stage.DataVolume,
		StageContainer: *sequenceTwelve.Stage.StageContainer, FinalContainer: last.Handover.Final,
		ApplicationNetworks: append([]gatewayRebindHandoverApplicationNetwork(nil), plan.ApplicationNetworks...),
	}
	resources.Digest, err = gatewayRebindFinalHandoverResourcesDigest(resources)
	if err != nil {
		return gatewayRebindFinalHandoverTerminalReceipt{}, invalid
	}
	value := gatewayRebindFinalHandoverTerminalReceipt{
		Version: gatewayRebindFinalHandoverTerminalVersion, Purpose: gatewayRebindFinalHandoverTerminalPurpose,
		Generation: intent.Generation, OperationID: intent.OperationID, Disposition: disposition,
		ProtectedIntentDigest: intent.Digest, ClaimRequestDigest: intent.Intent.Claim.RequestDigest,
		PreparedDatabaseDigest: intent.DatabaseDigest, PreparedClaimStateSequence: gatewayRebindPreparedClaimStateSequence,
		TerminalProgressSequence: last.Sequence, TerminalProgressPhase: last.Phase, TerminalProgressDigest: last.Digest,
		SequenceTwelveDigest: sequenceTwelve.Digest, PlanDigest: plan.Digest,
		Predecessor: intent.Intent.Predecessor, SuccessorProfile: intent.Intent.SuccessorProfile,
		SuccessorIdentity: intent.Intent.Identity, RosterDigest: intent.Intent.Claim.RosterDigest,
		RosterCount:        intent.Intent.Claim.RosterCount,
		RosterEntryDigests: append([]string(nil), intent.Intent.RosterEntryDigests...),
		RoutePlanDigest:    sequenceTwelve.Stage.FinalConfigIntent.RoutePlan.Digest,
		FinalConfigDigest:  sequenceTwelve.Stage.FinalConfigIntent.ContentDigest,
		Resources:          resources, PhysicalProof: *last.Handover.Outcome,
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
	}
	if last.Handover.Final != nil {
		value.FinalBindingDigest, err = gatewayRebindFinalContainerBindingDigest(*last.Handover.Final)
		if err != nil {
			return gatewayRebindFinalHandoverTerminalReceipt{}, invalid
		}
	}
	value.Digest, err = gatewayRebindFinalHandoverTerminalDigest(value)
	if err != nil || !validGatewayRebindFinalHandoverTerminalReceiptValue(value) {
		return gatewayRebindFinalHandoverTerminalReceipt{}, invalid
	}
	return value, nil
}

func validGatewayRebindFinalHandoverResourceBindingsValue(value gatewayRebindFinalHandoverResourceBindings) bool {
	if !validSHA256(value.ImageID) || !validContainerID(value.IngressNetwork.ID) ||
		normalizeID(value.IngressNetwork.ID) != value.IngressNetwork.ID ||
		!validSHA256(value.IngressNetwork.OwnershipDigest) ||
		!validGatewayRebindStageConfigVolumeBindingValue(value.ConfigVolume) ||
		!validGatewayRebindStageDataVolumeBindingValue(value.DataVolume) ||
		!validGatewayRebindStageContainerBindingValue(value.StageContainer) ||
		(value.FinalContainer != nil && !validGatewayRebindFinalContainerBindingValue(*value.FinalContainer)) ||
		!validGatewayRebindFinalHandoverApplicationNetworkValues(value.ApplicationNetworks) || !validSHA256(value.Digest) {
		return false
	}
	digest, err := gatewayRebindFinalHandoverResourcesDigest(value)
	return err == nil && digest == value.Digest
}

func validGatewayRebindFinalHandoverTerminalReceiptValue(value gatewayRebindFinalHandoverTerminalReceipt) bool {
	if value.Version != gatewayRebindFinalHandoverTerminalVersion || value.Purpose != gatewayRebindFinalHandoverTerminalPurpose ||
		value.Generation == 0 || value.Generation == math.MaxUint64 || !validCanonicalUUID(value.OperationID) ||
		(value.Disposition != gatewayRebindFinalHandoverTerminalCommit && value.Disposition != gatewayRebindFinalHandoverTerminalAbort) ||
		!validSHA256(value.ProtectedIntentDigest) || !validSHA256(value.ClaimRequestDigest) ||
		!validSHA256(value.PreparedDatabaseDigest) || value.PreparedClaimStateSequence != gatewayRebindPreparedClaimStateSequence ||
		value.TerminalProgressSequence < 15 || value.TerminalProgressSequence > 18 ||
		!validSHA256(value.TerminalProgressDigest) || !validSHA256(value.SequenceTwelveDigest) ||
		!validSHA256(value.PlanDigest) || (value.FinalBindingDigest != "" && !validSHA256(value.FinalBindingDigest)) ||
		!validGatewayRebindSuccessorSourceBinding(value.Predecessor) ||
		!validGatewayRebindSuccessorIntentProfile(value.SuccessorProfile) ||
		!validGatewayRebindSuccessorIdentityValue(value.SuccessorIdentity) || !validSHA256(value.RosterDigest) ||
		value.RosterCount < 0 || value.RosterCount != int64(len(value.RosterEntryDigests)) ||
		!validSHA256(value.RoutePlanDigest) || !validSHA256(value.FinalConfigDigest) ||
		!validGatewayRebindFinalHandoverResourceBindingsValue(value.Resources) ||
		!validGatewayRebindFinalHandoverOutcomeValue(value.PhysicalProof) || !validSHA256(value.Digest) {
		return false
	}
	for _, digest := range value.RosterEntryDigests {
		if !validSHA256(digest) {
			return false
		}
	}
	if _, err := parseGatewayRebindProgressTime(value.CreatedAt); err != nil {
		return false
	}
	if (value.Disposition == gatewayRebindFinalHandoverTerminalCommit &&
		(value.TerminalProgressPhase != gatewayRebindProgressHandoverCommitted ||
			value.PhysicalProof.Kind != gatewayRebindFinalHandoverOutcomeCommit ||
			value.FinalBindingDigest == "" || value.Resources.FinalContainer == nil)) ||
		(value.Disposition == gatewayRebindFinalHandoverTerminalAbort &&
			(value.TerminalProgressPhase != gatewayRebindProgressHandoverRolledBack ||
				value.PhysicalProof.Kind != gatewayRebindFinalHandoverOutcomeAbort)) {
		return false
	}
	digest, err := gatewayRebindFinalHandoverTerminalDigest(value)
	return err == nil && digest == value.Digest
}

func validGatewayRebindSuccessorSourceBinding(value gatewayRebindSuccessorSourceBinding) bool {
	return value.Kind == gatewayRebindInitialSourceKind && value.Generation < math.MaxUint64 &&
		validCanonicalUUID(value.OperationID) && validGatewayProfileBinding(value.Profile) &&
		validSHA256(value.StateDigest) && validSHA256(value.JournalDigest) &&
		validSHA256(value.IdentityDigest) && validGatewayV2ResourceBindings(gatewayPhaseCommitted, value.Resources)
}

func validGatewayRebindSuccessorIntentProfile(value gatewayRebindSuccessorIntentProfile) bool {
	return validCanonicalUUID(value.RevisionID) && value.RevisionNumber > 0 &&
		validCanonicalUUID(value.OperationID) && validSHA256(value.RequestDigest) && validSHA256(value.SpecDigest) &&
		value.SelectedIPv4 != "" && value.InterfaceID != "" && value.PortStart > 0 && value.PortEnd >= value.PortStart &&
		validCanonicalUUID(value.ApprovedBy)
}

func validGatewayRebindSuccessorIdentityValue(value gatewayRebindSuccessorIdentity) bool {
	return validSHA256(value.Digest) && value.Generation > 0 && validCanonicalUUID(value.OperationID) &&
		validCanonicalUUID(value.ProfileRevisionID) && value.ProfileRevisionNumber > 0 &&
		validSHA256(value.ProfileSpecDigest) && value.CaddyImageDigest == gatewayV2CaddyImageDigest &&
		value.FinalContainer != "" && value.StageContainer != "" && value.ConfigVolume != "" &&
		value.DataVolume != "" && value.IngressNetwork != "" && value.FinalHostname != "" && value.StageHostname != "" &&
		value.StageConfigFilename == gatewayV2StageConfigFilename && value.ActiveConfigFilename == gatewayV2ActiveConfigFile
}

func newGatewayRebindFinalHandoverTerminalStore(dataRoot string, generation uint64,
	operationID string,
) (*gatewayRebindFinalHandoverTerminalStore, error) {
	if generation == 0 || generation == math.MaxUint64 || !validCanonicalUUID(operationID) {
		return nil, errors.New("invalid generated ingress rebind terminal store")
	}
	directory, err := newStateStore(dataRoot)
	if err != nil {
		return nil, err
	}
	name, purpose := gatewayRebindFinalHandoverTerminalName(generation, operationID)
	return &gatewayRebindFinalHandoverTerminalStore{directory: directory, dataRoot: dataRoot,
		generation: generation, operationID: operationID, path: filepath.Join(directory.root, name), purpose: purpose}, nil
}

func gatewayRebindFinalHandoverTerminalName(generation uint64, operationID string) (string, string) {
	token := fmt.Sprintf("%0*d.%s", gatewayV2GenerationDigits, generation, operationID)
	scope := fmt.Sprintf("generation/%0*d/%s", gatewayV2GenerationDigits, generation, operationID)
	return gatewayRebindFinalHandoverTerminalFilenamePrefix + token + ".bundle",
		gatewayRebindFinalHandoverTerminalPurpose + "/" + scope
}

func (s *gatewayRebindFinalHandoverTerminalStore) load() (gatewayRebindFinalHandoverTerminalReceipt, error) {
	if s == nil || s.directory == nil {
		return gatewayRebindFinalHandoverTerminalReceipt{}, errors.New("invalid generated ingress rebind terminal store")
	}
	var value gatewayRebindFinalHandoverTerminalReceipt
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.readStrict(s.path, s.purpose, maxGatewayRebindFinalHandoverTerminalBytes, &value); err != nil ||
		value.Generation != s.generation || value.OperationID != s.operationID ||
		!validGatewayRebindFinalHandoverTerminalReceiptValue(value) {
		return gatewayRebindFinalHandoverTerminalReceipt{}, errors.New("invalid generated ingress rebind terminal receipt")
	}
	return value, nil
}

func (s *gatewayRebindFinalHandoverTerminalStore) installExact(ctx context.Context,
	value gatewayRebindFinalHandoverTerminalReceipt,
) error {
	if s == nil || s.directory == nil || ctx == nil || ctx.Err() != nil ||
		s.generation != value.Generation || s.operationID != value.OperationID ||
		!validGatewayRebindFinalHandoverTerminalReceiptValue(value) {
		return errors.New("invalid generated ingress rebind terminal install")
	}
	manager := &Manager{store: s.directory, options: Options{DataRoot: s.dataRoot}}
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !gatewayRebindFinalHandoverTerminalInstallPermitted(history, value) {
		return errors.New("generated ingress rebind terminal install is not the current exact replay")
	}
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.writeExact(s.path, s.purpose, value, true, maxGatewayRebindFinalHandoverTerminalBytes); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	installed, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(installed.Terminals) != 1 || !reflect.DeepEqual(installed.Terminals[0].Receipt, value) {
		return errors.New("generated ingress rebind terminal readback mismatch")
	}
	return nil
}

func gatewayRebindFinalHandoverTerminalInstallPermitted(history gatewayRebindProtectedIntentHistory,
	value gatewayRebindFinalHandoverTerminalReceipt,
) bool {
	if len(history.Terminals) == 1 {
		return reflect.DeepEqual(history.Terminals[0].Receipt, value)
	}
	if len(history.Terminals) != 0 {
		return false
	}
	createdAt, err := parseGatewayRebindProgressTime(value.CreatedAt)
	if err != nil {
		return false
	}
	expected, err := gatewayRebindFinalHandoverTerminalReceiptFor(history, createdAt)
	return err == nil && reflect.DeepEqual(expected, value)
}
