package deploymentplans

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/pathsecurity"
	"github.com/hostd/hostd/internal/secretfile"
)

var temporarySecretName = regexp.MustCompile(`^\.hostd-secret-[A-Za-z0-9]{8,}$`)

type bundle struct {
	Version  int                    `json:"version"`
	Revision DeploymentPlanRevision `json:"revision"`
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
		return nil, errors.New("deployment plan data root must be absolute and clean")
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

// Get returns the current head. Revision zero is the virtual empty head.
func (s *Store) Get(ctx context.Context, appID string) (DeploymentPlanRevision, error) {
	if !validUUID(appID) {
		return DeploymentPlanRevision{}, &Error{Code: "app_not_found"}
	}
	var id sql.NullString
	var number int64
	err := s.db.QueryRowContext(ctx, `SELECT h.revision_id,h.revision_number FROM deployment_plan_heads h JOIN applications a ON a.id=h.app_id AND a.archived_at IS NULL WHERE h.app_id=?`, appID).Scan(&id, &number)
	if errors.Is(err, sql.ErrNoRows) {
		return DeploymentPlanRevision{}, &Error{Code: "app_not_found"}
	}
	if err != nil {
		return DeploymentPlanRevision{}, err
	}
	if number == 0 {
		return DeploymentPlanRevision{AppID: appID}, nil
	}
	return s.readRevision(ctx, appID, id.String, number)
}

func (s *Store) GetRevision(ctx context.Context, appID, revisionID string, revisionNumber int64) (DeploymentPlanRevision, error) {
	if !validUUID(appID) || !validUUID(revisionID) || revisionNumber <= 0 {
		return DeploymentPlanRevision{}, &Error{Code: "deployment_plan_unavailable"}
	}
	revision, err := s.readRevision(ctx, appID, revisionID, revisionNumber)
	if err != nil {
		return DeploymentPlanRevision{}, &Error{Code: "deployment_plan_unavailable"}
	}
	return revision, nil
}

func (s *Store) Replace(ctx context.Context, appID, actorID string, input ReplaceInput) (DeploymentPlanRevision, error) {
	if !validUUID(appID) {
		return DeploymentPlanRevision{}, &Error{Code: "app_not_found"}
	}
	if input.ExpectedRevisionNumber < 0 {
		return DeploymentPlanRevision{}, invalid("expectedRevisionNumber", "Must be zero or greater")
	}
	canonical, err := canonicalPlan(input.Plan)
	if err != nil {
		return DeploymentPlanRevision{}, err
	}
	if validateText(actorID, 256) != nil {
		return DeploymentPlanRevision{}, invalid("actor", "An acceptance actor is required")
	}
	unlock := s.lock(appID)
	defer unlock()

	var current int64
	if err := s.db.QueryRowContext(ctx, `SELECT h.revision_number FROM deployment_plan_heads h JOIN applications a ON a.id=h.app_id AND a.archived_at IS NULL WHERE h.app_id=?`, appID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return DeploymentPlanRevision{}, &Error{Code: "app_not_found"}
	} else if err != nil {
		return DeploymentPlanRevision{}, err
	}
	if current != input.ExpectedRevisionNumber {
		return DeploymentPlanRevision{}, &Error{Code: "deployment_plan_conflict"}
	}
	digest, err := CanonicalDigest(canonical)
	if err != nil {
		return DeploymentPlanRevision{}, err
	}
	id := uuid.NewString()
	number := current + 1
	now := s.now().UTC().Format(time.RFC3339Nano)
	revision := DeploymentPlanRevision{ID: id, AppID: appID, RevisionNumber: number, Plan: canonical, CanonicalDigest: digest, RevisedBy: actorID, RevisedAt: now, AcceptedBy: actorID, AcceptedAt: now, State: RevisionAccepted}
	plaintext, err := json.Marshal(bundle{Version: 1, Revision: revision})
	if err != nil || len(plaintext) > maxBundleBytes {
		return DeploymentPlanRevision{}, invalid("plan", "Deployment plan is too large")
	}
	defer clear(plaintext)
	if err := s.planDirectory(appID, true); err != nil {
		return DeploymentPlanRevision{}, &Error{Code: "deployment_plan_unavailable"}
	}
	path := s.bundlePath(appID, id)
	if err := secretfile.WriteNew(path, purpose(appID, id), plaintext); err != nil {
		if secretfile.WasInstalled(err) {
			_ = secretfile.Remove(path)
		}
		return DeploymentPlanRevision{}, &Error{Code: "deployment_plan_unavailable"}
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
		return DeploymentPlanRevision{}, s.classifyWriteError(ctx, appID, input.ExpectedRevisionNumber, err)
	}
	defer tx.Rollback()
	ref := filepath.ToSlash(filepath.Join("apps", appID, "deployment-plans", id+".secret"))
	_, err = tx.ExecContext(ctx, `INSERT INTO deployment_plan_revisions(id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,canonical_digest,component_count,field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, appID, number, ref, canonical.Strategy, canonical.Detector.Name, canonical.Detector.Version, canonical.Detector.SourceStructuralFingerprint, digest, len(canonical.Components), len(canonical.FieldProvenance), migrationEvidenceDigest(canonical.Migration), actorID, now, RevisionAccepted, actorID, now)
	if err != nil {
		return DeploymentPlanRevision{}, s.classifyWriteError(ctx, appID, input.ExpectedRevisionNumber, err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE deployment_plan_heads SET revision_id=?,revision_number=?,updated_at=? WHERE app_id=? AND revision_number=?`, id, number, now, appID, input.ExpectedRevisionNumber)
	if err != nil {
		return DeploymentPlanRevision{}, s.classifyWriteError(ctx, appID, input.ExpectedRevisionNumber, err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return DeploymentPlanRevision{}, &Error{Code: "deployment_plan_conflict"}
	}
	metadata, _ := json.Marshal(map[string]any{"strategy": canonical.Strategy, "detector": canonical.Detector.Name, "detectorVersion": canonical.Detector.Version, "canonicalDigest": digest, "components": len(canonical.Components), "fieldProvenance": len(canonical.FieldProvenance), "acceptanceStatus": RevisionAccepted})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(actor_id,action,resource_type,resource_id,metadata_json,created_at) VALUES(?,?,?,?,?,?)`, nullable(actorID), "deployment_plan.replace", "application", appID, string(metadata), now); err != nil {
		return DeploymentPlanRevision{}, s.classifyWriteError(ctx, appID, input.ExpectedRevisionNumber, err)
	}
	if err = tx.Commit(); err != nil {
		return DeploymentPlanRevision{}, err
	}
	committed = true
	return revision, nil
}

func (s *Store) classifyWriteError(ctx context.Context, appID string, expected int64, cause error) error {
	var current int64
	if err := s.db.QueryRowContext(ctx, `SELECT revision_number FROM deployment_plan_heads WHERE app_id=?`, appID).Scan(&current); err == nil && current != expected {
		return &Error{Code: "deployment_plan_conflict"}
	}
	return cause
}

func (s *Store) readRevision(ctx context.Context, appID, id string, number int64) (DeploymentPlanRevision, error) {
	var ref, digest, revisedAt, status, detector, detectorVersion, structuralFingerprint, migrationDigest string
	var revisedBy, acceptedBy, acceptedAt string
	var strategy string
	var componentCount, fieldCount int
	err := s.db.QueryRowContext(ctx, `SELECT bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,canonical_digest,component_count,field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at FROM deployment_plan_revisions WHERE id=? AND app_id=? AND revision_number=?`, id, appID, number).Scan(&ref, &strategy, &detector, &detectorVersion, &structuralFingerprint, &digest, &componentCount, &fieldCount, &migrationDigest, &revisedBy, &revisedAt, &status, &acceptedBy, &acceptedAt)
	if err != nil {
		return DeploymentPlanRevision{}, err
	}
	if ref != filepath.ToSlash(filepath.Join("apps", appID, "deployment-plans", id+".secret")) || !validDigest(digest) || !validDigest(structuralFingerprint) || (migrationDigest != "" && !validDigest(migrationDigest)) {
		return DeploymentPlanRevision{}, errors.New("invalid deployment plan metadata")
	}
	if err := s.planDirectory(appID, false); err != nil {
		return DeploymentPlanRevision{}, err
	}
	plaintext, err := secretfile.Read(s.bundlePath(appID, id), purpose(appID, id))
	if err != nil {
		return DeploymentPlanRevision{}, err
	}
	defer clear(plaintext)
	if len(plaintext) > maxBundleBytes {
		return DeploymentPlanRevision{}, errors.New("deployment plan bundle too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	var stored bundle
	if err := decoder.Decode(&stored); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return DeploymentPlanRevision{}, errors.New("invalid deployment plan bundle")
	}
	canonical, err := canonicalPlan(stored.Revision.Plan)
	if err != nil {
		return DeploymentPlanRevision{}, err
	}
	computed, err := CanonicalDigest(canonical)
	if err != nil || stored.Version != 1 || stored.Revision.ID != id || stored.Revision.AppID != appID || stored.Revision.RevisionNumber != number || stored.Revision.CanonicalDigest != computed || digest != computed || string(canonical.Strategy) != strategy || canonical.Detector.Name != detector || canonical.Detector.Version != detectorVersion || canonical.Detector.SourceStructuralFingerprint != structuralFingerprint || len(canonical.Components) != componentCount || len(canonical.FieldProvenance) != fieldCount || migrationEvidenceDigest(canonical.Migration) != migrationDigest || stored.Revision.RevisedBy != revisedBy || stored.Revision.RevisedAt != revisedAt || stored.Revision.AcceptedBy != acceptedBy || stored.Revision.AcceptedAt != acceptedAt || status != string(RevisionAccepted) {
		return DeploymentPlanRevision{}, errors.New("deployment plan bundle metadata mismatch")
	}
	stored.Revision.Plan = canonical
	var headNumber int64
	if err := s.db.QueryRowContext(ctx, `SELECT revision_number FROM deployment_plan_heads WHERE app_id=?`, appID).Scan(&headNumber); err == nil && headNumber != number {
		stored.Revision.State = RevisionSuperseded
	} else {
		stored.Revision.State = RevisionAccepted
	}
	return stored.Revision, nil
}

// Recover validates every referenced bundle and deletes only recognized
// unreferenced bundle or temporary files from managed plan directories.
func (s *Store) Recover(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,app_id,revision_number FROM deployment_plan_revisions ORDER BY app_id,revision_number`)
	if err != nil {
		return err
	}
	type reference struct {
		id, app string
		number  int64
	}
	var references []reference
	apps := map[string]bool{}
	known := map[string]bool{}
	for rows.Next() {
		var reference reference
		if err := rows.Scan(&reference.id, &reference.app, &reference.number); err != nil {
			rows.Close()
			return err
		}
		if !validUUID(reference.id) || !validUUID(reference.app) {
			rows.Close()
			return errors.New("invalid deployment plan identity")
		}
		references = append(references, reference)
		apps[reference.app] = true
		known[filepath.Clean(s.bundlePath(reference.app, reference.id))] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, reference := range references {
		if _, err := s.readRevision(ctx, reference.app, reference.id, reference.number); err != nil {
			return err
		}
	}
	if err := safeDirectory(s.root, true); err != nil {
		return err
	}
	for app := range apps {
		directory := filepath.Join(s.root, app, "deployment-plans")
		if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := safeDirectory(directory, false); err != nil {
			return err
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			path := filepath.Join(directory, entry.Name())
			if entry.IsDir() {
				return errors.New("unrecognized directory in deployment plan directory")
			}
			if known[filepath.Clean(path)] {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".secret")
			if !(strings.HasSuffix(entry.Name(), ".secret") && validUUID(id)) && !temporarySecretName.MatchString(entry.Name()) {
				return errors.New("unrecognized file in deployment plan directory")
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) bundlePath(appID, revisionID string) string {
	return filepath.Join(s.root, appID, "deployment-plans", revisionID+".secret")
}

func (s *Store) planDirectory(appID string, create bool) error {
	if !validUUID(appID) {
		return errors.New("invalid deployment plan identity")
	}
	if err := safeDirectory(s.root, create); err != nil {
		return err
	}
	if err := safeDirectory(filepath.Join(s.root, appID), create); err != nil {
		return err
	}
	return safeDirectory(filepath.Join(s.root, appID, "deployment-plans"), create)
}

func safeDirectory(path string, create bool) error {
	if pathsecurity.RejectWindowsNamespace(path) {
		return errors.New("unsafe deployment plan path namespace")
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
		return errors.New("managed deployment plan path is not a directory")
	}
	return nil
}

func validUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func purpose(appID, revisionID string) string {
	return "hostd/deployment-plan/v1/" + appID + "/" + revisionID
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func migrationEvidenceDigest(migration *Migration) string {
	if migration == nil {
		return ""
	}
	return migration.EvidenceDigest
}
