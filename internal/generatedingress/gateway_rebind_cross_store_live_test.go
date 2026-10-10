package generatedingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const liveGatewayRebindCrossStoreEnvironment = "RIG_RUN_LIVE_GATEWAY_REBIND_CROSS_STORE"

// This gate uses real SQL, protected gateway state, and two actual applications.
// It covers proposal inspection only; commit/crash/repeated-rebind journeys must
// separately prove cross-store effects and recovery with the same fixture.
func TestLiveGatewayRebindCrossStoreProposalRetainsCurrentAuthority(t *testing.T) {
	f := newLiveGatewayRebindSourceFixture(t, liveGatewayRebindCrossStoreEnvironment)
	fixture := f.fixture
	options := fixture.ingress.options
	options.RebindFenceCheck = f.repository.CheckGatewayRebindFence
	options.RebindCurrentStateRepository = f.repository
	manager, err := New(fixture.runner, options)
	if err != nil {
		t.Fatal("open real manager with the repository rebind fence")
	}
	fixture.ingress = manager
	beforeSQL, err := f.repository.GatewayRebindRecoverySnapshot(fixture.ctx)
	if err != nil || beforeSQL.Active != nil || len(beforeSQL.History) != 0 || beforeSQL.CurrentSource == nil ||
		beforeSQL.CurrentSource.Kind != appaccess.GatewayRebindSourceGatewayUpgrade ||
		beforeSQL.CurrentSource.ProfileRevisionID != f.profile.ID {
		t.Fatal("source fixture has no exact native SQL gateway authority")
	}
	beforeCurrent, err := manager.InspectGatewayRebindCurrent(fixture.ctx, f.repository)
	if err != nil || beforeCurrent.SelectedCurrentAuthority != *beforeSQL.CurrentSource ||
		beforeCurrent.ActiveOperationID != "" || beforeCurrent.ActivePhase != "" ||
		!beforeCurrent.FenceReleased || len(beforeCurrent.Retained) != 0 {
		t.Fatal("source inspection does not agree with native SQL authority and an open fence")
	}
	beforeAccess, err := f.repository.CurrentAppAccess(fixture.ctx, fixture.spec.appID)
	if err != nil {
		t.Fatal("read immutable source application access")
	}
	beforeHeads, err := f.repository.GatewayRebindRuntimeHeads(fixture.ctx)
	if err != nil || len(beforeHeads) != 2 {
		t.Fatal("source fixture requires two complete runtime heads")
	}
	beforeState, beforeJournal, _ := liveGatewayV2LoadDurableOperation(t, fixture)
	beforeRoute, err := manager.store.load()
	if err != nil {
		t.Fatal("read protected source routes")
	}
	dockerDigest := func() string {
		t.Helper()
		observed, err := manager.inspectGatewayRebindDocker(fixture.ctx, beforeRoute, beforeState, beforeJournal)
		defer clearGatewayV2DockerObservation(&observed)
		if err != nil || !validGatewayRebindPredecessorDocker(beforeRoute, beforeState, beforeJournal, observed) {
			t.Fatal("source Docker ownership or topology changed")
		}
		digest, err := gatewayRebindPredecessorDockerDigest(observed, beforeState.Identity)
		if err != nil {
			t.Fatal("digest exact source Docker observation")
		}
		return digest
	}
	beforeDocker := dockerDigest()
	beforeRequests := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	input := GatewayRebindProposalInput{OperationID: f.proposal.Spec.OperationID,
		SuccessorProfileRevisionID:     f.proposal.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: f.proposal.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    f.proposal.Spec.SuccessorProfileOperationID,
		SuccessorProfile:               f.proposal.Spec.SuccessorProfile}
	inspection, err := manager.InspectGatewayRebindProposal(fixture.ctx, f.repository, input)
	if err != nil {
		failLiveIngress(t, "inspect real typed cross-store proposal", err)
	}
	assertLiveCrossStoreRuntimeHeads(t, inspection, beforeHeads, fixture.spec.appID, f.loopbackID)
	digest, err := appaccess.GatewayRebindSpecV2Digest(inspection.Spec)
	if err != nil || digest != inspection.SpecDigest || inspection.Spec.Version != appaccess.GatewayRebindSpecVersionV2 ||
		inspection.Spec.OperationID != input.OperationID || inspection.Spec.SuccessorProfile != input.SuccessorProfile ||
		inspection.Spec.Predecessor.Lineage.OperationID != beforeSQL.CurrentSource.OperationID ||
		inspection.Spec.Predecessor.Lineage.ProfileRevisionID != f.profile.ID ||
		inspection.PredecessorCheckpointDigest != inspection.Spec.Predecessor.PredecessorCheckpointDigest ||
		inspection.Spec.SuccessorProtectedGeneration != inspection.ProtectedGeneration ||
		inspection.ProtectedGeneration <= inspection.Spec.Predecessor.Lineage.ProtectedGeneration ||
		inspection.SourceStateVersion != beforeCurrent.CurrentStateVersion ||
		inspection.SourceStateRevision != beforeCurrent.CurrentStateRevision ||
		inspection.SourceStateDigest != beforeCurrent.CurrentStateDigest ||
		len(inspection.Roster) != 1 || inspection.Spec.RosterCount != 1 {
		t.Fatal("proposal does not bind exact current authority, successor, checkpoint and LAN roster")
	}
	roster := inspection.Roster[0]
	if roster.AppID != fixture.spec.appID || roster.AllocationID != beforeAccess.Allocation.ID ||
		roster.SourceProfileRevisionID != f.profile.ID || roster.SourceProfileRevisionNumber != f.profile.RevisionNumber ||
		roster.SourceProfileSpecDigest != f.profile.SpecDigest || roster.PredecessorTransferDigest != nil {
		t.Fatal("native proposal changed raw grant provenance or included the loopback-only application")
	}
	repeated, err := manager.InspectGatewayRebindProposal(fixture.ctx, f.repository, input)
	if err != nil || !reflect.DeepEqual(inspection, repeated) {
		t.Fatal("repeated inspection changed the exact approval proposal")
	}
	freshManager, err := New(fixture.runner, options)
	if err != nil {
		t.Fatal("open fresh manager for proposal replay")
	}
	freshInspection, err := freshManager.InspectGatewayRebindProposal(fixture.ctx, f.repository, input)
	if err != nil || !reflect.DeepEqual(inspection, freshInspection) {
		t.Fatal("fresh manager changed the exact approval proposal")
	}
	afterCurrent, err := freshManager.InspectGatewayRebindCurrent(fixture.ctx, f.repository)
	if err != nil || !reflect.DeepEqual(beforeCurrent, afterCurrent) {
		t.Fatal("proposal inspection changed current authority, protected revision or fence state")
	}
	afterSQL, err := f.repository.GatewayRebindRecoverySnapshot(fixture.ctx)
	if err != nil || !reflect.DeepEqual(beforeSQL, afterSQL) {
		t.Fatal("proposal inspection changed SQL authority or admitted a rebind")
	}
	afterAccess, err := f.repository.CurrentAppAccess(fixture.ctx, fixture.spec.appID)
	if err != nil || !reflect.DeepEqual(beforeAccess, afterAccess) {
		t.Fatal("proposal inspection changed immutable application access")
	}
	afterHeads, err := f.repository.GatewayRebindRuntimeHeads(fixture.ctx)
	if err != nil || !reflect.DeepEqual(beforeHeads, afterHeads) {
		t.Fatal("proposal inspection changed generated runtime heads")
	}
	afterState, afterJournal, _ := liveGatewayV2LoadDurableOperation(t, fixture)
	afterRoute, err := manager.store.load()
	if err != nil || !reflect.DeepEqual(beforeState, afterState) || !reflect.DeepEqual(beforeJournal, afterJournal) ||
		!reflect.DeepEqual(beforeRoute, afterRoute) || beforeDocker != dockerDigest() {
		t.Fatal("proposal inspection changed protected state or source Docker resources")
	}
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 0 || len(history.IntentsV2) != 0 || len(history.Checkpoints) != 0 || len(history.Progress) != 0 || len(history.Terminals) != 0 {
		t.Fatal("advisory inspection installed protected rebind evidence")
	}
	if afterRequests := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); afterRequests.Routed != beforeRequests.Routed {
		t.Fatal("advisory inspection forwarded an application request")
	}
	if err := f.repository.CheckGatewayRebindFence(fixture.ctx); err != nil {
		t.Fatal("advisory inspection left a rebind fence")
	}
}

