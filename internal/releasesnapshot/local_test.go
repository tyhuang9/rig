package releasesnapshot

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/database"
)

func TestMaterializeLocalRetainsBoundedSnapshotAndReusesByTreeAndRevision(t *testing.T) {
	materializer, db, dataRoot, appID, actorID, source := localMaterializerFixture(t, false)

	first, err := materializer.MaterializeLocal(context.Background(), appID, source)
	if err != nil {
		t.Fatal(err)
	}
	assertReadyLocalRelease(t, db, dataRoot, source, first)
	if err := materializer.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.WorkspacePath); err != nil {
		t.Fatalf("ready local workspace pruned by recovery: %v", err)
	}

	reused, err := materializer.MaterializeLocal(context.Background(), appID, source)
	if err != nil || reused.ID != first.ID {
		t.Fatalf("same tree/revision release=%#v err=%v", reused, err)
	}
	if err := os.WriteFile(filepath.Join(source, "app.env"), []byte("SAFE=changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changedTree, err := materializer.MaterializeLocal(context.Background(), appID, source)
	if err != nil || changedTree.ID == first.ID || changedTree.ResolvedSHA == first.ResolvedSHA {
		t.Fatalf("changed tree release=%#v first=%#v err=%v", changedTree, first, err)
	}

	configuration, err := appconfig.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configuration.Replace(context.Background(), appID, actorID, appconfig.ReplaceInput{ExpectedRevisionNumber: 0, Variables: []appconfig.ValueInput{{Key: "PORT", Value: "8080"}}}); err != nil {
		t.Fatal(err)
	}
	changedRevision, err := materializer.MaterializeLocal(context.Background(), appID, source)
	if err != nil || changedRevision.ID == changedTree.ID || changedRevision.ResolvedSHA != changedTree.ResolvedSHA || changedRevision.ConfigurationRevisionNumber != 1 {
		t.Fatalf("changed revision release=%#v prior=%#v err=%v", changedRevision, changedTree, err)
	}
}

func TestMaterializeLocalSupportsLegacyDirectAndNestedCompose(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "nested", true: "direct-file"}[direct], func(t *testing.T) {
			materializer, _, _, appID, _, source := localMaterializerFixture(t, direct)
			release, err := materializer.MaterializeLocal(context.Background(), appID, source)
			if err != nil {
				t.Fatal(err)
			}
			wantCompose := "deploy/compose.yaml"
			if direct {
				wantCompose = "compose.yaml"
			}
			if release.ComposePath != wantCompose || release.SourceProvider != "local" {
				t.Fatalf("release=%#v", release)
			}
		})
	}
}

func TestMaterializeLocalRejectsMutationAndSymlinkSwapWithoutInstallingWorkspace(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "content-mutation", true: "symlink-swap"}[symlink], func(t *testing.T) {
			materializer, db, _, appID, _, source := localMaterializerFixture(t, false)
			target := filepath.Join(source, "app.env")
			outside := filepath.Join(t.TempDir(), "outside.env")
			if err := os.WriteFile(outside, []byte("OUTSIDE=secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			materializer.afterLocalCopy = func() {
				if symlink {
					if err := os.Remove(target); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, target); err != nil {
						t.Skipf("symlink unavailable: %v", err)
					}
					return
				}
				if err := os.WriteFile(target, []byte("SAFE=mutated\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := materializer.MaterializeLocal(context.Background(), appID, source); !IsCode(err, "invalid_source") {
				t.Fatalf("materialization error=%v", err)
			}
			var id, state string
			if err := db.QueryRow(`SELECT id,workspace_state FROM releases WHERE source_provider='local' ORDER BY created_at DESC LIMIT 1`).Scan(&id, &state); err != nil || state != WorkspaceStateFailed {
				t.Fatalf("row id=%q state=%q err=%v", id, state, err)
			}
			if workspace, err := materializer.workspacePath(appID, id); err != nil {
				t.Fatal(err)
			} else if _, err := os.Stat(filepath.Dir(workspace)); !os.IsNotExist(err) {
				t.Fatalf("failed workspace retained: %v", err)
			}
			if staging, err := materializer.stagingPath(appID, id); err != nil {
				t.Fatal(err)
			} else if _, err := os.Stat(staging); !os.IsNotExist(err) {
				t.Fatalf("failed staging retained: %v", err)
			}
		})
	}
}

