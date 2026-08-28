package database

import (
	"bytes"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestEveryEmbeddedMigrationMirrorsPublicMigration(t *testing.T) {
	embedded := migrationNames(t, migrations, "migrations")
	publicDir := filepath.Join("..", "..", "db", "migrations")
	public := migrationNames(t, os.DirFS(publicDir), ".")
	if strings.Join(embedded, "\n") != strings.Join(public, "\n") {
		t.Fatalf("embedded migrations %v do not match public migrations %v", embedded, public)
	}
	for _, name := range embedded {
		got, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(publicDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("migration %s is not byte-mirrored", name)
		}
	}
}

func TestMigrateFreshUpgradePreservesDataAndIsIdempotent(t *testing.T) {
	db := openMemoryDatabase(t)
	first, err := migrations.ReadFile("migrations/001_foundation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations VALUES ('001_foundation.sql', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id, username, passphrase_hash, created_at, updated_at) VALUES ('owner', 'owner', 'hash', datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := Migrate(db); err != nil {
			t.Fatalf("Migrate pass %d: %v", i+1, err)
		}
	}
	var users, versions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = 'owner'`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if users != 1 || versions != 8 {
		t.Fatalf("preserved users = %d, migration versions = %d", users, versions)
	}
}

func TestReleaseSnapshotMigrationPreservesLegacyReleasesAndPreventsReadyDuplicates(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES ('app','app','App','draft',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at) VALUES ('legacy','app','ready','{}',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,workspace_state) VALUES ('ready','app','ready','{}',datetime('now'),'github',7,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','compose.yaml','ready')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,workspace_state) VALUES ('duplicate','app','ready','{}',datetime('now'),'github',7,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','compose.yaml','ready')`); err == nil {
		t.Fatal("duplicate ready snapshot was accepted")
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,workspace_state) VALUES ('failed','app','failed','{}',datetime('now'),'github',7,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','compose.yaml','failed')`); err != nil {
		t.Fatalf("failed retry was rejected: %v", err)
	}
	var state sql.NullString
	if err := db.QueryRow(`SELECT workspace_state FROM releases WHERE id='legacy'`).Scan(&state); err != nil || state.Valid {
		t.Fatalf("legacy workspace state = %#v, %v", state, err)
	}
}

