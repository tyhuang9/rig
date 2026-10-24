package generatedruntimestate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const routeAccountingHandoffMigration = "044_generated_route_accounting_handoff.sql"

func TestRouteAccountingHandoffTxSuccessAndReplay(t *testing.T) {
	f, activation, retirement, candidate := newRouteAccountingHandoffFixture(t, true)
	ctx := context.Background()
	authorizedAt := f.now.Add(time.Minute)
	var authorization RouteAccountingHandoffAuthorization
	f.commit(t, func(tx *sql.Tx) error {
		value, changed, err := f.repository.RecordRouteAccountingHandoffAuthorizationTx(
			ctx, tx, activation, retirement, candidate, nil, authorizedAt)
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("first authorization was not inserted")
		}
		authorization = value
		return nil
	})
	if authorization.ActivationReceiptID != activation.ReceiptID ||
		authorization.SuccessorComponentCount != len(candidate) || authorization.PredecessorComponentCount != 0 ||
		!authorization.AuthorizedAt.Equal(authorizedAt) {
		t.Fatalf("authorization = %#v", authorization)
	}
	f.commit(t, func(tx *sql.Tx) error {
		value, changed, err := f.repository.RecordRouteAccountingHandoffAuthorizationTx(
			ctx, tx, activation, retirement, candidate, nil, authorizedAt.Add(time.Hour))
		if err != nil {
			return err
		}
		if changed || value != authorization {
			return fmt.Errorf("authorization replay changed=%v value=%#v", changed, value)
		}
		return nil
	})

	handedOffAt := authorizedAt.Add(time.Minute)
	var receipt RouteAccountingHandoffReceipt
	f.commit(t, func(tx *sql.Tx) error {
		value, changed, err := f.repository.RecordRouteAccountingHandoffReceiptTx(ctx, tx, authorization, handedOffAt)
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("first handoff receipt was not inserted")
		}
		receipt = value
		return nil
	})
	if receipt.ActivationReceiptID != activation.ReceiptID ||
		receipt.SuccessorComponentCount != len(candidate) || receipt.PredecessorComponentCount != 0 ||
		!receipt.HandedOffAt.Equal(handedOffAt) {
		t.Fatalf("handoff receipt = %#v", receipt)
	}
	f.commit(t, func(tx *sql.Tx) error {
		value, changed, err := f.repository.RecordRouteAccountingHandoffReceiptTx(
			ctx, tx, authorization, handedOffAt.Add(time.Hour))
		if err != nil {
			return err
		}
		if changed || value != receipt {
			return fmt.Errorf("receipt replay changed=%v value=%#v", changed, value)
		}
		return nil
	})
}

func TestRouteAccountingHandoffTxRejectsMissingAndCrossedEvidence(t *testing.T) {
	t.Run("missing retirement aggregate", func(t *testing.T) {
		f, activation, _, candidate := newRouteAccountingHandoffFixture(t, false)
		retirement := PredecessorRetirementReceipt{
			ActivationReceiptID: activation.ReceiptID, AppID: activation.AppID,
			SuccessorDeploymentID:   activation.SuccessorDeploymentID,
			PredecessorRosterDigest: activation.PredecessorRosterDigest,
			ComponentCount:          0, RetiredAt: f.now,
		}
		err := f.inTx(false, func(tx *sql.Tx) error {
			_, _, err := f.repository.RecordRouteAccountingHandoffAuthorizationTx(
				context.Background(), tx, activation, retirement, candidate, nil, f.now.Add(time.Minute))
			return err
		})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("missing retirement error = %v", err)
		}
		assertRouteAccountingHandoffCounts(t, f.db, activation.ReceiptID, 0, 0)
	})

	t.Run("crossed fixed roster", func(t *testing.T) {
		f, activation, retirement, candidate := newRouteAccountingHandoffFixture(t, true)
		crossed := append([]RouteActivationComponent(nil), candidate...)
		crossed[0].ContainerID = strings.Repeat("f", 64)
		err := f.inTx(false, func(tx *sql.Tx) error {
			_, _, err := f.repository.RecordRouteAccountingHandoffAuthorizationTx(
				context.Background(), tx, activation, retirement, crossed, nil, f.now.Add(time.Minute))
			return err
		})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("crossed roster error = %v", err)
		}
		assertRouteAccountingHandoffCounts(t, f.db, activation.ReceiptID, 0, 0)
	})

	t.Run("receipt before authorization", func(t *testing.T) {
		f, activation, _, candidate := newRouteAccountingHandoffFixture(t, true)
		_, err := f.db.Exec(`INSERT INTO generated_route_accounting_handoff_receipts(
			activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
			candidate_roster_digest,predecessor_roster_digest,successor_component_count,
			predecessor_component_count,handed_off_at
		) VALUES(?,?,?,?,?,?,?,?,?)`, activation.ReceiptID, activation.AppID,
			activation.SuccessorDeploymentID, "", activation.CandidateRosterDigest,
			activation.PredecessorRosterDigest, len(candidate), 0, timestamp(f.now))
		if err == nil {
			t.Fatal("handoff receipt without authorization was accepted")
		}
		assertRouteAccountingHandoffCounts(t, f.db, activation.ReceiptID, 0, 0)
	})
}

