package generatedingress

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

type gatewayCurrentV2PromotionRefusingRepository struct{ calls int }

func (r *gatewayCurrentV2PromotionRefusingRepository) HostingGatewayStartupSnapshot(context.Context) (
	appaccess.HostingGatewayStartupSnapshot, error,
) {
	r.calls++
	return appaccess.HostingGatewayStartupSnapshot{}, errors.New("unexpected authority read")
}
func (r *gatewayCurrentV2PromotionRefusingRepository) CheckGatewayRebindFence(context.Context) error {
	r.calls++
	return errors.New("unexpected fence read")
}
func (r *gatewayCurrentV2PromotionRefusingRepository) Head(context.Context, string) (applifecycle.Head, error) {
	r.calls++
	return applifecycle.Head{}, errors.New("unexpected head read")
}
func (r *gatewayCurrentV2PromotionRefusingRepository) RebindExcluded(context.Context) (bool, error) {
	r.calls++
	return true, errors.New("unexpected exclusion read")
}

type gatewayCurrentV2PromotionTestRepository struct {
	snapshot      appaccess.HostingGatewayStartupSnapshot
	heads         map[string]applifecycle.Head
	startupCalls  int
	failStartupAt int
	fenceErr      error
	excluded      bool
}

func (r *gatewayCurrentV2PromotionTestRepository) HostingGatewayStartupSnapshot(context.Context) (
	appaccess.HostingGatewayStartupSnapshot, error,
) {
	r.startupCalls++
	if r.startupCalls == r.failStartupAt {
		return appaccess.HostingGatewayStartupSnapshot{}, errors.New("injected startup census failure")
	}
	return r.snapshot, nil
}

func (r *gatewayCurrentV2PromotionTestRepository) CheckGatewayRebindFence(context.Context) error {
	return r.fenceErr
}

func (r *gatewayCurrentV2PromotionTestRepository) Head(_ context.Context, appID string) (applifecycle.Head, error) {
	head, ok := r.heads[appID]
	if !ok {
		return applifecycle.Head{}, errors.New("missing lifecycle head")
	}
	return head, nil
}

func (r *gatewayCurrentV2PromotionTestRepository) RebindExcluded(context.Context) (bool, error) {
	return r.excluded, nil
}

type gatewayCurrentV2SQLitePromotionRepository struct {
	access    *appaccess.Repository
	lifecycle *applifecycle.Repository
}

func (r gatewayCurrentV2SQLitePromotionRepository) HostingGatewayStartupSnapshot(ctx context.Context) (
	appaccess.HostingGatewayStartupSnapshot, error,
) {
	return r.access.HostingGatewayStartupSnapshot(ctx)
}

func (r gatewayCurrentV2SQLitePromotionRepository) CheckGatewayRebindFence(ctx context.Context) error {
	return r.access.CheckGatewayRebindFence(ctx)
}

func (r gatewayCurrentV2SQLitePromotionRepository) Head(ctx context.Context, appID string) (applifecycle.Head, error) {
	return r.lifecycle.Head(ctx, appID)
}

func (r gatewayCurrentV2SQLitePromotionRepository) RebindExcluded(ctx context.Context) (bool, error) {
	return r.lifecycle.RebindExcluded(ctx)
}

type gatewayCurrentV2SelectionTestRepository struct {
	snapshot appaccess.GatewayRebindRecoverySnapshot
	calls    int
	failAt   int
}

func (r *gatewayCurrentV2SelectionTestRepository) GatewayRebindRecoverySnapshot(context.Context) (
	appaccess.GatewayRebindRecoverySnapshot, error,
) {
	r.calls++
	if r.calls == r.failAt {
		return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("injected current selection failure")
	}
	return r.snapshot, nil
}

type gatewayCurrentV2PromotionFullFixture struct {
	state       gatewayCurrentStateFixture
	selection   *gatewayCurrentV2SelectionTestRepository
	repository  *gatewayCurrentV2PromotionTestRepository
	context     context.Context
	physical    gatewayCurrentV2PhysicalOriginReader
	physicalHit *int
	commands    *gatewayCurrentNoCommandRunner
}

