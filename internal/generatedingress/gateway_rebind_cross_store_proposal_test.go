package generatedingress

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindProposalRepositoryFake struct {
	snapshot    appaccess.GatewayRebindRecoverySnapshot
	heads       []appaccess.GatewayRebindRuntimeHead
	resolutions map[string]appaccess.GatewayBindingResolution
	fenceErr    error
}

func (f *gatewayRebindProposalRepositoryFake) CheckGatewayRebindFence(context.Context) error {
	return f.fenceErr
}

func (f *gatewayRebindProposalRepositoryFake) GatewayRebindRecoverySnapshot(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
	return f.snapshot, nil
}

func (f *gatewayRebindProposalRepositoryFake) GatewayRebindRuntimeHeads(context.Context) ([]appaccess.GatewayRebindRuntimeHead, error) {
	return append([]appaccess.GatewayRebindRuntimeHead(nil), f.heads...), nil
}

func (f *gatewayRebindProposalRepositoryFake) ResolveGatewayBinding(_ context.Context,
	ref appaccess.GatewayBindingRef,
) (appaccess.GatewayBindingResolution, error) {
	return f.resolutions[ref.AppID], nil
}

func TestInspectGatewayRebindProposalBuildsTypedTransferAwareSource(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	fixture.manager.mu = newContextMutex()
	fixture.manager.options.RebindFenceCheck = func(context.Context) error { return nil }
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	repository := gatewayRebindProposalRepositoryForCurrentFixture(t, fixture)
	input := GatewayRebindProposalInput{
		OperationID: uuid.NewString(), SuccessorProfileRevisionID: uuid.NewString(),
		SuccessorProfileRevisionNumber: fixture.baseline.Profile.RevisionNumber + 1,
		SuccessorProfileOperationID:    uuid.NewString(),
		SuccessorProfile: appaccess.GatewayProfileSpec{
			SelectedIPv4: fixture.baseline.Profile.SelectedIPv4, InterfaceID: fixture.baseline.Profile.InterfaceID,
			PortStart: fixture.baseline.Profile.PortStart, PortEnd: fixture.baseline.Profile.PortEnd,
		},
	}
	inspection, err := fixture.manager.InspectGatewayRebindProposal(context.Background(), &repository, input)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Spec.Predecessor.Lineage != fixture.baseline.Lineage ||
		inspection.Spec.Predecessor.SourceStateVersion != fixture.baseline.Version ||
		inspection.Spec.Predecessor.SourceStateRevision != fixture.baseline.Revision ||
		inspection.Spec.Predecessor.SourceStateDigest != fixture.baseline.Digest ||
		inspection.PredecessorCheckpointDigest != inspection.Spec.Predecessor.PredecessorCheckpointDigest ||
		inspection.ProtectedGeneration != fixture.baseline.Lineage.ProtectedGeneration+1 ||
		inspection.SpecDigest == "" || inspection.SuccessorProfileSpecDigest == "" {
		t.Fatalf("unexpected inspected source: %#v", inspection)
	}
	if len(repository.heads) != len(fixture.baseline.Apps) || len(inspection.Roster) != len(fixture.transfers) {
		t.Fatalf("complete apps=%d heads=%d roster=%d transfers=%d", len(fixture.baseline.Apps), len(repository.heads),
			len(inspection.Roster), len(fixture.transfers))
	}
	for _, entry := range inspection.Roster {
		resolution := repository.resolutions[entry.AppID]
		if entry.PredecessorTransferDigest == nil ||
			*entry.PredecessorTransferDigest != resolution.TransferChainTipDigest ||
			entry.SourceProfileRevisionID != resolution.RawProfile.ID ||
			entry.SourceProfileRevisionID == fixture.baseline.Profile.RevisionID {
			t.Fatalf("roster lost raw/effective split: %#v", entry)
		}
	}

	changed := repository
	changed.heads = append([]appaccess.GatewayRebindRuntimeHead(nil), repository.heads...)
	changed.heads[0].Generation++
	changed.snapshot = repository.snapshot
	first := true
	changing := &gatewayRebindProposalChangingRepository{gatewayRebindProposalRepositoryFake: changed, first: &first,
		initialHeads: repository.heads}
	if _, err := fixture.manager.InspectGatewayRebindProposal(context.Background(), changing, input); err == nil {
		t.Fatal("changed SQL runtime heads produced an inspected proposal")
	}
}

