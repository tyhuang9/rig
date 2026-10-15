package appaccess

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"
)

const (
	GatewayRebindReconcileVersionV1 = 1
	GatewayRebindReconcilePurpose   = "reconcile_lan_gateway"
	gatewayRebindAuditResourceType  = "lan_gateway_rebind"
)

type GatewayRebindRecoveryPolicy string

const (
	GatewayRebindRecoveryEvidenceDecides GatewayRebindRecoveryPolicy = "evidence_decides"
	GatewayRebindRecoveryForwardOnly     GatewayRebindRecoveryPolicy = "forward_only"
	GatewayRebindRecoveryRollbackOnly    GatewayRebindRecoveryPolicy = "rollback_only"
)

type GatewayRebindProtectedEvidencePresence string

const (
	GatewayRebindProtectedEvidenceAbsent             GatewayRebindProtectedEvidencePresence = "validated_absent"
	GatewayRebindProtectedEvidencePresent            GatewayRebindProtectedEvidencePresence = "validated_present"
	GatewayRebindProtectedEvidenceCheckpointTerminal GatewayRebindProtectedEvidencePresence = "validated_checkpoint_terminal"
)

// GatewayRebindProtectedRecoveryEvidence is a projection supplied only after
// generated ingress has validated the complete protected scan. Validated
// absence means that the scan contained no intent, progress, or terminal for
// this exact operation; it is not proof that no Docker or filesystem effect
// exists. A checkpoint terminal binds an exact no-intent abort receipt without
// pretending that an intent or progress record existed.
type GatewayRebindProtectedRecoveryEvidence struct {
	Presence              GatewayRebindProtectedEvidencePresence `json:"presence"`
	OperationID           string                                 `json:"operationId"`
	Generation            uint64                                 `json:"generation"`
	ScanDigest            string                                 `json:"scanDigest"`
	IntentDigest          string                                 `json:"intentDigest,omitempty"`
	RecordSequence        uint64                                 `json:"recordSequence"`
	RecordDigest          string                                 `json:"recordDigest,omitempty"`
	FixedDisposition      GatewayRebindTerminalDisposition       `json:"fixedDisposition"`
	TerminalReceiptDigest string                                 `json:"terminalReceiptDigest,omitempty"`
}

// GatewayRebindSelectedCurrentEvidence is the protected current selection
// already validated by generated ingress. SQL can compare only the authority
// projection of Lineage; it cannot validate this protected state tuple.
type GatewayRebindSelectedCurrentEvidence struct {
	Lineage       GatewayCurrentLineageRef `json:"lineage"`
	StateVersion  uint64                   `json:"stateVersion"`
	StateRevision uint64                   `json:"stateRevision"`
	StateDigest   string                   `json:"stateDigest"`
}

// GatewayRebindTrustedRecoverySnapshot is trusted input from the locked
// generated-ingress inspection boundary. "Trusted" records provenance only:
// neither this value nor any digest returned from this package is protected,
// Docker, or effect authority.
type GatewayRebindTrustedRecoverySnapshot struct {
	Protected       GatewayRebindProtectedRecoveryEvidence `json:"protected"`
	SelectedCurrent GatewayRebindSelectedCurrentEvidence   `json:"selectedCurrent"`
}

// GatewayRebindReconcileSnapshot is the canonical advisory snapshot shared by
// preview and lock-held authorization. Authorization always rebuilds this
// value from a fresh SQL transaction and freshly validated trusted evidence.
type GatewayRebindReconcileSnapshot struct {
	Version                       int                                  `json:"version"`
	Purpose                       string                               `json:"purpose"`
	Claim                         GatewayRebindClaimV2                 `json:"claim"`
	DatabaseCommittedEvent        *GatewayRebindEvent                  `json:"databaseCommittedEvent,omitempty"`
	CurrentDatabaseCommittedEvent *GatewayRebindEvent                  `json:"currentDatabaseCommittedEvent,omitempty"`
	RetainedCommand               *GatewayRebindTransitionCommand      `json:"retainedCommand,omitempty"`
	CurrentProfile                *GatewayProfileRevision              `json:"currentProfile"`
	CurrentSource                 *GatewayCurrentAuthorityRef          `json:"currentSource"`
	Trusted                       GatewayRebindTrustedRecoverySnapshot `json:"trusted"`
	Policy                        GatewayRebindRecoveryPolicy          `json:"policy"`
}

