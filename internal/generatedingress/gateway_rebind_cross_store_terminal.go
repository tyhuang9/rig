package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

func gatewayRebindTerminalArtifactV2(path string) bool {
	return strings.HasPrefix(filepath.Base(path), gatewayRebindTerminalFilenamePrefixV2)
}

const (
	gatewayRebindTerminalVersionV2          = 2
	gatewayRebindTerminalPurposeV2          = "hostd/generated-ingress/rebind/terminal/v2"
	gatewayRebindTerminalFilenamePrefixV2   = "gateway-rebind-terminal.v2.g"
	gatewayRebindNoEffectProofVersion       = 1
	gatewayRebindNoEffectProofPurpose       = "hostd/generated-ingress/rebind/no-successor-effects/v1"
	gatewayRebindTerminalPhaseNoEffectAbort = "preintent_no_effects"
)

// gatewayRebindNoEffectAbortProof is produced only while the deployment
// effects lease and both gateway locks are held. ObservationDigest names two
// equal, complete Docker/host inventories proving every deterministic
// successor resource absent and the successor listener unbound.
type gatewayRebindNoEffectAbortProof struct {
	Version                     int    `json:"version"`
	Purpose                     string `json:"purpose"`
	Generation                  uint64 `json:"generation"`
	OperationID                 string `json:"operationId"`
	ClaimRequestDigest          string `json:"claimRequestDigest"`
	PredecessorCheckpointDigest string `json:"predecessorCheckpointDigest"`
	SourceStateDigest           string `json:"sourceStateDigest"`
	SuccessorIdentityDigest     string `json:"successorIdentityDigest"`
	ObservationDigest           string `json:"observationDigest"`
	CreatedAt                   string `json:"createdAt"`
	Digest                      string `json:"digest"`
}

// gatewayRebindTerminalReceiptV2 is the genuine typed-source terminal. It is
// distinct from the immutable v1 receipt and never contains a migration-026
// journal projection. Exactly one outcome proof is present.
type gatewayRebindTerminalReceiptV2 struct {
	Version                 int                                         `json:"version"`
	Purpose                 string                                      `json:"purpose"`
	Generation              uint64                                      `json:"generation"`
	OperationID             string                                      `json:"operationId"`
	Disposition             appaccess.GatewayRebindTerminalDisposition  `json:"disposition"`
	ClaimRequestDigest      string                                      `json:"claimRequestDigest"`
	ClaimSpecDigest         string                                      `json:"claimSpecDigest"`
	PreparedDatabaseDigest  string                                      `json:"preparedDatabaseDigest"`
	Predecessor             appaccess.GatewayRebindSourceRef            `json:"predecessor"`
	ProtectedIntentDigest   string                                      `json:"protectedIntentDigest,omitempty"`
	ProtectedPhase          string                                      `json:"protectedPhase"`
	ProtectedRecordSequence uint64                                      `json:"protectedRecordSequence"`
	ProtectedRecordDigest   string                                      `json:"protectedRecordDigest"`
	SuccessorProfile        gatewayRebindSuccessorIntentProfile         `json:"successorProfile"`
	SuccessorIdentity       gatewayRebindSuccessorIdentity              `json:"successorIdentity"`
	RosterDigest            string                                      `json:"rosterDigest"`
	RosterCount             int64                                       `json:"rosterCount"`
	RosterEntryDigests      []string                                    `json:"rosterEntryDigests"`
	Resources               *gatewayRebindFinalHandoverResourceBindings `json:"resources,omitempty"`
	PhysicalProof           *gatewayRebindFinalHandoverTerminalProof    `json:"physicalProof,omitempty"`
	NoEffectProof           *gatewayRebindNoEffectAbortProof            `json:"noEffectProof,omitempty"`
	RollbackProof           *gatewayRebindTypedRollbackProgress         `json:"rollbackProof,omitempty"`
	CreatedAt               string                                      `json:"createdAt"`
	Digest                  string                                      `json:"digest"`
}

type gatewayRebindTerminalStoreV2 struct {
	directory   *stateStore
	dataRoot    string
	generation  uint64
	operationID string
	path        string
	purpose     string
}

