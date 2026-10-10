package generatedingress

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindNoEffectRuntimeRunner struct {
	owned    bool
	requests [][]string
}

func TestGatewayRebindNoEffectRuntimeProvesAbsenceBeforePreparedRollback(t *testing.T) {
	for _, test := range []struct {
		name      string
		owned     bool
		wantAbort bool
	}{
		{name: "complete physical absence permits rollback", wantAbort: true},
		{name: "operation-scoped owned resource retains prepared fence", owned: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, input, _ := newGatewayRebindCoordinatorFixture(t)
			runner := &gatewayRebindNoEffectRuntimeRunner{owned: test.owned}
			fixture.manager.runner = runner
			fixture.manager.gatewayRebindV2NetworkObserver = func(context.Context,
				appaccess.GatewayRebindClaimV2,
			) (gatewayRebindSuccessorNetworkObservation, error) {
				return gatewayRebindSuccessorNetworkObservation{}, errors.New("injected successor network unavailable")
			}
			driver := managerGatewayRebindCrossStoreDriver{manager: fixture.manager}
			result, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(),
				fixture.repository, input, driver)
			if test.wantAbort {
				if err != nil {
					t.Fatalf("prove physical absence and rollback: %v; Docker requests=%#v", err, runner.requests)
				}
				if result.FinalPhase != appaccess.GatewayRebindRolledBack ||
					result.Disposition != appaccess.GatewayRebindDispositionAbort || !result.FenceReleased {
					t.Fatalf("unexpected no-effect rollback result: %#v", result)
				}
				history, historyErr := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
				if historyErr != nil || len(history.IntentsV2) != 0 || len(history.TerminalsV2) != 1 ||
					history.TerminalsV2[0].Receipt.NoEffectProof == nil {
					t.Fatalf("no-effect terminal history=%#v error=%v", history, historyErr)
				}
				return
			}
			if err == nil || result != (GatewayRebindCommitResult{}) {
				t.Fatalf("owned successor resource accepted: result=%#v error=%v", result, err)
			}
			snapshot, snapshotErr := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
			if snapshotErr != nil || snapshot.Active == nil || snapshot.Phase != appaccess.GatewayRebindPrepared ||
				snapshot.DatabaseCommitObserved || !snapshot.RollbackAllowed {
				t.Fatalf("owned resource did not retain prepared fence: snapshot=%#v error=%v", snapshot, snapshotErr)
			}
			if fenceErr := fixture.repository.CheckGatewayRebindFence(context.Background()); fenceErr == nil {
				t.Fatal("owned resource unexpectedly released the SQL fence")
			}
		})
	}
}

func (r *gatewayRebindNoEffectRuntimeRunner) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := append([]string(nil), request.Args...)
	r.requests = append(r.requests, args)
	if len(args) < 2 {
		return runtimeprocess.CommandResult{}, errors.New("unexpected no-effect Docker request")
	}
	if args[1] == "inspect" {
		return gatewayCurrentPhysicalNotFound(args[0])
	}
	if args[1] != "ls" {
		return runtimeprocess.CommandResult{}, errors.New("unexpected no-effect Docker request")
	}
	if r.owned && containsGatewayRebindNoEffectLabelFilter(args) {
		return runtimeprocess.CommandResult{Stdout: []byte("orphan-owned-resource\n")}, nil
	}
	return runtimeprocess.CommandResult{}, nil
}

func containsGatewayRebindNoEffectLabelFilter(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "label="+gatewayRebindGenerationLabelKey+"=") {
			return true
		}
	}
	return false
}
