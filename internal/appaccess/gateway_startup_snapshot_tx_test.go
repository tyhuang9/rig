package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

func TestHostingGatewayStartupSnapshotTxMatchesStandalone(t *testing.T) {
	ctx := context.Background()
	db := appAccessDB(t)
	repository := testRepository(db)
	want, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repository.HostingGatewayStartupSnapshotTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("caller-transaction snapshot differs from standalone snapshot")
	}
}

func TestHostingGatewayStartupSnapshotTxPreservesReadStageDiagnostics(t *testing.T) {
	ctx := context.Background()
	db := appAccessDB(t)
	if _, err := db.Exec(`DROP TABLE lan_gateway_profile_heads`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	snapshot, err := testRepository(db).HostingGatewayStartupSnapshotTx(ctx, tx)
	if !reflect.DeepEqual(snapshot, HostingGatewayStartupSnapshot{}) {
		t.Fatal("failed Tx read returned a partial snapshot")
	}
	stage, cause := HostingGatewayStartupDiagnostic(err)
	if stage != "upgrade" || cause != "sqlite_error" {
		t.Fatalf("diagnostic=(%q,%q), want=(upgrade,sqlite_error): %v", stage, cause, err)
	}
}

func TestHostingGatewayStartupSnapshotTxRejectsInvalidCanceledAndDoneTransactions(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	ctx := context.Background()

	if snapshot, err := repository.HostingGatewayStartupSnapshotTx(ctx, nil); err != ErrInvalidInput ||
		!reflect.DeepEqual(snapshot, HostingGatewayStartupSnapshot{}) {
		t.Fatalf("nil transaction snapshot=%#v error=%v", snapshot, err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot, err := repository.HostingGatewayStartupSnapshotTx(canceled, tx); !errors.Is(err, context.Canceled) ||
		!reflect.DeepEqual(snapshot, HostingGatewayStartupSnapshot{}) {
		_ = tx.Rollback()
		t.Fatalf("canceled snapshot=%#v error=%v", snapshot, err)
	}
	_ = tx.Rollback()

	done, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := done.Commit(); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := repository.HostingGatewayStartupSnapshotTx(ctx, done); !errors.Is(err, sql.ErrTxDone) ||
		!reflect.DeepEqual(snapshot, HostingGatewayStartupSnapshot{}) {
		t.Fatalf("done transaction snapshot=%#v error=%v", snapshot, err)
	}
}
