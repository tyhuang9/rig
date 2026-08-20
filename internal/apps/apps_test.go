package apps

import (
	"database/sql"
	"testing"

	"github.com/hostd/hostd/internal/database"
)

func TestCreateLocalApplicationPersistsExactlyOneSource(t *testing.T) {
	db := openTestDB(t)
	app, err := New(db).Create("Local App", "", " C:/apps/local ", "")
	if err != nil {
		t.Fatal(err)
	}
	if app.Source.Type != SourceLocal || app.Source.Path != "C:/apps/local" {
		t.Fatalf("source = %#v", app.Source)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_sources WHERE application_id=?`, app.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("source rows = %d", count)
	}
}

func TestCreateGithubApplicationIsAtomicAndCredentialFree(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES ('owner','owner','hash',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at) VALUES ('0123456789abcdef0123456789abcdef','owner','github','connected','1','octo',1,datetime('now','+1 hour'),datetime('now','+1 day'),datetime('now'),datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	source := Source{Type: SourceGitHub, ConnectionID: "0123456789abcdef0123456789abcdef", InstallationID: 3, RepositoryID: 7, RepositoryOwner: "renamed", RepositoryName: "repo", TrackedBranch: "main", TrackedRef: "refs/heads/main", ComposePath: "deploy/compose.yaml", ResolvedSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	app, err := New(db).CreateWithSource("GitHub App", "", "", source)
	if err != nil {
		t.Fatal(err)
	}
	if app.Source.RepositoryID != 7 || app.Source.Path != "" {
		t.Fatalf("source = %#v", app.Source)
	}
	var schema string
	if err := db.QueryRow(`SELECT group_concat(sql,' ') FROM sqlite_master`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"access_token", "refresh_token", "device_code"} {
		if containsFold(schema, secret) {
			t.Fatalf("database schema contains %q", secret)
		}
	}
	if _, err := New(db).CreateWithSource("Broken", "", "", Source{Type: SourceGitHub}); err == nil {
		t.Fatal("invalid GitHub source accepted")
	}
	var broken int
	if err := db.QueryRow(`SELECT COUNT(*) FROM applications WHERE slug='broken'`).Scan(&broken); err != nil {
		t.Fatal(err)
	}
	if broken != 0 {
		t.Fatal("invalid application was partially persisted")
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,created_at,updated_at) VALUES ('local','Local','local','ready','windows','amd64','localhost',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func containsFold(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		match := true
		for j := range part {
			a, b := value[i+j], part[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
