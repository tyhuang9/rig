package appconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const configTestPlan = "22222222-2222-2222-8222-222222222222"

var configTestComponents = []ComponentTarget{{Name: "api", Role: "server"}, {Name: "web", Role: "static"}}

func testScopedStore(t *testing.T) (*Store, string) {
	t.Helper()
	store, root := testStore(t)
	seedScopedPlan(t, store, configTestPlan, 1)
	return store, root
}

func seedScopedPlan(t *testing.T, store *Store, planID string, number int64) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT OR IGNORE INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES('owner','owner','hash',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO deployment_plan_revisions(id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at)
		VALUES(?,?,?,?,'generated_node','test','1',?,'local',0,?,?,2,0,'','owner',datetime('now'),'accepted','owner',datetime('now'))`, planID, configTestApp, number, "apps/"+configTestApp+"/deployment-plans/"+planID+".secret", strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE deployment_plan_heads SET revision_id=?,revision_number=?,updated_at=datetime('now') WHERE app_id=?`, planID, number, configTestApp); err != nil {
		t.Fatal(err)
	}
}

func valuePointer(value string) *string { return &value }

func TestScopedConfigurationExportsArePhaseAndComponentIsolated(t *testing.T) {
	store, root := testScopedStore(t)
	ctx := context.Background()
	revision, err := store.ReplaceScoped(ctx, configTestApp, "owner", ScopedReplaceInput{
		ExpectedRevisionNumber: 0, PlanRevisionID: configTestPlan, PlanRevisionNumber: 1, Components: configTestComponents,
		Entries: []ScopedValueInput{
			{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "SHARED"}, Sensitivity: SensitivitySecret, Value: valuePointer(`postgres://u:p%2Fword@example.test/db?sslmode=verify-full&x=$VALUE\tail ' "`)},
			{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "MODE"}, Sensitivity: SensitivityPublic, Value: valuePointer("production")},
			{ScopedKey: ScopedKey{Phase: PhaseMigration, Component: "api", Key: "MIGRATION_URL"}, Sensitivity: SensitivitySecret, Value: valuePointer("postgres://migration-secret")},
			{ScopedKey: ScopedKey{Phase: PhaseBuild, Component: "web", Key: "SHARED"}, Sensitivity: SensitivityPublic, Value: valuePointer(`browser $VALUE & exact='yes'`)},
			{ScopedKey: ScopedKey{Phase: PhaseBuild, Component: "web", Key: "VITE_LABEL"}, Sensitivity: SensitivityPublic, Value: valuePointer("release A")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if revision.FormatVersion != 2 || revision.DeploymentPlanRevisionID != configTestPlan || revision.DeploymentPlanRevisionNumber != 1 || len(revision.Entries) != 5 {
		t.Fatalf("scoped revision=%+v", revision)
	}
	for _, entry := range revision.Entries {
		if entry.Sensitive && entry.Value != "" {
			t.Fatalf("secret value returned from Get: %+v", entry)
		}
	}

	identity, err := store.RevisionIdentity(ctx, configTestApp)
	if err != nil || identity.RevisionID != revision.RevisionID || identity.FormatVersion != 2 || identity.DeploymentPlanRevisionID != configTestPlan {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
	runtime, err := store.ExportComponentRuntimeForExecution(ctx, configTestApp, revision.RevisionID, revision.RevisionNumber, configTestPlan, 1, "api")
	if err != nil {
		t.Fatal(err)
	}
	runtimeText := string(runtime.Environment)
	if !strings.Contains(runtimeText, "MODE='production'") || !strings.Contains(runtimeText, "SHARED='postgres://") || strings.Contains(runtimeText, "MIGRATION_URL") || strings.Contains(runtimeText, "VITE_LABEL") || len(runtime.SecretOrigins) != 1 {
		t.Fatalf("runtime export=%q origins=%+v", runtimeText, runtime.SecretOrigins)
	}
	build, err := store.ExportComponentBuildForExecution(ctx, configTestApp, revision.RevisionID, revision.RevisionNumber, configTestPlan, 1, "web")
	if err != nil {
		t.Fatal(err)
	}
	if build.Environment != nil || build.SecretOrigins != nil || len(build.PublicBuildValues) != 2 || build.PublicBuildValues[0].Key != "SHARED" || build.PublicBuildValues[0].Value != `browser $VALUE & exact='yes'` {
		t.Fatalf("build export=%q values=%+v origins=%+v", build.Environment, build.PublicBuildValues, build.SecretOrigins)
	}
	migration, err := store.ExportComponentMigrationForExecution(ctx, configTestApp, revision.RevisionID, revision.RevisionNumber, configTestPlan, 1, "api", []string{"MIGRATION_URL", "MODE"})
	if err != nil || !strings.Contains(string(migration.Environment), "MIGRATION_URL='postgres://migration-secret'") || !strings.Contains(string(migration.Environment), "MODE='production'") || strings.Contains(string(migration.Environment), "SHARED") {
		t.Fatalf("migration export=%q err=%v", migration.Environment, err)
	}
	if _, err := store.ExportRevisionForExecution(ctx, configTestApp, revision.RevisionID, revision.RevisionNumber); !IsCode(err, "configuration_review_required") {
		t.Fatalf("broad v2 export err=%v", err)
	}

	databaseFiles, err := filepath.Glob(filepath.Join(root, "control.db*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range databaseFiles {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(contents), "migration-secret") || strings.Contains(string(contents), "browser $VALUE") {
			t.Fatalf("configuration value found in SQLite file %s", filepath.Base(path))
		}
	}
	if err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	build.Clear()
	if build.Environment != nil || build.PublicBuildValues != nil || build.SecretOrigins != nil {
		t.Fatalf("build export was not cleared: %+v", build)
	}
}

func TestScopedMigrationRejectsAmbiguousRuntimeAndMigrationKey(t *testing.T) {
	store, _ := testScopedStore(t)
	revision, err := store.ReplaceScoped(context.Background(), configTestApp, "owner", ScopedReplaceInput{
		PlanRevisionID: configTestPlan, PlanRevisionNumber: 1, Components: configTestComponents,
		Entries: []ScopedValueInput{
			{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "DATABASE_URL"}, Sensitivity: SensitivitySecret, Value: valuePointer("runtime")},
			{ScopedKey: ScopedKey{Phase: PhaseMigration, Component: "api", Key: "DATABASE_URL"}, Sensitivity: SensitivitySecret, Value: valuePointer("migration")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExportComponentMigrationForExecution(context.Background(), configTestApp, revision.RevisionID, revision.RevisionNumber, configTestPlan, 1, "api", []string{"DATABASE_URL"}); !IsCode(err, "configuration_unavailable") {
		t.Fatalf("ambiguous migration export err=%v", err)
	}
}

func TestScopedMigrationRequiresEveryAllowedKeyAtRevisionZeroAndEmptyLegacyRevision(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	if _, err := store.db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES('owner','owner','hash',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.Replace(ctx, configTestApp, "owner", ReplaceInput{})
	if err != nil {
		t.Fatal(err)
	}
	seedScopedPlan(t, store, configTestPlan, 1)
	for _, revision := range []struct {
		id     string
		number int64
	}{{"", 0}, {legacy.RevisionID, legacy.RevisionNumber}} {
		if _, err := store.ExportComponentMigrationForExecution(ctx, configTestApp, revision.id, revision.number, configTestPlan, 1, "api", []string{"DATABASE_URL"}); !IsCode(err, "configuration_unavailable") {
			t.Fatalf("missing approved key at revision %d: %v", revision.number, err)
		}
		if empty, err := store.ExportComponentMigrationForExecution(ctx, configTestApp, revision.id, revision.number, configTestPlan, 1, "api", nil); err != nil || strings.Contains(string(empty.Environment), "DATABASE_URL") {
			t.Fatalf("empty approved subset at revision %d: %q %v", revision.number, empty.Environment, err)
		}
	}
}

func TestScopedUpgradeMapsOneLegacySecretWithoutRewritingHistory(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	if _, err := store.db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES('owner','owner','hash',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.Replace(ctx, configTestApp, "owner", ReplaceInput{Secrets: []ValueInput{{Key: "DATABASE_URL", Value: "legacy-secret"}}, Variables: []ValueInput{{Key: "MODE", Value: "legacy"}}})
	if err != nil {
		t.Fatal(err)
	}
	seedScopedPlan(t, store, configTestPlan, 1)
	if _, err := store.Replace(ctx, configTestApp, "owner", ReplaceInput{ExpectedRevisionNumber: 1, Variables: []ValueInput{{Key: "UNSCOPED", Value: "rejected"}}}); !IsCode(err, "configuration_review_required") {
		t.Fatalf("accepted generated plan allowed a new legacy revision: %v", err)
	}
	legacyMigration, err := store.ExportComponentMigrationForExecution(ctx, configTestApp, legacy.RevisionID, legacy.RevisionNumber, configTestPlan, 1, "api", []string{"DATABASE_URL"})
	if err != nil || !strings.Contains(string(legacyMigration.Environment), "DATABASE_URL='legacy-secret'") || strings.Contains(string(legacyMigration.Environment), "MODE") {
		t.Fatalf("legacy migration compatibility export=%q err=%v", legacyMigration.Environment, err)
	}
	legacyPath := store.bundlePath(configTestApp, legacy.RevisionID)
	before, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := store.ReplaceScoped(ctx, configTestApp, "owner", ScopedReplaceInput{
		ExpectedRevisionNumber: legacy.RevisionNumber, PlanRevisionID: configTestPlan, PlanRevisionNumber: 1, Components: configTestComponents,
		Entries: []ScopedValueInput{
			{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "DATABASE_URL"}, Sensitivity: SensitivitySecret, Value: nil},
			{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "MODE"}, Sensitivity: SensitivityPublic, Value: valuePointer("legacy")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("legacy protected bundle changed during scoped upgrade")
	}
	oldExport, err := store.ExportRevisionForExecution(ctx, configTestApp, legacy.RevisionID, legacy.RevisionNumber)
	if err != nil || !strings.Contains(string(oldExport.Environment), "DATABASE_URL='legacy-secret'") {
		t.Fatalf("legacy revision unreadable: %q err=%v", oldExport.Environment, err)
	}
	newExport, err := store.ExportComponentRuntimeForExecution(ctx, configTestApp, scoped.RevisionID, scoped.RevisionNumber, configTestPlan, 1, "api")
	if err != nil || !strings.Contains(string(newExport.Environment), "DATABASE_URL='legacy-secret'") {
		t.Fatalf("scoped preserved secret export=%q err=%v", newExport.Environment, err)
	}
}

func TestReplaceScopedRejectsInvalidExposureAndTransport(t *testing.T) {
	store, _ := testScopedStore(t)
	large := strings.Repeat("x", (8<<10)+1)
	tests := []ScopedReplaceInput{
		{Entries: []ScopedValueInput{{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "RIG_RUNTIME_INTERNAL_PORT"}, Sensitivity: SensitivityPublic, Value: valuePointer("1")}}},
		{Entries: []ScopedValueInput{{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "VITE_TOKEN"}, Sensitivity: SensitivitySecret, Value: valuePointer("secret")}}},
		{Entries: []ScopedValueInput{{ScopedKey: ScopedKey{Phase: PhaseBuild, Component: "web", Key: "TOKEN"}, Sensitivity: SensitivitySecret, Value: valuePointer("secret")}}},
		{Entries: []ScopedValueInput{{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "web", Key: "TOKEN"}, Sensitivity: SensitivitySecret, Value: valuePointer("secret")}}},
		{Entries: []ScopedValueInput{{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "missing", Key: "TOKEN"}, Sensitivity: SensitivitySecret, Value: valuePointer("secret")}}},
		{Entries: []ScopedValueInput{{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "TOKEN"}, Sensitivity: SensitivitySecret, Value: valuePointer("line\nbreak")}}},
		{Entries: []ScopedValueInput{{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "TOKEN"}, Sensitivity: SensitivitySecret, Value: valuePointer("nul\x00value")}}},
		{Entries: []ScopedValueInput{{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "TOKEN"}, Sensitivity: SensitivitySecret, Value: &large}}},
		{Entries: []ScopedValueInput{
			{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "TOKEN"}, Sensitivity: SensitivityPublic, Value: valuePointer("one")},
			{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "TOKEN"}, Sensitivity: SensitivityPublic, Value: valuePointer("two")},
		}},
	}
	for index := range tests {
		input := tests[index]
		input.PlanRevisionID = configTestPlan
		input.PlanRevisionNumber = 1
		input.Components = configTestComponents
		if _, err := store.ReplaceScoped(context.Background(), configTestApp, "owner", input); !IsCode(err, "invalid_configuration") {
			t.Fatalf("case %d err=%v", index, err)
		}
	}
	identity, err := store.RevisionIdentity(context.Background(), configTestApp)
	if err != nil || identity.RevisionNumber != 0 {
		t.Fatalf("invalid input advanced configuration head: %+v err=%v", identity, err)
	}
	configurationRoot := filepath.Join(store.root, configTestApp, "configuration")
	if entries, err := os.ReadDir(configurationRoot); err == nil && len(entries) != 0 {
		t.Fatalf("invalid writes left protected files: %v", entries)
	}
}

