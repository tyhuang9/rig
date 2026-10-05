package appaccess

import (
	"context"
	"database/sql"
	"errors"
)

// AppAccessGrantAuthorizationInput is the exact logical lease presented by a
// trusted controller after it acquires the gateway lock. Applying and later
// states provide the durable database fence; this read-only check never holds
// a SQLite write transaction across gateway or Docker work.
type AppAccessGrantAuthorizationInput struct {
	Owner           AppAccessGrantClaimOwner
	PermittedStates []AppAccessGrantState
}

type AppAccessGrantAuthorization struct {
	Claim                  AppAccessGrantClaim
	Revision               AppAccessRevision
	Allocation             Allocation
	Profile                GatewayProfileRevision
	EffectiveProfile       GatewayProfileRevision
	CurrentGatewaySource   GatewayCurrentLineageRef
	TransferChainTipDigest string
	TerminalReceiptDigest  string
}

// AuthorizeAppAccessGrant validates the claim history, exact current heads,
// allocation owner, profile, approving administrator, and absence of a disable
// intent in one SQLite read snapshot. The result is authorization data, not
// route attestation and not permission to report a LAN URL.
func (r *Repository) AuthorizeAppAccessGrant(ctx context.Context,
	input AppAccessGrantAuthorizationInput,
) (AppAccessGrantAuthorization, error) {
	permitted, err := validateAppAccessGrantAuthorizationInput(input)
	if r == nil || r.db == nil || err != nil {
		if err != nil {
			return AppAccessGrantAuthorization{}, err
		}
		return AppAccessGrantAuthorization{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AppAccessGrantAuthorization{}, err
	}
	defer tx.Rollback()
	claim, err := readAppAccessGrantClaim(ctx, tx, input.Owner.AttemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return AppAccessGrantAuthorization{}, ErrNotFound
	}
	if err != nil {
		return AppAccessGrantAuthorization{}, err
	}
	if !appAccessGrantOwnerMatches(claim, input.Owner) {
		return AppAccessGrantAuthorization{}, ErrIdempotencyMismatch
	}
	if _, ok := permitted[claim.State]; !ok {
		return AppAccessGrantAuthorization{}, ErrConflict
	}
	revision, profile, err := validateCurrentAppAccessGrantSpec(ctx, tx, claim.Spec, true)
	if err != nil {
		return AppAccessGrantAuthorization{}, err
	}
	if !appAccessGrantAllocationStateMatches(claim.State, revision.Allocation.State) {
		return AppAccessGrantAuthorization{}, ErrInvalidStoredState
	}
	if err := tx.Commit(); err != nil {
		return AppAccessGrantAuthorization{}, err
	}
	return AppAccessGrantAuthorization{
		Claim: claim, Revision: revision, Allocation: revision.Allocation,
		Profile: profile, EffectiveProfile: profile,
	}, nil
}

func validateAppAccessGrantAuthorizationInput(input AppAccessGrantAuthorizationInput) (map[AppAccessGrantState]struct{}, error) {
	if !validAppAccessGrantClaimOwner(input.Owner) || len(input.PermittedStates) == 0 {
		return nil, ErrInvalidInput
	}
	permitted := make(map[AppAccessGrantState]struct{}, len(input.PermittedStates))
	for _, state := range input.PermittedStates {
		if !validAppAccessGrantState(state) || state == AppAccessGrantPrepared || state == AppAccessGrantRolledBack {
			return nil, ErrInvalidInput
		}
		if _, duplicate := permitted[state]; duplicate {
			return nil, ErrInvalidInput
		}
		permitted[state] = struct{}{}
	}
	return permitted, nil
}
