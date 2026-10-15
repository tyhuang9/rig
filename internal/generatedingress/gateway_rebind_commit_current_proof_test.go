package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindCommitCurrentRepository struct {
	*appaccess.Repository
	mutate func(*appaccess.HostingGatewayStartupSnapshot)
	fence  func() error
}

func (r gatewayRebindCommitCurrentRepository) HostingGatewayStartupSnapshot(ctx context.Context) (appaccess.HostingGatewayStartupSnapshot, error) {
	snapshot, err := r.Repository.HostingGatewayStartupSnapshot(ctx)
	if err == nil && r.mutate != nil {
		r.mutate(&snapshot)
	}
	return snapshot, err
}

func (r gatewayRebindCommitCurrentRepository) CheckGatewayRebindFence(ctx context.Context) error {
	if r.fence != nil {
		return r.fence()
	}
	return r.Repository.CheckGatewayRebindFence(ctx)
}

func gatewayRebindCommitCurrentNativeFixture(t *testing.T) gatewayRebindPredecessorFixture {
	t.Helper()
	// The selected route and real SQL runtime component must identify the same
	// container. The older predecessor-only fixture deliberately differs here.
	f := newGatewayRebindPredecessorFixtureWithEndpoint(t, false, '3')
	f.manager.options.RebindCurrentStateRepository = f.repository
	f.manager.options.RebindFenceCheck = f.repository.CheckGatewayRebindFence
	f.manager.gatewayRebindFailStop = &atomic.Bool{}
	f.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	return f
}

func TestGatewayRebindCommitCurrentProofNativeReadOnly(t *testing.T) {
	for _, mode := range []string{"running", "stopped", "terminal barrier"} {
		t.Run(mode, func(t *testing.T) {
			f := gatewayRebindCommitCurrentNativeFixture(t)
			ctx := context.Background()
			before, err := f.repository.HostingGatewayStartupSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			files, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			commands := len(f.runner.commands)
			barrier := mode == "terminal barrier"
			defer f.manager.gatewayRebindCommitBarrierLatch().Store(false)
			var buffers [][]byte
			inspector := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
				value := gatewayRebindFixtureDockerObservation(t, f)
				if mode == "stopped" {
					makeGatewayRebindPreparedStoppedObservation(&value)
				}
				buffers = append(buffers, value.FinalConfig, value.FinalRestartConfig)
				return value, nil
			}
			var proof gatewayRebindCommitCurrentProof
			_, err = withCrossStoreFixtureEffectLocks(t, f.manager, func() (gatewayRebindPreparedAttempt, error) {
				f.manager.gatewayRebindCommitBarrierLatch().Store(barrier)
				var proofErr error
				proof, proofErr = f.manager.readGatewayRebindCommitCurrentProofWithInspectLocked(ctx, f.repository,
					before.Rebind, func(context.Context) error { return nil }, inspector)
				return gatewayRebindPreparedAttempt{}, proofErr
			})
			if err != nil || !validSHA256(proof.Digest) || proof.Selection.Kind != gatewayCurrentSelectionUpgrade {
				t.Fatalf("native proof mode=%s digest=%q error=%v", mode, proof.Digest, err)
			}
			after, sqlErr := f.repository.HostingGatewayStartupSnapshot(ctx)
			afterFiles, fileErr := readGatewayHistorySnapshotMode(f.manager.store, true)
			if sqlErr != nil || fileErr != nil || !reflect.DeepEqual(before, after) || !sameGatewayHistorySnapshot(files, afterFiles) ||
				len(f.runner.commands) != commands || f.manager.gatewayRebindFailStopLatch().Load() ||
				f.manager.gatewayRebindCommitBarrierLatch().Load() != barrier {
				t.Fatalf("observation changed SQL/files/commands/latches: sql=%v files=%v", sqlErr, fileErr)
			}
			for _, buffer := range buffers {
				for _, b := range buffer {
					if b != 0 {
						t.Fatal("physical config buffer retained after proof")
					}
				}
			}
		})
	}
}