type gatewayRebindTerminalSelectionV2 struct {
	Store      *gatewayRebindTerminalStoreV2
	Generation uint64
	Receipt    gatewayRebindTerminalReceiptV2
	Existing   bool
}

func gatewayRebindNoEffectAbortProofDigest(value gatewayRebindNoEffectAbortProof) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func validGatewayRebindNoEffectAbortProof(value gatewayRebindNoEffectAbortProof) bool {
	if value.Version != gatewayRebindNoEffectProofVersion || value.Purpose != gatewayRebindNoEffectProofPurpose ||
		value.Generation == 0 || value.Generation > math.MaxInt64 || !validCanonicalUUID(value.OperationID) ||
		!validSHA256(value.ClaimRequestDigest) || !validSHA256(value.PredecessorCheckpointDigest) ||
		!validSHA256(value.SourceStateDigest) || !validSHA256(value.SuccessorIdentityDigest) ||
		!validSHA256(value.ObservationDigest) || !validSHA256(value.Digest) {
		return false
	}
	if _, err := parseGatewayRebindProgressTime(value.CreatedAt); err != nil {
		return false
	}
	digest, err := gatewayRebindNoEffectAbortProofDigest(value)
	return err == nil && digest == value.Digest
}

func gatewayRebindTerminalDigestV2(value gatewayRebindTerminalReceiptV2) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func validGatewayRebindTerminalReceiptV2(value gatewayRebindTerminalReceiptV2) bool {
	if value.Version != gatewayRebindTerminalVersionV2 || value.Purpose != gatewayRebindTerminalPurposeV2 ||
		value.Generation == 0 || value.Generation > math.MaxInt64 || !validCanonicalUUID(value.OperationID) ||
		!validSHA256(value.ClaimRequestDigest) || !validSHA256(value.ClaimSpecDigest) ||
		!validSHA256(value.PreparedDatabaseDigest) || value.Predecessor.PredecessorCheckpointDigest == "" ||
		value.Generation <= value.Predecessor.Lineage.ProtectedGeneration || value.ProtectedPhase == "" ||
		!validSHA256(value.ProtectedRecordDigest) || !validGatewayRebindSuccessorIntentProfile(value.SuccessorProfile) ||
		!validGatewayRebindSuccessorIdentityValue(value.SuccessorIdentity) || !validSHA256(value.RosterDigest) ||
		value.RosterCount < 0 || value.RosterCount != int64(len(value.RosterEntryDigests)) || !validSHA256(value.Digest) {
		return false
	}
	for _, digest := range value.RosterEntryDigests {
		if !validSHA256(digest) {
			return false
		}
	}
	if value.SuccessorIdentity.Generation != value.Generation || value.SuccessorIdentity.OperationID != value.OperationID ||
		value.SuccessorIdentity.ProfileRevisionID != value.SuccessorProfile.RevisionID ||
		value.SuccessorIdentity.ProfileRevisionNumber != value.SuccessorProfile.RevisionNumber ||
		value.SuccessorIdentity.ProfileSpecDigest != value.SuccessorProfile.SpecDigest {
		return false
	}
	if _, err := parseGatewayRebindProgressTime(value.CreatedAt); err != nil {
		return false
	}
	switch value.Disposition {
	case appaccess.GatewayRebindDispositionCommit:
		if !validSHA256(value.ProtectedIntentDigest) || value.ProtectedPhase != string(gatewayRebindProgressHandoverCommitted) ||
			value.ProtectedRecordSequence != 17 ||
			value.Resources == nil || value.PhysicalProof == nil || value.NoEffectProof != nil || value.RollbackProof != nil ||
			!validGatewayRebindFinalHandoverResourceBindingsValue(*value.Resources) ||
			!validGatewayRebindFinalHandoverOutcomeValue(*value.PhysicalProof) ||
			value.PhysicalProof.Kind != gatewayRebindFinalHandoverOutcomeCommit {
			return false
		}
	case appaccess.GatewayRebindDispositionAbort:
		if value.Resources != nil || value.PhysicalProof != nil {
			return false
		}
		switch {
		case value.ProtectedIntentDigest == "":
			if value.ProtectedPhase != gatewayRebindTerminalPhaseNoEffectAbort || value.ProtectedRecordSequence != 1 ||
				value.NoEffectProof == nil || value.RollbackProof != nil ||
				!validGatewayRebindNoEffectAbortProof(*value.NoEffectProof) ||
				value.NoEffectProof.Generation != value.Generation || value.NoEffectProof.OperationID != value.OperationID ||
				value.NoEffectProof.Digest != value.ProtectedRecordDigest ||
				value.NoEffectProof.ClaimRequestDigest != value.ClaimRequestDigest ||
				value.NoEffectProof.PredecessorCheckpointDigest != value.Predecessor.PredecessorCheckpointDigest ||
				value.NoEffectProof.SourceStateDigest != value.Predecessor.SourceStateDigest ||
				value.NoEffectProof.SuccessorIdentityDigest != value.SuccessorIdentity.Digest {
				return false
			}
		case validSHA256(value.ProtectedIntentDigest):
			if value.ProtectedPhase != string(gatewayRebindProgressHandoverRolledBack) ||
				value.ProtectedRecordSequence < 3 || value.ProtectedRecordSequence > 18 ||
				value.NoEffectProof != nil || value.RollbackProof == nil ||
				!validGatewayRebindTypedRollbackProgress(*value.RollbackProof) ||
				value.RollbackProof.PhysicalProof == nil {
				return false
			}
		default:
			return false
		}
	default:
		return false
	}
	digest, err := gatewayRebindTerminalDigestV2(value)
	return err == nil && digest == value.Digest
}

