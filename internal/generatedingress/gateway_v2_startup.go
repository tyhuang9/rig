package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// GatewayV2StartupDisposition tells the process bootstrap which gateway mode
// can be exposed from one locked, read-only reconciliation of SQLite claims,
// protected ingress history, and the current host topology.
type GatewayV2StartupDisposition string

const (
	GatewayV2StartupNormalV1     GatewayV2StartupDisposition = "normal_v1"
	GatewayV2StartupNormalV2     GatewayV2StartupDisposition = "normal_v2"
	GatewayV2StartupRecoveryOnly GatewayV2StartupDisposition = "recovery_only"
)

// GatewayV2StartupClaim is constructed from one validated appaccess startup
// snapshot. Request retains the complete immutable approval and profile
// binding; State and StateSequence are copied from the same durable claim.
type GatewayV2StartupClaim struct {
	Request       GatewayV2UpgradeRequest
	State         appaccess.GatewayProfileUpgradeState
	StateSequence int64
}

// GatewayV2StartupInspection contains no endpoint or host information.
// OperationID is set only when startup must expose the recovery surface for
// one exact operation, or when normal v2 is owned by one committed operation.
type GatewayV2StartupInspection struct {
	Disposition GatewayV2StartupDisposition
	OperationID string
}

type gatewayV2StartupClaimSet struct {
	byOperation map[string]GatewayV2StartupClaim
	activeID    string
}

