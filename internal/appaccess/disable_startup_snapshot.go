package appaccess

import (
	"context"
	"database/sql"
	"errors"
)

type AppAccessDisableStartupClaim struct {
	Claim                          AppAccessDisableClaim
	Revision                       AppAccessRevision
	Allocation                     Allocation
	Profile                        GatewayProfileRevision
	EffectiveProfile               GatewayProfileRevision
	CurrentGatewaySource           GatewayCurrentAuthorityRef
	TransferChain                  []GatewayRebindAllocationTransfer
	TransferChainTipDigest         string
	TerminalReceiptDigest          string
	RetainedEffectiveProfile       GatewayProfileRevision
	RetainedGatewaySource          GatewayCurrentAuthorityRef
	RetainedTransferChain          []GatewayRebindAllocationTransfer
	RetainedTransferChainTipDigest string
	RetainedTerminalReceiptDigest  string
	SourceGrant                    *AppAccessGrantClaim
	AppArchived                    bool
	AccessHeadCurrent              bool
	ProfileHeadCurrent             bool
	ApproverIsAdministrator        bool
	ProtectedClearAck              *AppAccessDisableProtectedClearAck
	SuccessorAck                   *AppAccessDisableSuccessorAck
}

type AppAccessDisableStartupSnapshot struct {
	Claims []AppAccessDisableStartupClaim
}