func TestInspectGatewayRebindCurrentSeparatesSelectedAuthorityFromActivePhase(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	fixture.manager.mu = newContextMutex()
	fixture.manager.options.RebindFenceCheck = func(context.Context) error { return nil }
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	repository := gatewayRebindProposalRepositoryForCurrentFixture(t, fixture)
	inspection, err := fixture.manager.InspectGatewayRebindCurrent(context.Background(), &repository)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.SelectedCurrentAuthority != gatewayCurrentAuthority(fixture.baseline.Lineage) ||
		inspection.CurrentStateVersion != fixture.baseline.Version ||
		inspection.CurrentStateRevision != fixture.baseline.Revision ||
		inspection.CurrentStateDigest != fixture.baseline.Digest || !inspection.FenceReleased ||
		inspection.ActiveOperationID != "" || inspection.ActivePhase != "" || len(inspection.Retained) != 1 ||
		inspection.Retained[0].OperationID != fixture.receipt.OperationID ||
		inspection.Retained[0].TerminalReceiptDigest != fixture.receipt.Digest ||
		inspection.Retained[0].Disposition != appaccess.GatewayRebindDispositionCommit ||
		inspection.Retained[0].Resources.FinalContainerID == "" {
		t.Fatalf("unexpected current inspection: %#v", inspection)
	}

	activeOperationID := uuid.NewString()
	repository.snapshot.Active = &appaccess.GatewayRebindHistoryEntry{Claim: appaccess.GatewayRebindClaimRecord{
		SpecVersion: appaccess.GatewayRebindSpecVersionV2,
		V2: &appaccess.GatewayRebindClaimV2{Spec: appaccess.GatewayRebindSpecV2{
			Version: appaccess.GatewayRebindSpecVersionV2, OperationID: activeOperationID,
		}},
	}}
	repository.snapshot.Phase = appaccess.GatewayRebindPrepared
	repository.snapshot.RollbackAllowed = true
	repository.fenceErr = appaccess.ErrGatewayRebindActive
	inspection, err = fixture.manager.InspectGatewayRebindCurrent(context.Background(), &repository)
	if err != nil || inspection.SelectedCurrentAuthority.OperationID != fixture.receipt.OperationID ||
		inspection.ActiveOperationID != activeOperationID || inspection.ActivePhase != appaccess.GatewayRebindPrepared ||
		inspection.FenceReleased {
		t.Fatalf("prepared B obscured committed A: inspection=%#v error=%v", inspection, err)
	}
}

type gatewayRebindProposalChangingRepository struct {
	gatewayRebindProposalRepositoryFake
	first        *bool
	initialHeads []appaccess.GatewayRebindRuntimeHead
}

func (f *gatewayRebindProposalChangingRepository) GatewayRebindRuntimeHeads(context.Context) ([]appaccess.GatewayRebindRuntimeHead, error) {
	if *f.first {
		*f.first = false
		return append([]appaccess.GatewayRebindRuntimeHead(nil), f.initialHeads...), nil
	}
	return append([]appaccess.GatewayRebindRuntimeHead(nil), f.heads...), nil
}