const liveCrossStoreChildEnvironment = "RIG_TEST_CROSS_STORE_ADMISSION_CHILD"
const liveCrossStoreChildModeEnvironment = "RIG_TEST_CROSS_STORE_ADMISSION_MODE"

type liveCrossStoreAdmissionChild struct {
	Controller liveFinalHandoverChild
	Input      gatewayRebindCommitInput
}

func liveCrossStoreAdmissionChildInput(value liveCrossStoreAdmissionChild) (gatewayRebindCommitInput, error) {
	input := value.Input
	inspection := input.Inspection
	specDigest, err := appaccess.GatewayRebindSpecV2Digest(inspection.Spec)
	if err != nil || specDigest != inspection.SpecDigest || inspection.Spec.RosterCount != int64(len(inspection.Roster)) {
		return gatewayRebindCommitInput{}, errors.New("invalid private admission child inspection")
	}
	roster := append([]appaccess.GatewayRebindRosterEntryV2(nil), inspection.Roster...)
	for index := range roster {
		if roster[index].Ordinal != int64(index+1) || roster[index].OperationID != inspection.Spec.OperationID {
			return gatewayRebindCommitInput{}, errors.New("invalid private admission child inspection")
		}
		digest, digestErr := appaccess.GatewayRebindRosterEntryV2Digest(roster[index])
		if digestErr != nil {
			return gatewayRebindCommitInput{}, errors.New("invalid private admission child inspection")
		}
		roster[index].EntryDigest = digest
	}
	rosterDigest, err := appaccess.GatewayRebindRosterV2Digest(roster)
	if err != nil || rosterDigest != inspection.Spec.RosterDigest {
		return gatewayRebindCommitInput{}, errors.New("invalid private admission child inspection")
	}
	inspection.Roster = roster
	input.Inspection = inspection
	return input, nil
}

