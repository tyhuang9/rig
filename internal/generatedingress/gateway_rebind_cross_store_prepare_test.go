package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindAdmissionRepositoryFake struct {
	gatewayRebindProposalRepositoryFake
	claimCalls int
	claim      appaccess.GatewayRebindClaimV2
}

func (f *gatewayRebindAdmissionRepositoryFake) ClaimGatewayRebindV2(_ context.Context,
	proposal appaccess.GatewayRebindPreclaimProposalV2,
) (appaccess.GatewayRebindClaimV2, bool, error) {
	f.claimCalls++
	requestDigest, err := canonicalDigest(struct {
		Spec      appaccess.GatewayRebindSpecV2 `json:"spec"`
		Rebind    appaccess.Approval            `json:"rebindApproval"`
		Configure appaccess.Approval            `json:"configureApproval"`
	}{proposal.Spec, proposal.RebindApproval, proposal.ConfigureApproval})
	if err != nil {
		return appaccess.GatewayRebindClaimV2{}, false, err
	}
	profileRequestDigest, err := canonicalDigest(struct {
		Expected int64                        `json:"expectedRevisionNumber"`
		Spec     appaccess.GatewayProfileSpec `json:"spec"`
		Approval appaccess.Approval           `json:"approval"`
	}{proposal.Spec.Predecessor.Lineage.ProfileRevisionNumber, proposal.Spec.SuccessorProfile,
		proposal.ConfigureApproval})
	if err != nil {
		return appaccess.GatewayRebindClaimV2{}, false, err
	}
	stamp := time.Unix(3, 0).UTC()
	f.claim = appaccess.GatewayRebindClaimV2{
		Spec: proposal.Spec, RequestDigest: requestDigest,
		RebindApproval: proposal.RebindApproval, RebindApprovedAt: time.Unix(1, 0).UTC(),
		ConfigureApproval: proposal.ConfigureApproval, ConfigureApprovedAt: time.Unix(2, 0).UTC(),
		SuccessorProfileRequestDigest: profileRequestDigest,
		State:                         appaccess.GatewayRebindPrepared, StateSequence: 1, CreatedAt: stamp, UpdatedAt: stamp,
	}
	retained := appaccess.GatewayRebindHistoryEntry{Claim: appaccess.GatewayRebindClaimRecord{
		SpecVersion: appaccess.GatewayRebindSpecVersionV2, V2: &f.claim},
		RosterV2:     append([]appaccess.GatewayRebindRosterEntryV2(nil), proposal.Roster...),
		RuntimeHeads: append([]appaccess.GatewayRebindRuntimeHead(nil), proposal.RuntimeHeads...)}
	f.snapshot.History = append(f.snapshot.History, retained)
	f.snapshot.Active = &retained
	f.snapshot.Phase = appaccess.GatewayRebindPrepared
	f.snapshot.RollbackAllowed = true
	return f.claim, true, nil
}

