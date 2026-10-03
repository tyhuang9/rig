package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/database"
)

func TestGatewayRebindQuiescenceCensusAcceptsEmptyTables(t *testing.T) {
	db := openGatewayRebindCensusDB(t)
	got, err := New(db).GatewayRebindQuiescenceCensus(context.Background())
	if err != nil || got != (GatewayRebindQuiescenceCensus{}) {
		t.Fatalf("empty census = %#v error=%v", got, err)
	}
	if _, err := (*Repository)(nil).GatewayRebindQuiescenceCensus(context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil repository error = %v", err)
	}
}

func TestGatewayRebindQuiescenceCensusClassifiesEveryJobStatus(t *testing.T) {
	tests := []struct {
		status      string
		want        GatewayRebindJobCensus
		nonterminal bool
	}{
		{status: "queued", want: GatewayRebindJobCensus{Total: 1, Queued: 1}, nonterminal: true},
		{status: "assigned", want: GatewayRebindJobCensus{Total: 1, Assigned: 1}, nonterminal: true},
		{status: "running", want: GatewayRebindJobCensus{Total: 1, Running: 1}, nonterminal: true},
		{status: "waiting_external", want: GatewayRebindJobCensus{Total: 1, WaitingExternal: 1}, nonterminal: true},
		{status: "waiting_user", want: GatewayRebindJobCensus{Total: 1, WaitingUser: 1}, nonterminal: true},
		{status: "succeeded", want: GatewayRebindJobCensus{Total: 1, Succeeded: 1}},
		{status: "failed", want: GatewayRebindJobCensus{Total: 1, Failed: 1}},
		{status: "cancelled", want: GatewayRebindJobCensus{Total: 1, Cancelled: 1}},
		{status: "interrupted", want: GatewayRebindJobCensus{Total: 1, Interrupted: 1}},
		{status: "needs_attention", want: GatewayRebindJobCensus{Total: 1, NeedsAttention: 1}},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			db := openGatewayRebindCensusDB(t)
			if _, err := db.Exec(`INSERT INTO jobs(id,status) VALUES('job',?)`, test.status); err != nil {
				t.Fatal(err)
			}
			got, err := New(db).GatewayRebindQuiescenceCensus(context.Background())
			if test.nonterminal {
				if !errors.Is(err, ErrGatewayRebindNotQuiescent) {
					t.Fatalf("status %q error = %v", test.status, err)
				}
			} else if err != nil {
				t.Fatalf("status %q error = %v", test.status, err)
			}
			if got.Jobs != test.want || got.Deployments != (GatewayRebindDeploymentCensus{}) {
				t.Fatalf("status %q census = %#v", test.status, got)
			}
		})
	}
}

func TestGatewayRebindQuiescenceCensusClassifiesEveryDeploymentStatus(t *testing.T) {
	tests := []struct {
		status      string
		want        GatewayRebindDeploymentCensus
		nonterminal bool
	}{
		{status: "preparing", want: GatewayRebindDeploymentCensus{Total: 1, Preparing: 1}, nonterminal: true},
		{status: "applying", want: GatewayRebindDeploymentCensus{Total: 1, Applying: 1}, nonterminal: true},
		{status: "waiting_health", want: GatewayRebindDeploymentCensus{Total: 1, WaitingHealth: 1}, nonterminal: true},
		{status: "succeeded", want: GatewayRebindDeploymentCensus{Total: 1, Succeeded: 1}},
		{status: "failed", want: GatewayRebindDeploymentCensus{Total: 1, Failed: 1}},
		{status: "cancelled", want: GatewayRebindDeploymentCensus{Total: 1, Cancelled: 1}},
		{status: "needs_attention", want: GatewayRebindDeploymentCensus{Total: 1, NeedsAttention: 1}, nonterminal: true},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			db := openGatewayRebindCensusDB(t)
			if _, err := db.Exec(`INSERT INTO deployments(id,status) VALUES('deployment',?)`, test.status); err != nil {
				t.Fatal(err)
			}
			got, err := New(db).GatewayRebindQuiescenceCensus(context.Background())
			if test.nonterminal {
				if !errors.Is(err, ErrGatewayRebindNotQuiescent) {
					t.Fatalf("status %q error = %v", test.status, err)
				}
			} else if err != nil {
				t.Fatalf("status %q error = %v", test.status, err)
			}
			if got.Deployments != test.want || got.Jobs != (GatewayRebindJobCensus{}) {
				t.Fatalf("status %q census = %#v", test.status, got)
			}
		})
	}
}

