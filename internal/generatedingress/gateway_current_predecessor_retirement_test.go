package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayCurrentPredecessorRetirementFailurePreservesStrongestUncertainty(t *testing.T) {
	prior := &Error{Code: DiagnosticRouteUnresolved}
	cleanup := &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	err := gatewayCurrentPredecessorRetirementFailure(prior, cleanup)
	var diagnostic *Error
	if !errors.As(err, &diagnostic) || !diagnostic.CandidateMayBeLive() || !errors.Is(err, prior) || !errors.Is(err, cleanup) {
		t.Fatalf("retirement uncertainty=%v diagnostic=%#v", err, diagnostic)
	}
}

func TestGatewayCurrentPredecessorRetirementBuildsCompleteChainBeforeEffects(t *testing.T) {
	f, input, driver := newGatewayRebindMultiFixture(t)
	ctx := context.Background()
	if result, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, input, driver); err != nil ||
		result.FinalPhase != appaccess.GatewayRebindCommitted {
		t.Fatalf("first commit result=%#v err=%v", result, err)
	}
	first, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstSelection, err := f.manager.selectGatewayCurrentLocked(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	profile := input.Inspection.Spec.SuccessorProfile
	profile.SelectedIPv4, profile.InterfaceID = "192.168.98.8", "rebind-next-successor"
	next := gatewayRebindSequenceNextInput(t, f, input.Inspection.Spec.SuccessorProfileRevisionNumber+1, profile)
	if result, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, next, driver); err != nil ||
		result.FinalPhase != appaccess.GatewayRebindCommitted {
		t.Fatalf("second commit result=%#v err=%v", result, err)
	}
	snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	selection, selectionErr := f.manager.selectGatewayCurrentLocked(context.Background(), snapshot)
	history, historyErr := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || selectionErr != nil || historyErr != nil {
		t.Fatalf("fixture snapshot=%v selection=%v history=%v", err, selectionErr, historyErr)
	}
	action, err := gatewayCurrentPredecessorRetirementActionFor(selection, history)
	if err != nil || action.CurrentFinalID == action.Native.Journal.Resources.FinalContainerID ||
		action.Native.Lineage.Kind != appaccess.GatewayRebindSourceGatewayUpgrade || len(action.Predecessors) != 1 ||
		action.Predecessors[0].Facts.Lineage != firstSelection.Lineage {
		t.Fatalf("action=%#v err=%v", action, err)
	}
	if next.Inspection.Spec.Predecessor.Lineage != firstSelection.Lineage || action.Predecessors[0].Facts.Terminal.Resources.FinalContainer.ID == action.CurrentFinalID {
		t.Fatal("two-commit action did not retain the exact first typed predecessor")
	}
	order := make([]string, 0, 8)
	profileProofs, localProofs := 0, 0
	runtime := &gatewayCurrentPhysicalRuntimeFake{}
	runtime.stopPredecessorFn = func(check context.Context, facts gatewayFinalOwnershipFacts,
		guard func(context.Context) error,
	) error {
		if !reflect.DeepEqual(facts, action.Predecessors[0].Facts) {
			t.Fatal("wrong typed predecessor stop authority")
		}
		order = append(order, "stop typed")
		return guard(check)
	}
	runtime.stopNativeFn = func(check context.Context, native gatewayCurrentNativePredecessorRetirement,
		guard func(context.Context) error,
	) error {
		if native.Lineage != action.Native.Lineage || len(order) != 1 {
			t.Fatal("native predecessor stop was not ordered after typed stop")
		}
		order = append(order, "stop native")
		return guard(check)
	}
	runtime.profileSafeFn = func(check context.Context, _ gatewayCurrentPredecessorRetirementAction,
		profile gatewayProfileBinding, guard func(context.Context) error,
	) bool {
		if len(order) != 2+profileProofs || guard(check) != nil {
			t.Fatal("listener proof began before both predecessor stops")
		}
		if profile != action.Native.State.Profile && profile != action.Predecessors[0].Facts.profile() {
			t.Fatal("listener proof received unknown ancestor profile")
		}
		order = append(order, "profile")
		profileProofs++
		return true
	}
	runtime.localSafeFn = func(check context.Context, _ gatewayCurrentPredecessorRetirementAction,
		port uint16, guard func(context.Context) error,
	) bool {
		if len(order) != 4+localProofs || guard(check) != nil || (port != action.Native.Journal.Source.LocalHostPort && port != action.Predecessors[0].Facts.LocalHostPort) {
			t.Fatal("local listener proof did not follow all profile proof")
		}
		order = append(order, "local")
		localProofs++
		return true
	}
	runtime.inventoriesStoppedFn = func(check context.Context, got gatewayCurrentPredecessorRetirementAction) bool {
		if got.CurrentFinalID != action.CurrentFinalID || len(order) != 6 || check.Err() != nil {
			t.Fatal("final inventory did not follow every listener proof")
		}
		order = append(order, "inventory")
		return true
	}
	managed := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: f.manager}, runtime: runtime,
	}
	if err := managed.retireGatewayCurrentPredecessors(ctx, action, func(context.Context) error { return nil }); err != nil ||
		len(order) != 7 || order[0] != "stop typed" || order[1] != "stop native" || order[6] != "inventory" {
		t.Fatalf("full ancestry retirement order=%v err=%v", order, err)
	}
	t.Run("missing intent refuses whole chain", func(t *testing.T) {
		broken := history
		broken.Intents, broken.IntentsV2 = nil, nil
		if _, err := gatewayCurrentPredecessorRetirementActionFor(selection, broken); err == nil {
			t.Fatal("broken ancestry produced a retirement action")
		}
	})
	t.Run("current container cannot cross native owner", func(t *testing.T) {
		broken := history
		broken.Predecessor.Journal.Resources.FinalContainerID = action.CurrentFinalID
		if _, err := gatewayCurrentPredecessorRetirementActionFor(selection, broken); err == nil {
			t.Fatal("crossed current/native container produced a retirement action")
		}
	})
}

func TestGatewayCurrentPredecessorRetirementAcceptsCanonicalLegacyLineage(t *testing.T) {
	f, _, selection := gatewayRebindLegacyCommittedSeed(t)
	history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	action, err := gatewayCurrentPredecessorRetirementActionFor(selection, history)
	if err != nil || selection.Terminal == nil || selection.Terminal.Format != gatewayRebindAttemptTerminalLegacyV1 ||
		action.Native.Lineage.Kind != appaccess.GatewayRebindSourceGatewayUpgrade || len(action.Predecessors) != 0 ||
		action.CurrentFinalID == action.Native.Journal.Resources.FinalContainerID {
		t.Fatalf("legacy lineage action=%#v selection=%#v err=%v", action, selection, err)
	}
}
