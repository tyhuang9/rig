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
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/pathsecurity"
	"github.com/hostd/hostd/internal/secretfile"
)

const maxBundleBytes = 48 << 10

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var temporarySecretName = regexp.MustCompile(`^\.hostd-secret-[A-Za-z0-9]{8,}$`)

type Phase string

const (
	PhaseRuntime   Phase = "runtime"
	PhaseBuild     Phase = "build"
	PhaseMigration Phase = "migration"
)

type Sensitivity string

const (
	SensitivityPublic Sensitivity = "public"
	SensitivitySecret Sensitivity = "secret"
)

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
	Key             string      `json:"key"`
	Sensitive       bool        `json:"sensitive"`
	Value           string      `json:"value,omitempty"`
	Phase           Phase       `json:"phase,omitempty"`
	TargetComponent string      `json:"targetComponent,omitempty"`
	Sensitivity     Sensitivity `json:"sensitivity,omitempty"`
}

type Configuration struct {
	RevisionID                   string    `json:"revisionId,omitempty"`
	RevisionNumber               int64     `json:"revisionNumber"`
	FormatVersion                int       `json:"formatVersion,omitempty"`
	DeploymentPlanRevisionID     string    `json:"deploymentPlanRevisionId,omitempty"`
	DeploymentPlanRevisionNumber int64     `json:"deploymentPlanRevisionNumber,omitempty"`
	UpdatedAt                    time.Time `json:"updatedAt,omitempty"`
	Entries                      []Entry   `json:"entries"`
}

type RevisionIdentity struct {
	RevisionID                   string
	RevisionNumber               int64
	FormatVersion                int
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
}

// ExecutionConfiguration is a decrypted, exact configuration revision for a
// single runtime attempt. Environment is secret-bearing caller-owned memory and
// must be cleared after it has been written to protected temporary storage.
type ExecutionConfiguration struct {
	RevisionID        string
	RevisionNumber    int64
	Environment       []byte         `json:"-"`
	SecretOrigins     []SecretOrigin `json:"-"`
	PublicBuildValues []ValueInput   `json:"-"`
}

// SecretOrigin is caller-owned metadata used to keep values originating from a
// protected configuration secret out of durable policy findings. Key and Value
// are independent buffers and must be cleared with ExecutionConfiguration.Clear.
type SecretOrigin struct {
	RevisionID     string
	RevisionNumber int64
	Key            []byte `json:"-"`
	Value          []byte `json:"-"`
}

// Clear releases and overwrites the decrypted material owned by one execution
// export. It is safe to call more than once.
func (c *ExecutionConfiguration) Clear() {
	clear(c.Environment)
	c.Environment = nil
	for index := range c.SecretOrigins {
		clear(c.SecretOrigins[index].Key)
		clear(c.SecretOrigins[index].Value)
		c.SecretOrigins[index] = SecretOrigin{}
	}
	clear(c.SecretOrigins)
	c.SecretOrigins = nil
	for index := range c.PublicBuildValues {
		c.PublicBuildValues[index] = ValueInput{}
	}
	clear(c.PublicBuildValues)
	c.PublicBuildValues = nil
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

type ComponentTarget struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

type ScopedKey struct {
	Phase     Phase
	Component string
	Key       string
}

type ScopedValueInput struct {
	ScopedKey
	Sensitivity Sensitivity
	Value       *string
}

type ScopedReplaceInput struct {
	ExpectedRevisionNumber int64
	PlanRevisionID         string
	PlanRevisionNumber     int64
	Components             []ComponentTarget
	Entries                []ScopedValueInput
	Remove                 []ScopedKey
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

type scopedBundleEntry struct {
	Phase       Phase       `json:"phase"`
	Component   string      `json:"component"`
	Key         string      `json:"key"`
	Sensitivity Sensitivity `json:"sensitivity"`
	Value       string      `json:"value"`
}

type scopedBundle struct {
	Version                      int                 `json:"version"`
	ApplicationID                string              `json:"applicationId"`
	RevisionID                   string              `json:"revisionId"`
	RevisionNumber               int64               `json:"revisionNumber"`
	DeploymentPlanRevisionID     string              `json:"deploymentPlanRevisionId"`
	DeploymentPlanRevisionNumber int64               `json:"deploymentPlanRevisionNumber"`
	Components                   []ComponentTarget   `json:"components"`
	Entries                      []scopedBundleEntry `json:"entries"`
}

type revisionBundle struct {
	Version int
	Legacy  bundle
	Scoped  scopedBundle
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
	stored, err := s.readRevisionBundle(ctx, appID, revisionID.String, number)
	if err != nil {
		return Configuration{}, &Error{Code: "configuration_unavailable"}
	}
	result := Configuration{RevisionID: revisionID.String, RevisionNumber: number, FormatVersion: stored.Version}
	result.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
	if stored.Version == 1 {
		result.Entries = make([]Entry, 0, len(stored.Legacy.Entries))
		for key, value := range stored.Legacy.Entries {
			entry := Entry{Key: key, Sensitive: value.Sensitive}
			if !value.Sensitive {
				entry.Value = value.Value
			}
			result.Entries = append(result.Entries, entry)
		}
		sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Key < result.Entries[j].Key })
		return result, nil
	}
	result.DeploymentPlanRevisionID = stored.Scoped.DeploymentPlanRevisionID
	result.DeploymentPlanRevisionNumber = stored.Scoped.DeploymentPlanRevisionNumber
	result.Entries = make([]Entry, 0, len(stored.Scoped.Entries))
	for _, value := range stored.Scoped.Entries {
		entry := Entry{Key: value.Key, Sensitive: value.Sensitivity == SensitivitySecret, Phase: value.Phase, TargetComponent: value.Component, Sensitivity: value.Sensitivity}
		if value.Sensitivity == SensitivityPublic {
			entry.Value = value.Value
		}
		result.Entries = append(result.Entries, entry)
	}
	return result, nil
}