// InspectGatewayV2Startup classifies startup while holding both gateway
// writer locks. It is deliberately read-only: it does not recover, provision,
// install receipts, transition journals, or mutate Docker resources.
func (m *Manager) InspectGatewayV2Startup(ctx context.Context, claims []GatewayV2StartupClaim) (
	inspection GatewayV2StartupInspection, resultErr error,
) {
	claimSet, err := validateGatewayV2StartupClaims(claims)
	if m == nil || ctx == nil || err != nil {
		return GatewayV2StartupInspection{}, &Error{Code: DiagnosticValidationFailed}
	}
	release, err := m.lockGateway(ctx)
	if err != nil {
		return GatewayV2StartupInspection{}, err
	}
	defer func() {
		if err := release(); err != nil {
			inspection = GatewayV2StartupInspection{}
			resultErr = &Error{Code: DiagnosticRouteUnresolved}
		}
	}()

	history, err := m.scanGatewayUpgradeHistoryLockedMode(true, claimSet.activeID)
	if err != nil {
		return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
	}
	source, err := m.store.load()
	if err != nil {
		return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
	}

	matched := make(map[string]struct{}, len(history.generations))
	allHistoryTerminal := true
	recoveryOperationID := ""
	var committed *gatewayUpgradeGenerationSelection
	for index := range history.generations {
		selection := history.generations[index]
		claim, ok := claimSet.byOperation[selection.operationID]
		if !ok || !gatewayV2StartupClaimMatchesSelection(claim, selection, m.options.HostPort) {
			return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
		}
		matched[selection.operationID] = struct{}{}

		switch {
		case gatewayV2StartupSelectionHasTerminalReceipt(selection):
			switch claim.State {
			case appaccess.GatewayProfileUpgradeRolledBack:
			case appaccess.GatewayProfileUpgradePrepared,
				appaccess.GatewayProfileUpgradeServing,
				appaccess.GatewayProfileUpgradeUnresolved:
				recoveryOperationID = claim.Request.OperationID
			default:
				return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
		case selection.Existing && !selection.Aborted && selection.Journal.Phase == gatewayPhaseCommitted:
			allHistoryTerminal = false
			switch claim.State {
			case appaccess.GatewayProfileUpgradeCommitted:
				copy := selection
				committed = &copy
			case appaccess.GatewayProfileUpgradePrepared,
				appaccess.GatewayProfileUpgradeServing,
				appaccess.GatewayProfileUpgradeUnresolved:
				recoveryOperationID = claim.Request.OperationID
			default:
				return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
		default:
			allHistoryTerminal = false
			if !gatewayV2StartupClaimIsNonterminal(claim.State) {
				return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
			recoveryOperationID = claim.Request.OperationID
		}
	}

	var noArtifactClaim *GatewayV2StartupClaim
	for operationID, claim := range claimSet.byOperation {
		if _, ok := matched[operationID]; ok {
			continue
		}
		if noArtifactClaim != nil || !allHistoryTerminal ||
			(claim.State != appaccess.GatewayProfileUpgradePrepared &&
				claim.State != appaccess.GatewayProfileUpgradeUnresolved) {
			return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
		}
		copy := claim
		noArtifactClaim = &copy
		recoveryOperationID = operationID
	}

	proofCtx, cancel := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancel()
	switch {
	case committed != nil && recoveryOperationID == "":
		if !m.proveGatewayV2StartupCommitted(proofCtx, source, *committed) {
			return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
		}
		inspection = GatewayV2StartupInspection{
			Disposition: GatewayV2StartupNormalV2,
			OperationID: committed.operationID,
		}
	case allHistoryTerminal:
		for _, selection := range history.generations {
			claim := claimSet.byOperation[selection.operationID]
			if !m.proveGatewayV2StartupTerminalReceipt(proofCtx, source, selection, claim) {
				return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
		}
		if noArtifactClaim != nil && !m.proveGatewayV2StartupNoArtifacts(proofCtx, source, *noArtifactClaim) {
			return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
		}
		if recoveryOperationID != "" {
			inspection = GatewayV2StartupInspection{
				Disposition: GatewayV2StartupRecoveryOnly,
				OperationID: recoveryOperationID,
			}
		} else {
			inspection = GatewayV2StartupInspection{Disposition: GatewayV2StartupNormalV1}
		}
	default:
		if recoveryOperationID == "" {
			return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
		}
		inspection = GatewayV2StartupInspection{
			Disposition: GatewayV2StartupRecoveryOnly,
			OperationID: recoveryOperationID,
		}
	}

	confirmedSource, err := m.store.load()
	if err != nil || !reflect.DeepEqual(source, confirmedSource) || ctx.Err() != nil {
		return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
	}
	confirmedHistory, err := m.scanGatewayUpgradeHistoryLockedMode(true, claimSet.activeID)
	if err != nil || !sameGatewayV2StartupHistory(history, confirmedHistory) {
		return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
	}
	return inspection, nil
}

func validateGatewayV2StartupClaims(claims []GatewayV2StartupClaim) (gatewayV2StartupClaimSet, error) {
	result := gatewayV2StartupClaimSet{byOperation: make(map[string]GatewayV2StartupClaim, len(claims))}
	for _, claim := range claims {
		if _, err := gatewayV2UpgradePreparation(claim.Request); err != nil || !gatewayV2StartupClaimSequenceValid(claim) ||
			!gatewayV2StartupClaimStateValid(claim.State) {
			return gatewayV2StartupClaimSet{}, gatewayV2StartupInspectionError(nil)
		}
		operationID := claim.Request.OperationID
		if _, duplicate := result.byOperation[operationID]; duplicate {
			return gatewayV2StartupClaimSet{}, gatewayV2StartupInspectionError(nil)
		}
		result.byOperation[operationID] = claim
		if claim.State != appaccess.GatewayProfileUpgradeRolledBack {
			if result.activeID != "" {
				return gatewayV2StartupClaimSet{}, gatewayV2StartupInspectionError(nil)
			}
			result.activeID = operationID
		}
	}
	return result, nil
}

func gatewayV2StartupClaimSequenceValid(claim GatewayV2StartupClaim) bool {
	switch claim.State {
	case appaccess.GatewayProfileUpgradePrepared:
		return claim.StateSequence == 1
	case appaccess.GatewayProfileUpgradeServing:
		return claim.StateSequence == 2
	case appaccess.GatewayProfileUpgradeUnresolved,
		appaccess.GatewayProfileUpgradeCommitted,
		appaccess.GatewayProfileUpgradeRolledBack:
		return claim.StateSequence >= 2
	default:
		return false
	}
}

func gatewayV2StartupClaimStateValid(state appaccess.GatewayProfileUpgradeState) bool {
	switch state {
	case appaccess.GatewayProfileUpgradePrepared,
		appaccess.GatewayProfileUpgradeServing,
		appaccess.GatewayProfileUpgradeUnresolved,
		appaccess.GatewayProfileUpgradeCommitted,
		appaccess.GatewayProfileUpgradeRolledBack:
		return true
	default:
		return false
	}
}

func gatewayV2StartupClaimIsNonterminal(state appaccess.GatewayProfileUpgradeState) bool {
	return state == appaccess.GatewayProfileUpgradePrepared ||
		state == appaccess.GatewayProfileUpgradeServing ||
		state == appaccess.GatewayProfileUpgradeUnresolved
}

func gatewayV2StartupSelectionHasTerminalReceipt(selection gatewayUpgradeGenerationSelection) bool {
	return selection.Existing &&
		(selection.Aborted || (selection.Retired && selection.Journal.Phase == gatewayPhaseRolledBack))
}

func gatewayV2StartupClaimMatchesSelection(claim GatewayV2StartupClaim,
	selection gatewayUpgradeGenerationSelection, localHostPort uint16,
) bool {
	if selection.Store == nil || claim.Request.OperationID != selection.operationID {
		return false
	}
	switch {
	case selection.Aborted:
		receipt, err := selection.Store.loadPreJournalAbortReceipt()
		return err == nil && receipt.Source.LocalHostPort == localHostPort &&
			gatewayV2PreparationAbortRequestMatchesReceipt(claim.Request, receipt)
	case selection.PartialState:
		return gatewayV2PreparationAbortRequestMatchesState(claim.Request, selection.State)
	case selection.Existing:
		return selection.Journal.Source.LocalHostPort == localHostPort &&
			gatewayV2RequestMatchesState(claim.Request, selection.State, selection.Journal)
	default:
		return false
	}
}

func (m *Manager) proveGatewayV2StartupNoArtifacts(ctx context.Context, source routeState,
	claim GatewayV2StartupClaim,
) bool {
	preparation, err := gatewayV2UpgradePreparation(claim.Request)
	if err != nil {
		return false
	}
	preparation.LocalHostPort = m.options.HostPort
	driver := m.gatewayV2PreparationAbortDriver()
	if driver == nil {
		return false
	}
	digest, err := driver.attestPreparationAbort(ctx, source, preparation)
	return err == nil && validSHA256(digest)
}

func (m *Manager) proveGatewayV2StartupTerminalReceipt(ctx context.Context, source routeState,
	selection gatewayUpgradeGenerationSelection, claim GatewayV2StartupClaim,
) bool {
	if !gatewayV2StartupSelectionHasTerminalReceipt(selection) {
		return false
	}
	if selection.Aborted {
		return m.proveGatewayV2StartupNoArtifacts(ctx, source, claim)
	}
	if _, err := selection.Store.loadRollbackRetirementReceipt(selection.State, selection.Journal); err != nil {
		return false
	}
	driver := m.gatewayV2RollbackRetirementDriver()
	if driver == nil {
		return false
	}
	observation, err := driver.observeRollbackRetirement(ctx, source, selection.State, selection.Journal)
	return err == nil && observation.complete()
}

func (m *Manager) proveGatewayV2StartupCommitted(ctx context.Context, source routeState,
	selection gatewayUpgradeGenerationSelection,
) bool {
	if selection.Store == nil || !selection.Existing || selection.Aborted ||
		selection.Journal.Phase != gatewayPhaseCommitted {
		return false
	}
	driver := m.gatewayV2TransferDriver
	if driver == nil {
		driver = managerGatewayV2TransferDriver{manager: m}
	}
	return driver.observeTopology(ctx, source, selection.State, selection.Journal) == gatewayTopologyExactFinalV2 &&
		driver.proveFinalHostRoutes(ctx, selection.State, selection.Journal) &&
		driver.selectedInterfacePreflight(selection.State.Profile) == nil
}

func sameGatewayV2StartupHistory(left, right gatewayUpgradeHistory) bool {
	if left.committed != right.committed || len(left.generations) != len(right.generations) {
		return false
	}
	for index := range left.generations {
		if !sameObservedGatewayV2Selection(left.generations[index], right.generations[index]) {
			return false
		}
	}
	return true
}

func gatewayV2StartupInspectionError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}
