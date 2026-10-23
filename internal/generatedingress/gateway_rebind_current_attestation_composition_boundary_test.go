package generatedingress

import (
	"bytes"
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
)

// gatewayRebindCurrentAttestationCompositionBoundaryDriver observes the
// database-committed boundary without changing its coordinator result. It
// keeps the composition runner and current physical driver installed by the
// concrete fixture; the wrapper only records whether the managed current
// driver boundary is reached after the SQL and protected-state checks pass.
type gatewayRebindCurrentAttestationCompositionBoundaryDriver struct {
	*gatewayRebindCompositionDriver
	repository      *appaccess.Repository
	boundaryCalled  bool
	driverEntries   int
	delegateRefused bool
}

type gatewayRebindCurrentAttestationCompositionObserver struct {
	managedGatewayCurrentPhysicalDriver
	entries int
}

func (d *gatewayRebindCurrentAttestationCompositionObserver) attestGatewayRebindCurrent(ctx context.Context,
	target gatewayCurrentPhysicalTarget, guard func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	d.entries++
	return d.managedGatewayCurrentPhysicalDriver.attestGatewayRebindCurrent(ctx, target, guard)
}

func (d *gatewayRebindCurrentAttestationCompositionBoundaryDriver) attestCommittedCurrentLocked(ctx context.Context,
	selection gatewayCurrentSelection,
) (string, error) {
	d.t.Helper()
	d.boundaryCalled = true
	if d.repository == nil || selection.State == nil || selection.Terminal == nil {
		d.t.Fatal("current-attestation boundary fixture lacks selected typed current")
	}
	beforeSQL, err := d.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || beforeSQL.Rebind.Phase != appaccess.GatewayRebindDatabaseCommitted ||
		beforeSQL.Rebind.Active == nil || d.repository.CheckGatewayRebindFence(ctx) == nil {
		d.t.Fatal("current-attestation boundary did not reach database-committed SQL")
	}
	beforeHistory, err := readGatewayHistorySnapshotMode(d.manager.store, true)
	if err != nil {
		d.t.Fatal("current-attestation boundary cannot read protected history")
	}
	beforeEffects := len(d.runner.effects)
	beforeBarrier := d.manager.gatewayRebindCommitBarrierLatch().Load()
	beforeFailStop := d.manager.gatewayRebindFailStopLatch().Load()

	history, err := d.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 17 || len(history.IntentsV2) != 1 ||
		len(history.TerminalsV2) != 1 {
		d.t.Fatal("current-attestation boundary lacks complete typed P17 history")
	}
	attempt, err := gatewayRebindPreparedAttemptFromHistory(beforeSQL.Rebind, history)
	if err != nil || len(attempt.Intent.Roster) != 1 || len(attempt.Intent.RuntimeHeads) != 2 {
		d.t.Fatal("current-attestation boundary does not retain one LAN roster and two runtime heads")
	}
	receipt, err := gatewayRebindActiveTerminalV2(history, attempt.Claim)
	if err != nil || receipt == nil {
		d.t.Fatal("current-attestation boundary lacks active typed receipt")
	}
	request := gatewayRebindForwardConfirmationRequest(attempt, *receipt, beforeSQL.Rebind)
	if !d.manager.gatewayRebindCommittedServingCensusMatchesLocked(ctx, request, *selection.State, beforeSQL) {
		d.t.Fatal("current-attestation boundary committed SQL census is not exact")
	}
	lan, loopback := 0, 0
	for _, app := range selection.State.Apps {
		if app.LAN == nil {
			loopback++
		} else {
			lan++
		}
	}
	if len(selection.State.Apps) != 2 || len(beforeSQL.RuntimeHeads) != 2 ||
		len(beforeSQL.RuntimeComponents) != 2 || lan != 1 || loopback != 1 {
		d.t.Fatal("current-attestation boundary did not preserve LAN and loopback-only application census")
	}
	p11 := history.Progress[10].Record
	if p11.Sequence != 11 || p11.TypedEffect == nil || p11.TypedEffect.FinalConfigIntent == nil ||
		history.Progress[16].Record.TypedEffect == nil || history.Progress[16].Record.TypedEffect.Resources == nil ||
		len(history.Progress[16].Record.TypedEffect.Resources.ApplicationNetworks) != 2 {
		d.t.Fatal("current-attestation boundary lacks two-network P11/P17 evidence")
	}
	p11Bytes, err := gatewayRebindTypedFinalConfigBytes(attempt.Intent, attempt.Checkpoint,
		p11.TypedEffect.FinalConfigIntent.RoutePlan)
	if err != nil {
		d.t.Fatal("current-attestation boundary cannot render P11 final config")
	}
	baselineBytes, err := gatewayCurrentPhysicalConfigBytes(*selection.State)
	if err != nil || !bytes.Equal(p11Bytes, baselineBytes) ||
		!bytes.Equal(baselineBytes, d.runner.currentLiveBody()) ||
		!bytes.Equal(baselineBytes, d.runner.currentRestartBody()) {
		d.t.Fatal("current-attestation boundary P11, P17 and selected baseline config differ")
	}
	managed, ok := d.manager.gatewayCurrentPhysicalDriver.(managedGatewayCurrentPhysicalDriver)
	if !ok {
		d.t.Fatal("current-attestation boundary does not retain composition managed driver")
	}
	observer := &gatewayRebindCurrentAttestationCompositionObserver{managedGatewayCurrentPhysicalDriver: managed}
	d.manager.gatewayCurrentPhysicalDriver = observer

	digest, attestErr := d.gatewayRebindCompositionDriver.attestCommittedCurrentLocked(ctx, selection)
	d.driverEntries = observer.entries
	d.delegateRefused = attestErr != nil
	afterSQL, afterSQLErr := d.repository.HostingGatewayStartupSnapshot(ctx)
	afterHistory, afterHistoryErr := readGatewayHistorySnapshotMode(d.manager.store, true)
	if afterSQLErr != nil || afterHistoryErr != nil || !reflect.DeepEqual(beforeSQL, afterSQL) ||
		!sameGatewayHistorySnapshot(beforeHistory, afterHistory) || len(d.runner.effects) != beforeEffects ||
		d.manager.gatewayRebindCommitBarrierLatch().Load() != beforeBarrier ||
		d.manager.gatewayRebindFailStopLatch().Load() != beforeFailStop {
		d.t.Fatal("current-attestation boundary changed authority, protected history, latches, or effects")
	}
	if attestErr == nil && (!validSHA256(digest) || d.driverEntries != 1) {
		d.t.Fatal("current-attestation boundary did not complete through the managed driver boundary")
	}
	return digest, attestErr
}