func (r *Repository) AppAccessDisableStartupSnapshot(ctx context.Context) (AppAccessDisableStartupSnapshot, error) {
	if r == nil || r.db == nil {
		return AppAccessDisableStartupSnapshot{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AppAccessDisableStartupSnapshot{}, err
	}
	defer tx.Rollback()
	snapshot, err := r.readAppAccessDisableStartupSnapshot(ctx, tx)
	if err != nil {
		return AppAccessDisableStartupSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppAccessDisableStartupSnapshot{}, err
	}
	return snapshot, nil
}

func (r *Repository) readAppAccessDisableStartupSnapshot(ctx context.Context, tx *sql.Tx) (AppAccessDisableStartupSnapshot, error) {
	rows, err := tx.QueryContext(ctx, `SELECT operation_id
		FROM lan_app_access_disable_claims ORDER BY created_at,operation_id`)
	if err != nil {
		return AppAccessDisableStartupSnapshot{}, err
	}
	var operationIDs []string
	for rows.Next() {
		var operationID string
		if err := rows.Scan(&operationID); err != nil {
			_ = rows.Close()
			return AppAccessDisableStartupSnapshot{}, err
		}
		operationIDs = append(operationIDs, operationID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return AppAccessDisableStartupSnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return AppAccessDisableStartupSnapshot{}, err
	}
	if r.afterDisableStartupClaimsRead != nil {
		r.afterDisableStartupClaimsRead()
	}
	result := AppAccessDisableStartupSnapshot{Claims: make([]AppAccessDisableStartupClaim, 0, len(operationIDs))}
	for _, operationID := range operationIDs {
		value, err := readAppAccessDisableStartupClaim(ctx, tx, operationID)
		if err != nil {
			return AppAccessDisableStartupSnapshot{}, err
		}
		result.Claims = append(result.Claims, value)
	}
	return result, nil
}

func readAppAccessDisableStartupClaim(ctx context.Context, tx *sql.Tx, operationID string) (AppAccessDisableStartupClaim, error) {
	claim, err := readAppAccessDisableClaim(ctx, tx, operationID)
	if err != nil {
		return AppAccessDisableStartupClaim{}, err
	}
	revision, _, err := readAccessRevision(ctx, tx, claim.Spec.AppID,
		claim.Spec.AccessRevisionID, claim.Spec.AccessRevisionNumber)
	if err != nil {
		return AppAccessDisableStartupClaim{}, err
	}
	profile, _, err := readGatewayRevision(ctx, tx, claim.Spec.GatewayProfileRevisionID,
		claim.Spec.GatewayProfileRevisionNumber)
	if err != nil {
		return AppAccessDisableStartupClaim{}, err
	}
	if revision.OperationID != claim.Spec.OwnerOperationID ||
		revision.Allocation.ID != claim.Spec.AllocationID || revision.Allocation.Port != claim.Spec.Port ||
		revision.Allocation.GatewayProfileRevisionID != claim.Spec.GatewayProfileRevisionID ||
		revision.Allocation.GatewayProfileRevisionNumber != claim.Spec.GatewayProfileRevisionNumber {
		return AppAccessDisableStartupClaim{}, ErrInvalidStoredState
	}
	value := AppAccessDisableStartupClaim{Claim: claim, Revision: revision,
		Allocation: revision.Allocation, Profile: profile, EffectiveProfile: profile}
	if claim.SourceGrantAttemptID != "" {
		grant, err := readAppAccessGrantClaim(ctx, tx, claim.SourceGrantAttemptID)
		if err != nil {
			return AppAccessDisableStartupClaim{}, err
		}
		if grant.State != AppAccessGrantCommitted || grant.Spec.AppID != claim.Spec.AppID ||
			grant.Spec.AllocationID != claim.Spec.AllocationID ||
			grant.Spec.OwnerOperationID != claim.Spec.OwnerOperationID ||
			grant.Spec.AccessRevisionID != claim.Spec.AccessRevisionID {
			return AppAccessDisableStartupClaim{}, ErrInvalidStoredState
		}
		value.SourceGrant = &grant
		resolution, resolveErr := resolveGatewayBindingForStoredGrant(ctx, tx, GatewayBindingRef{
			AppID: claim.Spec.AppID, AllocationID: claim.Spec.AllocationID,
			AccessRevisionID: claim.Spec.AccessRevisionID, GrantAttemptID: grant.AttemptID,
		}, profile, grant)
		if resolveErr != nil || resolution.RawProfile.ID != profile.ID ||
			resolution.RawProfile.RevisionNumber != profile.RevisionNumber ||
			resolution.RawProfile.SpecDigest != profile.SpecDigest {
			return AppAccessDisableStartupClaim{}, invalidRebindStoredState(resolveErr)
		}
		if grant.RetiredAt != nil && resolution.CurrentGatewaySource.Kind != "" {
			value.EffectiveProfile = GatewayProfileRevision{}
			value.RetainedEffectiveProfile = resolution.EffectiveProfile
			value.RetainedGatewaySource = resolution.CurrentGatewaySource
			value.RetainedTransferChain = append([]GatewayRebindAllocationTransfer(nil), resolution.TransferChain...)
			value.RetainedTransferChainTipDigest = resolution.TransferChainTipDigest
			value.RetainedTerminalReceiptDigest = resolution.TerminalReceiptDigest
		} else if grant.RetiredAt == nil {
			value.EffectiveProfile = resolution.EffectiveProfile
			value.CurrentGatewaySource = resolution.CurrentGatewaySource
			value.TransferChain = append([]GatewayRebindAllocationTransfer(nil), resolution.TransferChain...)
			value.TransferChainTipDigest = resolution.TransferChainTipDigest
			value.TerminalReceiptDigest = resolution.TerminalReceiptDigest
		}
	}
	if claim.State == AppAccessDisableCommitted {
		if claim.Proof == nil || revision.Allocation.ReleasedAt == nil ||
			!revision.Allocation.ReleasedAt.Equal(claim.Proof.ObservedAt) {
			return AppAccessDisableStartupClaim{}, ErrInvalidStoredState
		}
		if value.SourceGrant != nil && (value.SourceGrant.RetiredAt == nil ||
			!value.SourceGrant.RetiredAt.Equal(claim.Proof.ObservedAt) ||
			value.SourceGrant.RetiredByDisableOperationID != claim.OperationID) {
			return AppAccessDisableStartupClaim{}, ErrInvalidStoredState
		}
	} else if claim.Proof != nil || revision.Allocation.ReleasedAt != nil ||
		(value.SourceGrant != nil && value.SourceGrant.RetiredAt != nil) {
		return AppAccessDisableStartupClaim{}, ErrInvalidStoredState
	}
	ack, err := readAppAccessDisableProtectedClearAck(ctx, tx, operationID)
	if err == nil {
		if claim.State != AppAccessDisableCommitted || claim.Proof == nil ||
			ack.GatewayOperationID != claim.Proof.GatewayOperationID ||
			ack.ObservedAt.Before(claim.Proof.ObservedAt) ||
			ack.AcknowledgedAt.Before(ack.ObservedAt) {
			return AppAccessDisableStartupClaim{}, ErrInvalidStoredState
		}
		value.ProtectedClearAck = &ack
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableStartupClaim{}, err
	}
	successorAck, err := readAppAccessDisableSuccessorAck(ctx, tx, operationID)
	if err == nil {
		if value.ProtectedClearAck != nil || claim.State != AppAccessDisableCommitted ||
			claim.Proof == nil || successorAck.GatewayOperationID != claim.Proof.GatewayOperationID ||
			successorAck.ObservedAt.Before(claim.Proof.ObservedAt) ||
			successorAck.SuccessorAllocationID == claim.Spec.AllocationID {
			return AppAccessDisableStartupClaim{}, ErrInvalidStoredState
		}
		successor, lookupErr := readAppAccessGrantClaim(ctx, tx, successorAck.SuccessorAttemptID)
		if lookupErr != nil {
			return AppAccessDisableStartupClaim{}, lookupErr
		}
		if successor.State != AppAccessGrantCommitted || successor.Proof == nil ||
			successor.Spec.AllocationID != successorAck.SuccessorAllocationID ||
			!successor.CreatedAt.After(claim.Proof.ObservedAt) ||
			successorAck.ObservedAt.Before(successor.Proof.ObservedAt) ||
			!disableSuccessorRelationMatches(claim, successor, successorAck.Relation) {
			return AppAccessDisableStartupClaim{}, ErrInvalidStoredState
		}
		successorRevision, _, lookupErr := readAccessRevision(ctx, tx, successor.Spec.AppID,
			successor.Spec.AccessRevisionID, successor.Spec.AccessRevisionNumber)
		if lookupErr != nil {
			return AppAccessDisableStartupClaim{}, lookupErr
		}
		if successorRevision.OperationID != successor.Spec.OwnerOperationID ||
			successorRevision.SpecDigest != successor.Spec.AccessSpecDigest ||
			successorRevision.ApprovedBy != successor.Spec.ApprovedBy ||
			!successorRevision.ApprovedAt.Equal(successor.ApprovedAt) ||
			successorRevision.Allocation.ID != successor.Spec.AllocationID ||
			successorRevision.Allocation.Port != successor.Spec.Port ||
			successorRevision.Allocation.GatewayProfileRevisionID != successor.Spec.GatewayProfileRevisionID ||
			successorRevision.Allocation.GatewayProfileRevisionNumber != successor.Spec.GatewayProfileRevisionNumber {
			return AppAccessDisableStartupClaim{}, ErrInvalidStoredState
		}
		successorProfile, _, lookupErr := readGatewayRevision(ctx, tx,
			successor.Spec.GatewayProfileRevisionID, successor.Spec.GatewayProfileRevisionNumber)
		if lookupErr != nil {
			return AppAccessDisableStartupClaim{}, lookupErr
		}
		if successorProfile.SpecDigest != successor.Spec.GatewayProfileSpecDigest {
			return AppAccessDisableStartupClaim{}, ErrInvalidStoredState
		}
		value.SuccessorAck = &successorAck
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableStartupClaim{}, err
	}
	var archived sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT archived_at FROM applications WHERE id=?`, claim.Spec.AppID).Scan(&archived); errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableStartupClaim{}, ErrInvalidStoredState
	} else if err != nil {
		return AppAccessDisableStartupClaim{}, err
	}
	value.AppArchived = archived.Valid
	var current int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lan_app_access_heads
		WHERE app_id=? AND revision_id=? AND revision_number=?
	)`, claim.Spec.AppID, claim.Spec.AccessRevisionID, claim.Spec.AccessRevisionNumber).Scan(&current); err != nil {
		return AppAccessDisableStartupClaim{}, err
	}
	value.AccessHeadCurrent = current == 1
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lan_gateway_profile_heads
		WHERE singleton=1 AND revision_id=? AND revision_number=?
	)`, value.EffectiveProfile.ID, value.EffectiveProfile.RevisionNumber).Scan(&current); err != nil {
		return AppAccessDisableStartupClaim{}, err
	}
	value.ProfileHeadCurrent = current == 1
	administrator, err := administratorExists(ctx, tx, claim.ApprovedBy)
	if err != nil {
		return AppAccessDisableStartupClaim{}, err
	}
	value.ApproverIsAdministrator = administrator
	return value, nil
}