func TestRecoverGatewayRebindPreparedAdmissionTreatsEmptyCensusesCanonically(t *testing.T) {
	for _, test := range []struct {
		name      string
		keepApps  bool
		wantHeads bool
	}{
		{name: "zero-heads-and-zero-lan"},
		{name: "zero-lan-with-runtime-heads", keepApps: true, wantHeads: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayCurrentStateFixture(t)
			fixture.manager.mu = newContextMutex()
			fixture.manager.options.RebindFenceCheck = func(context.Context) error { return nil }
			state := cloneGatewayCurrentRouteState(fixture.baseline)
			state.Apps = make(map[string]gatewayCurrentAppRoute)
			if test.keepApps {
				for appID, app := range fixture.baseline.Apps {
					app.LAN = nil
					state.Apps[appID] = app
				}
			}
			var err error
			state.Digest, err = gatewayCurrentRouteStateDigest(state)
			if err != nil || fixture.store.installBaseline(state) != nil {
				t.Fatalf("install source state: %v", err)
			}
			fixture.baseline = state
			repository := &gatewayRebindAdmissionRepositoryFake{
				gatewayRebindProposalRepositoryFake: gatewayRebindProposalRepositoryForCurrentFixture(t, fixture),
			}
			proposal := GatewayRebindProposalInput{OperationID: uuid.NewString(),
				SuccessorProfileRevisionID:     uuid.NewString(),
				SuccessorProfileRevisionNumber: state.Profile.RevisionNumber + 1,
				SuccessorProfileOperationID:    uuid.NewString(),
				SuccessorProfile: appaccess.GatewayProfileSpec{SelectedIPv4: state.Profile.SelectedIPv4,
					InterfaceID: state.Profile.InterfaceID, PortStart: state.Profile.PortStart, PortEnd: state.Profile.PortEnd}}
			inspection, err := fixture.manager.InspectGatewayRebindProposal(context.Background(), repository, proposal)
			if err != nil || len(inspection.Roster) != 0 || (len(inspection.RuntimeHeads) != 0) != test.wantHeads {
				t.Fatalf("inspect empty censuses: roster=%d heads=%d error=%v",
					len(inspection.Roster), len(inspection.RuntimeHeads), err)
			}
			input := gatewayRebindCommitInput{Inspection: inspection,
				RebindApproval: appaccess.Approval{Action: appaccess.ActionRebindGateway,
					SpecDigest: inspection.SpecDigest, ActorID: uuid.NewString()},
				ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
					SpecDigest: inspection.SuccessorProfileSpecDigest, ActorID: uuid.NewString()}}
			fixture.manager.gatewayRebindV2NetworkObserver = func(_ context.Context,
				claim appaccess.GatewayRebindClaimV2,
			) (gatewayRebindSuccessorNetworkObservation, error) {
				value := fixture.intent.NetworkObservation
				value.OperationID = claim.Spec.OperationID
				value.ClaimRequestDigest = claim.RequestDigest
				value.ProfileSpecDigest = claim.ConfigureApproval.SpecDigest
				return value, nil
			}
			fixture.manager.gatewayRebindClock = func() time.Time { return time.Unix(4, 0).UTC() }
			first, err := fixture.manager.prepareGatewayRebindLocked(context.Background(), repository, input)
			if err != nil {
				t.Fatal(err)
			}
			// A fresh SQL read materializes canonical empty result sets as
			// nonnil empty slices. Protected JSON may retain the same censuses
			// as nil; recovery compares their ordered values, not Go backing
			// representation.
			repository.snapshot.Active.RosterV2 = make([]appaccess.GatewayRebindRosterEntryV2, 0)
			if len(repository.snapshot.Active.RuntimeHeads) == 0 {
				repository.snapshot.Active.RuntimeHeads = make([]appaccess.GatewayRebindRuntimeHead, 0)
			}
			fresh := &Manager{runner: &gatewayCurrentNoCommandRunner{}, store: fixture.manager.store,
				options: fixture.manager.options, mu: newContextMutex()}
			observed := false
			fresh.gatewayRebindV2NetworkObserver = func(context.Context,
				appaccess.GatewayRebindClaimV2,
			) (gatewayRebindSuccessorNetworkObservation, error) {
				observed = true
				return gatewayRebindSuccessorNetworkObservation{}, errors.New("unexpected network observation during exact replay")
			}
			fresh.gatewayRebindClock = func() time.Time { return time.Unix(5, 0).UTC() }
			replayed, err := fresh.recoverGatewayRebindPreparedAdmissionLocked(context.Background(), repository,
				repository.snapshot)
			if err != nil || observed || replayed.Intent.Digest != first.Intent.Digest ||
				replayed.Progress.Digest != first.Progress.Digest {
				t.Fatalf("fresh prepared replay: intent=%s progress=%s observed=%t error=%v",
					replayed.Intent.Digest, replayed.Progress.Digest, observed, err)
			}
		})
	}
}

func TestPrepareGatewayRebindLockedClaimsBeforeProtectedIntent(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	fixture.manager.mu = newContextMutex()
	fixture.manager.options.RebindFenceCheck = func(context.Context) error { return nil }
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	repository := &gatewayRebindAdmissionRepositoryFake{
		gatewayRebindProposalRepositoryFake: gatewayRebindProposalRepositoryForCurrentFixture(t, fixture),
	}
	proposalInput := GatewayRebindProposalInput{
		OperationID: uuid.NewString(), SuccessorProfileRevisionID: uuid.NewString(),
		SuccessorProfileRevisionNumber: fixture.baseline.Profile.RevisionNumber + 1,
		SuccessorProfileOperationID:    uuid.NewString(), SuccessorProfile: appaccess.GatewayProfileSpec{
			SelectedIPv4: fixture.baseline.Profile.SelectedIPv4, InterfaceID: fixture.baseline.Profile.InterfaceID,
			PortStart: fixture.baseline.Profile.PortStart, PortEnd: fixture.baseline.Profile.PortEnd,
		},
	}
	inspection, err := fixture.manager.InspectGatewayRebindProposal(context.Background(), repository, proposalInput)
	if err != nil {
		t.Fatal(err)
	}
	input := gatewayRebindCommitInput{Inspection: inspection,
		RebindApproval: appaccess.Approval{Action: appaccess.ActionRebindGateway,
			SpecDigest: inspection.SpecDigest, ActorID: uuid.NewString()},
		ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
			SpecDigest: inspection.SuccessorProfileSpecDigest, ActorID: uuid.NewString()},
	}
	fixture.manager.gatewayRebindV2NetworkObserver = func(_ context.Context,
		claim appaccess.GatewayRebindClaimV2,
	) (gatewayRebindSuccessorNetworkObservation, error) {
		value := fixture.intent.NetworkObservation
		value.OperationID = claim.Spec.OperationID
		value.ClaimRequestDigest = claim.RequestDigest
		value.ProfileSpecDigest = claim.ConfigureApproval.SpecDigest
		return value, nil
	}
	fixture.manager.gatewayRebindClock = func() time.Time { return time.Unix(4, 0).UTC() }
	release, err := fixture.manager.lockGatewayRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	attempt, prepareErr := fixture.manager.prepareGatewayRebindLocked(context.Background(), repository, input)
	if releaseErr := release(); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	if repository.claimCalls != 1 || attempt.Claim.Spec.SuccessorProtectedGeneration != inspection.ProtectedGeneration ||
		attempt.Checkpoint.Digest != inspection.PredecessorCheckpointDigest ||
		attempt.Intent.Predecessor != inspection.Spec.Predecessor || attempt.Progress.Sequence != 1 {
		t.Fatalf("prepared attempt mismatch: %#v", attempt)
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.IntentsV2) != 1 || len(history.Progress) == 0 ||
		history.Progress[len(history.Progress)-1].Record.Digest != attempt.Progress.Digest {
		t.Fatalf("prepared history mismatch: intents=%d progress=%d error=%v",
			len(history.IntentsV2), len(history.Progress), err)
	}
}

