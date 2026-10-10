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

func TestGatewayRebindReleaseFailurePreservesWithdrawalUncertainty(t *testing.T) {
	for _, kind := range []string{"effects", "gateway", "both"} {
		t.Run(kind, func(t *testing.T) {
			m := &Manager{gatewayRebindFailStop: &atomic.Bool{}, gatewayRebindCommitBarrier: &atomic.Bool{}}
			m.gatewayRebindCommitBarrierLatch().Store(true)
			prior := &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
			var calls []string
			release := func(name string) func() error {
				return func() error {
					calls = append(calls, name)
					if kind == name || kind == "both" {
						return errors.New("injected release failure")
					}
					return nil
				}
			}
			err := m.releaseGatewayRebindTerminalLocks(release("effects"), release("gateway"), prior)
			var diagnostic *Error
			if !errors.Is(err, prior) || !errors.As(err, &diagnostic) || diagnostic != prior || !diagnostic.candidateMayBeLive ||
				!reflect.DeepEqual(calls, []string{"effects", "gateway"}) || !m.gatewayRebindFailStopLatch().Load() ||
				!m.gatewayRebindCommitBarrierLatch().Load() {
				t.Fatalf("release failure erased withdrawal uncertainty or admission latch: calls=%v err=%v", calls, err)
			}
		})
	}
}

func TestGatewayRebindTypedTerminalAttestationPreservesCommittedHistory(t *testing.T) {
	f, selection, _ := gatewayCurrentServingRestoreSQLFixture(t)
	ctx := context.Background()
	target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	port := f.journal.Source.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, *selection.State, *selection.State, port)
	f.manager.gatewayCurrentPhysicalDriver = managedGatewayCurrentServingExecutorDriver(
		gatewayCurrentStateFixture{manager: f.manager}, runner, port)
	driver := managerGatewayRebindCrossStoreDriver{manager: f.manager}
	before, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || before.Rebind.Active != nil || selection.State.Revision <= 1 {
		t.Fatalf("missing terminal current redeploy: %v", err)
	}
	history, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	for replay := 0; replay < 2; replay++ {
		if replay == 1 {
			f.manager = freshGatewayRebindRecoveryManager(f.manager)
			f.manager.gatewayCurrentPhysicalDriver = managedGatewayCurrentServingExecutorDriver(
				gatewayCurrentStateFixture{manager: f.manager}, runner, port)
			driver.manager = f.manager
		}
		result, err := f.manager.recoverGatewayRebindStartupWithDriver(ctx, f.repository, driver)
		if err != nil || !result.RebindHistoryPresent || result.Recovered || !result.FenceReleased ||
			!validSHA256(result.CurrentAttestationDigest) || result.CurrentStateDigest != selection.State.Digest ||
			result.CurrentStateRevision != selection.State.Revision || len(runner.effects) != 0 || !runner.container.Running ||
			f.manager.gatewayRebindAdmissionBlocked() {
			t.Fatalf("terminal attestation/replay refused current serving state: effects=%v err=%v", runner.effects, err)
		}
	}
	after, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	installed, loadErr := selection.Store.load()
	if err != nil || loadErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(*selection.State, installed) ||
		f.repository.CheckGatewayRebindFence(ctx) != nil {
		t.Fatalf("attestation changed committed authority: SQL=%v protected=%v", err, loadErr)
	}
	gatewayRebindSequenceRequireRetainedFiles(t, f.manager, history)
}

type gatewayRebindCurrentAttestationHookDriver struct {
	gatewayRebindCrossStoreDriver
	observer managerGatewayRebindCrossStoreDriver
	after    func()
}

func (d gatewayRebindCurrentAttestationHookDriver) attestCommittedCurrentLocked(ctx context.Context,
	selection gatewayCurrentSelection,
) (string, error) {
	digest, err := d.observer.attestCommittedCurrentLocked(ctx, selection)
	if err == nil && d.after != nil {
		d.after()
	}
	return digest, err
}

type gatewayRebindCurrentObservationRuntime struct {
	gatewayCurrentPhysicalRuntime
	calls int
	after func(int, *gatewayCurrentPhysicalAttestation)
}

func (r *gatewayRebindCurrentObservationRuntime) observe(ctx context.Context,
	target gatewayCurrentPhysicalTarget,
) (gatewayCurrentPhysicalAttestation, error) {
	proof, err := r.gatewayCurrentPhysicalRuntime.observe(ctx, target)
	r.calls++
	if err == nil && r.after != nil {
		r.after(r.calls, &proof)
	}
	return proof, err
}