type GatewayRebindReconcileReview struct {
	Snapshot               GatewayRebindReconcileSnapshot
	RecoveryDigest         string
	RecoveryApprovalDigest string
}

type GatewayRebindReconcileAuthorizationInput struct {
	OperationID            string
	ActorID                string
	RecoveryDigest         string
	RecoveryApprovalDigest string
	Trusted                GatewayRebindTrustedRecoverySnapshot
}

// GatewayRebindReconcileAuthorization proves only that this request's current
// administrator consent was audited against one exact SQL and externally
// validated snapshot. It is non-authoritative, non-replayable, and never
// permits a protected, Docker, transition, or other effect by itself.
type GatewayRebindReconcileAuthorization struct {
	Review       GatewayRebindReconcileReview
	ActorID      string
	AuditEventID int64
	AuditedAt    time.Time
}

// BuildGatewayRebindReconcileReview is the one canonical projection and digest
// builder used by preview and authorization. It validates SQL structure and
// the coherence of the typed trusted projection, but cannot establish the
// truth of protected or current evidence supplied by generated ingress.
func BuildGatewayRebindReconcileReview(snapshot GatewayRebindRecoverySnapshot,
	trusted GatewayRebindTrustedRecoverySnapshot, actorID string,
) (GatewayRebindReconcileReview, error) {
	canonical, err := buildGatewayRebindReconcileSnapshot(snapshot, trusted)
	if err != nil {
		return GatewayRebindReconcileReview{}, err
	}
	if !validUUID(actorID) {
		return GatewayRebindReconcileReview{}, ErrInvalidInput
	}
	recoveryDigest, err := digestJSON(canonical)
	if err != nil {
		return GatewayRebindReconcileReview{}, err
	}
	approvalDigest, err := gatewayRebindRecoveryApprovalDigest(canonical, recoveryDigest, actorID)
	if err != nil {
		return GatewayRebindReconcileReview{}, err
	}
	return GatewayRebindReconcileReview{
		Snapshot: canonical, RecoveryDigest: recoveryDigest,
		RecoveryApprovalDigest: approvalDigest,
	}, nil
}

// GatewayRebindRecoveryPolicyForSnapshot derives the canonical policy without
// creating actor consent. It applies the same SQL/trusted-evidence validation
// as BuildGatewayRebindReconcileReview.
func GatewayRebindRecoveryPolicyForSnapshot(snapshot GatewayRebindRecoverySnapshot,
	trusted GatewayRebindTrustedRecoverySnapshot,
) (GatewayRebindRecoveryPolicy, error) {
	canonical, err := buildGatewayRebindReconcileSnapshot(snapshot, trusted)
	if err != nil {
		return "", err
	}
	return canonical.Policy, nil
}