func TestRecoverGatewayRebindPreparedAdmissionUsesRetainedClaimGenerationAfterPreCheckpointCrash(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	fixture.manager.mu = newContextMutex()
	fixture.manager.options.RebindFenceCheck = func(context.Context) error { return nil }
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	repository := &gatewayRebindAdmissionRepositoryFake{
		gatewayRebindProposalRepositoryFake: gatewayRebindProposalRepositoryForCurrentFixture(t, fixture),
	}
	proposalInput := GatewayRebindProposalInput{OperationID: uuid.NewString(),
		SuccessorProfileRevisionID:     uuid.NewString(),
		SuccessorProfileRevisionNumber: fixture.baseline.Profile.RevisionNumber + 1,
		SuccessorProfileOperationID:    uuid.NewString(), SuccessorProfile: appaccess.GatewayProfileSpec{
			SelectedIPv4: fixture.baseline.Profile.SelectedIPv4, InterfaceID: fixture.baseline.Profile.InterfaceID,
			PortStart: fixture.baseline.Profile.PortStart, PortEnd: fixture.baseline.Profile.PortEnd,
		}}
	inspection, err := fixture.manager.InspectGatewayRebindProposal(context.Background(), repository, proposalInput)
	if err != nil {
		t.Fatal(err)
	}
	input := gatewayRebindCommitInput{Inspection: inspection,
		RebindApproval: appaccess.Approval{Action: appaccess.ActionRebindGateway,
			SpecDigest: inspection.SpecDigest, ActorID: uuid.NewString()},
		ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
			SpecDigest: inspection.SuccessorProfileSpecDigest, ActorID: uuid.NewString()}}
	crash := errors.New("injected crash after SQL prepared commit")
	fixture.manager.gatewayRebindAfterClaim = func(context.Context, appaccess.GatewayRebindClaimV2) error { return crash }
	fixture.manager.gatewayRebindClock = func() time.Time { return time.Unix(4, 0).UTC() }
	release, err := fixture.manager.lockGatewayRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, prepareErr := fixture.manager.prepareGatewayRebindLocked(context.Background(), repository, input)
	if releaseErr := release(); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if prepareErr == nil || repository.claimCalls != 1 || repository.claim.State != appaccess.GatewayRebindPrepared {
		t.Fatalf("pre-checkpoint crash did not retain prepared claim: calls=%d claim=%#v error=%v",
			repository.claimCalls, repository.claim, prepareErr)
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.IntentsV2) != 0 {
		t.Fatalf("pre-checkpoint crash history mismatch: checkpoints=%d intents=%d error=%v",
			len(history.Checkpoints), len(history.IntentsV2), err)
	}
	for _, retained := range history.Checkpoints {
		if retained.Checkpoint.OperationID == inspection.Spec.OperationID {
			t.Fatal("protected checkpoint was installed before the injected crash boundary")
		}
	}
	fixture.manager.gatewayRebindAfterClaim = nil
	fixture.manager.gatewayRebindClock = func() time.Time { return time.Unix(5, 0).UTC() }
	fixture.manager.gatewayRebindV2NetworkObserver = func(_ context.Context,
		claim appaccess.GatewayRebindClaimV2,
	) (gatewayRebindSuccessorNetworkObservation, error) {
		value := fixture.intent.NetworkObservation
		value.OperationID = claim.Spec.OperationID
		value.ClaimRequestDigest = claim.RequestDigest
		value.ProfileSpecDigest = claim.ConfigureApproval.SpecDigest
		return value, nil
	}
	release, err = fixture.manager.lockGatewayRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	recovered, recoverErr := fixture.manager.recoverGatewayRebindPreparedAdmissionLocked(context.Background(),
		repository, repository.snapshot)
	if releaseErr := release(); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if recoverErr != nil || recovered.Checkpoint.Generation != inspection.ProtectedGeneration ||
		recovered.Checkpoint.Digest != inspection.PredecessorCheckpointDigest ||
		recovered.Intent.Claim.RequestDigest != repository.claim.RequestDigest {
		t.Fatalf("prepared admission recovery mismatch: attempt=%#v error=%v", recovered, recoverErr)
	}
	history, err = fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.IntentsV2) != 1 ||
		!reflect.DeepEqual(history.IntentsV2[0].Intent.Predecessor, inspection.Spec.Predecessor) {
		t.Fatalf("recovered protected history mismatch: intents=%d error=%v", len(history.IntentsV2), err)
	}
}