// This is a real abrupt test-process exit, with no deferred lock cleanup,
// followed by two independent recovery processes. It covers prepared admission
// only, not hostd bootstrap dispatch or terminal cross-store commit recovery.
func TestLiveGatewayRebindCrossStorePreparedClaimProcessRecovery(t *testing.T) {
	f := newLiveGatewayRebindSourceFixture(t, liveGatewayRebindCrossStoreEnvironment)
	fixture := f.fixture
	options := fixture.ingress.options
	options.RebindFenceCheck = f.repository.CheckGatewayRebindFence
	options.RebindCurrentStateRepository = f.repository
	manager, err := New(fixture.runner, options)
	if err != nil {
		t.Fatal("open source manager with real SQL authority")
	}
	fixture.ingress = manager
	inspection, err := manager.InspectGatewayRebindProposal(fixture.ctx, f.repository, GatewayRebindProposalInput{
		OperationID: f.proposal.Spec.OperationID, SuccessorProfileRevisionID: f.proposal.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: f.proposal.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    f.proposal.Spec.SuccessorProfileOperationID, SuccessorProfile: f.proposal.Spec.SuccessorProfile,
	})
	if err != nil || len(inspection.Roster) != 1 {
		t.Fatal("inspect complete real native admission proposal")
	}
	beforeSQL, err := f.repository.GatewayRebindRecoverySnapshot(fixture.ctx)
	if err != nil || beforeSQL.CurrentSource == nil || beforeSQL.Active != nil || len(beforeSQL.History) != 0 {
		t.Fatal("prepared process fixture must start with one native authority")
	}
	beforeFiles, err := readGatewayHistorySnapshotMode(manager.store, true)
	if err != nil {
		t.Fatal("read predecessor protected evidence")
	}
	beforeHeads, err := f.repository.GatewayRebindRuntimeHeads(fixture.ctx)
	if err != nil || len(beforeHeads) != 2 {
		t.Fatal("read complete two-application runtime heads")
	}
	assertLiveCrossStoreRuntimeHeads(t, inspection, beforeHeads, fixture.spec.appID, f.loopbackID)
	beforeAccess, err := f.repository.CurrentAppAccess(fixture.ctx, fixture.spec.appID)
	if err != nil {
		t.Fatal("read immutable raw grant")
	}
	beforeState, beforeJournal, _ := liveGatewayV2LoadDurableOperation(t, fixture)
	beforeRoute, err := manager.store.load()
	if err != nil {
		t.Fatal("read original protected routes")
	}
	dockerDigest := func() string {
		t.Helper()
		observed, err := manager.inspectGatewayRebindDocker(fixture.ctx, beforeRoute, beforeState, beforeJournal)
		defer clearGatewayV2DockerObservation(&observed)
		if err != nil || !validGatewayRebindPredecessorDocker(beforeRoute, beforeState, beforeJournal, observed) {
			t.Fatal("prepared admission changed predecessor Docker ownership or topology")
		}
		digest, err := gatewayRebindPredecessorDockerDigest(observed, beforeState.Identity)
		if err != nil {
			t.Fatal("digest source Docker inventory")
		}
		return digest
	}
	beforeDocker := dockerDigest()
	beforeRequests := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	child := liveCrossStoreAdmissionChild{Controller: liveFinalHandoverChild{
		DataRoot: options.DataRoot, WorkingDirectory: options.WorkingDirectory, DockerExecutable: options.DockerExecutable,
		DockerConfigDirectory: options.DockerConfigDirectory, HostPort: options.HostPort,
	}, Input: gatewayRebindCommitInput{Inspection: inspection,
		RebindApproval:    appaccess.Approval{Action: appaccess.ActionRebindGateway, SpecDigest: inspection.SpecDigest, ActorID: gatewayRebindTestAdministrator},
		ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway, SpecDigest: inspection.SuccessorProfileSpecDigest, ActorID: gatewayRebindTestAdministrator},
	}}
	path := filepath.Join(fixture.root, "cross-store-admission-child.json")
	body, err := json.Marshal(child)
	if err != nil || os.WriteFile(path, body, 0o600) != nil {
		t.Fatal("write private admission child configuration")
	}
	liveRunCrossStoreAdmissionChild(t, fixture.ctx, path, "exit-after-claim")
	prepared, err := f.repository.GatewayRebindRecoverySnapshot(fixture.ctx)
	if err != nil || prepared.Active == nil || prepared.Active.Claim.V2 == nil || len(prepared.History) != 1 ||
		prepared.Phase != appaccess.GatewayRebindPrepared || !prepared.RollbackAllowed || prepared.DatabaseCommitObserved ||
		!reflect.DeepEqual(prepared.CurrentSource, beforeSQL.CurrentSource) || !reflect.DeepEqual(prepared.CurrentProfile, beforeSQL.CurrentProfile) ||
		!reflect.DeepEqual(prepared.Active.Claim.V2.Spec, inspection.Spec) ||
		!reflect.DeepEqual(prepared.Active.RuntimeHeads, beforeHeads) || !reflect.DeepEqual(prepared.History[0].RuntimeHeads, beforeHeads) ||
		!errors.Is(f.repository.CheckGatewayRebindFence(fixture.ctx), appaccess.ErrGatewayRebindActive) {
		t.Fatal("abrupt exit did not retain the exact prepared SQL claim and fence")
	}
	afterExitFiles, err := readGatewayHistorySnapshotMode(manager.store, true)
	if err != nil || !sameGatewayHistorySnapshot(beforeFiles, afterExitFiles) {
		t.Fatal("post-claim exit installed protected attempt evidence")
	}
	liveRunCrossStoreAdmissionChild(t, fixture.ctx, path, "recover")
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 0 || len(history.IntentsV2) != 1 || len(history.Checkpoints) != 1 ||
		len(history.Progress) != 1 || len(history.Terminals) != 0 ||
		history.IntentsV2[0].Intent.Predecessor != inspection.Spec.Predecessor ||
		history.IntentsV2[0].Intent.Generation != inspection.ProtectedGeneration ||
		history.Progress[0].Record.Phase != gatewayRebindProgressSuccessorIntent {
		t.Fatal("fresh process did not recover exactly one typed prepared intent")
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, history.Progress[0].Record.OccurredAt)
	if err != nil || !occurredAt.After(prepared.Active.Claim.V2.CreatedAt) {
		t.Fatal("protected progress did not follow the actual SQL commit")
	}
	recoveredFiles, err := readGatewayHistorySnapshotMode(manager.store, true)
	if err != nil {
		t.Fatal("read recovered protected evidence")
	}
	retainedFiles := gatewayHistorySnapshot{files: make(map[string]gatewayHistoryFileFingerprint)}
	for name := range beforeFiles.files {
		if value, exists := recoveredFiles.files[name]; exists {
			retainedFiles.files[name] = value
		}
	}
	if !sameGatewayHistorySnapshot(beforeFiles, retainedFiles) {
		t.Fatal("recovery replaced predecessor protected history")
	}
	liveRunCrossStoreAdmissionChild(t, fixture.ctx, path, "recover")
	finalFiles, err := readGatewayHistorySnapshotMode(manager.store, true)
	if err != nil || !sameGatewayHistorySnapshot(recoveredFiles, finalFiles) {
		t.Fatal("second recovery process replaced immutable prepared evidence")
	}
	finalSQL, err := f.repository.GatewayRebindRecoverySnapshot(fixture.ctx)
	if err != nil || !reflect.DeepEqual(prepared, finalSQL) || !errors.Is(f.repository.CheckGatewayRebindFence(fixture.ctx), appaccess.ErrGatewayRebindActive) {
		t.Fatal("prepared recovery changed SQL authority or released admission")
	}
	afterHeads, err := f.repository.GatewayRebindRuntimeHeads(fixture.ctx)
	if err != nil || !reflect.DeepEqual(beforeHeads, afterHeads) {
		t.Fatal("prepared recovery changed a serving application head")
	}
	afterAccess, err := f.repository.CurrentAppAccess(fixture.ctx, fixture.spec.appID)
	if err != nil || !reflect.DeepEqual(beforeAccess, afterAccess) || beforeDocker != dockerDigest() {
		t.Fatal("prepared recovery changed raw grant provenance or source Docker state")
	}
	if requests := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); requests.Routed != beforeRequests.Routed {
		t.Fatal("prepared admission or recovery forwarded an application request")
	}
	assertLiveOneShot(t, fixture.ctx, options.HostPort, fixture.spec.appID, "/", fixture.spec.applicationReply, "prepared recovery changed LAN app loopback route")
	assertLiveOneShot(t, fixture.ctx, options.HostPort, f.loopbackID, "/", f.loopbackReply, "prepared recovery changed loopback-only app")
	if !liveGatewayRebindRuntimeLANBody(fixture.ctx, f.predecessorIPv4, fixture.port, fixture.spec.applicationReply) {
		t.Fatal("prepared recovery changed the original LAN publication")
	}
	wrongHost := probeGatewayV2HostStatus(fixture.ctx, f.predecessorIPv4, fixture.port, f.loopbackID+".rig.localhost", "/")
	if !wrongHost.Connected || !wrongHost.Responded || wrongHost.Status != 404 ||
		!probeGatewayRebindHandoverListenerAbsent(fixture.ctx, f.successorIPv4, fixture.port) {
		t.Fatal("prepared recovery published the successor or loopback-only app on LAN")
	}
}

