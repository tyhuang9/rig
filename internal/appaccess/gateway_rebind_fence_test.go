package appaccess

import (
	"context"
	"errors"
	"testing"
)

func TestGatewayRebindFenceUsesValidatedPreparedClaim(t *testing.T) {
	fixture := newGatewayRebindFixture(t, true)
	if err := fixture.repository.CheckGatewayRebindFence(context.Background()); !errors.Is(err, ErrGatewayRebindActive) {
		t.Fatalf("valid prepared claim fence error = %v", err)
	}

	incomplete := newGatewayRebindFixture(t, false)
	if err := incomplete.repository.CheckGatewayRebindFence(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
		t.Fatalf("incomplete claim fence error = %v", err)
	}
}
