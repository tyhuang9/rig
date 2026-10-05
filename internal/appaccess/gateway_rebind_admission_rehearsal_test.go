package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
)

func finishGatewayRebindFixtureWork(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`UPDATE jobs SET status='succeeded' WHERE status='running';
		UPDATE deployments SET status='succeeded' WHERE status='preparing'`); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRebindImmediatePreclaimRollsBackCompleteObservation(t *testing.T) {
	root := t.TempDir()
	first, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	addUsers(t, first)
	fixture := newGatewayRebindFixtureOnDBWithClaim(t, first, false, false)
	proposal := gatewayRebindPreclaimProposalFor(fixture)
	ordinary, err := fixture.repository.GatewayRebindPreclaimSnapshot(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	finishGatewayRebindFixtureWork(t, fixture.db)
	result, err := fixture.repository.GatewayRebindImmediatePreclaim(context.Background(), proposal)
	if err != nil || !reflect.DeepEqual(result.Predecessor, ordinary) ||
		result.Quiescence.Jobs != (GatewayRebindJobCensus{Total: 1, Succeeded: 1}) ||
		result.Quiescence.Deployments != (GatewayRebindDeploymentCensus{Total: 1, Succeeded: 1}) {
		t.Fatalf("immediate preclaim result=%#v error=%v", result, err)
	}
	var claims, events, roster int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_claims`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_claim_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_roster_entries`).Scan(&roster); err != nil {
		t.Fatal(err)
	}
	if claims != 0 || events != 0 || roster != 0 {
		t.Fatalf("immediate preclaim persisted state: claims=%d events=%d roster=%d", claims, events, roster)
	}
	second, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	writer, err := beginImmediateTransaction(context.Background(), second)
	if err != nil {
		t.Fatalf("rehearsal did not release its writer reservation: %v", err)
	}
	if err := writer.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRebindImmediatePreclaimRejectsNonterminalAndClaimedState(t *testing.T) {
	t.Run("nonterminal job", func(t *testing.T) {
		fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
		if _, err := fixture.db.Exec(`UPDATE deployments SET status='succeeded' WHERE status='preparing'`); err != nil {
			t.Fatal(err)
		}
		result, err := fixture.repository.GatewayRebindImmediatePreclaim(context.Background(), gatewayRebindPreclaimProposalFor(fixture))
		if !errors.Is(err, ErrGatewayRebindNotQuiescent) || !reflect.DeepEqual(result, GatewayRebindImmediatePreclaimResult{}) {
			t.Fatalf("nonterminal preclaim result=%#v error=%v", result, err)
		}
	})
	t.Run("claim already exists", func(t *testing.T) {
		fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, true)
		result, err := fixture.repository.GatewayRebindImmediatePreclaim(context.Background(), gatewayRebindPreclaimProposalFor(fixture))
		if !errors.Is(err, ErrInvalidStoredState) || !reflect.DeepEqual(result, GatewayRebindImmediatePreclaimResult{}) {
			t.Fatalf("claimed preclaim result=%#v error=%v", result, err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result, err := fixture.repository.GatewayRebindImmediatePreclaim(ctx, gatewayRebindPreclaimProposalFor(fixture))
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, GatewayRebindImmediatePreclaimResult{}) {
			t.Fatalf("cancelled preclaim result=%#v error=%v", result, err)
		}
	})
}

func TestGatewayRebindImmediatePreclaimSerializesSecondHandleWriter(t *testing.T) {
	root := t.TempDir()
	first, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	addUsers(t, first)
	fixture := newGatewayRebindFixtureOnDBWithClaim(t, first, false, false)
	second, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	writer, err := beginImmediateTransaction(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.ExecContext(context.Background(), `UPDATE jobs SET status='succeeded' WHERE status='running'`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(context.Background(), `UPDATE deployments SET status='succeeded' WHERE status='preparing'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if result, err := fixture.repository.GatewayRebindImmediatePreclaim(ctx, gatewayRebindPreclaimProposalFor(fixture)); !(errors.Is(err, ErrConflict) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) ||
		!reflect.DeepEqual(result, GatewayRebindImmediatePreclaimResult{}) {
		t.Fatalf("rehearsal bypassed second-handle write lock: result=%#v error=%v", result, err)
	}
	if err := writer.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if result, err := fixture.repository.GatewayRebindImmediatePreclaim(context.Background(), gatewayRebindPreclaimProposalFor(fixture)); err != nil || result.Quiescence.Jobs.Succeeded != 1 || result.Quiescence.Deployments.Succeeded != 1 {
		t.Fatalf("committed competing terminal state was missed: result=%#v error=%v", result, err)
	}
}