func newGatewayRebindNoEffectAbortTerminalV2(claim appaccess.GatewayRebindClaimV2,
	roster []appaccess.GatewayRebindRosterEntryV2, runtimeHeads []appaccess.GatewayRebindRuntimeHead,
	checkpoint gatewayRebindPredecessorCheckpoint,
	proof gatewayRebindNoEffectAbortProof, createdAt time.Time,
) (gatewayRebindTerminalReceiptV2, error) {
	invalid := errors.New("invalid generated ingress no-effect rebind terminal")
	roster = append([]appaccess.GatewayRebindRosterEntryV2(nil), roster...)
	runtimeHeads = append([]appaccess.GatewayRebindRuntimeHead(nil), runtimeHeads...)
	if !validGatewayRebindPredecessorCheckpoint(checkpoint) || !validGatewayRebindNoEffectAbortProof(proof) ||
		claim.Spec.OperationID != checkpoint.OperationID || claim.Spec.Predecessor != checkpoint.sourceRef() ||
		claim.Spec.SuccessorProtectedGeneration != checkpoint.Generation || claim.RequestDigest != proof.ClaimRequestDigest ||
		proof.PredecessorCheckpointDigest != checkpoint.Digest || proof.SourceStateDigest != checkpoint.SourceStateDigest ||
		!createdAt.After(claim.CreatedAt) {
		return gatewayRebindTerminalReceiptV2{}, invalid
	}
	proofAt, err := parseGatewayRebindProgressTime(proof.CreatedAt)
	if err != nil || !createdAt.After(proofAt) {
		return gatewayRebindTerminalReceiptV2{}, invalid
	}
	profile := gatewayRebindSuccessorIntentProfile{
		RevisionID: claim.Spec.SuccessorProfileRevisionID, RevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
		OperationID: claim.Spec.SuccessorProfileOperationID, RequestDigest: claim.SuccessorProfileRequestDigest,
		SpecDigest: claim.ConfigureApproval.SpecDigest, SelectedIPv4: claim.Spec.SuccessorProfile.SelectedIPv4,
		InterfaceID: claim.Spec.SuccessorProfile.InterfaceID, PortStart: claim.Spec.SuccessorProfile.PortStart,
		PortEnd: claim.Spec.SuccessorProfile.PortEnd, ApprovedBy: claim.ConfigureApproval.ActorID,
	}
	identity, err := newGatewayRebindSuccessorIdentity(checkpoint.Generation, claim.Spec.OperationID,
		GatewayRebindSuccessorProfile(profile))
	if err != nil || proof.SuccessorIdentityDigest != identity.Digest {
		return gatewayRebindTerminalReceiptV2{}, invalid
	}
	rosterDigest, err := appaccess.GatewayRebindRosterV2Digest(roster)
	if err != nil || rosterDigest != claim.Spec.RosterDigest || int64(len(roster)) != claim.Spec.RosterCount {
		return gatewayRebindTerminalReceiptV2{}, invalid
	}
	runtimeHeadsDigest, err := appaccess.GatewayRebindRuntimeHeadsV2Digest(claim.Spec.OperationID, runtimeHeads)
	if err != nil || claim.Spec.RuntimeHeadsVersion != appaccess.GatewayRebindRuntimeHeadsVersionV1 ||
		runtimeHeadsDigest != claim.Spec.RuntimeHeadsDigest || int64(len(runtimeHeads)) != claim.Spec.RuntimeHeadsCount {
		return gatewayRebindTerminalReceiptV2{}, invalid
	}
	entryDigests := make([]string, len(roster))
	for i := range roster {
		entryDigests[i] = roster[i].EntryDigest
	}
	value := gatewayRebindTerminalReceiptV2{
		Version: gatewayRebindTerminalVersionV2, Purpose: gatewayRebindTerminalPurposeV2,
		Generation: checkpoint.Generation, OperationID: claim.Spec.OperationID, Disposition: appaccess.GatewayRebindDispositionAbort,
		ClaimRequestDigest: claim.RequestDigest, ClaimSpecDigest: claim.RebindApproval.SpecDigest,
		PreparedDatabaseDigest: checkpoint.Digest, Predecessor: checkpoint.sourceRef(),
		ProtectedPhase: gatewayRebindTerminalPhaseNoEffectAbort, ProtectedRecordSequence: 1,
		ProtectedRecordDigest: proof.Digest,
		SuccessorProfile:      profile, SuccessorIdentity: identity, RosterDigest: rosterDigest,
		RosterCount: int64(len(roster)), RosterEntryDigests: entryDigests, NoEffectProof: &proof,
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
	}
	// PreparedDatabaseDigest binds the exact retained SQL claim, LAN roster,
	// and complete runtime-head census rather than inventing an intent. This is
	// the same canonical shape used by V2 intent.
	value.PreparedDatabaseDigest, err = gatewayRebindPreparedDatabaseDigest(claim, roster, runtimeHeads)
	if err != nil {
		return gatewayRebindTerminalReceiptV2{}, invalid
	}
	value.Digest, err = gatewayRebindTerminalDigestV2(value)
	if err != nil || !validGatewayRebindTerminalReceiptV2(value) {
		return gatewayRebindTerminalReceiptV2{}, invalid
	}
	return value, nil
}