func TestGatewayRebindCommitCurrentProofRefusesDrift(t *testing.T) {
	for _, mode := range []string{"initial fence", "late fence", "initial session", "late session", "cancelled session",
		"initial failstop", "late failstop", "barrier changed", "SQL selection", "late runtime census", "Docker drift", "protected state",
		"last SQL census", "last session", "last fence", "last protected state", "final session", "final fence"} {
		t.Run(mode, func(t *testing.T) {
			f := gatewayRebindCommitCurrentNativeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			before, err := f.repository.HostingGatewayStartupSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			reads, sessions, fences, inspections := 0, 0, 0, 0
			repository := gatewayRebindCommitCurrentRepository{Repository: f.repository}
			repository.fence = func() error {
				fences++
				if mode == "initial fence" || (mode == "late fence" && fences == 3) || (mode == "last fence" && fences == 4) ||
					(mode == "final fence" && fences == 5) {
					return errors.New("fenced")
				}
				return nil
			}
			repository.mutate = func(s *appaccess.HostingGatewayStartupSnapshot) {
				reads++
				if mode == "SQL selection" {
					s.Rebind.CurrentSource = nil
				}
				if reads == 3 && mode == "last SQL census" {
					s.RuntimeComponents = nil
				}
				if reads == 3 && mode == "last protected state" {
					state := cloneGatewayV2RouteState(f.state)
					// Even identical content in a replaced protected file must
					// invalidate the captured filesystem identity.
					if err := f.store.saveCommittedV2State(state, f.journal); err != nil {
						t.Fatal(err)
					}
				}
				if reads != 2 {
					return
				}
				switch mode {
				case "late runtime census":
					s.RuntimeComponents = nil
				case "late failstop":
					f.manager.gatewayRebindFailStopLatch().Store(true)
				case "barrier changed":
					f.manager.gatewayRebindCommitBarrierLatch().Store(true)
				case "protected state":
					state := cloneGatewayV2RouteState(f.state)
					if err := f.store.saveCommittedV2State(state, f.journal); err != nil {
						t.Fatal(err)
					}
				}
			}
			defer f.manager.gatewayRebindFailStopLatch().Store(false)
			defer f.manager.gatewayRebindCommitBarrierLatch().Store(false)
			guard := func(context.Context) error {
				sessions++
				if mode == "initial session" || (mode == "late session" && sessions == 3) || (mode == "last session" && sessions == 4) ||
					(mode == "final session" && sessions == 5) {
					return errors.New("revoked")
				}
				if mode == "cancelled session" && sessions == 3 {
					cancel()
				}
				return nil
			}
			inspector := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
				inspections++
				value := gatewayRebindFixtureDockerObservation(t, f)
				if mode == "Docker drift" && inspections == 2 {
					value.FinalContainer.ID = strings.Repeat("f", 64)
				}
				return value, nil
			}
			var proof gatewayRebindCommitCurrentProof
			_, err = withCrossStoreFixtureEffectLocks(t, f.manager, func() (gatewayRebindPreparedAttempt, error) {
				if mode == "initial failstop" {
					f.manager.gatewayRebindFailStopLatch().Store(true)
				}
				var proofErr error
				proof, proofErr = f.manager.readGatewayRebindCommitCurrentProofWithInspectLocked(ctx, repository, before.Rebind, guard, inspector)
				return gatewayRebindPreparedAttempt{}, proofErr
			})
			if err == nil || !reflect.DeepEqual(proof, gatewayRebindCommitCurrentProof{}) {
				t.Fatalf("drift returned proof: mode=%s error=%v", mode, err)
			}
			after, sqlErr := f.repository.HostingGatewayStartupSnapshot(context.Background())
			if sqlErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("failed observation wrote SQL")
			}
		})
	}
}

