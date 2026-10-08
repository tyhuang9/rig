package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	"github.com/hostd/hostd/internal/runtime/docker"
)

type gatewayStartupRecoveryFixture struct {
	presence                        generatedingress.GatewayRebindStartupPresence
	before                          generatedingress.GatewayRebindCurrentInspection
	after                           generatedingress.GatewayRebindCurrentInspection
	result                          generatedingress.GatewayRebindStartupRecoveryResult
	calls                           []string
	readErr, restoreErr, recoverErr error
	unhandled                       bool
}

func newGatewayStartupRecoveryFixture(active bool) *gatewayStartupRecoveryFixture {
	f := &gatewayStartupRecoveryFixture{}
	source := appaccess.GatewayCurrentAuthorityRef{Kind: appaccess.GatewayRebindSourceGatewayRebind,
		OperationID: uuid.NewString(), ProfileRevisionID: uuid.NewString(), ProfileRevisionNumber: 2,
		ProfileSpecDigest: strings.Repeat("1", 64), TerminalReceiptDigest: strings.Repeat("2", 64)}
	f.before = generatedingress.GatewayRebindCurrentInspection{SelectedCurrentAuthority: source,
		CurrentStateVersion: 1, CurrentStateRevision: 1, CurrentStateDigest: strings.Repeat("3", 64),
		CurrentRecoveryMode: generatedingress.GatewayCurrentRecoveryStable, FenceReleased: true}
	f.after = f.before
	f.presence = generatedingress.GatewayRebindStartupPresence{Present: true, SelectedCurrentAuthority: &source}
	f.result = generatedingress.GatewayRebindStartupRecoveryResult{RebindHistoryPresent: true,
		SelectedCurrentAuthority: &source, CurrentStateVersion: 1, CurrentStateRevision: 1,
		CurrentStateDigest: f.before.CurrentStateDigest, CurrentAttestationDigest: strings.Repeat("4", 64), FenceReleased: true}
	if active {
		f.before.ActiveOperationID, f.before.ActiveSpecVersion = uuid.NewString(), appaccess.GatewayRebindSpecVersionV2
		f.before.ActivePhase, f.before.FenceReleased = appaccess.GatewayRebindPrepared, false
		f.presence.ActiveOperationID, f.presence.ActivePhase = f.before.ActiveOperationID, f.before.ActivePhase
		f.result.Recovered, f.result.ActiveOperationID = true, f.before.ActiveOperationID
		f.result.InitialActivePhase, f.result.FinalActivePhase = f.before.ActivePhase, appaccess.GatewayRebindRolledBack
		f.result.Disposition, f.result.TerminalReceiptDigest = appaccess.GatewayRebindDispositionAbort, strings.Repeat("5", 64)
		f.after.Retained = []generatedingress.GatewayRebindRetainedOperationInspection{{
			OperationID: f.before.ActiveOperationID, Disposition: f.result.Disposition, TerminalReceiptDigest: f.result.TerminalReceiptDigest}}
	}
	return f
}

func (f *gatewayStartupRecoveryFixture) callbacks() gatewayRebindStartupRecovery {
	reads := 0
	return gatewayRebindStartupRecovery{
		inspect: func(context.Context) (generatedingress.GatewayRebindCurrentInspection, error) {
			f.calls = append(f.calls, "inspect")
			reads++
			if reads == 1 {
				return f.before, f.readErr
			}
			return f.after, f.readErr
		},
		restore: func(context.Context) (bool, error) {
			f.calls = append(f.calls, "restore")
			return !f.unhandled, f.restoreErr
		},
		recover: func(context.Context) (generatedingress.GatewayRebindStartupRecoveryResult, error) {
			f.calls = append(f.calls, "recover")
			return f.result, f.recoverErr
		},
	}
}