func assertLiveCrossStoreRuntimeHeads(t *testing.T, inspection GatewayRebindProposalInspection,
	heads []appaccess.GatewayRebindRuntimeHead, lanAppID, loopbackAppID string,
) {
	t.Helper()
	spec := inspection.Spec
	digest, err := appaccess.GatewayRebindRuntimeHeadsV2Digest(spec.OperationID, heads)
	if err != nil || len(heads) != 2 || spec.RuntimeHeadsVersion != appaccess.GatewayRebindRuntimeHeadsVersionV1 ||
		spec.RuntimeHeadsCount != int64(len(heads)) || spec.RuntimeHeadsDigest != digest ||
		!reflect.DeepEqual(inspection.RuntimeHeads, heads) {
		t.Fatal("approval does not bind the complete runtime-head census")
	}
	seen := make(map[string]bool, len(heads))
	for _, head := range heads {
		seen[head.AppID] = true
	}
	if lanAppID == loopbackAppID || !seen[lanAppID] || !seen[loopbackAppID] {
		t.Fatal("approved runtime heads omit the LAN or loopback-only application")
	}
}

func liveRunCrossStoreAdmissionChild(t *testing.T, ctx context.Context, path, mode string) {
	t.Helper()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveGatewayRebindCrossStoreAdmissionChild$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), liveCrossStoreChildEnvironment+"="+path, liveCrossStoreChildModeEnvironment+"="+mode)
	output, err := command.CombinedOutput()
	if mode == "exit-after-claim" {
		var exited *exec.ExitError
		if !errors.As(err, &exited) || exited.ExitCode() != 91 || !bytes.Contains(output, []byte("CROSS_STORE_SQL_CLAIM_COMMITTED")) {
			t.Fatalf("child did not reach the SQL claim exit boundary: %v\n%s", err, output)
		}
	} else if err != nil || !bytes.Contains(output, []byte("CROSS_STORE_PREPARED_RECOVERED")) {
		t.Fatalf("fresh admission recovery process failed: %v\n%s", err, output)
	}
}