func TestGatewayRebindCommitCurrentProofNativeCensus(t *testing.T) {
	f := gatewayRebindCommitCurrentNativeFixture(t)
	ctx := context.Background()
	snapshot, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := f.manager.selectGatewayCurrentLocked(ctx, snapshot.Rebind)
	if err != nil || !f.manager.gatewayRebindCommitCurrentCensusMatchesLocked(ctx, selection, snapshot) {
		t.Fatalf("native census control: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*appaccess.HostingGatewayStartupSnapshot)
	}{
		{"missing heads", func(s *appaccess.HostingGatewayStartupSnapshot) { s.RuntimeHeads = nil }},
		{"missing components", func(s *appaccess.HostingGatewayStartupSnapshot) { s.RuntimeComponents = nil }},
		{"wrong container", func(s *appaccess.HostingGatewayStartupSnapshot) {
			s.RuntimeComponents[0].ContainerID = strings.Repeat("f", 64)
		}},
		{"inactive component", func(s *appaccess.HostingGatewayStartupSnapshot) { s.RuntimeComponents[0].State = "stopped" }},
		{"missing grant", func(s *appaccess.HostingGatewayStartupSnapshot) { s.Grants.Claims = nil }},
		{"lost grant approval", func(s *appaccess.HostingGatewayStartupSnapshot) { s.Grants.Claims[0].ApproverIsAdministrator = false }},
		{"lost access head", func(s *appaccess.HostingGatewayStartupSnapshot) { s.Grants.Claims[0].AccessHeadCurrent = false }},
		{"wrong grant source", func(s *appaccess.HostingGatewayStartupSnapshot) {
			s.Grants.Claims[0].CurrentGatewaySource.OperationID = "77777777-7777-4777-8777-777777777777"
		}},
		{"recovery phase", func(s *appaccess.HostingGatewayStartupSnapshot) { s.Rebind.Phase = appaccess.GatewayRebindPrepared }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fresh, err := f.repository.HostingGatewayStartupSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&fresh)
			if f.manager.gatewayRebindCommitCurrentCensusMatchesLocked(ctx, selection, fresh) {
				t.Fatal("invalid census accepted")
			}
		})
	}
}

func TestGatewayRebindCommitCurrentProofStoppedRebound(t *testing.T) {
	f, selection, _ := gatewayCurrentServingRestoreSQLFixture(t)
	ctx := context.Background()
	target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := newGatewayCurrentPhysicalExecutor(t, target, target.State, target.State, f.manager.options.HostPort)
	runner.stopAt(mustGatewayCurrentPhysicalConfig(t, target.State))
	f.manager.runner = runner
	before, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	files, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	var proof gatewayRebindCommitCurrentProof
	_, err = withCrossStoreFixtureEffectLocks(t, f.manager, func() (gatewayRebindPreparedAttempt, error) {
		var proofErr error
		proof, proofErr = f.manager.readGatewayRebindCommitCurrentProofLocked(ctx, f.repository, before.Rebind, func(context.Context) error { return nil })
		return gatewayRebindPreparedAttempt{}, proofErr
	})
	if err != nil || !validSHA256(proof.Digest) || !sameGatewayCurrentSelection(selection, proof.Selection) || len(runner.effects) != 0 {
		t.Fatalf("stopped current proof: effects=%v error=%v", runner.effects, err)
	}
	after, sqlErr := f.repository.HostingGatewayStartupSnapshot(ctx)
	afterFiles, fileErr := readGatewayHistorySnapshotMode(f.manager.store, true)
	if sqlErr != nil || fileErr != nil || !reflect.DeepEqual(before, after) || !sameGatewayHistorySnapshot(files, afterFiles) {
		t.Fatal("stopped proof wrote SQL or protected state")
	}
	for _, mode := range []string{"missing component", "lost grant approval", "missing grant", "wrong restart config"} {
		t.Run(mode, func(t *testing.T) {
			repository := gatewayRebindCommitCurrentRepository{Repository: f.repository, mutate: func(s *appaccess.HostingGatewayStartupSnapshot) {
				switch mode {
				case "missing component":
					s.RuntimeComponents = nil
				case "lost grant approval":
					s.Grants.Claims[0].ApproverIsAdministrator = false
				case "missing grant":
					s.Grants.Claims = nil
				}
			}}
			if mode == "wrong restart config" {
				runner.files[target.Identity.Rebind.ActiveConfigFilename] = []byte("{}")
			}
			var result gatewayRebindCommitCurrentProof
			_, err := withCrossStoreFixtureEffectLocks(t, f.manager, func() (gatewayRebindPreparedAttempt, error) {
				var proofErr error
				result, proofErr = f.manager.readGatewayRebindCommitCurrentProofLocked(ctx, repository, before.Rebind, func(context.Context) error { return nil })
				return gatewayRebindPreparedAttempt{}, proofErr
			})
			if err == nil || !reflect.DeepEqual(result, gatewayRebindCommitCurrentProof{}) || len(runner.effects) != 0 {
				t.Fatal("invalid stopped current was proved or changed")
			}
		})
	}
}