func newGatewayCurrentV2PromotionFullFixture(t *testing.T) gatewayCurrentV2PromotionFullFixture {
	t.Helper()
	fixture := newGatewayCurrentStateFixture(t)
	local := cloneGatewayCurrentRouteState(fixture.baseline)
	for appID, app := range local.Apps {
		app.LAN = nil
		local.Apps[appID] = app
	}
	gatewayCurrentProjectionV2RefreshV1(t, &local)
	fixture.baseline = local
	if err := fixture.store.installBaseline(local); err != nil {
		t.Fatal(err)
	}

	rebind := gatewayCurrentRebindSnapshotForV2Test(fixture)
	selection := &gatewayCurrentV2SelectionTestRepository{snapshot: rebind}
	fixture.manager.options.RebindCurrentStateRepository = selection
	directories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture.manager.options.WorkingDirectory = directories.WorkingDirectory
	commands := &gatewayCurrentNoCommandRunner{}
	fixture.manager.runner = commands

	snapshot := appaccess.HostingGatewayStartupSnapshot{Rebind: rebind}
	snapshot.Upgrades.CurrentProfile = rebind.CurrentProfile
	appIDs := make([]string, 0, len(local.Apps))
	for appID := range local.Apps {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	heads := make(map[string]applifecycle.Head, len(appIDs))
	for index, appID := range appIDs {
		app := local.Apps[appID]
		head := appaccess.GatewayRebindRuntimeHead{
			AppID: appID, DeploymentID: uuid.NewString(), ReleaseID: uuid.NewString(),
			Slot: string(app.Route.Slot), Generation: int64(index + 1),
			UpdatedAt: time.Unix(int64(index+1), 0).UTC(),
		}
		snapshot.RuntimeHeads = append(snapshot.RuntimeHeads, head)
		heads[appID] = applifecycle.Head{ApplicationID: appID}
		for _, endpoint := range app.Route.Endpoints {
			snapshot.RuntimeComponents = append(snapshot.RuntimeComponents, appaccess.GatewayStartupRuntimeComponent{
				AppID: appID, DeploymentID: head.DeploymentID, ReleaseID: head.ReleaseID,
				Name: endpoint.Component, Slot: head.Slot, ImageArtifactID: uuid.NewString(),
				ContainerName: "promotion-" + endpoint.Component, ContainerID: endpoint.ContainerID,
				State: "active", CreatedAt: head.UpdatedAt, UpdatedAt: head.UpdatedAt,
			})
		}
	}
	sort.Slice(snapshot.RuntimeComponents, func(i, j int) bool {
		if snapshot.RuntimeComponents[i].AppID != snapshot.RuntimeComponents[j].AppID {
			return snapshot.RuntimeComponents[i].AppID < snapshot.RuntimeComponents[j].AppID
		}
		return snapshot.RuntimeComponents[i].Name < snapshot.RuntimeComponents[j].Name
	})
	repository := &gatewayCurrentV2PromotionTestRepository{snapshot: snapshot, heads: heads}

	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), fixture.manager.options.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release effects scope: %v", err)
		}
	})
	ctx := deploymenteffects.WithScope(context.Background(), scope)
	physicalHits := 0
	physical := func(readCtx context.Context, source gatewayCurrentSelection) (
		gatewayRebindAdmissionPredecessorProof, error,
	) {
		physicalHits++
		if readCtx.Err() != nil || source.Kind != gatewayCurrentSelectionRebind || source.State == nil ||
			!reflect.DeepEqual(*source.State, local) {
			return gatewayRebindAdmissionPredecessorProof{}, errors.New("unexpected physical source")
		}
		return gatewayRebindAdmissionPredecessorProof{Current: &gatewayRebindAdmissionCurrentProof{
			State: cloneGatewayCurrentRouteState(local),
		}}, nil
	}
	return gatewayCurrentV2PromotionFullFixture{state: fixture, selection: selection,
		repository: repository, context: ctx, physical: physical, physicalHit: &physicalHits, commands: commands}
}

func cloneGatewayCurrentV2PromotionTestRepository(
	value *gatewayCurrentV2PromotionTestRepository,
) *gatewayCurrentV2PromotionTestRepository {
	result := &gatewayCurrentV2PromotionTestRepository{snapshot: value.snapshot,
		heads: make(map[string]applifecycle.Head, len(value.heads))}
	result.snapshot.RuntimeHeads = append([]appaccess.GatewayRebindRuntimeHead(nil), value.snapshot.RuntimeHeads...)
	result.snapshot.RuntimeComponents = append([]appaccess.GatewayStartupRuntimeComponent(nil), value.snapshot.RuntimeComponents...)
	result.snapshot.Grants.Claims = append([]appaccess.AppAccessGrantStartupClaim(nil), value.snapshot.Grants.Claims...)
	result.snapshot.Disables.Claims = append([]appaccess.AppAccessDisableStartupClaim(nil), value.snapshot.Disables.Claims...)
	for appID, head := range value.heads {
		result.heads[appID] = head
	}
	return result
}

func gatewayCurrentV2TestScope(t *testing.T, manager *Manager) context.Context {
	t.Helper()
	directories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager.options.WorkingDirectory = directories.WorkingDirectory
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), manager.options.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release effects scope: %v", err)
		}
	})
	return deploymenteffects.WithScope(context.Background(), scope)
}

func gatewayCurrentV2ReadOnlyDockerCommand(command []string) bool {
	managedFilter := "label=" + gatewayV2ManagedLabelKey
	identityFilter := "label=" + gatewayV2IdentityLabelKey + "=" + gatewayV2IdentityVersion
	if reflect.DeepEqual(command, []string{
		"container", "ls", "--all", "--filter", managedFilter,
		"--filter", identityFilter, "--format", "{{.Names}}",
	}) {
		return true
	}
	for _, resource := range []string{"volume", "network"} {
		if reflect.DeepEqual(command, []string{
			resource, "ls", "--filter", managedFilter,
			"--filter", identityFilter, "--format", "{{.Name}}",
		}) {
			return true
		}
	}
	if len(command) == 5 && command[1] == "inspect" && command[2] == "--format" &&
		command[3] != "" && command[4] != "" {
		switch command[0] {
		case "image", "container", "volume":
			return true
		case "network":
			return command[3] == "json"
		}
	}
	if len(command) == 15 && command[0] == "container" && command[1] == "exec" &&
		validContainerID(command[2]) && reflect.DeepEqual(command[3:], []string{
		"curl", "--disable", "--silent", "--show-error", "--fail", "--proto", "=http", "--noproxy", "*",
		"--max-time", "10", "http://127.0.0.1:2019/config/",
	}) {
		return true
	}
	if len(command) == 4 && command[0] == "container" && command[1] == "cp" && command[3] == "-" {
		container, filename, found := strings.Cut(command[2], ":/config/")
		return found && validContainerID(container) &&
			(filename == gatewayV2ActiveConfigFile || filename == gatewayV2StageConfigFilename)
	}
	return false
}

