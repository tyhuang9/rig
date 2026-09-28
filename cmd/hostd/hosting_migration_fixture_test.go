package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/projectanalysis"
)

// TestHostingMigrationFixtureAnalyzerContract keeps the live Docker journey's
// migration precondition locally executable. It verifies the committed fixture
// remains an unambiguous Node/Knex source and exposes only DATABASE_URL to the
// reviewed migration plan.
func TestHostingMigrationFixtureAnalyzerContract(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "examples", "hosting-migration"))
	if err != nil {
		t.Fatal(err)
	}
	// Knex 3.1's default CLI discovery includes knexfile.js but excludes
	// knexfile.cjs. Keep the inferred command executable without a --knexfile flag.
	if info, err := os.Stat(filepath.Join(root, "knexfile.js")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("Knex CLI-discoverable configuration is required: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "knexfile.cjs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ambiguous undiscoverable Knex configuration remains: %v", err)
	}
	files, err := migrationFixtureFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := projectanalysis.Analyze(context.Background(), files, migrationFixtureReader{root: root})
	if err != nil {
		t.Fatalf("analyze committed migration fixture: %v", err)
	}
	if len(analysis.Candidates) != 1 {
		t.Fatalf("migration fixture candidates=%d, want 1", len(analysis.Candidates))
	}
	candidate := analysis.Candidates[0]
	if candidate.Status != projectanalysis.StatusNeedsInput || candidate.RootDirectory != "" || candidate.PackageManager.Name != "npm" || candidate.Install == nil || candidate.Install.Command != "npm ci" || len(candidate.Components) != 1 {
		t.Fatalf("migration fixture candidate is no longer a reviewed Node deployment: %#v", candidate)
	}
	component := candidate.Components[0]
	if component.ID != "app" || component.Framework != projectanalysis.FrameworkNode || component.Migration == nil ||
		component.Migration.Command != "npm exec -- knex migrate:latest" || component.Migration.Phase != "migrate" ||
		!slices.Equal(component.Migration.EnvironmentKeys, []string{"DATABASE_URL"}) || component.MigrationFingerprint == "" {
		t.Fatalf("migration fixture inference=%#v", component)
	}
	if !migrationFixtureHasEvidence(component.Migration.Evidence, "knex_migration") || !migrationFixtureHasEvidence(component.Migration.Evidence, "migration_input") {
		t.Fatalf("migration fixture did not retain Knex evidence: %#v", component.Migration.Evidence)
	}
}

type migrationFixtureReader struct{ root string }

func (reader migrationFixtureReader) ReadFile(ctx context.Context, name string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filepath.IsAbs(name) || name == "." || strings.HasPrefix(filepath.ToSlash(filepath.Clean(name)), "../") {
		return nil, errors.New("fixture reader rejected source path")
	}
	body, err := os.ReadFile(filepath.Join(reader.root, filepath.FromSlash(name)))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, projectanalysis.ErrFileTooLarge
	}
	return body, nil
}

func migrationFixtureFiles(root string) ([]projectanalysis.File, error) {
	files := []projectanalysis.File{}
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, projectanalysis.File{Path: filepath.ToSlash(relative), Size: info.Size()})
		return nil
	})
	return files, err
}

func migrationFixtureHasEvidence(evidence []projectanalysis.Evidence, code string) bool {
	return slices.ContainsFunc(evidence, func(value projectanalysis.Evidence) bool { return value.Code == code })
}
