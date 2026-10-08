package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayCurrentServingRestoreRepository struct {
	*appaccess.Repository
	mutate func(*appaccess.HostingGatewayStartupSnapshot)
}

func (r gatewayCurrentServingRestoreRepository) HostingGatewayStartupSnapshot(ctx context.Context) (appaccess.HostingGatewayStartupSnapshot, error) {
	snapshot, err := r.Repository.HostingGatewayStartupSnapshot(ctx)
	if err == nil && r.mutate != nil {
		r.mutate(&snapshot)
	}
	return snapshot, err
}

type gatewayCurrentServingRestoreFake struct {
	*gatewayCurrentLANBatchRetirementDriver
	t              *testing.T
	calls, effects int
	before         func()
	after          func(*gatewayCurrentPhysicalAttestation)
	action         gatewayCurrentServingRestoreAction
}

func (d *gatewayCurrentServingRestoreFake) restoreGatewayCurrentServing(ctx context.Context,
	action gatewayCurrentServingRestoreAction, guard func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	d.calls++
	d.action = action
	if !validGatewayCurrentServingRestoreAction(action) || guard == nil {
		d.t.Fatal("restore driver received invalid action or missing authorization guard")
	}
	if d.before != nil {
		d.before()
	}
	if err := guard(ctx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	d.effects++
	outcome := gatewayCurrentPhysicalStableServing
	if action.Mode == gatewayCurrentServingRestoreCompletedBatch {
		outcome = gatewayCurrentPhysicalRecoveryMixed
	}
	proof := gatewayCurrentPhysicalAttestationFixture(d.t, action.Target, action.Terminal, outcome)
	proof.LANRecovery = cloneGatewayCurrentLANRecoveryBatch(action.Selected.LANRecovery)
	proof.Digest, _ = gatewayCurrentPhysicalAttestationDigest(proof)
	if d.after != nil {
		d.after(&proof)
	}
	return proof, nil
}

func gatewayCurrentServingRestoreSQLFixture(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayCurrentSelection, *gatewayCurrentServingRestoreFake,
) {
	t.Helper()
	f, input, physical := newGatewayRebindCoordinatorFixture(t)
	if _, err := f.manager.commitGatewayRebindWithDriver(context.Background(), f.repository, input, physical); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	selection, err := f.manager.selectGatewayCurrentLocked(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// The original commit fixture deliberately uses a physical container ID
	// different from its old SQL component. A real ordinary redeploy aligns
	// both stores through their public transitions before restart is tested.
	selection = gatewayCurrentServingRestoreRedeployFixture(t, f, selection, input.Inspection.Roster[0])
	driver := &gatewayCurrentServingRestoreFake{gatewayCurrentLANBatchRetirementDriver: newGatewayCurrentLANBatchRetirementDriver(t), t: t}
	f.manager.gatewayCurrentPhysicalDriver = driver
	return f, selection, driver
}

func gatewayCurrentServingRestoreRedeployFixture(t *testing.T, f gatewayRebindPredecessorFixture,
	selection gatewayCurrentSelection, entry appaccess.GatewayRebindRosterEntryV2,
) gatewayCurrentSelection {
	t.Helper()
	f.manager.gatewayCurrentPhysicalDriver = &gatewayCurrentStateMachineDriver{t: t, terminal: *selection.Terminal}
	gatewayRebindSequenceRedeploy(t, f, *selection.State, entry)
	snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	selection, err = f.manager.selectGatewayCurrentLocked(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return selection
}

func TestGatewayCurrentServingRestoreUsesRealCompleteSQLAuthority(t *testing.T) {
	f, selection, driver := gatewayCurrentServingRestoreSQLFixture(t)
	before, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	handled, err := f.manager.RestoreGatewayCurrentServingStartup(context.Background(), f.repository)
	if err != nil || !handled || driver.calls != 1 || driver.effects != 1 || len(driver.ownedStops) != 0 {
		snapshot, _ := f.repository.HostingGatewayStartupSnapshot(context.Background())
		t.Fatalf("restore denied complete authority: handled=%t calls=%d effects=%d err=%v census=%t routes=%+v components=%+v",
			handled, driver.calls, driver.effects, err, gatewayCurrentServingRuntimeCensusMatches(*selection.State, snapshot),
			selection.State.Apps, snapshot.RuntimeComponents)
	}
	if driver.action.Mode != gatewayCurrentServingRestoreStable || !reflect.DeepEqual(driver.action.Target, *selection.State) {
		t.Fatal("stable restore changed selected protected target")
	}
	installed, err := selection.Store.load()
	after, filesErr := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil || !reflect.DeepEqual(installed, *selection.State) || filesErr != nil || !sameGatewayHistorySnapshot(before, after) {
		t.Fatal("physical restart wrote protected state or immutable history")
	}
}

func TestGatewayCurrentServingRestoreRefusesLostRuntimeAuthority(t *testing.T) {
	for _, atEffect := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_driver", true: "at_effect"}[atEffect], func(t *testing.T) {
			f, selection, driver := gatewayCurrentServingRestoreSQLFixture(t)
			drift := !atEffect
			repository := gatewayCurrentServingRestoreRepository{Repository: f.repository,
				mutate: func(snapshot *appaccess.HostingGatewayStartupSnapshot) {
					if drift {
						snapshot.RuntimeComponents[0].State = "failed"
					}
				}}
			driver.before = func() { drift = true }
			handled, err := f.manager.RestoreGatewayCurrentServingStartup(context.Background(), repository)
			if err == nil || !handled || driver.effects != 0 || len(driver.ownedStops) != 1 || !f.manager.gatewayRebindFailStop.Load() ||
				(atEffect && driver.calls != 1) || (!atEffect && driver.calls != 0) {
				t.Fatalf("unauthorized restore reached effect: handled=%t calls=%d effects=%d stops=%d err=%v",
					handled, driver.calls, driver.effects, len(driver.ownedStops), err)
			}
			installed, err := selection.Store.load()
			if err != nil || !reflect.DeepEqual(installed, *selection.State) {
				t.Fatal("refused restart changed protected state")
			}
		})
	}
}

