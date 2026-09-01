package releasesnapshot

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/sourceinspection"
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

func TestMaterializeLocalGeneratedPlanAllowsCodeChangesAndPausesOnStructuralDrift(t *testing.T) {
	materializer, db, dataRoot, appID, actorID, source := localMaterializerFixture(t, false)
	if err := os.Remove(filepath.Join(source, "deploy", "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		filepath.Join(source, "package.json"):      `{"name":"demo","scripts":{"build":"npm run compile","start":"node server.js"},"dependencies":{"express":"1.0.0"}}`,
		filepath.Join(source, "package-lock.json"): `{"lockfileVersion":3,"packages":{}}`,
		filepath.Join(source, "server.js"):         "console.log('one')",
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inspection, err := sourceinspection.InspectLocalContext(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	revision := acceptLocalAnalysisPlan(t, db, dataRoot, appID, actorID, inspection.Analysis, nil)
	revisionID := revision.ID

	first, err := materializer.MaterializeLocal(context.Background(), appID, source)
	if err != nil || first.ComposePath != "" || first.DeploymentPlanRevisionID != revisionID {
		t.Fatalf("generated release=%#v err=%v", first, err)
	}
	if ready, err := materializer.ReadyWorkspace(context.Background(), appID, first.ID); err != nil || ready.ID != first.ID {
		t.Fatalf("generated ready workspace=%#v err=%v", ready, err)
	}

	if err := os.WriteFile(filepath.Join(source, "server.js"), []byte("console.log('ordinary code change')"), 0o600); err != nil {
		t.Fatal(err)
	}
	changedCode, err := materializer.MaterializeLocal(context.Background(), appID, source)
	if err != nil || changedCode.ID == first.ID || changedCode.DeploymentPlanRevisionID != revisionID {
		t.Fatalf("ordinary code release=%#v first=%#v err=%v", changedCode, first, err)
	}

	if err := os.WriteFile(filepath.Join(source, "package.json"), []byte(`{"name":"demo","description":"metadata changed","scripts":{"build":"npm run compile","start":"node server.js"},"dependencies":{"express":"1.0.0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	compatibleMetadata, err := materializer.MaterializeLocal(context.Background(), appID, source)
	if err != nil || compatibleMetadata.ID == changedCode.ID || compatibleMetadata.DeploymentPlanRevisionID != revisionID {
		t.Fatalf("compatible metadata release=%#v prior=%#v err=%v", compatibleMetadata, changedCode, err)
	}

	if err := os.WriteFile(filepath.Join(source, "package.json"), []byte(`{"name":"demo","scripts":{"build":"npm run compile"},"dependencies":{"express":"1.0.0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	driftInspection, err := sourceinspection.InspectLocalContext(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	differences, err := deploymentplans.CompareAnalysis(revision.Plan, driftInspection.Analysis)
	if err != nil || len(differences) == 0 {
		t.Fatalf("changed start command differences=%#v err=%v", differences, err)
	}
	if _, err := materializer.MaterializeLocal(context.Background(), appID, source); !IsCode(err, "deployment_plan_review_required") {
		t.Fatalf("structural drift error=%v", err)
	}
	var state, code string
	if err := db.QueryRow(`SELECT workspace_state,materialization_error_code FROM releases ORDER BY created_at DESC LIMIT 1`).Scan(&state, &code); err != nil || state != WorkspaceStateFailed || code != "deployment_plan_review_required" {
		t.Fatalf("drift release state=%q code=%q err=%v", state, code, err)
	}
}

func acceptLocalAnalysisPlan(t *testing.T, db *sql.DB, dataRoot, appID, actorID string, analysis projectanalysis.SourceAnalysis, mutate func(*deploymentplans.Plan)) deploymentplans.DeploymentPlanRevision {
	t.Helper()
	var candidate projectanalysis.DeploymentPlanCandidate
	for _, value := range analysis.Candidates {
		if value.Kind == projectanalysis.PlanKindJavaScript && len(value.Components) == 1 {
			candidate = value
			break
		}
	}
	if candidate.Kind == "" {
		t.Fatalf("analysis topology = %#v", analysis.Candidates)
	}
	inferred := candidate.Components[0]
	root := inferred.RootDirectory
	if root == "" {
		root = "."
	}
	port := uint64(3000)
	if inferred.InternalPort != nil {
		var err error
		port, err = strconv.ParseUint(inferred.InternalPort.Value, 10, 16)
		if err != nil {
			t.Fatal(err)
		}
	}
	healthProbe := "/health"
	if inferred.HealthProbe != nil {
		healthProbe = inferred.HealthProbe.Path
	}
	component := deploymentplans.Component{
		Name: inferred.ID, Role: inferred.Kind, RootDirectory: root,
		PackageManager: candidate.PackageManager.Name, InstallBehavior: candidate.Install.Command,
		NodeVersion: candidate.NodeVersion.Value, RunCommand: inferred.Run.Command,
		InternalPort: uint16(port), HealthProbe: healthProbe,
	}
	if inferred.Build != nil {
		component.BuildCommand = inferred.Build.Command
	}
	plan := deploymentplans.Plan{
		Strategy:   deploymentplans.StrategyGeneratedNode,
		Detector:   deploymentplans.Detector{Name: "projectanalysis", Version: analysis.SchemaVersion, SourceStructuralFingerprint: analysis.StructuralFingerprint},
		Source:     deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: analysis.StructuralFingerprint},
		Components: []deploymentplans.Component{component},
	}
	if inferred.Migration != nil {
		plan.Migration = &deploymentplans.Migration{
			ComponentName:   component.Name,
			RootDirectory:   component.RootDirectory,
			Command:         inferred.Migration.Command,
			EnvironmentKeys: append([]string(nil), inferred.Migration.EnvironmentKeys...),
			EvidenceDigest:  inferred.MigrationFingerprint,
			Approval:        deploymentplans.MigrationApproval{Status: deploymentplans.MigrationApprovalPending},
		}
	}
	fields := []string{"role", "rootDirectory", "packageManager", "installBehavior", "nodeVersion", "runCommand", "internalPort", "healthProbe"}
	if component.BuildCommand != "" {
		fields = append(fields, "buildCommand")
	}
	for _, field := range fields {
		origin := deploymentplans.ProvenanceInferred
		confidence := 90
		evidence := []string{"package.json"}
		if (field == "internalPort" && inferred.InternalPort == nil) || (field == "healthProbe" && inferred.HealthProbe == nil) {
			origin, confidence, evidence = deploymentplans.ProvenanceUser, 100, []string{"user:override"}
		}
		plan.FieldProvenance = append(plan.FieldProvenance, deploymentplans.FieldProvenance{
			Field: "components." + component.Name + "." + field, Origin: origin,
			Confidence: confidence, Evidence: evidence,
		})
	}
	if mutate != nil {
		mutate(&plan)
	}
	store, err := deploymentplans.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := store.Replace(context.Background(), appID, actorID, deploymentplans.ReplaceInput{Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestMaterializeLocalGeneratedPlanPreservesManualCommandOverride(t *testing.T) {
	materializer, db, dataRoot, appID, actorID, source := localMaterializerFixture(t, false)
	if err := os.Remove(filepath.Join(source, "deploy", "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		filepath.Join(source, "package.json"):      `{"name":"demo","scripts":{"start":"node server.js"},"dependencies":{"express":"1.0.0"}}`,
		filepath.Join(source, "package-lock.json"): `{"lockfileVersion":3,"packages":{}}`,
		filepath.Join(source, "server.js"):         "console.log('ready')",
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inspection, err := sourceinspection.InspectLocalContext(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	revision := acceptLocalAnalysisPlan(t, db, dataRoot, appID, actorID, inspection.Analysis, func(plan *deploymentplans.Plan) {
		componentName := plan.Components[0].Name
		plan.Components[0].RunCommand = "node custom-server.js && echo ready"
		for index := range plan.FieldProvenance {
			if plan.FieldProvenance[index].Field == "components."+componentName+".runCommand" {
				plan.FieldProvenance[index].Origin = deploymentplans.ProvenanceUser
				plan.FieldProvenance[index].Confidence = 100
				plan.FieldProvenance[index].Evidence = []string{"user:override"}
			}
		}
	})

	release, err := materializer.MaterializeLocal(context.Background(), appID, source)
	if err != nil || release.DeploymentPlanRevisionID != revision.ID {
		t.Fatalf("manual override release=%#v err=%v", release, err)
	}
}

func TestMaterializeLocalGeneratedPlanPausesOnMigrationEvidenceChange(t *testing.T) {
	materializer, db, dataRoot, appID, actorID, source := localMaterializerFixture(t, false)
	if err := os.Remove(filepath.Join(source, "deploy", "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		filepath.Join(source, "package.json"):                  `{"name":"demo","scripts":{"start":"node server.js"},"dependencies":{"express":"5","prisma":"6"}}`,
		filepath.Join(source, "package-lock.json"):             `{"lockfileVersion":3,"packages":{}}`,
		filepath.Join(source, "server.js"):                     "console.log('ready')",
		filepath.Join(source, "prisma", "schema.prisma"):       `datasource db { provider = "postgresql" url = env("DATABASE_URL") }`,
		filepath.Join(source, "prisma", "migrations", "1.sql"): "SELECT 1;",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inspection, err := sourceinspection.InspectLocalContext(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	revision := acceptLocalAnalysisPlan(t, db, dataRoot, appID, actorID, inspection.Analysis, nil)
	if revision.Plan.Migration == nil {
		t.Fatal("expected inferred migration")
	}
	if _, err := materializer.MaterializeLocal(context.Background(), appID, source); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "prisma", "migrations", "1.sql"), []byte("SELECT 2;"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := materializer.MaterializeLocal(context.Background(), appID, source); !IsCode(err, "deployment_plan_review_required") {
		t.Fatalf("migration drift error=%v", err)
	}
}

func TestReadyWorkspaceRejectsGeneratedSnapshotTamper(t *testing.T) {
	materializer, db, dataRoot, appID, actorID, source := localMaterializerFixture(t, false)
	if err := os.Remove(filepath.Join(source, "deploy", "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		filepath.Join(source, "package.json"):      `{"name":"demo","scripts":{"start":"node server.js"},"dependencies":{"express":"1.0.0"}}`,
		filepath.Join(source, "package-lock.json"): `{"lockfileVersion":3,"packages":{}}`,
		filepath.Join(source, "server.js"):         "console.log('ready')",
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inspection, err := sourceinspection.InspectLocalContext(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	acceptLocalAnalysisPlan(t, db, dataRoot, appID, actorID, inspection.Analysis, nil)
	release, err := materializer.MaterializeLocal(context.Background(), appID, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release.WorkspacePath, "server.js"), []byte("console.log('tampered')"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := materializer.ReadyWorkspace(context.Background(), appID, release.ID); !IsCode(err, "invalid_source") {
		t.Fatalf("tampered generated workspace error=%v", err)
	}
	var state, code string
	if err := db.QueryRow(`SELECT workspace_state,materialization_error_code FROM releases WHERE id=?`, release.ID).Scan(&state, &code); err != nil || state != WorkspaceStateFailed || code != "invalid_source" {
		t.Fatalf("state=%q code=%q err=%v", state, code, err)
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

func TestReadyLocalReleaseCancellationPreservesWorkspace(t *testing.T) {
	materializer, db, _, appID, _, source := localMaterializerFixture(t, false)
	release, err := materializer.MaterializeLocal(context.Background(), appID, source)
	if err != nil {
		t.Fatal(err)
	}
	assertUnchanged := func(phase string) {
		t.Helper()
		var state string
		if err := db.QueryRow(`SELECT workspace_state FROM releases WHERE id=?`, release.ID).Scan(&state); err != nil || state != WorkspaceStateReady {
			t.Fatalf("%s state=%q err=%v", phase, state, err)
		}
		if _, err := os.Stat(release.WorkspacePath); err != nil {
			t.Fatalf("%s removed workspace: %v", phase, err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := materializer.ReadyRelease(canceled, appID, release.ID); !IsCode(err, "canceled") {
		t.Fatalf("pre-canceled ready lookup error=%v", err)
	}
	assertUnchanged("pre-canceled lookup")

	canonicalHash := materializer.hashTree
	hashCalls := 0
	materializer.hashTree = func(context.Context, string) (string, error) {
		hashCalls++
		return "", context.Canceled
	}
	if _, err := materializer.ReadyRelease(context.Background(), appID, release.ID); !IsCode(err, "canceled") || hashCalls != 1 {
		t.Fatalf("hash-canceled ready lookup error=%v calls=%d", err, hashCalls)
	}
	assertUnchanged("hash-canceled lookup")

	boundary, cancelBoundary := context.WithCancel(context.Background())
	materializer.hashTree = func(ctx context.Context, root string) (string, error) {
		digest, err := canonicalHash(ctx, root)
		cancelBoundary()
		return digest, err
	}
	if _, err := materializer.ReadyRelease(boundary, appID, release.ID); !IsCode(err, "canceled") {
		t.Fatalf("post-hash canceled ready lookup error=%v", err)
	}
	assertUnchanged("post-hash canceled lookup")
	materializer.hashTree = canonicalHash

	if ready, err := materializer.ReadyRelease(context.Background(), appID, release.ID); err != nil || ready.ID != release.ID {
		t.Fatalf("uncanceled ready lookup=%+v err=%v", ready, err)
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
	if release.ID == "" || release.SourceProvider != "local" || release.RepositoryID != 0 || len(release.ResolvedSHA) != 64 || release.ResolvedSHA != release.ArchiveSHA256 || release.ResolvedSHA != release.WorkspaceTreeSHA256 || release.WorkspaceState != WorkspaceStateReady || !strings.HasPrefix(release.WorkspacePath, dataRoot) || strings.HasPrefix(release.WorkspacePath, source) {
		t.Fatalf("release=%#v", release)
	}
	var provider, resolved, archive, tree, workspace string
	if err := db.QueryRow(`SELECT source_provider,resolved_sha,archive_sha256,workspace_tree_sha256,workspace_path FROM releases WHERE id=?`, release.ID).Scan(&provider, &resolved, &archive, &tree, &workspace); err != nil {
		t.Fatal(err)
	}
	if provider != "local" || resolved != release.ResolvedSHA || archive != release.ArchiveSHA256 || tree != release.WorkspaceTreeSHA256 || filepath.IsAbs(workspace) || strings.Contains(provider+resolved+archive+tree+workspace, source) {
		t.Fatalf("stored provider=%q resolved=%q archive=%q tree=%q workspace=%q", provider, resolved, archive, tree, workspace)
	}
}
