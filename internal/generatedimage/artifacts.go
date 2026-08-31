// Package generatedimage owns the metadata and compilation boundary for
// controller-generated application images.
package generatedimage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

type ArtifactState string

const (
	ArtifactBuilding  ArtifactState = "building"
	ArtifactReady     ArtifactState = "ready"
	ArtifactFailed    ArtifactState = "failed"
	ArtifactCancelled ArtifactState = "cancelled"
)

type DiagnosticCode string

const (
	DiagnosticBuildFailed              DiagnosticCode = "build_failed"
	DiagnosticBuildTimeout             DiagnosticCode = "build_timeout"
	DiagnosticBuildContextInvalid      DiagnosticCode = "build_context_invalid"
	DiagnosticBuildContextTooLarge     DiagnosticCode = "build_context_too_large"
	DiagnosticBuildOutputTruncated     DiagnosticCode = "build_output_truncated"
	DiagnosticBuildDiskExhausted       DiagnosticCode = "build_disk_exhausted"
	DiagnosticBuildCapacityExceeded    DiagnosticCode = "build_capacity_exceeded"
	DiagnosticRuntimeUnavailable       DiagnosticCode = "runtime_unavailable"
	DiagnosticSourceUnavailable        DiagnosticCode = "source_unavailable"
	DiagnosticSourceIntegrityFailed    DiagnosticCode = "source_integrity_failed"
	DiagnosticProcessTerminationFailed DiagnosticCode = "process_termination_failed"
	DiagnosticDaemonRestarted          DiagnosticCode = "daemon_restarted"
	DiagnosticInternalError            DiagnosticCode = "internal_error"
	DiagnosticBuildCancelled           DiagnosticCode = "build_cancelled"
)

var (
	ErrInvalidArtifact           = errors.New("invalid generated image artifact")
	ErrArtifactNotFound          = errors.New("generated image artifact not found")
	ErrInvalidArtifactTransition = errors.New("invalid generated image artifact transition")
	ErrReleaseNotBuildable       = errors.New("release is not ready with the requested deployment plan revision")
)

type Artifact struct {
	ID                           string
	ReleaseID                    string
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
	ComponentID                  string
	CompilerVersion              string
	BuildDefinitionDigest        string
	AttemptNumber                int64
	ImageContentID               string
	State                        ArtifactState
	DiagnosticCode               DiagnosticCode
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
	FinishedAt                   time.Time
}

type BeginArtifactInput struct {
	ReleaseID                    string
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
	ComponentID                  string
	CompilerVersion              string
	BuildDefinitionDigest        string
}

// ArtifactRepository persists only audit-safe build provenance and stable
// outcomes. Commands and build output never cross this boundary.
type ArtifactRepository struct {
	db  *sql.DB
	now func() time.Time
}

func NewArtifactRepository(db *sql.DB) *ArtifactRepository {
	return &ArtifactRepository{db: db, now: time.Now}
}

