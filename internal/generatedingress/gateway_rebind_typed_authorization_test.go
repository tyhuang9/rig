package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindTypedAuthorizationStageDriver struct {
	*gatewayRebindTypedStageDriverFake
	effects int
}

type gatewayRebindTypedAuthorizationRepository struct {
	*appaccess.Repository
	claimCalls    int
	claimErr      error
	recoveryCalls int
	headCalls     int
	resolveCalls  int
}

func (r *gatewayRebindTypedAuthorizationRepository) GatewayRebindRecoverySnapshot(ctx context.Context) (
	appaccess.GatewayRebindRecoverySnapshot, error,
) {
	r.recoveryCalls++
	return r.Repository.GatewayRebindRecoverySnapshot(ctx)
}

func (r *gatewayRebindTypedAuthorizationRepository) GatewayRebindRuntimeHeads(ctx context.Context) (
	[]appaccess.GatewayRebindRuntimeHead, error,
) {
	r.headCalls++
	return r.Repository.GatewayRebindRuntimeHeads(ctx)
}

func (r *gatewayRebindTypedAuthorizationRepository) ResolveGatewayBinding(ctx context.Context,
	reference appaccess.GatewayBindingRef,
) (appaccess.GatewayBindingResolution, error) {
	r.resolveCalls++
	return r.Repository.ResolveGatewayBinding(ctx, reference)
}

func (r *gatewayRebindTypedAuthorizationRepository) ClaimGatewayRebindV2(ctx context.Context,
	proposal appaccess.GatewayRebindPreclaimProposalV2,
) (appaccess.GatewayRebindClaimV2, bool, error) {
	r.claimCalls++
	claim, created, err := r.Repository.ClaimGatewayRebindV2(ctx, proposal)
	r.claimErr = err
	return claim, created, err
}

func TestGatewayRebindTypedDriverRechecksDistinctLANApproverBeforePhysicalEffect(t *testing.T) {
	for _, test := range []struct {
		name        string
		demote      bool
		wantEffects int
	}{
		{name: "unchanged LAN approver reaches first authorized effect", wantEffects: 1},
		{name: "demoted LAN approver is refused before first effect", demote: true, wantEffects: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			originalEffects, originalGateway := gatewayRebindAcquireDeploymentEffects, managerAcquireGatewayOSLock
			effectsCalls, gatewayCalls := 0, 0
			var effectsErr, gatewayErr error
			gatewayRebindAcquireDeploymentEffects = func(ctx context.Context, directory string) (func() error, error) {
				effectsCalls++
				release, err := originalEffects(ctx, directory)
				effectsErr = err
				return release, err
			}
			managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
				gatewayCalls++
				release, err := originalGateway(ctx, store)
				gatewayErr = err
				return release, err
			}
			t.Cleanup(func() {
				gatewayRebindAcquireDeploymentEffects, managerAcquireGatewayOSLock = originalEffects, originalGateway
			})
			lanApproverID := uuid.NewString()
			fixture := newGatewayRebindPredecessorFixtureWithLANApprover(t, false, lanApproverID)
			repository := &gatewayRebindTypedAuthorizationRepository{Repository: fixture.repository}
			fixture.manager.options.RebindFenceCheck = fixture.repository.CheckGatewayRebindFence
			fixture.manager.options.RebindCurrentStateRepository = repository
			fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
			fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
			input := completeGatewayRebindCoordinatorFixture(t, fixture)
			fixture.manager.gatewayRebindV2NetworkObserver = func(_ context.Context,
				claim appaccess.GatewayRebindClaimV2,
			) (gatewayRebindSuccessorNetworkObservation, error) {
				return gatewayRebindTypedAuthorizationNetworkObservation(t, claim)
			}
			if test.demote {
				fixture.manager.gatewayRebindAfterClaim = func(ctx context.Context,
					_ appaccess.GatewayRebindClaimV2,
				) error {
					_, err := fixture.db.ExecContext(ctx, `UPDATE users SET role='viewer' WHERE id=?`, lanApproverID)
					return err
				}
			}
			stage := &gatewayRebindTypedAuthorizationStageDriver{
				gatewayRebindTypedStageDriverFake: &gatewayRebindTypedStageDriverFake{t: t},
			}
			driver := managerGatewayRebindCrossStoreDriver{manager: fixture.manager, stage: stage}
			result, commitErr := fixture.manager.commitGatewayRebindWithDriver(context.Background(), repository,
				input, driver)
			if commitErr == nil || result != (GatewayRebindCommitResult{}) {
				t.Fatalf("bounded first-effect stop returned result=%#v error=%v", result, commitErr)
			}
			if stage.effects != test.wantEffects {
				snapshot, snapshotErr := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
				t.Fatalf("physical effect count=%d want=%d locks effects=%d/%v gateway=%d/%v calls recovery=%d heads=%d resolve=%d claim=%d claimError=%v commitError=%v snapshot=%#v snapshotError=%v",
					stage.effects, test.wantEffects, effectsCalls, effectsErr, gatewayCalls, gatewayErr,
					repository.recoveryCalls, repository.headCalls,
					repository.resolveCalls, repository.claimCalls, repository.claimErr, commitErr, snapshot, snapshotErr)
			}
			history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil || len(history.IntentsV2) != 1 || len(history.Progress) != 1 ||
				history.Progress[0].Record.Sequence != 1 {
				t.Fatalf("authorization refusal changed protected prefix: history=%#v error=%v", history, err)
			}
		})
	}
}