func newGatewayRebindCommitTerminalV2(intent gatewayRebindProtectedIntentV2,
	last gatewayRebindProgressRecord, resources gatewayRebindFinalHandoverResourceBindings,
	proof gatewayRebindFinalHandoverTerminalProof, createdAt time.Time,
) (gatewayRebindTerminalReceiptV2, error) {
	invalid := errors.New("invalid generated ingress typed commit terminal")
	if !validGatewayRebindProtectedIntentV2(intent) || !validGatewayRebindProgressRecord(last) ||
		last.Generation != intent.Generation || last.OperationID != intent.OperationID ||
		last.ProtectedIntentDigest != intent.Digest || last.Sequence != 17 ||
		last.Phase != gatewayRebindProgressHandoverCommitted || last.TypedEffect == nil ||
		last.TypedEffect.Resources == nil || last.TypedEffect.PhysicalProof == nil ||
		!reflect.DeepEqual(*last.TypedEffect.Resources, resources) || !reflect.DeepEqual(*last.TypedEffect.PhysicalProof, proof) ||
		!validGatewayRebindFinalHandoverResourceBindingsValue(resources) ||
		!validGatewayRebindFinalHandoverOutcomeValue(proof) || proof.Kind != gatewayRebindFinalHandoverOutcomeCommit ||
		!validGatewayRebindProgressTime(createdAt) {
		return gatewayRebindTerminalReceiptV2{}, invalid
	}
	lastAt, err := parseGatewayRebindProgressTime(last.OccurredAt)
	if err != nil || !createdAt.After(lastAt) {
		return gatewayRebindTerminalReceiptV2{}, invalid
	}
	value := gatewayRebindTerminalReceiptV2{
		Version: gatewayRebindTerminalVersionV2, Purpose: gatewayRebindTerminalPurposeV2,
		Generation: intent.Generation, OperationID: intent.OperationID,
		Disposition:        appaccess.GatewayRebindDispositionCommit,
		ClaimRequestDigest: intent.Claim.RequestDigest, ClaimSpecDigest: intent.Claim.RebindApproval.SpecDigest,
		PreparedDatabaseDigest: intent.DatabaseDigest, Predecessor: intent.Predecessor,
		ProtectedIntentDigest: intent.Digest, ProtectedPhase: string(last.Phase),
		ProtectedRecordSequence: last.Sequence, ProtectedRecordDigest: last.Digest,
		SuccessorProfile: intent.SuccessorProfile, SuccessorIdentity: intent.Identity,
		RosterDigest: intent.Claim.Spec.RosterDigest, RosterCount: intent.Claim.Spec.RosterCount,
		RosterEntryDigests: append([]string{}, intent.RosterEntryDigests...),
		Resources:          &resources, PhysicalProof: &proof,
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
	}
	value.Digest, err = gatewayRebindTerminalDigestV2(value)
	if err != nil || !validGatewayRebindTerminalReceiptV2(value) {
		return gatewayRebindTerminalReceiptV2{}, invalid
	}
	return value, nil
}