func gatewayRebindProposalRepositoryForCurrentFixture(t *testing.T,
	fixture gatewayCurrentStateFixture,
) gatewayRebindProposalRepositoryFake {
	t.Helper()
	profile := gatewayCurrentFixtureProfile(fixture.baseline.Lineage, fixture.baseline.Profile)
	authority := gatewayCurrentAuthority(fixture.baseline.Lineage)
	currentEvent := appaccess.GatewayRebindEvent{OperationID: fixture.receipt.OperationID,
		Sequence: 4, State: appaccess.GatewayRebindDatabaseCommitted, CreatedAt: time.Unix(4, 0).UTC()}
	history := appaccess.GatewayRebindHistoryEntry{
		Claim: appaccess.GatewayRebindClaimRecord{SpecVersion: 1, Legacy: &appaccess.GatewayRebindClaim{
			Spec: appaccess.GatewayRebindSpec{OperationID: fixture.receipt.OperationID},
		}},
		Events: []appaccess.GatewayRebindEvent{currentEvent},
	}
	result := gatewayRebindProposalRepositoryFake{
		snapshot: appaccess.GatewayRebindRecoverySnapshot{
			History: []appaccess.GatewayRebindHistoryEntry{history}, CurrentProfile: &profile,
			CurrentSource: &authority, CurrentDatabaseCommittedEvent: &currentEvent,
			CurrentTransfers: append([]appaccess.GatewayRebindAllocationTransfer(nil), fixture.transfers...),
		},
		resolutions: make(map[string]appaccess.GatewayBindingResolution),
	}
	appIDs := make([]string, 0, len(fixture.baseline.Apps))
	for appID := range fixture.baseline.Apps {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	for _, appID := range appIDs {
		app := fixture.baseline.Apps[appID]
		head := appaccess.GatewayRebindRuntimeHead{AppID: appID, DeploymentID: uuid.NewString(),
			ReleaseID: uuid.NewString(), Slot: string(app.Route.Slot), Generation: int64(len(result.heads) + 1),
			UpdatedAt: time.Unix(int64(len(result.heads)+1), 0).UTC()}
		result.heads = append(result.heads, head)
		if app.LAN == nil || app.LAN.Transfer == nil {
			continue
		}
		raw, transfer := app.LAN.Raw, *app.LAN.Transfer
		rawProfile := appaccess.GatewayProfileRevision{ID: raw.ProfileRevisionID,
			RevisionNumber: raw.ProfileRevisionNumber, SpecDigest: raw.ProfileSpecDigest,
			Spec: appaccess.GatewayProfileSpec{SelectedIPv4: fixture.predecessor.Profile.SelectedIPv4,
				InterfaceID: fixture.predecessor.Profile.InterfaceID, PortStart: fixture.predecessor.Profile.PortStart,
				PortEnd: fixture.predecessor.Profile.PortEnd}}
		result.resolutions[appID] = appaccess.GatewayBindingResolution{
			RawAllocation: appaccess.Allocation{ID: raw.AllocationID, AppID: appID, Port: raw.Port,
				OwnerOperationID: raw.OwnerOperationID, State: appaccess.AllocationActive},
			RawAccessRevision: appaccess.AppAccessRevision{ID: raw.AccessRevisionID, AppID: appID,
				RevisionNumber: raw.AccessRevisionNumber, SpecDigest: raw.AccessSpecDigest},
			RawGrant: appaccess.AppAccessGrantClaim{AttemptID: raw.GrantAttemptID, StateSequence: 4,
				Spec: appaccess.AppAccessGrantSpec{AppID: appID, AllocationID: raw.AllocationID,
					OwnerOperationID: raw.OwnerOperationID, AccessRevisionID: raw.AccessRevisionID},
				Proof: &appaccess.AppAccessGrantProof{ProtectedStateDigest: raw.GrantRequestDigest}},
			RawProfile: rawProfile, EffectiveProfile: profile, CurrentGatewaySource: authority,
			TransferChain:          []appaccess.GatewayRebindAllocationTransfer{transfer},
			TransferChainTipDigest: transfer.TransferDigest, TerminalReceiptDigest: fixture.receipt.Digest,
		}
	}
	if !sort.SliceIsSorted(result.heads, func(i, j int) bool { return result.heads[i].AppID < result.heads[j].AppID }) {
		t.Fatal("proposal fixture heads are not sorted")
	}
	return result
}

var _ gatewayRebindProposalRepository = (*gatewayRebindProposalRepositoryFake)(nil)
var _ gatewayRebindProposalRepository = (*gatewayRebindProposalChangingRepository)(nil)
var _ gatewayRebindCurrentInspectionRepository = (*gatewayRebindProposalRepositoryFake)(nil)
