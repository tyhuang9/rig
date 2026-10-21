package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"weak"

	"github.com/hostd/hostd/internal/controllerowner"
	"github.com/hostd/hostd/internal/pathsecurity"
	"modernc.org/sqlite"
)

const generatedDomainDatabaseFilename = "control.db"

var errGeneratedDomainConnectionSpent = errors.New("generated domain database connection is unavailable")
var generatedDomainConstructionGate = make(chan struct{}, 1)

type generatedDomainCapture struct {
	dataRoot     string
	working      string
	databasePath string
	root         os.FileInfo
	databaseFile os.FileInfo
	file         *os.File
	lease        *controllerowner.Lease
}

type generatedDomainPendingDatabase struct {
	database  *sql.DB
	connector *generatedDomainConnector
	capture   *generatedDomainCapture
}

type generatedDomainConnector struct {
	sync.Mutex
	driver     *sqlite.Driver
	dsn        string
	used       bool
	connection driver.Conn
	sealed     generatedDomainSealedDriver
}

type generatedDomainSealedDriver struct{}

type generatedDomainProvenance struct {
	dataRoot     string
	working      string
	databasePath string
	root         os.FileInfo
	databaseFile os.FileInfo
	lease        *controllerowner.Lease
	connector    *generatedDomainConnector
}

var generatedDomainProvenances = struct {
	sync.Mutex
	values map[weak.Pointer[sql.DB]]*generatedDomainProvenance
}{values: make(map[weak.Pointer[sql.DB]]*generatedDomainProvenance)}

