package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func TestGatewayRebindRuntimeHeadsIncludesCompleteSortedLANAndLoopbackHeads(t *testing.T) {
	fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
	ctx := context.Background()
	lanHead, err := generatedruntimestate.New(fixture.db).Active(ctx, fixture.entry.AppID)
	if err != nil {
		t.Fatal(err)
	}
	want := []GatewayRebindRuntimeHead{runtimeHeadForRebindTest(lanHead)}
	apps := addApps(t, fixture.db, 2)
	sort.Sort(sort.Reverse(sort.StringSlice(apps)))
	for _, appID := range apps {
		want = append(want, runtimeHeadForRebindTest(seedGatewayRebindActiveRuntime(t, fixture.db, appID)))
		var grants int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM lan_app_access_grant_claims WHERE app_id=?`, appID).Scan(&grants); err != nil {
			t.Fatal(err)
		}
		if grants != 0 {
			t.Fatalf("loopback fixture unexpectedly has %d LAN grants", grants)
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i].AppID < want[j].AppID })

	got, err := fixture.repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("complete sorted heads = %#v, want %#v, error=%v", got, want, err)
	}
}

func TestGatewayRebindRuntimeHeadsOmitsInactiveAndArchivedApplications(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	ctx := context.Background()
	if got, err := repository.GatewayRebindRuntimeHeads(ctx); err != nil || len(got) != 0 {
		t.Fatalf("fresh database heads = %#v, error=%v", got, err)
	}
	apps := addApps(t, db, 3)
	want := runtimeHeadForRebindTest(seedGatewayRebindActiveRuntime(t, db, apps[0]))
	archivedHead := seedGatewayRebindActiveRuntime(t, db, apps[1])
	if _, err := db.Exec(`UPDATE applications SET archived_at=? WHERE id=?`, formatTime(testNow), apps[1]); err != nil {
		t.Fatal(err)
	}
	// The third application's schema-created generation-zero row is a legitimate
	// inactive head, with no deployment, release, slot, or update timestamp.
	inactive, err := generatedruntimestate.New(db).Active(ctx, apps[2])
	if err != nil || inactive.Generation != 0 || inactive.DeploymentID != "" || inactive.ReleaseID != "" || inactive.Slot != "" || !inactive.UpdatedAt.IsZero() {
		t.Fatalf("inactive fixture head = %#v, error=%v", inactive, err)
	}

	got, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || !reflect.DeepEqual(got, []GatewayRebindRuntimeHead{want}) {
		t.Fatalf("serving heads = %#v, want %#v, error=%v", got, want, err)
	}
	retained, err := generatedruntimestate.New(db).Active(ctx, apps[1])
	if err != nil || retained != archivedHead {
		t.Fatalf("archived head was changed: %#v, error=%v", retained, err)
	}
}

func TestGatewayRebindRuntimeHeadsReadsAreIndependentAndDoNotMutateState(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	ctx := context.Background()
	apps := addApps(t, db, 2)
	sort.Strings(apps)
	firstHead := seedGatewayRebindActiveRuntime(t, db, apps[0])
	before := runtimeHeadReadChanges(t, db)
	first, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || !reflect.DeepEqual(first, []GatewayRebindRuntimeHead{runtimeHeadForRebindTest(firstHead)}) {
		t.Fatalf("first snapshot = %#v, error=%v", first, err)
	}
	if after := runtimeHeadReadChanges(t, db); after != before {
		t.Fatalf("read changed SQLite rows: before=%d after=%d", before, after)
	}

	secondHead := seedGatewayRebindActiveRuntime(t, db, apps[1])
	want := []GatewayRebindRuntimeHead{runtimeHeadForRebindTest(firstHead), runtimeHeadForRebindTest(secondHead)}
	before = runtimeHeadReadChanges(t, db)
	second, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || !reflect.DeepEqual(second, want) {
		t.Fatalf("newly activated head missing from fresh snapshot: %#v, error=%v", second, err)
	}
	if !reflect.DeepEqual(first, want[:1]) {
		t.Fatalf("previous snapshot changed after activation: %#v", first)
	}
	second[0].Generation++
	second[0].DeploymentID = uuid.NewString()
	third, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || !reflect.DeepEqual(third, want) {
		t.Fatalf("caller mutation affected subsequent read: %#v, error=%v", third, err)
	}
	if after := runtimeHeadReadChanges(t, db); after != before {
		t.Fatalf("snapshot reads changed SQLite rows: before=%d after=%d", before, after)
	}

	replacement := redeployRuntimeHeadForRebindTest(t, db, firstHead)
	want[0] = runtimeHeadForRebindTest(replacement)
	before = runtimeHeadReadChanges(t, db)
	fourth, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || !reflect.DeepEqual(fourth, want) {
		t.Fatalf("replacement deployment missing from fresh snapshot: %#v, error=%v", fourth, err)
	}
	if !reflect.DeepEqual(first, []GatewayRebindRuntimeHead{runtimeHeadForRebindTest(firstHead)}) ||
		third[0] != runtimeHeadForRebindTest(firstHead) {
		t.Fatalf("replacement changed prior snapshots: first=%#v third=%#v", first, third)
	}
	if after := runtimeHeadReadChanges(t, db); after != before {
		t.Fatalf("replacement read changed SQLite rows: before=%d after=%d", before, after)
	}
}

func TestGatewayRebindRuntimeHeadsRejectsInvalidInputAndCanceledContext(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	for _, test := range []struct {
		name       string
		repository *Repository
		ctx        context.Context
	}{
		{name: "nil repository", ctx: context.Background()},
		{name: "nil database", repository: New(nil), ctx: context.Background()},
		{name: "nil context", repository: repository},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.repository.GatewayRebindRuntimeHeads(test.ctx)
			if !errors.Is(err, ErrInvalidInput) || got != nil {
				t.Fatalf("invalid read = %#v, error=%v", got, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := repository.GatewayRebindRuntimeHeads(ctx); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("canceled read = %#v, error=%v", got, err)
	}
	if got, err := repository.GatewayRebindRuntimeHeads(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("read after cancellation = %#v, error=%v", got, err)
	}
}

func TestGatewayRebindRuntimeHeadsRejectsCorruptStoredHeadsWithoutPartialResults(t *testing.T) {
	for _, test := range []struct {
		name    string
		queries []string
	}{
		{name: "application identity", queries: []string{
			`UPDATE applications SET id='invalid-app' WHERE id=?`,
			`UPDATE generated_runtime_active_heads SET app_id='invalid-app' WHERE app_id=?`,
		}},
		{name: "deployment identity", queries: []string{`UPDATE generated_runtime_active_heads SET deployment_id='invalid-deployment' WHERE app_id=?`}},
		{name: "release identity", queries: []string{`UPDATE generated_runtime_active_heads SET release_id='invalid-release' WHERE app_id=?`}},
		{name: "slot", queries: []string{`UPDATE generated_runtime_active_heads SET slot='red' WHERE app_id=?`}},
		{name: "malformed timestamp", queries: []string{`UPDATE generated_runtime_active_heads SET updated_at='not-a-time' WHERE app_id=?`}},
		{name: "zero timestamp", queries: []string{`UPDATE generated_runtime_active_heads SET updated_at='0001-01-01T00:00:00Z' WHERE app_id=?`}},
		{name: "negative generation", queries: []string{`UPDATE generated_runtime_active_heads SET generation=-1 WHERE app_id=?`}},
		{name: "inactive generation retains serving identity", queries: []string{`UPDATE generated_runtime_active_heads SET generation=0 WHERE app_id=?`}},
		{name: "missing application", queries: []string{`UPDATE generated_runtime_active_heads SET app_id='ffffffff-ffff-4fff-8fff-ffffffffffff' WHERE app_id=?`}},
		{name: "missing deployment", queries: []string{`UPDATE generated_runtime_active_heads SET deployment_id='ffffffff-ffff-4fff-8fff-ffffffffffff' WHERE app_id=?`}},
		{name: "missing release", queries: []string{`UPDATE generated_runtime_active_heads SET release_id='ffffffff-ffff-4fff-8fff-ffffffffffff' WHERE app_id=?`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := appAccessDB(t)
			apps := addApps(t, db, 2)
			sort.Strings(apps)
			for _, appID := range apps {
				seedGatewayRebindActiveRuntime(t, db, appID)
			}
			// Corruption is confined to this disposable database. Disable write
			// guards only while constructing states that the read must reject.
			for _, statement := range []string{
				`DROP TRIGGER generated_runtime_active_head_valid_update`,
				`PRAGMA foreign_keys=OFF`,
				`PRAGMA ignore_check_constraints=ON`,
			} {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			for _, query := range test.queries {
				result, err := db.Exec(query, apps[1])
				if err != nil {
					t.Fatal(err)
				}
				if changed, err := result.RowsAffected(); err != nil || changed != 1 {
					t.Fatalf("corruption changed %d rows, error=%v", changed, err)
				}
			}
			for _, statement := range []string{`PRAGMA foreign_keys=ON`, `PRAGMA ignore_check_constraints=OFF`} {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}

			got, err := testRepository(db).GatewayRebindRuntimeHeads(context.Background())
			if !errors.Is(err, ErrInvalidStoredState) || got != nil {
				t.Fatalf("corrupt read returned partial or accepted heads: %#v, error=%v", got, err)
			}
		})
	}
}

func TestGatewayRebindRuntimeHeadsRejectsCrossedDeploymentReleaseAndSlot(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *sql.DB, generatedruntimestate.ActiveHead, generatedruntimestate.ActiveHead)
	}{
		{name: "same-app release differs from deployment", mutate: func(t *testing.T, db *sql.DB, _, second generatedruntimestate.ActiveHead) {
			otherReleaseID := uuid.NewString()
			if _, err := db.Exec(`INSERT INTO releases(
				id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,
				workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
			) SELECT ?,app_id,'ready','{}',?,'local',0,?,'ready',workspace_tree_sha256,
				deployment_plan_revision_id,deployment_plan_revision_number FROM releases WHERE id=?`,
				otherReleaseID, formatTime(testNow), strings.Repeat("d", 64), second.ReleaseID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE generated_runtime_active_heads SET release_id=? WHERE app_id=?`,
				otherReleaseID, second.AppID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "slot differs from deployment candidate", mutate: func(t *testing.T, db *sql.DB, _, second generatedruntimestate.ActiveHead) {
			slot := "blue"
			if second.Slot == slot {
				slot = "green"
			}
			if _, err := db.Exec(`UPDATE generated_runtime_active_heads SET slot=? WHERE app_id=?`, slot, second.AppID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := appAccessDB(t)
			apps := addApps(t, db, 2)
			first := seedGatewayRebindActiveRuntime(t, db, apps[0])
			second := seedGatewayRebindActiveRuntime(t, db, apps[1])
			for _, statement := range []string{
				`DROP TRIGGER generated_runtime_active_head_valid_update`,
				`PRAGMA foreign_keys=OFF`,
			} {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			test.mutate(t, db, first, second)
			if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
				t.Fatal(err)
			}
			got, err := testRepository(db).GatewayRebindRuntimeHeads(context.Background())
			if !errors.Is(err, ErrInvalidStoredState) || got != nil {
				t.Fatalf("crossed head accepted: %#v, error=%v", got, err)
			}
		})
	}
}

