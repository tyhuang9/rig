// Package appconfig owns immutable, purpose-bound application configuration
// bundles. SQLite contains metadata only; values exist exclusively in protected
// files below the controller data root.
package appconfig

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/pathsecurity"
	"github.com/hostd/hostd/internal/secretfile"
)

const maxBundleBytes = 48 << 10

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var temporarySecretName = regexp.MustCompile(`^\.hostd-secret-[A-Za-z0-9]{8,}$`)

type Error struct {
	Code   string
	Fields map[string]string
}

func (e *Error) Error() string { return "application configuration: " + e.Code }
func IsCode(err error, code string) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}

type Entry struct {
	Key       string `json:"key"`
	Sensitive bool   `json:"sensitive"`
	Value     string `json:"value,omitempty"`
}

type Configuration struct {
	RevisionID     string    `json:"revisionId,omitempty"`
	RevisionNumber int64     `json:"revisionNumber"`
	UpdatedAt      time.Time `json:"updatedAt,omitempty"`
	Entries        []Entry   `json:"entries"`
}

// ExecutionConfiguration is a decrypted, exact configuration revision for a
// single runtime attempt. Environment is secret-bearing caller-owned memory and
// must be cleared after it has been written to protected temporary storage.
type ExecutionConfiguration struct {
	RevisionID     string
	RevisionNumber int64
	Environment    []byte `json:"-"`
}

type ValueInput struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type ReplaceInput struct {
	ExpectedRevisionNumber int64
	Variables              []ValueInput
	Secrets                []ValueInput
	Remove                 []string
}

type bundleEntry struct {
	Sensitive bool   `json:"sensitive"`
	Value     string `json:"value"`
}
type bundle struct {
	Version        int                    `json:"version"`
	ApplicationID  string                 `json:"applicationId"`
	RevisionID     string                 `json:"revisionId"`
	RevisionNumber int64                  `json:"revisionNumber"`
	Entries        map[string]bundleEntry `json:"entries"`
}

type Store struct {
	db                *sql.DB
	root              string
	now               func() time.Time
	mu                sync.Mutex
	appLocks          map[string]*appLock
	beforeTransaction func()
}

type appLock struct {
	mutex sync.Mutex
	users int
}

func New(db *sql.DB, dataRoot string) (*Store, error) {
	if db == nil || dataRoot == "" || pathsecurity.RejectWindowsNamespace(dataRoot) || !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return nil, errors.New("application configuration data root must be absolute and clean")
	}
	return &Store{db: db, root: filepath.Join(dataRoot, "apps"), now: time.Now, appLocks: map[string]*appLock{}}, nil
}

func (s *Store) lock(appID string) func() {
	s.mu.Lock()
	entry := s.appLocks[appID]
	if entry == nil {
		entry = &appLock{}
		s.appLocks[appID] = entry
	}
	entry.users++
	s.mu.Unlock()
	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		s.mu.Lock()
		entry.users--
		if entry.users == 0 {
			delete(s.appLocks, appID)
		}
		s.mu.Unlock()
	}
}

