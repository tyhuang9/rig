package generatedingress

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sort"

	"github.com/hostd/hostd/internal/appaccess"
)

// GatewayRebindProposalInput contains only the intended successor identity.
// The protected predecessor, checkpoint and complete roster are observed by
// InspectGatewayRebindProposal. Administrator approvals are deliberately
// supplied only after that exact result has been reviewed.
type GatewayRebindProposalInput struct {
	OperationID                    string
	SuccessorProfileRevisionID     string
	SuccessorProfileRevisionNumber int64
	SuccessorProfileOperationID    string
	SuccessorProfile               appaccess.GatewayProfileSpec
}

// GatewayRebindProposalInspection is advisory, immutable input to the private
// commit path. Commit reconstructs this value under the effects lease and both
// gateway locks before it creates a SQL claim.
type GatewayRebindProposalInspection struct {
	Spec                        appaccess.GatewayRebindSpecV2
	Roster                      []appaccess.GatewayRebindRosterEntryV2
	SpecDigest                  string
	SuccessorProfileSpecDigest  string
	ProtectedGeneration         uint64
	PredecessorCheckpointDigest string
	SourceStateVersion          uint64
	SourceStateRevision         uint64
	SourceStateDigest           string
}

type gatewayRebindProposalRepository interface {
	GatewayRebindRecoverySnapshot(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error)
	GatewayRebindRuntimeHeads(context.Context) ([]appaccess.GatewayRebindRuntimeHead, error)
	ResolveGatewayBinding(context.Context, appaccess.GatewayBindingRef) (appaccess.GatewayBindingResolution, error)
}

type gatewayRebindProposalRoute struct {
	route routeRecord
	lan   *gatewayCurrentLANBinding
	proof GatewayV2LANEffectiveBindingProof
}

// InspectGatewayRebindProposal builds a complete V2 proposal from one stable
// SQL/protected observation. It does not acquire the deployment-effects lease,
// install the checkpoint, create a claim, or perform a Docker effect. The
// private commit path repeats the same observation while holding its stronger
// lock chain.
func (m *Manager) InspectGatewayRebindProposal(ctx context.Context, repository gatewayRebindProposalRepository,
	input GatewayRebindProposalInput,
) (result GatewayRebindProposalInspection, resultErr error) {
	if m == nil || ctx == nil || repository == nil || !validCanonicalUUID(input.OperationID) ||
		!validCanonicalUUID(input.SuccessorProfileRevisionID) ||
		!validCanonicalUUID(input.SuccessorProfileOperationID) ||
		input.SuccessorProfileOperationID == input.OperationID {
		return GatewayRebindProposalInspection{}, &Error{Code: DiagnosticValidationFailed}
	}
	if _, err := appaccess.GatewayProfileSpecDigest(input.SuccessorProfile); err != nil {
		return GatewayRebindProposalInspection{}, &Error{Code: DiagnosticValidationFailed}
	}
	release, err := m.lockGateway(ctx)
	if err != nil {
		return GatewayRebindProposalInspection{}, err
	}
	defer releaseGatewayLock(release, &resultErr)
	result, err = m.inspectGatewayRebindProposalLocked(ctx, repository, input)
	if err != nil {
		return GatewayRebindProposalInspection{}, err
	}
	return result, nil
}