func (f *gatewayStartupRecoveryFixture) nativePredecessor() {
	source := f.before.SelectedCurrentAuthority
	source.Kind, source.TerminalReceiptDigest = appaccess.GatewayRebindSourceGatewayUpgrade, ""
	f.before.SelectedCurrentAuthority, f.after.SelectedCurrentAuthority = source, source
	f.presence.SelectedCurrentAuthority, f.result.SelectedCurrentAuthority = &source, &source
	f.before.CurrentRecoveryMode, f.after.CurrentRecoveryMode = generatedingress.GatewayCurrentRecoveryNative, generatedingress.GatewayCurrentRecoveryNative
	f.before.CurrentStateVersion, f.after.CurrentStateVersion = 2, 2
	f.before.CurrentStateRevision, f.after.CurrentStateRevision = 0, 0
	f.result.CurrentStateVersion, f.result.CurrentStateRevision = 0, 0
	f.result.CurrentStateDigest, f.result.CurrentAttestationDigest = "", ""
}

func TestGatewayRebindStartupDispatchPreservesDedicatedRecovery(t *testing.T) {
	for _, mode := range []generatedingress.GatewayCurrentRecoveryMode{
		generatedingress.GatewayCurrentRecoveryStable, generatedingress.GatewayCurrentRecoveryNative,
		generatedingress.GatewayCurrentRecoveryRoute, generatedingress.GatewayCurrentRecoveryLAN,
		generatedingress.GatewayCurrentRecoveryLANBatch, generatedingress.GatewayCurrentRecoveryLANBatchDone,
	} {
		t.Run(string(mode), func(t *testing.T) {
			f := newGatewayStartupRecoveryFixture(false)
			f.before.CurrentRecoveryMode, f.after.CurrentRecoveryMode = mode, mode
			if mode == generatedingress.GatewayCurrentRecoveryNative {
				f.nativePredecessor()
			}
			want := []string{"inspect"}
			if mode == generatedingress.GatewayCurrentRecoveryStable {
				want = []string{"inspect", "restore", "recover", "inspect"}
			}
			if mode == generatedingress.GatewayCurrentRecoveryLANBatchDone {
				want = []string{"inspect", "restore", "inspect"}
			}
			got, err := prepareGatewayRebindRecovery(context.Background(), f.presence, f.callbacks())
			if err != nil || !reflect.DeepEqual(got, f.before) || !reflect.DeepEqual(f.calls, want) {
				t.Fatalf("dispatch=%v inspection=%+v error=%v", f.calls, got, err)
			}
		})
	}
}

