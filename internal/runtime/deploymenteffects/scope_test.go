package deploymenteffects

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestScopedLeaseBindsExactDirectoryAndExpiresBeforeRelease(t *testing.T) {
	directory := deploymentEffectsTestDirectory(t)
	scope, release, err := AcquireScoped(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithScope(context.Background(), scope)
	leave, err := EnterScope(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	leave()
	if _, err := EnterScope(ctx, directory+"-foreign"); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("foreign scope error = %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if _, err := EnterScope(ctx, directory); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("expired scope error = %v", err)
	}
}

func TestScopedLeaseReleaseWaitsForBoundedOperationAndClosesEntry(t *testing.T) {
	directory := deploymentEffectsTestDirectory(t)
	scope, release, err := AcquireScoped(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithScope(context.Background(), scope)
	leave, err := EnterScope(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() { released <- release() }()
	deadline := time.Now().Add(time.Second)
	for {
		entered, err := EnterScope(ctx, directory)
		if errors.Is(err, ErrScopeUnavailable) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		entered()
		if time.Now().After(deadline) {
			t.Fatal("release did not close new scope entries")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-released:
		t.Fatalf("lease released while operation was active: %v", err)
	default:
	}
	leave()
	select {
	case err := <-released:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("lease did not release after operation left")
	}
}

func TestScopedContextPreservesParentCancellation(t *testing.T) {
	directory := deploymentEffectsTestDirectory(t)
	scope, release, err := AcquireScoped(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	parent, cancel := context.WithCancel(context.Background())
	ctx := WithScope(parent, scope)
	cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("scoped context error = %v", ctx.Err())
	}
	if _, err := EnterScope(ctx, directory); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("cancelled scope error = %v", err)
	}
}
