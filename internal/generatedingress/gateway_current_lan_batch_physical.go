package generatedingress

import (
	"context"
	"errors"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	gatewayCurrentLANRecoveryPhysicalActionVersion = 1
	gatewayCurrentLANRecoveryPhysicalActionPurpose = "hostd/generated-ingress/routes/current/lan-recovery-physical/v1"
)

// gatewayCurrentLANRecoveryPhysicalAction is the complete immutable input for
// one whole-batch physical withdrawal. Selected is the exact protected state
// observed with SQL authority. Cleared is only the ordered head transition the
// caller may later persist after SQL terminal proof. Before and Withdrawn are
// same-revision runtime projections; Withdrawn removes every immutable batch
// binding so a blocked head cannot leave a later unauthorized listener live.
// The driver never advances or clears protected state.
type gatewayCurrentLANRecoveryPhysicalAction struct {
	Version   int
	Purpose   string
	Lineage   appaccess.GatewayCurrentLineageRef
	Terminal  gatewayRebindAttemptTerminalView
	Selected  gatewayCurrentRouteState
	Head      gatewayCurrentLANRecoveryItem
	Before    gatewayCurrentRouteState
	Withdrawn gatewayCurrentRouteState
	Cleared   gatewayCurrentRouteState
	Digest    string
}

type gatewayCurrentLANRecoveryPhysicalResult struct {
	ActionDigest string
	BatchAbsent  bool
	Attestation  gatewayCurrentPhysicalAttestation
	Digest       string
}

// This optional extension keeps the ordinary single-transition driver stable.
// The managed driver implements it with the same exact Docker inventory and
// runtime reconciler used by ordinary operations. Tests or legacy drivers that
// do not implement batch effects fail closed at the wrapper.
type gatewayCurrentLANRecoveryPhysicalDriver interface {
	attestGatewayCurrentLANRecoveryBatch(context.Context,
		gatewayCurrentLANRecoveryPhysicalAction) (gatewayCurrentLANRecoveryPhysicalResult, error)
	withdrawGatewayCurrentLANRecoveryBatch(context.Context,
		gatewayCurrentLANRecoveryPhysicalAction) (gatewayCurrentLANRecoveryPhysicalResult, error)
}

func (m *Manager) gatewayCurrentLANRecoveryPhysicalActionForSelectionLocked(
	selection gatewayCurrentSelection,
) (gatewayCurrentLANRecoveryPhysicalAction, error) {
	invalid := errors.New("invalid current gateway LAN recovery physical action")
	terminal, err := gatewayCurrentSelectionTerminalView(selection)
	if m == nil || selection.Kind != gatewayCurrentSelectionRebind || selection.State == nil ||
		err != nil || selection.Lineage != selection.State.Lineage ||
		!gatewayRebindAttemptTerminalMatchesLineage(terminal, selection.Lineage) ||
		selection.State.LANRecovery == nil || selection.State.Pending != nil ||
		selection.State.LANRecovery.Head < 0 ||
		selection.State.LANRecovery.Head >= len(selection.State.LANRecovery.Items) {
		return gatewayCurrentLANRecoveryPhysicalAction{}, invalid
	}
	selected := cloneGatewayCurrentRouteState(*selection.State)
	cleared, err := gatewayCurrentLANRecoveryClearedHeadState(selected)
	if err != nil {
		return gatewayCurrentLANRecoveryPhysicalAction{}, invalid
	}
	projection, err := gatewayCurrentPhysicalOutcomeProjectionForSelection(selected)
	if err != nil || projection.Effective != nil || projection.Withdrawn == nil {
		return gatewayCurrentLANRecoveryPhysicalAction{}, invalid
	}
	action := gatewayCurrentLANRecoveryPhysicalAction{
		Version: gatewayCurrentLANRecoveryPhysicalActionVersion,
		Purpose: gatewayCurrentLANRecoveryPhysicalActionPurpose,
		Lineage: selection.Lineage, Terminal: terminal, Selected: selected,
		Head:   cloneGatewayCurrentLANRecoveryItem(selected.LANRecovery.Items[selected.LANRecovery.Head]),
		Before: projection.Before, Withdrawn: *projection.Withdrawn, Cleared: cleared,
	}
	action.Digest, err = gatewayCurrentLANRecoveryPhysicalActionDigest(action)
	if err != nil || !validGatewayCurrentLANRecoveryPhysicalAction(action) {
		return gatewayCurrentLANRecoveryPhysicalAction{}, invalid
	}
	return action, nil
}

