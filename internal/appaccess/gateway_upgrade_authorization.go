package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// GatewayProfileUpgradeAuthorizationInput is the exact durable approval
// binding that a controller must retain between claiming an upgrade and
// entering the generated-ingress maintenance lock.
type GatewayProfileUpgradeAuthorizationInput struct {
	OperationID           string
	RequestDigest         string
	ActionDigest          string
	ActorID               string
	ProfileRevisionID     string
	ProfileRevisionNumber int64
	ProfileSpecDigest     string
	PermittedStates       []GatewayProfileUpgradeState
}

// GatewayProfileUpgradeAuthorization contains the claim and the canonical
// profile values observed in the same SQLite read snapshot.
type GatewayProfileUpgradeAuthorization struct {
	Claim   GatewayProfileUpgradeClaim
	Profile GatewayProfileRevision
}

// AuthorizeGatewayProfileUpgrade rechecks a previously claimed administrator
// approval after the caller has acquired its external maintenance lock. It is
// read-only: it performs no database write, host inspection, or Docker action.
//
// The exact operation is read directly so a permitted terminal claim can be
// replayed while its profile is still current. The current profile head, actor
// role, claim history, canonical action and request digests, and profile values
// are all checked in one SQLite snapshot.
func (r *Repository) AuthorizeGatewayProfileUpgrade(ctx context.Context, input GatewayProfileUpgradeAuthorizationInput) (GatewayProfileUpgradeAuthorization, error) {
	spec, permitted, err := validateGatewayProfileUpgradeAuthorizationInput(input)
	if r == nil || r.db == nil || err != nil {
		if err != nil {
			return GatewayProfileUpgradeAuthorization{}, err
		}
		return GatewayProfileUpgradeAuthorization{}, ErrInvalidInput
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GatewayProfileUpgradeAuthorization{}, err
	}
	defer tx.Rollback()

	claim, err := readGatewayProfileUpgradeClaim(ctx, tx, input.OperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return GatewayProfileUpgradeAuthorization{}, ErrNotFound
	}
	if err != nil {
		return GatewayProfileUpgradeAuthorization{}, err
	}
	if r.afterUpgradeAuthClaimRead != nil {
		r.afterUpgradeAuthClaimRead()
	}
	if claim.RequestDigest != input.RequestDigest || claim.ApprovedBy != input.ActorID ||
		claim.ProfileRevisionID != spec.ProfileRevisionID || claim.ProfileRevisionNumber != spec.ProfileRevisionNumber ||
		claim.ProfileSpecDigest != spec.ProfileSpecDigest {
		return GatewayProfileUpgradeAuthorization{}, ErrIdempotencyMismatch
	}
	if _, ok := permitted[claim.State]; !ok {
		return GatewayProfileUpgradeAuthorization{}, ErrConflict
	}
	if err := validateGatewayProfileUpgradeClaimHistory(ctx, tx, claim); err != nil {
		return GatewayProfileUpgradeAuthorization{}, err
	}

	administrator, err := administratorExists(ctx, tx, input.ActorID)
	if err != nil {
		return GatewayProfileUpgradeAuthorization{}, err
	}
	if !administrator {
		return GatewayProfileUpgradeAuthorization{}, ErrApprovalRequired
	}

	profile, _, err := readGatewayRevision(ctx, tx, spec.ProfileRevisionID, spec.ProfileRevisionNumber)
	if errors.Is(err, sql.ErrNoRows) {
		return GatewayProfileUpgradeAuthorization{}, ErrInvalidStoredState
	}
	if err != nil {
		return GatewayProfileUpgradeAuthorization{}, err
	}
	if profile.ID != spec.ProfileRevisionID || profile.RevisionNumber != spec.ProfileRevisionNumber ||
		profile.SpecDigest != spec.ProfileSpecDigest {
		return GatewayProfileUpgradeAuthorization{}, ErrInvalidStoredState
	}

	var headID sql.NullString
	var headNumber int64
	if err := tx.QueryRowContext(ctx, `SELECT revision_id,revision_number FROM lan_gateway_profile_heads WHERE singleton=1`).Scan(&headID, &headNumber); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return GatewayProfileUpgradeAuthorization{}, ErrInvalidStoredState
		}
		return GatewayProfileUpgradeAuthorization{}, err
	}
	if !headID.Valid || !validUUID(headID.String) || headNumber <= 0 {
		return GatewayProfileUpgradeAuthorization{}, ErrInvalidStoredState
	}
	if headID.String != profile.ID || headNumber != profile.RevisionNumber {
		return GatewayProfileUpgradeAuthorization{}, ErrConflict
	}

	if err := tx.Commit(); err != nil {
		return GatewayProfileUpgradeAuthorization{}, err
	}
	return GatewayProfileUpgradeAuthorization{Claim: claim, Profile: profile}, nil
}

