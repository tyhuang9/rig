package appaccess

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestHostingGatewayStartupDiagnosticFiniteMapping(t *testing.T) {
	stages := []struct {
		value hostingGatewayStartupStage
		name  string
	}{
		{hostingGatewayStartupStageBegin, "begin"},
		{hostingGatewayStartupStageUpgrade, "upgrade"},
		{hostingGatewayStartupStageGrants, "grants"},
		{hostingGatewayStartupStageDisables, "disables"},
		{hostingGatewayStartupStageRebind, "rebind"},
		{hostingGatewayStartupStageRuntimeHeads, "runtime_heads"},
		{hostingGatewayStartupStageRuntimeComponents, "runtime_components"},
		{hostingGatewayStartupStageActiveApprovalAuthority, "active_approval_authority"},
		{hostingGatewayStartupStageCommit, "commit"},
	}
	for _, test := range stages {
		t.Run("stage "+test.name, func(t *testing.T) {
			stage, cause := HostingGatewayStartupDiagnostic(&hostingGatewayStartupDiagnosticError{
				stage: test.value, cause: hostingGatewayStartupCauseUnknown, err: ErrInvalidInput,
			})
			if stage != test.name || cause != "unknown" {
				t.Fatalf("diagnostic=(%q,%q) want=(%q,unknown)", stage, cause, test.name)
			}
		})
	}

	causes := []struct {
		value hostingGatewayStartupCause
		name  string
	}{
		{hostingGatewayStartupCauseUnknown, "unknown"},
		{hostingGatewayStartupCauseInvalidStoredState, "invalid_stored_state"},
		{hostingGatewayStartupCauseCanceled, "canceled"},
		{hostingGatewayStartupCauseDeadlineExceeded, "deadline_exceeded"},
		{hostingGatewayStartupCauseSQLiteBusy, "sqlite_busy"},
		{hostingGatewayStartupCauseSQLiteLocked, "sqlite_locked"},
		{hostingGatewayStartupCauseSQLiteError, "sqlite_error"},
		{hostingGatewayStartupCauseSQLiteCorrupt, "sqlite_corrupt"},
		{hostingGatewayStartupCauseSQLiteIO, "sqlite_io"},
		{hostingGatewayStartupCauseSQLiteOther, "sqlite_other"},
	}
	for _, test := range causes {
		t.Run("cause "+test.name, func(t *testing.T) {
			stage, cause := HostingGatewayStartupDiagnostic(&hostingGatewayStartupDiagnosticError{
				stage: hostingGatewayStartupStageBegin, cause: test.value, err: ErrInvalidInput,
			})
			if stage != "begin" || cause != test.name {
				t.Fatalf("diagnostic=(%q,%q) want=(begin,%q)", stage, cause, test.name)
			}
		})
	}

	var nilDiagnostic *hostingGatewayStartupDiagnosticError
	for name, err := range map[string]error{
		"nil":           nil,
		"external":      ErrInvalidStoredState,
		"typed nil":     nilDiagnostic,
		"invalid enums": &hostingGatewayStartupDiagnosticError{stage: 255, cause: 255, err: ErrInvalidInput},
	} {
		t.Run(name, func(t *testing.T) {
			stage, cause := HostingGatewayStartupDiagnostic(err)
			if stage != "unknown" || cause != "unknown" {
				t.Fatalf("diagnostic=(%q,%q) want=(unknown,unknown)", stage, cause)
			}
		})
	}
}

func TestHostingGatewayStartupDiagnosticTrustAndErrorCompatibility(t *testing.T) {
	calls := &hostingGatewayStartupHostileCalls{}
	hostile := &hostingGatewayStartupHostileError{calls: calls}
	diagnostic := newHostingGatewayStartupDiagnosticError(hostingGatewayStartupStageGrants, hostile)
	if stage, cause := HostingGatewayStartupDiagnostic(diagnostic); stage != "grants" || cause != "unknown" {
		t.Fatalf("hostile diagnostic=(%q,%q)", stage, cause)
	}
	if *calls != (hostingGatewayStartupHostileCalls{}) {
		t.Fatal("diagnostic construction or observation invoked an untrusted error method")
	}

	external := &hostingGatewayStartupHostileError{calls: calls, inner: diagnostic}
	if stage, cause := HostingGatewayStartupDiagnostic(external); stage != "unknown" || cause != "unknown" {
		t.Fatalf("external diagnostic=(%q,%q)", stage, cause)
	}
	if *calls != (hostingGatewayStartupHostileCalls{}) {
		t.Fatal("direct-only observer invoked methods on an external wrapper")
	}

	wrappedDiagnostic := fmt.Errorf("outer: %w", diagnostic)
	if stage, cause := HostingGatewayStartupDiagnostic(wrappedDiagnostic); stage != "unknown" || cause != "unknown" {
		t.Fatalf("wrapped diagnostic=(%q,%q)", stage, cause)
	}

	cause := &hostingGatewayStartupSerializableError{Message: "opaque underlying detail"}
	compatible := newHostingGatewayStartupDiagnosticError(hostingGatewayStartupStageUpgrade, cause)
	if compatible.Error() != cause.Error() || errors.Unwrap(compatible) != cause || !errors.Is(compatible, cause) {
		t.Fatal("diagnostic wrapper changed Error or Unwrap behavior")
	}
	var extracted *hostingGatewayStartupSerializableError
	if !errors.As(compatible, &extracted) || extracted != cause {
		t.Fatal("diagnostic wrapper hid the original cause from caller-initiated errors.As")
	}
	wantJSON, err := json.Marshal(cause)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(compatible)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatal("diagnostic wrapper changed original error JSON")
	}
	if bytes.Contains(gotJSON, []byte("stage")) || bytes.Contains(gotJSON, []byte("cause")) {
		t.Fatal("diagnostic wrapper serialized private metadata")
	}
}

