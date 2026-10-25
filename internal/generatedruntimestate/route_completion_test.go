package generatedruntimestate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const routeCompletionMigration = "045_generated_route_completion.sql"

func TestGeneratedRouteCompletionMigrationAndReceipt(t *testing.T) {
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", routeCompletionMigration))
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := os.ReadFile(filepath.Join("..", "database", "migrations", routeCompletionMigration))
	if err != nil {
		t.Fatal(err)
	}
	if string(public) != string(embedded) {
		t.Fatal("generated route completion migration is not byte mirrored")
	}

	f, input := newRouteCompletionFixture(t)
	var receipt RouteCompletionReceipt
	f.commit(t, func(tx *sql.Tx) error {
		if _, _, err := f.repository.RecordGeneratedRouteSucceededTx(context.Background(), tx, input); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE deployments SET status='succeeded',finished_at=?
			WHERE id=? AND status='waiting_health'`, timestamp(input.CompletedAt), input.Activation.SuccessorDeploymentID); err != nil {
			return err
		}
		value, changed, err := f.repository.RecordRouteCompletionReceiptTx(context.Background(), tx, input)
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("first completion receipt was not inserted")
		}
		receipt = value
		return nil
	})
	if !routeCompletionReceiptMatchesInput(receipt, input) {
		t.Fatalf("completion receipt = %#v", receipt)
	}
	f.commit(t, func(tx *sql.Tx) error {
		value, changed, err := f.repository.RecordRouteCompletionReceiptTx(context.Background(), tx, input)
		if err != nil {
			return err
		}
		if changed || value != receipt {
			return errors.New("exact completion receipt replay changed state")
		}
		return nil
	})
	for name, statement := range map[string]string{
		"update": `UPDATE generated_route_completion_receipts SET completed_at='changed'
			WHERE activation_receipt_id=?`,
		"delete": `DELETE FROM generated_route_completion_receipts WHERE activation_receipt_id=?`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.db.Exec(statement, input.Activation.ReceiptID); err == nil {
				t.Fatal("immutable completion receipt was changed")
			}
		})
	}

	crossed, crossedInput := newRouteCompletionFixture(t)
	if _, err := crossed.db.Exec(`INSERT INTO generated_route_completion_receipts(
		activation_receipt_id,app_id,successor_deployment_id,creator_job_id,creator_worker_attempt,
		current_job_id,current_worker_attempt,completion_code,completed_at
	) VALUES(?,?,?,?,?,?,?,?,?)`, crossedInput.Activation.ReceiptID, crossedInput.Activation.AppID,
		crossedInput.Activation.SuccessorDeploymentID, crossedInput.Activation.CreatorJobID,
		crossedInput.Activation.CreatorWorkerAttempt, crossedInput.CurrentJobID,
		crossedInput.CurrentWorkerAttempt+1, routeCompletionCode, timestamp(crossedInput.CompletedAt)); err == nil {
		t.Fatal("crossed current attempt completion receipt was accepted")
	}
}

func TestRecordGeneratedRouteRuntimeSucceededTx(t *testing.T) {
	t.Run("success and committed replay", func(t *testing.T) {
		f, input := newRouteCompletionFixture(t)
		f.commit(t, func(tx *sql.Tx) error {
			value, changed, err := f.repository.RecordGeneratedRouteSucceededTx(context.Background(), tx, input)
			if err != nil {
				return err
			}
			if !changed || value.Phase != PhaseSucceeded {
				return errors.New("runtime did not reach succeeded")
			}
			if _, err := tx.Exec(`UPDATE deployments SET status='succeeded',finished_at=?
				WHERE id=? AND status='waiting_health'`, timestamp(input.CompletedAt), input.Activation.SuccessorDeploymentID); err != nil {
				return err
			}
			_, _, err = f.repository.RecordRouteCompletionReceiptTx(context.Background(), tx, input)
			return err
		})
		f.commit(t, func(tx *sql.Tx) error {
			value, changed, err := f.repository.RecordGeneratedRouteSucceededTx(context.Background(), tx, input)
			if err != nil {
				return err
			}
			if changed || value.Phase != PhaseSucceeded {
				return errors.New("runtime replay changed state")
			}
			return nil
		})
	})

	t.Run("crossed handoff", func(t *testing.T) {
		f, input := newRouteCompletionFixture(t)
		input.AccountingHandoff.CandidateRosterDigest = strings.Repeat("f", 64)
		err := f.inTx(false, func(tx *sql.Tx) error {
			_, _, err := f.repository.RecordGeneratedRouteSucceededTx(context.Background(), tx, input)
			return err
		})
		if !errors.Is(err, ErrInvalidState) && !errors.Is(err, ErrConflict) {
			t.Fatalf("crossed handoff error = %v", err)
		}
		assertRouteCompletionRuntimePhase(t, f.db, input.Activation.SuccessorDeploymentID, PhaseDraining)
	})

	t.Run("stale current attempt", func(t *testing.T) {
		f, input := newRouteCompletionFixture(t)
		input.CurrentWorkerAttempt++
		err := f.inTx(false, func(tx *sql.Tx) error {
			_, _, err := f.repository.RecordGeneratedRouteSucceededTx(context.Background(), tx, input)
			return err
		})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("stale attempt error = %v", err)
		}
		assertRouteCompletionRuntimePhase(t, f.db, input.Activation.SuccessorDeploymentID, PhaseDraining)
	})
}

func newRouteCompletionFixture(t *testing.T) (*candidateFixture, RouteCompletionInput) {
	t.Helper()
	f, activation, retirement, candidate := newRouteAccountingHandoffFixture(t, true)
	ctx := context.Background()
	var authorization RouteAccountingHandoffAuthorization
	f.commit(t, func(tx *sql.Tx) error {
		value, changed, err := f.repository.RecordRouteAccountingHandoffAuthorizationTx(
			ctx, tx, activation, retirement, candidate, nil, f.now.Add(time.Minute))
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("accounting authorization was not inserted")
		}
		authorization = value
		return nil
	})
	var handoff RouteAccountingHandoffReceipt
	f.commit(t, func(tx *sql.Tx) error {
		value, changed, err := f.repository.RecordRouteAccountingHandoffReceiptTx(
			ctx, tx, authorization, f.now.Add(2*time.Minute))
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("accounting handoff was not inserted")
		}
		handoff = value
		return nil
	})
	return f, RouteCompletionInput{
		Activation: activation, AccountingHandoff: handoff,
		CurrentJobID: activation.CreatorJobID, CurrentWorkerAttempt: activation.CreatorWorkerAttempt,
		CompletedAt: f.now.Add(3 * time.Minute),
	}
}

func assertRouteCompletionRuntimePhase(t *testing.T, db *sql.DB, deploymentID string, want Phase) {
	t.Helper()
	var actual Phase
	if err := db.QueryRow(`SELECT phase FROM generated_runtime_deployments WHERE deployment_id=?`,
		deploymentID).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != want {
		t.Fatalf("runtime phase = %s want %s", actual, want)
	}
}
