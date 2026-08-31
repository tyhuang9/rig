package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const generatedImageArtifactsMigration = "019_generated_image_artifacts.sql"

func TestGeneratedImageArtifactsMigrationMirrorAndSafeColumns(t *testing.T) {
	embedded, err := migrations.ReadFile("migrations/" + generatedImageArtifactsMigration)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", generatedImageArtifactsMigration))
	if err != nil {
		t.Fatal(err)
	}
	if string(embedded) != string(public) {
		t.Fatal("embedded migration differs from public migration")
	}

	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('generated_image_artifacts') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(name)
		if strings.Contains(lower, "command") || strings.Contains(lower, "output") || strings.Contains(lower, "log") || strings.Contains(lower, "detail") || strings.Contains(lower, "summary") {
			t.Fatalf("unsafe generated-image metadata column %q", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var tableDefinition, transitionDefinition string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='generated_image_artifacts'`).Scan(&tableDefinition); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='generated_image_artifact_transition'`).Scan(&transitionDefinition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tableDefinition, "'unavailable'") || !strings.Contains(tableDefinition, "'image_unavailable'") || !strings.Contains(transitionDefinition, "OLD.state = 'ready' AND NEW.state = 'unavailable'") {
		t.Fatal("migration does not retain missing ready images as unavailable artifacts")
	}
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, generatedImageArtifactsMigration).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration record=%d err=%v", applied, err)
	}
}