// ValidateGatewayRebindReconcileReview checks the canonical review and its
// actor-bound digests without granting SQL, protected-state, Docker, or effect
// authority. Callers still have to rebuild and authorize the review from fresh
// evidence at the mutation boundary.
func ValidateGatewayRebindReconcileReview(review GatewayRebindReconcileReview, actorID string) error {
	snapshot := review.Snapshot
	if !validUUID(actorID) || snapshot.Version != GatewayRebindReconcileVersionV1 ||
		snapshot.Purpose != GatewayRebindReconcilePurpose ||
		!validUUID(snapshot.Claim.Spec.OperationID) || !validDigest(review.RecoveryDigest) ||
		!validDigest(review.RecoveryApprovalDigest) || snapshot.CurrentProfile == nil ||
		snapshot.CurrentSource == nil || !validGatewayRebindProtectedEvidence(snapshot.Trusted.Protected) ||
		!validGatewayCurrentLineageRef(snapshot.Trusted.SelectedCurrent.Lineage) ||
		!validDigest(snapshot.Trusted.SelectedCurrent.StateDigest) ||
		gatewayRebindCurrentAuthorityProjection(snapshot.Trusted.SelectedCurrent.Lineage) != *snapshot.CurrentSource {
		return ErrInvalidStoredState
	}
	profileDigest, err := GatewayProfileSpecDigest(snapshot.CurrentProfile.Spec)
	if err != nil || !validUUID(snapshot.CurrentProfile.ID) || snapshot.CurrentProfile.RevisionNumber <= 0 ||
		!validUUID(snapshot.CurrentProfile.OperationID) || profileDigest != snapshot.CurrentProfile.SpecDigest ||
		snapshot.CurrentProfile.ID != snapshot.CurrentSource.ProfileRevisionID ||
		snapshot.CurrentProfile.RevisionNumber != snapshot.CurrentSource.ProfileRevisionNumber ||
		snapshot.CurrentProfile.SpecDigest != snapshot.CurrentSource.ProfileSpecDigest {
		return ErrInvalidStoredState
	}
	claim := snapshot.Claim
	specDigest, err := GatewayRebindSpecV2Digest(claim.Spec)
	successorProfileDigest, successorProfileErr := GatewayProfileSpecDigest(claim.Spec.SuccessorProfile)
	requestDigest, requestErr := gatewayRebindClaimV2RequestDigest(claim.Spec,
		claim.RebindApproval, claim.ConfigureApproval)
	if err != nil || successorProfileErr != nil || requestErr != nil || requestDigest != claim.RequestDigest ||
		claim.RebindApproval.Action != ActionRebindGateway || claim.RebindApproval.SpecDigest != specDigest ||
		claim.ConfigureApproval.Action != ActionConfigureGateway ||
		claim.ConfigureApproval.SpecDigest != successorProfileDigest ||
		!validUUID(claim.RebindApproval.ActorID) || !validUUID(claim.ConfigureApproval.ActorID) ||
		!validGatewayRebindState(claim.State) || claim.StateSequence < 1 ||
		claim.UpdatedAt.Before(claim.CreatedAt) {
		return ErrInvalidStoredState
	}
	if snapshot.DatabaseCommittedEvent != nil &&
		(snapshot.DatabaseCommittedEvent.OperationID != claim.Spec.OperationID ||
			snapshot.DatabaseCommittedEvent.State != GatewayRebindDatabaseCommitted ||
			snapshot.DatabaseCommittedEvent.Sequence <= 0 ||
			snapshot.DatabaseCommittedEvent.Sequence > claim.StateSequence) {
		return ErrInvalidStoredState
	}
	if snapshot.RetainedCommand != nil &&
		(snapshot.RetainedCommand.OperationID != claim.Spec.OperationID ||
			snapshot.RetainedCommand.Sequence <= 0 || !validDigest(snapshot.RetainedCommand.CommandDigest) ||
			snapshot.RetainedCommand.TerminalDisposition == GatewayRebindDispositionNone ||
			!validDigest(snapshot.RetainedCommand.TerminalReceiptDigest)) {
		return ErrInvalidStoredState
	}
	active := GatewayRebindHistoryEntry{Claim: GatewayRebindClaimRecord{
		SpecVersion: GatewayRebindSpecVersionV2, V2: &claim,
	}}
	rollbackAllowed := snapshot.DatabaseCommittedEvent == nil &&
		(snapshot.RetainedCommand == nil ||
			snapshot.RetainedCommand.TerminalDisposition != GatewayRebindDispositionCommit)
	recovery := GatewayRebindRecoverySnapshot{
		Active: &active, CurrentSource: snapshot.CurrentSource,
		DatabaseCommittedEvent: cloneGatewayRebindEvent(snapshot.DatabaseCommittedEvent),
		Phase:                  claim.State, DatabaseCommitObserved: snapshot.DatabaseCommittedEvent != nil,
		RollbackAllowed: rollbackAllowed,
	}
	canonicalPolicy, policyErr := gatewayRebindRecoveryPolicy(recovery,
		snapshot.RetainedCommand, snapshot.Trusted)
	switch snapshot.Policy {
	case GatewayRebindRecoveryEvidenceDecides, GatewayRebindRecoveryForwardOnly,
		GatewayRebindRecoveryRollbackOnly:
	default:
		return ErrInvalidStoredState
	}
	if policyErr != nil || canonicalPolicy != snapshot.Policy {
		return ErrInvalidStoredState
	}
	computed, err := digestJSON(snapshot)
	if err != nil || computed != review.RecoveryDigest {
		return ErrInvalidStoredState
	}
	approval, err := gatewayRebindRecoveryApprovalDigest(snapshot, computed, actorID)
	if err != nil || approval != review.RecoveryApprovalDigest {
		return ErrInvalidStoredState
	}
	return nil
}

