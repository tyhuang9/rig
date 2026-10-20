package generatedimage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"modernc.org/sqlite"
)

type imageCommitAcknowledgementFault struct {
	armed            atomic.Bool
	trackTerminal    atomic.Bool
	terminalAttempts atomic.Int32
	cause            error
}

type imageCommitConnector struct {
	dsn   string
	fault *imageCommitAcknowledgementFault
}

func (c *imageCommitConnector) Connect(context.Context) (driver.Conn, error) {
	connection, err := (&sqlite.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &imageCommitConnection{Conn: connection, fault: c.fault}, nil
}

func (c *imageCommitConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type imageCommitConnection struct {
	driver.Conn
	fault *imageCommitAcknowledgementFault
}

func (c *imageCommitConnection) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &imageCommitTransaction{Tx: tx, fault: c.fault}, nil
}

func (c *imageCommitConnection) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (c *imageCommitConnection) ExecContext(ctx context.Context, query string, values []driver.NamedValue) (driver.Result, error) {
	if c.fault.trackTerminal.Load() && imageCommitTerminalAttempt(query, values) {
		c.fault.terminalAttempts.Add(1)
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, values)
}

func imageCommitTerminalAttempt(query string, values []driver.NamedValue) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
	switch normalized {
	case "update generated_image_artifacts set state='unavailable',diagnostic_code=?,updated_at=? where id=? and state='ready'",
		"update generated_image_artifacts set state='failed',diagnostic_code=?,updated_at=?,finished_at=? where state='building'":
		return true
	case "update generated_image_artifacts set state=?,diagnostic_code=?,updated_at=?,finished_at=? where id=? and state='building'":
		if len(values) == 0 {
			return false
		}
		state, ok := values[0].Value.(string)
		return ok && (state == "failed" || state == "cancelled" || state == "unavailable")
	default:
		return false
	}
}

func (c *imageCommitConnection) QueryContext(ctx context.Context, query string, values []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, values)
}

func (c *imageCommitConnection) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}

func (c *imageCommitConnection) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}

func (c *imageCommitConnection) IsValid() bool { return c.Conn.(driver.Validator).IsValid() }

type imageCommitTransaction struct {
	driver.Tx
	fault *imageCommitAcknowledgementFault
}

func (t *imageCommitTransaction) Commit() error {
	if err := t.Tx.Commit(); err != nil {
		return err
	}
	if t.fault.armed.CompareAndSwap(true, false) {
		return t.fault.cause
	}
	return nil
}

func openImageCommitAcknowledgementDatabase(t *testing.T, fault *imageCommitAcknowledgementFault) *sql.DB {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(&imageCommitConnector{dsn: filepath.Join(root, "control.db"), fault: fault})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, pragma := range []string{"PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL", "PRAGMA synchronous = NORMAL", "PRAGMA busy_timeout = 5000"} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatalf("%s: %v", pragma, err)
		}
	}
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestCompilerPreservesCommittedReadyResultWhenCommitAcknowledgementIsLost(t *testing.T) {
	sentinel := errors.New("image-ready commit acknowledgement lost")
	fault := &imageCommitAcknowledgementFault{cause: sentinel}
	db := openImageCommitAcknowledgementDatabase(t, fault)
	fixture := newCompilerFixtureWithAuthority(t, func(t *testing.T, release releasesnapshot.Release, revision deploymentplans.DeploymentPlanRevision) *generatedImageTestAuthority {
		return newGeneratedImageTestAuthorityWithDependencies(t, db, nil, nil, "", release, revision, "app", "", 0)
	})
	imageID := "sha256:" + strings.Repeat("8", 64)
	fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
		return os.WriteFile(flagValue(t, request.Args, "--iidfile"), []byte(imageID), 0o600)
	}
	barriers := 0
	fixture.compiler.afterImageValidated = func() {
		barriers++
		fault.trackTerminal.Store(true)
		fault.armed.Store(true)
	}

	artifact, err := fixture.compile(context.Background())
	if artifact != (Artifact{}) || !IsCompileCode(err, "image_result_unavailable") || !IsCompileUncertain(err) || !errors.Is(err, sentinel) || barriers != 1 {
		t.Fatalf("lost commit acknowledgement artifact=%#v barriers=%d error=%v", artifact, barriers, err)
	}
	persisted := fixture.latestArtifact(t)
	component, componentErr := generatedruntimestate.New(fixture.authority.db).Component(context.Background(), fixture.request.Claim.ApplicationID(), fixture.request.DeploymentID, fixture.request.ComponentName)
	if componentErr != nil || persisted.State != ArtifactReady || persisted.ImageContentID != imageID || persisted.AttemptNumber != 1 || persisted.DiagnosticCode != "" ||
		component.State != generatedruntimestate.ComponentImageReady || component.ImageArtifactID != persisted.ID {
		t.Fatalf("committed Ready result was not retained: artifact=%#v component=%#v error=%v", persisted, component, componentErr)
	}

	fixture.compiler.afterImageValidated = nil
	buildCalls := fixture.runner.calls
	prepared := fixture.builderPrepareCalls()
	validated := len(fixture.validator.specs)
	retried, retryErr := fixture.compile(context.Background())
	if retryErr != nil || retried != persisted {
		t.Fatalf("retry did not attest and reuse committed result: artifact=%#v error=%v", retried, retryErr)
	}
	if fixture.runner.calls != buildCalls || fixture.builderPrepareCalls() != prepared {
		t.Fatalf("retry rebuilt committed image: build=%d/%d builder=%d/%d", buildCalls, fixture.runner.calls, prepared, fixture.builderPrepareCalls())
	}
	if len(fixture.validator.specs) != validated+1 {
		t.Fatalf("retry did not physically attest committed image: validations=%d/%d", validated, len(fixture.validator.specs))
	}
	if attempts := fault.terminalAttempts.Load(); attempts != 0 {
		t.Fatalf("commit acknowledgement loss attempted terminal fallback SQL: %d", attempts)
	}
	after := fixture.latestArtifact(t)
	if after != persisted {
		t.Fatalf("retry changed committed artifact: before=%#v after=%#v", persisted, after)
	}
}
