package generatedingress

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

type committedCapacityAuthorityFixture struct {
	fixture   gatewayRebindPredecessorFixture
	selection gatewayCurrentSelection
	terminal  gatewayRebindAttemptTerminalView
	sql       appaccess.GatewayRebindRecoverySnapshot
}

type capacityCurrentPhysicalDriver struct {
	gatewayCurrentPhysicalDriver
	attest func(context.Context, gatewayCurrentSelection) (gatewayCurrentPhysicalAttestation, error)
}

func (d capacityCurrentPhysicalDriver) attestGatewayCurrentPhysical(ctx context.Context,
	selection gatewayCurrentSelection,
) (gatewayCurrentPhysicalAttestation, error) {
	return d.attest(ctx, selection)
}

func newCommittedCapacityAuthorityFixture(t *testing.T) committedCapacityAuthorityFixture {
	t.Helper()
	ctx := context.Background()
	fixture, input, commitDriver := newGatewayRebindCoordinatorFixture(t)
	result, err := fixture.manager.commitGatewayRebindWithDriver(ctx, fixture.repository, input, commitDriver)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != appaccess.GatewayRebindDispositionCommit || !result.FenceReleased ||
		result.OperationID != input.Inspection.Spec.OperationID || !validSHA256(result.TerminalReceiptDigest) {
		t.Fatalf("completed rebind did not commit exact current authority: %#v", result)
	}

	sql, err := fixture.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sql.Active != nil || sql.CurrentSource == nil ||
		sql.CurrentSource.Kind != appaccess.GatewayRebindSourceGatewayRebind ||
		sql.CurrentSource.OperationID != result.OperationID ||
		sql.CurrentSource.TerminalReceiptDigest != result.TerminalReceiptDigest {
		t.Fatalf("committed SQL current authority is incomplete: %#v", sql)
	}
	selection, err := fixture.manager.selectGatewayCurrentLocked(ctx, sql)
	if err != nil || selection.Kind != gatewayCurrentSelectionRebind || selection.State == nil ||
		selection.State.Pending != nil || selection.State.LANRecovery != nil {
		t.Fatalf("committed selected current is not stable: selection=%#v error=%v", selection, err)
	}
	terminal, err := gatewayCurrentSelectionTerminalView(selection)
	if err != nil || terminal.Disposition != gatewayRebindFinalHandoverTerminalCommit ||
		terminal.Resources.FinalContainer == nil {
		t.Fatalf("committed selected current terminal is incomplete: terminal=%#v error=%v", terminal, err)
	}
	return committedCapacityAuthorityFixture{fixture: fixture, selection: selection, terminal: terminal, sql: sql}
}

func (f committedCapacityAuthorityFixture) installStablePhysicalDriver(t *testing.T) *gatewayCurrentStateMachineDriver {
	t.Helper()
	driver := &gatewayCurrentStateMachineDriver{t: t, terminal: f.terminal}
	f.fixture.manager.gatewayCurrentPhysicalDriver = driver
	return driver
}

func (f committedCapacityAuthorityFixture) requireSnapshotRefusalWithoutProbe(t *testing.T) {
	t.Helper()
	before := len(f.fixture.runner.commands)
	if _, err := f.fixture.manager.Snapshot(context.Background()); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("capacity snapshot error=%v want=%s", err, DiagnosticRouteUnresolved)
	}
	if commands := f.fixture.runner.commands[before:]; len(commands) != 0 {
		t.Fatalf("refused capacity snapshot issued Docker commands: %v", commands)
	}
}