func TestHostingGatewayStartupDiagnosticDirectCauseClassification(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want hostingGatewayStartupCause
	}{
		{"invalid stored state", ErrInvalidStoredState, hostingGatewayStartupCauseInvalidStoredState},
		{"canceled", context.Canceled, hostingGatewayStartupCauseCanceled},
		{"deadline", context.DeadlineExceeded, hostingGatewayStartupCauseDeadlineExceeded},
		{"wrapped invalid stored state", fmt.Errorf("outer: %w", ErrInvalidStoredState), hostingGatewayStartupCauseUnknown},
		{"generic", errors.New("opaque"), hostingGatewayStartupCauseUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyHostingGatewayStartupCause(test.err); got != test.want {
				t.Fatalf("cause=%d want=%d", got, test.want)
			}
		})
	}
	if got := classifyHostingGatewayStartupCause(nil); got != hostingGatewayStartupCauseUnknown {
		t.Fatalf("nil cause=%d", got)
	}
	var nilSQLite *sqlite.Error
	if got := classifyHostingGatewayStartupCause(nilSQLite); got != hostingGatewayStartupCauseUnknown {
		t.Fatalf("typed nil sqlite cause=%d", got)
	}

	for _, test := range []struct {
		name string
		code int
		want hostingGatewayStartupCause
	}{
		{"busy", sqlite3.SQLITE_BUSY, hostingGatewayStartupCauseSQLiteBusy},
		{"extended busy", sqlite3.SQLITE_BUSY | 5<<8, hostingGatewayStartupCauseSQLiteBusy},
		{"locked", sqlite3.SQLITE_LOCKED, hostingGatewayStartupCauseSQLiteLocked},
		{"error", sqlite3.SQLITE_ERROR, hostingGatewayStartupCauseSQLiteError},
		{"corrupt", sqlite3.SQLITE_CORRUPT, hostingGatewayStartupCauseSQLiteCorrupt},
		{"not a database", sqlite3.SQLITE_NOTADB, hostingGatewayStartupCauseSQLiteCorrupt},
		{"io", sqlite3.SQLITE_IOERR, hostingGatewayStartupCauseSQLiteIO},
		{"other", sqlite3.SQLITE_CONSTRAINT, hostingGatewayStartupCauseSQLiteOther},
	} {
		t.Run("sqlite "+test.name, func(t *testing.T) {
			if got := classifyHostingGatewayStartupSQLiteCause(test.code & 0xff); got != test.want {
				t.Fatalf("cause=%d want=%d", got, test.want)
			}
		})
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var ignored int
	err = db.QueryRow(`SELECT value FROM missing_startup_diagnostic_table`).Scan(&ignored)
	if _, ok := err.(*sqlite.Error); !ok {
		t.Fatalf("missing-table error type=%T", err)
	}
	if got := classifyHostingGatewayStartupCause(err); got != hostingGatewayStartupCauseSQLiteError {
		t.Fatalf("real sqlite cause=%d want=%d", got, hostingGatewayStartupCauseSQLiteError)
	}
	if got := classifyHostingGatewayStartupCause(fmt.Errorf("outer: %w", err)); got != hostingGatewayStartupCauseUnknown {
		t.Fatalf("wrapped real sqlite cause=%d want=%d", got, hostingGatewayStartupCauseUnknown)
	}
}