func gatewayRebindTypedAuthorizationNetworkObservation(t *testing.T,
	claim appaccess.GatewayRebindClaimV2,
) (gatewayRebindSuccessorNetworkObservation, error) {
	t.Helper()
	address, err := netip.ParseAddr(claim.Spec.SuccessorProfile.SelectedIPv4)
	if err != nil {
		t.Fatal(err)
	}
	candidate := hostNetworkCandidate{InterfaceID: claim.Spec.SuccessorProfile.InterfaceID,
		IPv4: address.String(), Prefix: netip.PrefixFrom(address, 24).Masked()}
	profile := gatewayProfileBinding{
		RevisionID: claim.Spec.SuccessorProfileRevisionID, RevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
		SpecDigest: claim.ConfigureApproval.SpecDigest, SelectedIPv4: claim.Spec.SuccessorProfile.SelectedIPv4,
		InterfaceID: claim.Spec.SuccessorProfile.InterfaceID, PortStart: claim.Spec.SuccessorProfile.PortStart,
		PortEnd: claim.Spec.SuccessorProfile.PortEnd,
	}
	plan, err := selectGatewayV2NetworkPlan(context.Background(), profile, gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) { return []hostNetworkCandidate{candidate}, nil },
		host:       func() (gatewayV2HostNetworkSnapshot, error) { return gatewayV2HostNetworkSnapshot{}, nil },
		docker:     func(context.Context) ([]netip.Prefix, error) { return []netip.Prefix{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return newGatewayRebindSuccessorNetworkObservation(gatewayRebindSuccessorPreflightObservation{
		candidates: []hostNetworkCandidate{candidate}, host: gatewayV2HostNetworkSnapshot{},
		dockerIDs: []string{}, docker: []netip.Prefix{},
		result: GatewayRebindSuccessorPreflight{
			RebindOperationID: claim.Spec.OperationID, ClaimRequestDigest: claim.RequestDigest,
			Profile: GatewayRebindSuccessorProfile{
				RevisionID: claim.Spec.SuccessorProfileRevisionID, RevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
				OperationID: claim.Spec.SuccessorProfileOperationID, RequestDigest: claim.SuccessorProfileRequestDigest,
				SpecDigest: claim.ConfigureApproval.SpecDigest, SelectedIPv4: claim.Spec.SuccessorProfile.SelectedIPv4,
				InterfaceID: claim.Spec.SuccessorProfile.InterfaceID, PortStart: claim.Spec.SuccessorProfile.PortStart,
				PortEnd: claim.Spec.SuccessorProfile.PortEnd, ApprovedBy: claim.ConfigureApproval.ActorID,
			},
			Network: GatewayRebindSuccessorNetworkPlan(plan),
		},
	})
}

func (d *gatewayRebindTypedAuthorizationStageDriver) observeImage(ctx context.Context,
	_ gatewayRebindProtectedIntentV2, guard gatewayRebindTypedEffectGuard,
) (string, error) {
	if guard == nil || guard(ctx) != nil {
		return "", errors.New("typed effect authority refused")
	}
	d.effects++
	return "", errors.New("test stopped after the first authorized physical effect boundary")
}
