package appaccess

import (
	"context"
	"database/sql"
	"errors"
)

type AppAccessDisableAuthorizationInput struct {
	Owner           AppAccessDisableClaimOwner
	PermittedStates []AppAccessDisableState
}

type AppAccessDisableAuthorization struct {
	Claim                  AppAccessDisableClaim
	Revision               AppAccessRevision
	Allocation             Allocation
	Profile                GatewayProfileRevision
	EffectiveProfile       GatewayProfileRevision
	CurrentGatewaySource   GatewayCurrentLineageRef
	TransferChainTipDigest string
	TerminalReceiptDigest  string
	SourceGrant            *AppAccessGrantClaim
}

// AuthorizeAppAccessDisable reconstructs the immutable request binding in one
// read snapshot. Committed claims remain reconstructable after their exact
// source grant and allocation have been retired by the terminal transition.
func (r *Repository) AuthorizeAppAccessDisable(ctx context.Context,
	input AppAccessDisableAuthorizationInput,
) (AppAccessDisableAuthorization, error) {
	permitted, err := validateAppAccessDisableAuthorizationInput(input)
	if r == nil || r.db == nil || err != nil {
		if err != nil {
			return AppAccessDisableAuthorization{}, err
		}
		return AppAccessDisableAuthorization{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AppAccessDisableAuthorization{}, err
	}
	defer tx.Rollback()
	administrator, err := administratorExists(ctx, tx, input.Owner.ActorID)
	if err != nil {
		return AppAccessDisableAuthorization{}, err
	}
	if !administrator {
		return AppAccessDisableAuthorization{}, ErrApprovalRequired
	}
	claim, err := readAppAccessDisableClaim(ctx, tx, input.Owner.OperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableAuthorization{}, ErrNotFound
	}
	if err != nil {
		return AppAccessDisableAuthorization{}, err
	}
	if !appAccessDisableOwnerMatches(claim, input.Owner) {
		return AppAccessDisableAuthorization{}, ErrIdempotencyMismatch
	}
	if _, ok := permitted[claim.State]; !ok {
		return AppAccessDisableAuthorization{}, ErrConflict
	}
	value, err := readAppAccessDisableAuthorization(ctx, tx, claim)
	if err != nil {
		return AppAccessDisableAuthorization{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppAccessDisableAuthorization{}, err
	}
	return value, nil
}

func readAppAccessDisableAuthorization(ctx context.Context, query appAccessDisableQuerier,
	claim AppAccessDisableClaim,
) (AppAccessDisableAuthorization, error) {
	if claim.State != AppAccessDisableCommitted {
		var headID sql.NullString
		var headNumber int64
		var archived sql.NullString
		if err := query.QueryRowContext(ctx, `SELECT h.revision_id,h.revision_number,a.archived_at
			FROM lan_app_access_heads h JOIN applications a ON a.id=h.app_id
			WHERE h.app_id=?`, claim.Spec.AppID).Scan(&headID, &headNumber, &archived); errors.Is(err, sql.ErrNoRows) {
			return AppAccessDisableAuthorization{}, ErrNotFound
		} else if err != nil {
			return AppAccessDisableAuthorization{}, err
		}
		if archived.Valid || !headID.Valid || headID.String != claim.Spec.AccessRevisionID ||
			headNumber != claim.Spec.AccessRevisionNumber {
			return AppAccessDisableAuthorization{}, ErrConflict
		}
	}
	revision, _, err := readAccessRevision(ctx, query, claim.Spec.AppID,
		claim.Spec.AccessRevisionID, claim.Spec.AccessRevisionNumber)
	if err != nil {
		return AppAccessDisableAuthorization{}, err
	}
	if revision.OperationID != claim.Spec.OwnerOperationID ||
		revision.Allocation.ID != claim.Spec.AllocationID || revision.Allocation.Port != claim.Spec.Port ||
		revision.Allocation.GatewayProfileRevisionID != claim.Spec.GatewayProfileRevisionID ||
		revision.Allocation.GatewayProfileRevisionNumber != claim.Spec.GatewayProfileRevisionNumber {
		return AppAccessDisableAuthorization{}, ErrConflict
	}
	if claim.State != AppAccessDisableCommitted {
		var profileID sql.NullString
		var profileNumber int64
		if err := query.QueryRowContext(ctx, `SELECT revision_id,revision_number
			FROM lan_gateway_profile_heads WHERE singleton=1`).Scan(&profileID, &profileNumber); err != nil {
			return AppAccessDisableAuthorization{}, err
		}
		if !profileID.Valid || profileID.String != claim.Spec.GatewayProfileRevisionID ||
			profileNumber != claim.Spec.GatewayProfileRevisionNumber {
			return AppAccessDisableAuthorization{}, ErrConflict
		}
	}
	profile, _, err := readGatewayRevision(ctx, query, claim.Spec.GatewayProfileRevisionID,
		claim.Spec.GatewayProfileRevisionNumber)
	if err != nil {
		return AppAccessDisableAuthorization{}, err
	}
	value := AppAccessDisableAuthorization{Claim: claim, Revision: revision,
		Allocation: revision.Allocation, Profile: profile}
	if claim.SourceGrantAttemptID != "" {
		grant, err := readAppAccessGrantClaim(ctx, query, claim.SourceGrantAttemptID)
		if err != nil {
			return AppAccessDisableAuthorization{}, err
		}
		if grant.State != AppAccessGrantCommitted || grant.Spec.AppID != claim.Spec.AppID ||
			grant.Spec.AllocationID != claim.Spec.AllocationID ||
			grant.Spec.OwnerOperationID != claim.Spec.OwnerOperationID ||
			grant.Spec.AccessRevisionID != claim.Spec.AccessRevisionID {
			return AppAccessDisableAuthorization{}, ErrInvalidStoredState
		}
		value.SourceGrant = &grant
	}
	if claim.State == AppAccessDisableCommitted {
		if claim.Proof == nil || revision.Allocation.ReleasedAt == nil ||
			!revision.Allocation.ReleasedAt.Equal(claim.Proof.ObservedAt) {
			return AppAccessDisableAuthorization{}, ErrInvalidStoredState
		}
		if value.SourceGrant != nil && (value.SourceGrant.RetiredAt == nil ||
			!value.SourceGrant.RetiredAt.Equal(claim.Proof.ObservedAt) ||
			value.SourceGrant.RetiredByDisableOperationID != claim.OperationID) {
			return AppAccessDisableAuthorization{}, ErrInvalidStoredState
		}
	} else {
		if claim.Proof != nil || revision.Allocation.ReleasedAt != nil {
			return AppAccessDisableAuthorization{}, ErrInvalidStoredState
		}
		if value.SourceGrant != nil && value.SourceGrant.RetiredAt != nil {
			return AppAccessDisableAuthorization{}, ErrInvalidStoredState
		}
	}
	return value, nil
}

func validateAppAccessDisableAuthorizationInput(input AppAccessDisableAuthorizationInput) (map[AppAccessDisableState]struct{}, error) {
	if !validAppAccessDisableClaimOwner(input.Owner) || len(input.PermittedStates) == 0 {
		return nil, ErrInvalidInput
	}
	permitted := make(map[AppAccessDisableState]struct{}, len(input.PermittedStates))
	for _, state := range input.PermittedStates {
		if !validAppAccessDisableState(state) {
			return nil, ErrInvalidInput
		}
		if _, duplicate := permitted[state]; duplicate {
			return nil, ErrInvalidInput
		}
		permitted[state] = struct{}{}
	}
	return permitted, nil
}