func gatewayRebindTypedCheckpointRoutesDigest(checkpoint gatewayRebindPredecessorCheckpoint) (string, error) {
	switch {
	case checkpoint.UpgradeState != nil && checkpoint.CurrentState == nil:
		return canonicalDigest(struct {
			Purpose string                       `json:"purpose"`
			Apps    map[string]gatewayV2AppRoute `json:"apps"`
		}{"hostd/generated-ingress/rebind/typed-rollback-predecessor-routes/v1", checkpoint.UpgradeState.Apps})
	case checkpoint.UpgradeState == nil && checkpoint.CurrentState != nil:
		return canonicalDigest(struct {
			Purpose string                            `json:"purpose"`
			Apps    map[string]gatewayCurrentAppRoute `json:"apps"`
		}{"hostd/generated-ingress/rebind/typed-rollback-predecessor-routes/v1", checkpoint.CurrentState.Apps})
	default:
		return "", errors.New("invalid typed rollback predecessor checkpoint")
	}
}

func newGatewayRebindRollbackTerminalV2(intent gatewayRebindProtectedIntentV2,
	last gatewayRebindProgressRecord, checkpoint gatewayRebindPredecessorCheckpoint, createdAt time.Time,
) (gatewayRebindTerminalReceiptV2, error) {
	invalid := errors.New("invalid generated ingress typed rollback terminal")
	if !validGatewayRebindProtectedIntentV2(intent) || !validGatewayRebindPredecessorCheckpoint(checkpoint) ||
		intent.Predecessor != checkpoint.sourceRef() || !validGatewayRebindProgressRecord(last) ||
		last.Generation != intent.Generation || last.OperationID != intent.OperationID ||
		last.ProtectedIntentDigest != intent.Digest || last.Phase != gatewayRebindProgressHandoverRolledBack ||
		last.TypedRollback == nil || last.TypedRollback.PhysicalProof == nil || !validGatewayRebindProgressTime(createdAt) {
		return gatewayRebindTerminalReceiptV2{}, invalid
	}
	lastAt, err := parseGatewayRebindProgressTime(last.OccurredAt)
	routesDigest, routesErr := gatewayRebindTypedCheckpointRoutesDigest(checkpoint)
	if err != nil || !createdAt.After(lastAt) || routesErr != nil ||
		last.TypedRollback.PredecessorRoutesDigest != routesDigest {
		return gatewayRebindTerminalReceiptV2{}, invalid
	}
	rollback := *last.TypedRollback
	entryDigests := append([]string{}, intent.RosterEntryDigests...)
	value := gatewayRebindTerminalReceiptV2{
		Version: gatewayRebindTerminalVersionV2, Purpose: gatewayRebindTerminalPurposeV2,
		Generation: intent.Generation, OperationID: intent.OperationID,
		Disposition:        appaccess.GatewayRebindDispositionAbort,
		ClaimRequestDigest: intent.Claim.RequestDigest, ClaimSpecDigest: intent.Claim.RebindApproval.SpecDigest,
		PreparedDatabaseDigest: intent.DatabaseDigest, Predecessor: intent.Predecessor,
		ProtectedIntentDigest: intent.Digest, ProtectedPhase: string(last.Phase),
		ProtectedRecordSequence: last.Sequence, ProtectedRecordDigest: last.Digest,
		SuccessorProfile: intent.SuccessorProfile, SuccessorIdentity: intent.Identity,
		RosterDigest: intent.Claim.Spec.RosterDigest, RosterCount: intent.Claim.Spec.RosterCount,
		RosterEntryDigests: entryDigests, RollbackProof: &rollback,
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
	}
	value.Digest, err = gatewayRebindTerminalDigestV2(value)
	if err != nil || !validGatewayRebindTerminalReceiptV2(value) {
		return gatewayRebindTerminalReceiptV2{}, invalid
	}
	return value, nil
}