func TestGatewayCurrentServingRestoreStopsExactOwnerAfterLostProof(t *testing.T) {
	for _, stage := range []string{"runtime authority", "cancelled", "wrong valid proof", "stop failed"} {
		t.Run(stage, func(t *testing.T) {
			f, selection, driver := gatewayCurrentServingRestoreSQLFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			drift := false
			repository := gatewayCurrentServingRestoreRepository{Repository: f.repository,
				mutate: func(snapshot *appaccess.HostingGatewayStartupSnapshot) {
					if drift {
						snapshot.RuntimeComponents[0].State = "failed"
					}
				}}
			driver.after = func(proof *gatewayCurrentPhysicalAttestation) {
				switch stage {
				case "runtime authority":
					drift = true
				case "cancelled":
					cancel()
				default:
					// A self-consistent proof for another revision still cannot
					// stand in for the exact selected target.
					wrong := cloneGatewayCurrentRouteState(proof.State)
					wrong.Revision++
					wrong.Digest, _ = gatewayCurrentRouteStateDigest(wrong)
					*proof = gatewayCurrentPhysicalAttestationFixture(t, wrong, proof.Terminal, gatewayCurrentPhysicalStableServing)
					if stage == "stop failed" {
						driver.stopErr = errors.New("stop acknowledgment unavailable")
					}
				}
			}
			handled, err := f.manager.RestoreGatewayCurrentServingStartup(ctx, repository)
			var diagnostic *Error
			if !handled || err == nil || !errors.As(err, &diagnostic) ||
				diagnostic.candidateMayBeLive != (stage == "stop failed") || driver.effects != 1 ||
				len(driver.ownedStops) != 1 || !f.manager.gatewayRebindAdmissionBlocked() {
				t.Fatalf("lost serving proof not compensated: handled=%t effects=%d stops=%d err=%+v",
					handled, driver.effects, len(driver.ownedStops), diagnostic)
			}
			installed, loadErr := selection.Store.load()
			if loadErr != nil || !reflect.DeepEqual(installed, *selection.State) ||
				!reflect.DeepEqual(driver.ownedStops[0].State, *selection.State) {
				t.Fatal("compensation changed protected state or stopped a different owner")
			}
		})
	}
}

