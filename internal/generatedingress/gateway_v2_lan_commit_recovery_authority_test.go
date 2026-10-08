package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func quarantinedGatewayV2LANCommitFixture(t *testing.T) (*Manager, *gatewayUpgradeStateStore,
	gatewayV2RouteState, gatewayMigrationJournal, gatewayV2LANGrantRequest, *fakeGatewayV2LANGrantDriver,
) {
	t.Helper()
	manager, store, _, journal, request, driver := gatewayV2LANGrantFixture(t)
	authorize, _ := allowGatewayV2LANGrant(t, manager, request, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), request, authorize); err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("ambiguous terminal DB commit")
	if err := manager.WithGatewayV2LANCommitResolution(context.Background(), request,
		func(context.Context, GatewayV2LANGrantReceipt) error { return callbackErr }); !errors.Is(err, callbackErr) {
		t.Fatal(err)
	}
	pending, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || pending.Pending == nil || pending.Pending.Kind != gatewayV2PendingLANWithdrawal {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	driver.events = nil
	driver.allGrantProofCalls = 0
	return manager, store, pending, journal, request, driver
}

func TestGatewayV2LANCommitRecoveryRevalidatesAuthorityAtEveryServingBoundary(t *testing.T) {
	for _, test := range []struct {
		name   string
		denyAt int
	}{
		{name: "before copy", denyAt: 2},
		{name: "before reload", denyAt: 4},
		{name: "after reload", denyAt: 5},
		{name: "before marker clear", denyAt: 10},
		{name: "after marker clear", denyAt: 11},
		{name: "before final publication proof", denyAt: 12},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store, pending, journal, request, driver := quarantinedGatewayV2LANCommitFixture(t)
			authorityErr := errors.New("committed grant authority revoked")
			calls := 0
			var first GatewayV2LANGrantObservation
			err := manager.WithGatewayV2LANCommitRecovery(context.Background(), request,
				func(_ context.Context, observation GatewayV2LANGrantObservation) error {
					calls++
					if calls == 1 {
						first = observation
					} else if !reflect.DeepEqual(observation, first) {
						t.Fatalf("callback identity changed: first=%#v later=%#v", first, observation)
					}
					if calls == test.denyAt {
						return authorityErr
					}
					return nil
				})
			if !errors.Is(err, authorityErr) {
				t.Fatalf("err=%v calls=%d events=%v", err, calls, driver.events)
			}
			retained, retainedJournal, loadErr := store.loadBoundUpgrade(journal.OperationID)
			if loadErr != nil || !reflect.DeepEqual(retainedJournal, journal) ||
				!reflect.DeepEqual(retained, pending) || driver.live.Apps[request.AppID].LAN != nil {
				t.Fatalf("retained=%#v live=%#v journal=%#v err=%v events=%v",
					retained, driver.live, retainedJournal, loadErr, driver.events)
			}
		})
	}
}

func TestGatewayV2LANCommitRecoveryRevalidatesAfterFinalProof(t *testing.T) {
	manager, store, pending, journal, request, driver := quarantinedGatewayV2LANCommitFixture(t)
	authorityErr := errors.New("committed grant authority revoked during final proof")
	revoked := false
	driver.proveAllGrantedHook = func(call int) {
		if call == 2 {
			revoked = true
		}
	}
	err := manager.WithGatewayV2LANCommitRecovery(context.Background(), request,
		func(context.Context, GatewayV2LANGrantObservation) error {
			if revoked {
				return authorityErr
			}
			return nil
		})
	if !errors.Is(err, authorityErr) || !revoked {
		t.Fatalf("err=%v revoked=%t events=%v", err, revoked, driver.events)
	}
	retained, retainedJournal, loadErr := store.loadBoundUpgrade(journal.OperationID)
	if loadErr != nil || !reflect.DeepEqual(retained, pending) || !reflect.DeepEqual(retainedJournal, journal) ||
		driver.live.Apps[request.AppID].LAN != nil {
		t.Fatalf("retained=%#v live=%#v journal=%#v err=%v", retained, driver.live, retainedJournal, loadErr)
	}
}