func gatewayRebindTerminalV2MatchesIntentHistory(value gatewayRebindTerminalReceiptV2,
	intent gatewayRebindProtectedIntentV2, checkpoint gatewayRebindPredecessorCheckpoint,
	progress []gatewayRebindProgressSelection,
) bool {
	if !validGatewayRebindTerminalReceiptV2(value) ||
		value.Generation != intent.Generation || value.OperationID != intent.OperationID ||
		value.ClaimRequestDigest != intent.Claim.RequestDigest ||
		value.ClaimSpecDigest != intent.Claim.RebindApproval.SpecDigest ||
		value.PreparedDatabaseDigest != intent.DatabaseDigest || value.Predecessor != intent.Predecessor ||
		value.Predecessor != checkpoint.sourceRef() || value.ProtectedIntentDigest != intent.Digest ||
		value.SuccessorProfile != intent.SuccessorProfile || value.SuccessorIdentity != intent.Identity ||
		value.RosterDigest != intent.Claim.Spec.RosterDigest || value.RosterCount != intent.Claim.Spec.RosterCount ||
		!sameGatewayRebindDigestList(value.RosterEntryDigests, intent.RosterEntryDigests) || len(progress) == 0 {
		return false
	}
	last := progress[len(progress)-1].Record
	if value.ProtectedRecordSequence != last.Sequence || value.ProtectedPhase != string(last.Phase) ||
		value.ProtectedRecordDigest != last.Digest || !gatewayRebindTerminalCreatedAfterProgress(value.CreatedAt, last.OccurredAt) {
		return false
	}
	switch value.Disposition {
	case appaccess.GatewayRebindDispositionCommit:
		return len(progress) == 17 && last.Sequence == 17 && last.Phase == gatewayRebindProgressHandoverCommitted &&
			last.TypedEffect != nil && last.TypedEffect.Resources != nil && last.TypedEffect.PhysicalProof != nil &&
			value.Resources != nil && value.PhysicalProof != nil && value.RollbackProof == nil &&
			reflect.DeepEqual(*value.Resources, *last.TypedEffect.Resources) &&
			reflect.DeepEqual(*value.PhysicalProof, *last.TypedEffect.PhysicalProof)
	case appaccess.GatewayRebindDispositionAbort:
		routesDigest, err := gatewayRebindTypedCheckpointRoutesDigest(checkpoint)
		return err == nil && len(progress) >= 3 && len(progress) <= 18 &&
			last.Phase == gatewayRebindProgressHandoverRolledBack && last.TypedRollback != nil &&
			last.TypedRollback.PhysicalProof != nil && value.RollbackProof != nil &&
			last.TypedRollback.PredecessorRoutesDigest == routesDigest &&
			reflect.DeepEqual(*value.RollbackProof, *last.TypedRollback)
	default:
		return false
	}
}