func TestGatewayCurrentServingRestoreRefusesUnfinishedBatch(t *testing.T) {
	f, _, _, _, batch := gatewayCurrentLANBatchObservationFixture(t)
	driver := &gatewayCurrentServingRestoreFake{gatewayCurrentLANBatchRetirementDriver: newGatewayCurrentLANBatchRetirementDriver(t), t: t}
	f.manager.gatewayCurrentPhysicalDriver = driver
	// Incomplete recovery must refuse before consulting serving authority.
	// A nil embedded repository deliberately panics if the boundary is crossed.
	handled, err := f.manager.RestoreGatewayCurrentServingStartup(context.Background(), gatewayCurrentServingRestoreRepository{})
	if !handled || err == nil || driver.calls != 0 || len(driver.ownedStops) != 0 {
		t.Fatal("unfinished batch reached serving restoration")
	}
	installed, err := f.store.load()
	if err != nil || !reflect.DeepEqual(installed, batch) {
		t.Fatal("unfinished batch was normalized or overwritten")
	}
}

func TestGatewayCurrentServingRestoreRefusesLastReadProtectedReplacement(t *testing.T) {
	f, selection, driver := gatewayCurrentServingRestoreSQLFixture(t)
	reads := 0
	changed := cloneGatewayCurrentRouteState(*selection.State)
	changed.Revision++
	changed.Digest, _ = gatewayCurrentRouteStateDigest(changed)
	repository := gatewayCurrentServingRestoreRepository{Repository: f.repository,
		mutate: func(*appaccess.HostingGatewayStartupSnapshot) {
			reads++
			// Initial authority, then the guard's opening and closing reads.
			// Replace the mutable bundle on the final read after immutable
			// history has already been verified, leaving SQL unchanged.
			if reads == 3 {
				if err := selection.Store.saveNext(*selection.State, changed); err != nil {
					t.Fatal(err)
				}
			}
		}}
	handled, err := f.manager.RestoreGatewayCurrentServingStartup(context.Background(), repository)
	var diagnostic *Error
	if !handled || !errors.As(err, &diagnostic) || !diagnostic.candidateMayBeLive || driver.calls != 0 ||
		len(driver.ownedStops) != 0 || !f.manager.gatewayRebindAdmissionBlocked() {
		t.Fatalf("late protected replacement reached driver or stale stop: handled=%t calls=%d stops=%d err=%+v",
			handled, driver.calls, len(driver.ownedStops), diagnostic)
	}
	installed, loadErr := selection.Store.load()
	if loadErr != nil || !reflect.DeepEqual(installed, changed) {
		t.Fatal("refusal rewrote the externally replaced protected state")
	}
}

