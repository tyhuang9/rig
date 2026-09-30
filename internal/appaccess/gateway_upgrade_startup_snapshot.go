package appaccess

import (
	"context"
	"database/sql"
	"errors"
)

// GatewayUpgradeStartupSnapshot is the complete durable gateway-upgrade view
// observed during one SQLite read transaction. CurrentProfile is nil before a
// gateway profile has been configured. Claims includes retained rolled-back
// history in deterministic approval order.
type GatewayUpgradeStartupSnapshot struct {
	CurrentProfile *GatewayProfileRevision
	Claims         []GatewayUpgradeStartupClaim
}

// GatewayUpgradeStartupClaim retains the exact claim and immutable profile
// values needed to reconstruct a generated-ingress startup claim. The action
// digest is recomputed from the validated profile binding rather than trusted
// from a caller or a second database read.
type GatewayUpgradeStartupClaim struct {
	Claim                GatewayProfileUpgradeClaim
	Profile              GatewayProfileRevision
	ApprovedActionDigest string
}

// GatewayUpgradeStartupSnapshot returns the current profile and every retained
// gateway-upgrade claim from one read-only SQLite snapshot. It fails closed on
// malformed profile or claim data, broken event history, competing live
// claims, or a live claim whose current profile or administrator binding has
// drifted. A rolled-back historical claim remains valid after its profile is
// superseded or its original approving administrator is demoted.
func (r *Repository) GatewayUpgradeStartupSnapshot(ctx context.Context) (GatewayUpgradeStartupSnapshot, error) {
	if r == nil || r.db == nil {
		return GatewayUpgradeStartupSnapshot{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GatewayUpgradeStartupSnapshot{}, err
	}
	defer tx.Rollback()
	result, err := r.readGatewayUpgradeStartupSnapshot(ctx, tx)
	if err != nil {
		return GatewayUpgradeStartupSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return GatewayUpgradeStartupSnapshot{}, err
	}
	return result, nil
}

func readGatewayUpgradeStartupHead(ctx context.Context, tx *sql.Tx) (*GatewayProfileRevision, error) {
	var revisionID, updatedAt sql.NullString
	var revisionNumber int64
	if err := tx.QueryRowContext(ctx, `SELECT revision_id,revision_number,updated_at
		FROM lan_gateway_profile_heads WHERE singleton=1`).Scan(&revisionID, &revisionNumber, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidStoredState
		}
		return nil, err
	}
	if revisionNumber == 0 {
		if revisionID.Valid || updatedAt.Valid {
			return nil, ErrInvalidStoredState
		}
		return nil, nil
	}
	if revisionNumber < 0 || !revisionID.Valid || !updatedAt.Valid || !validUUID(revisionID.String) {
		return nil, ErrInvalidStoredState
	}
	if _, err := parseTime(updatedAt.String); err != nil {
		return nil, ErrInvalidStoredState
	}
	profile, requestDigest, err := readGatewayRevision(ctx, tx, revisionID.String, revisionNumber)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidStoredState
	}
	if err != nil {
		return nil, err
	}
	if err := validateGatewayUpgradeStartupProfile(ctx, tx, profile, requestDigest); err != nil {
		return nil, err
	}
	return &profile, nil
}

func readGatewayUpgradeStartupOperationIDs(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT operation_id FROM lan_gateway_upgrade_claims
		ORDER BY approved_at,operation_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	operationIDs := make([]string, 0)
	for rows.Next() {
		var operationID string
		if err := rows.Scan(&operationID); err != nil {
			return nil, err
		}
		if !validUUID(operationID) {
			return nil, ErrInvalidStoredState
		}
		operationIDs = append(operationIDs, operationID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return operationIDs, nil
}

func validateGatewayUpgradeStartupProfile(ctx context.Context, tx *sql.Tx, profile GatewayProfileRevision, storedRequestDigest string) error {
	if profile.RevisionNumber <= 0 || !validUUID(profile.ID) || !validUUID(profile.OperationID) ||
		!validUUID(profile.ApprovedBy) || !validDigest(profile.SpecDigest) || !validDigest(storedRequestDigest) {
		return ErrInvalidStoredState
	}
	wantRequestDigest, err := gatewayRequestDigest(ConfigureGatewayInput{
		OperationID:            profile.OperationID,
		ExpectedRevisionNumber: profile.RevisionNumber - 1,
		Spec:                   profile.Spec,
		Approval: Approval{
			Action:     ActionConfigureGateway,
			SpecDigest: profile.SpecDigest,
			ActorID:    profile.ApprovedBy,
		},
	}, profile.Spec)
	if err != nil || wantRequestDigest != storedRequestDigest {
		return ErrInvalidStoredState
	}
	if _, err := gatewayUpgradeStartupActorRole(ctx, tx, profile.ApprovedBy); errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidStoredState
	} else if err != nil {
		return err
	}
	return nil
}

func gatewayUpgradeStartupActorRole(ctx context.Context, query rowQuerier, actorID string) (string, error) {
	if !validUUID(actorID) {
		return "", ErrInvalidStoredState
	}
	var role string
	if err := query.QueryRowContext(ctx, `SELECT role FROM users WHERE id=?`, actorID).Scan(&role); err != nil {
		return "", err
	}
	if !validOpaqueText(role, 64) {
		return "", ErrInvalidStoredState
	}
	return role, nil
}