func TestRouteAccountingHandoffMigrationMirrorAndImmutability(t *testing.T) {
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", routeAccountingHandoffMigration))
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := os.ReadFile(filepath.Join("..", "database", "migrations", routeAccountingHandoffMigration))
	if err != nil {
		t.Fatal(err)
	}
	if string(public) != string(embedded) {
		t.Fatal("generated route accounting handoff migration is not byte mirrored")
	}

	f, activation, retirement, candidate := newRouteAccountingHandoffFixture(t, true)
	var authorization RouteAccountingHandoffAuthorization
	f.commit(t, func(tx *sql.Tx) error {
		value, _, err := f.repository.RecordRouteAccountingHandoffAuthorizationTx(
			context.Background(), tx, activation, retirement, candidate, nil, f.now.Add(time.Minute))
		authorization = value
		return err
	})
	f.commit(t, func(tx *sql.Tx) error {
		_, _, err := f.repository.RecordRouteAccountingHandoffReceiptTx(
			context.Background(), tx, authorization, f.now.Add(2*time.Minute))
		return err
	})
	for name, statement := range map[string]string{
		"authorization update": `UPDATE generated_route_accounting_handoff_authorizations
			SET authorized_at='changed' WHERE activation_receipt_id=?`,
		"authorization delete": `DELETE FROM generated_route_accounting_handoff_authorizations
			WHERE activation_receipt_id=?`,
		"receipt update": `UPDATE generated_route_accounting_handoff_receipts
			SET handed_off_at='changed' WHERE activation_receipt_id=?`,
		"receipt delete": `DELETE FROM generated_route_accounting_handoff_receipts
			WHERE activation_receipt_id=?`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.db.Exec(statement, activation.ReceiptID); err == nil {
				t.Fatal("immutable accounting handoff row was changed")
			}
		})
	}
	assertRouteAccountingHandoffCounts(t, f.db, activation.ReceiptID, 1, 1)
}