// RevisionIdentity resolves the current head using metadata only. It does not
// read or decrypt protected configuration values.
func (s *Store) RevisionIdentity(ctx context.Context, appID string) (RevisionIdentity, error) {
	if !validUUID(appID) {
		return RevisionIdentity{}, &Error{Code: "app_not_found"}
	}
	var revisionID sql.NullString
	var revisionNumber int64
	err := s.db.QueryRowContext(ctx, `SELECT h.revision_id,h.revision_number FROM application_configuration_heads h JOIN applications a ON a.id=h.app_id AND a.archived_at IS NULL WHERE h.app_id=?`, appID).Scan(&revisionID, &revisionNumber)
	if errors.Is(err, sql.ErrNoRows) {
		return RevisionIdentity{}, &Error{Code: "app_not_found"}
	}
	if err != nil {
		return RevisionIdentity{}, err
	}
	if revisionNumber == 0 {
		return RevisionIdentity{}, nil
	}
	return s.revisionIdentity(ctx, appID, revisionID.String, revisionNumber)
}

// ExactRevisionIdentity validates one exact immutable pin using metadata only.
func (s *Store) ExactRevisionIdentity(ctx context.Context, appID, revisionID string, revisionNumber int64) (RevisionIdentity, error) {
	if !validUUID(appID) || !validUUID(revisionID) || revisionNumber <= 0 {
		return RevisionIdentity{}, &Error{Code: "configuration_unavailable"}
	}
	identity, err := s.revisionIdentity(ctx, appID, revisionID, revisionNumber)
	if err != nil {
		return RevisionIdentity{}, &Error{Code: "configuration_unavailable"}
	}
	return identity, nil
}

