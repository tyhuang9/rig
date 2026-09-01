package releasesnapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestGeneratedWorkspaceValidationPreservesCancellationAndDatabaseErrors(t *testing.T) {
	workspace := t.TempDir()
	for name, body := range map[string]string{
		"package.json":      `{"name":"demo","scripts":{"start":"node server.js"},"dependencies":{"express":"1.0.0"}}`,
		"package-lock.json": `{"lockfileVersion":3,"packages":{}}`,
		"server.js":         "console.log('ready')",
	} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	release := Release{
		ID: uuid.NewString(), AppID: "11111111-1111-1111-1111-111111111111", SourceProvider: "github", RepositoryID: 7,
		DeploymentPlanRevisionID: uuid.NewString(), DeploymentPlanRevisionNumber: 1,
	}

	db := snapshotDB(t)
	materializer, err := New(db, &fakeSources{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := materializer.validateMaterializedWorkspace(canceled, release, workspace); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was reclassified as plan drift: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := materializer.validateMaterializedWorkspace(context.Background(), release, workspace); !errors.Is(err, errLocal) {
		t.Fatalf("database failure was reclassified as plan drift: %v", err)
	}
}
