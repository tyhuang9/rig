package generatedingress

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
)

func TestGatewayRebindRecoveryProjectorProvesOnlyStablePristineAbsence(t *testing.T) {
	for _, mode := range []string{
		"absent", "fence", "SQL drift", "route drift", "nonempty routes", "pending route",
		"upgrade artifact", "rebind artifact", "current route artifact", "unknown artifact",
	} {
		t.Run(mode, func(t *testing.T) {
			manager, runner := newManagerFixture(t, false)
			db, err := openGatewayRebindFixtureDatabase(manager.options.DataRoot)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			repository := &gatewayRebindCommitCurrentRepository{Repository: appaccess.New(db)}
			operationID := uuid.NewString()
			route := routeRecord{Slot: generatedidentity.SlotBlue, Endpoints: []generatedroute.RouteEndpoint{runner.endpoint}}
			saveNonemptyRoute := func() {
				t.Helper()
				if err := manager.store.save(routeState{Version: stateVersion,
					Active: map[string]routeRecord{runner.appID: route}}); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "fence":
				repository.fence = func() error { return appaccess.ErrGatewayRebindActive }
			case "SQL drift":
				reads := 0
				repository.mutate = func(value *appaccess.HostingGatewayStartupSnapshot) {
					reads++
					if reads == 2 {
						value.RuntimeHeads = append(value.RuntimeHeads, appaccess.GatewayRebindRuntimeHead{})
					}
				}
			case "route drift":
				reads := 0
				repository.mutate = func(*appaccess.HostingGatewayStartupSnapshot) {
					reads++
					if reads == 2 {
						saveNonemptyRoute()
					}
				}
			case "nonempty routes":
				saveNonemptyRoute()
			case "pending route":
				if err := manager.store.save(routeState{Version: stateVersion,
					Active: map[string]routeRecord{}, Pending: &pendingRoute{
						AppID: runner.appID, Proposed: route,
					}}); err != nil {
					t.Fatal(err)
				}
			case "upgrade artifact":
				store, storeErr := newGatewayUpgradeGenerationStore(manager.options.DataRoot, 1, uuid.NewString())
				if storeErr != nil || os.WriteFile(store.v2Path, []byte("partial"), 0o600) != nil {
					t.Fatalf("install partial upgrade artifact: %v", storeErr)
				}
			case "rebind artifact":
				store, storeErr := newGatewayRebindPredecessorCheckpointStore(manager.options.DataRoot, 1, operationID)
				if storeErr != nil || os.WriteFile(store.path, []byte("partial"), 0o600) != nil {
					t.Fatalf("install partial rebind artifact: %v", storeErr)
				}
			case "current route artifact":
				name, _ := gatewayCurrentRouteStateName(1, operationID)
				if err := os.WriteFile(filepath.Join(manager.store.root, name), []byte("partial"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unknown artifact":
				if err := os.WriteFile(filepath.Join(manager.store.root, "unknown.artifact"), []byte("partial"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			commands := len(runner.commands)
			status, inspectErr := manager.InspectGatewayRebindOperation(context.Background(), repository, operationID)
			if mode == "absent" {
				if inspectErr != nil || status.Availability != GatewayRebindOperationValidatedAbsent ||
					status.OperationID != operationID || status.Active || status.Protected != nil ||
					status.TerminalProof != GatewayRebindProofUnknown || status.CurrentProof != GatewayRebindProofUnknown ||
					ValidateGatewayRebindOperationInspection(status, operationID) != nil {
					t.Fatalf("pristine absence status=%#v error=%v", status, inspectErr)
				}
			} else if inspectErr == nil || !reflect.DeepEqual(status, GatewayRebindOperationInspection{}) {
				t.Fatalf("mode %q produced absence: status=%#v error=%v", mode, status, inspectErr)
			}
			if len(runner.commands) != commands {
				t.Fatalf("mode %q executed Docker commands: %v", mode,
					runner.commands[commands:])
			}
		})
	}
}

func TestGatewayRebindRecoveryPristineSQLRejectsResidualFields(t *testing.T) {
	baseline := appaccess.HostingGatewayStartupSnapshot{Rebind: appaccess.GatewayRebindRecoverySnapshot{
		History: []appaccess.GatewayRebindHistoryEntry{},
	}}
	if !gatewayRebindRecoveryPristineSQL(baseline) {
		t.Fatal("canonical empty SQL was not pristine")
	}
	for _, mutate := range []func(*appaccess.HostingGatewayStartupSnapshot){
		func(v *appaccess.HostingGatewayStartupSnapshot) {
			v.Upgrades.Claims = []appaccess.GatewayUpgradeStartupClaim{{}}
		},
		func(v *appaccess.HostingGatewayStartupSnapshot) {
			v.Rebind.History = []appaccess.GatewayRebindHistoryEntry{{}}
		},
		func(v *appaccess.HostingGatewayStartupSnapshot) { v.Rebind.Phase = appaccess.GatewayRebindPrepared },
		func(v *appaccess.HostingGatewayStartupSnapshot) { v.Rebind.DatabaseCommitObserved = true },
		func(v *appaccess.HostingGatewayStartupSnapshot) { v.Rebind.RollbackAllowed = true },
		func(v *appaccess.HostingGatewayStartupSnapshot) {
			v.Rebind.CurrentDatabaseCommittedEvent = &appaccess.GatewayRebindEvent{
				OperationID: gatewayRebindTestAdministrator, Sequence: 1,
				State: appaccess.GatewayRebindDatabaseCommitted,
			}
		},
	} {
		value := baseline
		mutate(&value)
		if gatewayRebindRecoveryPristineSQL(value) {
			t.Fatalf("residual SQL accepted as pristine: %#v", value)
		}
	}
}