func TestApplicationConfigurationMigrationBackfillsAndEnforcesRevisionIntegrity(t *testing.T) {
	db := openMemoryDatabase(t)
	for _, name := range []string{"001_foundation.sql", "002_github_connections.sql", "003_application_sources.sql", "004_release_snapshots.sql"} {
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES('legacy','legacy','Legacy','draft',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/005_application_configuration.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}

	for _, app := range []string{"legacy", "future"} {
		if app == "future" {
			if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES('future','future','Future','draft',datetime('now'),datetime('now'))`); err != nil {
				t.Fatal(err)
			}
		}
		var number int64
		var id sql.NullString
		if err := db.QueryRow(`SELECT revision_id,revision_number FROM application_configuration_heads WHERE app_id=?`, app).Scan(&id, &number); err != nil || id.Valid || number != 0 {
			t.Fatalf("%s head id=%v number=%d err=%v", app, id, number, err)
		}
	}
	if _, err := db.Exec(`UPDATE application_configuration_heads SET revision_id='missing',revision_number=1,updated_at=datetime('now') WHERE app_id='legacy'`); err == nil {
		t.Fatal("revision-zero pairing accepted an unknown revision")
	}
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES('owner','owner','hash',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO application_configuration_revisions(id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count) VALUES('rev-a','legacy',1,'legacy/rev-a.bundle','owner',datetime('now'),0,1),('rev-b','future',1,'future/rev-b.bundle',NULL,datetime('now'),1,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE application_configuration_heads SET revision_id='rev-a',revision_number=1,updated_at=datetime('now') WHERE app_id='legacy'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE application_configuration_heads SET revision_id='rev-b',revision_number=1,updated_at=datetime('now') WHERE app_id='legacy'`); err == nil {
		t.Fatal("cross-app head revision accepted")
	}

	invalidReleaseStatements := []string{
		`INSERT INTO releases(id,app_id,status,metadata_json,created_at,configuration_revision_id,configuration_revision_number) VALUES('cross','legacy','ready','{}',datetime('now'),'rev-b',1)`,
		`INSERT INTO releases(id,app_id,status,metadata_json,created_at,configuration_revision_id,configuration_revision_number) VALUES('wrong-number','legacy','ready','{}',datetime('now'),'rev-a',2)`,
		`INSERT INTO releases(id,app_id,status,metadata_json,created_at,configuration_revision_number) VALUES('negative','legacy','ready','{}',datetime('now'),-1)`,
	}
	for _, statement := range invalidReleaseStatements {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("invalid release accepted: %s", statement)
		}
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,configuration_revision_id,configuration_revision_number) VALUES('valid','legacy','ready','{}',datetime('now'),'rev-a',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE releases SET configuration_revision_id=NULL,configuration_revision_number=0 WHERE id='valid'`); err == nil {
		t.Fatal("release configuration was mutable")
	}
	if _, err := db.Exec(`UPDATE releases SET app_id='future' WHERE id='valid'`); err == nil {
		t.Fatal("release was allowed to cross application configuration ownership")
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at) VALUES('revision-zero','legacy','ready','{}',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE releases SET app_id='future' WHERE id='revision-zero'`); err == nil {
		t.Fatal("revision-zero release application was mutable")
	}
	if _, err := db.Exec(`UPDATE application_configuration_revisions SET bundle_ref='changed' WHERE id='rev-a'`); err == nil {
		t.Fatal("configuration revision was mutable")
	}
	if _, err := db.Exec(`DELETE FROM application_configuration_revisions WHERE id='rev-a'`); err == nil {
		t.Fatal("referenced configuration revision was deletable")
	}
	if _, err := db.Exec(`UPDATE application_configuration_revisions SET created_by='owner' WHERE id='rev-b'`); err == nil {
		t.Fatal("immutable revision unexpectedly allowed actor reassignment")
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id='owner'`); err == nil {
		t.Fatal("configuration revision creator was deleted")
	}
	// Configuration history is intentionally retained. Applications with
	// revisions must be archived rather than hard-deleted.
	if _, err := db.Exec(`DELETE FROM applications WHERE id='legacy'`); err == nil {
		t.Fatal("application configuration history was cascade-deleted")
	}
}

func TestComposeRuntimeMigrationEnforcesDeploymentAndApprovalIntegrity(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES('owner','owner','hash',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"app-a", "app-b"} {
		if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`, app, app, app); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at) VALUES('release-a','app-a','ready','{}',datetime('now')),('release-b','app-b','ready','{}',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,created_at,updated_at) VALUES('job-a','deploy','application','app-a','queued','queued','owner',datetime('now'),datetime('now')),('job-b','deploy','application','app-b','queued','queued','owner',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployments(id,app_id,release_id,job_id,status) VALUES('bad','app-a','release-b','job-a','preparing')`); err == nil {
		t.Fatal("cross-application release linkage was accepted")
	}
	if _, err := db.Exec(`INSERT INTO deployments(id,app_id,release_id,job_id,status) VALUES('deployment-a','app-a','release-a','job-a','preparing')`); err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("a", 64)
	if _, err := db.Exec(`INSERT INTO deployment_policy_findings(id,deployment_id,policy_version,capability,scope,fingerprint,disposition,created_at) VALUES('finding','deployment-a','compose-v1','host_bind','/srv/data',?,'approval_required',datetime('now'))`, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO runtime_approvals(id,app_id,policy_version,capability,scope,fingerprint,granted_by,granted_at) VALUES('approval','app-a','compose-v1','host_bind','/srv/data',?,'owner',datetime('now'))`, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE deployments SET status='applying' WHERE id='deployment-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE runtime_approvals SET revoked_by='owner',revoked_at=datetime('now') WHERE id='approval'`); err == nil {
		t.Fatal("approval used by an applying deployment was revoked")
	}
	if _, err := db.Exec(`UPDATE deployments SET status='failed' WHERE id='deployment-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE runtime_approvals SET revoked_by='owner',revoked_at=datetime('now') WHERE id='approval'`); err != nil {
		t.Fatalf("inactive approval could not be revoked: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM runtime_approvals WHERE id='approval'`); err == nil {
		t.Fatal("approval history was deletable")
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,workspace_state,workspace_size_bytes,workspace_pruned_at) VALUES('release-unavailable','app-a','ready','{}',datetime('now'),'pruned',0,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployments(id,app_id,release_id,status) VALUES('deployment-unavailable','app-a','release-unavailable','preparing')`); err == nil {
		t.Fatal("deployment linked a pruned release workspace")
	}
	input := `{"releaseId":"release-unavailable","configurationMode":"current"}`
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,input_json,created_at,updated_at) VALUES('job-unavailable','deploy','application','app-a','queued','queued',?,datetime('now'),datetime('now'))`, input); err == nil {
		t.Fatal("active job selected a pruned release workspace")
	}
}

func TestComposeRuntimeMigrationCapsEventsPerAttempt(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,attempt,created_at,updated_at) VALUES('job','test','machine','machine','running','running',1,datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	for sequence := 1; sequence <= 32; sequence++ {
		if _, err := db.Exec(`INSERT INTO job_events(job_id,sequence,attempt,timestamp,level,phase,code,message) VALUES('job',?,1,datetime('now'),'info','running','phase_started','safe')`, sequence); err != nil {
			t.Fatalf("event %d: %v", sequence, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO job_events(job_id,sequence,attempt,timestamp,level,phase,code,message) VALUES('job',33,1,datetime('now'),'info','running','phase_started','safe')`); err == nil {
		t.Fatal("33rd event in an attempt was accepted")
	}
	if _, err := db.Exec(`UPDATE jobs SET attempt=2 WHERE id='job'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO job_events(job_id,sequence,attempt,timestamp,level,phase,code,message) VALUES('job',33,2,datetime('now'),'info','running','phase_started','safe')`); err != nil {
		t.Fatalf("first event in next attempt: %v", err)
	}
}

func TestReleaseSnapshotMigrationContainsNoSensitiveMaterial(t *testing.T) {
	body, err := migrations.ReadFile("migrations/004_release_snapshots.sql")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(body))
	for _, forbidden := range []string{"access_token", "refresh_token", "archive_body", "compose_document", "secret", "variable"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("migration contains forbidden material %q", forbidden)
		}
	}
}

func TestApplicationSourceBackfillConstraintsAndCascade(t *testing.T) {
	db := openMemoryDatabase(t)
	first, err := migrations.ReadFile("migrations/001_foundation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations VALUES ('001_foundation.sql', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,source_path,status,created_at,updated_at) VALUES ('app','app','App','C:/apps/app','draft',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var sourceType string
	if err := db.QueryRow(`SELECT source_type FROM application_sources WHERE application_id='app'`).Scan(&sourceType); err != nil {
		t.Fatal(err)
	}
	if sourceType != "local" {
		t.Fatalf("backfilled source type = %q", sourceType)
	}
	if _, err := db.Exec(`INSERT INTO application_sources(application_id,source_type,created_at,updated_at) VALUES ('app','local',datetime('now'),datetime('now'))`); err == nil {
		t.Fatal("second source row was accepted")
	}
	if _, err := db.Exec(`UPDATE application_sources SET source_type='github', connection_id='missing', installation_id=1, repository_id=2, repository_owner='o', repository_name='r', tracked_branch='main', tracked_ref='refs/heads/main', compose_path='../compose.yaml', resolved_sha='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE application_id='app'`); err == nil {
		t.Fatal("invalid GitHub source was accepted")
	}
	if _, err := db.Exec(`DELETE FROM applications WHERE id='app'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_sources WHERE application_id='app'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("cascaded sources = %d", count)
	}
}

func TestGithubConnectionConstraintsAndCascade(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id, username, passphrase_hash, created_at, updated_at) VALUES ('owner', 'owner', 'hash', datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_connections(id, owner_user_id, provider, status, pending_expires_at, poll_interval_seconds, next_poll_at, created_at, updated_at) VALUES ('one', 'owner', 'github', 'pending', datetime('now','+10 minutes'), 5, datetime('now'), datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO github_installations(connection_id, installation_id, account_login, account_type, target_type, repository_selection, cached_at) VALUES ('one', 7, 'octo', 'Organization', 'Organization', 'selected', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_connections(id, owner_user_id, provider, status, created_at, updated_at) VALUES ('bad', 'owner', 'other', 'connected', datetime('now'), datetime('now'))`); err == nil {
		t.Fatal("unsupported provider was accepted")
	}
	if _, err := db.Exec(`INSERT INTO source_connections(id, owner_user_id, provider, status, provider_user_id, provider_login, credential_generation, access_expires_at, refresh_expires_at, connected_at, created_at, updated_at) VALUES ('incomplete', 'owner', 'github', 'connected', '41', 'octo', 0, NULL, NULL, NULL, datetime('now'), datetime('now'))`); err == nil {
		t.Fatal("connected row without credential metadata was accepted")
	}
	if _, err := db.Exec(`INSERT INTO source_connections(id, owner_user_id, provider, status, provider_user_id, provider_login, credential_generation, access_expires_at, refresh_expires_at, connected_at, created_at, updated_at) VALUES ('two', 'owner', 'github', 'connected', '42', 'octo', 1, datetime('now','+1 hour'), datetime('now','+1 day'), datetime('now'), datetime('now'), datetime('now')), ('three', 'owner', 'github', 'connected', '42', 'octo', 1, datetime('now','+1 hour'), datetime('now','+1 day'), datetime('now'), datetime('now'), datetime('now'))`); err == nil {
		t.Fatal("duplicate active owner identity was accepted")
	}
	if _, err := db.Exec(`DELETE FROM source_connections WHERE id = 'one'`); err != nil {
		t.Fatal(err)
	}
	var installations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM github_installations WHERE connection_id = 'one'`).Scan(&installations); err != nil {
		t.Fatal(err)
	}
	if installations != 0 {
		t.Fatalf("cascaded installations = %d", installations)
	}
}

func TestGithubMigrationContainsNoCredentialMaterial(t *testing.T) {
	body, err := migrations.ReadFile("migrations/002_github_connections.sql")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(body))
	for _, forbidden := range []string{"access_token", "refresh_token", "device_code", "credential_path", "provider_body", "provider_description"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("migration contains forbidden credential field %q", forbidden)
		}
	}
}

func migrationNames(t *testing.T, filesystem fs.FS, directory string) []string {
	t.Helper()
	entries, err := fs.ReadDir(filesystem, directory)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func openMemoryDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	return db
}
