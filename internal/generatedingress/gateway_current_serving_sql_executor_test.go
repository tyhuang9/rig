package generatedingress

import (
	"context"
	"reflect"
	"testing"
)

// Real SQLite, protected history, public consumers and the concrete managed
// driver are composed here. Docker commands and reachability remain simulated.
func TestGatewayCurrentServingRestoreComposesSQLWithConcreteExecutor(t *testing.T) {
	for _, completedBatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "stable", true: "completed batch then retirement"}[completedBatch], func(t *testing.T) {
			var f gatewayRebindPredecessorFixture
			if completedBatch {
				f = gatewayCurrentLANRecoveryCompletedSQLFixture(t)
			} else {
				f, _, _ = gatewayCurrentServingRestoreSQLFixture(t)
			}
			ctx := context.Background()
			snapshot, err := f.repository.HostingGatewayStartupSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			selection, err := f.manager.selectGatewayCurrentLocked(ctx, snapshot.Rebind)
			if err != nil {
				t.Fatal(err)
			}
			authorizationDigest, _ := canonicalDigest(snapshot)
			action, err := gatewayCurrentServingRestoreActionForSelection(selection, authorizationDigest)
			if err != nil {
				t.Fatal(err)
			}
			target, err := gatewayCurrentPhysicalTargetFor(selection, action.Target, nil, action.Selected.LANRecovery)
			if err != nil {
				t.Fatal(err)
			}
			localPort := f.journal.Source.LocalHostPort
			runner := newGatewayCurrentPhysicalExecutor(t, target, action.Target, action.Target, localPort)
			runner.stopAt(mustGatewayCurrentPhysicalConfig(t, action.Target))
			runner.lostStartAck = true
			driver := managedGatewayCurrentServingExecutorDriver(
				gatewayCurrentStateFixture{manager: f.manager}, runner, localPort)
			contract := installGatewayRebindStrictPredecessorRetirementContract(t, f.manager, driver)
			files, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			var expectedRetired gatewayCurrentRouteState
			var postRetirementAction gatewayCurrentPredecessorRetirementAction
			if completedBatch {
				history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
				if err != nil {
					t.Fatal(err)
				}
				expectedRetired, err = gatewayCurrentLANRecoveryRetiredState(*selection.State)
				if err != nil {
					t.Fatal(err)
				}
				postSelection := selection
				postSelection.State = &expectedRetired
				postRetirementAction, err = gatewayCurrentPredecessorRetirementActionFor(postSelection, history)
				if err != nil || contract.expected.CurrentLineage != postRetirementAction.CurrentLineage ||
					contract.expected.CurrentFinalID != postRetirementAction.CurrentFinalID ||
					!reflect.DeepEqual(contract.expected.CurrentFacts, postRetirementAction.CurrentFacts) ||
					!reflect.DeepEqual(contract.expected.Native, postRetirementAction.Native) ||
					!reflect.DeepEqual(contract.expected.Predecessors, postRetirementAction.Predecessors) {
					t.Fatalf("completed batch changed predecessor ownership or ancestry: %v", err)
				}
				contract.observedActions = []gatewayCurrentPredecessorRetirementAction{contract.expected, postRetirementAction}
			}
			handled, err := f.manager.RestoreGatewayCurrentServingStartup(ctx, f.repository)
			if err != nil || !handled || !runner.container.Running || contract.retireCalls != 1 || !containsGatewayCurrentPhysicalEffect(runner.effects,
				[]string{"container", "start", target.Resources.FinalContainer.ID}) {
				t.Fatalf("SQL-authorized concrete restore failed: handled=%t effects=%v err=%v", handled, runner.effects, err)
			}
			installed, loadErr := selection.Store.load()
			after, readErr := f.repository.HostingGatewayStartupSnapshot(ctx)
			if loadErr != nil || readErr != nil || !reflect.DeepEqual(installed, *selection.State) || !reflect.DeepEqual(snapshot, after) {
				t.Fatal("concrete restart rewrote SQL or protected recovery state")
			}
			if completedBatch {
				if err := f.manager.RetireGatewayV2LANRecoveryBatch(ctx, GatewayLANGrantStartupClaims(after.Grants),
					GatewayLANDisableStartupClaims(after.Disables)); err != nil {
					t.Fatalf("proved running completed batch did not retire: %v", err)
				}
				retired, err := selection.Store.load()
				if err != nil || !reflect.DeepEqual(retired, expectedRetired) || retired.LANRecovery != nil ||
					retired.Revision != installed.Revision+1 || !reflect.DeepEqual(retired.Apps, installed.Apps) {
					t.Fatal("retirement changed serving routes or retained the completed marker")
				}
				contract.expected = postRetirementAction
				effects := len(runner.effects)
				if handled, err := f.manager.RestoreGatewayCurrentServingStartup(ctx, f.repository); err != nil || !handled ||
					len(runner.effects) != effects || !runner.container.Running || contract.retireCalls != 2 {
					t.Fatalf("stable post-retirement restoration was not idempotent: effects=%v err=%v", runner.effects, err)
				}
			}
			gatewayRebindSequenceRequireRetainedFiles(t, f.manager, files)
		})
	}
}
