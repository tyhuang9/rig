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
	startupCalls  int
	afterStartup  func(int)
	mutateStartup func(*appaccess.HostingGatewayStartupSnapshot)
}

func (r *gatewayRebindTypedAuthorizationRepository) HostingGatewayStartupSnapshot(ctx context.Context) (
	appaccess.HostingGatewayStartupSnapshot, error,
) {
	r.startupCalls++
	snapshot, err := r.Repository.HostingGatewayStartupSnapshot(ctx)
	if err == nil && r.mutateStartup != nil {
		r.mutateStartup(&snapshot)
	}
	if err == nil && r.afterStartup != nil {
		r.afterStartup(r.startupCalls)
	}
	return snapshot, err
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
		demote      string
		late        bool
		projection  string
		wantEffects int
	}{
		{name: "unchanged LAN approver reaches first authorized effect", wantEffects: 1},
		{name: "demoted LAN approver is refused before first effect", demote: "LAN"},
		{name: "demoted rebind approver is refused before first effect", demote: "rebind"},
		{name: "demoted configure approver is refused before first effect", demote: "configure"},
		{name: "LAN demotion between authority reads", demote: "LAN", late: true},
		{name: "rebind demotion between authority reads", demote: "rebind", late: true},
		{name: "configure demotion between authority reads", demote: "configure", late: true},
		{name: "missing active approval projection", projection: "missing approvals"},
		{name: "crossed grant projection", projection: "crossed grant"},
		{name: "missing grant projection", projection: "missing grant"},
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
			for _, approval := range []*appaccess.Approval{&input.RebindApproval, &input.ConfigureApproval} {
				approval.ActorID = uuid.NewString()
				if _, err := fixture.db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
					VALUES(?,?,'hash','administrator',datetime('now'),datetime('now'))`, approval.ActorID, approval.ActorID); err != nil {
					t.Fatal(err)
				}
			}
			fixture.manager.gatewayRebindV2NetworkObserver = func(_ context.Context,
				claim appaccess.GatewayRebindClaimV2,
			) (gatewayRebindSuccessorNetworkObservation, error) {
				return gatewayRebindTypedAuthorizationNetworkObservation(t, claim)
			}
			demoted := false
			demote := func(ctx context.Context) error {
				actor := lanApproverID
				if test.demote == "rebind" {
					actor = input.RebindApproval.ActorID
				} else if test.demote == "configure" {
					actor = input.ConfigureApproval.ActorID
				}
				result, err := fixture.db.ExecContext(ctx, `UPDATE users SET role='viewer' WHERE id=?`, actor)
				if err != nil {
					return err
				}
				if affected, err := result.RowsAffected(); err != nil || affected != 1 {
					t.Fatalf("demotion affected %d users: %v", affected, err)
				}
				demoted = true
				return nil
			}
			if test.demote != "" {
				if test.late {
					repository.afterStartup = func(call int) {
						if call == 1 {
							if err := demote(context.Background()); err != nil {
								t.Fatal(err)
							}
						}
					}
				} else {
					fixture.manager.gatewayRebindAfterClaim = func(ctx context.Context, _ appaccess.GatewayRebindClaimV2) error {
						return demote(ctx)
					}
				}
			}
			repository.mutateStartup = func(snapshot *appaccess.HostingGatewayStartupSnapshot) {
				switch test.projection {
				case "missing approvals":
					snapshot.ActiveRebindApprovalAuthority = nil
				case "crossed grant":
					snapshot.Grants.Claims[0].Claim.Spec.ApprovedBy = uuid.NewString()
				case "missing grant":
					snapshot.Grants.Claims = nil
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
			if test.demote != "" && !demoted || test.late && repository.startupCalls < 2 {
				t.Fatalf("demotion boundary was not reached: demoted=%t startup reads=%d", demoted, repository.startupCalls)
			}
			if _, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background()); err != nil {
				t.Fatalf("authorization refusal made ownership history unreadable: %v", err)
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
