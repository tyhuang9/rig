package appaccess

import (
	"context"
	"database/sql"
)

// GatewayRebindPreclaimProposal is an informational, unpersisted request. Its
// approvals and roster must be checked again inside a future claim-insert
// transaction; this read cannot authorize an external effect.
type GatewayRebindPreclaimProposal struct {
	Spec              GatewayRebindSpec
	RebindApproval    Approval
	ConfigureApproval Approval
	Roster            []GatewayRebindRosterEntry
}

// GatewayRebindPreclaimSnapshot is the validated proposal and current
// predecessor read from one SQLite snapshot. No claim row is created.
type GatewayRebindPreclaimSnapshot struct {
	Proposal         GatewayRebindPreclaimProposal
	ProposedClaim    GatewayRebindClaim
	CurrentProfile   GatewayProfileRevision
	PredecessorClaim GatewayProfileUpgradeClaim
	GrantBindings    []GatewayRebindStartupGrantBinding
}

type gatewayRebindReadQuery interface {
	rowQuerier
	gatewayRebindQuiescenceQuerier
}

func (r *Repository) GatewayRebindPreclaimSnapshot(ctx context.Context,
	proposal GatewayRebindPreclaimProposal,
) (GatewayRebindPreclaimSnapshot, error) {
	if r == nil || r.db == nil || ctx == nil {
		return GatewayRebindPreclaimSnapshot{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GatewayRebindPreclaimSnapshot{}, err
	}
	defer tx.Rollback()
	result, err := r.readGatewayRebindPreclaimSnapshot(ctx, tx, proposal)
	if err != nil {
		return GatewayRebindPreclaimSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return GatewayRebindPreclaimSnapshot{}, err
	}
	return result, nil
}

func validateGatewayRebindPreclaimProposal(proposal GatewayRebindPreclaimProposal) error {
	specDigest, err := GatewayRebindSpecDigest(proposal.Spec)
	if err != nil || !validApproval(proposal.RebindApproval, ActionRebindGateway, specDigest) {
		return ErrInvalidInput
	}
	successor, err := canonicalGatewaySpec(proposal.Spec.SuccessorProfile)
	if err != nil || successor != proposal.Spec.SuccessorProfile {
		return ErrInvalidInput
	}
	configureDigest, err := GatewayProfileSpecDigest(successor)
	if err != nil || !validApproval(proposal.ConfigureApproval, ActionConfigureGateway, configureDigest) {
		return ErrInvalidInput
	}
	if proposal.Spec.RosterCount != int64(len(proposal.Roster)) {
		return ErrInvalidInput
	}
	for index, entry := range proposal.Roster {
		if entry.OperationID != proposal.Spec.OperationID || entry.Ordinal != int64(index+1) ||
			entry.Port < successor.PortStart || entry.Port > successor.PortEnd {
			return ErrInvalidInput
		}
		digest, err := GatewayRebindRosterEntryDigest(entry)
		if err != nil || entry.EntryDigest != digest {
			return ErrInvalidInput
		}
	}
	rosterDigest, err := GatewayRebindRosterDigest(proposal.Roster)
	if err != nil || rosterDigest != proposal.Spec.RosterDigest {
		return ErrInvalidInput
	}
	return nil
}

func (r *Repository) readGatewayRebindPreclaimSnapshot(ctx context.Context, tx gatewayRebindReadQuery,
	proposal GatewayRebindPreclaimProposal,
) (GatewayRebindPreclaimSnapshot, error) {
	proposal.Roster = append([]GatewayRebindRosterEntry(nil), proposal.Roster...)
	if err := validateGatewayRebindPreclaimProposal(proposal); err != nil {
		return GatewayRebindPreclaimSnapshot{}, err
	}
	current, err := r.readGatewayRebindStartupSnapshot(ctx, tx)
	if err != nil {
		return GatewayRebindPreclaimSnapshot{}, err
	}
	if current.CurrentProfile == nil || len(current.Claims) != 0 {
		return GatewayRebindPreclaimSnapshot{}, ErrInvalidStoredState
	}
	profile := *current.CurrentProfile
	spec := proposal.Spec
	if profile.ID != spec.PredecessorProfileRevisionID ||
		profile.RevisionNumber != spec.PredecessorProfileRevisionNumber ||
		profile.SpecDigest != spec.PredecessorProfileSpecDigest {
		return GatewayRebindPreclaimSnapshot{}, ErrInvalidStoredState
	}
	upgrade, err := readGatewayProfileUpgradeClaim(ctx, tx, spec.PredecessorUpgradeOperationID)
	if err != nil {
		return GatewayRebindPreclaimSnapshot{}, invalidRebindStoredState(err)
	}
	if err := validateGatewayProfileUpgradeClaimHistory(ctx, tx, upgrade); err != nil {
		return GatewayRebindPreclaimSnapshot{}, invalidRebindStoredState(err)
	}
	if upgrade.State != GatewayProfileUpgradeCommitted || upgrade.ProfileRevisionID != profile.ID ||
		upgrade.ProfileRevisionNumber != profile.RevisionNumber || upgrade.ProfileSpecDigest != profile.SpecDigest {
		return GatewayRebindPreclaimSnapshot{}, ErrInvalidStoredState
	}
	for _, actorID := range []string{proposal.RebindApproval.ActorID, proposal.ConfigureApproval.ActorID} {
		administrator, err := administratorExists(ctx, tx, actorID)
		if err != nil {
			return GatewayRebindPreclaimSnapshot{}, err
		}
		if !administrator {
			return GatewayRebindPreclaimSnapshot{}, ErrInvalidStoredState
		}
	}
	var successorExists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lan_gateway_profile_revisions
		WHERE id=? OR revision_number=? OR operation_id=?
	)`, spec.SuccessorProfileRevisionID, spec.SuccessorProfileRevisionNumber,
		spec.SuccessorProfileOperationID).Scan(&successorExists); err != nil {
		return GatewayRebindPreclaimSnapshot{}, err
	}
	if successorExists != 0 {
		return GatewayRebindPreclaimSnapshot{}, ErrInvalidStoredState
	}
	if err := validateGatewayRebindPreclaimSettledRoster(ctx, tx, spec, proposal.Roster); err != nil {
		return GatewayRebindPreclaimSnapshot{}, err
	}
	proposedClaim := GatewayRebindClaim{
		Spec: spec, RebindApproval: proposal.RebindApproval,
		ConfigureApproval: proposal.ConfigureApproval,
		State:             GatewayRebindPrepared, StateSequence: 1,
	}
	proposedClaim.RequestDigest, err = gatewayRebindClaimRequestDigest(
		spec, proposal.RebindApproval, proposal.ConfigureApproval)
	if err != nil {
		return GatewayRebindPreclaimSnapshot{}, ErrInvalidInput
	}
	proposedClaim.SuccessorProfileRequestDigest, err = gatewayRequestDigest(ConfigureGatewayInput{
		OperationID: spec.SuccessorProfileOperationID, ExpectedRevisionNumber: profile.RevisionNumber,
		Spec: spec.SuccessorProfile, Approval: proposal.ConfigureApproval,
	}, spec.SuccessorProfile)
	if err != nil {
		return GatewayRebindPreclaimSnapshot{}, ErrInvalidInput
	}
	bindings := make([]GatewayRebindStartupGrantBinding, 0, len(proposal.Roster))
	seenAttempts := make(map[string]struct{}, len(proposal.Roster))
	for _, entry := range proposal.Roster {
		if _, exists := seenAttempts[entry.GrantAttemptID]; exists {
			return GatewayRebindPreclaimSnapshot{}, ErrInvalidInput
		}
		seenAttempts[entry.GrantAttemptID] = struct{}{}
		binding, err := validateGatewayRebindRosterEntry(ctx, tx, proposedClaim, entry)
		if err != nil {
			return GatewayRebindPreclaimSnapshot{}, invalidRebindStoredState(err)
		}
		bindings = append(bindings, binding)
	}
	return GatewayRebindPreclaimSnapshot{
		Proposal: GatewayRebindPreclaimProposal{
			Spec: spec, RebindApproval: proposal.RebindApproval,
			ConfigureApproval: proposal.ConfigureApproval,
			Roster:            append([]GatewayRebindRosterEntry(nil), proposal.Roster...),
		},
		ProposedClaim: proposedClaim, CurrentProfile: profile,
		PredecessorClaim: upgrade, GrantBindings: bindings,
	}, nil
}

func validateGatewayRebindPreclaimSettledRoster(ctx context.Context, tx rowQuerier,
	spec GatewayRebindSpec, roster []GatewayRebindRosterEntry,
) error {
	var liveCount, unsettled, pendingGrants, pendingDisables int64
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN state <> 'active' OR port < ? OR port > ? THEN 1 ELSE 0 END),0)
		FROM lan_port_allocations WHERE released_at IS NULL AND disabled_at IS NULL`,
		spec.SuccessorProfile.PortStart, spec.SuccessorProfile.PortEnd).Scan(&liveCount, &unsettled)
	if err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lan_app_access_grant_claims
		WHERE state IN ('prepared','applying','db_active','uncertain') AND retired_at IS NULL`).
		Scan(&pendingGrants); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lan_app_access_disable_claims
		WHERE state IN ('prepared','withdrawing','uncertain')`).Scan(&pendingDisables); err != nil {
		return err
	}
	if liveCount != int64(len(roster)) || unsettled != 0 || pendingGrants != 0 || pendingDisables != 0 {
		return ErrInvalidStoredState
	}
	return nil
}
