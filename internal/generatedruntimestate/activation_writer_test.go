package generatedruntimestate

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestReadRouteActivationReceiptTxNormalizesLegacyWriterAndRejectsHalfPair(t *testing.T) {
	fixture, activation, _, _ := newRouteAccountingHandoffFixture(t, true)
	read := func() (RouteActivationReceipt, error) {
		tx, err := fixture.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return RouteActivationReceipt{}, err
		}
		defer tx.Rollback()
		value, err := fixture.repository.ReadRouteActivationReceiptTx(context.Background(), tx, activation.ReceiptID)
		if err != nil {
			return RouteActivationReceipt{}, err
		}
		if err := tx.Commit(); err != nil {
			return RouteActivationReceipt{}, err
		}
		return value, nil
	}
	value, err := read()
	if err != nil {
		t.Fatal(err)
	}
	if value.WriterJobID != activation.CreatorJobID || value.WriterWorkerAttempt != activation.CreatorWorkerAttempt {
		t.Fatalf("legacy writer normalized to %q/%d want %q/%d", value.WriterJobID,
			value.WriterWorkerAttempt, activation.CreatorJobID, activation.CreatorWorkerAttempt)
	}
	if _, err := fixture.db.Exec(`DROP TRIGGER generated_route_activation_immutable`); err != nil {
		t.Fatal(err)
	}
	for _, update := range []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "writer job only",
			query: `UPDATE generated_route_activations SET writer_job_id=?,writer_worker_attempt=NULL WHERE id=?`,
			args:  []any{activation.CreatorJobID, activation.ReceiptID},
		},
		{
			name:  "writer attempt only",
			query: `UPDATE generated_route_activations SET writer_job_id=NULL,writer_worker_attempt=? WHERE id=?`,
			args:  []any{activation.CreatorWorkerAttempt, activation.ReceiptID},
		},
	} {
		t.Run(update.name, func(t *testing.T) {
			if _, err := fixture.db.Exec(update.query, update.args...); err != nil {
				t.Fatal(err)
			}
			if _, err := read(); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("half-null writer read error=%v", err)
			}
		})
	}
	if _, err := fixture.db.Exec(`UPDATE generated_route_activations
		SET writer_job_id=?,writer_worker_attempt=? WHERE id=?`, activation.CreatorJobID,
		activation.CreatorWorkerAttempt-1, activation.ReceiptID); err != nil {
		t.Fatal(err)
	}
	if _, err := read(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("writer before creator read error=%v", err)
	}
}