func TestGatewayCurrentServingRuntimeCensusRejectsIncompleteOrCrossedComponents(t *testing.T) {
	f, selection, _ := gatewayCurrentServingRestoreSQLFixture(t)
	snapshot, err := f.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || !gatewayCurrentServingRuntimeCensusMatches(*selection.State, snapshot) {
		t.Fatalf("positive census setup: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*appaccess.HostingGatewayStartupSnapshot)
	}{
		{"missing head", func(s *appaccess.HostingGatewayStartupSnapshot) { s.RuntimeHeads = nil }},
		{"missing component", func(s *appaccess.HostingGatewayStartupSnapshot) { s.RuntimeComponents = nil }},
		{"duplicate component", func(s *appaccess.HostingGatewayStartupSnapshot) {
			s.RuntimeComponents = append(s.RuntimeComponents, s.RuntimeComponents[0])
		}},
		{"different deployment", func(s *appaccess.HostingGatewayStartupSnapshot) {
			s.RuntimeComponents[0].DeploymentID = "14141414-1414-4414-8414-141414141414"
		}},
		{"different container", func(s *appaccess.HostingGatewayStartupSnapshot) {
			s.RuntimeComponents[0].ContainerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{"different slot", func(s *appaccess.HostingGatewayStartupSnapshot) { s.RuntimeComponents[0].Slot = "blue" }},
		{"failed component", func(s *appaccess.HostingGatewayStartupSnapshot) { s.RuntimeComponents[0].State = "failed" }},
		{"extra component", func(s *appaccess.HostingGatewayStartupSnapshot) {
			copy := s.RuntimeComponents[0]
			copy.Name = "unrecorded"
			s.RuntimeComponents = append(s.RuntimeComponents, copy)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := snapshot
			changed.RuntimeHeads = append([]appaccess.GatewayRebindRuntimeHead(nil), snapshot.RuntimeHeads...)
			changed.RuntimeComponents = append([]appaccess.GatewayStartupRuntimeComponent(nil), snapshot.RuntimeComponents...)
			test.mutate(&changed)
			if gatewayCurrentServingRuntimeCensusMatches(*selection.State, changed) {
				t.Fatal("incomplete or crossed runtime SQL authority accepted")
			}
		})
	}
	// Ensure repository errors cannot be confused with an empty, authorized census.
	if _, err := f.manager.RestoreGatewayCurrentServingStartup(context.Background(), nil); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatal("missing repository accepted")
	}
}

func TestGatewayCurrentServingRestoreKeepsCompletedBatchAndTerminalSQL(t *testing.T) {
	f := gatewayCurrentLANRecoveryCompletedSQLFixture(t)
	prepared, err := f.manager.InspectGatewayRebindCurrent(context.Background(), f.repository)
	if err != nil || prepared.CurrentRecoveryMode != GatewayCurrentRecoveryLANBatchDone {
		t.Fatalf("completed batch startup classification: %+v error=%v", prepared, err)
	}
	snapshot, err := f.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	selection, err := f.manager.selectGatewayCurrentLocked(context.Background(), snapshot.Rebind)
	if err != nil || selection.State.LANRecovery == nil {
		t.Fatalf("completed SQL fixture selection: %v", err)
	}
	driver := &gatewayCurrentServingRestoreFake{gatewayCurrentLANBatchRetirementDriver: newGatewayCurrentLANBatchRetirementDriver(t), t: t}
	f.manager.gatewayCurrentPhysicalDriver = driver
	handled, err := f.manager.RestoreGatewayCurrentServingStartup(context.Background(), f.repository)
	if err != nil || !handled || driver.calls != 1 || driver.effects != 1 ||
		driver.action.Mode != gatewayCurrentServingRestoreCompletedBatch || driver.action.Target.LANRecovery != nil {
		t.Fatalf("completed batch serving restore: handled=%t calls=%d effects=%d err=%v", handled, driver.calls, driver.effects, err)
	}
	installed, err := selection.Store.load()
	after, readErr := f.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || readErr != nil || !reflect.DeepEqual(installed, *selection.State) || !reflect.DeepEqual(snapshot, after) ||
		len(driver.ownedStops) != 0 {
		t.Fatal("restart retired the completed queue or changed terminal SQL authority")
	}
	confirmed, err := f.manager.InspectGatewayRebindCurrent(context.Background(), f.repository)
	if err != nil || !reflect.DeepEqual(prepared, confirmed) {
		t.Fatalf("completed batch startup checkpoint changed during restore: %v", err)
	}
	// Structural validity alone does not allow an acknowledged batch to
	// restart if its terminal census subsequently loses the clear proof.
	repository := gatewayCurrentServingRestoreRepository{Repository: f.repository,
		mutate: func(snapshot *appaccess.HostingGatewayStartupSnapshot) {
			snapshot.Disables.Claims[0].ProtectedClearAck = nil
		}}
	if _, err := f.manager.RestoreGatewayCurrentServingStartup(context.Background(), repository); err == nil ||
		driver.calls != 1 || len(driver.ownedStops) != 1 {
		t.Fatal("missing terminal clear proof reached a completed-batch restart")
	}
}

func TestGatewayCurrentServingRestoreActionRejectsIncompleteOrAlteredTarget(t *testing.T) {
	f, selection, _ := gatewayCurrentServingRestoreSQLFixture(t)
	snapshot, err := f.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := canonicalDigest(snapshot)
	action, err := gatewayCurrentServingRestoreActionForSelection(selection, digest)
	if err != nil || !validGatewayCurrentServingRestoreAction(action) {
		t.Fatalf("positive action: %v", err)
	}
	for _, field := range []string{"mode", "authorization", "revision", "apps"} {
		t.Run(field, func(t *testing.T) {
			changed := action
			changed.Target = cloneGatewayCurrentRouteState(action.Target)
			switch field {
			case "mode":
				changed.Mode = gatewayCurrentServingRestoreCompletedBatch
			case "authorization":
				changed.AuthorizationDigest = ""
			case "revision":
				changed.Target.Revision++
				changed.Target.Digest, _ = gatewayCurrentRouteStateDigest(changed.Target)
			case "apps":
				for appID := range changed.Target.Apps {
					delete(changed.Target.Apps, appID)
					break
				}
				changed.Target.Digest, _ = gatewayCurrentRouteStateDigest(changed.Target)
			}
			changed.Digest, _ = gatewayCurrentServingRestoreActionDigest(changed)
			if validGatewayCurrentServingRestoreAction(changed) {
				t.Fatal("changed purpose/authorization/protected target accepted")
			}
		})
	}
}