func (s *Store) Get(ctx context.Context, appID string) (Configuration, error) {
	if !validUUID(appID) {
		return Configuration{}, &Error{Code: "app_not_found"}
	}
	var revisionID sql.NullString
	var number int64
	var updated sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT h.revision_id,h.revision_number,h.updated_at FROM application_configuration_heads h JOIN applications a ON a.id=h.app_id AND a.archived_at IS NULL WHERE h.app_id=?`, appID).Scan(&revisionID, &number, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Configuration{}, &Error{Code: "app_not_found"}
	}
	if err != nil {
		return Configuration{}, err
	}
	if number == 0 {
		return Configuration{RevisionNumber: 0, Entries: []Entry{}}, nil
	}
	b, err := s.readBundle(ctx, appID, revisionID.String, number)
	if err != nil {
		return Configuration{}, &Error{Code: "configuration_unavailable"}
	}
	result := Configuration{RevisionID: revisionID.String, RevisionNumber: number, Entries: make([]Entry, 0, len(b.Entries))}
	result.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
	for key, value := range b.Entries {
		entry := Entry{Key: key, Sensitive: value.Sensitive}
		if !value.Sensitive {
			entry.Value = value.Value
		}
		result.Entries = append(result.Entries, entry)
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Key < result.Entries[j].Key })
	return result, nil
}

// ExportRevisionForExecution returns one exact historical revision as a
// Compose-compatible dotenv document. It never silently substitutes the head
// revision. Revision zero is represented by a nonempty comment-only document.
func (s *Store) ExportRevisionForExecution(ctx context.Context, appID, revisionID string, revisionNumber int64) (ExecutionConfiguration, error) {
	if !validUUID(appID) || revisionNumber < 0 || (revisionNumber == 0) != (revisionID == "") {
		return ExecutionConfiguration{}, &Error{Code: "configuration_unavailable"}
	}

	var entries map[string]bundleEntry
	if revisionNumber == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM applications WHERE id=? AND archived_at IS NULL`, appID).Scan(&exists); err != nil {
			return ExecutionConfiguration{}, &Error{Code: "configuration_unavailable"}
		}
		entries = map[string]bundleEntry{}
	} else {
		b, err := s.readBundle(ctx, appID, revisionID, revisionNumber)
		if err != nil {
			return ExecutionConfiguration{}, &Error{Code: "configuration_unavailable"}
		}
		entries = b.Entries
	}

	environment := []byte("# hostd application configuration\n")
	for _, key := range sortedBundleKeys(entries) {
		environment = append(environment, key...)
		environment = append(environment, '=')
		environment = appendDotenvSingleQuoted(environment, entries[key].Value)
		environment = append(environment, '\n')
	}
	return ExecutionConfiguration{RevisionID: revisionID, RevisionNumber: revisionNumber, Environment: environment}, nil
}

// ExportCurrentForExecution resolves the current head once, then delegates to
// exact-revision export so concurrent configuration changes cannot alter it.
func (s *Store) ExportCurrentForExecution(ctx context.Context, appID string) (ExecutionConfiguration, error) {
	if !validUUID(appID) {
		return ExecutionConfiguration{}, &Error{Code: "configuration_unavailable"}
	}
	var revisionID sql.NullString
	var revisionNumber int64
	if err := s.db.QueryRowContext(ctx, `SELECT h.revision_id,h.revision_number FROM application_configuration_heads h JOIN applications a ON a.id=h.app_id AND a.archived_at IS NULL WHERE h.app_id=?`, appID).Scan(&revisionID, &revisionNumber); err != nil {
		return ExecutionConfiguration{}, &Error{Code: "configuration_unavailable"}
	}
	return s.ExportRevisionForExecution(ctx, appID, revisionID.String, revisionNumber)
}

func sortedBundleKeys(entries map[string]bundleEntry) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func appendDotenvSingleQuoted(destination []byte, value string) []byte {
	destination = append(destination, '\'')
	for _, character := range []byte(value) {
		if character == '\'' {
			destination = append(destination, '\\')
		}
		destination = append(destination, character)
	}
	return append(destination, '\'')
}