// AuthorizeAndAuditGatewayRebindReconcile revalidates the exact SQL snapshot,
// current administrator roles, actor-bound consent, and trusted evidence while
// holding SQLite's writer reservation. The audit commits before this method
// returns. A cancellation or commit error returns a zero result even when the
// database may have retained the attempted-consent audit; callers must issue a
// fresh request and must never treat the audit row as effect authority.
func (r *Repository) AuthorizeAndAuditGatewayRebindReconcile(ctx context.Context,
	input GatewayRebindReconcileAuthorizationInput,
) (authorization GatewayRebindReconcileAuthorization, resultErr error) {
	if r == nil || r.db == nil || r.now == nil || ctx == nil ||
		!validUUID(input.OperationID) || !validUUID(input.ActorID) ||
		!validDigest(input.RecoveryDigest) || !validDigest(input.RecoveryApprovalDigest) {
		return GatewayRebindReconcileAuthorization{}, ErrInvalidInput
	}
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return GatewayRebindReconcileAuthorization{}, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			authorization = GatewayRebindReconcileAuthorization{}
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()

	snapshot, err := readGatewayRebindRecoverySnapshot(ctx, tx)
	if err != nil {
		return GatewayRebindReconcileAuthorization{}, err
	}
	review, err := BuildGatewayRebindReconcileReview(snapshot, input.Trusted, input.ActorID)
	if err != nil {
		return GatewayRebindReconcileAuthorization{}, err
	}
	if review.Snapshot.Claim.Spec.OperationID != input.OperationID ||
		review.RecoveryDigest != input.RecoveryDigest {
		return GatewayRebindReconcileAuthorization{}, ErrConflict
	}
	if review.RecoveryApprovalDigest != input.RecoveryApprovalDigest {
		return GatewayRebindReconcileAuthorization{}, ErrApprovalRequired
	}

	authority, err := gatewayRebindApprovalBinding(GatewayRebindClaimRecord{
		SpecVersion: GatewayRebindSpecVersionV2, V2: &review.Snapshot.Claim,
	})
	if err != nil {
		return GatewayRebindReconcileAuthorization{}, err
	}
	for _, actorID := range []string{input.ActorID, authority.RebindActorID, authority.ConfigureActorID} {
		administrator, roleErr := administratorExists(ctx, tx, actorID)
		if roleErr != nil {
			return GatewayRebindReconcileAuthorization{}, roleErr
		}
		if !administrator {
			return GatewayRebindReconcileAuthorization{}, ErrApprovalRequired
		}
	}
	if err := ctx.Err(); err != nil {
		return GatewayRebindReconcileAuthorization{}, err
	}

	stamp := r.now().UTC()
	metadata, err := gatewayRebindReconcileAuditMetadata(review)
	if err != nil {
		return GatewayRebindReconcileAuthorization{}, err
	}
	insert, err := tx.ExecContext(ctx, `INSERT INTO audit_events(
		actor_id,action,resource_type,resource_id,metadata_json,created_at
	) VALUES(?,?,?,?,?,?)`, input.ActorID, GatewayRebindReconcilePurpose,
		gatewayRebindAuditResourceType, input.OperationID, string(metadata), formatTime(stamp))
	if err != nil {
		return GatewayRebindReconcileAuthorization{}, err
	}
	auditID, err := insert.LastInsertId()
	if err != nil || auditID <= 0 {
		return GatewayRebindReconcileAuthorization{}, invalidRebindStoredState(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return GatewayRebindReconcileAuthorization{}, err
	}
	if err := ctx.Err(); err != nil {
		return GatewayRebindReconcileAuthorization{}, err
	}
	return GatewayRebindReconcileAuthorization{
		Review: review, ActorID: input.ActorID, AuditEventID: auditID, AuditedAt: stamp,
	}, nil
}

func buildGatewayRebindReconcileSnapshot(snapshot GatewayRebindRecoverySnapshot,
	trusted GatewayRebindTrustedRecoverySnapshot,
) (GatewayRebindReconcileSnapshot, error) {
	if snapshot.Active == nil || snapshot.Active.Claim.SpecVersion != GatewayRebindSpecVersionV2 ||
		snapshot.Active.Claim.Legacy != nil || snapshot.Active.Claim.V2 == nil ||
		snapshot.CurrentProfile == nil || snapshot.CurrentSource == nil {
		return GatewayRebindReconcileSnapshot{}, ErrConflict
	}
	active := *snapshot.Active
	claim := *active.Claim.V2
	if validateStoredGatewayRebindClaimV2(claim, active.RosterV2, active.RuntimeHeads) != nil ||
		validateGatewayRebindRetainedHistory(active) != nil || snapshot.Phase != claim.State ||
		snapshot.DatabaseCommitObserved != (snapshot.DatabaseCommittedEvent != nil) ||
		snapshot.RollbackAllowed != (!snapshot.DatabaseCommitObserved &&
			retainedGatewayRebindDisposition(active.Commands) != GatewayRebindDispositionCommit) {
		return GatewayRebindReconcileSnapshot{}, ErrInvalidStoredState
	}
	foundActive := 0
	for index := range snapshot.History {
		if historyClaimOperationID(snapshot.History[index].Claim) == claim.Spec.OperationID {
			foundActive++
			if !reflect.DeepEqual(snapshot.History[index], active) {
				return GatewayRebindReconcileSnapshot{}, ErrInvalidStoredState
			}
		}
	}
	if foundActive != 1 {
		return GatewayRebindReconcileSnapshot{}, ErrInvalidStoredState
	}
	if err := validateActiveGatewayRebindHeads(active, *snapshot.CurrentProfile,
		*snapshot.CurrentSource, snapshot.DatabaseCommitObserved); err != nil {
		return GatewayRebindReconcileSnapshot{}, err
	}
	if err := validateGatewayRebindCommittedEvent(snapshot.DatabaseCommittedEvent, active.Events,
		claim.Spec.OperationID); err != nil {
		return GatewayRebindReconcileSnapshot{}, err
	}
	if snapshot.CurrentDatabaseCommittedEvent != nil &&
		(snapshot.CurrentSource.Kind != GatewayRebindSourceGatewayRebind ||
			snapshot.CurrentDatabaseCommittedEvent.OperationID != snapshot.CurrentSource.OperationID ||
			snapshot.CurrentDatabaseCommittedEvent.State != GatewayRebindDatabaseCommitted) {
		return GatewayRebindReconcileSnapshot{}, ErrInvalidStoredState
	}

	retained := gatewayRebindRetainedCommandIdentity(active.Commands)
	policy, err := gatewayRebindRecoveryPolicy(snapshot, retained, trusted)
	if err != nil {
		return GatewayRebindReconcileSnapshot{}, err
	}
	currentProfile, currentSource := *snapshot.CurrentProfile, *snapshot.CurrentSource
	return GatewayRebindReconcileSnapshot{
		Version: GatewayRebindReconcileVersionV1, Purpose: GatewayRebindReconcilePurpose,
		Claim: claim, DatabaseCommittedEvent: cloneGatewayRebindEvent(snapshot.DatabaseCommittedEvent),
		CurrentDatabaseCommittedEvent: cloneGatewayRebindEvent(snapshot.CurrentDatabaseCommittedEvent),
		RetainedCommand:               cloneGatewayRebindCommand(retained), CurrentProfile: &currentProfile,
		CurrentSource: &currentSource, Trusted: trusted, Policy: policy,
	}, nil
}

func gatewayRebindRecoveryPolicy(snapshot GatewayRebindRecoverySnapshot,
	retained *GatewayRebindTransitionCommand, trusted GatewayRebindTrustedRecoverySnapshot,
) (GatewayRebindRecoveryPolicy, error) {
	claim := snapshot.Active.Claim.V2
	protected := trusted.Protected
	current := trusted.SelectedCurrent
	if claim == nil || !validGatewayRebindProtectedEvidence(protected) ||
		protected.OperationID != claim.Spec.OperationID ||
		protected.Generation != claim.Spec.SuccessorProtectedGeneration ||
		!validGatewayCurrentLineageRef(current.Lineage) ||
		!validDigest(current.StateDigest) ||
		gatewayRebindCurrentAuthorityProjection(current.Lineage) != *snapshot.CurrentSource {
		return "", ErrInvalidInput
	}
	switch current.Lineage.Kind {
	case GatewayRebindSourceGatewayUpgrade:
		if current.StateVersion != 2 || current.StateRevision != 0 {
			return "", ErrInvalidInput
		}
	case GatewayRebindSourceGatewayRebind:
		if current.StateVersion != 1 || current.StateRevision == 0 {
			return "", ErrInvalidInput
		}
	default:
		return "", ErrInvalidInput
	}
	if !snapshot.DatabaseCommitObserved &&
		(current.Lineage != claim.Spec.Predecessor.Lineage ||
			current.StateVersion != claim.Spec.Predecessor.SourceStateVersion ||
			current.StateRevision != claim.Spec.Predecessor.SourceStateRevision ||
			current.StateDigest != claim.Spec.Predecessor.SourceStateDigest) {
		return "", ErrConflict
	}
	if snapshot.DatabaseCommitObserved &&
		(current.Lineage.Kind != GatewayRebindSourceGatewayRebind ||
			current.Lineage.OperationID != claim.Spec.OperationID ||
			current.Lineage.ProtectedGeneration != claim.Spec.SuccessorProtectedGeneration ||
			current.Lineage.ProtectedIntentDigest != protected.IntentDigest) {
		return "", ErrConflict
	}
	if protected.Presence == GatewayRebindProtectedEvidenceAbsent &&
		(snapshot.Phase != GatewayRebindPrepared || snapshot.DatabaseCommitObserved || retained != nil) {
		return "", ErrConflict
	}
	if protected.Presence == GatewayRebindProtectedEvidenceCheckpointTerminal &&
		(snapshot.Phase != GatewayRebindPrepared || snapshot.DatabaseCommitObserved ||
			!snapshot.RollbackAllowed) {
		return "", ErrConflict
	}

	retainedDisposition, retainedReceipt := GatewayRebindDispositionNone, ""
	if retained != nil {
		retainedDisposition, retainedReceipt = retained.TerminalDisposition,
			retained.TerminalReceiptDigest
	}
	if snapshot.DatabaseCommitObserved &&
		(protected.FixedDisposition == GatewayRebindDispositionAbort ||
			retainedDisposition == GatewayRebindDispositionAbort) {
		return "", ErrConflict
	}
	if retained != nil &&
		(protected.FixedDisposition != retainedDisposition ||
			protected.TerminalReceiptDigest != retainedReceipt) {
		return "", ErrConflict
	}

	switch {
	case snapshot.DatabaseCommitObserved:
		if protected.FixedDisposition != GatewayRebindDispositionCommit {
			return "", ErrConflict
		}
		return GatewayRebindRecoveryForwardOnly, nil
	case protected.FixedDisposition == GatewayRebindDispositionCommit:
		return GatewayRebindRecoveryForwardOnly, nil
	case protected.FixedDisposition == GatewayRebindDispositionAbort:
		if !snapshot.RollbackAllowed {
			return "", ErrConflict
		}
		return GatewayRebindRecoveryRollbackOnly, nil
	case snapshot.Phase == GatewayRebindPrepared && retained == nil:
		return GatewayRebindRecoveryEvidenceDecides, nil
	default:
		return "", ErrConflict
	}
}

func validGatewayRebindProtectedEvidence(value GatewayRebindProtectedRecoveryEvidence) bool {
	if !validUUID(value.OperationID) || value.Generation == 0 || !validDigest(value.ScanDigest) {
		return false
	}
	switch value.Presence {
	case GatewayRebindProtectedEvidenceAbsent:
		return value.IntentDigest == "" && value.RecordSequence == 0 && value.RecordDigest == "" &&
			value.FixedDisposition == GatewayRebindDispositionNone && value.TerminalReceiptDigest == ""
	case GatewayRebindProtectedEvidencePresent:
		if !validDigest(value.IntentDigest) || value.RecordSequence == 0 || !validDigest(value.RecordDigest) {
			return false
		}
		switch value.FixedDisposition {
		case GatewayRebindDispositionNone:
			return value.TerminalReceiptDigest == ""
		case GatewayRebindDispositionCommit, GatewayRebindDispositionAbort:
			return value.TerminalReceiptDigest == "" || validDigest(value.TerminalReceiptDigest)
		default:
			return false
		}
	case GatewayRebindProtectedEvidenceCheckpointTerminal:
		return value.IntentDigest == "" && value.RecordSequence == 0 && value.RecordDigest == "" &&
			value.FixedDisposition == GatewayRebindDispositionAbort &&
			validDigest(value.TerminalReceiptDigest)
	default:
		return false
	}
}

func validateGatewayRebindCommittedEvent(selected *GatewayRebindEvent,
	events []GatewayRebindEvent, operationID string,
) error {
	var retained *GatewayRebindEvent
	for index := range events {
		if events[index].State == GatewayRebindDatabaseCommitted {
			if retained != nil {
				return ErrInvalidStoredState
			}
			value := events[index]
			retained = &value
		}
	}
	if (selected == nil) != (retained == nil) {
		return ErrInvalidStoredState
	}
	if selected != nil && (*selected != *retained || selected.OperationID != operationID) {
		return ErrInvalidStoredState
	}
	return nil
}

func gatewayRebindRetainedCommandIdentity(commands []GatewayRebindTransitionCommand,
) *GatewayRebindTransitionCommand {
	for index := range commands {
		if commands[index].TerminalDisposition != GatewayRebindDispositionNone {
			value := commands[index]
			return &value
		}
	}
	return nil
}

func gatewayRebindCurrentAuthorityProjection(value GatewayCurrentLineageRef) GatewayCurrentAuthorityRef {
	return GatewayCurrentAuthorityRef{
		Kind: value.Kind, OperationID: value.OperationID,
		ProfileRevisionID: value.ProfileRevisionID, ProfileRevisionNumber: value.ProfileRevisionNumber,
		ProfileSpecDigest: value.ProfileSpecDigest, TerminalReceiptDigest: value.TerminalReceiptDigest,
	}
}

func gatewayRebindRecoveryApprovalDigest(snapshot GatewayRebindReconcileSnapshot,
	recoveryDigest, actorID string,
) (string, error) {
	if snapshot.Version != GatewayRebindReconcileVersionV1 ||
		snapshot.Purpose != GatewayRebindReconcilePurpose || !validDigest(recoveryDigest) ||
		!validUUID(actorID) {
		return "", ErrInvalidInput
	}
	return digestJSON(struct {
		Version            int                         `json:"version"`
		Purpose            string                      `json:"purpose"`
		OperationID        string                      `json:"operationId"`
		ClaimRequestDigest string                      `json:"claimRequestDigest"`
		RecoveryDigest     string                      `json:"recoveryDigest"`
		Policy             GatewayRebindRecoveryPolicy `json:"policy"`
		ActorID            string                      `json:"actorId"`
	}{
		Version: GatewayRebindReconcileVersionV1, Purpose: GatewayRebindReconcilePurpose,
		OperationID:        snapshot.Claim.Spec.OperationID,
		ClaimRequestDigest: snapshot.Claim.RequestDigest, RecoveryDigest: recoveryDigest,
		Policy: snapshot.Policy, ActorID: actorID,
	})
}

func gatewayRebindReconcileAuditMetadata(review GatewayRebindReconcileReview) ([]byte, error) {
	snapshot := review.Snapshot
	return json.Marshal(struct {
		Version                       int                                    `json:"version"`
		RecoveryDigest                string                                 `json:"recoveryDigest"`
		RecoveryApprovalDigest        string                                 `json:"recoveryApprovalDigest"`
		RecoveryPolicy                GatewayRebindRecoveryPolicy            `json:"recoveryPolicy"`
		ClaimRequestDigest            string                                 `json:"claimRequestDigest"`
		ClaimSpecDigest               string                                 `json:"claimSpecDigest"`
		State                         GatewayRebindState                     `json:"state"`
		StateSequence                 int64                                  `json:"stateSequence"`
		DatabaseCommittedEvent        *GatewayRebindEvent                    `json:"databaseCommittedEvent,omitempty"`
		RetainedCommandSequence       int64                                  `json:"retainedCommandSequence,omitempty"`
		RetainedCommandDigest         string                                 `json:"retainedCommandDigest,omitempty"`
		RetainedDisposition           GatewayRebindTerminalDisposition       `json:"retainedDisposition"`
		RetainedTerminalReceiptDigest string                                 `json:"retainedTerminalReceiptDigest,omitempty"`
		Protected                     GatewayRebindProtectedRecoveryEvidence `json:"protected"`
		SelectedCurrentLineage        GatewayCurrentLineageRef               `json:"selectedCurrentLineage"`
		SelectedCurrentStateVersion   uint64                                 `json:"selectedCurrentStateVersion"`
		SelectedCurrentStateRevision  uint64                                 `json:"selectedCurrentStateRevision"`
		SelectedCurrentStateDigest    string                                 `json:"selectedCurrentStateDigest"`
	}{
		Version: GatewayRebindReconcileVersionV1, RecoveryDigest: review.RecoveryDigest,
		RecoveryApprovalDigest: review.RecoveryApprovalDigest, RecoveryPolicy: snapshot.Policy,
		ClaimRequestDigest: snapshot.Claim.RequestDigest,
		ClaimSpecDigest:    snapshot.Claim.RebindApproval.SpecDigest,
		State:              snapshot.Claim.State, StateSequence: snapshot.Claim.StateSequence,
		DatabaseCommittedEvent:        cloneGatewayRebindEvent(snapshot.DatabaseCommittedEvent),
		RetainedCommandSequence:       gatewayRebindCommandSequence(snapshot.RetainedCommand),
		RetainedCommandDigest:         gatewayRebindCommandDigest(snapshot.RetainedCommand),
		RetainedDisposition:           gatewayRebindCommandDisposition(snapshot.RetainedCommand),
		RetainedTerminalReceiptDigest: gatewayRebindCommandReceipt(snapshot.RetainedCommand),
		Protected:                     snapshot.Trusted.Protected,
		SelectedCurrentLineage:        snapshot.Trusted.SelectedCurrent.Lineage,
		SelectedCurrentStateVersion:   snapshot.Trusted.SelectedCurrent.StateVersion,
		SelectedCurrentStateRevision:  snapshot.Trusted.SelectedCurrent.StateRevision,
		SelectedCurrentStateDigest:    snapshot.Trusted.SelectedCurrent.StateDigest,
	})
}

func cloneGatewayRebindEvent(value *GatewayRebindEvent) *GatewayRebindEvent {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneGatewayRebindCommand(value *GatewayRebindTransitionCommand) *GatewayRebindTransitionCommand {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func gatewayRebindCommandSequence(value *GatewayRebindTransitionCommand) int64 {
	if value == nil {
		return 0
	}
	return value.Sequence
}

func gatewayRebindCommandDigest(value *GatewayRebindTransitionCommand) string {
	if value == nil {
		return ""
	}
	return value.CommandDigest
}

func gatewayRebindCommandDisposition(value *GatewayRebindTransitionCommand) GatewayRebindTerminalDisposition {
	if value == nil {
		return GatewayRebindDispositionNone
	}
	return value.TerminalDisposition
}

func gatewayRebindCommandReceipt(value *GatewayRebindTransitionCommand) string {
	if value == nil {
		return ""
	}
	return value.TerminalReceiptDigest
}
