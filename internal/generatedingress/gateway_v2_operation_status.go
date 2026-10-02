package generatedingress

import (
	"context"
	"reflect"
)

// GatewayV2OperationAvailability is the fresh observed availability of one
// exact gateway-v2 operation. Unknown means the protected history or live host
// evidence was incomplete or contradictory and must not be treated as either
// serving or safely unavailable.
type GatewayV2OperationAvailability string

const (
	GatewayV2OperationUnknown     GatewayV2OperationAvailability = "unknown"
	GatewayV2OperationUnavailable GatewayV2OperationAvailability = "unavailable"
	GatewayV2OperationServing     GatewayV2OperationAvailability = "serving"
)

// GatewayV2OperationStatus intentionally contains no host or LAN URL. The
// authenticated controller owns presentation of an approved endpoint.
type GatewayV2OperationStatus struct {
	OperationID  string                         `json:"operationId"`
	Availability GatewayV2OperationAvailability `json:"availability"`
}

// ObserveGatewayV2Operation reads the latest exact operation while holding the
// Manager and cross-process gateway locks. It never mutates history, performs
// recovery, or changes Docker resources. Serving requires both a committed
// journal and fresh exact final-v2 Docker, host-route, and interface proof.
func (m *Manager) ObserveGatewayV2Operation(ctx context.Context, operationID string) (
	status GatewayV2OperationStatus, resultErr error,
) {
	status = GatewayV2OperationStatus{OperationID: operationID, Availability: GatewayV2OperationUnknown}
	if m == nil || ctx == nil || !validCanonicalUUID(operationID) {
		return status, &Error{Code: DiagnosticValidationFailed}
	}
	release, err := m.lockGateway(ctx)
	if err != nil {
		return status, err
	}
	defer func() {
		if err := release(); err != nil {
			status.Availability = GatewayV2OperationUnknown
			resultErr = &Error{Code: DiagnosticRouteUnresolved}
		}
	}()

	selection, err := m.resolveGatewayUpgradeGenerationLocked(operationID)
	if err != nil || selection.Store == nil {
		return status, gatewayV2OperationObservationError(ctx)
	}
	if !selection.Existing {
		return status, nil
	}
	if selection.Aborted {
		if !m.observeAbortedGatewayV2Operation(ctx, selection) {
			return status, gatewayV2OperationObservationError(ctx)
		}
		status.Availability = GatewayV2OperationUnavailable
		return status, nil
	}
	if selection.Journal.Phase == gatewayPhaseRolledBack {
		if !m.observeRolledBackGatewayV2Operation(ctx, selection) {
			return status, gatewayV2OperationObservationError(ctx)
		}
		status.Availability = GatewayV2OperationUnavailable
		return status, nil
	}
	if selection.Journal.Phase != gatewayPhaseCommitted {
		return status, nil
	}

	driver := m.gatewayV2TransferDriver
	if driver == nil {
		driver = managerGatewayV2TransferDriver{manager: m}
	}
	source, err := m.store.load()
	if err != nil || driver.observeTopology(ctx, source, selection.State, selection.Journal) != gatewayTopologyExactFinalV2 ||
		!driver.proveFinalHostRoutes(ctx, selection.State, selection.Journal) ||
		driver.selectedInterfacePreflight(selection.State.Profile) != nil {
		return status, gatewayV2OperationObservationError(ctx)
	}
	confirmedSource, err := m.store.load()
	if err != nil || !reflect.DeepEqual(source, confirmedSource) {
		return status, gatewayV2OperationObservationError(ctx)
	}
	confirmed, err := m.resolveGatewayUpgradeGenerationLocked(operationID)
	if err != nil || !sameObservedGatewayV2Selection(selection, confirmed) {
		return status, gatewayV2OperationObservationError(ctx)
	}
	status.Availability = GatewayV2OperationServing
	return status, nil
}

func (m *Manager) observeAbortedGatewayV2Operation(ctx context.Context, selection gatewayUpgradeGenerationSelection) bool {
	if m == nil || ctx == nil || selection.Store == nil || !selection.Existing || !selection.Aborted {
		return false
	}
	receipt, err := selection.Store.loadPreJournalAbortReceipt()
	if err != nil || receipt.OperationID != selection.operationID || receipt.Source.LocalHostPort != m.options.HostPort {
		return false
	}
	request := GatewayV2UpgradeRequest{
		OperationID: receipt.OperationID,
		Profile: GatewayV2ProfileBinding{
			RevisionID: receipt.Profile.RevisionID, RevisionNumber: receipt.Profile.RevisionNumber,
			SpecDigest: receipt.Profile.SpecDigest, SelectedIPv4: receipt.Profile.SelectedIPv4,
			InterfaceID: receipt.Profile.InterfaceID, PortStart: receipt.Profile.PortStart, PortEnd: receipt.Profile.PortEnd,
		},
		ApprovedBy: receipt.Action.ApprovedBy, ApprovedActionDigest: receipt.Action.Digest,
	}
	preparation, err := gatewayV2UpgradePreparation(request)
	if err != nil {
		return false
	}
	preparation.LocalHostPort = m.options.HostPort
	source, err := m.store.load()
	if err != nil {
		return false
	}
	driver := m.gatewayV2PreparationAbortDriver()
	if driver == nil {
		return false
	}
	identityDigest, err := driver.attestPreparationAbort(ctx, source, preparation)
	if err != nil || !validSHA256(identityDigest) {
		return false
	}
	confirmedSource, err := m.store.load()
	if err != nil || !reflect.DeepEqual(source, confirmedSource) {
		return false
	}
	confirmed, err := m.resolveGatewayUpgradeGenerationLocked(receipt.OperationID)
	confirmedReceipt, receiptErr := selection.Store.loadPreJournalAbortReceipt()
	return err == nil && receiptErr == nil && reflect.DeepEqual(receipt, confirmedReceipt) &&
		sameGatewayV2PreparationAbortSelection(selection, confirmed)
}

func (m *Manager) observeRolledBackGatewayV2Operation(ctx context.Context, selection gatewayUpgradeGenerationSelection) bool {
	if m == nil || ctx == nil || selection.Store == nil || !selection.Existing ||
		selection.Aborted || selection.Journal.Phase != gatewayPhaseRolledBack {
		return false
	}
	driver := m.gatewayV2RollbackRetirementDriver()
	if driver == nil {
		return false
	}
	source, err := m.store.load()
	if err != nil {
		return false
	}
	if _, err := driver.observeRollbackRetirement(ctx, source, selection.State, selection.Journal); err != nil {
		return false
	}
	confirmedSource, err := m.store.load()
	return err == nil && reflect.DeepEqual(source, confirmedSource) &&
		m.gatewayV2RollbackRetirementSelectionMatches(
			selection.Store, selection.State, selection.Journal, selection.Retired,
		)
}

func sameObservedGatewayV2Selection(a, b gatewayUpgradeGenerationSelection) bool {
	return a.Existing == b.Existing && a.PartialState == b.PartialState && a.Retired == b.Retired && a.Aborted == b.Aborted &&
		a.Generation == b.Generation && a.operationID == b.operationID && reflect.DeepEqual(a.State, b.State) &&
		reflect.DeepEqual(a.Journal, b.Journal) && sameGatewayV2PreparationAbortStore(a.Store, b.Store)
}

func gatewayV2OperationObservationError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return gatewayV2CoordinatorError(ctx)
	}
	return nil
}
