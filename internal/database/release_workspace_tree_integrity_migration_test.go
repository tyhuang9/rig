package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const releaseWorkspaceTreeIntegrityMigration = "016_release_workspace_tree_integrity.sql"

func TestReleaseWorkspaceTreeIntegrityMigration(t *testing.T) {
	t.Run("mirror", func(t *testing.T) {
		embedded, err := migrations.ReadFile("migrations/" + releaseWorkspaceTreeIntegrityMigration)
		if err != nil {
			t.Fatal(err)
		}
		public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", releaseWorkspaceTreeIntegrityMigration))
		if err != nil {
			t.Fatal(err)
		}
		if string(embedded) != string(public) {
			t.Fatal("embedded migration differs from public migration")
		}
	})

	t.Run("fresh and trigger", func(t *testing.T) {
		db := openMemoryDatabase(t)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		seedReleaseWorkspaceTreeApplication(t, db, "tree-fresh")
		valid := strings.Repeat("a", 64)
		if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,workspace_state)
			VALUES('tree-materializing','tree-fresh','materializing','{}',datetime('now'),'github',7,?,'compose.yaml','materializing')`, strings.Repeat("b", 40)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,workspace_state,workspace_tree_sha256)
			VALUES('tree-ready-valid','tree-fresh','ready','{}',datetime('now'),'github',8,?,'compose.yaml','ready',?)`, strings.Repeat("c", 40), valid); err != nil {
			t.Fatalf("ready insert with valid digest: %v", err)
		}
		for index, digest := range []any{nil, strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("g", 64)} {
			if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,workspace_state,workspace_tree_sha256)
				VALUES(?,?, 'ready','{}',datetime('now'),'github',?,?, 'compose.yaml','ready',?)`, "tree-ready-invalid-"+string(rune('0'+index)), "tree-fresh", index+9, strings.Repeat(string(rune('d'+index)), 40), digest); err == nil {
				t.Fatalf("ready insert with invalid digest %q was accepted", digest)
			}
		}
		if _, err := db.Exec(`UPDATE releases SET workspace_tree_sha256=?,workspace_state='ready' WHERE id='tree-materializing'`, valid); err != nil {
			t.Fatalf("valid materializing-to-ready transition: %v", err)
		}
		if _, err := db.Exec(`UPDATE releases SET workspace_tree_sha256=? WHERE id='tree-materializing'`, valid); err != nil {
			t.Fatalf("same ready digest update: %v", err)
		}
		if _, err := db.Exec(`UPDATE releases SET workspace_tree_sha256=? WHERE id='tree-materializing'`, strings.Repeat("f", 64)); err == nil {
			t.Fatal("ready digest replacement was accepted")
		}
		if _, err := db.Exec(`UPDATE releases SET workspace_tree_sha256=NULL WHERE id='tree-materializing'`); err == nil {
			t.Fatal("ready digest clearing was accepted")
		}
		if _, err := db.Exec(`UPDATE releases SET status='failed',workspace_state='failed' WHERE id='tree-materializing'`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE releases SET workspace_tree_sha256=? WHERE id='tree-materializing'`, strings.Repeat("e", 64)); err == nil {
			t.Fatal("non-null failed digest replacement was accepted")
		}
		if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,workspace_state)
			VALUES('tree-invalid','tree-fresh','materializing','{}',datetime('now'),'github',8,?,'compose.yaml','materializing')`, strings.Repeat("c", 40)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE releases SET workspace_state='ready' WHERE id='tree-invalid'`); err == nil {
			t.Fatal("materializing-to-ready transition without digest was accepted")
		}
		var applied int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, releaseWorkspaceTreeIntegrityMigration).Scan(&applied); err != nil || applied != 1 {
			t.Fatalf("fresh migration record=%d err=%v", applied, err)
		}
	})

	t.Run("upgrade and backfill", func(t *testing.T) {
		db := openMemoryDatabase(t)
		applyMigrationsThrough(t, db, "015_relay_binding_owner_removable.sql")
		seedReleaseWorkspaceTreeApplication(t, db, "tree-upgrade")
		valid := strings.Repeat("e", 64)
		insertReleaseWorkspaceTreeHistory(t, db, "tree-local-valid", "tree-upgrade", "local", 0, strings.Repeat("1", 40), valid)
		insertReleaseWorkspaceTreeHistory(t, db, "tree-local-invalid", "tree-upgrade", "local", 0, strings.Repeat("2", 40), strings.Repeat("E", 64))
		insertReleaseWorkspaceTreeHistory(t, db, "tree-github-legacy", "tree-upgrade", "github", 7, strings.Repeat("3", 40), valid)
		insertReleaseWorkspaceTreeHistory(t, db, "tree-github-legacy-repair", "tree-upgrade", "github", 8, strings.Repeat("4", 40), valid)
		if _, err := db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		for _, name := range migrationNames(t, migrations, "migrations") {
			if name == releaseWorkspaceTreeIntegrityMigration {
				break
			}
			if _, err := db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,datetime('now'))`, name); err != nil {
				t.Fatalf("record %s: %v", name, err)
			}
		}
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		if err := Migrate(db); err != nil {
			t.Fatalf("idempotent upgrade: %v", err)
		}
		for _, test := range []struct {
			id   string
			want sql.NullString
		}{
			{"tree-local-valid", sql.NullString{String: valid, Valid: true}},
			{"tree-local-invalid", sql.NullString{}},
			{"tree-github-legacy", sql.NullString{}},
			{"tree-github-legacy-repair", sql.NullString{}},
		} {
			var got sql.NullString
			if err := db.QueryRow(`SELECT workspace_tree_sha256 FROM releases WHERE id=?`, test.id).Scan(&got); err != nil || got != test.want {
				t.Fatalf("backfill %s=%#v want=%#v err=%v", test.id, got, test.want, err)
			}
		}
		if _, err := db.Exec(`UPDATE releases SET workspace_tree_sha256=? WHERE id='tree-github-legacy-repair'`, valid); err != nil {
			t.Fatalf("legacy null-to-valid digest repair: %v", err)
		}
		if _, err := db.Exec(`UPDATE releases SET status='failed',workspace_state='failed' WHERE id='tree-github-legacy'`); err != nil {
			t.Fatalf("legacy digest-less GitHub release could not be terminalized: %v", err)
		}
	})
}

func seedReleaseWorkspaceTreeApplication(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`, id, id, id); err != nil {
		t.Fatal(err)
	}
}

func insertReleaseWorkspaceTreeHistory(t *testing.T, db *sql.DB, id, app, provider string, repositoryID int64, resolved, archive string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,archive_sha256,workspace_state)
		VALUES(?,?, 'ready','{}',datetime('now'),?,?,?,?,?,'ready')`, id, app, provider, repositoryID, resolved, "compose.yaml", archive); err != nil {
		t.Fatal(err)
	}
}