func validateGatewayProfileUpgradeAuthorizationInput(input GatewayProfileUpgradeAuthorizationInput) (GatewayProfileUpgradeSpec, map[GatewayProfileUpgradeState]struct{}, error) {
	spec := GatewayProfileUpgradeSpec{
		ProfileRevisionID:     input.ProfileRevisionID,
		ProfileRevisionNumber: input.ProfileRevisionNumber,
		ProfileSpecDigest:     input.ProfileSpecDigest,
	}
	actionDigest, err := GatewayProfileUpgradeSpecDigest(spec)
	if err != nil || !validUUID(input.OperationID) || !validUUID(input.ActorID) || !validDigest(input.RequestDigest) || len(input.PermittedStates) == 0 {
		return GatewayProfileUpgradeSpec{}, nil, ErrInvalidInput
	}
	if input.ActionDigest != actionDigest {
		return GatewayProfileUpgradeSpec{}, nil, ErrApprovalRequired
	}
	requestDigest, err := gatewayUpgradeClaimRequestDigest(ClaimGatewayProfileUpgradeInput{
		OperationID: input.OperationID,
		Spec:        spec,
		Approval: Approval{
			Action:     ActionUpgradeGateway,
			SpecDigest: input.ActionDigest,
			ActorID:    input.ActorID,
		},
	})
	if err != nil {
		return GatewayProfileUpgradeSpec{}, nil, err
	}
	if requestDigest != input.RequestDigest {
		return GatewayProfileUpgradeSpec{}, nil, ErrIdempotencyMismatch
	}
	permitted := make(map[GatewayProfileUpgradeState]struct{}, len(input.PermittedStates))
	for _, state := range input.PermittedStates {
		if !validGatewayProfileUpgradeState(state) {
			return GatewayProfileUpgradeSpec{}, nil, ErrInvalidInput
		}
		if _, duplicate := permitted[state]; duplicate {
			return GatewayProfileUpgradeSpec{}, nil, ErrInvalidInput
		}
		permitted[state] = struct{}{}
	}
	return spec, permitted, nil
}

func validateGatewayProfileUpgradeClaimHistory(ctx context.Context, tx gatewayRebindQuiescenceQuerier, claim GatewayProfileUpgradeClaim) error {
	rows, err := tx.QueryContext(ctx, `SELECT sequence,state,created_at
		FROM lan_gateway_upgrade_claim_events WHERE operation_id=? ORDER BY sequence`, claim.OperationID)
	if err != nil {
		return err
	}
	defer rows.Close()

	var (
		count       int64
		previous    GatewayProfileUpgradeState
		previousAt  time.Time
		lastState   GatewayProfileUpgradeState
		lastUpdated time.Time
	)
	for rows.Next() {
		var (
			sequence  int64
			state     GatewayProfileUpgradeState
			createdAt string
		)
		if err := rows.Scan(&sequence, &state, &createdAt); err != nil {
			return err
		}
		count++
		stamp, parseErr := parseTime(createdAt)
		if parseErr != nil || sequence != count || !validGatewayProfileUpgradeState(state) {
			return ErrInvalidStoredState
		}
		if sequence == 1 {
			if state != GatewayProfileUpgradePrepared || !stamp.Equal(claim.ApprovedAt) {
				return ErrInvalidStoredState
			}
		} else if !validGatewayProfileUpgradeTransition(previous, state) || stamp.Before(previousAt) {
			return ErrInvalidStoredState
		}
		previous, previousAt = state, stamp
		lastState, lastUpdated = state, stamp
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != claim.StateSequence || lastState != claim.State || !lastUpdated.Equal(claim.UpdatedAt) {
		return ErrInvalidStoredState
	}
	return nil
}