func TestLiveGatewayRebindCrossStoreAdmissionChild(t *testing.T) {
	path := os.Getenv(liveCrossStoreChildEnvironment)
	if path == "" {
		t.Skip("child helper requires a parent-owned fixture")
	}
	if os.Getenv("RIG_RUN_LIVE_GATEWAY_V2") != "1" || os.Getenv(liveGatewayRebindCrossStoreEnvironment) != "1" ||
		runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("child requires explicit disposable local Linux Docker permission")
	}
	body, err := os.ReadFile(path)
	var value liveCrossStoreAdmissionChild
	if err != nil || json.Unmarshal(body, &value) != nil {
		t.Fatal("read private admission fixture configuration")
	}
	input, inputErr := liveCrossStoreAdmissionChildInput(value)
	if inputErr != nil {
		t.Fatal("validate private admission fixture configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	db, err := database.Open(value.Controller.DataRoot)
	if err != nil {
		t.Fatal("reopen real SQLite in the admission child")
	}
	defer db.Close()
	repository := appaccess.New(db)
	manager, err := New(runtimeprocess.ExecRunner{}, Options{DataRoot: value.Controller.DataRoot, WorkingDirectory: value.Controller.WorkingDirectory,
		DockerExecutable: value.Controller.DockerExecutable, DockerConfigDirectory: value.Controller.DockerConfigDirectory,
		HostPort: value.Controller.HostPort, CommandTimeout: 45 * time.Second, PullTimeout: 5 * time.Minute, OutputLimit: liveDockerOutputLimit,
		RebindFenceCheck: repository.CheckGatewayRebindFence, RebindCurrentStateRepository: repository})
	if err != nil {
		t.Fatal("reopen production manager in the admission child")
	}
	mode := os.Getenv(liveCrossStoreChildModeEnvironment)
	switch mode {
	case "exit-after-claim":
		manager.gatewayRebindAfterClaim = func(context.Context, appaccess.GatewayRebindClaimV2) error {
			fmt.Println("CROSS_STORE_SQL_CLAIM_COMMITTED")
			_ = os.Stdout.Sync()
			os.Exit(91)
			return errors.New("unreachable exit boundary")
		}
		_, err = withCrossStoreFixtureEffectLocks(t, manager, func() (gatewayRebindPreparedAttempt, error) {
			return manager.prepareGatewayRebindLocked(ctx, repository, input)
		})
		t.Fatalf("prepared admission returned instead of reaching exit boundary: %v", err)
	case "recover":
		snapshot, readErr := repository.GatewayRebindRecoverySnapshot(ctx)
		if readErr != nil {
			t.Fatal("read committed prepared claim in recovery child")
		}
		_, err = withCrossStoreFixtureEffectLocks(t, manager, func() (gatewayRebindPreparedAttempt, error) {
			return manager.recoverGatewayRebindPreparedAdmissionLocked(ctx, repository, snapshot)
		})
		if err != nil {
			failLiveIngress(t, "recover prepared admission in fresh process", err)
		}
		fmt.Println("CROSS_STORE_PREPARED_RECOVERED")
	default:
		t.Fatal("unknown admission child mode")
	}
}