func TestReadyLocalReleaseRejectsRetainedWorkspaceTamper(t *testing.T) {
	materializer, db, _, appID, _, source := localMaterializerFixture(t, false)
	release, err := materializer.MaterializeLocal(context.Background(), appID, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release.WorkspacePath, "app.env"), []byte("TAMPERED=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := materializer.ReadyRelease(context.Background(), appID, release.ID); !IsCode(err, "invalid_source") {
		t.Fatalf("tampered release error=%v", err)
	}
	var state, code string
	if err := db.QueryRow(`SELECT workspace_state,materialization_error_code FROM releases WHERE id=?`, release.ID).Scan(&state, &code); err != nil || state != WorkspaceStateFailed || code != "invalid_source" {
		t.Fatalf("state=%q code=%q err=%v", state, code, err)
	}
}

func localMaterializerFixture(t *testing.T, direct bool) (*Materializer, *sql.DB, string, string, string, string) {
	t.Helper()
	dataRoot := t.TempDir()
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	appID, actorID := uuid.NewString(), uuid.NewString()
	sourceRoot := t.TempDir()
	composeDirectory := filepath.Join(sourceRoot, "deploy")
	composePath := "deploy/compose.yaml"
	storedSource := sourceRoot
	if direct {
		composeDirectory = sourceRoot
		composePath = "compose.yaml"
		storedSource = filepath.Join(sourceRoot, composePath)
	}
	referencePrefix := "../"
	buildContext := ".."
	if direct {
		referencePrefix = ""
		buildContext = "."
	}
	if err := os.MkdirAll(filepath.Join(sourceRoot, "deploy"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(composeDirectory, "compose.yaml"):  "services:\n  web:\n    build:\n      context: " + buildContext + "\n      dockerfile: Dockerfile\n    env_file: " + referencePrefix + "app.env\n    volumes:\n      - " + referencePrefix + "data:/data:ro\nconfigs:\n  cfg:\n    file: " + referencePrefix + "config.txt\nsecrets:\n  sec:\n    file: " + referencePrefix + "secret.txt\n",
		filepath.Join(sourceRoot, "Dockerfile"):          "FROM scratch\n",
		filepath.Join(sourceRoot, "app.env"):             "SAFE=value\n",
		filepath.Join(sourceRoot, "config.txt"):          "configuration\n",
		filepath.Join(sourceRoot, "secret.txt"):          "container-secret\n",
		filepath.Join(sourceRoot, "data", "content.txt"): "data\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,'owner','hash',?,?)`, actorID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,source_path,status,created_at,updated_at) VALUES(?,?,?,?,'draft',?,?)`, appID, appID, "Local App", storedSource, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO application_sources(application_id,source_type,created_at,updated_at) VALUES(?,'local',?,?)`, appID, now, now); err != nil {
		t.Fatal(err)
	}
	materializer, err := New(db, &fakeSources{}, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	return materializer, db, dataRoot, appID, actorID, storedSource
}

func assertReadyLocalRelease(t *testing.T, db *sql.DB, dataRoot, source string, release Release) {
	t.Helper()
	if release.ID == "" || release.SourceProvider != "local" || release.RepositoryID != 0 || len(release.ResolvedSHA) != 64 || release.ResolvedSHA != release.ArchiveSHA256 || release.WorkspaceState != WorkspaceStateReady || !strings.HasPrefix(release.WorkspacePath, dataRoot) || strings.HasPrefix(release.WorkspacePath, source) {
		t.Fatalf("release=%#v", release)
	}
	var provider, resolved, archive, workspace string
	if err := db.QueryRow(`SELECT source_provider,resolved_sha,archive_sha256,workspace_path FROM releases WHERE id=?`, release.ID).Scan(&provider, &resolved, &archive, &workspace); err != nil {
		t.Fatal(err)
	}
	if provider != "local" || resolved != release.ResolvedSHA || archive != release.ArchiveSHA256 || filepath.IsAbs(workspace) || strings.Contains(provider+resolved+archive+workspace, source) {
		t.Fatalf("stored provider=%q resolved=%q archive=%q workspace=%q", provider, resolved, archive, workspace)
	}
}