func TestGatewayRebindQuiescenceCensusRejectsUnknownNullAndCorruptStatuses(t *testing.T) {
	tests := []struct {
		name  string
		table string
		value any
	}{
		{name: "unknown job", table: "jobs", value: "future_job_state"},
		{name: "null job", table: "jobs", value: nil},
		{name: "corrupt job", table: "jobs", value: []byte{0xff, 0xfe}},
		{name: "unknown deployment", table: "deployments", value: "future_deployment_state"},
		{name: "null deployment", table: "deployments", value: nil},
		{name: "corrupt deployment", table: "deployments", value: []byte{0xff, 0xfe}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openGatewayRebindCensusDB(t)
			query := fmt.Sprintf("INSERT INTO %s(id,status) VALUES('row',?)", test.table)
			if _, err := db.Exec(query, test.value); err != nil {
				t.Fatal(err)
			}
			got, err := New(db).GatewayRebindQuiescenceCensus(context.Background())
			if !errors.Is(err, ErrInvalidStoredState) {
				t.Fatalf("census = %#v error=%v", got, err)
			}
			if got.Jobs.Total+got.Deployments.Total != 1 {
				t.Fatalf("invalid row was not included in full census: %#v", got)
			}
		})
	}
}

func TestGatewayRebindQuiescenceCensusHasNoPaginationBoundary(t *testing.T) {
	db := openGatewayRebindCensusDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 257; index++ {
		if _, err := tx.Exec(`INSERT INTO jobs(id,status) VALUES(?, 'succeeded')`, fmt.Sprintf("job-%03d", index)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO deployments(id,status) VALUES(?, 'failed')`, fmt.Sprintf("deployment-%03d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := New(db).GatewayRebindQuiescenceCensus(context.Background())
	if err != nil || got.Jobs.Total != 257 || got.Jobs.Succeeded != 257 ||
		got.Deployments.Total != 257 || got.Deployments.Failed != 257 {
		t.Fatalf("large census = %#v error=%v", got, err)
	}
}

func TestGatewayRebindQuiescenceCensusPropagatesCancellationAndReadErrors(t *testing.T) {
	t.Run("cancel between table reads", func(t *testing.T) {
		db := openGatewayRebindCensusDB(t)
		ctx, cancel := context.WithCancel(context.Background())
		_, err := New(db).gatewayRebindQuiescenceCensus(ctx, cancel)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled census error = %v", err)
		}
	})

	t.Run("deployment read failure", func(t *testing.T) {
		db := openGatewayRebindCensusDB(t)
		if _, err := db.Exec(`DROP TABLE deployments`); err != nil {
			t.Fatal(err)
		}
		if _, err := New(db).GatewayRebindQuiescenceCensus(context.Background()); err == nil ||
			errors.Is(err, ErrGatewayRebindNotQuiescent) || errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("deployment read error = %v", err)
		}
	})
}

func TestGatewayRebindQuiescenceCensusUsesOneSnapshotAcrossHandles(t *testing.T) {
	dataRoot := t.TempDir()
	readerDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readerDB.Close() })
	seedGatewayRebindCensusDatabase(t, readerDB)
	writerDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writerDB.Close() })

	var writeErr error
	got, err := New(readerDB).gatewayRebindQuiescenceCensus(context.Background(), func() {
		_, writeErr = writerDB.Exec(`UPDATE jobs SET status='queued' WHERE id='census-job'`)
		if writeErr == nil {
			_, writeErr = writerDB.Exec(`UPDATE deployments SET status='needs_attention' WHERE id='census-deployment'`)
		}
	})
	if writeErr != nil {
		t.Fatalf("concurrent write: %v", writeErr)
	}
	if err != nil || got.Jobs.Succeeded != 1 || got.Deployments.Succeeded != 1 {
		t.Fatalf("original snapshot census = %#v error=%v", got, err)
	}
	fresh, err := New(readerDB).GatewayRebindQuiescenceCensus(context.Background())
	if !errors.Is(err, ErrGatewayRebindNotQuiescent) || fresh.Jobs.Queued != 1 || fresh.Deployments.NeedsAttention != 1 {
		t.Fatalf("fresh census = %#v error=%v", fresh, err)
	}
}

func TestEvaluateGatewayRebindQuiescenceSupportsImmediateTransactionWithoutWrites(t *testing.T) {
	tests := []struct {
		name             string
		jobStatus        string
		deploymentStatus string
		wantErr          error
	}{
		{name: "terminal", jobStatus: "succeeded", deploymentStatus: "succeeded"},
		{name: "nonterminal", jobStatus: "queued", deploymentStatus: "preparing", wantErr: ErrGatewayRebindNotQuiescent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, err := database.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			seedGatewayRebindCensusDatabase(t, db)
			if _, err := db.Exec(`UPDATE jobs SET status=?,phase=? WHERE id='census-job'`, test.jobStatus, test.jobStatus); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE deployments SET status=? WHERE id='census-deployment'`, test.deploymentStatus); err != nil {
				t.Fatal(err)
			}
			beforeRows := gatewayRebindCensusRows(t, db)
			var beforeChanges int64
			if err := db.QueryRow(`SELECT total_changes()`).Scan(&beforeChanges); err != nil {
				t.Fatal(err)
			}

			tx, err := beginImmediateTransaction(context.Background(), db)
			if err != nil {
				t.Fatal(err)
			}
			got, evaluationErr := evaluateGatewayRebindQuiescence(context.Background(), tx, nil)
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(evaluationErr, test.wantErr) {
				t.Fatalf("evaluation error = %v, want %v", evaluationErr, test.wantErr)
			}
			if got.Jobs.Total != 1 || got.Deployments.Total != 1 {
				t.Fatalf("immediate transaction census = %#v", got)
			}
			if test.wantErr == nil && (got.Jobs.Succeeded != 1 || got.Deployments.Succeeded != 1) {
				t.Fatalf("terminal decision census = %#v", got)
			}
			if test.wantErr != nil && (got.Jobs.Queued != 1 || got.Deployments.Preparing != 1) {
				t.Fatalf("nonterminal decision census = %#v", got)
			}

			var afterChanges int64
			if err := db.QueryRow(`SELECT total_changes()`).Scan(&afterChanges); err != nil {
				t.Fatal(err)
			}
			afterRows := gatewayRebindCensusRows(t, db)
			if beforeChanges != afterChanges || !reflect.DeepEqual(beforeRows, afterRows) {
				t.Fatalf("evaluator wrote data: changes=%d->%d rows=%v->%v", beforeChanges, afterChanges, beforeRows, afterRows)
			}
		})
	}
}

