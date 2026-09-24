package controller

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/jobs"
)

func TestUnpinnedDeploymentCreateRechecksPlanHeadInsideWriterTransaction(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const appID = "11111111-1111-4111-8111-111111111111"
	const actorID = "22222222-2222-4222-8222-222222222222"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, actorID, "actor", "fixture", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,created_at,updated_at) VALUES(?,?,?,?,?)`, appID, "fixture", "Fixture", now, now); err != nil {
		t.Fatal(err)
	}
	service := jobs.New(db)
	create := func(key string) (jobs.Job, bool, error) {
		return service.CreateWithInputChecked(jobs.CreateRequest{Type: "deploy", ResourceType: "application", ResourceID: appID, RequestedBy: actorID, IdempotencyKey: key, Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent}}, func(tx *sql.Tx, _ jobs.Job) error {
			return checkUnpinnedComposeHead(tx, appID)
		})
	}
	legacy, created, err := create("legacy")
	if err != nil || !created {
		t.Fatalf("legacy create=%+v created=%t err=%v", legacy, created, err)
	}
	if _, err := service.Cancel(legacy.ID); err != nil {
		t.Fatal(err)
	}

	insertPlan := func(id, strategy string, number int64) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO deployment_plan_revisions(id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at) VALUES(?,?,?, ?,?,'fixture','1',?,'local',0,?,?,0,0,'',?,?,'accepted',?,?)`,
			id, appID, number, "apps/"+appID+"/plans/"+id, strategy, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), actorID, now, actorID, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE deployment_plan_heads SET revision_id=?,revision_number=?,updated_at=? WHERE app_id=?`, id, number, now, appID); err != nil {
			t.Fatal(err)
		}
	}
	insertPlan("33333333-3333-4333-8333-333333333333", "compose", 1)
	compose, created, err := create("compose")
	if err != nil || !created {
		t.Fatalf("compose create=%+v created=%t err=%v", compose, created, err)
	}
	if _, err := service.Cancel(compose.ID); err != nil {
		t.Fatal(err)
	}

	insertPlan("44444444-4444-4444-8444-444444444444", "generated_node", 2)
	replayed, created, err := create("compose")
	if err != nil || created || replayed.ID != compose.ID {
		t.Fatalf("exact replay after generated head drift: job=%+v created=%t err=%v", replayed, created, err)
	}
	if _, created, err := create("stale-compose"); !errors.Is(err, jobs.ErrReviewedRevisionsRequired) || created {
		t.Fatalf("generated head accepted unpinned job: created=%t err=%v", created, err)
	}
	if _, err := service.GetDeploymentByIdempotency(appID, actorID, "stale-compose"); !errors.Is(err, jobs.ErrJobNotFound) {
		t.Fatalf("rejected job survived rollback: %v", err)
	}
}
