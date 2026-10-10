package generatedingress

import (
	"context"
	"path/filepath"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayCurrentLANHistoricalOrigin struct {
	profile   GatewayV2ProfileBinding
	state     *gatewayCurrentRouteState
	transfers []appaccess.GatewayRebindAllocationTransfer
}

// The caller holds both gateway locks and supplies its validated, same-snapshot
// claims. Retained authority proves provenance only: it never proves that an old
// listener is absent, selects the current gateway, or authorizes publication.
func (m *Manager) validateGatewayCurrentLANRetainedHistoryLocked(ctx context.Context,
	claims gatewayV2LANAccessStartupClaims, snapshot appaccess.GatewayRebindRecoverySnapshot,
) error {
	// Once a rebind is current, the SQL reader's legacy no-authority fallback
	// is no longer valid. A retired grant must retain its explicit source even
	// after clearance; dropping the projection cannot bypass historical proof.
	for _, disable := range claims.disables {
		if disable.State == appaccess.AppAccessDisableCommitted && disable.Request.SourceGrant != nil && disable.RetainedBinding == nil {
			return gatewayV2StartupInspectionError(ctx)
		}
	}
	var retained []GatewayV2LANStartupClaim
	for _, claim := range claims.grants.byAttempt {
		if claim.RetainedBinding != nil {
			retained = append(retained, claim)
		}
	}
	if len(retained) == 0 {
		return nil
	}
	invalid := gatewayV2StartupInspectionError(ctx)
	if m == nil || ctx == nil || ctx.Err() != nil || m.options.RebindCurrentStateRepository == nil {
		return invalid
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return invalid
	}
	origins := make(map[appaccess.GatewayCurrentAuthorityRef]gatewayCurrentLANHistoricalOrigin)
	load := func(source appaccess.GatewayCurrentAuthorityRef) (gatewayCurrentLANHistoricalOrigin, error) {
		if origin, found := origins[source]; found {
			return origin, nil
		}
		origin, err := m.gatewayCurrentLANHistoricalOriginLocked(history, snapshot, source)
		if err == nil {
			origins[source] = origin
		}
		return origin, err
	}
	for _, claim := range retained {
		projection := *claim.RetainedBinding
		if !validGatewayV2LANStartupGrantProjection(projection, claim.Request) ||
			!gatewayCurrentLANHistoricalChainMatches(snapshot.History, claim.Request, projection.TransferChain) {
			return invalid
		}
		origin, err := load(projection.GatewaySource)
		if err != nil || origin.profile != projection.EffectiveProfile {
			return invalid
		}
		// Check every retained link against its own protected manifest, including
		// generations that are no longer current and no longer contain a LAN app.
		for _, transfer := range projection.TransferChain {
			source := appaccess.GatewayCurrentAuthorityRef{
				Kind: appaccess.GatewayRebindSourceGatewayRebind, OperationID: transfer.OperationID,
				ProfileRevisionID:     transfer.SuccessorProfileRevisionID,
				ProfileRevisionNumber: transfer.SuccessorProfileRevisionNumber,
				ProfileSpecDigest:     transfer.SuccessorProfileSpecDigest,
				TerminalReceiptDigest: transfer.TerminalReceiptDigest,
			}
			linked, err := load(source)
			if err != nil || !gatewayCurrentLANHistoricalTransferMatches(linked.transfers, transfer) {
				return invalid
			}
		}
	}
	confirmed, err := m.options.RebindCurrentStateRepository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(snapshot, confirmed) || ctx.Err() != nil {
		return invalid
	}
	after, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !sameGatewayRebindCurrentHistory(history, after) {
		return invalid
	}
	for source, before := range origins {
		confirmed, err := m.gatewayCurrentLANHistoricalOriginLocked(after, snapshot, source)
		if err != nil || !reflect.DeepEqual(before, confirmed) || ctx.Err() != nil {
			return invalid
		}
	}
	return nil
}

func gatewayCurrentLANHistoricalChainMatches(history []appaccess.GatewayRebindHistoryEntry,
	raw GatewayV2LANGrantRequest, chain []appaccess.GatewayRebindAllocationTransfer,
) bool {
	retained := make(map[string]appaccess.GatewayRebindAllocationTransfer)
	for _, entry := range history {
		for _, transfer := range entry.Transfers {
			if transfer.AppID != raw.AppID || transfer.AllocationID != raw.AllocationID || transfer.GrantAttemptID != raw.AttemptID {
				continue
			}
			if _, duplicate := retained[transfer.TransferDigest]; duplicate {
				return false
			}
			retained[transfer.TransferDigest] = transfer
		}
	}
	if len(retained) != len(chain) {
		return false
	}
	for _, transfer := range chain {
		if !reflect.DeepEqual(retained[transfer.TransferDigest], transfer) {
			return false
		}
		delete(retained, transfer.TransferDigest)
	}
	return len(retained) == 0
}

func gatewayCurrentLANHistoricalTransferMatches(transfers []appaccess.GatewayRebindAllocationTransfer,
	want appaccess.GatewayRebindAllocationTransfer,
) bool {
	for _, transfer := range transfers {
		if transfer.AppID == want.AppID && transfer.AllocationID == want.AllocationID &&
			transfer.GrantAttemptID == want.GrantAttemptID {
			return reflect.DeepEqual(transfer, want)
		}
	}
	return false
}

func (m *Manager) gatewayCurrentLANHistoricalOriginLocked(history gatewayRebindProtectedIntentHistory,
	snapshot appaccess.GatewayRebindRecoverySnapshot, source appaccess.GatewayCurrentAuthorityRef,
) (gatewayCurrentLANHistoricalOrigin, error) {
	invalid := gatewayV2StartupInspectionError(nil)
	if source.Kind == appaccess.GatewayRebindSourceGatewayUpgrade {
		lineage, err := gatewayUpgradeCurrentLineage(history.Predecessor)
		if err != nil || gatewayCurrentAuthority(lineage) != source {
			return gatewayCurrentLANHistoricalOrigin{}, invalid
		}
		return gatewayCurrentLANHistoricalOrigin{profile: GatewayV2ProfileBinding(history.Predecessor.State.Profile)}, nil
	}
	if source.Kind != appaccess.GatewayRebindSourceGatewayRebind {
		return gatewayCurrentLANHistoricalOrigin{}, invalid
	}
	var terminal *gatewayRebindAttemptTerminalView
	for _, selected := range history.Terminals {
		if selected.Receipt.OperationID == source.OperationID {
			value, err := newGatewayRebindAttemptTerminalViewLegacy(selected.Receipt)
			if err != nil || terminal != nil {
				return gatewayCurrentLANHistoricalOrigin{}, invalid
			}
			terminal = &value
		}
	}
	for _, selected := range history.TerminalsV2 {
		if selected.Receipt.OperationID == source.OperationID {
			value, err := newGatewayRebindAttemptTerminalViewV2(selected.Receipt)
			if err != nil || terminal != nil {
				return gatewayCurrentLANHistoricalOrigin{}, invalid
			}
			terminal = &value
		}
	}
	if terminal == nil {
		return gatewayCurrentLANHistoricalOrigin{}, invalid
	}
	var lineage appaccess.GatewayCurrentLineageRef
	var err error
	if terminal.LegacyReceipt != nil {
		lineage, err = gatewayRebindCurrentLineage(*terminal.LegacyReceipt)
	} else {
		lineage, err = gatewayRebindCurrentLineageV2(*terminal.TypedReceipt)
	}
	if err != nil || gatewayCurrentAuthority(lineage) != source || !gatewayRebindAttemptTerminalMatchesLineage(*terminal, lineage) {
		return gatewayCurrentLANHistoricalOrigin{}, invalid
	}
	var entry *appaccess.GatewayRebindHistoryEntry
	for index := range snapshot.History {
		operation, err := gatewayRebindHistoryOperationID(snapshot.History[index])
		if err != nil {
			return gatewayCurrentLANHistoricalOrigin{}, invalid
		}
		if operation == source.OperationID {
			if entry != nil {
				return gatewayCurrentLANHistoricalOrigin{}, invalid
			}
			entry = &snapshot.History[index]
		}
	}
	if entry == nil ||
		(terminal.LegacyReceipt != nil && (entry.Claim.Legacy == nil || entry.Claim.Legacy.State != appaccess.GatewayRebindCommitted)) ||
		(terminal.TypedReceipt != nil && (entry.Claim.V2 == nil || entry.Claim.V2.State != appaccess.GatewayRebindCommitted)) {
		return gatewayCurrentLANHistoricalOrigin{}, invalid
	}
	var committed *appaccess.GatewayRebindEvent
	for index := range entry.Events {
		if entry.Events[index].State == appaccess.GatewayRebindDatabaseCommitted {
			if committed != nil {
				return gatewayCurrentLANHistoricalOrigin{}, invalid
			}
			committed = &entry.Events[index]
		}
	}
	if !gatewayCurrentAuthorityHasDatabaseCommit(snapshot.History, committed, source) {
		return gatewayCurrentLANHistoricalOrigin{}, invalid
	}
	// Open existing protected storage without creating a directory or changing
	// permissions. The mutable routes are not evidence of historical clearance;
	// only the immutable origin and full retained manifest are used for binding.
	directory, present, _, err := inspectStateStoreReadOnly(m.options.DataRoot)
	if err != nil || !present {
		return gatewayCurrentLANHistoricalOrigin{}, invalid
	}
	name, purpose := gatewayCurrentRouteStateName(lineage.ProtectedGeneration, lineage.OperationID)
	store := gatewayCurrentRouteStateStore{directory: directory, dataRoot: m.options.DataRoot,
		generation: lineage.ProtectedGeneration, operationID: lineage.OperationID,
		path: filepath.Join(directory.root, name), purpose: purpose}
	state, err := store.load()
	manifest, manifestErr := appaccess.GatewayRebindTransferManifestDigest(entry.Transfers)
	if err != nil || manifestErr != nil || state.Lineage != lineage || state.TransferManifestDigest != manifest ||
		!gatewayCurrentTransfersMatchState(entry.Transfers, state) {
		return gatewayCurrentLANHistoricalOrigin{}, invalid
	}
	for _, selected := range history.Checkpoints {
		checkpoint := selected.Checkpoint
		if checkpoint.Lineage == lineage && (checkpoint.CurrentState == nil ||
			!sameGatewayCurrentRouteOrigin(state, *checkpoint.CurrentState)) {
			return gatewayCurrentLANHistoricalOrigin{}, invalid
		}
	}
	return gatewayCurrentLANHistoricalOrigin{profile: GatewayV2ProfileBinding(state.Profile),
		state: &state, transfers: entry.Transfers}, nil
}
