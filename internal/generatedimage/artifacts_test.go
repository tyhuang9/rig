package generatedimage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
)

const (
	testAppID       = "10000000-0000-4000-8000-000000000001"
	testReleaseID   = "20000000-0000-4000-8000-000000000002"
	testPlanID      = "30000000-0000-4000-8000-000000000003"
	testComponent   = "api"
	testCompiler    = "generated-node/v1"
	testBuildDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestArtifactRepositoryRetriesTerminalAttemptsAndReusesReady(t *testing.T) {
	db := openArtifactDatabase(t)
	seedBuildableRelease(t, db)
	repository := NewArtifactRepository(db)
	clock := sequenceClock(
		time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 31, 12, 1, 0, 0, time.UTC),
		time.Date(2026, 8, 31, 12, 2, 0, 0, time.UTC),
		time.Date(2026, 8, 31, 12, 3, 0, 0, time.UTC),
		time.Date(2026, 8, 31, 12, 4, 0, 0, time.UTC),
		time.Date(2026, 8, 31, 12, 5, 0, 0, time.UTC),
	)
	repository.now = clock
	input := artifactInput()

	first, created, err := repository.Begin(context.Background(), input)
	if err != nil || !created {
		t.Fatalf("begin first created=%t err=%v", created, err)
	}
	assertArtifactProvenance(t, first, 1, ArtifactBuilding)
	reusedBuilding, created, err := repository.Begin(context.Background(), input)
	if err != nil || created || reusedBuilding.ID != first.ID {
		t.Fatalf("reuse building=%#v created=%t err=%v", reusedBuilding, created, err)
	}

	failed, err := repository.Fail(context.Background(), first.ID, DiagnosticBuildTimeout)
	if err != nil || failed.State != ArtifactFailed || failed.DiagnosticCode != DiagnosticBuildTimeout || failed.FinishedAt.IsZero() {
		t.Fatalf("failed=%#v err=%v", failed, err)
	}
	second, created, err := repository.Begin(context.Background(), input)
	if err != nil || !created || second.ID == first.ID {
		t.Fatalf("begin retry=%#v created=%t err=%v", second, created, err)
	}
	assertArtifactProvenance(t, second, 2, ArtifactBuilding)
	if _, err := repository.Cancel(context.Background(), second.ID); err != nil {
		t.Fatal(err)
	}

	third, created, err := repository.Begin(context.Background(), input)
	if err != nil || !created {
		t.Fatalf("begin third created=%t err=%v", created, err)
	}
	assertArtifactProvenance(t, third, 3, ArtifactBuilding)
	imageID := "sha256:" + strings.Repeat("b", 64)
	ready, err := repository.Complete(context.Background(), third.ID, imageID)
	if err != nil || ready.State != ArtifactReady || ready.ImageContentID != imageID || ready.DiagnosticCode != "" {
		t.Fatalf("ready=%#v err=%v", ready, err)
	}
	reusedReady, created, err := repository.Begin(context.Background(), input)
	if err != nil || created || reusedReady.ID != ready.ID || reusedReady.AttemptNumber != 3 {
		t.Fatalf("reuse ready=%#v created=%t err=%v", reusedReady, created, err)
	}

	var attempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_image_artifacts WHERE release_id=?`, testReleaseID).Scan(&attempts); err != nil || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestArtifactRepositoryBeginIsConcurrentAndIdempotent(t *testing.T) {
	db := openArtifactDatabase(t)
	seedBuildableRelease(t, db)
	repository := NewArtifactRepository(db)
	input := artifactInput()

	const workers = 16
	var created atomic.Int64
	ids := make(chan string, workers)
	errorsSeen := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			artifact, wasCreated, err := repository.Begin(context.Background(), input)
			if err != nil {
				errorsSeen <- err
				return
			}
			if wasCreated {
				created.Add(1)
			}
			ids <- artifact.ID
		}()
	}
	group.Wait()
	close(ids)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("concurrent begin: %v", err)
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		} else if id != first {
			t.Errorf("artifact id=%s want=%s", id, first)
		}
	}
	if created.Load() != 1 {
		t.Fatalf("created=%d want=1", created.Load())
	}
}

func TestArtifactRepositoryValidatesProvenanceAndTransitions(t *testing.T) {
	db := openArtifactDatabase(t)
	seedBuildableRelease(t, db)
	repository := NewArtifactRepository(db)
	input := artifactInput()

	invalid := input
	invalid.BuildDefinitionDigest = strings.Repeat("A", 64)
	if _, _, err := repository.Begin(context.Background(), invalid); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("invalid digest err=%v", err)
	}
	mismatch := input
	mismatch.DeploymentPlanRevisionID = "40000000-0000-4000-8000-000000000004"
	if _, _, err := repository.Begin(context.Background(), mismatch); !errors.Is(err, ErrReleaseNotBuildable) {
		t.Fatalf("mismatched plan err=%v", err)
	}
	if _, err := db.Exec(`INSERT INTO generated_image_artifacts(
		id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,
		build_definition_digest,attempt_number,state,created_at,updated_at
	) VALUES('40000000-0000-4000-8000-000000000004',?, '50000000-0000-4000-8000-000000000005',1,?,?,?,1,'building',datetime('now'),datetime('now'))`,
		testReleaseID, testComponent, testCompiler, testBuildDigest); err == nil {
		t.Fatal("artifact with a plan revision not pinned by its release was persisted")
	}

	artifact, _, err := repository.Begin(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Fail(context.Background(), artifact.ID, DiagnosticCode("docker said no")); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("raw diagnostic err=%v", err)
	}
	if _, err := repository.Complete(context.Background(), artifact.ID, "latest"); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("mutable image reference err=%v", err)
	}
	ready, err := repository.Complete(context.Background(), artifact.ID, "sha256:"+strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Fail(context.Background(), ready.ID, DiagnosticBuildFailed); !errors.Is(err, ErrInvalidArtifactTransition) {
		t.Fatalf("terminal rewrite err=%v", err)
	}
	building, _, err := repository.Begin(context.Background(), BeginArtifactInput{
		ReleaseID:                    testReleaseID,
		DeploymentPlanRevisionID:     testPlanID,
		DeploymentPlanRevisionNumber: 1,
		ComponentID:                  "worker",
		CompilerVersion:              testCompiler,
		BuildDefinitionDigest:        strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE generated_image_artifacts SET component_id='other',state='failed',diagnostic_code='build_failed',updated_at=datetime('now'),finished_at=datetime('now') WHERE id=?`, building.ID); err == nil || !strings.Contains(err.Error(), "provenance is immutable") {
		t.Fatalf("provenance rewrite err=%v", err)
	}
	if _, err := db.Exec(`DELETE FROM generated_image_artifacts WHERE id=?`, ready.ID); err == nil || !strings.Contains(err.Error(), "artifacts are retained") {
		t.Fatalf("artifact delete err=%v", err)
	}
	if _, err := db.Exec(`INSERT INTO generated_image_artifacts(id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,build_definition_digest,attempt_number,state,diagnostic_code,created_at,updated_at,finished_at)
		VALUES('50000000-0000-4000-8000-000000000005',?,?,?,?,?,?,2,'failed','the command failed',datetime('now'),datetime('now'),datetime('now'))`, testReleaseID, testPlanID, 1, testComponent, testCompiler, testBuildDigest); err == nil {
		t.Fatal("raw diagnostic text persisted")
	}
}

