package generatedingress

import (
	"context"
	"reflect"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	GatewayV2LANSuccessorSameAppNewPort404 = "same_app_new_port_404"
	GatewayV2LANSuccessorSamePort          = "same_port_successor"
)

// GatewayV2LANDisableSuccessorObservation attests a historical disable after
// a distinct, currently healthy grant superseded its former allocation.
// It contains no address or URL. The caller must establish the DB chronology
// of a cross-app successor, which cannot be inferred from ingress identities.
type GatewayV2LANDisableSuccessorObservation struct {
	Request              GatewayV2LANDisableRequest
	Successor            GatewayV2LANGrantRequest
	Relation             string
	GatewayOperationID   string
	ProtectedStateDigest string
	ObservedAt           time.Time
}

// gatewayV2LANPortAbsenceProver performs the exact selected-interface,
// wrong-Host, container challenge, 404, and loopback checks for a retired port
// while the current app may have a different LAN binding.
type gatewayV2LANPortAbsenceProver interface {
	provePortAbsent(context.Context, gatewayV2RouteState, gatewayMigrationJournal, uint16) bool
}

func (d managerGatewayV2LANGrantDriver) provePortAbsent(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, port uint16,
) bool {
	if d.manager == nil || d.manager.attestCommittedV2Locked(ctx, state, journal) != nil ||
		!validSHA256(journal.Resources.FinalContainerID) {
		return false
	}
	return proveGatewayV2LANRollbackPublication(ctx, state, port, journal.Resources.FinalContainerID,
		probeGatewayV2HostStatus, d.manager.probeGatewayV2ContainerChallenge)
}