func TestGeneratedIngressCapacitySnapshotUsesCommittedRebindCurrentAuthority(t *testing.T) {
	ctx := context.Background()
	f := newCommittedCapacityAuthorityFixture(t)
	driver := f.installStablePhysicalDriver(t)

	beforeProtected, err := readGatewayHistorySnapshotMode(f.fixture.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	beforeCommandCount := len(f.fixture.runner.commands)
	snapshot, err := f.fixture.manager.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.MemoryAvailableBytes != 2147483648 || snapshot.DiskAvailableBytes != 8589934592 {
		t.Fatalf("capacity snapshot = %#v", snapshot)
	}
	wantCommand := []string{
		"container", "exec", f.terminal.Resources.FinalContainer.ID,
		"sh", "-c", capacityProbeCommand,
	}
	commands := f.fixture.runner.commands[beforeCommandCount:]
	if driver.attestCalls != 1 || len(commands) != 1 || !reflect.DeepEqual(commands[0], wantCommand) {
		t.Fatalf("capacity authority attestation=%d commands=%v want=%v", driver.attestCalls, commands, wantCommand)
	}

	afterSQL, err := f.fixture.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterProtected, err := readGatewayHistorySnapshotMode(f.fixture.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.sql, afterSQL) || !sameGatewayHistorySnapshot(beforeProtected, afterProtected) {
		t.Fatal("capacity snapshot mutated committed current authority")
	}
}

func TestGeneratedIngressCapacitySnapshotRefusesUnstableRebindCurrentAuthority(t *testing.T) {
	t.Run("active newer rebind", func(t *testing.T) {
		f := newCommittedCapacityAuthorityFixture(t)
		active := appaccess.GatewayRebindHistoryEntry{Claim: appaccess.GatewayRebindClaimRecord{
			SpecVersion: appaccess.GatewayRebindSpecVersionV2,
			V2: &appaccess.GatewayRebindClaimV2{Spec: appaccess.GatewayRebindSpecV2{
				OperationID: uuid.NewString(),
			}},
		}}
		snapshot := f.sql
		snapshot.Active = &active
		snapshot.Phase = appaccess.GatewayRebindPrepared
		if selected, err := f.fixture.manager.selectGatewayCurrentLocked(context.Background(), snapshot); err != nil ||
			selected.Kind != gatewayCurrentSelectionRebind || selected.State == nil {
			t.Fatalf("active rebind lost its prior selected current: selection=%#v error=%v", selected, err)
		}
		f.fixture.manager.options.RebindCurrentStateRepository = &routeOperationSnapshotRepository{
			snapshots: []appaccess.GatewayRebindRecoverySnapshot{snapshot},
		}
		f.installStablePhysicalDriver(t)
		f.requireSnapshotRefusalWithoutProbe(t)
	})

	t.Run("SQL current head drift", func(t *testing.T) {
		f := newCommittedCapacityAuthorityFixture(t)
		drifted := f.sql
		source := *drifted.CurrentSource
		source.ProfileSpecDigest = strings.Repeat("f", 64)
		drifted.CurrentSource = &source
		f.fixture.manager.options.RebindCurrentStateRepository = &routeOperationSnapshotRepository{
			snapshots: []appaccess.GatewayRebindRecoverySnapshot{f.sql, f.sql, drifted},
		}
		f.installStablePhysicalDriver(t)
		f.requireSnapshotRefusalWithoutProbe(t)
	})

	t.Run("protected terminal mismatch", func(t *testing.T) {
		f := newCommittedCapacityAuthorityFixture(t)
		mismatched := f.sql
		source := *mismatched.CurrentSource
		source.TerminalReceiptDigest = strings.Repeat("f", 64)
		mismatched.CurrentSource = &source
		f.fixture.manager.options.RebindCurrentStateRepository = &routeOperationSnapshotRepository{
			snapshots: []appaccess.GatewayRebindRecoverySnapshot{mismatched},
		}
		f.installStablePhysicalDriver(t)
		f.requireSnapshotRefusalWithoutProbe(t)
	})

	t.Run("pending route", func(t *testing.T) {
		f := newCommittedCapacityAuthorityFixture(t)
		before := cloneGatewayCurrentRouteState(*f.selection.State)
		appID, app := routeOperationTransferredApp(t, before)
		transition, err := gatewayCurrentSwitchTransition(before, routeOperationSwitchRequest(t, appID, app.Route))
		if err != nil {
			t.Fatalf("install pending route: %v", err)
		}
		if err := f.selection.Store.saveNext(before, transition.Pending); err != nil {
			t.Fatalf("persist pending route: %v", err)
		}
		f.installStablePhysicalDriver(t)
		f.requireSnapshotRefusalWithoutProbe(t)
	})

	t.Run("LAN recovery batch", func(t *testing.T) {
		fixture, _, _, _, batch := gatewayCurrentLANBatchObservationFixture(t)
		if batch.LANRecovery == nil || batch.Pending != nil {
			t.Fatalf("LAN recovery fixture is not isolated: %#v", batch)
		}
		runner := &gatewayCurrentNoCommandRunner{}
		fixture.manager.runner = runner
		if _, err := fixture.manager.Snapshot(context.Background()); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("capacity snapshot error=%v want=%s", err, DiagnosticRouteUnresolved)
		}
		if runner.calls != 0 {
			t.Fatalf("LAN recovery refusal issued %d Docker commands", runner.calls)
		}
	})

	t.Run("foreign physical binding", func(t *testing.T) {
		f := newCommittedCapacityAuthorityFixture(t)
		base := &gatewayCurrentStateMachineDriver{t: t, terminal: f.terminal}
		f.fixture.manager.gatewayCurrentPhysicalDriver = capacityCurrentPhysicalDriver{
			gatewayCurrentPhysicalDriver: base,
			attest: func(ctx context.Context, selection gatewayCurrentSelection) (gatewayCurrentPhysicalAttestation, error) {
				value, err := base.attestGatewayCurrentPhysical(ctx, selection)
				if err != nil {
					return gatewayCurrentPhysicalAttestation{}, err
				}
				foreign := *value.Resources.FinalContainer
				foreign.ID = strings.Repeat("f", 64)
				value.Resources.FinalContainer = &foreign
				value.Digest, _ = gatewayCurrentPhysicalAttestationDigest(value)
				return value, nil
			},
		}
		f.requireSnapshotRefusalWithoutProbe(t)
	})

	for _, test := range []struct {
		name   string
		mutate func(*gatewayCurrentPhysicalAttestation)
	}{
		{
			name: "ambiguous physical outcome",
			mutate: func(value *gatewayCurrentPhysicalAttestation) {
				value.Outcome = gatewayCurrentPhysicalRecoveryMixed
			},
		},
		{
			name: "non-serving physical outcome",
			mutate: func(value *gatewayCurrentPhysicalAttestation) {
				value.Outcome = gatewayCurrentPhysicalRecoveryStopped
				value.Runtime.ListenerAbsent = true
				value.Runtime.Digest, _ = gatewayCurrentRuntimeProofDigest(value.Runtime)
			},
		},
		{
			name: "missing final container",
			mutate: func(value *gatewayCurrentPhysicalAttestation) {
				value.Resources.FinalContainer = nil
			},
		},
		{
			name: "malformed final container",
			mutate: func(value *gatewayCurrentPhysicalAttestation) {
				final := *value.Resources.FinalContainer
				final.ID = "malformed"
				value.Resources.FinalContainer = &final
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCommittedCapacityAuthorityFixture(t)
			base := &gatewayCurrentStateMachineDriver{t: t, terminal: f.terminal}
			f.fixture.manager.gatewayCurrentPhysicalDriver = capacityCurrentPhysicalDriver{
				gatewayCurrentPhysicalDriver: base,
				attest: func(ctx context.Context, selection gatewayCurrentSelection) (gatewayCurrentPhysicalAttestation, error) {
					value, err := base.attestGatewayCurrentPhysical(ctx, selection)
					if err != nil {
						return gatewayCurrentPhysicalAttestation{}, err
					}
					test.mutate(&value)
					value.Digest, _ = gatewayCurrentPhysicalAttestationDigest(value)
					return value, nil
				},
			}
			f.requireSnapshotRefusalWithoutProbe(t)
		})
	}
}