func runtimeHeadForRebindTest(head generatedruntimestate.ActiveHead) GatewayRebindRuntimeHead {
	return GatewayRebindRuntimeHead{
		AppID: head.AppID, DeploymentID: head.DeploymentID, ReleaseID: head.ReleaseID,
		Slot: head.Slot, Generation: head.Generation, UpdatedAt: head.UpdatedAt,
	}
}

func runtimeHeadReadChanges(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var count int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func redeployRuntimeHeadForRebindTest(t *testing.T, db *sql.DB, previous generatedruntimestate.ActiveHead) generatedruntimestate.ActiveHead {
	t.Helper()
	ctx := context.Background()
	runtime := generatedruntimestate.New(db)
	oldDeployment, err := runtime.Advance(ctx, previous.AppID, previous.DeploymentID,
		generatedruntimestate.PhaseSwitchingRoute, generatedruntimestate.PhaseDraining, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Advance(ctx, previous.AppID, previous.DeploymentID,
		generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	deploymentID, jobID := uuid.NewString(), uuid.NewString()
	stamp := formatTime(testNow)
	if _, err := db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',updated_at=?,finished_at=?
		WHERE id=(SELECT job_id FROM deployments WHERE id=?)`, stamp, stamp, previous.DeploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,created_at,updated_at)
		VALUES(?,'deploy','application',?,'running','running',?,?,?)`, jobID, previous.AppID, testAdministrator, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployments(
		id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,?,?,'preparing','current',1,'generated_node',?,?)`, deploymentID, previous.AppID,
		previous.ReleaseID, jobID, oldDeployment.DeploymentPlanRevisionID, oldDeployment.DeploymentPlanRevisionNumber); err != nil {
		t.Fatal(err)
	}
	value, _, err := runtime.Begin(ctx, generatedruntimestate.BeginInput{
		DeploymentID: deploymentID, AppID: previous.AppID, ReleaseID: previous.ReleaseID,
		DeploymentPlanRevisionID:     oldDeployment.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: oldDeployment.DeploymentPlanRevisionNumber, ComponentNames: []string{"api"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SetImageReady(ctx, previous.AppID, deploymentID, "api", oldDeployment.Components[0].ImageArtifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SetContainerStarting(ctx, previous.AppID, deploymentID, "api", "rig-rebind-replacement-api"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SetContainerRunning(ctx, previous.AppID, deploymentID, "api", strings.Repeat("4", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AdvanceComponent(ctx, previous.AppID, deploymentID, "api",
		generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
		t.Fatal(err)
	}
	for _, next := range []generatedruntimestate.Phase{
		generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate,
		generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute,
	} {
		value, err = runtime.Advance(ctx, previous.AppID, deploymentID, value.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	head, changed, err := runtime.SwitchActive(ctx, previous.AppID, deploymentID, previous.Generation)
	if err != nil || !changed || head.DeploymentID == previous.DeploymentID || head.Slot == previous.Slot || head.Generation != previous.Generation+1 {
		t.Fatalf("replacement active head=%#v changed=%t error=%v", head, changed, err)
	}
	return head
}
