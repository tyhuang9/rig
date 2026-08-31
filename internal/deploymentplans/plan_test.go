package deploymentplans

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
)

const planTestApp = "11111111-1111-1111-1111-111111111111"

func TestValidateCommand(t *testing.T) {
	for _, command := range []string{"", " \t", "npm run build\nrm -rf /", "echo\x00bad", "echo\x1bbad", strings.Repeat("x", maxCommandBytes+1)} {
		if err := ValidateCommand(command); err == nil {
			t.Fatalf("invalid command %q accepted", command)
		}
	}
	if err := ValidateCommand("npm run build -- --mode=production"); err != nil {
		t.Fatalf("valid command: %v", err)
	}
}

func TestCanonicalDigestSortsUnorderedMetadata(t *testing.T) {
	first := testPlan()
	second := testPlan()
	second.Components[0], second.Components[1] = second.Components[1], second.Components[0]
	first.FieldProvenance[0].Evidence = append(first.FieldProvenance[0].Evidence, "package-lock.json")
	second.FieldProvenance[0].Evidence = append(second.FieldProvenance[0].Evidence, "package-lock.json")
	second.FieldProvenance[0].Evidence[0], second.FieldProvenance[0].Evidence[1] = second.FieldProvenance[0].Evidence[1], second.FieldProvenance[0].Evidence[0]
	firstDigest, err := CanonicalDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := CanonicalDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("digest changed after unordered metadata reorder: %s != %s", firstDigest, secondDigest)
	}
}

func TestCanonicalPlanAcceptsLocalSnapshotIdentity(t *testing.T) {
	plan := testPlan()
	plan.Source = SourceIdentity{Provider: "local", RepositoryID: 0, ResolvedDigest: strings.Repeat("d", 64)}
	if _, err := CanonicalDigest(plan); err != nil {
		t.Fatalf("local plan rejected: %v", err)
	}
	plan.Source.ResolvedDigest = strings.Repeat("d", 40)
	if _, err := CanonicalDigest(plan); err == nil {
		t.Fatal("short local digest accepted")
	}
}

