package appaccess

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func TestHostingGatewayStartupRuntimeCensusUsesOneTransaction(t *testing.T) {
	fixture := newGeneratedDomainRuntimeFixture(t)
	t.Cleanup(fixture.Close)
	db := fixture.Database()
	addUsers(t, db)
	appID := addApps(t, db, 1)[0]
	head, _ := fixture.Activate(t, GeneratedDomainRuntimeSeed{AppID: appID})
	repository := New(fixture.OpenSealedReader(t))
	before, err := repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || len(before.RuntimeHeads) != 1 || len(before.RuntimeComponents) != 1 {
		t.Fatalf("complete initial runtime census: %+v %v", before.RuntimeHeads, err)
	}
	var replacement generatedruntimestate.ActiveHead
	repository.afterGrantStartupClaimsRead = func() {
		repository.afterGrantStartupClaimsRead = nil
		replacement, _ = fixture.Activate(t, GeneratedDomainRuntimeSeed{AppID: head.AppID, ReleaseID: head.ReleaseID})
	}
	during, err := repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || replacement.DeploymentID == "" || !reflect.DeepEqual(during, before) {
		t.Fatalf("concurrent redeploy mixed startup transactions: %v", err)
	}
	after, err := repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || len(after.RuntimeHeads) != 1 || len(after.RuntimeComponents) != 1 ||
		after.RuntimeHeads[0] != runtimeHeadForRebindTest(replacement) ||
		after.RuntimeComponents[0].DeploymentID != replacement.DeploymentID ||
		after.RuntimeComponents[0].ContainerID == before.RuntimeComponents[0].ContainerID ||
		after.RuntimeComponents[0].Slot != replacement.Slot {
		t.Fatalf("fresh snapshot missed replacement component/head: %v", err)
	}
}

func TestHostingGatewayStartupRuntimeCensusIncludesLANAndLoopback(t *testing.T) {
	f := newHistoricalGatewayRebindFixtureOnDBWithClaim(t, historicalAppAccessDB(t), false, false)
	apps := addApps(t, f.db, 3)
	sort.Strings(apps)
	loopback := seedHistoricalGatewayRebindActiveRuntime(t, f.db, apps[0])
	seedHistoricalGatewayRebindActiveRuntime(t, f.db, apps[1])
	if _, err := f.db.Exec(`UPDATE applications SET archived_at=? WHERE id=?`, formatTime(testNow), apps[1]); err != nil {
		t.Fatal(err)
	}
	upgradeHistoricalGatewayDatabase(t, f.db)
	before := runtimeHeadReadChanges(t, f.db)
	snapshot, err := f.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || len(snapshot.RuntimeHeads) != 2 || len(snapshot.RuntimeComponents) != 2 {
		t.Fatalf("LAN and loopback census: heads=%d components=%d error=%v", len(snapshot.RuntimeHeads), len(snapshot.RuntimeComponents), err)
	}
	for i, head := range snapshot.RuntimeHeads {
		component := snapshot.RuntimeComponents[i]
		if component.AppID != head.AppID || component.DeploymentID != head.DeploymentID ||
			component.ReleaseID != head.ReleaseID || component.Slot != head.Slot || component.Name != "api" ||
			component.State != "active" || !validDigest(component.ContainerID) || component.UpdatedAt.IsZero() {
			t.Fatal("component lost exact active head identity")
		}
	}
	if after := runtimeHeadReadChanges(t, f.db); after != before {
		t.Fatal("startup census wrote database rows")
	}
	// A legacy (receipt-free) component failure after upgrade must remain
	// visible to restart authorization. Receipt-owned failure is tested by its owner.
	runtime := generatedruntimestate.New(f.db)
	if _, err := runtime.FailComponent(context.Background(), loopback.AppID, loopback.DeploymentID, "api",
		generatedruntimestate.ComponentActive, generatedruntimestate.DiagnosticDaemonRestarted); err != nil {
		t.Fatal(err)
	}
	after, err := f.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || len(after.RuntimeComponents) != 2 {
		t.Fatalf("non-serving component disappeared: %v", err)
	}
	found := false
	for _, component := range after.RuntimeComponents {
		if component.AppID == loopback.AppID {
			found = component.State == "failed" && component.DiagnosticCode == "daemon_restarted" && !component.FinishedAt.IsZero()
		}
	}
	if !found || reflect.DeepEqual(snapshot.RuntimeComponents, after.RuntimeComponents) {
		t.Fatal("failed runtime component was hidden or earlier snapshot mutated")
	}
}

func TestHostingGatewayStartupRuntimeCensusRefusesCorruptionWithoutPartialResults(t *testing.T) {
	for _, test := range []struct{ name, query string }{
		{"missing component", `DELETE FROM generated_runtime_components WHERE deployment_id=?`},
		{"wrong slot", `UPDATE generated_runtime_components SET slot='green' WHERE deployment_id=?`},
		{"invalid container", `UPDATE generated_runtime_components SET container_id='not-an-id' WHERE deployment_id=?`},
		{"unknown state", `UPDATE generated_runtime_components SET state='unknown' WHERE deployment_id=?`},
		{"missing active container", `UPDATE generated_runtime_components SET container_id=NULL WHERE deployment_id=?`},
		{"invalid timestamp", `UPDATE generated_runtime_components SET updated_at='invalid' WHERE deployment_id=?`},
		{"unexpected terminal timestamp", `UPDATE generated_runtime_components SET finished_at=updated_at WHERE deployment_id=?`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := historicalAppAccessDB(t)
			apps := addApps(t, db, 2)
			sort.Strings(apps)
			seedHistoricalGatewayRebindActiveRuntime(t, db, apps[0])
			broken := seedHistoricalGatewayRebindActiveRuntime(t, db, apps[1])
			upgradeHistoricalGatewayDatabase(t, db)
			// These guards are disabled only in this disposable corruption fixture.
			for _, query := range []string{
				`DROP TRIGGER generated_runtime_component_no_delete`,
				`DROP TRIGGER generated_runtime_component_identity_immutable`,
				`DROP TRIGGER generated_runtime_component_runtime_identity_immutable`,
				`DROP TRIGGER generated_runtime_component_state_transition`,
				`PRAGMA ignore_check_constraints=ON`,
			} {
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(test.query, broken.DeploymentID); err != nil {
				t.Fatal(err)
			}
			got, err := New(db).HostingGatewayStartupSnapshot(context.Background())
			if !errors.Is(err, ErrInvalidStoredState) || !reflect.DeepEqual(got, HostingGatewayStartupSnapshot{}) {
				t.Fatalf("corrupt census returned partial authority: %+v %v", got.RuntimeComponents, err)
			}
		})
	}
}
