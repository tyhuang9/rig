package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayV2StartupConfirmsSQLSelectedNativeAuthority(t *testing.T) {
	manager, store, request, _, _, driver, _ := gatewayV2CoordinatorTestFixture(t)
	installCommittedGeneration(t, manager, store, request.OperationID)
	driver.v1Running, driver.stageExists, driver.stageRunning = false, false, false
	driver.finalExists, driver.finalRunning = true, true
	driver.finalConfig = []byte("attested startup config")
	selection, err := manager.selectGatewayUpgradeGenerationLocked(request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := gatewayUpgradeCurrentLineage(selection)
	if err != nil {
		t.Fatal(err)
	}
	profile := gatewayCurrentFixtureProfile(lineage, selection.State.Profile)
	authority := gatewayCurrentAuthority(lineage)
	snapshot := appaccess.GatewayRebindRecoverySnapshot{CurrentProfile: &profile, CurrentSource: &authority}
	claims := []GatewayV2StartupClaim{{Request: request, State: appaccess.GatewayProfileUpgradeCommitted, StateSequence: 2}}
	for _, failure := range []string{"", "missing provider", "SQL failure", "changed SQL after proof", "wrong current operation", "active rebind", "changed protected selection"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			driver.events = nil
			manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
				calls++
				if failure == "SQL failure" {
					return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("read failure")
				}
				if failure == "changed protected selection" && calls == 3 {
					changed := cloneGatewayV2RouteState(selection.State)
					changed.Apps = map[string]gatewayV2AppRoute{}
					if !validGatewayV2RouteState(changed) {
						t.Fatal("replacement fixture is invalid")
					}
					data, err := json.Marshal(changed)
					if err != nil {
						t.Fatal(err)
					}
					// Inject external protected-file replacement, bypassing the
					// normal writer's pending-transition protocol deliberately.
					if err := upgradeProtectedWrite(store.v2Path, store.v2Purpose, data); err != nil {
						t.Fatal(err)
					}
				}
				value := snapshot
				if failure == "active rebind" {
					value.Active = &appaccess.GatewayRebindHistoryEntry{}
					value.Phase = appaccess.GatewayRebindPrepared
				}
				if failure == "wrong current operation" || (failure == "changed SQL after proof" && calls >= 4) {
					changed := authority
					changed.OperationID = "41414141-4141-4141-8141-414141414141"
					value.CurrentSource = &changed
				}
				return value, nil
			})
			if failure == "missing provider" {
				manager.options.RebindCurrentStateRepository = nil
			}
			inspection, err := manager.InspectGatewayV2Startup(context.Background(), claims)
			if failure == "" {
				if err != nil || inspection.Disposition != GatewayV2StartupNormalV2 || inspection.OperationID != request.OperationID || calls != 6 {
					t.Fatalf("exact projected SQL authority refused: inspection=%+v calls=%d err=%v", inspection, calls, err)
				}
			} else if err == nil || inspection != (GatewayV2StartupInspection{}) {
				t.Fatalf("unstable or missing SQL authority allowed startup: inspection=%+v err=%v", inspection, err)
			}
			if failure == "changed protected selection" && len(driver.events) != 0 {
				t.Fatal("physically proved a different selection from the SQL-bound observation")
			}
		})
	}
}
