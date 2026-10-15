package generatedingress

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindAlternatingCurrentRepository struct {
	snapshots []appaccess.GatewayRebindRecoverySnapshot
	calls     int
}

type gatewayRebindBlockingCurrentRepository struct {
	repository GatewayRebindCurrentStateRepository
	blockAt    int
	calls      int
	observed   chan context.Context
}

func (r *gatewayRebindBlockingCurrentRepository) GatewayRebindRecoverySnapshot(ctx context.Context) (
	appaccess.GatewayRebindRecoverySnapshot, error,
) {
	r.calls++
	if r.calls == r.blockAt {
		r.observed <- ctx
		<-ctx.Done()
		return appaccess.GatewayRebindRecoverySnapshot{}, ctx.Err()
	}
	return r.repository.GatewayRebindRecoverySnapshot(ctx)
}

func (r *gatewayRebindAlternatingCurrentRepository) GatewayRebindRecoverySnapshot(context.Context) (
	appaccess.GatewayRebindRecoverySnapshot, error,
) {
	index := r.calls
	r.calls++
	if index >= len(r.snapshots) {
		index = len(r.snapshots) - 1
	}
	return r.snapshots[index], nil
}

func TestGatewayNativeAbortAuthorityRejectsSQLAndProtectedDrift(t *testing.T) {
	fixture, driver := newGatewayRebindStartupCheckpointFixture(t)
	active, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || active.Active == nil {
		t.Fatalf("active fixture snapshot=%#v error=%v", active, err)
	}
	fresh := freshGatewayRebindRecoveryManager(fixture.manager)
	result, err := fresh.recoverGatewayRebindStartupWithDriver(context.Background(), fixture.repository,
		&gatewayRebindBoundedPhysicalDriver{t: t, template: driver.template})
	if err != nil || result.Disposition != appaccess.GatewayRebindDispositionAbort || !result.FenceReleased {
		t.Fatalf("prepared abort recovery=%#v error=%v", result, err)
	}
	stable, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || stable.Active != nil || stable.CurrentSource == nil {
		t.Fatalf("stable fixture snapshot=%#v error=%v", stable, err)
	}
	if present, err := fresh.HasGatewayV2LANRecoveryBatch(context.Background()); err != nil || present {
		t.Fatalf("valid native abort authority presence=%v error=%v", present, err)
	}
	original := fresh.options.RebindCurrentStateRepository
	t.Cleanup(func() { fresh.options.RebindCurrentStateRepository = original })

	wrongAuthority := stable
	changedAuthority := *stable.CurrentSource
	changedAuthority.OperationID = uuid.NewString()
	wrongAuthority.CurrentSource = &changedAuthority
	stale := stable
	changedDigest := *stable.CurrentSource
	changedDigest.ProfileSpecDigest = strings.Repeat("f", 64)
	stale.CurrentSource = &changedDigest
	for _, test := range []struct {
		name       string
		repository GatewayRebindCurrentStateRepository
	}{
		{name: "wrong native authority", repository: &gatewayRebindProposalRepositoryFake{snapshot: wrongAuthority}},
		{name: "active SQL attempt", repository: &gatewayRebindProposalRepositoryFake{snapshot: active}},
		{name: "stale SQL reads", repository: &gatewayRebindAlternatingCurrentRepository{
			snapshots: []appaccess.GatewayRebindRecoverySnapshot{stable, stale},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fresh.options.RebindCurrentStateRepository = test.repository
			present, inspectErr := fresh.HasGatewayV2LANRecoveryBatch(context.Background())
			if inspectErr == nil || present || !IsCode(inspectErr, DiagnosticRouteUnresolved) {
				t.Fatalf("unbound native authority was accepted: present=%v error=%v", present, inspectErr)
			}
		})
	}
	fresh.options.RebindCurrentStateRepository = original

	orphan := newGatewayCurrentStateFixture(t)
	orphanStore, err := newGatewayCurrentRouteStateStore(fresh.options.DataRoot, orphan.baseline.Lineage)
	if err != nil {
		t.Fatal(err)
	}
	if err := orphanStore.installBaseline(orphan.baseline); err != nil {
		t.Fatal(err)
	}
	present, inspectErr := fresh.HasGatewayV2LANRecoveryBatch(context.Background())
	if inspectErr == nil || present || !IsCode(inspectErr, DiagnosticRouteUnresolved) {
		t.Fatalf("orphan current bundle was accepted: present=%v error=%v", present, inspectErr)
	}
	retained, err := orphanStore.load()
	if err != nil || !reflect.DeepEqual(retained, orphan.baseline) {
		t.Fatalf("orphan refusal changed protected current bundle: state=%#v error=%v", retained, err)
	}
}

