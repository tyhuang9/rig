package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayStartupDiagnosticPreservesUnlockFailureBehavior(t *testing.T) {
	t.Run("upgrade clears inspection", func(t *testing.T) {
		manager, _, _, _, _, _, osLockHeld := gatewayV2CoordinatorTestFixture(t)
		successfulAcquire := managerAcquireGatewayOSLock
		t.Cleanup(func() { managerAcquireGatewayOSLock = successfulAcquire })
		managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
			release, err := successfulAcquire(ctx, store)
			if err != nil {
				return nil, err
			}
			return func() error {
				_ = release()
				return errors.New("injected startup unlock failure")
			}, nil
		}
		inspection, err := manager.InspectGatewayV2Startup(context.Background(), nil)
		if inspection != (GatewayV2StartupInspection{}) || !IsCode(err, DiagnosticRouteUnresolved) ||
			GatewayStartupDiagnosticCheckpoint(err) != "upgrade_gateway_unlock" || *osLockHeld {
			t.Fatalf("inspection=%+v checkpoint=%q held=%t err=%v",
				inspection, GatewayStartupDiagnosticCheckpoint(err), *osLockHeld, err)
		}
	})

	t.Run("LAN retains inspection", func(t *testing.T) {
		manager, _, journal, request, driver := grantedLANForDisable(t)
		_, projection := installNativeStartupSQL(t, manager, journal.OperationID)
		claim := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantCommitted, 4)
		claim.CurrentBinding = &projection
		driver.applyCalls = 0
		successfulAcquire := managerAcquireGatewayOSLock
		t.Cleanup(func() { managerAcquireGatewayOSLock = successfulAcquire })
		managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
			release, err := successfulAcquire(ctx, store)
			if err != nil {
				return nil, err
			}
			return func() error {
				_ = release()
				return errors.New("injected LAN startup unlock failure")
			}, nil
		}
		inspection, err := manager.InspectGatewayV2LANAccessStartup(
			context.Background(), []GatewayV2LANStartupClaim{claim}, nil)
		if inspection.Disposition != GatewayV2LANStartupNormal || !IsCode(err, DiagnosticRouteUnresolved) ||
			GatewayStartupDiagnosticCheckpoint(err) != "lan_gateway_unlock" || driver.applyCalls != 0 {
			t.Fatalf("inspection=%+v checkpoint=%q applies=%d err=%v",
				inspection, GatewayStartupDiagnosticCheckpoint(err), driver.applyCalls, err)
		}
	})
}

func TestGatewayV2LANStartupAuthorityPreservesCapturedCodeWhenCurrentReadCancels(t *testing.T) {
	manager, store, journal, request, driver := grantedLANForDisable(t)
	_, projection := installNativeStartupSQL(t, manager, journal.OperationID)
	claim := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantCommitted, 4)
	claim.CurrentBinding = &projection
	claims, err := validateGatewayV2LANAccessStartupClaims([]GatewayV2LANStartupClaim{claim}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, retainedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(
		func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
			cancel()
			return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("injected cancelling SQL read")
		})
	driver.events = nil
	release, err := manager.lockGateway(ctx)
	if err != nil {
		t.Fatal(err)
	}
	authority, authorityErr := manager.readGatewayV2LANStartupAuthorityLocked(
		ctx, store, state, retainedJournal, claims.grants, claims.disables)
	if releaseErr := release(); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if !reflect.DeepEqual(authority, gatewayV2LANStartupAuthority{}) || !IsCode(authorityErr, DiagnosticRouteUnresolved) ||
		IsCode(authorityErr, DiagnosticCancelled) ||
		GatewayStartupDiagnosticCheckpoint(authorityErr) != "current_sql_first_read" || len(driver.events) != 0 {
		t.Fatalf("authority=%+v checkpoint=%q events=%v err=%v",
			authority, GatewayStartupDiagnosticCheckpoint(authorityErr), driver.events, authorityErr)
	}
}

func TestGatewayV2LANAccessStartupSeparatesCommittedDiagnosticLeaves(t *testing.T) {
	for _, test := range []struct {
		name      string
		want      string
		configure func(*fakeGatewayV2LANGrantDriver)
		wantCalls []string
	}{
		{
			name: "topology",
			want: "lan_committed_topology",
			configure: func(driver *fakeGatewayV2LANGrantDriver) {
				driver.live.Profile.SelectedIPv4 = "192.0.2.100"
			},
			wantCalls: []string{"selected_preflight", "prove_committed"},
		},
		{
			name: "publications",
			want: "lan_committed_publications",
			configure: func(driver *fakeGatewayV2LANGrantDriver) {
				driver.failGrantProof = true
			},
			wantCalls: []string{"selected_preflight", "prove_committed", "prove_all_granted"},
		},
		{
			name: "selected interface",
			want: "lan_selected_interface",
			configure: func(driver *fakeGatewayV2LANGrantDriver) {
				driver.failSelectedPreflightAt = driver.selectedPreflightCalls + 1
			},
			wantCalls: []string{"selected_preflight"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, _, journal, request, driver := grantedLANForDisable(t)
			_, projection := installNativeStartupSQL(t, manager, journal.OperationID)
			claim := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantCommitted, 4)
			claim.CurrentBinding = &projection
			driver.events = nil
			driver.applyCalls = 0
			test.configure(driver)

			inspection, err := manager.InspectGatewayV2LANAccessStartup(
				context.Background(), []GatewayV2LANStartupClaim{claim}, nil)
			if err == nil || !reflect.DeepEqual(inspection, GatewayV2LANAccessStartupInspection{}) ||
				GatewayStartupDiagnosticCheckpoint(err) != test.want ||
				!reflect.DeepEqual(driver.events, test.wantCalls) || driver.applyCalls != 0 {
				t.Fatalf("inspection=%+v checkpoint=%q events=%v applies=%d err=%v",
					inspection, GatewayStartupDiagnosticCheckpoint(err), driver.events, driver.applyCalls, err)
			}
		})
	}
}