func TestHostingGatewayStartupSnapshotReportsDiagnosticStageAndCause(t *testing.T) {
	t.Run("canceled begin", func(t *testing.T) {
		db := appAccessDB(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		snapshot, err := New(db).HostingGatewayStartupSnapshot(ctx)
		assertHostingGatewayStartupFailure(t, snapshot, err, "begin", "canceled", context.Canceled)
	})

	t.Run("invalid upgrade", func(t *testing.T) {
		ctx := context.Background()
		db := appAccessDB(t)
		repository := testRepository(db)
		profile, _, err := repository.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
			GatewayProfileSpec{SelectedIPv4: "192.168.72.8", InterfaceID: "diagnostic-upgrade", PortStart: 8100, PortEnd: 8119}, 0))
		if err != nil {
			t.Fatal(err)
		}
		claim, _, err := repository.ClaimGatewayProfileUpgrade(ctx, approvedGatewayUpgradeClaimInput(t, profile))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, gatewayUpgradeClaimOwner(claim),
			GatewayProfileUpgradePrepared, GatewayProfileUpgradeServing); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DROP TRIGGER lan_gateway_upgrade_claim_event_retain`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DELETE FROM lan_gateway_upgrade_claim_events WHERE operation_id=? AND sequence=1`,
			claim.OperationID); err != nil {
			t.Fatal(err)
		}
		snapshot, err := repository.HostingGatewayStartupSnapshot(ctx)
		assertHostingGatewayStartupFailure(t, snapshot, err, "upgrade", "invalid_stored_state", ErrInvalidStoredState)
	})

	t.Run("invalid grant", func(t *testing.T) {
		db, repository, _, _, input := approvedGrantFixture(t)
		claim, _, err := repository.ClaimAppAccessGrant(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DROP TRIGGER lan_app_access_grant_event_retain`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DELETE FROM lan_app_access_grant_claim_events WHERE attempt_id=?`,
			claim.AttemptID); err != nil {
			t.Fatal(err)
		}
		snapshot, err := repository.HostingGatewayStartupSnapshot(context.Background())
		assertHostingGatewayStartupFailure(t, snapshot, err, "grants", "invalid_stored_state", ErrInvalidStoredState)
	})

	t.Run("invalid runtime component", func(t *testing.T) {
		db := appAccessDB(t)
		appID := addApps(t, db, 1)[0]
		head := seedGatewayRebindActiveRuntime(t, db, appID)
		if _, err := db.Exec(`DROP TRIGGER generated_runtime_component_no_delete`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DELETE FROM generated_runtime_components WHERE deployment_id=?`,
			head.DeploymentID); err != nil {
			t.Fatal(err)
		}
		snapshot, err := New(db).HostingGatewayStartupSnapshot(context.Background())
		assertHostingGatewayStartupFailure(t, snapshot, err, "runtime_components", "invalid_stored_state", ErrInvalidStoredState)
	})

	t.Run("success", func(t *testing.T) {
		snapshot, err := New(appAccessDB(t)).HostingGatewayStartupSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if stage, cause := HostingGatewayStartupDiagnostic(err); stage != "unknown" || cause != "unknown" {
			t.Fatalf("success diagnostic=(%q,%q)", stage, cause)
		}
		if len(snapshot.Upgrades.Claims) != 0 || len(snapshot.Grants.Claims) != 0 ||
			len(snapshot.Disables.Claims) != 0 || snapshot.Rebind.Active != nil ||
			len(snapshot.RuntimeHeads) != 0 || len(snapshot.RuntimeComponents) != 0 ||
			snapshot.ActiveRebindApprovalAuthority != nil {
			t.Fatal("empty database returned invented startup authority")
		}
	})
}

func assertHostingGatewayStartupFailure(t *testing.T, snapshot HostingGatewayStartupSnapshot,
	err error, wantStage, wantCause string, target error,
) {
	t.Helper()
	if err == nil {
		t.Fatal("startup snapshot unexpectedly succeeded")
	}
	if !reflect.DeepEqual(snapshot, HostingGatewayStartupSnapshot{}) {
		t.Fatal("startup snapshot returned partial authority on failure")
	}
	if !errors.Is(err, target) {
		t.Fatal("startup snapshot changed its original error identity")
	}
	stage, cause := HostingGatewayStartupDiagnostic(err)
	if stage != wantStage || cause != wantCause {
		t.Fatalf("diagnostic=(%q,%q) want=(%q,%q)", stage, cause, wantStage, wantCause)
	}
}

type hostingGatewayStartupSerializableError struct {
	Message string `json:"message"`
}

func (e *hostingGatewayStartupSerializableError) Error() string { return e.Message }

type hostingGatewayStartupHostileCalls struct {
	error  int
	is     int
	as     int
	unwrap int
}

type hostingGatewayStartupHostileError struct {
	calls *hostingGatewayStartupHostileCalls
	inner error
}

func (e *hostingGatewayStartupHostileError) Error() string {
	e.calls.error++
	return "hostile"
}

func (e *hostingGatewayStartupHostileError) Is(error) bool {
	e.calls.is++
	return false
}

func (e *hostingGatewayStartupHostileError) As(any) bool {
	e.calls.as++
	return false
}

func (e *hostingGatewayStartupHostileError) Unwrap() error {
	e.calls.unwrap++
	return e.inner
}