func TestGatewayNativeAbortAuthorityRejectsCommittedTerminal(t *testing.T) {
	fixture, snapshot, _, _ := gatewayRebindCurrentStartupFixture(t)
	repository := &gatewayRebindProposalRepositoryFake{snapshot: snapshot}
	current, err := fixture.manager.InspectGatewayRebindCurrent(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	history, err := fixture.manager.scanGatewayRebindCompletedProtectedHistoryLocked()
	if err != nil || len(history.Terminals)+len(history.TerminalsV2) == 0 {
		t.Fatalf("committed terminal census=%d/%d error=%v", len(history.Terminals), len(history.TerminalsV2), err)
	}
	lineage, err := gatewayUpgradeCurrentLineage(history.Predecessor)
	if err != nil {
		t.Fatal(err)
	}
	current.SelectedCurrentAuthority = gatewayCurrentAuthority(lineage)
	current.SelectedCurrentEvidence.Lineage = lineage
	if gatewayRebindNativeAbortHistoryMatchesCurrent(history, current) {
		t.Fatal("non-ABORT terminal history was accepted as native authority")
	}
}

func TestGatewayNativeAbortAuthorityCallersBoundSQLReadsWhileLocked(t *testing.T) {
	prepare := func(t *testing.T) (*Manager, GatewayRebindCurrentStateRepository, gatewayV2RouteState) {
		t.Helper()
		fixture, driver := newGatewayRebindStartupCheckpointFixture(t)
		fresh := freshGatewayRebindRecoveryManager(fixture.manager)
		result, err := fresh.recoverGatewayRebindStartupWithDriver(context.Background(), fixture.repository,
			&gatewayRebindBoundedPhysicalDriver{t: t, template: driver.template})
		if err != nil || result.Disposition != appaccess.GatewayRebindDispositionAbort || !result.FenceReleased {
			t.Fatalf("prepared abort recovery=%#v error=%v", result, err)
		}
		return fresh, fixture.repository, fixture.state
	}
	assertDeadline := func(t *testing.T, observed <-chan context.Context, maximum time.Duration) {
		t.Helper()
		select {
		case ctx := <-observed:
			deadline, ok := ctx.Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining <= 0 || remaining > maximum {
				t.Errorf("SQL read context deadline present=%t remaining=%s maximum=%s", ok, remaining, maximum)
			}
		case <-time.After(5 * time.Second):
			t.Error("SQL read did not reach the blocking repository")
		}
	}

	t.Run("observation selection", func(t *testing.T) {
		manager, repository, _ := prepare(t)
		blocking := &gatewayRebindBlockingCurrentRepository{
			repository: repository, blockAt: 1, observed: make(chan context.Context, 1),
		}
		manager.options.RebindCurrentStateRepository = blocking
		callerCtx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		callbackInvoked := make(chan struct{}, 1)
		go func() {
			done <- manager.WithObservation(callerCtx, upgradeTestAppA, func(context.Context, Observation) error {
				callbackInvoked <- struct{}{}
				return nil
			})
		}()
		assertDeadline(t, blocking.observed, observationTimeout)
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Error("canceled observation returned no error")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("observation did not stop after caller cancellation")
		}
		select {
		case <-callbackInvoked:
			t.Error("observation callback ran after the SQL read was canceled")
		default:
		}
	})

	t.Run("LAN commit resolution", func(t *testing.T) {
		manager, repository, state := prepare(t)
		// The current-state selector consumes three stable SQL reads before
		// committedV2Locked reaches the retained native-ABORT fallback.
		blocking := &gatewayRebindBlockingCurrentRepository{
			repository: repository, blockAt: 4, observed: make(chan context.Context, 1),
		}
		manager.options.RebindCurrentStateRepository = blocking
		callerCtx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		callbackInvoked := make(chan struct{}, 1)
		request := gatewayV2LANGrantRequestForState(t, state)
		go func() {
			done <- manager.withGatewayV2LANCommitResolution(callerCtx, request,
				func(context.Context, GatewayV2LANGrantObservation) error {
					callbackInvoked <- struct{}{}
					return nil
				})
		}()
		assertDeadline(t, blocking.observed, v2ObservationTimeout)
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Error("canceled LAN commit resolution returned no error")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("LAN commit resolution did not stop after caller cancellation")
		}
		select {
		case <-callbackInvoked:
			t.Error("LAN commit callback ran after the SQL read was canceled")
		default:
		}
	})
}