func newRouteAccountingHandoffFixture(t *testing.T, recordRetirement bool) (
	*candidateFixture,
	RouteActivationReceipt,
	PredecessorRetirementReceipt,
	[]RouteActivationComponent,
) {
	t.Helper()
	f := newCandidateFixture(t, []string{"api"})
	ctx := context.Background()
	input := f.input("api")
	f.commit(t, func(tx *sql.Tx) error { return f.repository.BeginCandidateTx(ctx, tx, input) })
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.RecordCandidateCreatedTx(ctx, tx, f.appID, f.deploymentID,
			"api", input.ReceiptID, input.ContainerName, f.containerIDs["api"])
	})
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.FinishCandidateStartingTx(ctx, tx, f.appID, f.deploymentID)
	})
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.RecordCandidateHealthyTx(ctx, tx, f.appID, f.deploymentID,
			"api", input.ReceiptID, f.containerIDs["api"])
	})
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.FinishCandidateHealthTx(ctx, tx, f.appID, f.deploymentID)
	})
	candidate, err := f.repository.RouteActivationRoster(ctx, f.appID, f.deploymentID, ComponentHealthy)
	if err != nil {
		t.Fatal(err)
	}
	canonicalCandidate, ok := canonicalPredecessorRetirementRoster(candidate, f.deploymentID)
	if !ok {
		t.Fatal("candidate roster is not canonical")
	}
	canonicalPredecessor, ok := canonicalPredecessorRetirementRoster(nil, "")
	if !ok {
		t.Fatal("empty predecessor roster is not canonical")
	}
	activation := RouteActivationReceipt{
		ReceiptID: uuid.NewString(), AppID: f.appID,
		SuccessorDeploymentID: f.deploymentID, SuccessorReleaseID: f.releaseID,
		SuccessorSlot: "blue", SuccessorGeneration: 1, CreatorJobID: f.jobID,
		CreatorWorkerAttempt:      f.jobAttempt,
		CandidateRosterDigest:     predecessorRetirementRosterDigest(canonicalCandidate),
		PredecessorRosterDigest:   predecessorRetirementRosterDigest(canonicalPredecessor),
		RouteIntentDigest:         strings.Repeat("4", 64),
		CommittedRouteProofDigest: strings.Repeat("5", 64),
		ActivatedAt:               f.now,
	}
	f.commit(t, func(tx *sql.Tx) error {
		stamp := timestamp(activation.ActivatedAt)
		if _, err := tx.Exec(`INSERT INTO generated_route_activations(
			id,app_id,successor_deployment_id,successor_release_id,successor_slot,successor_generation,
			predecessor_deployment_id,predecessor_release_id,predecessor_slot,predecessor_generation,
			predecessor_activation_receipt_id,creator_job_id,creator_worker_attempt,candidate_roster_digest,
			predecessor_roster_digest,route_intent_digest,committed_route_proof_digest,activated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, activation.ReceiptID, activation.AppID,
			activation.SuccessorDeploymentID, activation.SuccessorReleaseID, activation.SuccessorSlot,
			activation.SuccessorGeneration, "", "", "", 0, "", activation.CreatorJobID,
			activation.CreatorWorkerAttempt, activation.CandidateRosterDigest,
			activation.PredecessorRosterDigest, activation.RouteIntentDigest,
			activation.CommittedRouteProofDigest, stamp); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE generated_runtime_active_heads
			SET deployment_id=?,release_id=?,slot=?,generation=1,activation_receipt_id=?,updated_at=?
			WHERE app_id=? AND generation=0`, f.deploymentID, f.releaseID, "blue",
			activation.ReceiptID, stamp, f.appID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE generated_runtime_components SET state='active',updated_at=?
			WHERE deployment_id=? AND state='healthy'`, stamp, f.deploymentID); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE generated_runtime_deployments SET phase='draining',updated_at=?
			WHERE deployment_id=? AND phase='switching_route'`, stamp, f.deploymentID)
		return err
	})
	retirement := PredecessorRetirementReceipt{}
	if recordRetirement {
		f.commit(t, func(tx *sql.Tx) error {
			value, changed, err := f.repository.RecordPredecessorRetirementReceiptTx(
				ctx, tx, activation, nil, f.now.Add(time.Second))
			if err != nil {
				return err
			}
			if !changed {
				return errors.New("zero-roster retirement aggregate was not inserted")
			}
			retirement = value
			return nil
		})
	}
	return f, activation, retirement, canonicalCandidate
}

func assertRouteAccountingHandoffCounts(t *testing.T, db *sql.DB, receiptID string, authorizations, receipts int) {
	t.Helper()
	var actualAuthorizations, actualReceipts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_route_accounting_handoff_authorizations
		WHERE activation_receipt_id=?`, receiptID).Scan(&actualAuthorizations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts
		WHERE activation_receipt_id=?`, receiptID).Scan(&actualReceipts); err != nil {
		t.Fatal(err)
	}
	if actualAuthorizations != authorizations || actualReceipts != receipts {
		t.Fatalf("handoff rows authorizations=%d receipts=%d", actualAuthorizations, actualReceipts)
	}
}