func TestStorePersistsProtectedImmutableRevisionAndCASHead(t *testing.T) {
	db := planDB(t)
	root := t.TempDir()
	store, err := New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Get(context.Background(), planTestApp)
	if err != nil || initial.RevisionNumber != 0 || initial.ID != "" {
		t.Fatalf("initial=%#v err=%v", initial, err)
	}
	created, err := store.Replace(context.Background(), planTestApp, "owner", ReplaceInput{Plan: testPlan()})
	if err != nil {
		t.Fatal(err)
	}
	if created.RevisionNumber != 1 || created.CanonicalDigest == "" || created.State != RevisionAccepted || created.AcceptedAt == "" || created.AcceptedBy != "owner" {
		t.Fatalf("created=%#v", created)
	}
	path := filepath.Join(root, "apps", planTestApp, "deployment-plans", created.ID+".secret")
	persisted, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(persisted), "npm run build") {
		t.Fatalf("protected bundle path=%q err=%v contains command=%t", path, err, strings.Contains(string(persisted), "npm run build"))
	}
	var commands int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('deployment_plan_revisions') WHERE lower(name) LIKE '%command%'`).Scan(&commands); err != nil || commands != 0 {
		t.Fatalf("command columns=%d err=%v", commands, err)
	}
	loaded, err := store.Get(context.Background(), planTestApp)
	if err != nil || loaded.ID != created.ID || loaded.CanonicalDigest != created.CanonicalDigest || loaded.Plan.Components[0].BuildCommand != "npm run build" {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	if _, err := store.Replace(context.Background(), planTestApp, "owner", ReplaceInput{ExpectedRevisionNumber: 0, Plan: testPlan()}); !IsCode(err, "deployment_plan_conflict") {
		t.Fatalf("stale replacement error=%v", err)
	}
	if _, err := db.Exec(`UPDATE deployment_plan_revisions SET detector='changed' WHERE id=?`, created.ID); err == nil {
		t.Fatal("immutable revision update accepted")
	}
}

func TestRecoverDeletesRecognizedOrphanOnly(t *testing.T) {
	db := planDB(t)
	root := t.TempDir()
	store, err := New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Replace(context.Background(), planTestApp, "owner", ReplaceInput{Plan: testPlan()}); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "apps", planTestApp, "deployment-plans")
	orphan := filepath.Join(directory, uuid.NewString()+".secret")
	if err := os.WriteFile(orphan, []byte("not a protected plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan remains: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "unexpected.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(context.Background()); err == nil {
		t.Fatal("unrecognized file accepted")
	}
}

func TestApproveMigrationIsCASAndAudited(t *testing.T) {
	db := planDB(t)
	store, err := New(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	revision, err := store.Replace(context.Background(), planTestApp, "owner", ReplaceInput{Plan: testPlan()})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApproveMigration(context.Background(), planTestApp, revision.ID, revision.RevisionNumber, 0, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.ApproveMigration(context.Background(), planTestApp, revision.ID, revision.RevisionNumber, 0, "owner"); !IsCode(err, "migration_approval_conflict") {
		t.Fatalf("second approval=%v", err)
	}
	loaded, err := store.GetRevision(context.Background(), planTestApp, revision.ID, revision.RevisionNumber)
	if err != nil || loaded.Plan.Migration.Approval.Status != MigrationApprovalApproved || loaded.Plan.Migration.Approval.ActorID != "owner" {
		t.Fatalf("approval=%#v err=%v", loaded.Plan.Migration, err)
	}
	var metadata string
	if err := db.QueryRow(`SELECT metadata_json FROM audit_events WHERE action='deployment_plan.migration.approve'`).Scan(&metadata); err != nil || !strings.Contains(metadata, revision.ID) || strings.Contains(metadata, "npm run migrate") {
		t.Fatalf("audit=%q err=%v", metadata, err)
	}
}

func TestApproveMigrationRejectsSupersededRevision(t *testing.T) {
	db := planDB(t)
	store, err := New(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Replace(context.Background(), planTestApp, "owner", ReplaceInput{Plan: testPlan()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Replace(context.Background(), planTestApp, "owner", ReplaceInput{ExpectedRevisionNumber: first.RevisionNumber, Plan: testPlan()}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApproveMigration(context.Background(), planTestApp, first.ID, first.RevisionNumber, 0, "owner"); !IsCode(err, "deployment_plan_conflict") {
		t.Fatalf("stale approval=%v", err)
	}
}

func planDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES('owner','owner','hash',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`, planTestApp, "plan-app", "Plan App"); err != nil {
		t.Fatal(err)
	}
	return db
}

func testPlan() Plan {
	return Plan{
		Strategy: StrategyGeneratedNode,
		Detector: Detector{Name: "package-json", Version: "1", SourceStructuralFingerprint: strings.Repeat("a", 64)},
		Source:   SourceIdentity{Provider: "github", RepositoryID: 7, ResolvedDigest: strings.Repeat("c", 64)},
		Components: []Component{
			{Name: "web", Role: "server", RootDirectory: "apps/web", PackageManager: "npm", InstallBehavior: "npm ci", NodeVersion: "22", BuildCommand: "npm run build", RunCommand: "npm run start", InternalPort: 3000, HealthProbe: "/health"},
			{Name: "worker", Role: "server", RootDirectory: "apps/worker", PackageManager: "npm", InstallBehavior: "npm ci", NodeVersion: "22", RunCommand: "npm run worker", InternalPort: 3001, HealthProbe: "/health"},
		},
		FieldProvenance: provenanceFor(
			Component{Name: "web", Role: "server", RootDirectory: "apps/web", PackageManager: "npm", InstallBehavior: "npm ci", NodeVersion: "22", BuildCommand: "npm run build", RunCommand: "npm run start", InternalPort: 3000, HealthProbe: "/health"},
			Component{Name: "worker", Role: "server", RootDirectory: "apps/worker", PackageManager: "npm", InstallBehavior: "npm ci", NodeVersion: "22", RunCommand: "npm run worker", InternalPort: 3001, HealthProbe: "/health"},
		),
		Migration: &Migration{Command: "npm run migrate", EvidenceDigest: strings.Repeat("b", 64), Approval: MigrationApproval{Status: MigrationApprovalPending}},
	}
}

func provenanceFor(components ...Component) []FieldProvenance {
	var values []FieldProvenance
	for _, component := range components {
		for _, field := range componentExecutionFields(component) {
			values = append(values, FieldProvenance{Field: field, Origin: ProvenanceInferred, Confidence: 90, Evidence: []string{"package.json"}})
		}
	}
	return values
}
