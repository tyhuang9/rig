package generatedingress

import (
	"errors"
	"reflect"
	"sort"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
)

func gatewayRebindV2CheckpointApps(checkpoint gatewayRebindPredecessorCheckpoint) (map[string]gatewayCurrentAppRoute, error) {
	result := make(map[string]gatewayCurrentAppRoute)
	switch {
	case checkpoint.UpgradeState != nil && checkpoint.CurrentState == nil:
		for appID, app := range checkpoint.UpgradeState.Apps {
			current := gatewayCurrentAppRoute{Route: app.Route}
			current.Route.Endpoints = append([]generatedruntime.RouteEndpoint(nil), app.Route.Endpoints...)
			if app.LAN != nil {
				raw := *app.LAN
				current.LAN = &gatewayCurrentLANBinding{Raw: raw}
			}
			result[appID] = current
		}
	case checkpoint.UpgradeState == nil && checkpoint.CurrentState != nil:
		for appID, app := range checkpoint.CurrentState.Apps {
			current := app
			current.Route.Endpoints = append([]generatedruntime.RouteEndpoint(nil), app.Route.Endpoints...)
			if app.LAN != nil {
				binding := *app.LAN
				if app.LAN.Transfer != nil {
					transfer := *app.LAN.Transfer
					binding.Transfer = &transfer
				}
				current.LAN = &binding
			}
			result[appID] = current
		}
	default:
		return nil, errors.New("invalid typed rebind checkpoint app source")
	}
	return result, nil
}

func gatewayRebindV2SourceBindingDigest(appID string, app gatewayCurrentAppRoute,
	entry appaccess.GatewayRebindRosterEntryV2,
) (string, error) {
	return canonicalDigest(struct {
		Version int                                  `json:"version"`
		Purpose string                               `json:"purpose"`
		AppID   string                               `json:"appId"`
		Route   routeRecord                          `json:"route"`
		Raw     gatewayV2LANBinding                  `json:"raw"`
		Roster  appaccess.GatewayRebindRosterEntryV2 `json:"roster"`
	}{1, "hostd/generated-ingress/rebind/source-binding/v2", appID, app.Route, app.LAN.Raw, entry})
}

func newGatewayRebindTransfersV2(intent gatewayRebindProtectedIntentV2,
	receipt gatewayRebindTerminalReceiptV2, checkpoint gatewayRebindPredecessorCheckpoint,
) ([]appaccess.GatewayRebindAllocationTransfer, error) {
	invalid := errors.New("invalid typed rebind allocation transfers")
	// The complete progress chain is validated by the protected scanner. This
	// constructor repeats the immutable receipt/intent/checkpoint bindings.
	if !validGatewayRebindProtectedIntentV2(intent) || !validGatewayRebindTerminalReceiptV2(receipt) ||
		receipt.Disposition != appaccess.GatewayRebindDispositionCommit || receipt.OperationID != intent.OperationID ||
		receipt.ProtectedIntentDigest != intent.Digest || receipt.Predecessor != checkpoint.sourceRef() ||
		receipt.RosterDigest != intent.Claim.Spec.RosterDigest ||
		!sameGatewayRebindDigestList(receipt.RosterEntryDigests, intent.RosterEntryDigests) {
		return nil, invalid
	}
	apps, err := gatewayRebindV2CheckpointApps(checkpoint)
	if err != nil {
		return nil, invalid
	}
	result := make([]appaccess.GatewayRebindAllocationTransfer, len(intent.Roster))
	for index, entry := range intent.Roster {
		app, ok := apps[entry.AppID]
		if !ok || app.LAN == nil || app.LAN.Raw.AllocationID != entry.AllocationID ||
			app.LAN.Raw.GrantAttemptID != entry.GrantAttemptID {
			return nil, invalid
		}
		sourceDigest, digestErr := gatewayRebindV2SourceBindingDigest(entry.AppID, app, entry)
		if digestErr != nil {
			return nil, invalid
		}
		transfer := appaccess.GatewayRebindAllocationTransfer{
			Version: appaccess.GatewayRebindTransferVersionV1, OperationID: intent.OperationID,
			Ordinal: entry.Ordinal, AppID: entry.AppID, AllocationID: entry.AllocationID,
			GrantAttemptID: entry.GrantAttemptID, SourceBindingDigest: sourceDigest,
			RosterEntryDigest: entry.EntryDigest, SourceProfileRevisionID: entry.SourceProfileRevisionID,
			SourceProfileRevisionNumber:    entry.SourceProfileRevisionNumber,
			SourceProfileSpecDigest:        entry.SourceProfileSpecDigest,
			SuccessorProfileRevisionID:     receipt.SuccessorProfile.RevisionID,
			SuccessorProfileRevisionNumber: receipt.SuccessorProfile.RevisionNumber,
			SuccessorProfileSpecDigest:     receipt.SuccessorProfile.SpecDigest,
			TerminalReceiptDigest:          receipt.Digest,
		}
		if entry.PredecessorTransferDigest != nil {
			value := *entry.PredecessorTransferDigest
			transfer.PredecessorTransferDigest = &value
		}
		transfer.TransferDigest, err = appaccess.GatewayRebindAllocationTransferDigest(transfer)
		if err != nil {
			return nil, invalid
		}
		result[index] = transfer
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Ordinal < result[j].Ordinal })
	for index := range result {
		if result[index].Ordinal != int64(index+1) {
			return nil, invalid
		}
	}
	return result, nil
}