// AttestGatewayV2LANDisableSuccessor acknowledges one old committed disable
// only after a fresh, locked proof of its exact current successor and the
// complete healthy gateway topology. It never changes a route. The callback
// must perform one short conditional DB transaction and must not call Manager.
func (m *Manager) AttestGatewayV2LANDisableSuccessor(ctx context.Context,
	request GatewayV2LANDisableRequest, successor GatewayV2LANGrantRequest, relation string,
	grants []GatewayV2LANStartupClaim, disables []GatewayV2LANDisableStartupClaim,
	acknowledge func(context.Context, GatewayV2LANDisableSuccessorObservation) error,
) (resultErr error) {
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if m == nil || ctx == nil || acknowledge == nil || err != nil ||
		!validGatewayV2LANDisableRequest(request) ||
		!validGatewayV2LANSuccessorRelation(request, successor, relation) {
		return &Error{Code: DiagnosticValidationFailed}
	}
	old, exists := claims.disables[request.OperationID]
	current, currentExists := claims.grants.byAttempt[successor.AttemptID]
	if !exists || !reflect.DeepEqual(old.Request, request) ||
		old.State != appaccess.AppAccessDisableCommitted || old.ClearAcknowledged ||
		!currentExists || current.Request != successor ||
		current.State != appaccess.AppAccessGrantCommitted || current.RequiresRecovery ||
		current.DisableIntentOperationID != "" {
		return &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	proofCtx, cancelProof := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelProof()
	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil || !validGatewayV2RouteState(state) || state.Pending != nil {
		return gatewayV2LANDisableError(proofCtx)
	}
	driver := m.gatewayV2LANDisableDriver()
	if !gatewayV2LANSuccessorCensusMatches(state, claims, request, successor) ||
		driver.selectedInterfacePreflight(state.Profile) != nil ||
		!driver.proveCommitted(proofCtx, state, journal) ||
		!driver.proveAllGranted(proofCtx, state, journal) ||
		!driver.proveGranted(proofCtx, state, journal, successor) {
		return gatewayV2LANDisableError(proofCtx)
	}
	if relation == GatewayV2LANSuccessorSameAppNewPort404 {
		prover, ok := driver.(gatewayV2LANPortAbsenceProver)
		if !ok || !prover.provePortAbsent(proofCtx, state, journal, request.Port) {
			return gatewayV2LANDisableError(proofCtx)
		}
	}
	if driver.selectedInterfacePreflight(state.Profile) != nil || proofCtx.Err() != nil {
		return gatewayV2LANDisableError(proofCtx)
	}
	// Complete the final protected-state and live-route reread before the
	// callback can commit an immutable acknowledgment and lift admission.
	confirmed, confirmedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || proofCtx.Err() != nil || !reflect.DeepEqual(confirmed, state) ||
		!reflect.DeepEqual(confirmedJournal, journal) ||
		driver.selectedInterfacePreflight(state.Profile) != nil ||
		!driver.proveCommitted(proofCtx, state, journal) ||
		!driver.proveAllGranted(proofCtx, state, journal) ||
		!driver.proveGranted(proofCtx, state, journal, successor) {
		return gatewayV2LANDisableError(proofCtx)
	}
	if relation == GatewayV2LANSuccessorSameAppNewPort404 {
		prover := driver.(gatewayV2LANPortAbsenceProver)
		if !prover.provePortAbsent(proofCtx, state, journal, request.Port) {
			return gatewayV2LANDisableError(proofCtx)
		}
	}
	digest, err := canonicalDigest(state)
	if err != nil || !validSHA256(digest) {
		return gatewayV2LANDisableError(proofCtx)
	}
	observation := GatewayV2LANDisableSuccessorObservation{
		Request: request, Successor: successor, Relation: relation,
		GatewayOperationID: state.OperationID, ProtectedStateDigest: digest,
		ObservedAt: time.Now().UTC(),
	}
	if err := acknowledge(proofCtx, observation); err != nil {
		return err
	}
	return nil
}

func validGatewayV2LANSuccessorRelation(old GatewayV2LANDisableRequest,
	successor GatewayV2LANGrantRequest, relation string,
) bool {
	if _, err := gatewayV2LANBindingForRequest(successor); err != nil ||
		old.AllocationID == successor.AllocationID || old.OwnerOperationID == successor.OwnerOperationID ||
		old.GatewayProfileRevisionID != successor.GatewayProfileRevisionID ||
		old.GatewayProfileRevisionNumber != successor.GatewayProfileRevisionNumber ||
		old.GatewayProfileSpecDigest != successor.GatewayProfileSpecDigest {
		return false
	}
	if old.AppID == successor.AppID &&
		(successor.AccessRevisionNumber <= old.AccessRevisionNumber ||
			successor.AccessRevisionID == old.AccessRevisionID) {
		return false
	}
	switch relation {
	case GatewayV2LANSuccessorSameAppNewPort404:
		return old.AppID == successor.AppID && old.Port != successor.Port
	case GatewayV2LANSuccessorSamePort:
		return old.Port == successor.Port
	default:
		return false
	}
}

func gatewayV2LANSuccessorCensusMatches(state gatewayV2RouteState, claims gatewayV2LANAccessStartupClaims,
	old GatewayV2LANDisableRequest, successor GatewayV2LANGrantRequest,
) bool {
	if state.Pending != nil || state.Profile.RevisionID != old.GatewayProfileRevisionID ||
		state.Profile.RevisionNumber != old.GatewayProfileRevisionNumber ||
		state.Profile.SpecDigest != old.GatewayProfileSpecDigest ||
		old.Port < state.Profile.PortStart || old.Port > state.Profile.PortEnd {
		return false
	}
	protected := make(map[string]struct{})
	for appID, app := range state.Apps {
		if app.LAN == nil {
			continue
		}
		binding := *app.LAN
		if binding.AllocationID == old.AllocationID ||
			(appID == old.AppID && binding.OwnerOperationID == old.OwnerOperationID &&
				binding.AccessRevisionID == old.AccessRevisionID) ||
			(old.SourceGrant != nil && binding.GrantAttemptID == old.SourceGrant.AttemptID) {
			return false
		}
		request := gatewayV2LANGrantRequestForBinding(appID, binding)
		claim, exists := claims.grants.byAttempt[request.AttemptID]
		if !exists || claim.Request != request || claim.State != appaccess.AppAccessGrantCommitted ||
			claim.RequiresRecovery || claim.DisableIntentOperationID != "" {
			return false
		}
		if _, disabled := claims.byAllocation[request.AllocationID]; disabled {
			return false
		}
		protected[request.AttemptID] = struct{}{}
	}
	app, exists := state.Apps[successor.AppID]
	if !exists || app.LAN == nil || gatewayV2LANGrantRequestForBinding(successor.AppID, *app.LAN) != successor {
		return false
	}
	for attemptID, grant := range claims.grants.byAttempt {
		if _, live := protected[attemptID]; live {
			continue
		}
		if grant.State == appaccess.AppAccessGrantRolledBack {
			continue
		}
		disable, exists := claims.disables[grant.DisableIntentOperationID]
		if !exists || grant.State != appaccess.AppAccessGrantCommitted ||
			disable.Request.SourceGrant == nil || *disable.Request.SourceGrant != grant.Request ||
			disable.State != appaccess.AppAccessDisableCommitted {
			return false
		}
	}
	for _, disable := range claims.disables {
		if disable.State != appaccess.AppAccessDisableCommitted {
			return false
		}
		if disable.Request.SourceGrant != nil {
			if _, live := protected[disable.Request.SourceGrant.AttemptID]; live {
				return false
			}
		}
	}
	return true
}