func TestArtifactRepositoryRecoverRetainsHistoryAndAllowsRetry(t *testing.T) {
	db := openArtifactDatabase(t)
	seedBuildableRelease(t, db)
	repository := NewArtifactRepository(db)
	input := artifactInput()
	first, _, err := repository.Begin(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}

	recovered, err := repository.Recover(context.Background())
	if err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	interrupted, err := repository.Get(context.Background(), first.ID)
	if err != nil || interrupted.State != ArtifactFailed || interrupted.DiagnosticCode != DiagnosticDaemonRestarted {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	retry, created, err := repository.Begin(context.Background(), input)
	if err != nil || !created || retry.AttemptNumber != 2 {
		t.Fatalf("retry=%#v created=%t err=%v", retry, created, err)
	}
	if recovered, err = repository.Recover(context.Background()); err != nil || recovered != 1 {
		t.Fatalf("second recovery=%d err=%v", recovered, err)
	}
	if recovered, err = repository.Recover(context.Background()); err != nil || recovered != 0 {
		t.Fatalf("idempotent recovery=%d err=%v", recovered, err)
	}
}

func artifactInput() BeginArtifactInput {
	return BeginArtifactInput{
		ReleaseID:                    testReleaseID,
		DeploymentPlanRevisionID:     testPlanID,
		DeploymentPlanRevisionNumber: 1,
		ComponentID:                  testComponent,
		CompilerVersion:              testCompiler,
		BuildDefinitionDigest:        testBuildDigest,
	}
}

func assertArtifactProvenance(t *testing.T, artifact Artifact, attempt int64, state ArtifactState) {
	t.Helper()
	if artifact.ReleaseID != testReleaseID || artifact.DeploymentPlanRevisionID != testPlanID || artifact.DeploymentPlanRevisionNumber != 1 || artifact.ComponentID != testComponent || artifact.CompilerVersion != testCompiler || artifact.BuildDefinitionDigest != testBuildDigest || artifact.AttemptNumber != attempt || artifact.State != state || artifact.CreatedAt.IsZero() || artifact.UpdatedAt.IsZero() {
		t.Fatalf("artifact=%#v", artifact)
	}
}

func openArtifactDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedBuildableRelease(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES('artifact-owner','artifact-owner','hash','administrator',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`, testAppID, "artifact-app", "Artifact app"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','js-ts','1',?,'local',0,?,?,1,8,'','artifact-owner',datetime('now'),'accepted','artifact-owner',datetime('now'))`,
		testPlanID, testAppID, "apps/"+testAppID+"/deployment-plans/"+testPlanID+".secret", strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE deployment_plan_heads SET revision_id=?,revision_number=1,updated_at=datetime('now') WHERE app_id=?`, testPlanID, testAppID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,
		archive_sha256,workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,'ready','{}',datetime('now'),'local',0,?,'',?,'ready',?,?,1)`,
		testReleaseID, testAppID, strings.Repeat("2", 64), strings.Repeat("4", 64), strings.Repeat("4", 64), testPlanID); err != nil {
		t.Fatal(err)
	}
}

func sequenceClock(values ...time.Time) func() time.Time {
	var mutex sync.Mutex
	index := 0
	return func() time.Time {
		mutex.Lock()
		defer mutex.Unlock()
		if index >= len(values) {
			return values[len(values)-1]
		}
		value := values[index]
		index++
		return value
	}
}