// Begin reuses a ready artifact or the definition's sole in-progress attempt.
// A failed or cancelled terminal attempt is retained and followed by a new,
// monotonically numbered attempt.
func (r *ArtifactRepository) Begin(ctx context.Context, input BeginArtifactInput) (Artifact, bool, error) {
	if r == nil || r.db == nil || ctx == nil || !validBeginInput(input) {
		return Artifact{}, false, ErrInvalidArtifact
	}
	for retry := 0; retry < 3; retry++ {
		id := uuid.NewString()
		now := r.now().UTC().Format(time.RFC3339Nano)
		result, err := r.db.ExecContext(ctx, `
			INSERT INTO generated_image_artifacts(
				id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,
				component_id,compiler_version,build_definition_digest,attempt_number,
				image_content_id,state,diagnostic_code,created_at,updated_at,finished_at
			)
			SELECT ?,r.id,r.deployment_plan_revision_id,r.deployment_plan_revision_number,
				?,?,?,COALESCE((
					SELECT MAX(a.attempt_number) FROM generated_image_artifacts a
					WHERE a.release_id=r.id AND a.component_id=? AND a.compiler_version=? AND a.build_definition_digest=?
				),0)+1,NULL,'building',NULL,?,?,NULL
			FROM releases r
			WHERE r.id=? AND r.workspace_state='ready'
			  AND r.deployment_plan_revision_id=? AND r.deployment_plan_revision_number=?
			  AND NOT EXISTS (
				SELECT 1 FROM generated_image_artifacts current
				WHERE current.release_id=r.id AND current.component_id=? AND current.compiler_version=?
				  AND current.build_definition_digest=? AND current.state IN ('building','ready')
			  )`,
			id, input.ComponentID, input.CompilerVersion, input.BuildDefinitionDigest,
			input.ComponentID, input.CompilerVersion, input.BuildDefinitionDigest,
			now, now, input.ReleaseID, input.DeploymentPlanRevisionID, input.DeploymentPlanRevisionNumber,
			input.ComponentID, input.CompilerVersion, input.BuildDefinitionDigest,
		)
		if err != nil {
			if existing, found, lookupErr := r.findReusable(ctx, input); lookupErr == nil && found {
				return existing, false, nil
			}
			return Artifact{}, false, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return Artifact{}, false, err
		}
		if count == 1 {
			artifact, err := r.Get(ctx, id)
			return artifact, true, err
		}
		if count != 0 {
			return Artifact{}, false, ErrInvalidArtifact
		}
		if existing, found, err := r.findReusable(ctx, input); err != nil {
			return Artifact{}, false, err
		} else if found {
			return existing, false, nil
		}
		buildable, err := r.releaseBuildable(ctx, input)
		if err != nil {
			return Artifact{}, false, err
		}
		if !buildable {
			return Artifact{}, false, ErrReleaseNotBuildable
		}
		// An in-progress attempt may have become terminal between the insert
		// guard and lookup. Retry against the newly available attempt number.
	}
	return Artifact{}, false, ErrInvalidArtifactTransition
}

func (r *ArtifactRepository) Get(ctx context.Context, artifactID string) (Artifact, error) {
	if r == nil || r.db == nil || ctx == nil || !canonicalUUID(artifactID) {
		return Artifact{}, ErrArtifactNotFound
	}
	artifact, err := scanArtifact(r.db.QueryRowContext(ctx, artifactSelect+` WHERE id=?`, artifactID))
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, ErrArtifactNotFound
	}
	return artifact, err
}

func (r *ArtifactRepository) Complete(ctx context.Context, artifactID, imageContentID string) (Artifact, error) {
	if r == nil || r.db == nil || ctx == nil || !canonicalUUID(artifactID) || !validImageContentID(imageContentID) {
		return Artifact{}, ErrInvalidArtifact
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	result, err := r.db.ExecContext(ctx, `UPDATE generated_image_artifacts SET image_content_id=?,state='ready',updated_at=?,finished_at=? WHERE id=? AND state='building'`, imageContentID, now, now, artifactID)
	if err != nil {
		return Artifact{}, err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return Artifact{}, err
	} else if changed != 1 {
		return Artifact{}, r.transitionError(ctx, artifactID)
	}
	return r.Get(ctx, artifactID)
}

func (r *ArtifactRepository) Fail(ctx context.Context, artifactID string, code DiagnosticCode) (Artifact, error) {
	if r == nil || r.db == nil || ctx == nil || !canonicalUUID(artifactID) || !failureDiagnostic(code) {
		return Artifact{}, ErrInvalidArtifact
	}
	return r.finish(ctx, artifactID, ArtifactFailed, code)
}

func (r *ArtifactRepository) Cancel(ctx context.Context, artifactID string) (Artifact, error) {
	if r == nil || r.db == nil || ctx == nil || !canonicalUUID(artifactID) {
		return Artifact{}, ErrInvalidArtifact
	}
	return r.finish(ctx, artifactID, ArtifactCancelled, DiagnosticBuildCancelled)
}

// Recover terminalizes interrupted builds without discarding their immutable
// attempt history. The returned count is useful for startup diagnostics.
func (r *ArtifactRepository) Recover(ctx context.Context) (int64, error) {
	if r == nil || r.db == nil || ctx == nil {
		return 0, ErrInvalidArtifact
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	result, err := r.db.ExecContext(ctx, `UPDATE generated_image_artifacts SET state='failed',diagnostic_code=?,updated_at=?,finished_at=? WHERE state='building'`, DiagnosticDaemonRestarted, now, now)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *ArtifactRepository) finish(ctx context.Context, artifactID string, state ArtifactState, code DiagnosticCode) (Artifact, error) {
	now := r.now().UTC().Format(time.RFC3339Nano)
	result, err := r.db.ExecContext(ctx, `UPDATE generated_image_artifacts SET state=?,diagnostic_code=?,updated_at=?,finished_at=? WHERE id=? AND state='building'`, state, code, now, now, artifactID)
	if err != nil {
		return Artifact{}, err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return Artifact{}, err
	} else if changed != 1 {
		return Artifact{}, r.transitionError(ctx, artifactID)
	}
	return r.Get(ctx, artifactID)
}

func (r *ArtifactRepository) transitionError(ctx context.Context, artifactID string) error {
	var exists int
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM generated_image_artifacts WHERE id=?)`, artifactID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrArtifactNotFound
	}
	return ErrInvalidArtifactTransition
}

func (r *ArtifactRepository) findReusable(ctx context.Context, input BeginArtifactInput) (Artifact, bool, error) {
	artifact, err := scanArtifact(r.db.QueryRowContext(ctx, artifactSelect+`
		WHERE release_id=? AND component_id=? AND compiler_version=? AND build_definition_digest=?
		  AND state IN ('ready','building')
		ORDER BY CASE state WHEN 'ready' THEN 0 ELSE 1 END, attempt_number DESC LIMIT 1`,
		input.ReleaseID, input.ComponentID, input.CompilerVersion, input.BuildDefinitionDigest))
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, false, nil
	}
	return artifact, err == nil, err
}

func (r *ArtifactRepository) releaseBuildable(ctx context.Context, input BeginArtifactInput) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM releases WHERE id=? AND workspace_state='ready'
		AND deployment_plan_revision_id=? AND deployment_plan_revision_number=?
	)`, input.ReleaseID, input.DeploymentPlanRevisionID, input.DeploymentPlanRevisionNumber).Scan(&exists)
	return exists == 1, err
}

