package generatedingress

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedruntime"
)

func TestIndependentManagersDoNoGatewayWorkWhileAnotherHoldsTheLock(t *testing.T) {
	first, runner := newManagerFixture(t, false)
	second, err := New(runner, first.options)
	if err != nil {
		t.Fatal(err)
	}
	release, err := first.lockGateway(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	}()
	before := len(runner.commands)
	methods := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "provision", run: second.Provision},
		{name: "switch", run: func(ctx context.Context) error {
			return second.Switch(ctx, switchRequest(runner))
		}},
		{name: "observation", run: func(ctx context.Context) error {
			return second.WithObservation(ctx, runner.appID, func(context.Context, Observation) error {
				t.Error("observation callback ran without the gateway lock")
				return nil
			})
		}},
		{name: "capacity", run: func(ctx context.Context) error {
			_, err := second.Snapshot(ctx)
			return err
		}},
	}
	for _, method := range methods {
		t.Run(method.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
			defer cancel()
			if err := method.run(ctx); !IsCode(err, DiagnosticCancelled) {
				t.Fatalf("competing method error = %v, want cancelled", err)
			}
			if len(runner.commands) != before {
				t.Fatalf("competing method issued %d Docker commands", len(runner.commands)-before)
			}
		})
	}
}

func TestSwitchReleaseFailurePreservesLiveCandidate(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	original := managerAcquireGatewayOSLock
	managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
		release, err := original(ctx, store)
		if err != nil {
			return nil, err
		}
		return func() error {
			if err := release(); err != nil {
				return err
			}
			return errors.New("injected post-switch release failure")
		}, nil
	}
	t.Cleanup(func() { managerAcquireGatewayOSLock = original })

	err := manager.Switch(context.Background(), switchRequest(runner))
	if !IsCode(err, DiagnosticRouteUnresolved) || !generatedruntime.RouteCandidateMayBeLive(err) {
		t.Fatalf("switch error=%v candidateMayBeLive=%t", err, generatedruntime.RouteCandidateMayBeLive(err))
	}
	state, loadErr := manager.store.load()
	if loadErr != nil || state.Active[runner.appID].Slot != generatedruntime.SlotBlue {
		t.Fatalf("serving route after release fault=%#v err=%v", state.Active[runner.appID], loadErr)
	}
}