func newGatewayRebindCurrentAttestationTwoAppCompositionFixture(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayRebindCommitInput, *gatewayRebindCurrentAttestationCompositionBoundaryDriver,
) {
	t.Helper()
	f := newGatewayRebindPredecessorFixtureWithEndpointAndLANApprover(t, false, '3', uuid.NewString())
	migration, _ := newGeneratedRouteCanonicalMigrationOnDatabase(t, f.db, f.manager.options.DataRoot,
		f.manager.options.WorkingDirectory)
	loopbackID := migration.guarded.appID
	head, loopbackEndpoint := seedGatewayRebindPredecessorRuntimeWithContainerID(t, f.manager, f.runner,
		migration, strings.Repeat("7", 64))
	var loopbackJobID string
	if err := f.db.QueryRow(`SELECT job_id FROM deployments
		WHERE id=? AND app_id=? AND status='preparing'`, head.DeploymentID, loopbackID).Scan(&loopbackJobID); err != nil {
		t.Fatal("two-app composition fixture cannot find preparing loopback deployment")
	}
	completed := time.Now().UTC().Format(time.RFC3339Nano)
	jobResult, err := f.db.Exec(`UPDATE jobs SET status='succeeded',phase='completed',updated_at=?,finished_at=?
		WHERE id=? AND resource_type='application' AND resource_id=? AND status='running'`,
		completed, completed, loopbackJobID, loopbackID)
	if err != nil {
		t.Fatal("two-app composition fixture cannot settle loopback job")
	}
	jobRows, err := jobResult.RowsAffected()
	if err != nil || jobRows != 1 {
		t.Fatal("two-app composition fixture did not settle exactly one loopback job")
	}
	deploymentResult, err := f.db.Exec(`UPDATE deployments SET status='succeeded',finished_at=?
		WHERE id=? AND app_id=? AND job_id=? AND status='preparing'`, completed, head.DeploymentID, loopbackID, loopbackJobID)
	if err != nil {
		t.Fatal("two-app composition fixture cannot settle loopback deployment")
	}
	deploymentRows, err := deploymentResult.RowsAffected()
	if err != nil || deploymentRows != 1 {
		t.Fatal("two-app composition fixture did not settle exactly one loopback deployment")
	}
	state := cloneGatewayV2RouteState(f.state)
	lanAppID := f.stateAppID()
	lanRoute, found := state.Apps[lanAppID]
	if !found || lanRoute.LAN == nil {
		t.Fatal("two-app composition fixture lacks original LAN source")
	}
	loopbackRoute := cloneGatewayV2AppRoute(lanRoute)
	loopbackRoute.LAN = nil
	loopbackRoute.Route.Slot = generatedidentity.Slot(head.Slot)
	loopbackRoute.Route.Endpoints = []generatedroute.RouteEndpoint{loopbackEndpoint}
	state.Apps[loopbackID] = loopbackRoute
	if err := f.store.writeExact(f.store.v2Path, f.store.v2Purpose, state, false, maxV2RouteStateBytes); err != nil {
		t.Fatal(err)
	}
	f.state = state
	f, input, template := newGatewayRebindCoordinatorFixtureWithPredecessor(t, f)
	if len(input.Inspection.Roster) != 1 || len(input.Inspection.RuntimeHeads) != 2 {
		t.Fatal("two-app composition proposal changed LAN roster or runtime-head census")
	}
	runner := &gatewayRebindCompositionRunner{gatewayRebindTypedHandoverRuntimeRunner: &gatewayRebindTypedHandoverRuntimeRunner{
		t: t, predecessorState: f.state, predecessor: gatewayRebindFixtureDockerObservation(t, f), finalID: strings.Repeat("9", 64)},
		before: f.manager.runner}
	f.manager.gatewayRebindV2NetworkObserver = func(_ context.Context,
		claim appaccess.GatewayRebindClaimV2,
	) (gatewayRebindSuccessorNetworkObservation, error) {
		value := template.template.intent.NetworkObservation
		value.OperationID, value.ClaimRequestDigest, value.ProfileSpecDigest = claim.Spec.OperationID,
			claim.RequestDigest, claim.ConfigureApproval.SpecDigest
		value.Candidates = append(value.Candidates, gatewayRebindSuccessorNetworkCandidate{
			InterfaceID: f.state.Profile.InterfaceID, IPv4: f.state.Profile.SelectedIPv4, Prefix: "192.168.96.0/24"})
		sort.Slice(value.Candidates, func(i, j int) bool { return gatewayRebindCandidateLess(value.Candidates[i], value.Candidates[j]) })
		value.HostInterfaces = append(value.HostInterfaces, "192.168.96.0/24")
		sort.Strings(value.HostInterfaces)
		ids, prefixes := runner.networkInventory()
		value.DockerNetworkIDs = ids
		value.DockerPrefixes = nil
		for _, prefix := range prefixes {
			value.DockerPrefixes = append(value.DockerPrefixes, prefix.String())
		}
		sort.Strings(value.DockerPrefixes)
		return value, nil
	}
	f.manager.runner = runner
	composition := &gatewayRebindCompositionDriver{managerGatewayRebindCrossStoreDriver: managerGatewayRebindCrossStoreDriver{manager: f.manager},
		t: t, runner: runner}
	driver := &gatewayRebindCurrentAttestationCompositionBoundaryDriver{gatewayRebindCompositionDriver: composition,
		repository: f.repository}
	t.Cleanup(func() {
		if runner.stage != nil {
			clear(runner.stage.stageBody)
			clear(runner.stage.activeBody)
			clear(runner.stage.autosave)
		}
		clear(runner.currentCandidate)
		clear(runner.currentLive)
		clear(runner.currentNext)
		clear(runner.currentRestart)
		clear(runner.currentAutosave)
		clearGatewayV2DockerObservation(&runner.predecessor)
	})
	return f, input, driver
}

func TestGatewayRebindCurrentAttestationCompositionBoundaryRetainsTwoAppCommittedCensus(t *testing.T) {
	f, input, driver := newGatewayRebindCurrentAttestationTwoAppCompositionFixture(t)
	result, err := f.manager.commitGatewayRebindWithDriver(context.Background(), f.repository, input, driver)
	if err != nil || result.FinalPhase != appaccess.GatewayRebindCommitted || !result.FenceReleased || driver.driverEntries != 1 {
		t.Fatalf("two-app database-committed current attestation incomplete: boundary_called=%t driver_entries=%d delegate_refused=%t",
			driver.boundaryCalled, driver.driverEntries, driver.delegateRefused)
	}
}
