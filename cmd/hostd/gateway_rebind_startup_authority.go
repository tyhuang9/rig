package main

import (
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedingress"
)

// Only a complete same-transaction SQL census can identify the selected
// rebound gateway. Raw grant/disable operations keep their own identities.
func committedGatewayStartupInspection(snapshot appaccess.HostingGatewayStartupSnapshot,
	inspection generatedingress.GatewayV2StartupInspection,
) bool {
	source := inspection.CurrentGatewaySource
	current := snapshot.Rebind
	if source == (appaccess.GatewayCurrentAuthorityRef{}) {
		return (current.CurrentSource == nil || current.CurrentSource.Kind == appaccess.GatewayRebindSourceGatewayUpgrade) &&
			committedGatewayStartupOperation(snapshot.Upgrades, inspection.OperationID)
	}
	if source.Kind != appaccess.GatewayRebindSourceGatewayRebind || source.OperationID == "" ||
		inspection.OperationID != source.OperationID || source.TerminalReceiptDigest == "" ||
		current.CurrentSource == nil || *current.CurrentSource != source || current.CurrentProfile == nil ||
		current.CurrentProfile.ID != source.ProfileRevisionID || current.CurrentProfile.RevisionNumber != source.ProfileRevisionNumber ||
		current.CurrentProfile.SpecDigest != source.ProfileSpecDigest ||
		!reflect.DeepEqual(snapshot.Upgrades.CurrentProfile, current.CurrentProfile) ||
		current.Active != nil || current.Phase != "" || current.DatabaseCommittedEvent != nil ||
		current.DatabaseCommitObserved || current.RollbackAllowed || current.CurrentDatabaseCommittedEvent == nil {
		return false
	}
	event := *current.CurrentDatabaseCommittedEvent
	if event.OperationID != source.OperationID || event.State != appaccess.GatewayRebindDatabaseCommitted || event.Sequence <= 0 {
		return false
	}
	matches := 0
	for _, entry := range current.History {
		var operationID string
		var state appaccess.GatewayRebindState
		switch {
		case entry.Claim.SpecVersion == 1 && entry.Claim.Legacy != nil && entry.Claim.V2 == nil:
			operationID, state = entry.Claim.Legacy.Spec.OperationID, entry.Claim.Legacy.State
		case entry.Claim.SpecVersion == appaccess.GatewayRebindSpecVersionV2 && entry.Claim.Legacy == nil && entry.Claim.V2 != nil:
			operationID, state = entry.Claim.V2.Spec.OperationID, entry.Claim.V2.State
		default:
			return false
		}
		if operationID != source.OperationID {
			continue
		}
		if state != appaccess.GatewayRebindCommitted {
			return false
		}
		matches++
		commits := 0
		for _, retained := range entry.Events {
			if retained.State == appaccess.GatewayRebindDatabaseCommitted {
				if !reflect.DeepEqual(retained, event) {
					return false
				}
				commits++
			}
		}
		if commits != 1 {
			return false
		}
	}
	return matches == 1
}