func TestGatewayRebindCurrentAttestationCrossesSQLCommitBarrier(t *testing.T) {
	f, request, selection, _ := gatewayRebindCommittedServingFixture(t)
	ctx := context.Background()
	target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	port := f.journal.Source.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, *selection.State, *selection.State, port)
	f.manager.gatewayCurrentPhysicalDriver = managedGatewayCurrentServingExecutorDriver(
		gatewayCurrentStateFixture{manager: f.manager}, runner, port)
	history, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	// The pre-existing fixture bounds handover/forward confirmation. Both
	// current attestations use the concrete managed runtime and real SQL.
	driver := gatewayRebindCurrentAttestationHookDriver{
		gatewayRebindCrossStoreDriver: &gatewayRebindBoundedPhysicalDriver{t: t},
		observer:                      managerGatewayRebindCrossStoreDriver{manager: f.manager},
	}
	commit, err := f.manager.recoverGatewayRebindCommitTerminalLocked(ctx, f.repository, driver, request.Attempt, *request.Terminal)
	if err != nil || !commit.FenceReleased || commit.FinalPhase != appaccess.GatewayRebindCommitted ||
		!f.manager.gatewayRebindCommitBarrierLatch().Load() || f.manager.gatewayRebindFailStopLatch().Load() {
		t.Fatalf("active current attestation did not commit SQL: %v", err)
	}
	before, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || before.Rebind.Active != nil || f.repository.CheckGatewayRebindFence(ctx) != nil {
		t.Fatalf("commit did not release SQL fence: %v", err)
	}
	for replay := 0; replay < 2; replay++ {
		result, err := f.manager.recoverGatewayRebindTerminalCurrentLocked(ctx, f.repository, driver, commit)
		if err != nil || !validSHA256(result.CurrentAttestationDigest) || !result.FenceReleased || !result.Recovered ||
			result.CurrentStateDigest != selection.State.Digest || !f.manager.gatewayRebindCommitBarrierLatch().Load() ||
			f.manager.gatewayRebindFailStopLatch().Load() || len(runner.effects) != 0 || !runner.container.Running {
			t.Fatalf("terminal read crossed/cleared commit barrier: effects=%v err=%v", runner.effects, err)
		}
	}
	after, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal replay wrote SQL: %v", err)
	}
	gatewayRebindSequenceRequireRetainedFiles(t, f.manager, history)
}

func TestGatewayRebindCurrentAttestationRefusesActiveAuthorityDrift(t *testing.T) {
	f, _, selection, _ := gatewayRebindCommittedServingFixture(t)
	ctx := context.Background()
	target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	history, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"approved", "missing census", "late revocation", "different proofs", "stopped", "commit barrier", "fail-stop"} {
		t.Run(kind, func(t *testing.T) {
			f.manager.gatewayRebindFailStopLatch().Store(false)
			f.manager.gatewayRebindCommitBarrierLatch().Store(false)
			repository := &gatewayRebindCompletedRestoreRepository{Repository: f.repository}
			f.manager.options.RebindCurrentStateRepository = repository
			port := f.journal.Source.LocalHostPort
			runner := newGatewayCurrentPhysicalExecutor(t, target, *selection.State, *selection.State, port)
			physical := managedGatewayCurrentServingExecutorDriver(gatewayCurrentStateFixture{manager: f.manager}, runner, port)
			observations := &gatewayRebindCurrentObservationRuntime{gatewayCurrentPhysicalRuntime: physical.runtime}
			physical.runtime = observations
			f.manager.gatewayCurrentPhysicalDriver = physical
			switch kind {
			case "missing census":
				repository.alter = func(s *appaccess.HostingGatewayStartupSnapshot) { s.RuntimeComponents = nil }
			case "late revocation":
				observations.after = func(_ int, _ *gatewayCurrentPhysicalAttestation) {
					repository.alter = func(s *appaccess.HostingGatewayStartupSnapshot) {
						s.ActiveRebindApprovalAuthority.ConfigureApproverIsAdministrator = false
					}
				}
			case "different proofs":
				observations.after = func(call int, p *gatewayCurrentPhysicalAttestation) {
					if call == 2 {
						p.Runtime.ListenerDigest = strings.Repeat("a", 64)
						p.Runtime.Digest, _ = gatewayCurrentRuntimeProofDigest(p.Runtime)
						p.Digest, _ = gatewayCurrentPhysicalAttestationDigest(*p)
						if !validGatewayCurrentPhysicalAttestation(*p) {
							t.Fatal("drift fixture is not a valid distinct proof")
						}
					}
				}
			case "stopped":
				runner.stopAt(mustGatewayCurrentPhysicalConfig(t, *selection.State))
			case "commit barrier":
				f.manager.gatewayRebindCommitBarrierLatch().Store(true)
			case "fail-stop":
				f.manager.gatewayRebindFailStopLatch().Store(true)
			}
			digest, err := f.manager.attestGatewayRebindCurrentLocked(ctx, selection)
			if (err == nil) != (kind == "approved") || (kind == "approved" && !validSHA256(digest)) ||
				(kind != "approved" && digest != "") || len(runner.effects) != 0 || runner.container.Running != (kind != "stopped") ||
				f.manager.gatewayRebindCommitBarrierLatch().Load() != (kind == "commit barrier") ||
				f.manager.gatewayRebindFailStopLatch().Load() != (kind == "fail-stop") {
				t.Fatalf("active observation changed effects/latches or accepted drift: calls=%d effects=%v err=%v", observations.calls, runner.effects, err)
			}
			after, err := f.repository.HostingGatewayStartupSnapshot(ctx)
			if err != nil || !reflect.DeepEqual(before, after) || f.repository.CheckGatewayRebindFence(ctx) == nil {
				t.Fatalf("failed active observation changed SQL/fence: %v", err)
			}
			gatewayRebindSequenceRequireRetainedFiles(t, f.manager, history)
		})
	}
}