func (s *Store) revisionIdentity(ctx context.Context, appID, revisionID string, revisionNumber int64) (RevisionIdentity, error) {
	var version int
	var planID sql.NullString
	var planNumber sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT bundle_version,deployment_plan_revision_id,deployment_plan_revision_number FROM application_configuration_revisions WHERE id=? AND app_id=? AND revision_number=?`, revisionID, appID, revisionNumber).Scan(&version, &planID, &planNumber)
	if err != nil {
		return RevisionIdentity{}, err
	}
	return RevisionIdentity{RevisionID: revisionID, RevisionNumber: revisionNumber, FormatVersion: version, DeploymentPlanRevisionID: planID.String, DeploymentPlanRevisionNumber: planNumber.Int64}, nil
}

// ExportRevisionForExecution returns one exact historical revision as a
// Compose-compatible dotenv document. It never silently substitutes the head
// revision. Revision zero is represented by a nonempty comment-only document.
func (s *Store) ExportRevisionForExecution(ctx context.Context, appID, revisionID string, revisionNumber int64) (ExecutionConfiguration, error) {
	return s.exportRevisionForExecution(ctx, appID, revisionID, revisionNumber, nil, false)
}

// ExportRevisionKeysForExecution returns one exact historical revision while
// exposing only the explicitly accepted environment keys. Every requested key
// must exist in the pinned revision; missing or malformed allowlists fail
// closed instead of silently broadening or weakening migration configuration.
func (s *Store) ExportRevisionKeysForExecution(ctx context.Context, appID, revisionID string, revisionNumber int64, allowedKeys []string) (ExecutionConfiguration, error) {
	if len(allowedKeys) > 8 {
		return ExecutionConfiguration{}, &Error{Code: "configuration_unavailable"}
	}
	keys := append([]string(nil), allowedKeys...)
	sort.Strings(keys)
	for index, key := range keys {
		if validateKey(key) != nil || (index > 0 && keys[index-1] == key) {
			return ExecutionConfiguration{}, &Error{Code: "configuration_unavailable"}
		}
	}
	return s.exportRevisionForExecution(ctx, appID, revisionID, revisionNumber, keys, true)
}

func (s *Store) exportRevisionForExecution(ctx context.Context, appID, revisionID string, revisionNumber int64, allowedKeys []string, filtered bool) (ExecutionConfiguration, error) {
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
		stored, err := s.readRevisionBundle(ctx, appID, revisionID, revisionNumber)
		if err != nil {
			return ExecutionConfiguration{}, &Error{Code: "configuration_unavailable"}
		}
		if stored.Version != 1 {
			return ExecutionConfiguration{}, &Error{Code: "configuration_review_required"}
		}
		entries = stored.Legacy.Entries
	}

	keys := sortedBundleKeys(entries)
	if filtered {
		keys = allowedKeys
		for _, key := range keys {
			if _, exists := entries[key]; !exists {
				return ExecutionConfiguration{}, &Error{Code: "configuration_unavailable"}
			}
		}
	}
	environment := []byte("# hostd application configuration\n")
	secretOrigins := make([]SecretOrigin, 0)
	for _, key := range keys {
		entry := entries[key]
		environment = append(environment, key...)
		environment = append(environment, '=')
		environment = appendDotenvSingleQuoted(environment, entry.Value)
		environment = append(environment, '\n')
		if entry.Sensitive && entry.Value != "" {
			secretOrigins = append(secretOrigins, SecretOrigin{
				RevisionID:     revisionID,
				RevisionNumber: revisionNumber,
				Key:            append([]byte(nil), key...),
				Value:          append([]byte(nil), entry.Value...),
			})
		}
	}
	return ExecutionConfiguration{RevisionID: revisionID, RevisionNumber: revisionNumber, Environment: environment, SecretOrigins: secretOrigins}, nil
}

// ExportComponentRuntimeForExecution exports only the runtime scope for one
// component and one exact configuration/plan pin.
func (s *Store) ExportComponentRuntimeForExecution(ctx context.Context, appID, configurationRevisionID string, configurationRevisionNumber int64, planRevisionID string, planRevisionNumber int64, component string) (ExecutionConfiguration, error) {
	stored, err := s.scopedRevisionForExport(ctx, appID, configurationRevisionID, configurationRevisionNumber, planRevisionID, planRevisionNumber, component, false)
	if err != nil {
		return ExecutionConfiguration{}, err
	}
	if stored.Version == 1 {
		return exportEmptyOrLegacyReview(stored, configurationRevisionID, configurationRevisionNumber)
	}
	entries := make([]scopedBundleEntry, 0)
	for _, entry := range stored.Scoped.Entries {
		if entry.Phase == PhaseRuntime && entry.Component == component {
			entries = append(entries, entry)
		}
	}
	return exportScopedEntries(configurationRevisionID, configurationRevisionNumber, entries), nil
}

// ExportComponentBuildForExecution exports the explicitly public build scope
// for one component. Scoped bundle validation guarantees these entries are
// non-secret.
func (s *Store) ExportComponentBuildForExecution(ctx context.Context, appID, configurationRevisionID string, configurationRevisionNumber int64, planRevisionID string, planRevisionNumber int64, component string) (ExecutionConfiguration, error) {
	stored, err := s.scopedRevisionForExport(ctx, appID, configurationRevisionID, configurationRevisionNumber, planRevisionID, planRevisionNumber, component, false)
	if err != nil {
		return ExecutionConfiguration{}, err
	}
	if stored.Version == 1 {
		return ExecutionConfiguration{RevisionID: configurationRevisionID, RevisionNumber: configurationRevisionNumber}, nil
	}
	entries := make([]scopedBundleEntry, 0)
	for _, entry := range stored.Scoped.Entries {
		if entry.Phase == PhaseBuild && entry.Component == component {
			entries = append(entries, entry)
		}
	}
	result := ExecutionConfiguration{RevisionID: configurationRevisionID, RevisionNumber: configurationRevisionNumber}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	result.PublicBuildValues = make([]ValueInput, 0, len(entries))
	for _, entry := range entries {
		result.PublicBuildValues = append(result.PublicBuildValues, ValueInput{Key: entry.Key, Value: entry.Value})
	}
	return result, nil
}

// ExportComponentMigrationForExecution exports only explicitly selected keys
// from the runtime or migration scope of one server component. A key present
// in both eligible phases is ambiguous and fails closed.
func (s *Store) ExportComponentMigrationForExecution(ctx context.Context, appID, configurationRevisionID string, configurationRevisionNumber int64, planRevisionID string, planRevisionNumber int64, component string, allowedKeys []string) (ExecutionConfiguration, error) {
	keys, err := validatedAllowedKeys(allowedKeys)
	if err != nil {
		return ExecutionConfiguration{}, err
	}
	stored, err := s.scopedRevisionForExport(ctx, appID, configurationRevisionID, configurationRevisionNumber, planRevisionID, planRevisionNumber, component, true)
	if err != nil {
		return ExecutionConfiguration{}, err
	}
	if stored.Version == 1 {
		if len(stored.Legacy.Entries) == 0 {
			if len(keys) != 0 {
				return ExecutionConfiguration{}, &Error{Code: "configuration_unavailable"}
			}
			return exportScopedEntries(configurationRevisionID, configurationRevisionNumber, nil), nil
		}
		legacy := make([]scopedBundleEntry, 0, len(keys))
		for _, key := range keys {
			entry, exists := stored.Legacy.Entries[key]
			if !exists {
				return ExecutionConfiguration{}, &Error{Code: "configuration_unavailable"}
			}
			// Docker's --env-file has no quoting or multiline value syntax.
			// Keep legacy Compose revisions readable, but fail closed when an
			// explicitly approved migration value cannot be passed to Docker.
			if strings.ContainsAny(entry.Value, "\r\n") {
				return ExecutionConfiguration{}, &Error{Code: "configuration_unavailable"}
			}
			sensitivity := SensitivityPublic
			if entry.Sensitive {
				sensitivity = SensitivitySecret
			}
			legacy = append(legacy, scopedBundleEntry{Key: key, Sensitivity: sensitivity, Value: entry.Value})
		}
		return exportScopedEntries(configurationRevisionID, configurationRevisionNumber, legacy), nil
	}
	selected := make([]scopedBundleEntry, 0, len(keys))
	for _, key := range keys {
		var match *scopedBundleEntry
		for index := range stored.Scoped.Entries {
			entry := &stored.Scoped.Entries[index]
			if entry.Component != component || entry.Key != key || (entry.Phase != PhaseRuntime && entry.Phase != PhaseMigration) {
				continue
			}
			if match != nil {
				return ExecutionConfiguration{}, &Error{Code: "configuration_unavailable"}
			}
			match = entry
		}
		if match == nil {
			return ExecutionConfiguration{}, &Error{Code: "configuration_unavailable"}
		}
		selected = append(selected, *match)
	}
	return exportScopedEntries(configurationRevisionID, configurationRevisionNumber, selected), nil
}

func validatedAllowedKeys(allowedKeys []string) ([]string, error) {
	if len(allowedKeys) > 8 {
		return nil, &Error{Code: "configuration_unavailable"}
	}
	keys := append([]string(nil), allowedKeys...)
	sort.Strings(keys)
	for index, key := range keys {
		if validateKey(key) != nil || isReservedKey(key) || (index > 0 && keys[index-1] == key) {
			return nil, &Error{Code: "configuration_unavailable"}
		}
	}
	return keys, nil
}

func (s *Store) scopedRevisionForExport(ctx context.Context, appID, configurationRevisionID string, configurationRevisionNumber int64, planRevisionID string, planRevisionNumber int64, component string, allowLegacy bool) (revisionBundle, error) {
	if !validUUID(appID) || configurationRevisionNumber < 0 || (configurationRevisionNumber == 0) != (configurationRevisionID == "") || !validUUID(planRevisionID) || planRevisionNumber <= 0 || validateComponentName(component) != nil {
		return revisionBundle{}, &Error{Code: "configuration_unavailable"}
	}
	if configurationRevisionNumber == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM deployment_plan_revisions WHERE id=? AND app_id=? AND revision_number=? AND acceptance_status='accepted')`, planRevisionID, appID, planRevisionNumber).Scan(&exists); err != nil || exists != 1 {
			return revisionBundle{}, &Error{Code: "configuration_unavailable"}
		}
		return revisionBundle{Version: 1, Legacy: bundle{Version: 1, Entries: map[string]bundleEntry{}}}, nil
	}
	stored, err := s.readRevisionBundle(ctx, appID, configurationRevisionID, configurationRevisionNumber)
	if err != nil {
		return revisionBundle{}, &Error{Code: "configuration_unavailable"}
	}
	if stored.Version == 1 {
		if len(stored.Legacy.Entries) != 0 && !allowLegacy {
			return revisionBundle{}, &Error{Code: "configuration_review_required"}
		}
		return stored, nil
	}
	if stored.Scoped.DeploymentPlanRevisionID != planRevisionID || stored.Scoped.DeploymentPlanRevisionNumber != planRevisionNumber || !scopedComponentExists(stored.Scoped.Components, component) {
		return revisionBundle{}, &Error{Code: "configuration_review_required"}
	}
	return stored, nil
}