func TestGatewayRebindQuiescenceCensusIsReadOnly(t *testing.T) {
	db := openGatewayRebindCensusDB(t)
	if _, err := db.Exec(`INSERT INTO jobs(id,status) VALUES('terminal-job','interrupted');
		INSERT INTO deployments(id,status) VALUES('terminal-deployment','cancelled')`); err != nil {
		t.Fatal(err)
	}
	beforeRows := gatewayRebindCensusRows(t, db)
	var beforeChanges int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&beforeChanges); err != nil {
		t.Fatal(err)
	}
	got, err := New(db).GatewayRebindQuiescenceCensus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var afterChanges int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&afterChanges); err != nil {
		t.Fatal(err)
	}
	afterRows := gatewayRebindCensusRows(t, db)
	if beforeChanges != afterChanges || !reflect.DeepEqual(beforeRows, afterRows) ||
		got.Jobs.Interrupted != 1 || got.Deployments.Cancelled != 1 {
		t.Fatalf("read-only census=%#v changes=%d->%d rows=%v->%v", got, beforeChanges, afterChanges, beforeRows, afterRows)
	}
}

func openGatewayRebindCensusDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE jobs(id TEXT PRIMARY KEY,status TEXT);
		CREATE TABLE deployments(id TEXT PRIMARY KEY,status TEXT)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedGatewayRebindCensusDatabase(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
		VALUES('census-app','census-app','Census App','draft',datetime('now'),datetime('now'));
		INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,created_at,updated_at)
		VALUES('census-job','deploy','application','census-app','succeeded','succeeded',datetime('now'),datetime('now'));
		INSERT INTO deployments(id,app_id,job_id,status,configuration_mode,provenance_initialized)
		VALUES('census-deployment','census-app','census-job','succeeded','current',1)`); err != nil {
		t.Fatal(err)
	}
}

func gatewayRebindCensusRows(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT 'job:' || id || ':' || COALESCE(status,'<null>') FROM jobs
		UNION ALL SELECT 'deployment:' || id || ':' || COALESCE(status,'<null>') FROM deployments
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