func TestGatewayRebindStartupActiveResultAndLeaseOrdering(t *testing.T) {
	for _, name := range []string{"abort rebound", "abort native", "commit prepared", "commit successor ready", "commit database committed"} {
		t.Run(name, func(t *testing.T) {
			f := newGatewayStartupRecoveryFixture(true)
			if name == "abort native" {
				f.nativePredecessor()
			}
			if strings.HasPrefix(name, "commit") {
				f.result.FinalActivePhase, f.result.Disposition = appaccess.GatewayRebindCommitted, appaccess.GatewayRebindDispositionCommit
				source := f.after.SelectedCurrentAuthority
				source.OperationID, source.TerminalReceiptDigest = f.before.ActiveOperationID, f.result.TerminalReceiptDigest
				f.after.SelectedCurrentAuthority, f.result.SelectedCurrentAuthority = source, &source
				f.after.Retained[0].Disposition = f.result.Disposition
				if name == "commit successor ready" {
					f.before.ActivePhase = appaccess.GatewayRebindSuccessorReady
				}
				if name == "commit database committed" {
					f.before.ActivePhase = appaccess.GatewayRebindDatabaseCommitted
				}
				f.presence.ActivePhase, f.result.InitialActivePhase = f.before.ActivePhase, f.before.ActivePhase
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			directories, err := docker.PrepareControllerDirectories(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			callbacks := f.callbacks()
			recover := callbacks.recover
			callbacks.recover = func(ctx context.Context) (generatedingress.GatewayRebindStartupRecoveryResult, error) {
				release, err := deploymenteffects.Acquire(ctx, directories.WorkingDirectory)
				if err != nil {
					t.Fatal(err)
				}
				value, err := recover(ctx)
				return value, errors.Join(err, release())
			}
			got, err := prepareGatewayRebindRecovery(ctx, f.presence, callbacks)
			if err != nil || !reflect.DeepEqual(got, f.after) || !reflect.DeepEqual(f.calls, []string{"inspect", "recover", "inspect"}) {
				t.Fatalf("active dispatch=%v result=%+v error=%v", f.calls, got, err)
			}
			// Ordinary admission can acquire the same lease only after recovery
			// has returned. A leaked recovery lease fails this bounded check.
			release, err := deploymenteffects.Acquire(ctx, directories.WorkingDirectory)
			if err != nil {
				t.Fatal(err)
			}
			if err := release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGatewayRebindStartupRefusesUnverifiedRecovery(t *testing.T) {
	for _, test := range []struct {
		name   string
		active bool
		mutate func(*gatewayStartupRecoveryFixture)
	}{
		{"presence drift", true, func(f *gatewayStartupRecoveryFixture) { f.presence.ActiveOperationID = uuid.NewString() }},
		{"legacy active", true, func(f *gatewayStartupRecoveryFixture) { f.before.ActiveSpecVersion = 1 }},
		{"unknown active version", true, func(f *gatewayStartupRecoveryFixture) { f.before.ActiveSpecVersion = 99 }},
		{"unknown active phase", true, func(f *gatewayStartupRecoveryFixture) {
			f.before.ActivePhase = "unknown"
			f.presence.ActivePhase = "unknown"
		}},
		{"active fence already released", true, func(f *gatewayStartupRecoveryFixture) { f.before.FenceReleased = true }},
		{"unreleased terminal fence", false, func(f *gatewayStartupRecoveryFixture) { f.before.FenceReleased = false }},
		{"stray active format", false, func(f *gatewayStartupRecoveryFixture) { f.before.ActiveSpecVersion = 2 }},
		{"unknown current mode", false, func(f *gatewayStartupRecoveryFixture) { f.before.CurrentRecoveryMode = "unknown" }},
		{"read error", true, func(f *gatewayStartupRecoveryFixture) { f.readErr = errors.New("read failed") }},
		{"restore unhandled", false, func(f *gatewayStartupRecoveryFixture) { f.unhandled = true }},
		{"restore release failure", false, func(f *gatewayStartupRecoveryFixture) { f.restoreErr = errors.New("release failed") }},
		{"recovery release failure", true, func(f *gatewayStartupRecoveryFixture) { f.recoverErr = errors.New("release failed") }},
		{"history absent", true, func(f *gatewayStartupRecoveryFixture) { f.result.RebindHistoryPresent = false }},
		{"no recovery", true, func(f *gatewayStartupRecoveryFixture) { f.result.Recovered = false }},
		{"wrong recovered identity", true, func(f *gatewayStartupRecoveryFixture) { f.result.ActiveOperationID = uuid.NewString() }},
		{"wrong initial phase", true, func(f *gatewayStartupRecoveryFixture) {
			f.result.InitialActivePhase = appaccess.GatewayRebindSuccessorReady
		}},
		{"wrong terminal pair", true, func(f *gatewayStartupRecoveryFixture) {
			f.result.Disposition = appaccess.GatewayRebindDispositionCommit
		}},
		{"database commit cannot abort", true, func(f *gatewayStartupRecoveryFixture) {
			f.before.ActivePhase = appaccess.GatewayRebindDatabaseCommitted
			f.presence.ActivePhase = f.before.ActivePhase
			f.result.InitialActivePhase = f.before.ActivePhase
		}},
		{"bad receipt digest", true, func(f *gatewayStartupRecoveryFixture) { f.result.TerminalReceiptDigest = "bad" }},
		{"missing retained receipt", true, func(f *gatewayStartupRecoveryFixture) { f.after.Retained = nil }},
		{"wrong current revision", true, func(f *gatewayStartupRecoveryFixture) { f.result.CurrentStateRevision++ }},
		{"abort rebound predecessor drift", true, func(f *gatewayStartupRecoveryFixture) {
			f.after.CurrentStateRevision++
			f.after.CurrentStateDigest = strings.Repeat("a", 64)
			f.result.CurrentStateRevision, f.result.CurrentStateDigest = f.after.CurrentStateRevision, f.after.CurrentStateDigest
		}},
		{"abort native predecessor drift", true, func(f *gatewayStartupRecoveryFixture) {
			f.nativePredecessor()
			f.after.CurrentStateDigest = strings.Repeat("a", 64)
		}},
		{"invalid attestation", true, func(f *gatewayStartupRecoveryFixture) { f.result.CurrentAttestationDigest = "bad" }},
		{"post recovery active", true, func(f *gatewayStartupRecoveryFixture) { f.after.ActiveOperationID = uuid.NewString() }},
		{"post recovery fence", true, func(f *gatewayStartupRecoveryFixture) { f.after.FenceReleased = false }},
		{"stable state drift", false, func(f *gatewayStartupRecoveryFixture) { f.after.CurrentStateRevision++ }},
		{"completed batch cleared", false, func(f *gatewayStartupRecoveryFixture) {
			f.before.CurrentRecoveryMode = generatedingress.GatewayCurrentRecoveryLANBatchDone
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newGatewayStartupRecoveryFixture(test.active)
			test.mutate(f)
			got, err := prepareGatewayRebindRecovery(context.Background(), f.presence, f.callbacks())
			if err == nil || !reflect.DeepEqual(got, generatedingress.GatewayRebindCurrentInspection{}) {
				t.Fatalf("unverified recovery returned %+v error=%v", got, err)
			}
		})
	}
	f := newGatewayStartupRecoveryFixture(true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareGatewayRebindRecovery(ctx, f.presence, f.callbacks()); err == nil || len(f.calls) != 0 {
		t.Fatalf("canceled preparation performed work: %v %v", f.calls, err)
	}
}

func TestGatewayRebindStartupFreshDatabaseAndManagerReuse(t *testing.T) {
	f := newRuntimeCompositionFixture(t)
	f.configuration.GeneratedRuntime = true
	directories, err := docker.PrepareControllerDirectories(f.configuration.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareGatewayRebindStartup(context.Background(), f.configuration, f.db, f.dockerExecutable, directories)
	if err != nil || prepared != nil {
		t.Fatalf("fresh preparation=%+v error=%v", prepared, err)
	}
	admit, err := deploymentEffectsAdmission(f.db, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	release, err := admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	}()
	gate, err := inspectGatewayStartupAfterPreparation(context.Background(), f.configuration, f.db, f.dockerExecutable, directories, prepared)
	if err != nil || gate.ingress == nil {
		t.Fatalf("gate=%+v error=%v", gate, err)
	}
	reused, err := inspectGatewayStartupUsingIngress(context.Background(), f.configuration, f.db,
		f.dockerExecutable, directories, rebindFenceCheck(f.db), gate.ingress)
	if err != nil || reused.ingress != gate.ingress {
		t.Fatalf("ordinary inspection replaced the supplied manager: %v", err)
	}
	calls := 0
	runtime, err := prepareRuntimeComposition(context.Background(), f.configuration, f.dependencies, runtimeCompositionOptions{
		dockerExecutable: f.dockerExecutable, preinspectedIngress: gate.ingress,
		recoverIngress: func(_ context.Context, ingress *generatedingress.Manager) error {
			calls++
			if ingress != gate.ingress {
				t.Fatal("ordinary recovery replaced the inspected manager")
			}
			return nil
		},
	})
	if err != nil || calls != 1 || runtime.ingress != gate.ingress {
		t.Fatalf("manager reuse: calls=%d error=%v", calls, err)
	}
	// A fabricated checkpoint cannot cause the same manager to skip a fresh
	// protected/SQL selection after admission.
	_, err = inspectGatewayStartupAfterPreparation(context.Background(), f.configuration, f.db, f.dockerExecutable, directories,
		&gatewayRebindStartupPreparation{ingress: gate.ingress, current: newGatewayStartupRecoveryFixture(false).before})
	if err == nil {
		t.Fatal("accepted a stale preparation checkpoint")
	}
}