const artifactSelect = `SELECT id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,build_definition_digest,attempt_number,COALESCE(image_content_id,''),state,COALESCE(diagnostic_code,''),created_at,updated_at,COALESCE(finished_at,'') FROM generated_image_artifacts`

type artifactScanner interface {
	Scan(...any) error
}

func scanArtifact(row artifactScanner) (Artifact, error) {
	var artifact Artifact
	var createdAt, updatedAt, finishedAt string
	if err := row.Scan(
		&artifact.ID, &artifact.ReleaseID, &artifact.DeploymentPlanRevisionID, &artifact.DeploymentPlanRevisionNumber,
		&artifact.ComponentID, &artifact.CompilerVersion, &artifact.BuildDefinitionDigest, &artifact.AttemptNumber,
		&artifact.ImageContentID, &artifact.State, &artifact.DiagnosticCode, &createdAt, &updatedAt, &finishedAt,
	); err != nil {
		return Artifact{}, err
	}
	var err error
	if artifact.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return Artifact{}, err
	}
	if artifact.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return Artifact{}, err
	}
	if finishedAt != "" {
		if artifact.FinishedAt, err = time.Parse(time.RFC3339Nano, finishedAt); err != nil {
			return Artifact{}, err
		}
	}
	return artifact, nil
}

func validBeginInput(input BeginArtifactInput) bool {
	return validReleaseID(input.ReleaseID) && canonicalUUID(input.DeploymentPlanRevisionID) && input.DeploymentPlanRevisionNumber > 0 &&
		validText(input.ComponentID, 256) && validText(input.CompilerVersion, 128) && lowerHex(input.BuildDefinitionDigest, 64)
}

func validReleaseID(value string) bool {
	return canonicalUUID(value) || lowerHex(value, 32)
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validText(value string, maximum int) bool {
	if !utf8.ValidString(value) || value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validImageContentID(value string) bool {
	return strings.HasPrefix(value, "sha256:") && lowerHex(strings.TrimPrefix(value, "sha256:"), 64)
}

func failureDiagnostic(code DiagnosticCode) bool {
	switch code {
	case DiagnosticBuildFailed,
		DiagnosticBuildTimeout,
		DiagnosticBuildContextInvalid,
		DiagnosticBuildContextTooLarge,
		DiagnosticBuildOutputTruncated,
		DiagnosticBuildDiskExhausted,
		DiagnosticBuildCapacityExceeded,
		DiagnosticRuntimeUnavailable,
		DiagnosticSourceUnavailable,
		DiagnosticSourceIntegrityFailed,
		DiagnosticProcessTerminationFailed,
		DiagnosticDaemonRestarted,
		DiagnosticInternalError:
		return true
	default:
		return false
	}
}