// OpenForGeneratedDomain opens the controller database only after binding the
// exact file and fixed controller owner lease. It never accepts a caller DSN,
// VFS, connection hook, or replacement proof.
func OpenForGeneratedDomain(ctx context.Context, dataRoot string, lease *controllerowner.Lease) (*sql.DB, error) {
	if ctx == nil {
		return nil, errors.New("generated domain database context is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := acquireGeneratedDomainConstruction(ctx); err != nil {
		return nil, err
	}
	constructionHeld := true
	defer func() {
		if constructionHeld {
			releaseGeneratedDomainConstruction()
		}
	}()
	capture, err := captureGeneratedDomainDatabase(dataRoot, lease)
	if err != nil {
		return nil, err
	}
	ownedCapture := true
	defer func() {
		if ownedCapture {
			_ = capture.close()
		}
	}()
	pending, err := connectGeneratedDomainDatabase(ctx, capture)
	if err != nil {
		return nil, err
	}
	issued := false
	defer func() {
		if !issued {
			_ = pending.database.Close()
		}
	}()
	if err := pending.bind(ctx); err != nil {
		return nil, err
	}
	releaseGeneratedDomainConstruction()
	constructionHeld = false
	if err := pending.initialize(ctx); err != nil {
		return nil, err
	}
	if err := pending.issue(ctx); err != nil {
		return nil, err
	}
	ownedCapture = false
	issued = true
	return pending.database, nil
}

func acquireGeneratedDomainConstruction(ctx context.Context) error {
	select {
	case generatedDomainConstructionGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseGeneratedDomainConstruction() {
	<-generatedDomainConstructionGate
}

// GeneratedDomainFileIdentity returns the immutable opening identity only for
// an exact live database issued by OpenForGeneratedDomain. The returned value
// is configuration provenance, not filesystem mutation authority.
func GeneratedDomainFileIdentity(ctx context.Context, database *sql.DB, dataRoot string) (os.FileInfo, error) {
	if ctx == nil || database == nil {
		return nil, errors.New("generated domain database provenance is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := weak.Make(database)
	generatedDomainProvenances.Lock()
	provenance := generatedDomainProvenances.values[key]
	generatedDomainProvenances.Unlock()
	if provenance == nil || provenance.dataRoot != dataRoot {
		runtime.KeepAlive(database)
		return nil, errors.New("generated domain database provenance is unavailable")
	}
	if err := provenance.validate(ctx, database); err != nil {
		runtime.KeepAlive(database)
		return nil, err
	}
	runtime.KeepAlive(database)
	return provenance.databaseFile, nil
}

func captureGeneratedDomainDatabase(dataRoot string, lease *controllerowner.Lease) (*generatedDomainCapture, error) {
	working, err := generatedDomainWorkingDirectory(dataRoot)
	if err != nil || lease == nil || lease.ValidateForDirectory(working) != nil {
		return nil, errors.New("generated domain database boundary is invalid")
	}
	root, err := os.Lstat(dataRoot)
	if err != nil || !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("generated domain data root is unsafe")
	}
	databasePath := filepath.Join(dataRoot, generatedDomainDatabaseFilename)
	if filepath.Dir(databasePath) != dataRoot || filepath.Clean(databasePath) != databasePath {
		return nil, errors.New("generated domain database path is invalid")
	}
	file, _, err := openGeneratedDomainDatabaseFile(databasePath, true)
	if err != nil {
		return nil, errors.New("generated domain database file is unavailable")
	}
	capture := &generatedDomainCapture{
		dataRoot: dataRoot, working: working, databasePath: databasePath,
		root: root, file: file, lease: lease,
	}
	databaseFile, err := capture.recheck()
	if err != nil {
		_ = capture.close()
		return nil, err
	}
	capture.databaseFile = databaseFile
	return capture, nil
}

func connectGeneratedDomainDatabase(ctx context.Context, capture *generatedDomainCapture) (*generatedDomainPendingDatabase, error) {
	if ctx == nil || capture == nil || capture.file == nil {
		return nil, errors.New("generated domain database capture is unavailable")
	}
	if capture.databasePath != filepath.Join(capture.dataRoot, generatedDomainDatabaseFilename) ||
		strings.ContainsAny(capture.databasePath, "?#\x00") {
		return nil, errors.New("generated domain database capture is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	registered, err := sql.Open("sqlite", "")
	if err != nil {
		return nil, errors.New("generated domain SQLite driver is unavailable")
	}
	databaseDriver, ok := registered.Driver().(*sqlite.Driver)
	closeErr := registered.Close()
	if !ok || databaseDriver == nil || closeErr != nil {
		return nil, errors.New("generated domain SQLite driver is unavailable")
	}
	connector := &generatedDomainConnector{driver: databaseDriver, dsn: capture.databasePath}
	database := sql.OpenDB(connector)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	pending := &generatedDomainPendingDatabase{database: database, connector: connector, capture: capture}
	if err := pending.validateConnection(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	return pending, nil
}

func (pending *generatedDomainPendingDatabase) bind(ctx context.Context) error {
	if pending == nil || pending.capture == nil || pending.database == nil || pending.connector == nil {
		return errors.New("generated domain database binding is unavailable")
	}
	if _, err := pending.capture.recheck(); err != nil {
		return err
	}
	return pending.validateConnection(ctx)
}

func (pending *generatedDomainPendingDatabase) initialize(ctx context.Context) error {
	if pending == nil || pending.capture == nil || pending.database == nil {
		return errors.New("generated domain database initialization is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := pending.capture.recheck(); err != nil {
		return err
	}
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := pending.database.ExecContext(ctx, pragma); err != nil {
			return errors.New("generated domain database initialization failed")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := Migrate(pending.database); err != nil {
		return errors.New("generated domain database migration failed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := pending.capture.recheck(); err != nil {
		return err
	}
	return pending.validateConnection(ctx)
}

func (pending *generatedDomainPendingDatabase) issue(ctx context.Context) error {
	if pending == nil || pending.capture == nil || pending.capture.file == nil || pending.database == nil {
		return errors.New("generated domain database issuance is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := pending.capture.recheck(); err != nil {
		return err
	}
	if err := pending.validateConnection(ctx); err != nil {
		return err
	}
	if err := pending.capture.file.Close(); err != nil {
		return errors.New("generated domain database capture close failed")
	}
	pending.capture.file = nil
	provenance := &generatedDomainProvenance{
		dataRoot: pending.capture.dataRoot, working: pending.capture.working,
		databasePath: pending.capture.databasePath, root: pending.capture.root,
		databaseFile: pending.capture.databaseFile, lease: pending.capture.lease,
		connector: pending.connector,
	}
	if err := provenance.validate(ctx, pending.database); err != nil {
		return err
	}
	key := weak.Make(pending.database)
	generatedDomainProvenances.Lock()
	if generatedDomainProvenances.values[key] != nil {
		generatedDomainProvenances.Unlock()
		return errors.New("generated domain database provenance already exists")
	}
	generatedDomainProvenances.values[key] = provenance
	generatedDomainProvenances.Unlock()
	runtime.AddCleanup(pending.database, cleanupGeneratedDomainProvenance, key)
	runtime.KeepAlive(pending.database)
	return nil
}

func (pending *generatedDomainPendingDatabase) validateConnection(ctx context.Context) error {
	if pending == nil || pending.database == nil || pending.connector == nil {
		return errors.New("generated domain database connection is unavailable")
	}
	return validateGeneratedDomainRawConnection(ctx, pending.database, pending.connector)
}

func (connector *generatedDomainConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if connector == nil || ctx == nil || connector.driver == nil || connector.dsn == "" {
		return nil, errGeneratedDomainConnectionSpent
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	connector.Lock()
	if connector.used {
		connector.Unlock()
		return nil, errGeneratedDomainConnectionSpent
	}
	connector.used = true
	connector.Unlock()
	connection, err := connector.driver.Open(connector.dsn)
	if err != nil {
		return nil, errors.New("generated domain database connection failed")
	}
	if err := ctx.Err(); err != nil {
		_ = connection.Close()
		return nil, err
	}
	connector.Lock()
	connector.connection = connection
	connector.Unlock()
	return connection, nil
}

func (connector *generatedDomainConnector) Driver() driver.Driver {
	if connector == nil {
		return generatedDomainSealedDriver{}
	}
	return connector.sealed
}

func (generatedDomainSealedDriver) Open(string) (driver.Conn, error) {
	return nil, errGeneratedDomainConnectionSpent
}

func (connector *generatedDomainConnector) matches(connection any) bool {
	if connector == nil || connection == nil {
		return false
	}
	connector.Lock()
	defer connector.Unlock()
	return connector.connection != nil && connection == connector.connection
}

func validateGeneratedDomainRawConnection(ctx context.Context, database *sql.DB, connector *generatedDomainConnector) error {
	if ctx == nil || database == nil || connector == nil {
		return errors.New("generated domain database connection is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return errors.New("generated domain database connection is unavailable")
	}
	defer connection.Close()
	if err := connection.Raw(func(raw any) error {
		if !connector.matches(raw) {
			return errors.New("generated domain database connection changed")
		}
		return nil
	}); err != nil {
		return errors.New("generated domain database connection is unavailable")
	}
	return nil
}

func (capture *generatedDomainCapture) recheck() (os.FileInfo, error) {
	if capture == nil || capture.file == nil || capture.lease == nil ||
		capture.lease.ValidateForDirectory(capture.working) != nil {
		return nil, errors.New("generated domain database capture changed")
	}
	root, err := os.Lstat(capture.dataRoot)
	if err != nil || !root.IsDir() || root.Mode()&os.ModeSymlink != 0 ||
		!sameGeneratedDomainFile(capture.root, root) {
		return nil, errors.New("generated domain data root changed")
	}
	databaseFile, err := validateGeneratedDomainDatabaseFile(capture.file, capture.databasePath)
	if err != nil {
		return nil, errors.New("generated domain database file changed")
	}
	if capture.databaseFile != nil && !sameGeneratedDomainFile(capture.databaseFile, databaseFile) {
		return nil, errors.New("generated domain database file changed")
	}
	return databaseFile, nil
}

func (capture *generatedDomainCapture) close() error {
	if capture == nil || capture.file == nil {
		return nil
	}
	err := capture.file.Close()
	capture.file = nil
	return err
}

func (provenance *generatedDomainProvenance) validate(ctx context.Context, database *sql.DB) error {
	if provenance == nil || ctx == nil || database == nil || provenance.lease == nil ||
		provenance.lease.ValidateForDirectory(provenance.working) != nil {
		return errors.New("generated domain database provenance is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.Lstat(provenance.dataRoot)
	if err != nil || !root.IsDir() || root.Mode()&os.ModeSymlink != 0 ||
		!sameGeneratedDomainFile(provenance.root, root) {
		return errors.New("generated domain data root changed")
	}
	databaseFile, err := inspectGeneratedDomainDatabasePath(provenance.databasePath)
	if err != nil || !sameGeneratedDomainFile(provenance.databaseFile, databaseFile) {
		return errors.New("generated domain database file changed")
	}
	if err := validateGeneratedDomainRawConnection(ctx, database, provenance.connector); err != nil {
		return err
	}
	if provenance.lease.ValidateForDirectory(provenance.working) != nil {
		return errors.New("generated domain database provenance is unavailable")
	}
	return nil
}

func inspectGeneratedDomainDatabasePath(path string) (os.FileInfo, error) {
	file, _, err := openGeneratedDomainDatabaseFile(path, false)
	if err != nil {
		return nil, err
	}
	info, validateErr := validateGeneratedDomainDatabaseFile(file, path)
	closeErr := file.Close()
	if validateErr != nil || closeErr != nil {
		return nil, errors.New("generated domain database file is unavailable")
	}
	return info, nil
}

func generatedDomainWorkingDirectory(dataRoot string) (string, error) {
	if dataRoot == "" || pathsecurity.RejectWindowsNamespace(dataRoot) ||
		strings.ContainsAny(dataRoot, "?#\x00") || !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return "", errors.New("generated domain data root is invalid")
	}
	working := filepath.Join(dataRoot, "runtime", "generated-builder", "controller-working")
	if filepath.Clean(working) != working || filepath.Dir(filepath.Dir(filepath.Dir(working))) != dataRoot {
		return "", errors.New("generated domain controller working directory is invalid")
	}
	return working, nil
}

func sameGeneratedDomainFile(left, right os.FileInfo) bool {
	return left != nil && right != nil && left.Mode() == right.Mode() && os.SameFile(left, right)
}

func cleanupGeneratedDomainProvenance(key weak.Pointer[sql.DB]) {
	generatedDomainProvenances.Lock()
	delete(generatedDomainProvenances.values, key)
	generatedDomainProvenances.Unlock()
}
