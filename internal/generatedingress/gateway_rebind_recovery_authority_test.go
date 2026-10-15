package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayRebindRequestedRollbackGuardsOnlyNewDirectionalEffects(t *testing.T) {
	t.Run("expired request session", func(t *testing.T) {
		fixture, driver, request := newGatewayRebindFixedAbortRecoveryFixture(t, nil)
		guardCalls := 0
		driver.rollbackStartGuard = func(context.Context) error {
			guardCalls++
			return errors.New("injected expired recovery session")
		}

		assertGatewayRebindRequestedRollbackRefusedWithoutEffects(t, fixture, driver, request, func() {
			if guardCalls != 1 {
				t.Fatalf("expired session guard calls=%d", guardCalls)
			}
		})
	})

	for _, approval := range []string{"rebind", "configure"} {
		t.Run("revoked original "+approval+" approver", func(t *testing.T) {
			fixture, driver, request := newGatewayRebindFixedAbortRecoveryFixture(t,
				func(f gatewayRebindPredecessorFixture, input *gatewayRebindCommitInput) {
					for _, value := range []*appaccess.Approval{&input.RebindApproval, &input.ConfigureApproval} {
						value.ActorID = uuid.NewString()
						if _, err := f.db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
							VALUES(?,?,'hash','administrator',datetime('now'),datetime('now'))`,
							value.ActorID, value.ActorID); err != nil {
							t.Fatal(err)
						}
					}
				})
			actorID := fixture.attempt.Claim.RebindApproval.ActorID
			if approval == "configure" {
				actorID = fixture.attempt.Claim.ConfigureApproval.ActorID
			}
			result, err := fixture.predecessor.db.ExecContext(context.Background(),
				`UPDATE users SET role='viewer' WHERE id=? AND role='administrator'`, actorID)
			if err != nil {
				t.Fatal(err)
			}
			affected, err := result.RowsAffected()
			if err != nil || affected != 1 {
				t.Fatalf("revoke original %s approver affected=%d error=%v", approval, affected, err)
			}
			guardCalls := 0
			driver.rollbackStartGuard = func(context.Context) error {
				guardCalls++
				return nil
			}

			assertGatewayRebindRequestedRollbackRefusedWithoutEffects(t, fixture, driver, request, func() {
				if guardCalls == 0 {
					t.Fatal("requested rollback did not check the live request session")
				}
			})
		})
	}

	t.Run("mandatory compensation survives session expiry", func(t *testing.T) {
		fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
		request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
			Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared,
			RollbackAllowed: true}
		driver := (managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
			stage: fixture.stageDriver, handover: fixture.runtime}).withRecoveryRepository(fixture.predecessor.repository)
		sessionExpired := false
		forwardGuardCalls, requestedRollbackGuardCalls := 0, 0
		driver.forwardSessionGuard = func(context.Context) error {
			forwardGuardCalls++
			if sessionExpired {
				return errors.New("injected expired recovery session")
			}
			return nil
		}
		driver.rollbackStartGuard = func(context.Context) error {
			requestedRollbackGuardCalls++
			if sessionExpired {
				return errors.New("injected expired recovery session")
			}
			return nil
		}
		fixture.runner.failFinalCreate = true
		fixture.runner.effects = nil
		fixture.runner.afterEffect = func(args []string) {
			if len(args) >= 2 && args[0] == "container" && args[1] == "create" {
				sessionExpired = true
			}
		}

		result, err := driver.reconcileSuccessorLocked(context.Background(), request, fixture.appendProgress)
		history, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		stage, stageErr := fixture.runtime.stage.read(context.Background(), fixture.attempt.Intent)
		predecessor, predecessorErr := fixture.runtime.predecessor(context.Background(), fixture.attempt)
		lastPhase := gatewayRebindProgressPhase("")
		if len(history.Progress) > 0 {
			lastPhase = history.Progress[len(history.Progress)-1].Record.Phase
		}
		cleaned := stageErr == nil && !stage.StageContainerFound && !stage.FinalContainerFound &&
			!stage.ConfigVolumeFound && !stage.DataVolumeFound && !stage.NetworkFound &&
			len(stage.OwnedContainers) == 0 && len(stage.OwnedVolumes) == 0 && len(stage.OwnedNetworks) == 0
		if err != nil || result.Disposition != appaccess.GatewayRebindDispositionAbort ||
			result.Last.Phase != gatewayRebindProgressHandoverRolledBack || historyErr != nil ||
			lastPhase != gatewayRebindProgressHandoverRolledBack || !sessionExpired || forwardGuardCalls == 0 ||
			requestedRollbackGuardCalls != 0 || fixture.runner.effectCount("container", "create") != 1 ||
			!cleaned || predecessorErr != nil || !predecessor.Running {
			t.Fatalf("mandatory rollback disposition=%s phase=%s retained=%s error=%v historyErr=%v sessionExpired=%t forwardGuards=%d requestedRollbackGuards=%d cleaned=%t stageErr=%v predecessorErr=%v predecessorRunning=%t effects=%#v",
				result.Disposition, result.Last.Phase, lastPhase, err, historyErr, sessionExpired,
				forwardGuardCalls, requestedRollbackGuardCalls, cleaned, stageErr, predecessorErr,
				predecessor.Running, fixture.runner.effects)
		}
	})
}

func newGatewayRebindFixedAbortRecoveryFixture(t *testing.T,
	prepare func(gatewayRebindPredecessorFixture, *gatewayRebindCommitInput),
) (gatewayRebindTypedHandoverRuntimeFixture, managerGatewayRebindCrossStoreDriver,
	gatewayRebindPhysicalReconcileRequest,
) {
	t.Helper()
	fixture := newGatewayRebindTypedHandoverRuntimeFixtureWithApprovals(t, true, prepare)
	driver := (managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}).withRecoveryRepository(fixture.predecessor.repository)
	fixture.runner.failFinalCreate = true
	physical, err := driver.reconcileSuccessorLocked(context.Background(), gatewayRebindPhysicalReconcileRequest{
		Attempt: fixture.attempt, Mode: gatewayRebindPhysicalReconcileUndecided,
		SQLPhase: appaccess.GatewayRebindPrepared, RollbackAllowed: true,
	}, fixture.appendProgress)
	if err != nil || physical.Disposition != appaccess.GatewayRebindDispositionAbort {
		t.Fatalf("prepare fixed abort physical result: disposition=%s error=%v", physical.Disposition, err)
	}
	receipt, err := fixture.predecessor.manager.installGatewayRebindTerminalForPhysicalLocked(
		context.Background(), fixture.attempt, physical)
	if err != nil {
		t.Fatal(err)
	}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileRollbackOnly, SQLPhase: appaccess.GatewayRebindPrepared,
		RollbackAllowed: true, Terminal: &receipt}
	if !validGatewayRebindPhysicalReconcileRequest(request) {
		t.Fatal("fixed abort recovery request is invalid")
	}
	fixture.runner.effects = nil
	return fixture, driver, request
}

func assertGatewayRebindRequestedRollbackRefusedWithoutEffects(t *testing.T,
	fixture gatewayRebindTypedHandoverRuntimeFixture, driver managerGatewayRebindCrossStoreDriver,
	request gatewayRebindPhysicalReconcileRequest, check func(),
) {
	t.Helper()
	ctx := context.Background()
	beforeSQL, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeProtected, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeStage, err := fixture.runtime.stage.read(ctx, fixture.attempt.Intent)
	if err != nil {
		t.Fatal(err)
	}
	fixture.runner.effects = nil
	result, reconcileErr := driver.reconcileSuccessorLocked(ctx, request, fixture.appendProgress)
	check()
	afterSQL, sqlErr := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	afterProtected, protectedErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	afterStage, stageErr := fixture.runtime.stage.read(ctx, fixture.attempt.Intent)
	if reconcileErr == nil || !reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) ||
		sqlErr != nil || protectedErr != nil || stageErr != nil || !reflect.DeepEqual(beforeSQL, afterSQL) ||
		!reflect.DeepEqual(beforeProtected, afterProtected) || !reflect.DeepEqual(beforeStage, afterStage) ||
		len(fixture.runner.effects) != 0 {
		t.Fatalf("requested rollback refusal result=%#v error=%v sqlErr=%v protectedErr=%v stageErr=%v sqlStable=%t protectedStable=%t stageStable=%t effects=%#v",
			result, reconcileErr, sqlErr, protectedErr, stageErr, reflect.DeepEqual(beforeSQL, afterSQL),
			reflect.DeepEqual(beforeProtected, afterProtected), reflect.DeepEqual(beforeStage, afterStage),
			fixture.runner.effects)
	}
}

func TestGatewayRebindRecoveryProjectionBindsCanonicalClaimRosterAndRuntimeHeads(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	snapshot, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil || snapshot.Active.Claim.V2 == nil {
		t.Fatalf("read active typed rebind: %v", err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	if evidence, phase, complete, err := gatewayRebindProtectedRecoveryProjection(history, *snapshot.Active); err != nil || !complete || evidence.Presence != appaccess.GatewayRebindProtectedEvidencePresent || phase == "" {
		t.Fatalf("canonical SQL/protected projection incomplete: presence=%s phase=%q complete=%t error=%v",
			evidence.Presence, phase, complete, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *appaccess.GatewayRebindHistoryEntry)
	}{
		{name: "actor-bound claim", mutate: func(_ *testing.T, entry *appaccess.GatewayRebindHistoryEntry) {
			entry.Claim.V2.RebindApproval.ActorID = uuid.NewString()
		}},
		{name: "roster", mutate: func(t *testing.T, entry *appaccess.GatewayRebindHistoryEntry) {
			if len(entry.RosterV2) == 0 {
				t.Fatal("typed rebind fixture has no retained roster")
			}
			entry.RosterV2[0].GrantAttemptID = uuid.NewString()
			digest, digestErr := appaccess.GatewayRebindRosterEntryV2Digest(entry.RosterV2[0])
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			entry.RosterV2[0].EntryDigest = digest
		}},
		{name: "runtime heads", mutate: func(t *testing.T, entry *appaccess.GatewayRebindHistoryEntry) {
			if len(entry.RuntimeHeads) == 0 {
				t.Fatal("typed rebind fixture has no retained runtime heads")
			}
			entry.RuntimeHeads[0].Generation++
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneGatewayRebindRecoveryHistoryEntry(*snapshot.Active)
			test.mutate(t, &changed)
			canonicalizeGatewayRebindRecoveryHistoryEntry(t, &changed)
			proposal := appaccess.GatewayRebindPreclaimProposalV2{
				Spec: changed.Claim.V2.Spec, RebindApproval: changed.Claim.V2.RebindApproval,
				ConfigureApproval: changed.Claim.V2.ConfigureApproval,
				Roster:            append([]appaccess.GatewayRebindRosterEntryV2(nil), changed.RosterV2...),
				RuntimeHeads:      append([]appaccess.GatewayRebindRuntimeHead(nil), changed.RuntimeHeads...),
			}
			if err := appaccess.ValidateGatewayRebindClaimV2ForProposal(*changed.Claim.V2, proposal); err != nil {
				t.Fatalf("independently canonical alternate SQL tuple rejected: %v", err)
			}
			evidence, phase, complete, projectionErr := gatewayRebindProtectedRecoveryProjection(history, changed)
			if projectionErr == nil || complete || evidence != (appaccess.GatewayRebindProtectedRecoveryEvidence{}) || phase != "" {
				t.Fatalf("canonical but foreign %s tuple paired with protected history: presence=%s phase=%q complete=%t error=%v",
					test.name, evidence.Presence, phase, complete, projectionErr)
			}
		})
	}
}

func TestGatewayRebindTypedCompletedRecoveryPreservesFixedPreDBUnresolvedPhase(t *testing.T) {
	for _, disposition := range []appaccess.GatewayRebindTerminalDisposition{
		appaccess.GatewayRebindDispositionCommit,
		appaccess.GatewayRebindDispositionAbort,
	} {
		t.Run(string(disposition), func(t *testing.T) {
			var fixture gatewayRebindTypedHandoverRuntimeFixture
			var driver managerGatewayRebindCrossStoreDriver
			var request gatewayRebindPhysicalReconcileRequest
			if disposition == appaccess.GatewayRebindDispositionCommit {
				fixture, driver, request = newGatewayRebindTypedForwardOnlyFixture(t)
				driver = driver.withRecoveryRepository(fixture.predecessor.repository)
			} else {
				fixture, driver, request = newGatewayRebindFixedAbortRecoveryFixture(t, nil)
			}
			before := gatewayRebindRequestedUnresolvedSnapshot(t, fixture, request.Terminal)
			request.SQLPhase = appaccess.GatewayRebindUnresolved
			request.RollbackAllowed = before.RollbackAllowed
			request.DatabaseCommitObserved = before.DatabaseCommitObserved
			if !validGatewayRebindPhysicalReconcileRequest(request) || before.Phase != appaccess.GatewayRebindUnresolved ||
				before.DatabaseCommitObserved || before.DatabaseCommittedEvent != nil {
				t.Fatal("fixed pre-DB unresolved recovery request is invalid")
			}
			fixture.runner.effects = nil

			result, err := driver.reconcileSuccessorLocked(context.Background(), request, fixture.appendProgress)
			after, readErr := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
			if err != nil || result.Disposition != disposition || readErr != nil ||
				!reflect.DeepEqual(before, after) || after.Phase != appaccess.GatewayRebindUnresolved ||
				after.DatabaseCommitObserved || after.DatabaseCommittedEvent != nil || len(fixture.runner.effects) != 0 {
				t.Fatalf("fixed pre-DB unresolved %s result=%s error=%v readErr=%v phase=%s database=%t stable=%t effects=%#v",
					disposition, result.Disposition, err, readErr, after.Phase, after.DatabaseCommitObserved,
					reflect.DeepEqual(before, after), fixture.runner.effects)
			}
		})
	}
}

func cloneGatewayRebindRecoveryHistoryEntry(value appaccess.GatewayRebindHistoryEntry) appaccess.GatewayRebindHistoryEntry {
	result := value
	claim := *value.Claim.V2
	result.Claim.V2 = &claim
	result.RosterV2 = append([]appaccess.GatewayRebindRosterEntryV2(nil), value.RosterV2...)
	result.RuntimeHeads = append([]appaccess.GatewayRebindRuntimeHead(nil), value.RuntimeHeads...)
	result.Events = append([]appaccess.GatewayRebindEvent(nil), value.Events...)
	result.Commands = append([]appaccess.GatewayRebindTransitionCommand(nil), value.Commands...)
	result.Transfers = append([]appaccess.GatewayRebindAllocationTransfer(nil), value.Transfers...)
	return result
}

func canonicalizeGatewayRebindRecoveryHistoryEntry(t *testing.T, entry *appaccess.GatewayRebindHistoryEntry) {
	t.Helper()
	claim := entry.Claim.V2
	var err error
	claim.Spec.RosterDigest, err = appaccess.GatewayRebindRosterV2Digest(entry.RosterV2)
	if err != nil {
		t.Fatal(err)
	}
	claim.Spec.RosterCount = int64(len(entry.RosterV2))
	claim.Spec.RuntimeHeadsDigest, err = appaccess.GatewayRebindRuntimeHeadsV2Digest(claim.Spec.OperationID,
		entry.RuntimeHeads)
	if err != nil {
		t.Fatal(err)
	}
	claim.Spec.RuntimeHeadsCount = int64(len(entry.RuntimeHeads))
	claim.RebindApproval.SpecDigest, err = appaccess.GatewayRebindSpecV2Digest(claim.Spec)
	if err != nil {
		t.Fatal(err)
	}
	claim.ConfigureApproval.SpecDigest, err = appaccess.GatewayProfileSpecDigest(claim.Spec.SuccessorProfile)
	if err != nil {
		t.Fatal(err)
	}
	claim.RequestDigest, err = canonicalDigest(struct {
		Spec      appaccess.GatewayRebindSpecV2 `json:"spec"`
		Rebind    appaccess.Approval            `json:"rebindApproval"`
		Configure appaccess.Approval            `json:"configureApproval"`
	}{claim.Spec, claim.RebindApproval, claim.ConfigureApproval})
	if err != nil {
		t.Fatal(err)
	}
	claim.SuccessorProfileRequestDigest, err = canonicalDigest(struct {
		Expected int64                        `json:"expectedRevisionNumber"`
		Spec     appaccess.GatewayProfileSpec `json:"spec"`
		Approval appaccess.Approval           `json:"approval"`
	}{claim.Spec.Predecessor.Lineage.ProfileRevisionNumber, claim.Spec.SuccessorProfile, claim.ConfigureApproval})
	if err != nil {
		t.Fatal(err)
	}
}