func TestApplyCommittedV2RoutesGuardedAuditsEveryDockerCommand(t *testing.T) {
	manager, _, state, journal, _, _ := gatewayV2LANGrantFixture(t)
	runner := newCommittedV2Runner()
	if runner.containerID != journal.Resources.FinalContainerID {
		t.Fatalf("runner container=%q journal container=%q", runner.containerID, journal.Resources.FinalContainerID)
	}
	manager.runner = runner
	commandCounts := make([]int, 0, 6)
	err := manager.applyCommittedV2RoutesGuarded(context.Background(), state, runner.containerID,
		"lan-grant-commit-recovery.json", func(context.Context) error {
			commandCounts = append(commandCounts, len(runner.commands))
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{0, 1, 2, 3, 4, 5}; !reflect.DeepEqual(commandCounts, want) {
		t.Fatalf("guard command counts=%v want=%v commands=%v", commandCounts, want, runner.commands)
	}
}

type gatewayV2LANCommitRecoveryUnsupportedDriver struct{ gatewayV2LANGrantDriver }

func TestGatewayV2LANCommitRecoveryFailsClosedForUnsupportedDriver(t *testing.T) {
	manager, store, pending, journal, request, driver := quarantinedGatewayV2LANCommitFixture(t)
	manager.gatewayV2LANGrantDriver = gatewayV2LANCommitRecoveryUnsupportedDriver{gatewayV2LANGrantDriver: driver}
	calls := 0
	err := manager.WithGatewayV2LANCommitRecovery(context.Background(), request,
		func(context.Context, GatewayV2LANGrantObservation) error { calls++; return nil })
	if err == nil || calls != 0 {
		t.Fatalf("err=%v calls=%d events=%v", err, calls, driver.events)
	}
	retained, retainedJournal, loadErr := store.loadBoundUpgrade(journal.OperationID)
	if loadErr != nil || !reflect.DeepEqual(retained, pending) || !reflect.DeepEqual(retainedJournal, journal) ||
		driver.live.Apps[request.AppID].LAN != nil {
		t.Fatalf("retained=%#v live=%#v journal=%#v err=%v", retained, driver.live, retainedJournal, loadErr)
	}
}

func TestGatewayV2LANCommitRecoveryReleaseFailurePreservesSafetyOutcome(t *testing.T) {
	installReleaseFailure := func(t *testing.T) *bool {
		t.Helper()
		originalAcquire := managerAcquireGatewayOSLock
		released := false
		managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
			release, err := originalAcquire(ctx, store)
			if err != nil {
				return nil, err
			}
			return func() error {
				released = true
				if err := release(); err != nil {
					return err
				}
				return errors.New("injected gateway lock release failure")
			}, nil
		}
		t.Cleanup(func() { managerAcquireGatewayOSLock = originalAcquire })
		return &released
	}

	t.Run("uncertain serving candidate is preserved", func(t *testing.T) {
		manager, _, _, _, request, driver := quarantinedGatewayV2LANCommitFixture(t)
		released := installReleaseFailure(t)
		driver.failApply = driver.applyCalls + 1
		driver.stopOwnedGatewayErr = errors.New("injected owned stop failure")
		calls := 0
		err := manager.WithGatewayV2LANCommitRecovery(context.Background(), request,
			func(context.Context, GatewayV2LANGrantObservation) error {
				calls++
				if calls == 5 {
					return errors.New("authority revoked after reload")
				}
				return nil
			})
		var diagnostic *Error
		if !*released || !errors.As(err, &diagnostic) || !diagnostic.CandidateMayBeLive() {
			t.Fatalf("err=%v released=%t diagnostic=%#v events=%v", err, *released, diagnostic, driver.events)
		}
	})

	t.Run("early refusal does not invent serving uncertainty", func(t *testing.T) {
		manager, _, _, _, request, driver := quarantinedGatewayV2LANCommitFixture(t)
		released := installReleaseFailure(t)
		authorityErr := errors.New("authority revoked before effects")
		err := manager.WithGatewayV2LANCommitRecovery(context.Background(), request,
			func(context.Context, GatewayV2LANGrantObservation) error { return authorityErr })
		var diagnostic *Error
		if !*released || !errors.Is(err, authorityErr) || !errors.As(err, &diagnostic) ||
			diagnostic.CandidateMayBeLive() || containsString(driver.events, "apply:lan-grant-commit-recovery.json") {
			t.Fatalf("err=%v released=%t diagnostic=%#v events=%v", err, *released, diagnostic, driver.events)
		}
	})
}