func exportEmptyOrLegacyReview(stored revisionBundle, revisionID string, revisionNumber int64) (ExecutionConfiguration, error) {
	if len(stored.Legacy.Entries) != 0 {
		return ExecutionConfiguration{}, &Error{Code: "configuration_review_required"}
	}
	return exportScopedEntries(revisionID, revisionNumber, nil), nil
}

func exportScopedEntries(revisionID string, revisionNumber int64, entries []scopedBundleEntry) ExecutionConfiguration {
	entries = append([]scopedBundleEntry(nil), entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	// Generated containers use Docker CLI --env-file, which treats quotes as
	// literal bytes. Scoped values cannot contain line breaks or NUL bytes.
	environment := []byte("# hostd application configuration\n")
	origins := make([]SecretOrigin, 0)
	for _, entry := range entries {
		environment = append(environment, entry.Key...)
		environment = append(environment, '=')
		environment = append(environment, entry.Value...)
		environment = append(environment, '\n')
		if entry.Sensitivity == SensitivitySecret && entry.Value != "" {
			origins = append(origins, SecretOrigin{RevisionID: revisionID, RevisionNumber: revisionNumber, Key: append([]byte(nil), entry.Key...), Value: append([]byte(nil), entry.Value...)})
		}
	}
	return ExecutionConfiguration{RevisionID: revisionID, RevisionNumber: revisionNumber, Environment: environment, SecretOrigins: origins}
}

func scopedComponentExists(components []ComponentTarget, component string) bool {
	for _, target := range components {
		if target.Name == component {
			return true
		}
	}
	return false
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
	var acceptedStrategy string
	err = tx.QueryRowContext(ctx, `SELECT r.strategy FROM deployment_plan_heads h JOIN deployment_plan_revisions r ON r.id=h.revision_id AND r.app_id=h.app_id AND r.revision_number=h.revision_number WHERE h.app_id=? AND r.acceptance_status='accepted'`, appID).Scan(&acceptedStrategy)
	if err == nil && acceptedStrategy == "generated_node" {
		return Configuration{}, &Error{Code: "configuration_review_required"}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Configuration{}, s.classifyWriteError(ctx, appID, input.ExpectedRevisionNumber, err)
	}
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

// ReplaceScoped saves a v2 immutable revision bound to one accepted generated
// deployment plan. Protected content is installed before the metadata CAS; a
// failed or conflicting transaction removes only the newly installed file.
func (s *Store) ReplaceScoped(ctx context.Context, appID, actorID string, input ScopedReplaceInput) (Configuration, error) {
	if !validUUID(appID) {
		return Configuration{}, &Error{Code: "app_not_found"}
	}
	if input.ExpectedRevisionNumber < 0 {
		return Configuration{}, invalid("expectedRevisionNumber", "Must be zero or greater")
	}
	if !validUUID(input.PlanRevisionID) || input.PlanRevisionNumber <= 0 {
		return Configuration{}, invalid("deploymentPlan", "An accepted deployment plan revision is required")
	}
	components, componentRoles, err := validateComponentTargets(input.Components)
	if err != nil {
		return Configuration{}, err
	}

	unlock := s.lock(appID)
	defer unlock()
	current, err := s.loadHeadRevisionBundle(ctx, appID)
	if err != nil {
		return Configuration{}, err
	}
	currentNumber := int64(0)
	mergeBase := current
	if current.Version != 0 {
		if current.Version == 1 {
			currentNumber = current.Legacy.RevisionNumber
		} else {
			currentNumber = current.Scoped.RevisionNumber
			if current.Scoped.DeploymentPlanRevisionID != input.PlanRevisionID || current.Scoped.DeploymentPlanRevisionNumber != input.PlanRevisionNumber {
				// A newly accepted plan may receive a fully reviewed replacement,
				// but secrets from the old plan are never carried across implicitly.
				mergeBase = revisionBundle{}
			}
		}
	}
	if currentNumber != input.ExpectedRevisionNumber {
		return Configuration{}, &Error{Code: "configuration_conflict"}
	}
	if err := s.validateAcceptedPlan(ctx, appID, input.PlanRevisionID, input.PlanRevisionNumber, len(components)); err != nil {
		return Configuration{}, err
	}
	entries, err := mergeScoped(mergeBase, componentRoles, input)
	if err != nil {
		return Configuration{}, err
	}

	revisionID := uuid.NewString()
	number := currentNumber + 1
	stored := scopedBundle{
		Version: 2, ApplicationID: appID, RevisionID: revisionID, RevisionNumber: number,
		DeploymentPlanRevisionID: input.PlanRevisionID, DeploymentPlanRevisionNumber: input.PlanRevisionNumber,
		Components: components, Entries: entries,
	}
	plaintext, err := json.Marshal(stored)
	if err != nil || len(plaintext) > maxBundleBytes {
		return Configuration{}, invalid("configuration", "Configuration is too large")
	}
	defer clear(plaintext)
	path := s.bundlePath(appID, revisionID)
	if err := s.configurationDirectory(appID, true); err != nil {
		return Configuration{}, &Error{Code: "configuration_unavailable"}
	}
	if err := secretfile.WriteNew(path, scopedPurpose(appID, revisionID), plaintext); err != nil {
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
		return Configuration{}, s.classifyScopedWriteError(ctx, appID, input.ExpectedRevisionNumber, input.PlanRevisionID, input.PlanRevisionNumber, err)
	}
	defer tx.Rollback()
	var planID sql.NullString
	var planNumber int64
	var planComponents int
	var strategy string
	if err := tx.QueryRowContext(ctx, `SELECT h.revision_id,h.revision_number,r.component_count,r.strategy FROM deployment_plan_heads h JOIN deployment_plan_revisions r ON r.id=h.revision_id AND r.app_id=h.app_id AND r.revision_number=h.revision_number WHERE h.app_id=?`, appID).Scan(&planID, &planNumber, &planComponents, &strategy); err != nil || !planID.Valid || planID.String != input.PlanRevisionID || planNumber != input.PlanRevisionNumber || planComponents != len(components) || strategy != "generated_node" {
		_ = tx.Rollback()
		return Configuration{}, &Error{Code: "configuration_review_required"}
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	variables, secrets := scopedCounts(entries)
	ref := filepath.ToSlash(filepath.Join("apps", appID, "configuration", revisionID+".secret"))
	if _, err = tx.ExecContext(ctx, `INSERT INTO application_configuration_revisions(id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count,bundle_version,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,?,?,?,?,?,?,2,?,?)`, revisionID, appID, number, ref, nullable(actorID), now, variables, secrets, input.PlanRevisionID, input.PlanRevisionNumber); err != nil {
		_ = tx.Rollback()
		return Configuration{}, s.classifyScopedWriteError(ctx, appID, input.ExpectedRevisionNumber, input.PlanRevisionID, input.PlanRevisionNumber, err)
	}
	for _, entry := range entries {
		if _, err = tx.ExecContext(ctx, `INSERT INTO application_configuration_scoped_entries(revision_id,phase,target_component,key,sensitivity) VALUES(?,?,?,?,?)`, revisionID, entry.Phase, entry.Component, entry.Key, entry.Sensitivity); err != nil {
			_ = tx.Rollback()
			return Configuration{}, s.classifyScopedWriteError(ctx, appID, input.ExpectedRevisionNumber, input.PlanRevisionID, input.PlanRevisionNumber, err)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE application_configuration_heads SET revision_id=?,revision_number=?,updated_at=? WHERE app_id=? AND revision_number=?`, revisionID, number, now, appID, input.ExpectedRevisionNumber)
	if err != nil {
		_ = tx.Rollback()
		return Configuration{}, s.classifyScopedWriteError(ctx, appID, input.ExpectedRevisionNumber, input.PlanRevisionID, input.PlanRevisionNumber, err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return Configuration{}, &Error{Code: "configuration_conflict"}
	}
	metadata, _ := json.Marshal(map[string]any{"variables": variables, "secrets": secrets, "removed": len(input.Remove), "formatVersion": 2, "deploymentPlanRevisionId": input.PlanRevisionID, "deploymentPlanRevisionNumber": input.PlanRevisionNumber})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(actor_id,action,resource_type,resource_id,metadata_json,created_at) VALUES(?,?,?,?,?,?)`, nullable(actorID), "application.configuration.replace_scoped", "application", appID, string(metadata), now); err != nil {
		_ = tx.Rollback()
		return Configuration{}, s.classifyScopedWriteError(ctx, appID, input.ExpectedRevisionNumber, input.PlanRevisionID, input.PlanRevisionNumber, err)
	}
	if err = tx.Commit(); err != nil {
		return Configuration{}, err
	}
	committed = true
	return s.Get(ctx, appID)
}

func (s *Store) validateAcceptedPlan(ctx context.Context, appID, planID string, planNumber int64, componentCount int) error {
	var currentID sql.NullString
	var currentNumber int64
	var storedComponentCount int
	var strategy string
	err := s.db.QueryRowContext(ctx, `SELECT h.revision_id,h.revision_number,r.component_count,r.strategy FROM deployment_plan_heads h JOIN deployment_plan_revisions r ON r.id=h.revision_id AND r.app_id=h.app_id AND r.revision_number=h.revision_number JOIN applications a ON a.id=h.app_id AND a.archived_at IS NULL WHERE h.app_id=?`, appID).Scan(&currentID, &currentNumber, &storedComponentCount, &strategy)
	if errors.Is(err, sql.ErrNoRows) {
		return &Error{Code: "app_not_found"}
	}
	if err != nil {
		return err
	}
	if !currentID.Valid || currentID.String != planID || currentNumber != planNumber || storedComponentCount != componentCount || strategy != "generated_node" {
		return &Error{Code: "configuration_review_required"}
	}
	return nil
}

func (s *Store) classifyScopedWriteError(ctx context.Context, appID string, expected int64, planID string, planNumber int64, cause error) error {
	var configurationNumber int64
	if err := s.db.QueryRowContext(ctx, `SELECT revision_number FROM application_configuration_heads WHERE app_id=?`, appID).Scan(&configurationNumber); err == nil && configurationNumber != expected {
		return &Error{Code: "configuration_conflict"}
	}
	var currentPlanID sql.NullString
	var currentPlanNumber int64
	if err := s.db.QueryRowContext(ctx, `SELECT revision_id,revision_number FROM deployment_plan_heads WHERE app_id=?`, appID).Scan(&currentPlanID, &currentPlanNumber); err == nil && (!currentPlanID.Valid || currentPlanID.String != planID || currentPlanNumber != planNumber) {
		return &Error{Code: "configuration_review_required"}
	}
	return cause
}

func (s *Store) classifyWriteError(ctx context.Context, appID string, expected int64, cause error) error {
	var current int64
	if err := s.db.QueryRowContext(ctx, `SELECT revision_number FROM application_configuration_heads WHERE app_id=?`, appID).Scan(&current); err == nil && current != expected {
		return &Error{Code: "configuration_conflict"}
	}
	return cause
}

func (s *Store) loadHeadBundle(ctx context.Context, appID string) (bundle, error) {
	stored, err := s.loadHeadRevisionBundle(ctx, appID)
	if err != nil {
		return bundle{}, err
	}
	if stored.Version == 0 {
		return bundle{Version: 1, ApplicationID: appID, Entries: map[string]bundleEntry{}}, nil
	}
	if stored.Version != 1 {
		return bundle{}, &Error{Code: "configuration_review_required"}
	}
	return stored.Legacy, nil
}

func (s *Store) loadHeadRevisionBundle(ctx context.Context, appID string) (revisionBundle, error) {
	var id sql.NullString
	var number int64
	err := s.db.QueryRowContext(ctx, `SELECT h.revision_id,h.revision_number FROM application_configuration_heads h JOIN applications a ON a.id=h.app_id AND a.archived_at IS NULL WHERE h.app_id=?`, appID).Scan(&id, &number)
	if errors.Is(err, sql.ErrNoRows) {
		return revisionBundle{}, &Error{Code: "app_not_found"}
	}
	if err != nil {
		return revisionBundle{}, err
	}
	if number == 0 {
		return revisionBundle{}, nil
	}
	return s.readRevisionBundle(ctx, appID, id.String, number)
}

func (s *Store) readBundle(ctx context.Context, appID, revisionID string, number int64) (bundle, error) {
	stored, err := s.readRevisionBundle(ctx, appID, revisionID, number)
	if err != nil {
		return bundle{}, err
	}
	if stored.Version != 1 {
		return bundle{}, errors.New("scoped configuration requires a scoped reader")
	}
	return stored.Legacy, nil
}

func (s *Store) readRevisionBundle(ctx context.Context, appID, revisionID string, number int64) (revisionBundle, error) {
	var ref string
	var version, variableCount, secretCount int
	var planID sql.NullString
	var planNumber sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT bundle_ref,bundle_version,deployment_plan_revision_id,deployment_plan_revision_number,variable_count,secret_count FROM application_configuration_revisions WHERE id=? AND app_id=? AND revision_number=?`, revisionID, appID, number).Scan(&ref, &version, &planID, &planNumber, &variableCount, &secretCount); err != nil {
		return revisionBundle{}, err
	}
	expected := filepath.ToSlash(filepath.Join("apps", appID, "configuration", revisionID+".secret"))
	if ref != expected {
		return revisionBundle{}, errors.New("invalid configuration bundle reference")
	}
	if (version == 1 && (planID.Valid || planNumber.Valid)) || (version == 2 && (!planID.Valid || !planNumber.Valid || !validUUID(planID.String) || planNumber.Int64 <= 0)) || (version != 1 && version != 2) {
		return revisionBundle{}, errors.New("invalid configuration revision metadata")
	}
	if err := s.configurationDirectory(appID, false); err != nil {
		return revisionBundle{}, err
	}
	purposeName := purpose(appID, revisionID)
	if version == 2 {
		purposeName = scopedPurpose(appID, revisionID)
	}
	plaintext, err := secretfile.Read(s.bundlePath(appID, revisionID), purposeName)
	if err != nil {
		return revisionBundle{}, err
	}
	defer clear(plaintext)
	if len(plaintext) > maxBundleBytes {
		return revisionBundle{}, errors.New("configuration bundle too large")
	}
	if err := rejectDuplicateJSONKeys(plaintext); err != nil {
		return revisionBundle{}, err
	}
	if version == 1 {
		return s.decodeLegacyBundle(ctx, plaintext, appID, revisionID, number, variableCount, secretCount)
	}
	return s.decodeScopedBundle(ctx, plaintext, appID, revisionID, number, planID.String, planNumber.Int64, variableCount, secretCount)
}

func (s *Store) decodeLegacyBundle(ctx context.Context, plaintext []byte, appID, revisionID string, number int64, variableCount, secretCount int) (revisionBundle, error) {
	var b bundle
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&b); err != nil {
		return revisionBundle{}, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return revisionBundle{}, errors.New("configuration bundle has trailing content")
	}
	if b.Version != 1 || b.ApplicationID != appID || b.RevisionID != revisionID || b.RevisionNumber != number || b.Entries == nil {
		return revisionBundle{}, errors.New("configuration bundle metadata mismatch")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key,sensitive FROM application_configuration_entries WHERE revision_id=? ORDER BY key`, revisionID)
	if err != nil {
		return revisionBundle{}, err
	}
	metadata := map[string]bool{}
	for rows.Next() {
		var key string
		var sensitive bool
		if err := rows.Scan(&key, &sensitive); err != nil {
			rows.Close()
			return revisionBundle{}, err
		}
		metadata[key] = sensitive
	}
	if err := rows.Close(); err != nil {
		return revisionBundle{}, err
	}
	variables, secrets := counts(b.Entries)
	if len(metadata) != len(b.Entries) || variables != variableCount || secrets != secretCount {
		return revisionBundle{}, errors.New("configuration bundle entry metadata mismatch")
	}
	for key, value := range b.Entries {
		if sensitive, ok := metadata[key]; !ok || sensitive != value.Sensitive {
			return revisionBundle{}, errors.New("configuration bundle entry metadata mismatch")
		}
	}
	var scopedCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_configuration_scoped_entries WHERE revision_id=?`, revisionID).Scan(&scopedCount); err != nil || scopedCount != 0 {
		return revisionBundle{}, errors.New("configuration bundle entry metadata mismatch")
	}
	return revisionBundle{Version: 1, Legacy: b}, nil
}

func (s *Store) decodeScopedBundle(ctx context.Context, plaintext []byte, appID, revisionID string, number int64, planID string, planNumber int64, variableCount, secretCount int) (revisionBundle, error) {
	var b scopedBundle
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&b); err != nil {
		return revisionBundle{}, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return revisionBundle{}, errors.New("configuration bundle has trailing content")
	}
	if b.Version != 2 || b.ApplicationID != appID || b.RevisionID != revisionID || b.RevisionNumber != number || b.DeploymentPlanRevisionID != planID || b.DeploymentPlanRevisionNumber != planNumber || b.Components == nil || b.Entries == nil {
		return revisionBundle{}, errors.New("configuration bundle metadata mismatch")
	}
	components, roles, err := validateComponentTargets(b.Components)
	if err != nil || !equalComponentTargets(components, b.Components) {
		return revisionBundle{}, errors.New("configuration bundle component metadata mismatch")
	}
	for index, entry := range b.Entries {
		if err := validateScopedBundleEntry(entry, roles); err != nil || (index > 0 && !scopedEntryLess(b.Entries[index-1], entry)) {
			return revisionBundle{}, errors.New("invalid scoped configuration bundle entry")
		}
	}
	variables, secrets := scopedCounts(b.Entries)
	if variables != variableCount || secrets != secretCount {
		return revisionBundle{}, errors.New("configuration bundle entry metadata mismatch")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT phase,target_component,key,sensitivity FROM application_configuration_scoped_entries WHERE revision_id=? ORDER BY phase,target_component,key`, revisionID)
	if err != nil {
		return revisionBundle{}, err
	}
	metadata := make([]scopedBundleEntry, 0, len(b.Entries))
	for rows.Next() {
		var entry scopedBundleEntry
		if err := rows.Scan(&entry.Phase, &entry.Component, &entry.Key, &entry.Sensitivity); err != nil {
			rows.Close()
			return revisionBundle{}, err
		}
		metadata = append(metadata, entry)
	}
	if err := rows.Close(); err != nil {
		return revisionBundle{}, err
	}
	if len(metadata) != len(b.Entries) {
		return revisionBundle{}, errors.New("configuration bundle entry metadata mismatch")
	}
	for index := range metadata {
		stored := b.Entries[index]
		if metadata[index].Phase != stored.Phase || metadata[index].Component != stored.Component || metadata[index].Key != stored.Key || metadata[index].Sensitivity != stored.Sensitivity {
			return revisionBundle{}, errors.New("configuration bundle entry metadata mismatch")
		}
	}
	var legacyCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_configuration_entries WHERE revision_id=?`, revisionID).Scan(&legacyCount); err != nil || legacyCount != 0 {
		return revisionBundle{}, errors.New("configuration bundle entry metadata mismatch")
	}
	return revisionBundle{Version: 2, Scoped: b}, nil
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

func scopedPurpose(appID, revisionID string) string {
	return "hostd/application-configuration/v2/" + appID + "/" + revisionID
}

func validateComponentTargets(values []ComponentTarget) ([]ComponentTarget, map[string]string, error) {
	if len(values) < 1 || len(values) > 64 {
		return nil, nil, invalid("components", "Accepted plan components are required")
	}
	components := append([]ComponentTarget(nil), values...)
	sort.Slice(components, func(i, j int) bool { return components[i].Name < components[j].Name })
	roles := make(map[string]string, len(components))
	for _, component := range components {
		if validateComponentName(component.Name) != nil || (component.Role != "server" && component.Role != "static") || roles[component.Name] != "" {
			return nil, nil, invalid("components", "Components must uniquely match the accepted deployment plan")
		}
		roles[component.Name] = component.Role
	}
	return components, roles, nil
}

func validateComponentName(value string) error {
	if !utf8.ValidString(value) || len(value) == 0 || len(value) > 256 || strings.TrimSpace(value) == "" {
		return errors.New("invalid component name")
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return errors.New("invalid component name")
		}
	}
	return nil
}

func equalComponentTargets(left, right []ComponentTarget) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func mergeScoped(current revisionBundle, componentRoles map[string]string, input ScopedReplaceInput) ([]scopedBundleEntry, error) {
	if len(input.Entries) > 256 || len(input.Remove) > 256 || len(input.Entries)+len(input.Remove) > 512 {
		return nil, invalid("configuration", "Too many entries")
	}
	result := make(map[string]scopedBundleEntry)
	seen := make(map[string]string)
	usedLegacySecrets := make(map[string]bool)
	for _, key := range input.Remove {
		if err := validateScopedKey(key, componentRoles); err != nil {
			return nil, invalid("remove", err.Error())
		}
		identity := scopedKeyIdentity(key)
		if seen[identity] != "" {
			return nil, invalid("remove", "Contains duplicate scopes")
		}
		seen[identity] = "remove"
	}
	if current.Version == 2 {
		for _, entry := range current.Scoped.Entries {
			identity := scopedEntryIdentity(entry)
			if entry.Sensitivity == SensitivitySecret && seen[identity] == "" {
				result[identity] = entry
			}
		}
	}
	for _, item := range input.Entries {
		if err := validateScopedInput(item, componentRoles); err != nil {
			return nil, invalid("entries", err.Error())
		}
		identity := scopedKeyIdentity(item.ScopedKey)
		if seen[identity] != "" {
			return nil, invalid("entries", "Keys must be unique within each phase and target")
		}
		seen[identity] = "entry"
		if item.Value == nil {
			existing, exists := result[identity]
			if exists && existing.Sensitivity == SensitivitySecret && item.Sensitivity == SensitivitySecret {
				continue
			}
			if current.Version == 1 && item.Sensitivity == SensitivitySecret {
				legacy, legacyExists := current.Legacy.Entries[item.Key]
				if legacyExists && legacy.Sensitive && !usedLegacySecrets[item.Key] {
					result[identity] = scopedBundleEntry{Phase: item.Phase, Component: item.Component, Key: item.Key, Sensitivity: SensitivitySecret, Value: legacy.Value}
					usedLegacySecrets[item.Key] = true
					continue
				}
			}
			return nil, invalid("entries", "Only an existing secret in the same scope can be preserved")
		}
		result[identity] = scopedBundleEntry{Phase: item.Phase, Component: item.Component, Key: item.Key, Sensitivity: item.Sensitivity, Value: *item.Value}
	}
	if len(result) > 256 {
		return nil, invalid("configuration", "Too many entries")
	}
	entries := make([]scopedBundleEntry, 0, len(result))
	for _, entry := range result {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return scopedEntryLess(entries[i], entries[j]) })
	return entries, nil
}

func validateScopedInput(input ScopedValueInput, componentRoles map[string]string) error {
	if err := validateScopedKey(input.ScopedKey, componentRoles); err != nil {
		return err
	}
	if input.Sensitivity != SensitivityPublic && input.Sensitivity != SensitivitySecret {
		return errors.New("Sensitivity must be public or secret")
	}
	if input.Phase == PhaseBuild && input.Sensitivity != SensitivityPublic {
		return errors.New("Build values must be explicitly non-secret")
	}
	if input.Sensitivity == SensitivitySecret && hasPublicPrefix(input.Key) {
		return errors.New("Browser-public environment names cannot be secret")
	}
	if input.Value == nil {
		if input.Sensitivity != SensitivitySecret {
			return errors.New("Public values must be supplied")
		}
		return nil
	}
	if err := validateScopedValue(*input.Value); err != nil {
		return err
	}
	if input.Sensitivity == SensitivitySecret && *input.Value == "" {
		return errors.New("Submitted secrets cannot be empty")
	}
	return nil
}

func validateScopedKey(input ScopedKey, componentRoles map[string]string) error {
	if err := ValidateScopedEnvironmentKey(input.Key); err != nil {
		return err
	}
	role, exists := componentRoles[input.Component]
	if !exists {
		return errors.New("Target component is not in the accepted deployment plan")
	}
	switch input.Phase {
	case PhaseBuild:
		return nil
	case PhaseRuntime, PhaseMigration:
		if role != "server" {
			return errors.New("Runtime and migration values require a server component")
		}
		return nil
	default:
		return errors.New("Phase must be runtime, build, or migration")
	}
}

func validateScopedBundleEntry(entry scopedBundleEntry, componentRoles map[string]string) error {
	if err := validateScopedInput(ScopedValueInput{ScopedKey: ScopedKey{Phase: entry.Phase, Component: entry.Component, Key: entry.Key}, Sensitivity: entry.Sensitivity, Value: &entry.Value}, componentRoles); err != nil {
		return err
	}
	return nil
}

func validateScopedValue(value string) error {
	if !utf8.ValidString(value) {
		return errors.New("Values must be valid UTF-8")
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("Values cannot contain NUL bytes or line breaks")
	}
	if len(value) > 8<<10 {
		return errors.New("A value is too large")
	}
	return nil
}

// ValidateScopedEnvironmentKey applies the v2 portable-name and reserved-name
// policy without assigning a phase, target, or sensitivity.
func ValidateScopedEnvironmentKey(key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if isReservedKey(key) {
		return errors.New("RIG_ and HOSTD_ environment names are reserved")
	}
	return nil
}

func isReservedKey(key string) bool {
	upper := strings.ToUpper(key)
	return strings.HasPrefix(upper, "RIG_") || strings.HasPrefix(upper, "HOSTD_")
}

func hasPublicPrefix(key string) bool {
	for _, prefix := range []string{"VITE_", "NEXT_PUBLIC_", "REACT_APP_", "PUBLIC_", "NUXT_PUBLIC_", "EXPO_PUBLIC_", "GATSBY_"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func scopedKeyIdentity(key ScopedKey) string {
	return string(key.Phase) + "\x00" + key.Component + "\x00" + key.Key
}

func scopedEntryIdentity(entry scopedBundleEntry) string {
	return scopedKeyIdentity(ScopedKey{Phase: entry.Phase, Component: entry.Component, Key: entry.Key})
}

func scopedEntryLess(left, right scopedBundleEntry) bool {
	if left.Phase != right.Phase {
		return left.Phase < right.Phase
	}
	if left.Component != right.Component {
		return left.Component < right.Component
	}
	return left.Key < right.Key
}

func scopedCounts(entries []scopedBundleEntry) (variables, secrets int) {
	for _, entry := range entries {
		if entry.Sensitivity == SensitivitySecret {
			secrets++
		} else {
			variables++
		}
	}
	return variables, secrets
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
		if _, err := s.readRevisionBundle(ctx, r.app, r.id, r.number); err != nil {
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