func TestGeneratedIngressCapacitySnapshotPreservesNativeAbortPredecessorAuthority(t *testing.T) {
	ctx := context.Background()
	aborted := newGatewayRebindAbortedTerminalFixture(t)
	sql, err := aborted.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || sql.CurrentSource == nil ||
		sql.CurrentSource.Kind != appaccess.GatewayRebindSourceGatewayUpgrade || sql.Active != nil {
		t.Fatalf("native abort predecessor SQL is incomplete: snapshot=%#v error=%v", sql, err)
	}
	aborted.manager.gatewayTopologyObserver = func(context.Context, routeState, gatewayV2RouteState,
		gatewayMigrationJournal,
	) gatewayObservedTopology {
		return gatewayTopologyExactFinalV2
	}
	aborted.manager.runner = aborted.fixture.predecessor.runner
	before := len(aborted.fixture.predecessor.runner.commands)
	snapshot, err := aborted.manager.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.MemoryAvailableBytes != 2147483648 || snapshot.DiskAvailableBytes != 8589934592 {
		t.Fatalf("capacity snapshot = %#v", snapshot)
	}
	want := []string{"container", "exec", aborted.fixture.predecessor.state.Identity.FinalContainer,
		"sh", "-c", capacityProbeCommand}
	commands := aborted.fixture.predecessor.runner.commands[before:]
	if len(commands) != 1 || !reflect.DeepEqual(commands[0], want) {
		t.Fatalf("native abort capacity commands=%v want=%v", commands, want)
	}
}