func TestReplaceScopedCASCleansLosingProtectedFile(t *testing.T) {
	store, root := testScopedStore(t)
	winner, err := New(store.db, root)
	if err != nil {
		t.Fatal(err)
	}
	store.beforeTransaction = func() {
		store.beforeTransaction = nil
		if _, err := winner.ReplaceScoped(context.Background(), configTestApp, "owner", ScopedReplaceInput{
			PlanRevisionID: configTestPlan, PlanRevisionNumber: 1, Components: configTestComponents,
			Entries: []ScopedValueInput{{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "WINNER"}, Sensitivity: SensitivitySecret, Value: valuePointer("kept")}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = store.ReplaceScoped(context.Background(), configTestApp, "owner", ScopedReplaceInput{
		PlanRevisionID: configTestPlan, PlanRevisionNumber: 1, Components: configTestComponents,
		Entries: []ScopedValueInput{{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "LOSER"}, Sensitivity: SensitivitySecret, Value: valuePointer("removed")}},
	})
	if !IsCode(err, "configuration_conflict") {
		t.Fatalf("losing write err=%v", err)
	}
	files, err := os.ReadDir(filepath.Join(root, "apps", configTestApp, "configuration"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("configuration files after CAS=%v", files)
	}
	if err := store.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceScopedPlanDriftCleansProtectedFile(t *testing.T) {
	store, root := testScopedStore(t)
	const replacementPlan = "33333333-3333-3333-8333-333333333333"
	store.beforeTransaction = func() {
		store.beforeTransaction = nil
		seedScopedPlan(t, store, replacementPlan, 2)
	}
	_, err := store.ReplaceScoped(context.Background(), configTestApp, "owner", ScopedReplaceInput{
		PlanRevisionID: configTestPlan, PlanRevisionNumber: 1, Components: configTestComponents,
		Entries: []ScopedValueInput{{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "TOKEN"}, Sensitivity: SensitivitySecret, Value: valuePointer("removed")}},
	})
	if !IsCode(err, "configuration_review_required") {
		t.Fatalf("plan drift err=%v", err)
	}
	configurationRoot := filepath.Join(root, "apps", configTestApp, "configuration")
	if files, readErr := os.ReadDir(configurationRoot); readErr != nil || len(files) != 0 {
		t.Fatalf("plan drift residue files=%v err=%v", files, readErr)
	}
	identity, identityErr := store.RevisionIdentity(context.Background(), configTestApp)
	if identityErr != nil || identity.RevisionNumber != 0 {
		t.Fatalf("plan drift advanced configuration: %+v err=%v", identity, identityErr)
	}
}

func TestReplaceScopedRebindsEmptyConfigurationWithoutCarryingOldSecrets(t *testing.T) {
	store, _ := testScopedStore(t)
	ctx := context.Background()
	initial, err := store.ReplaceScoped(ctx, configTestApp, "owner", ScopedReplaceInput{
		PlanRevisionID: configTestPlan, PlanRevisionNumber: 1, Components: configTestComponents,
		Entries: []ScopedValueInput{{ScopedKey: ScopedKey{Phase: PhaseRuntime, Component: "api", Key: "OLD_TOKEN"}, Sensitivity: SensitivitySecret, Value: valuePointer("old-plan-secret")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []struct {
		id     string
		number int64
	}{
		{"33333333-3333-3333-8333-333333333333", 2},
		{"44444444-4444-4444-8444-444444444444", 3},
	} {
		seedScopedPlan(t, store, next.id, next.number)
		current, err := store.ReplaceScoped(ctx, configTestApp, "owner", ScopedReplaceInput{
			ExpectedRevisionNumber: next.number - 1,
			PlanRevisionID:         next.id, PlanRevisionNumber: next.number, Components: configTestComponents,
		})
		if err != nil {
			t.Fatalf("rebind empty configuration to plan %d: %v", next.number, err)
		}
		if current.RevisionNumber != next.number || current.DeploymentPlanRevisionID != next.id || len(current.Entries) != 0 {
			t.Fatalf("rebind retained entries or wrong plan: %+v", current)
		}
		_, err = store.ExportComponentRuntimeForExecution(ctx, configTestApp, current.RevisionID, current.RevisionNumber, next.id, next.number, "api")
		if err != nil {
			t.Fatalf("export empty configuration for plan %d: %v", next.number, err)
		}
	}
	stored, err := store.readRevisionBundle(ctx, configTestApp, initial.RevisionID, initial.RevisionNumber)
	if err != nil || stored.Version != 2 || len(stored.Scoped.Entries) != 1 || stored.Scoped.Entries[0].Value != "old-plan-secret" {
		t.Fatalf("historical protected revision changed: %+v err=%v", stored, err)
	}
}