func TestGatewayRebindTerminalAttestationWithdrawsOnlyRetainedOwner(t *testing.T) {
	f, selection, _ := gatewayCurrentServingRestoreSQLFixture(t)
	ctx := context.Background()
	target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	history, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing census", "late LAN revocation", "cancelled observation", "stopped", "replacement container", "post-attestation SQL drift", "protected state changed"} {
		t.Run(kind, func(t *testing.T) {
			f.manager.gatewayRebindFailStopLatch().Store(false)
			f.manager.gatewayRebindCommitBarrierLatch().Store(false)
			repository := &gatewayRebindCompletedRestoreRepository{Repository: f.repository}
			f.manager.options.RebindCurrentStateRepository = repository
			port := f.journal.Source.LocalHostPort
			runner := newGatewayCurrentPhysicalExecutor(t, target, *selection.State, *selection.State, port)
			physical := managedGatewayCurrentServingExecutorDriver(gatewayCurrentStateFixture{manager: f.manager}, runner, port)
			observations := &gatewayRebindCurrentObservationRuntime{gatewayCurrentPhysicalRuntime: physical.runtime}
			physical.runtime = observations
			f.manager.gatewayCurrentPhysicalDriver = physical
			base := managerGatewayRebindCrossStoreDriver{manager: f.manager}
			driver := gatewayRebindCurrentAttestationHookDriver{gatewayRebindCrossStoreDriver: base, observer: base}
			coordinatorRepository := &gatewayRebindRecoverySnapshotDriftRepository{Repository: f.repository}
			callCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			expectedState := *selection.State
			switch kind {
			case "missing census":
				repository.alter = func(s *appaccess.HostingGatewayStartupSnapshot) { s.RuntimeComponents = nil }
			case "late LAN revocation":
				observations.after = func(_ int, _ *gatewayCurrentPhysicalAttestation) {
					repository.alter = func(s *appaccess.HostingGatewayStartupSnapshot) { s.Grants.Claims[0].ApproverIsAdministrator = false }
				}
			case "cancelled observation":
				observations.after = func(_ int, _ *gatewayCurrentPhysicalAttestation) { cancel() }
			case "stopped":
				runner.stopAt(mustGatewayCurrentPhysicalConfig(t, *selection.State))
			case "replacement container":
				runner.container.ID = strings.Repeat("8", 64)
			case "post-attestation SQL drift":
				driver.after = func() { coordinatorRepository.drift = true }
			case "protected state changed":
				observations.after = func(call int, _ *gatewayCurrentPhysicalAttestation) {
					if call != 1 {
						return
					}
					expectedState = cloneGatewayCurrentRouteState(*selection.State)
					expectedState.Revision++
					expectedState.Digest, _ = gatewayCurrentRouteStateDigest(expectedState)
					if err := selection.Store.saveNext(*selection.State, expectedState); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := f.manager.recoverGatewayRebindStartupWithDriver(callCtx, coordinatorRepository, driver)
			uncertain := kind == "replacement container" || kind == "protected state changed"
			wantEffects := [][]string(nil)
			if !uncertain && kind != "stopped" {
				wantEffects = [][]string{{"container", "stop", "--time", "10", target.Resources.FinalContainer.ID}}
			}
			var diagnostic *Error
			if err == nil || !errors.As(err, &diagnostic) || diagnostic.candidateMayBeLive != uncertain ||
				result != (GatewayRebindStartupRecoveryResult{}) || !reflect.DeepEqual(wantEffects, runner.effects) ||
				runner.container.Running != uncertain || !f.manager.gatewayRebindFailStopLatch().Load() {
				t.Fatalf("terminal failure lost exact cleanup/uncertainty: calls=%d effects=%v live=%t err=%v", observations.calls, runner.effects, runner.container.Running, err)
			}
			after, sqlErr := f.repository.GatewayRebindRecoverySnapshot(ctx)
			installed, loadErr := selection.Store.load()
			if sqlErr != nil || loadErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(expectedState, installed) ||
				f.repository.CheckGatewayRebindFence(ctx) != nil || !runner.containerPresent {
				t.Fatalf("terminal cleanup reversed SQL or changed history/resources: SQL=%v state=%v", sqlErr, loadErr)
			}
			gatewayRebindSequenceRequireRetainedFiles(t, f.manager, history)
		})
	}
}