func sameGatewayRebindDigestList(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func gatewayRebindTerminalCreatedAfterProgress(createdAt, progressAt string) bool {
	created, createdErr := parseGatewayRebindProgressTime(createdAt)
	progress, progressErr := parseGatewayRebindProgressTime(progressAt)
	return createdErr == nil && progressErr == nil && created.After(progress)
}

func gatewayRebindTimeStrictlyAfter(candidate time.Time, prior string) time.Time {
	priorAt, err := parseGatewayRebindProgressTime(prior)
	if err == nil && !candidate.After(priorAt) {
		return priorAt.Add(time.Nanosecond)
	}
	return candidate
}

func gatewayRebindTerminalV2MatchesNoIntentHistory(value gatewayRebindTerminalReceiptV2,
	checkpoint gatewayRebindPredecessorCheckpoint,
) bool {
	if value.NoEffectProof == nil || !gatewayRebindTerminalCreatedAfterProgress(value.CreatedAt, value.NoEffectProof.CreatedAt) {
		return false
	}
	return validGatewayRebindTerminalReceiptV2(value) && value.Disposition == appaccess.GatewayRebindDispositionAbort &&
		value.OperationID == checkpoint.OperationID && value.Generation == checkpoint.Generation &&
		value.Predecessor == checkpoint.sourceRef() && value.NoEffectProof != nil
}

func gatewayRebindCurrentLineageV2(receipt gatewayRebindTerminalReceiptV2) (appaccess.GatewayCurrentLineageRef, error) {
	if !validGatewayRebindTerminalReceiptV2(receipt) || receipt.Disposition != appaccess.GatewayRebindDispositionCommit {
		return appaccess.GatewayCurrentLineageRef{}, errors.New("invalid typed current rebind lineage")
	}
	return appaccess.GatewayCurrentLineageRef{
		Kind: appaccess.GatewayRebindSourceGatewayRebind, OperationID: receipt.OperationID,
		ProfileRevisionID:     receipt.SuccessorProfile.RevisionID,
		ProfileRevisionNumber: receipt.SuccessorProfile.RevisionNumber,
		ProfileSpecDigest:     receipt.SuccessorProfile.SpecDigest,
		ProtectedGeneration:   receipt.Generation, ProtectedIdentityDigest: receipt.SuccessorIdentity.Digest,
		ProtectedIntentDigest: receipt.ProtectedIntentDigest, TerminalReceiptDigest: receipt.Digest,
	}, nil
}

func newGatewayRebindTerminalStoreV2(dataRoot string, generation uint64, operationID string) (*gatewayRebindTerminalStoreV2, error) {
	if generation == 0 || generation > math.MaxInt64 || !validCanonicalUUID(operationID) {
		return nil, errors.New("invalid typed rebind terminal store")
	}
	directory, err := newStateStore(dataRoot)
	if err != nil {
		return nil, err
	}
	name, purpose := gatewayRebindTerminalNameV2(generation, operationID)
	return &gatewayRebindTerminalStoreV2{directory: directory, dataRoot: dataRoot, generation: generation,
		operationID: operationID, path: filepath.Join(directory.root, name), purpose: purpose}, nil
}

func gatewayRebindTerminalNameV2(generation uint64, operationID string) (string, string) {
	token := fmt.Sprintf("%0*d.%s", gatewayV2GenerationDigits, generation, operationID)
	scope := fmt.Sprintf("generation/%0*d/%s", gatewayV2GenerationDigits, generation, operationID)
	return gatewayRebindTerminalFilenamePrefixV2 + token + ".bundle", gatewayRebindTerminalPurposeV2 + "/" + scope
}

func (s *gatewayRebindTerminalStoreV2) load() (gatewayRebindTerminalReceiptV2, error) {
	if s == nil || s.directory == nil {
		return gatewayRebindTerminalReceiptV2{}, errors.New("invalid typed rebind terminal store")
	}
	var value gatewayRebindTerminalReceiptV2
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.readStrict(s.path, s.purpose, maxGatewayRebindFinalHandoverTerminalBytes, &value); err != nil ||
		value.Generation != s.generation || value.OperationID != s.operationID || !validGatewayRebindTerminalReceiptV2(value) {
		return gatewayRebindTerminalReceiptV2{}, errors.New("invalid typed rebind terminal receipt")
	}
	return value, nil
}

func (s *gatewayRebindTerminalStoreV2) installExact(ctx context.Context, value gatewayRebindTerminalReceiptV2) error {
	if s == nil || s.directory == nil || ctx == nil || ctx.Err() != nil || s.generation != value.Generation ||
		s.operationID != value.OperationID || !validGatewayRebindTerminalReceiptV2(value) {
		return errors.New("invalid typed rebind terminal install")
	}
	if installed, err := s.load(); err == nil {
		if reflect.DeepEqual(installed, value) {
			return nil
		}
		return errors.New("typed rebind terminal decision conflicts")
	}
	manager := &Manager{store: s.directory, options: Options{DataRoot: s.dataRoot}}
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !gatewayRebindTerminalV2InstallPermitted(history, value) {
		return errors.New("typed rebind terminal install is not the current exact decision")
	}
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.writeExact(s.path, s.purpose, value, true, maxGatewayRebindFinalHandoverTerminalBytes); err != nil {
		return err
	}
	installed, err := s.load()
	if err != nil || !reflect.DeepEqual(installed, value) {
		return errors.New("typed rebind terminal readback mismatch")
	}
	confirmed, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(confirmed.TerminalsV2) == 0 ||
		!reflect.DeepEqual(confirmed.TerminalsV2[len(confirmed.TerminalsV2)-1].Receipt, value) {
		return errors.New("typed rebind terminal protected history mismatch")
	}
	return ctx.Err()
}

func gatewayRebindTerminalV2InstallPermitted(history gatewayRebindProtectedIntentHistory,
	value gatewayRebindTerminalReceiptV2,
) bool {
	for _, terminal := range history.TerminalsV2 {
		if terminal.Generation == value.Generation || terminal.Receipt.OperationID == value.OperationID {
			return reflect.DeepEqual(terminal.Receipt, value)
		}
	}
	var checkpoint *gatewayRebindPredecessorCheckpoint
	for index := range history.Checkpoints {
		candidate := &history.Checkpoints[index].Checkpoint
		if candidate.Generation == value.Generation && candidate.OperationID == value.OperationID {
			if checkpoint != nil {
				return false
			}
			checkpoint = candidate
		}
	}
	if checkpoint == nil {
		return false
	}
	if value.Disposition == appaccess.GatewayRebindDispositionAbort && value.ProtectedIntentDigest == "" {
		for _, intent := range history.IntentsV2 {
			if intent.Generation == value.Generation || intent.Intent.OperationID == value.OperationID {
				return false
			}
		}
		for _, intent := range history.Intents {
			if intent.Generation == value.Generation || intent.Intent.OperationID == value.OperationID {
				return false
			}
		}
		for _, record := range history.Progress {
			if record.Generation == value.Generation || record.Record.OperationID == value.OperationID {
				return false
			}
		}
		return gatewayRebindTerminalV2MatchesNoIntentHistory(value, *checkpoint)
	}
	var intent *gatewayRebindProtectedIntentV2
	for index := range history.IntentsV2 {
		candidate := &history.IntentsV2[index].Intent
		if candidate.Generation == value.Generation && candidate.OperationID == value.OperationID {
			if intent != nil {
				return false
			}
			intent = candidate
		}
	}
	if intent == nil {
		return false
	}
	progress := make([]gatewayRebindProgressSelection, 0)
	for _, record := range history.Progress {
		if record.Generation == value.Generation && record.Record.OperationID == value.OperationID {
			progress = append(progress, record)
		}
	}
	return gatewayRebindTerminalV2MatchesIntentHistory(value, *intent, *checkpoint, progress)
}
