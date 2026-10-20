//go:build live_docker

package generatedimage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedruntime"
)

func TestHostingLiveConfigurationBootstrapAndRevisionChain(t *testing.T) {
	ctx := context.Background()
	dataRoot := filepath.Join(t.TempDir(), "data")
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	appID, planID := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES('hosting-live','hosting-live','test',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`, appID, appID, appID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at) VALUES(?,?,1,?,'generated_node','hosting-live-configuration','1',?,'local',0,?,?,2,0,'','hosting-live',datetime('now'),'accepted','hosting-live',datetime('now'))`, planID, appID, "hosting-live-plan:"+planID, strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE deployment_plan_heads SET revision_id=?,revision_number=1,updated_at=datetime('now') WHERE app_id=?`, planID, appID); err != nil {
		t.Fatal(err)
	}
	plan := deploymentplans.DeploymentPlanRevision{
		ID: planID, AppID: appID, RevisionNumber: 1,
		Plan: deploymentplans.Plan{Components: []deploymentplans.Component{
			{Name: "api", Role: generatedruntime.RoleServer},
			{Name: "frontend", Role: generatedruntime.RoleStatic},
		}},
	}
	store, err := appconfig.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := hostingLiveBootstrapConfiguration(t, ctx, store, appID, plan)
	var headID, bundleRef string
	var headNumber int64
	var bundleVersion, variableCount, secretCount int
	if err := db.QueryRow(`SELECT revision_id,revision_number FROM application_configuration_heads WHERE app_id=?`, appID).Scan(&headID, &headNumber); err != nil || headID != bootstrap.RevisionID || headNumber != 1 {
		t.Fatalf("bootstrap head=%q/%d error=%v", headID, headNumber, err)
	}
	if err := db.QueryRow(`SELECT bundle_ref,bundle_version,variable_count,secret_count FROM application_configuration_revisions WHERE id=?`, bootstrap.RevisionID).Scan(&bundleRef, &bundleVersion, &variableCount, &secretCount); err != nil || bundleRef != filepath.ToSlash(filepath.Join("apps", appID, "configuration", bootstrap.RevisionID+".secret")) || bundleVersion != 2 || variableCount != 0 || secretCount != 0 {
		t.Fatalf("bootstrap bundle metadata=%q version=%d variables=%d secrets=%d error=%v", bundleRef, bundleVersion, variableCount, secretCount, err)
	}
	if info, err := os.Stat(filepath.Join(dataRoot, "apps", appID, "configuration", bootstrap.RevisionID+".secret")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("protected bootstrap bundle missing: %v", err)
	}

	settings := hostingLiveSettings{
		dbURL:    "postgresql://fixture-user:fixture-password@fixture.test/notes?sslmode=verify-full",
		httpsURL: "https://fixture.test/dependency",
		ca:       "fixture-ca-a",
		token:    "fixture-token",
		sentinel: hostingLiveSentinelPrefix + "fixture-only",
	}
	initial := hostingLiveReplaceConfig(t, ctx, store, appID, plan, bootstrap.RevisionNumber, settings, "runtime-A", true)
	settings.ca = "fixture-ca-bad"
	bad := hostingLiveReplaceConfig(t, ctx, store, appID, plan, initial.RevisionNumber, settings, "bad-ca", false)
	settings.ca = "fixture-ca-b"
	final := hostingLiveReplaceConfig(t, ctx, store, appID, plan, bad.RevisionNumber, settings, "runtime-B", false)
	if initial.RevisionNumber != 2 || bad.RevisionNumber != 3 || final.RevisionNumber != 4 {
		t.Fatalf("scoped revision chain=%d,%d,%d", initial.RevisionNumber, bad.RevisionNumber, final.RevisionNumber)
	}
	var revisionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_configuration_revisions WHERE app_id=?`, appID).Scan(&revisionCount); err != nil || revisionCount != 4 {
		t.Fatalf("retained configuration revisions=%d error=%v", revisionCount, err)
	}
	badExport, err := store.ExportComponentRuntimeForExecution(ctx, appID, bad.RevisionID, bad.RevisionNumber, plan.ID, plan.RevisionNumber, "api")
	if err != nil {
		t.Fatal(err)
	}
	badEnvironment := string(badExport.Environment)
	if !strings.Contains(badEnvironment, "DATABASE_TLS_CA_PEM_BASE64=fixture-ca-bad\n") || !strings.Contains(badEnvironment, "NOTES_FIXTURE_DB_URL="+settings.dbURL+"\n") || !strings.Contains(badEnvironment, "NOTES_FIXTURE_HTTPS_TOKEN="+settings.token+"\n") || len(badExport.SecretOrigins) != 5 {
		badExport.Clear()
		t.Fatal("intermediate revision did not replace the selected CA and preserve the other required secrets")
	}
	badExport.Clear()

	runtimeExport, err := store.ExportComponentRuntimeForExecution(ctx, appID, final.RevisionID, final.RevisionNumber, plan.ID, plan.RevisionNumber, "api")
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeExport.Clear()
	runtimeEnvironment := string(runtimeExport.Environment)
	for _, expected := range []string{
		"API_RUNTIME_MARKER=runtime-B\n",
		"DATABASE_TLS_CA_PEM_BASE64=fixture-ca-b\n",
		"NOTES_FIXTURE_DB_URL=" + settings.dbURL + "\n",
		"NOTES_FIXTURE_HTTPS_TOKEN=" + settings.token + "\n",
		"TEST_SENTINEL_SECRET=" + settings.sentinel + "\n",
	} {
		if !strings.Contains(runtimeEnvironment, expected) {
			t.Fatalf("final API runtime export omitted exact scoped value for %q", strings.SplitN(expected, "=", 2)[0])
		}
	}
	if strings.Contains(runtimeEnvironment, "VITE_BUILD_LABEL") || len(runtimeExport.SecretOrigins) != 5 {
		t.Fatal("API runtime export crossed build scope or lost preserved secret origins")
	}
	buildExport, err := store.ExportComponentBuildForExecution(ctx, appID, final.RevisionID, final.RevisionNumber, plan.ID, plan.RevisionNumber, "frontend")
	if err != nil {
		t.Fatal(err)
	}
	defer buildExport.Clear()
	if buildExport.Environment != nil || buildExport.SecretOrigins != nil || len(buildExport.PublicBuildValues) != 1 || buildExport.PublicBuildValues[0].Key != "VITE_BUILD_LABEL" || buildExport.PublicBuildValues[0].Value != "public-build-A" {
		t.Fatal("frontend build export crossed runtime or secret scope")
	}
	for _, entry := range final.Entries {
		if entry.Sensitive && entry.Value != "" {
			t.Fatal("configuration metadata exposed a protected value")
		}
	}
}