func (m *Manager) attestGatewayCurrentLANRecoveryBatchLocked(ctx context.Context,
	selection gatewayCurrentSelection,
) (gatewayCurrentLANRecoveryPhysicalResult, error) {
	if m == nil || ctx == nil || ctx.Err() != nil {
		return gatewayCurrentLANRecoveryPhysicalResult{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	action, err := m.gatewayCurrentLANRecoveryPhysicalActionForSelectionLocked(selection)
	driver, ok := m.currentPhysicalDriver().(gatewayCurrentLANRecoveryPhysicalDriver)
	if err != nil || !ok {
		return gatewayCurrentLANRecoveryPhysicalResult{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	result, err := driver.attestGatewayCurrentLANRecoveryBatch(ctx, action)
	if err != nil || !validGatewayCurrentLANRecoveryPhysicalResult(action, result) {
		return gatewayCurrentLANRecoveryPhysicalResult{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	return result, nil
}

func (m *Manager) withdrawGatewayCurrentLANRecoveryBatchLocked(ctx context.Context,
	selection gatewayCurrentSelection,
) (gatewayCurrentLANRecoveryPhysicalResult, error) {
	if m == nil || ctx == nil || ctx.Err() != nil {
		return gatewayCurrentLANRecoveryPhysicalResult{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	action, err := m.gatewayCurrentLANRecoveryPhysicalActionForSelectionLocked(selection)
	driver, ok := m.currentPhysicalDriver().(gatewayCurrentLANRecoveryPhysicalDriver)
	if err != nil || !ok {
		return gatewayCurrentLANRecoveryPhysicalResult{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	result, err := driver.withdrawGatewayCurrentLANRecoveryBatch(ctx, action)
	if err != nil || !validGatewayCurrentLANRecoveryPhysicalResult(action, result) || !result.BatchAbsent {
		return gatewayCurrentLANRecoveryPhysicalResult{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	return result, nil
}

func gatewayCurrentLANRecoveryPhysicalActionDigest(value gatewayCurrentLANRecoveryPhysicalAction) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func validGatewayCurrentLANRecoveryPhysicalAction(value gatewayCurrentLANRecoveryPhysicalAction) bool {
	if value.Version != gatewayCurrentLANRecoveryPhysicalActionVersion ||
		value.Purpose != gatewayCurrentLANRecoveryPhysicalActionPurpose ||
		!validGatewayCurrentRouteState(value.Selected) || value.Selected.Pending != nil ||
		value.Selected.LANRecovery == nil || value.Selected.Lineage != value.Lineage ||
		!gatewayRebindAttemptTerminalMatchesLineage(value.Terminal, value.Lineage) ||
		value.Selected.LANRecovery.Head < 0 ||
		value.Selected.LANRecovery.Head >= len(value.Selected.LANRecovery.Items) ||
		!reflect.DeepEqual(value.Head,
			value.Selected.LANRecovery.Items[value.Selected.LANRecovery.Head]) {
		return false
	}
	cleared, err := gatewayCurrentLANRecoveryClearedHeadState(value.Selected)
	if err != nil || !reflect.DeepEqual(cleared, value.Cleared) {
		return false
	}
	projection, err := gatewayCurrentPhysicalOutcomeProjectionForSelection(value.Selected)
	if err != nil || projection.Effective != nil || projection.Withdrawn == nil ||
		!reflect.DeepEqual(projection.Before, value.Before) ||
		!reflect.DeepEqual(*projection.Withdrawn, value.Withdrawn) {
		return false
	}
	digest, err := gatewayCurrentLANRecoveryPhysicalActionDigest(value)
	return err == nil && digest == value.Digest
}

func gatewayCurrentLANRecoveryPhysicalResultDigest(value gatewayCurrentLANRecoveryPhysicalResult) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func validGatewayCurrentLANRecoveryPhysicalResult(action gatewayCurrentLANRecoveryPhysicalAction,
	value gatewayCurrentLANRecoveryPhysicalResult,
) bool {
	if !validGatewayCurrentLANRecoveryPhysicalAction(action) || value.ActionDigest != action.Digest ||
		!validGatewayCurrentPhysicalAttestation(value.Attestation) ||
		value.Attestation.Lineage != action.Lineage ||
		!reflect.DeepEqual(value.Attestation.Terminal, action.Terminal) ||
		!reflect.DeepEqual(value.Attestation.LANRecovery, action.Selected.LANRecovery) ||
		value.Attestation.Pending != nil {
		return false
	}
	batchAbsent, ok := gatewayCurrentLANRecoveryObservedProjection(action, value.Attestation.State)
	if !ok || batchAbsent != value.BatchAbsent {
		return false
	}
	if value.BatchAbsent {
		if value.Attestation.Outcome != gatewayCurrentPhysicalRecoveryMixed &&
			value.Attestation.Outcome != gatewayCurrentPhysicalRecoveryStopped {
			return false
		}
	} else if value.Attestation.Outcome != gatewayCurrentPhysicalRecoveryMixed {
		return false
	}
	digest, err := gatewayCurrentLANRecoveryPhysicalResultDigest(value)
	return err == nil && digest == value.Digest
}

// gatewayCurrentLANRecoveryObservedProjection accepts only the runtime
// topology obtained by independently withdrawing zero or more immutable batch
// items. Routes and unrelated authorized bindings remain exact. The complete
// absence bit is derived from that topology rather than trusted from a driver.
func gatewayCurrentLANRecoveryObservedProjection(action gatewayCurrentLANRecoveryPhysicalAction,
	observed gatewayCurrentRouteState,
) (bool, bool) {
	if !validGatewayCurrentRouteState(observed) || observed.Pending != nil || observed.LANRecovery != nil ||
		observed.Revision != action.Before.Revision || !sameGatewayCurrentRouteOrigin(observed, action.Before) ||
		len(observed.Apps) != len(action.Before.Apps) {
		return false, false
	}
	batchApps := make(map[string]struct{}, len(action.Selected.LANRecovery.Items))
	allAbsent := true
	for _, item := range action.Selected.LANRecovery.Items {
		batchApps[item.AppID] = struct{}{}
		before, beforeOK := action.Before.Apps[item.AppID]
		actual, actualOK := observed.Apps[item.AppID]
		if !beforeOK || !actualOK || !reflect.DeepEqual(actual.Route, before.Route) {
			return false, false
		}
		if actual.LAN == nil {
			continue
		}
		if before.LAN == nil || !reflect.DeepEqual(actual.LAN, before.LAN) {
			return false, false
		}
		allAbsent = false
	}
	for appID, before := range action.Before.Apps {
		if _, batched := batchApps[appID]; batched {
			continue
		}
		if !reflect.DeepEqual(observed.Apps[appID], before) {
			return false, false
		}
	}
	return allAbsent, true
}

func cloneGatewayCurrentLANRecoveryItem(value gatewayCurrentLANRecoveryItem) gatewayCurrentLANRecoveryItem {
	result := gatewayCurrentLANRecoveryItem{Kind: value.Kind, AppID: value.AppID}
	if value.Grant != nil {
		grant := cloneGatewayCurrentLANBinding(*value.Grant)
		result.Grant = &grant
	}
	if value.Disable != nil {
		disable := cloneGatewayCurrentDisableRequest(*value.Disable)
		result.Disable = &disable
	}
	return result
}