func (m *Manager) inspectGatewayRebindProposalLocked(ctx context.Context, repository gatewayRebindProposalRepository,
	input GatewayRebindProposalInput,
) (GatewayRebindProposalInspection, error) {
	if ctx.Err() != nil {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	firstSnapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || firstSnapshot.Active != nil || firstSnapshot.Phase != "" ||
		firstSnapshot.DatabaseCommittedEvent != nil || firstSnapshot.DatabaseCommitObserved || firstSnapshot.RollbackAllowed {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	selection, err := m.selectGatewayCurrentLocked(ctx, firstSnapshot)
	if err != nil {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	generation, err := gatewayRebindNextGeneration(history, firstSnapshot, input.OperationID)
	if err != nil || generation <= selection.Lineage.ProtectedGeneration {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	checkpoint, err := gatewayRebindCheckpointForSelection(generation, input.OperationID, selection)
	if err != nil {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	routes, err := gatewayRebindProposalRoutes(selection)
	if err != nil {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	heads, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	roster, err := gatewayRebindProposalRoster(ctx, repository, input, routes, heads)
	if err != nil {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	rosterDigest, err := appaccess.GatewayRebindRosterV2Digest(roster)
	if err != nil {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	spec := appaccess.GatewayRebindSpecV2{
		Version: appaccess.GatewayRebindSpecVersionV2, OperationID: input.OperationID,
		Predecessor: checkpoint.sourceRef(), SuccessorProtectedGeneration: generation,
		SuccessorProfileRevisionID:     input.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: input.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    input.SuccessorProfileOperationID, SuccessorProfile: input.SuccessorProfile,
		RosterVersion: appaccess.GatewayRebindRosterVersionV2, RosterDigest: rosterDigest, RosterCount: int64(len(roster)),
	}
	specDigest, err := appaccess.GatewayRebindSpecV2Digest(spec)
	if err != nil {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	profileDigest, err := appaccess.GatewayProfileSpecDigest(input.SuccessorProfile)
	if err != nil {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	result := GatewayRebindProposalInspection{
		Spec: spec, Roster: append([]appaccess.GatewayRebindRosterEntryV2(nil), roster...),
		SpecDigest: specDigest, SuccessorProfileSpecDigest: profileDigest,
		ProtectedGeneration: generation, PredecessorCheckpointDigest: checkpoint.Digest,
		SourceStateVersion: checkpoint.SourceStateVersion, SourceStateRevision: checkpoint.SourceStateRevision,
		SourceStateDigest: checkpoint.SourceStateDigest,
	}
	if ctx.Err() != nil {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	confirmedSnapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(firstSnapshot, confirmedSnapshot) {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	confirmedHeads, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || !reflect.DeepEqual(heads, confirmedHeads) {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	confirmed, err := m.selectGatewayCurrentLocked(ctx, confirmedSnapshot)
	if err != nil || !sameGatewayCurrentSelection(selection, confirmed) {
		return GatewayRebindProposalInspection{}, gatewayRebindProposalError(ctx)
	}
	return result, nil
}

func gatewayRebindNextGeneration(history gatewayRebindProtectedIntentHistory,
	snapshot appaccess.GatewayRebindRecoverySnapshot, operationID string,
) (uint64, error) {
	latest := history.Predecessor.Generation
	seen := map[string]struct{}{history.Predecessor.operationID: {}}
	for _, value := range history.Intents {
		if value.Generation > latest {
			latest = value.Generation
		}
		seen[value.Intent.OperationID] = struct{}{}
	}
	for _, value := range history.Checkpoints {
		if value.Generation > latest {
			latest = value.Generation
		}
		seen[value.Checkpoint.OperationID] = struct{}{}
	}
	// A prepared SQL claim is durable before its protected checkpoint is
	// installed. Retain that generation in the allocator even after rollback or
	// an interrupted admission so no later attempt can reuse its identity.
	for _, entry := range snapshot.History {
		switch {
		case entry.Claim.SpecVersion == appaccess.GatewayRebindSpecVersionV2 && entry.Claim.V2 != nil:
			claim := entry.Claim.V2
			seen[claim.Spec.OperationID] = struct{}{}
			if claim.Spec.SuccessorProtectedGeneration > latest {
				latest = claim.Spec.SuccessorProtectedGeneration
			}
		case entry.Claim.Legacy != nil:
			seen[entry.Claim.Legacy.Spec.OperationID] = struct{}{}
		}
	}
	if _, duplicate := seen[operationID]; duplicate || latest >= uint64(math.MaxInt64) {
		return 0, errors.New("generated ingress rebind generation is unavailable")
	}
	return latest + 1, nil
}

func gatewayRebindCheckpointForSelection(generation uint64, operationID string,
	selection gatewayCurrentSelection,
) (gatewayRebindPredecessorCheckpoint, error) {
	switch selection.Kind {
	case gatewayCurrentSelectionUpgrade:
		if selection.Upgrade == nil {
			return gatewayRebindPredecessorCheckpoint{}, errors.New("generated ingress upgrade predecessor is missing")
		}
		state := cloneGatewayV2RouteState(selection.Upgrade.State)
		return newGatewayRebindPredecessorCheckpoint(generation, operationID, selection.Lineage, &state, nil)
	case gatewayCurrentSelectionRebind:
		if selection.State == nil {
			return gatewayRebindPredecessorCheckpoint{}, errors.New("generated ingress rebind predecessor is missing")
		}
		state := cloneGatewayCurrentRouteState(*selection.State)
		return newGatewayRebindPredecessorCheckpoint(generation, operationID, selection.Lineage, nil, &state)
	default:
		return gatewayRebindPredecessorCheckpoint{}, errors.New("generated ingress predecessor kind is invalid")
	}
}

func gatewayRebindProposalRoutes(selection gatewayCurrentSelection) (map[string]gatewayRebindProposalRoute, error) {
	result := make(map[string]gatewayRebindProposalRoute)
	switch selection.Kind {
	case gatewayCurrentSelectionUpgrade:
		if selection.Upgrade == nil || !validGatewayV2RouteState(selection.Upgrade.State) ||
			selection.Upgrade.State.Pending != nil || selection.Upgrade.State.LANRecovery != nil {
			return nil, errors.New("generated ingress upgrade routes are unavailable")
		}
		proof, err := gatewayUpgradeEffectiveProof(*selection.Upgrade)
		if err != nil {
			return nil, err
		}
		for appID, app := range selection.Upgrade.State.Apps {
			value := gatewayRebindProposalRoute{route: app.Route, proof: proof}
			value.route.Endpoints = append(value.route.Endpoints[:0:0], app.Route.Endpoints...)
			if app.LAN != nil {
				raw := *app.LAN
				value.lan = &gatewayCurrentLANBinding{Raw: raw}
			}
			result[appID] = value
		}
	case gatewayCurrentSelectionRebind:
		if selection.State == nil || !validGatewayCurrentRouteState(*selection.State) ||
			selection.State.Pending != nil || selection.State.LANRecovery != nil {
			return nil, errors.New("generated ingress current routes are unavailable")
		}
		for appID, app := range selection.State.Apps {
			value := gatewayRebindProposalRoute{route: app.Route}
			value.route.Endpoints = append(value.route.Endpoints[:0:0], app.Route.Endpoints...)
			if app.LAN != nil {
				binding := *app.LAN
				if app.LAN.Transfer != nil {
					transfer := *app.LAN.Transfer
					binding.Transfer = &transfer
				}
				value.lan = &binding
				proof, err := gatewayCurrentEffectiveProof(*selection.State, &binding)
				if err != nil {
					return nil, err
				}
				value.proof = proof
			}
			result[appID] = value
		}
	default:
		return nil, errors.New("generated ingress current routes are unavailable")
	}
	return result, nil
}

func gatewayRebindProposalRoster(ctx context.Context, repository gatewayRebindProposalRepository,
	input GatewayRebindProposalInput, routes map[string]gatewayRebindProposalRoute,
	heads []appaccess.GatewayRebindRuntimeHead,
) ([]appaccess.GatewayRebindRosterEntryV2, error) {
	if len(routes) != len(heads) {
		return nil, errors.New("generated ingress protected routes and SQL runtime heads are incomplete")
	}
	headByApp := make(map[string]appaccess.GatewayRebindRuntimeHead, len(heads))
	for index, head := range heads {
		if index > 0 && heads[index-1].AppID >= head.AppID {
			return nil, errors.New("generated ingress SQL runtime heads are not canonical")
		}
		if route, ok := routes[head.AppID]; !ok || string(route.route.Slot) != head.Slot || head.Generation <= 0 {
			return nil, errors.New("generated ingress protected route disagrees with SQL runtime head")
		}
		headByApp[head.AppID] = head
	}
	appIDs := make([]string, 0, len(routes))
	for appID := range routes {
		if _, ok := headByApp[appID]; !ok {
			return nil, errors.New("generated ingress SQL runtime head is missing")
		}
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	roster := make([]appaccess.GatewayRebindRosterEntryV2, 0)
	for _, appID := range appIDs {
		route := routes[appID]
		if route.lan == nil {
			continue
		}
		raw := route.lan.Raw
		resolution, err := repository.ResolveGatewayBinding(ctx, appaccess.GatewayBindingRef{
			AppID: appID, AllocationID: raw.AllocationID, AccessRevisionID: raw.AccessRevisionID,
			GrantAttemptID: raw.GrantAttemptID,
		})
		if err != nil || !route.proof.MatchesResolution(resolution) ||
			!gatewayRebindRawBindingMatchesResolution(appID, raw, resolution) {
			return nil, errors.New("generated ingress SQL binding resolution disagrees with protected route")
		}
		head := headByApp[appID]
		var predecessorTransfer *string
		if resolution.TransferChainTipDigest != "" {
			value := resolution.TransferChainTipDigest
			predecessorTransfer = &value
		}
		entry := appaccess.GatewayRebindRosterEntryV2{
			Version: appaccess.GatewayRebindRosterVersionV2, OperationID: input.OperationID,
			Ordinal: int64(len(roster) + 1), AppID: appID, AllocationID: raw.AllocationID, Port: raw.Port,
			AllocationOwnerOperationID: resolution.RawAllocation.OwnerOperationID,
			AllocationState:            resolution.RawAllocation.State, AccessRevisionID: raw.AccessRevisionID,
			AccessRevisionNumber: raw.AccessRevisionNumber, AccessSpecDigest: raw.AccessSpecDigest,
			GrantAttemptID: raw.GrantAttemptID, GrantStateSequence: resolution.RawGrant.StateSequence,
			GrantProtectedStateDigest:   resolution.RawGrant.Proof.ProtectedStateDigest,
			SourceProfileRevisionID:     resolution.RawProfile.ID,
			SourceProfileRevisionNumber: resolution.RawProfile.RevisionNumber,
			SourceProfileSpecDigest:     resolution.RawProfile.SpecDigest,
			PredecessorTransferDigest:   predecessorTransfer,
			ServingDeploymentID:         head.DeploymentID, ServingReleaseID: head.ReleaseID,
			ServingSlot: head.Slot, RouteGeneration: head.Generation,
		}
		digest, err := appaccess.GatewayRebindRosterEntryV2Digest(entry)
		if err != nil || raw.Port < input.SuccessorProfile.PortStart || raw.Port > input.SuccessorProfile.PortEnd {
			return nil, errors.New("generated ingress rebind roster entry is invalid")
		}
		entry.EntryDigest = digest
		roster = append(roster, entry)
	}
	return roster, nil
}

func gatewayRebindRawBindingMatchesResolution(appID string, raw gatewayV2LANBinding,
	resolution appaccess.GatewayBindingResolution,
) bool {
	allocation, access, grant, profile := resolution.RawAllocation, resolution.RawAccessRevision,
		resolution.RawGrant, resolution.RawProfile
	return grant.Proof != nil && allocation.ID == raw.AllocationID && allocation.AppID == appID &&
		allocation.Port == raw.Port && allocation.OwnerOperationID == raw.OwnerOperationID &&
		allocation.State == appaccess.AllocationActive && allocation.ReleasedAt == nil &&
		access.ID == raw.AccessRevisionID && access.AppID == appID &&
		access.RevisionNumber == raw.AccessRevisionNumber && access.SpecDigest == raw.AccessSpecDigest &&
		grant.AttemptID == raw.GrantAttemptID && grant.Spec.AppID == appID &&
		grant.Spec.AllocationID == raw.AllocationID && grant.Spec.AccessRevisionID == raw.AccessRevisionID &&
		grant.Proof.ProtectedStateDigest != "" && profile.ID == raw.ProfileRevisionID &&
		profile.RevisionNumber == raw.ProfileRevisionNumber && profile.SpecDigest == raw.ProfileSpecDigest
}

func sameGatewayCurrentSelection(left, right gatewayCurrentSelection) bool {
	return left.Kind == right.Kind && left.Lineage == right.Lineage &&
		reflect.DeepEqual(left.Upgrade, right.Upgrade) && reflect.DeepEqual(left.Receipt, right.Receipt) &&
		reflect.DeepEqual(left.State, right.State)
}

func gatewayRebindProposalError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}