func newGatewayCurrentRouteBaselineFromV2Terminal(intent gatewayRebindProtectedIntentV2,
	receipt gatewayRebindTerminalReceiptV2, checkpoint gatewayRebindPredecessorCheckpoint,
	transfers []appaccess.GatewayRebindAllocationTransfer,
) (gatewayCurrentRouteState, error) {
	invalid := errors.New("invalid typed rebind current baseline")
	if !validGatewayRebindProtectedIntentV2(intent) || !validGatewayRebindTerminalReceiptV2(receipt) ||
		receipt.Disposition != appaccess.GatewayRebindDispositionCommit || receipt.ProtectedIntentDigest != intent.Digest ||
		receipt.Predecessor != checkpoint.sourceRef() || len(transfers) != len(intent.Roster) {
		return gatewayCurrentRouteState{}, invalid
	}
	lineage, err := gatewayRebindCurrentLineageV2(receipt)
	if err != nil {
		return gatewayCurrentRouteState{}, invalid
	}
	manifest, err := appaccess.GatewayRebindTransferManifestDigest(transfers)
	if err != nil {
		return gatewayCurrentRouteState{}, invalid
	}
	apps, err := gatewayRebindV2CheckpointApps(checkpoint)
	if err != nil {
		return gatewayCurrentRouteState{}, invalid
	}
	byApp := make(map[string]appaccess.GatewayRebindAllocationTransfer, len(transfers))
	for _, transfer := range transfers {
		digest, digestErr := appaccess.GatewayRebindAllocationTransferDigest(transfer)
		if digestErr != nil || digest != transfer.TransferDigest || transfer.OperationID != receipt.OperationID ||
			transfer.TerminalReceiptDigest != receipt.Digest {
			return gatewayCurrentRouteState{}, invalid
		}
		if _, duplicate := byApp[transfer.AppID]; duplicate {
			return gatewayCurrentRouteState{}, invalid
		}
		byApp[transfer.AppID] = transfer
	}
	for appID, app := range apps {
		if app.LAN == nil {
			continue
		}
		transfer, ok := byApp[appID]
		if !ok || transfer.AllocationID != app.LAN.Raw.AllocationID || transfer.GrantAttemptID != app.LAN.Raw.GrantAttemptID {
			return gatewayCurrentRouteState{}, invalid
		}
		copy := transfer
		app.LAN = &gatewayCurrentLANBinding{Raw: app.LAN.Raw, Transfer: &copy}
		apps[appID] = app
	}
	if len(byApp) != len(transfers) {
		return gatewayCurrentRouteState{}, invalid
	}
	profile := receipt.SuccessorProfile
	identity := receipt.SuccessorIdentity
	state := gatewayCurrentRouteState{
		Version: gatewayCurrentRouteStateVersion, Revision: 1, Lineage: lineage,
		Profile: gatewayProfileBinding{RevisionID: profile.RevisionID, RevisionNumber: profile.RevisionNumber,
			SpecDigest: profile.SpecDigest, SelectedIPv4: profile.SelectedIPv4, InterfaceID: profile.InterfaceID,
			PortStart: profile.PortStart, PortEnd: profile.PortEnd},
		Identity: gatewayCurrentIdentity{Kind: gatewayCurrentIdentityRebind, Rebind: &identity, RebindProfile: &profile},
		Network:  gatewayV2NetworkPlan(intent.Network), TransferManifestDigest: manifest, Apps: apps,
	}
	state.Digest, err = gatewayCurrentRouteStateDigest(state)
	if err != nil || !validGatewayCurrentRouteState(state) || !gatewayCurrentTransfersMatchState(transfers, state) {
		return gatewayCurrentRouteState{}, invalid
	}
	// Every transfer must be active at the create-only baseline. Later ordinary
	// disables may retire it while SQL retains the immutable manifest.
	active := 0
	for _, app := range state.Apps {
		if app.LAN != nil && app.LAN.Transfer != nil {
			active++
		}
	}
	if active != len(transfers) || !reflect.DeepEqual(state.Lineage, lineage) {
		return gatewayCurrentRouteState{}, invalid
	}
	return state, nil
}