func (s *Store) Replace(ctx context.Context, appID, actorID string, input ReplaceInput) (Configuration, error) {
	if !validUUID(appID) {
		return Configuration{}, &Error{Code: "app_not_found"}
	}
	if input.ExpectedRevisionNumber < 0 {
		return Configuration{}, invalid("expectedRevisionNumber", "Must be zero or greater")
	}
	unlock := s.lock(appID)
	defer unlock()

	current, err := s.loadHeadBundle(ctx, appID)
	if err != nil {
		return Configuration{}, err
	}
	if current.RevisionNumber != input.ExpectedRevisionNumber {
		return Configuration{}, &Error{Code: "configuration_conflict"}
	}
	entries, err := merge(current.Entries, input)
	if err != nil {
		return Configuration{}, err
	}
	revisionID := uuid.NewString()
	number := current.RevisionNumber + 1
	b := bundle{Version: 1, ApplicationID: appID, RevisionID: revisionID, RevisionNumber: number, Entries: entries}
	plaintext, err := json.Marshal(b)
	if err != nil || len(plaintext) > maxBundleBytes {
		return Configuration{}, invalid("configuration", "Configuration is too large")
	}
	defer clear(plaintext)
	path := s.bundlePath(appID, revisionID)
	if err := s.configurationDirectory(appID, true); err != nil {
		return Configuration{}, &Error{Code: "configuration_unavailable"}
	}
	if err := secretfile.WriteNew(path, purpose(appID, revisionID), plaintext); err != nil {
		if secretfile.WasInstalled(err) {
			_ = secretfile.Remove(path)
		}
		return Configuration{}, &Error{Code: "configuration_unavailable"}
	}
	committed := false
	defer func() {
		if !committed {
			_ = secretfile.Remove(path)
		}
	}()
	if s.beforeTransaction != nil {
		s.beforeTransaction()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Configuration{}, s.classifyWriteError(ctx, appID, input.ExpectedRevisionNumber, err)
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	variables, secrets := counts(entries)
	ref := filepath.ToSlash(filepath.Join("apps", appID, "configuration", revisionID+".secret"))
	if _, err = tx.ExecContext(ctx, `INSERT INTO application_configuration_revisions(id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count) VALUES(?,?,?,?,?,?,?,?)`, revisionID, appID, number, ref, nullable(actorID), now, variables, secrets); err != nil {
		_ = tx.Rollback()
		return Configuration{}, s.classifyWriteError(ctx, appID, input.ExpectedRevisionNumber, err)
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err = tx.ExecContext(ctx, `INSERT INTO application_configuration_entries(revision_id,key,sensitive) VALUES(?,?,?)`, revisionID, key, entries[key].Sensitive); err != nil {
			_ = tx.Rollback()
			return Configuration{}, s.classifyWriteError(ctx, appID, input.ExpectedRevisionNumber, err)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE application_configuration_heads SET revision_id=?,revision_number=?,updated_at=? WHERE app_id=? AND revision_number=?`, revisionID, number, now, appID, input.ExpectedRevisionNumber)
	if err != nil {
		_ = tx.Rollback()
		return Configuration{}, s.classifyWriteError(ctx, appID, input.ExpectedRevisionNumber, err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Configuration{}, &Error{Code: "configuration_conflict"}
	}
	metadata, _ := json.Marshal(map[string]int{"variables": variables, "secrets": secrets, "removed": len(input.Remove)})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(actor_id,action,resource_type,resource_id,metadata_json,created_at) VALUES(?,?,?,?,?,?)`, nullable(actorID), "application.configuration.replace", "application", appID, string(metadata), now); err != nil {
		_ = tx.Rollback()
		return Configuration{}, s.classifyWriteError(ctx, appID, input.ExpectedRevisionNumber, err)
	}
	if err = tx.Commit(); err != nil {
		return Configuration{}, err
	}
	committed = true
	return s.Get(ctx, appID)
}

func (s *Store) classifyWriteError(ctx context.Context, appID string, expected int64, cause error) error {
	var current int64
	if err := s.db.QueryRowContext(ctx, `SELECT revision_number FROM application_configuration_heads WHERE app_id=?`, appID).Scan(&current); err == nil && current != expected {
		return &Error{Code: "configuration_conflict"}
	}
	return cause
}

func (s *Store) loadHeadBundle(ctx context.Context, appID string) (bundle, error) {
	var id sql.NullString
	var number int64
	err := s.db.QueryRowContext(ctx, `SELECT h.revision_id,h.revision_number FROM application_configuration_heads h JOIN applications a ON a.id=h.app_id AND a.archived_at IS NULL WHERE h.app_id=?`, appID).Scan(&id, &number)
	if errors.Is(err, sql.ErrNoRows) {
		return bundle{}, &Error{Code: "app_not_found"}
	}
	if err != nil {
		return bundle{}, err
	}
	if number == 0 {
		return bundle{Version: 1, ApplicationID: appID, Entries: map[string]bundleEntry{}}, nil
	}
	return s.readBundle(ctx, appID, id.String, number)
}

func (s *Store) readBundle(ctx context.Context, appID, revisionID string, number int64) (bundle, error) {
	var ref string
	if err := s.db.QueryRowContext(ctx, `SELECT bundle_ref FROM application_configuration_revisions WHERE id=? AND app_id=? AND revision_number=?`, revisionID, appID, number).Scan(&ref); err != nil {
		return bundle{}, err
	}
	expected := filepath.ToSlash(filepath.Join("apps", appID, "configuration", revisionID+".secret"))
	if ref != expected {
		return bundle{}, errors.New("invalid configuration bundle reference")
	}
	if err := s.configurationDirectory(appID, false); err != nil {
		return bundle{}, err
	}
	plaintext, err := secretfile.Read(s.bundlePath(appID, revisionID), purpose(appID, revisionID))
	if err != nil {
		return bundle{}, err
	}
	defer clear(plaintext)
	if len(plaintext) > maxBundleBytes {
		return bundle{}, errors.New("configuration bundle too large")
	}
	var b bundle
	if err := rejectDuplicateJSONKeys(plaintext); err != nil {
		return bundle{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&b); err != nil {
		return bundle{}, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return bundle{}, errors.New("configuration bundle has trailing content")
	}
	if b.Version != 1 || b.ApplicationID != appID || b.RevisionID != revisionID || b.RevisionNumber != number || b.Entries == nil {
		return bundle{}, errors.New("configuration bundle metadata mismatch")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key,sensitive FROM application_configuration_entries WHERE revision_id=? ORDER BY key`, revisionID)
	if err != nil {
		return bundle{}, err
	}
	metadata := map[string]bool{}
	for rows.Next() {
		var key string
		var sensitive bool
		if err := rows.Scan(&key, &sensitive); err != nil {
			rows.Close()
			return bundle{}, err
		}
		metadata[key] = sensitive
	}
	if err := rows.Close(); err != nil {
		return bundle{}, err
	}
	if len(metadata) != len(b.Entries) {
		return bundle{}, errors.New("configuration bundle entry metadata mismatch")
	}
	for key, value := range b.Entries {
		if sensitive, ok := metadata[key]; !ok || sensitive != value.Sensitive {
			return bundle{}, errors.New("configuration bundle entry metadata mismatch")
		}
	}
	return b, nil
}

func (s *Store) bundlePath(appID, revisionID string) string {
	return filepath.Join(s.root, appID, "configuration", revisionID+".secret")
}

func safeDirectory(path string, create bool) error {
	if pathsecurity.RejectWindowsNamespace(path) {
		return errors.New("unsafe application configuration path namespace")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed configuration path is not a directory")
	}
	return nil
}

func (s *Store) configurationDirectory(appID string, create bool) error {
	if !validUUID(appID) {
		return errors.New("invalid application configuration identity")
	}
	if err := safeDirectory(s.root, create); err != nil {
		return err
	}
	appRoot := filepath.Join(s.root, appID)
	if err := safeDirectory(appRoot, create); err != nil {
		return err
	}
	return safeDirectory(filepath.Join(appRoot, "configuration"), create)
}

func validUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func rejectDuplicateJSONKeys(document []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	var value func() error
	value = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("configuration bundle contains duplicate object keys")
				}
				seen[key] = true
				if err := value(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("invalid configuration bundle object")
			}
		case '[':
			for decoder.More() {
				if err := value(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("invalid configuration bundle array")
			}
		default:
			return errors.New("invalid configuration bundle delimiter")
		}
		return nil
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("configuration bundle has trailing content")
		}
		return err
	}
	return nil
}
func purpose(appID, revisionID string) string {
	return "hostd/application-configuration/v1/" + appID + "/" + revisionID
}

func merge(existing map[string]bundleEntry, input ReplaceInput) (map[string]bundleEntry, error) {
	if len(input.Variables)+len(input.Secrets) > 256 || len(input.Remove) > 256 {
		return nil, invalid("configuration", "Too many entries")
	}
	result := make(map[string]bundleEntry)
	seen := make(map[string]string)
	for _, key := range input.Remove {
		if err := validateKey(key); err != nil {
			return nil, invalid("remove", err.Error())
		}
		if _, ok := seen[key]; ok {
			return nil, invalid("remove", "Contains duplicate keys")
		}
		seen[key] = "remove"
	}
	removed := seen
	for key, value := range existing {
		if value.Sensitive && removed[key] == "" {
			result[key] = value
		}
	}
	for _, item := range input.Variables {
		if err := validateItem(item); err != nil {
			return nil, invalid("variables", err.Error())
		}
		if previous := seen[item.Key]; previous != "" {
			return nil, invalid("variables", "Contains duplicate keys")
		}
		seen[item.Key] = "variable"
		result[item.Key] = bundleEntry{Value: item.Value}
	}
	for _, item := range input.Secrets {
		if err := validateItem(item); err != nil {
			return nil, invalid("secrets", err.Error())
		}
		if item.Value == "" {
			return nil, invalid("secrets", "Submitted secrets cannot be empty")
		}
		if seen[item.Key] != "" {
			return nil, invalid("secrets", "Keys must be unique across variables, secrets, and removals")
		}
		seen[item.Key] = "secret"
		result[item.Key] = bundleEntry{Sensitive: true, Value: item.Value}
	}
	if len(result) > 256 {
		return nil, invalid("configuration", "Too many entries")
	}
	return result, nil
}

func validateKey(key string) error {
	if !utf8.ValidString(key) || !envKey.MatchString(key) || len(key) > 128 {
		return errors.New("Keys must use portable environment variable syntax")
	}
	return nil
}
func validateItem(item ValueInput) error {
	if err := validateKey(item.Key); err != nil {
		return err
	}
	if !utf8.ValidString(item.Value) {
		return errors.New("Values must be valid UTF-8")
	}
	if strings.ContainsRune(item.Value, 0) {
		return errors.New("Values cannot contain NUL bytes")
	}
	if len(item.Value) > 8<<10 {
		return errors.New("A value is too large")
	}
	return nil
}
func invalid(field, message string) error {
	return &Error{Code: "invalid_configuration", Fields: map[string]string{field: message}}
}
func counts(entries map[string]bundleEntry) (variables, secrets int) {
	for _, e := range entries {
		if e.Sensitive {
			secrets++
		} else {
			variables++
		}
	}
	return
}
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// Recover verifies all referenced bundles and removes only recognized orphaned
// bundle or temporary files below the managed configuration root.
func (s *Store) Recover(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,app_id,revision_number,bundle_ref FROM application_configuration_revisions ORDER BY app_id,revision_number`)
	if err != nil {
		return err
	}
	type reference struct {
		id, app, ref string
		number       int64
	}
	var refs []reference
	known := map[string]bool{}
	apps := map[string]bool{}
	for rows.Next() {
		var r reference
		if err := rows.Scan(&r.id, &r.app, &r.number, &r.ref); err != nil {
			rows.Close()
			return err
		}
		if !validUUID(r.app) || !validUUID(r.id) {
			rows.Close()
			return errors.New("invalid configuration bundle identity")
		}
		expected := filepath.ToSlash(filepath.Join("apps", r.app, "configuration", r.id+".secret"))
		if r.ref != expected {
			rows.Close()
			return errors.New("invalid configuration bundle reference")
		}
		known[filepath.Clean(s.bundlePath(r.app, r.id))] = true
		apps[r.app] = true
		refs = append(refs, r)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, r := range refs {
		if _, err := s.readBundle(ctx, r.app, r.id, r.number); err != nil {
			return fmt.Errorf("validate configuration bundle: %w", err)
		}
	}
	headRows, err := s.db.QueryContext(ctx, `SELECT app_id FROM application_configuration_heads`)
	if err != nil {
		return err
	}
	for headRows.Next() {
		var app string
		if err := headRows.Scan(&app); err != nil {
			headRows.Close()
			return err
		}
		if !validUUID(app) {
			headRows.Close()
			return errors.New("invalid application configuration identity")
		}
		apps[app] = true
	}
	if err := headRows.Close(); err != nil {
		return err
	}
	if err := safeDirectory(s.root, true); err != nil {
		return err
	}
	for app := range apps {
		configurationRoot := filepath.Join(s.root, app, "configuration")
		appRoot := filepath.Join(s.root, app)
		if _, err := os.Lstat(appRoot); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := safeDirectory(appRoot, false); err != nil {
			return err
		}
		if _, err := os.Lstat(configurationRoot); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := safeDirectory(configurationRoot, false); err != nil {
			return err
		}
		entries, err := os.ReadDir(configurationRoot)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			path := filepath.Join(configurationRoot, entry.Name())
			if entry.IsDir() {
				return errors.New("unrecognized directory in application configuration directory")
			}
			if known[filepath.Clean(path)] {
				continue
			}
			name := entry.Name()
			orphanRevision := strings.TrimSuffix(name, ".secret")
			validOrphan := strings.HasSuffix(name, ".secret") && validUUID(orphanRevision)
			validTemporary := temporarySecretName.MatchString(name)
			if !validOrphan && !validTemporary {
				return errors.New("unrecognized file in application configuration directory")
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}