func gatewayCurrentV2AssertReadOnlyDockerCommands(t *testing.T, commands [][]string) {
	t.Helper()
	for _, command := range commands {
		if !gatewayCurrentV2ReadOnlyDockerCommand(command) {
			t.Fatalf("promotion issued a command outside the read-only allowlist: %v", command)
		}
	}
}

func gatewayCurrentV2PromotionAuthorityForTest(t *testing.T,
	selection gatewayCurrentSelection,
) gatewayCurrentV2PromotionAuthority {
	t.Helper()
	apps, err := gatewayCurrentPromotionSourceApps(selection)
	if err != nil {
		t.Fatal(err)
	}
	value := gatewayCurrentV2PromotionAuthority{}
	for appID, app := range apps {
		value.Snapshot.RuntimeHeads = append(value.Snapshot.RuntimeHeads, appaccess.GatewayRebindRuntimeHead{
			AppID: appID, DeploymentID: "77777777-7777-4777-8777-777777777777",
			ReleaseID: "88888888-8888-4888-8888-888888888888", Slot: string(app.Route.Slot), Generation: 3,
		})
		value.Heads = append(value.Heads, applifecycle.Head{ApplicationID: appID})
	}
	return value
}

func TestPrepareGatewayCurrentV2(t *testing.T) {
	t.Run("rebind_and_upgrade_baselines_preserve_exact_config_and_runtime_pins", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		rebind := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind,
			Lineage: fixture.baseline.Lineage, State: &fixture.baseline}
		rebindAuthority := gatewayCurrentV2PromotionAuthorityForTest(t, rebind)
		rebindBaseline, err := gatewayCurrentV2BaselineFor(rebind, rebindAuthority)
		if err != nil {
			t.Fatal(err)
		}
		got, err := gatewayCurrentPhysicalConfigBytesV2(rebindBaseline)
		want, wantErr := gatewayCurrentPhysicalConfigBytes(fixture.baseline)
		if err != nil || wantErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("rebind baseline changed config: error=%v wantError=%v equal=%v", err, wantErr, bytes.Equal(got, want))
		}
		for appID, app := range rebindBaseline.Apps {
			if app.Traffic != gatewayCurrentTrafficServingV2 ||
				app.Authority != (gatewayCurrentAppliedAuthorityV2{DesiredMode: gatewayCurrentDesiredUninitializedV2}) ||
				app.Runtime.ActiveGeneration != 3 || app.Runtime.Slot != app.Route.Slot {
				t.Fatalf("rebind app %s baseline=%#v", appID, app)
			}
		}

		upgrade := gatewayCurrentSelection{Kind: gatewayCurrentSelectionUpgrade}
		upgrade.Upgrade = &fixture.history.Predecessor
		source := cloneRouteState(fixture.history.Source)
		upgrade.UpgradeSource = &source
		upgrade.Lineage, err = gatewayUpgradeCurrentLineage(*upgrade.Upgrade)
		if err != nil {
			t.Fatal(err)
		}
		upgradeAuthority := gatewayCurrentV2PromotionAuthorityForTest(t, upgrade)
		upgradeBaseline, err := gatewayCurrentV2BaselineFor(upgrade, upgradeAuthority)
		if err != nil {
			t.Fatal(err)
		}
		got, err = gatewayCurrentPhysicalConfigBytesV2(upgradeBaseline)
		want, wantErr = expectedGatewayV2FinalConfig(upgrade.Upgrade.State)
		if err != nil || wantErr != nil || !bytes.Equal(got, want) || upgradeBaseline.SourceV1 != nil ||
			upgradeBaseline.SourceUpgrade == nil {
			t.Fatalf("upgrade baseline changed config or origin: error=%v wantError=%v equal=%v", err, wantErr, bytes.Equal(got, want))
		}
	})

	t.Run("positive_upgrade_history_selects_and_promotes_its_real_generation", func(t *testing.T) {
		manager, runner := newManagerFixture(t, false)
		retired, retiredState, retiredJournal := installRolledBackGeneration(t, manager, 0,
			"55555555-5555-4555-8555-555555555555")
		if _, err := retired.installRollbackRetirementReceipt(retiredState, retiredJournal); err != nil {
			t.Fatal(err)
		}
		operationID := "77777777-7777-4777-8777-777777777777"
		store, err := newGatewayUpgradeGenerationStore(manager.options.DataRoot, 1, operationID)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _ = installCommittedGeneration(t, manager, store, operationID)
		history, err := manager.scanGatewayRebindProtectedIntentHistoryCurrentV2AwareLocked(nil)
		if err != nil || history.Predecessor.Generation != 1 {
			t.Fatalf("select positive upgrade history: generation=%d error=%v", history.Predecessor.Generation, err)
		}
		lineage, err := gatewayUpgradeCurrentLineage(history.Predecessor)
		if err != nil {
			t.Fatal(err)
		}
		profile := gatewayCurrentFixtureProfile(lineage, history.Predecessor.State.Profile)
		sourceAuthority := gatewayCurrentAuthority(lineage)
		sqlSnapshot := appaccess.GatewayRebindRecoverySnapshot{
			CurrentProfile: &profile, CurrentSource: &sourceAuthority,
		}
		selectionRepository := &gatewayCurrentV2SelectionTestRepository{snapshot: sqlSnapshot}
		manager.options.RebindCurrentStateRepository = selectionRepository
		selected, selectedSQL, err := manager.readGatewayCurrentProtectedSelectionLocked(context.Background())
		if err != nil || selected.Legacy == nil || selected.V2 != nil ||
			selected.Legacy.Kind != gatewayCurrentSelectionUpgrade || selected.Legacy.Lineage.ProtectedGeneration != 1 {
			t.Fatalf("positive upgrade legacy selection=%#v error=%v", selected, err)
		}
		source := *selected.Legacy
		hosting := appaccess.HostingGatewayStartupSnapshot{Rebind: selectedSQL}
		hosting.Upgrades.CurrentProfile = selectedSQL.CurrentProfile
		heads := make(map[string]applifecycle.Head, len(source.Upgrade.State.Apps))
		appIDs := make([]string, 0, len(source.Upgrade.State.Apps))
		for appID := range source.Upgrade.State.Apps {
			appIDs = append(appIDs, appID)
		}
		sort.Strings(appIDs)
		for index, appID := range appIDs {
			app := source.Upgrade.State.Apps[appID]
			head := appaccess.GatewayRebindRuntimeHead{AppID: appID, DeploymentID: uuid.NewString(),
				ReleaseID: uuid.NewString(), Slot: string(app.Route.Slot), Generation: int64(index + 1),
				UpdatedAt: time.Unix(int64(index+20), 0).UTC()}
			hosting.RuntimeHeads = append(hosting.RuntimeHeads, head)
			heads[appID] = applifecycle.Head{ApplicationID: appID}
			for _, endpoint := range app.Route.Endpoints {
				hosting.RuntimeComponents = append(hosting.RuntimeComponents, appaccess.GatewayStartupRuntimeComponent{
					AppID: appID, DeploymentID: head.DeploymentID, ReleaseID: head.ReleaseID,
					Name: endpoint.Component, Slot: head.Slot, ImageArtifactID: uuid.NewString(),
					ContainerName: "positive-" + endpoint.Component, ContainerID: endpoint.ContainerID,
					State: "active", CreatedAt: head.UpdatedAt, UpdatedAt: head.UpdatedAt,
				})
			}
		}
		sort.Slice(hosting.RuntimeComponents, func(i, j int) bool {
			if hosting.RuntimeComponents[i].AppID != hosting.RuntimeComponents[j].AppID {
				return hosting.RuntimeComponents[i].AppID < hosting.RuntimeComponents[j].AppID
			}
			return hosting.RuntimeComponents[i].Name < hosting.RuntimeComponents[j].Name
		})
		repository := &gatewayCurrentV2PromotionTestRepository{snapshot: hosting, heads: heads}
		ctx := gatewayCurrentV2TestScope(t, manager)
		beforeCommands := len(runner.commands)
		physical := func(context.Context, gatewayCurrentSelection) (gatewayRebindAdmissionPredecessorProof, error) {
			return gatewayRebindAdmissionPredecessorProof{Upgrade: &gatewayRebindAdmissionUpgradeProof{
				Source: cloneRouteState(*source.UpgradeSource), State: cloneGatewayV2RouteState(source.Upgrade.State),
				Journal: source.Upgrade.Journal,
			}}, nil
		}
		prepared, err := manager.prepareGatewayCurrentV2WithPhysicalLocked(ctx, repository, physical)
		confirmed, _, confirmErr := manager.readGatewayCurrentProtectedSelectionLocked(context.Background())
		if err != nil || confirmErr != nil || !prepared.Created || prepared.Selection.Lineage.ProtectedGeneration != 1 ||
			prepared.Selection.State.SourceUpgrade == nil || confirmed.V2 == nil || confirmed.Legacy != nil ||
			len(runner.commands) != beforeCommands {
			t.Fatalf("positive promotion result=%#v confirmed=%#v commands=%d error=%v confirmError=%v",
				prepared, confirmed, len(runner.commands)-beforeCommands, err, confirmErr)
		}
	})

	t.Run("later_revision_is_never_a_preparation_baseline", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind,
			Lineage: fixture.baseline.Lineage, State: &fixture.baseline}
		authority := gatewayCurrentV2PromotionAuthorityForTest(t, selection)
		baseline, err := gatewayCurrentV2BaselineFor(selection, authority)
		if err != nil {
			t.Fatal(err)
		}
		baseline.Revision++
		baseline.Digest, _ = gatewayCurrentRouteStateDigestV2(baseline)
		if gatewayCurrentV2PreparedBaselineMatches(baseline, selection, authority) {
			t.Fatal("later V2 revision was accepted as a resettable baseline")
		}
	})

	t.Run("missing_effects_scope_refuses_before_authority_or_physical_reads", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		repository := &gatewayCurrentV2PromotionRefusingRepository{}
		result, err := fixture.manager.prepareGatewayCurrentV2Locked(context.Background(), repository)
		if err == nil || !reflect.DeepEqual(result, gatewayCurrentV2Preparation{}) || repository.calls != 0 {
			t.Fatalf("missing scope result=%#v calls=%d error=%v", result, repository.calls, err)
		}
	})

	t.Run("production_observer_wrapper_uses_real_SQL_authority", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixtureWithEndpoint(t, false, '3')
		fixture.manager.options.RebindCurrentStateRepository = fixture.repository
		observation := gatewayRebindFixtureDockerObservation(t, fixture)
		runner := &gatewayRebindTypedHandoverRuntimeRunner{
			t: t, predecessorState: fixture.state, predecessor: observation,
			intent: gatewayRebindProtectedIntentV2{Identity: gatewayRebindSuccessorIdentity{
				StageContainer: "unused-current-v2-stage", FinalContainer: "unused-current-v2-final",
			}},
			effect:  gatewayRebindTypedEffectProgress{StageContainer: &gatewayRebindStageContainerBinding{ID: strings.Repeat("f", 64)}},
			finalID: strings.Repeat("a", 64),
		}
		fixture.manager.runner = runner
		t.Cleanup(func() { clearGatewayV2DockerObservation(&runner.predecessor) })
		repository := gatewayCurrentV2SQLitePromotionRepository{
			access: fixture.repository, lifecycle: applifecycle.New(fixture.db),
		}
		ctx := gatewayCurrentV2TestScope(t, fixture.manager)
		selected, selectedSQL, selectErr := fixture.manager.readGatewayCurrentProtectedSelectionLocked(ctx)
		if selectErr != nil || selected.Legacy == nil || selected.V2 != nil {
			t.Fatalf("real authority protected selection=%#v error=%v", selected, selectErr)
		}
		authority, authorityErr := readGatewayCurrentV2PromotionAuthority(ctx, repository, *selected.Legacy)
		if authorityErr != nil || !reflect.DeepEqual(authority.Snapshot.Rebind, selectedSQL) {
			snapshot, snapshotErr := repository.HostingGatewayStartupSnapshot(ctx)
			fenceErr := repository.CheckGatewayRebindFence(ctx)
			excluded, excludedErr := repository.RebindExcluded(ctx)
			apps, appsErr := gatewayCurrentPromotionSourceApps(*selected.Legacy)
			t.Fatalf("real promotion authority mismatch=%t error=%v snapshotError=%v fence=%v excluded=%t/%v snapshotMatches=%t ready=%t activeAuthorityNil=%t approvals=%t LAN=%t appsError=%v runtime=%t runtimeHeads=%d components=%d grants=%d",
				reflect.DeepEqual(authority.Snapshot.Rebind, selectedSQL), authorityErr, snapshotErr, fenceErr,
				excluded, excludedErr, gatewayCurrentV2PromotionSnapshotMatches(*selected.Legacy, snapshot),
				gatewayCurrentRouteMutationSnapshotReady(snapshot.Rebind), snapshot.ActiveRebindApprovalAuthority == nil,
				snapshot.ActiveRebindApprovalsAuthorizeServing(), gatewayCurrentV2PromotionLANConsentMatches(*selected.Legacy, snapshot),
				appsErr, gatewayServingRuntimeComponentsMatch(apps, snapshot),
				len(snapshot.RuntimeHeads), len(snapshot.RuntimeComponents), len(snapshot.Grants.Claims))
		}
		if err := fixture.manager.validateGatewayCurrentV2PromotionLANHistoryLocked(ctx, authority); err != nil {
			t.Fatalf("real promotion LAN history: %v", err)
		}
		beforeCommands := len(runner.requests)
		result, err := fixture.manager.prepareGatewayCurrentV2Locked(ctx, repository)
		commands := runner.requests[beforeCommands:]
		if err != nil || !result.Created || result.Selection.State.SourceUpgrade == nil ||
			result.Selection.State.SourceV1 != nil || len(commands) == 0 {
			t.Fatalf("real authority promotion result=%#v commands=%v error=%v", result, commands, err)
		}
		gatewayCurrentV2AssertReadOnlyDockerCommands(t, commands)
	})

	t.Run("read_only_command_allowlist_rejects_config_mutations", func(t *testing.T) {
		containerID := strings.Repeat("a", 64)
		for _, command := range [][]string{
			{"container", "exec", containerID, "curl", "--disable", "--silent", "--show-error", "--fail", "--proto", "=http", "--noproxy", "*", "--max-time", "10", "http://127.0.0.1:2019/config/"},
			{"container", "cp", containerID + ":/config/" + gatewayV2ActiveConfigFile, "-"},
			{"network", "inspect", "--format", "json", gatewayV2NetworkName},
		} {
			if !gatewayCurrentV2ReadOnlyDockerCommand(command) {
				t.Fatalf("read-only positive control was refused: %v", command)
			}
		}
		for _, command := range [][]string{
			{"container", "exec", containerID, "caddy", "reload", "--config", "/config/" + gatewayV2ActiveConfigFile},
			{"container", "exec", containerID, "curl", "--request", "POST", "http://127.0.0.1:2019/load"},
			{"container", "cp", "-", containerID + ":/config/" + gatewayV2ActiveConfigFile},
		} {
			if gatewayCurrentV2ReadOnlyDockerCommand(command) {
				t.Fatalf("config mutation entered the read-only allowlist: %v", command)
			}
		}
	})

	t.Run("authority_refusal_matrix", func(t *testing.T) {
		fixture := newGatewayCurrentV2PromotionFullFixture(t)
		source := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind,
			Lineage: fixture.state.baseline.Lineage, State: &fixture.state.baseline}
		profile := *fixture.repository.snapshot.Upgrades.CurrentProfile
		upgradeActionDigest, err := appaccess.GatewayProfileUpgradeSpecDigest(appaccess.GatewayProfileUpgradeSpec{
			ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber,
			ProfileSpecDigest: profile.SpecDigest,
		})
		if err != nil {
			t.Fatal(err)
		}
		updatedAt := time.Unix(11, 0).UTC()
		nonterminalUpgrade := appaccess.GatewayUpgradeStartupClaim{
			Claim: appaccess.GatewayProfileUpgradeClaim{
				OperationID: "99999999-9999-4999-8999-999999999999", RequestDigest: strings.Repeat("9", 64),
				ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber,
				ProfileSpecDigest: profile.SpecDigest, ApprovedBy: gatewayRebindTestAdministrator,
				ApprovedAt: updatedAt, State: appaccess.GatewayProfileUpgradeUnresolved,
				StateSequence: 2, UpdatedAt: updatedAt,
			},
			Profile: profile, ApprovedActionDigest: upgradeActionDigest,
		}
		tests := []struct {
			name   string
			mutate func(*gatewayCurrentV2PromotionTestRepository)
		}{
			{name: "missing_desired_head", mutate: func(r *gatewayCurrentV2PromotionTestRepository) {
				for appID := range r.heads {
					delete(r.heads, appID)
					break
				}
			}},
			{name: "authored_desired_head", mutate: func(r *gatewayCurrentV2PromotionTestRepository) {
				for appID, head := range r.heads {
					state, stamp := applifecycle.Stopped, time.Unix(9, 0).UTC()
					head.Revision, head.DesiredState, head.UpdatedAt = 1, &state, &stamp
					r.heads[appID] = head
					break
				}
			}},
			{name: "rebind_excluded", mutate: func(r *gatewayCurrentV2PromotionTestRepository) {
				r.excluded = true
			}},
			{name: "fenced_operation", mutate: func(r *gatewayCurrentV2PromotionTestRepository) {
				r.fenceErr = appaccess.ErrGatewayRebindActive
			}},
			{name: "active_operation", mutate: func(r *gatewayCurrentV2PromotionTestRepository) {
				r.snapshot.Rebind.Phase = appaccess.GatewayRebindPrepared
			}},
			{name: "nonterminal_upgrade", mutate: func(r *gatewayCurrentV2PromotionTestRepository) {
				r.snapshot.Upgrades.Claims = []appaccess.GatewayUpgradeStartupClaim{nonterminalUpgrade}
			}},
			{name: "runtime_component_mismatch", mutate: func(r *gatewayCurrentV2PromotionTestRepository) {
				r.snapshot.RuntimeComponents[0].ContainerID = strings.Repeat("f", 64)
			}},
			{name: "LAN_consent_mismatch", mutate: func(r *gatewayCurrentV2PromotionTestRepository) {
				r.snapshot.Grants.Claims = append(r.snapshot.Grants.Claims, appaccess.AppAccessGrantStartupClaim{})
			}},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				repository := cloneGatewayCurrentV2PromotionTestRepository(fixture.repository)
				test.mutate(repository)
				if authority, err := readGatewayCurrentV2PromotionAuthority(fixture.context, repository, source); err == nil ||
					!reflect.DeepEqual(authority, gatewayCurrentV2PromotionAuthority{}) {
					t.Fatalf("invalid authority accepted: %#v error=%v", authority, err)
				}
			})
		}
		census, err := scanGatewayCurrentV2Census(fixture.state.manager.store)
		if err != nil || len(census.files) != 0 || fixture.commands.calls != 0 {
			t.Fatalf("authority refusal mutated state: files=%d commands=%d error=%v",
				len(census.files), fixture.commands.calls, err)
		}
	})

	t.Run("create_exclusive_and_exact_retry_preserve_source_and_issue_no_commands", func(t *testing.T) {
		fixture := newGatewayCurrentV2PromotionFullFixture(t)
		created, err := fixture.state.manager.prepareGatewayCurrentV2WithPhysicalLocked(
			fixture.context, fixture.repository, fixture.physical)
		if err != nil || !created.Created || created.Selection.State.Revision != 1 ||
			created.Selection.State.SourceV1 == nil || created.Selection.State.SourceUpgrade != nil ||
			*fixture.physicalHit != 3 || fixture.commands.calls != 0 {
			t.Fatalf("create result=%#v physical=%d commands=%d error=%v",
				created, *fixture.physicalHit, fixture.commands.calls, err)
		}
		first := cloneGatewayCurrentRouteStateV2(created.Selection.State)
		fixture.selection.calls, fixture.repository.startupCalls = 0, 0
		retried, err := fixture.state.manager.prepareGatewayCurrentV2WithPhysicalLocked(
			fixture.context, fixture.repository, fixture.physical)
		if err != nil || retried.Created || !reflect.DeepEqual(retried.Selection.State, first) ||
			*fixture.physicalHit != 6 || fixture.commands.calls != 0 {
			t.Fatalf("retry result=%#v physical=%d commands=%d error=%v",
				retried, *fixture.physicalHit, fixture.commands.calls, err)
		}
	})

	t.Run("post_create_physical_drift_refuses_new_and_existing_baselines", func(t *testing.T) {
		for _, existing := range []bool{false, true} {
			name := "new_baseline"
			if existing {
				name = "existing_baseline"
			}
			t.Run(name, func(t *testing.T) {
				fixture := newGatewayCurrentV2PromotionFullFixture(t)
				if existing {
					created, err := fixture.state.manager.prepareGatewayCurrentV2WithPhysicalLocked(
						fixture.context, fixture.repository, fixture.physical)
					if err != nil || !created.Created {
						t.Fatalf("prepare existing baseline: result=%#v error=%v", created, err)
					}
					fixture.selection.calls, fixture.repository.startupCalls = 0, 0
				}
				calls := 0
				drifting := func(ctx context.Context, source gatewayCurrentSelection) (
					gatewayRebindAdmissionPredecessorProof, error,
				) {
					calls++
					proof, err := fixture.physical(ctx, source)
					if calls == 3 && proof.Current != nil {
						proof.Current.State.Revision++
					}
					return proof, err
				}
				result, err := fixture.state.manager.prepareGatewayCurrentV2WithPhysicalLocked(
					fixture.context, fixture.repository, drifting)
				if err == nil || !reflect.DeepEqual(result, gatewayCurrentV2Preparation{}) || calls != 3 ||
					fixture.commands.calls != 0 {
					t.Fatalf("drift result=%#v physical=%d commands=%d error=%v",
						result, calls, fixture.commands.calls, err)
				}
				fixture.selection.calls, fixture.repository.startupCalls = 0, 0
				retried, err := fixture.state.manager.prepareGatewayCurrentV2WithPhysicalLocked(
					fixture.context, fixture.repository, fixture.physical)
				if err != nil || retried.Created || retried.Selection.State.Revision != 1 || fixture.commands.calls != 0 {
					t.Fatalf("retained drift retry result=%#v commands=%d error=%v",
						retried, fixture.commands.calls, err)
				}
			})
		}
	})

	t.Run("authority_drift_before_and_after_install_has_bounded_retention", func(t *testing.T) {
		for _, phase := range []string{"before_create", "after_install"} {
			for _, target := range []string{"desired_head", "runtime_census"} {
				t.Run(phase+"_"+target, func(t *testing.T) {
					fixture := newGatewayCurrentV2PromotionFullFixture(t)
					calls := 0
					physical := func(ctx context.Context, source gatewayCurrentSelection) (
						gatewayRebindAdmissionPredecessorProof, error,
					) {
						calls++
						proof, err := fixture.physical(ctx, source)
						trigger := (phase == "before_create" && calls == 1) || (phase == "after_install" && calls == 3)
						if trigger && target == "desired_head" {
							for appID, head := range fixture.repository.heads {
								state, stamp := applifecycle.Stopped, time.Unix(10, 0).UTC()
								head.Revision, head.DesiredState, head.UpdatedAt = 1, &state, &stamp
								fixture.repository.heads[appID] = head
								break
							}
						}
						if trigger && target == "runtime_census" {
							fixture.repository.snapshot.RuntimeComponents[0].ContainerID = strings.Repeat("f", 64)
						}
						return proof, err
					}
					result, err := fixture.state.manager.prepareGatewayCurrentV2WithPhysicalLocked(
						fixture.context, fixture.repository, physical)
					if err == nil || !reflect.DeepEqual(result, gatewayCurrentV2Preparation{}) || fixture.commands.calls != 0 {
						t.Fatalf("authority drift result=%#v physical=%d commands=%d error=%v",
							result, calls, fixture.commands.calls, err)
					}
					census, censusErr := scanGatewayCurrentV2Census(fixture.state.manager.store)
					wantFiles := 0
					if phase == "after_install" {
						wantFiles = 1
					}
					if censusErr != nil || len(census.files) != wantFiles {
						t.Fatalf("authority drift retention files=%d want=%d error=%v",
							len(census.files), wantFiles, censusErr)
					}
				})
			}
		}
	})

	t.Run("exclusive_create_preserves_conflict_installed_at_second_physical_read", func(t *testing.T) {
		fixture := newGatewayCurrentV2PromotionFullFixture(t)
		source := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind,
			Lineage: fixture.state.baseline.Lineage, State: &fixture.state.baseline}
		authority, err := readGatewayCurrentV2PromotionAuthority(fixture.context, fixture.repository, source)
		if err != nil {
			t.Fatal(err)
		}
		fixture.repository.startupCalls = 0
		conflict, err := gatewayCurrentV2BaselineFor(source, authority)
		if err != nil {
			t.Fatal(err)
		}
		appID := gatewayCurrentRouteStateV2FirstApp(t, conflict)
		app := conflict.Apps[appID]
		app.Runtime.ActiveGeneration++
		conflict.Apps[appID] = app
		conflict.Digest, _ = gatewayCurrentRouteStateDigestV2(conflict)
		store, err := newGatewayCurrentRouteStateV2StoreForOrigin(fixture.state.dataRoot,
			conflict.Lineage, gatewayCurrentRouteOriginForStateV2(conflict))
		if err != nil || !validGatewayCurrentRouteStateV2(conflict) {
			t.Fatalf("build conflicting baseline: %v", err)
		}
		calls := 0
		var installErr error
		physical := func(ctx context.Context, selected gatewayCurrentSelection) (
			gatewayRebindAdmissionPredecessorProof, error,
		) {
			calls++
			proof, readErr := fixture.physical(ctx, selected)
			if calls == 2 {
				installErr = store.installBaseline(conflict)
			}
			return proof, readErr
		}
		result, err := fixture.state.manager.prepareGatewayCurrentV2WithPhysicalLocked(
			fixture.context, fixture.repository, physical)
		loaded, loadErr := store.load()
		if err == nil || installErr != nil || loadErr != nil || !reflect.DeepEqual(loaded, conflict) ||
			!reflect.DeepEqual(result, gatewayCurrentV2Preparation{}) || fixture.commands.calls != 0 {
			t.Fatalf("exclusive conflict result=%#v installed=%v loaded=%v commands=%d error=%v",
				result, installErr, loadErr, fixture.commands.calls, err)
		}
	})

	t.Run("cancellation_at_final_physical_read_retains_created_baseline", func(t *testing.T) {
		fixture := newGatewayCurrentV2PromotionFullFixture(t)
		ctx, cancel := context.WithCancel(fixture.context)
		calls := 0
		physical := func(readCtx context.Context, source gatewayCurrentSelection) (
			gatewayRebindAdmissionPredecessorProof, error,
		) {
			calls++
			proof, err := fixture.physical(readCtx, source)
			if calls == 3 {
				cancel()
			}
			return proof, err
		}
		result, err := fixture.state.manager.prepareGatewayCurrentV2WithPhysicalLocked(
			ctx, fixture.repository, physical)
		census, censusErr := scanGatewayCurrentV2Census(fixture.state.manager.store)
		if err == nil || !reflect.DeepEqual(result, gatewayCurrentV2Preparation{}) || calls != 3 ||
			censusErr != nil || len(census.files) != 1 || fixture.commands.calls != 0 {
			t.Fatalf("final cancel result=%#v physical=%d files=%d commands=%d censusError=%v error=%v",
				result, calls, len(census.files), fixture.commands.calls, censusErr, err)
		}
	})

	t.Run("post_create_read_failure_retains_exact_baseline_for_retry", func(t *testing.T) {
		fixture := newGatewayCurrentV2PromotionFullFixture(t)
		// Each protected selection performs two SQL reads. The fifth read is the
		// first readback after the create-exclusive install.
		fixture.selection.failAt = 5
		failed, err := fixture.state.manager.prepareGatewayCurrentV2WithPhysicalLocked(
			fixture.context, fixture.repository, fixture.physical)
		if err == nil || !reflect.DeepEqual(failed, gatewayCurrentV2Preparation{}) || fixture.commands.calls != 0 {
			t.Fatalf("post-create failure result=%#v commands=%d error=%v", failed, fixture.commands.calls, err)
		}
		fixture.selection.calls, fixture.selection.failAt, fixture.repository.startupCalls = 0, 0, 0
		retried, err := fixture.state.manager.prepareGatewayCurrentV2WithPhysicalLocked(
			fixture.context, fixture.repository, fixture.physical)
		if err != nil || retried.Created || retried.Selection.State.Revision != 1 || fixture.commands.calls != 0 {
			t.Fatalf("retained retry result=%#v commands=%d error=%v", retried, fixture.commands.calls, err)
		}
	})

	t.Run("cancellation_after_physical_recheck_prevents_create", func(t *testing.T) {
		fixture := newGatewayCurrentV2PromotionFullFixture(t)
		parent, cancel := context.WithCancel(fixture.context)
		calls := 0
		physical := func(ctx context.Context, source gatewayCurrentSelection) (
			gatewayRebindAdmissionPredecessorProof, error,
		) {
			calls++
			proof, err := fixture.physical(ctx, source)
			if calls == 2 {
				cancel()
			}
			return proof, err
		}
		result, err := fixture.state.manager.prepareGatewayCurrentV2WithPhysicalLocked(
			parent, fixture.repository, physical)
		if err == nil || !reflect.DeepEqual(result, gatewayCurrentV2Preparation{}) || calls != 2 ||
			fixture.commands.calls != 0 {
			t.Fatalf("cancel result=%#v physical=%d commands=%d error=%v",
				result, calls, fixture.commands.calls, err)
		}
		census, censusErr := scanGatewayCurrentV2Census(fixture.state.manager.store)
		if censusErr != nil || len(census.files) != 0 {
			t.Fatalf("cancelled promotion created V2 state: files=%d error=%v", len(census.files), censusErr)
		}
	})
}
