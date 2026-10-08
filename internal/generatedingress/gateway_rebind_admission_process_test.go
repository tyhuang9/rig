package generatedingress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	gatewayRebindAdmissionProcessModeEnvironment     = "RIG_TEST_GATEWAY_REBIND_ADMISSION_PROCESS_MODE"
	gatewayRebindAdmissionProcessRootEnvironment     = "RIG_TEST_GATEWAY_REBIND_ADMISSION_PROCESS_ROOT"
	gatewayRebindAdmissionProcessManifestEnvironment = "RIG_TEST_GATEWAY_REBIND_ADMISSION_PROCESS_MANIFEST"
)

type gatewayRebindAdmissionProcessRepository struct {
	*appaccess.Repository
	entered      chan<- struct{}
	delegating   chan<- struct{}
	continueC    <-chan struct{}
	enterOnce    sync.Once
	delegateOnce sync.Once
	claimErr     error
}

func (r *gatewayRebindAdmissionProcessRepository) ClaimGatewayRebindV2(ctx context.Context,
	proposal appaccess.GatewayRebindPreclaimProposalV2,
) (appaccess.GatewayRebindClaimV2, bool, error) {
	r.enterOnce.Do(func() { close(r.entered) })
	select {
	case <-ctx.Done():
	case <-r.continueC:
	}
	r.delegateOnce.Do(func() { close(r.delegating) })
	claim, created, err := r.Repository.ClaimGatewayRebindV2(ctx, proposal)
	r.claimErr = err
	return claim, created, err
}

type gatewayRebindAdmissionProcessManifest struct {
	DataRoot              string                              `json:"dataRoot"`
	WorkingDirectory      string                              `json:"workingDirectory"`
	DockerExecutable      string                              `json:"dockerExecutable"`
	DockerConfigDirectory string                              `json:"dockerConfigDirectory"`
	HostPort              uint16                              `json:"hostPort"`
	Input                 gatewayRebindCommitInput            `json:"input"`
	PredecessorSubnet     string                              `json:"predecessorSubnet"`
	Mutation              generatedruntime.RouteSwitchRequest `json:"mutation"`
	PinnedTopology        gatewayObservedTopology             `json:"pinnedTopology"`
}

type gatewayRebindAdmissionProcessCommand struct {
	command  *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Scanner
	stderr   bytes.Buffer
	waitDone chan struct{}
	waitErr  error
	waitOnce sync.Once
	killOnce sync.Once
	killErr  error
}

func TestGatewayRebindAdmissionProcessRejectsBoundaryWork(t *testing.T) {
	f := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f.manager.options.RebindFenceCheck = f.repository.CheckGatewayRebindFence
	inspection := gatewayRebindAdmissionProcessInspection(t, f)
	completeCrossStoreRepositoryFixture(t, f, inspection.Roster[0])
	beforeSQL, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	beforeCommands := len(f.runner.commands)

	entered, delegating, continueClaim := make(chan struct{}), make(chan struct{}), make(chan struct{})
	repository := &gatewayRebindAdmissionProcessRepository{
		Repository: f.repository, entered: entered, delegating: delegating, continueC: continueClaim,
	}
	input := gatewayRebindAdmissionProcessInput(inspection)
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, f.manager.options.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	releaseGateway, err := f.manager.lockGatewayRaw(ctx)
	if err != nil {
		_ = releaseEffects()
		cancel()
		t.Fatal(err)
	}
	claimDone := make(chan error, 1)
	claimFinished := false
	var continueOnce sync.Once
	defer func() {
		cancel()
		continueOnce.Do(func() { close(continueClaim) })
		if !claimFinished {
			select {
			case <-claimDone:
			case <-time.After(10 * time.Second):
				t.Errorf("claim goroutine did not stop before lock release")
			}
		}
		if releaseErr := errors.Join(releaseGateway(), releaseEffects()); releaseErr != nil {
			t.Errorf("release boundary admission locks: %v", releaseErr)
		}
	}()
	go func() {
		_, prepareErr := f.manager.prepareGatewayRebindLocked(ctx, repository, input)
		claimDone <- prepareErr
	}()
	gatewayRebindAdmissionAwaitSignal(t, ctx, entered, "claim did not reach the real repository boundary")

	writer := gatewayRebindAdmissionStartProcess(t, "boundary-writer", f.manager.options.DataRoot, "")
	gatewayRebindAdmissionReadMarker(t, ctx, writer, "BOUNDARY_WRITER_READY")
	continueOnce.Do(func() { close(continueClaim) })
	gatewayRebindAdmissionAwaitSignal(t, ctx, delegating, "claim wrapper did not delegate to the real repository")
	select {
	case prepareErr := <-claimDone:
		claimFinished = true
		t.Fatalf("real claim returned while the competing BEGIN IMMEDIATE transaction remained open: %v", prepareErr)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := writer.stdin.Write([]byte{'c'}); err != nil {
		t.Fatalf("release boundary writer: %v", err)
	}
	if err := writer.stdin.Close(); err != nil {
		t.Fatalf("close boundary writer control: %v", err)
	}
	gatewayRebindAdmissionReadMarker(t, ctx, writer, "BOUNDARY_WRITER_COMMITTED")
	gatewayRebindAdmissionWaitSuccess(t, ctx, writer)

	select {
	case prepareErr := <-claimDone:
		claimFinished = true
		if prepareErr == nil || !errors.Is(repository.claimErr, appaccess.ErrGatewayRebindNotQuiescent) {
			t.Fatalf("claim crossed committed boundary work: prepare=%v claim=%v", prepareErr, repository.claimErr)
		}
	case <-ctx.Done():
		t.Fatal("claim did not finish after the competing writer committed")
	}
	afterSQL, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(beforeSQL, afterSQL) || f.repository.CheckGatewayRebindFence(ctx) != nil {
		t.Fatalf("rejected boundary work changed gateway SQL authority: %v", err)
	}
	census, err := f.repository.GatewayRebindQuiescenceCensus(ctx)
	if !errors.Is(err, appaccess.ErrGatewayRebindNotQuiescent) ||
		census.Jobs.Queued != 1 || census.Deployments.Preparing != 1 {
		t.Fatalf("actual committed boundary work missing from census: %#v error=%v", census, err)
	}
	afterFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil || !sameGatewayHistorySnapshot(beforeFiles, afterFiles) || len(f.runner.commands) != beforeCommands {
		t.Fatalf("rejected boundary work caused a protected or external effect: files=%v commands=%d", err, len(f.runner.commands)-beforeCommands)
	}
}

func TestGatewayRebindAdmissionProcessSerializesContenders(t *testing.T) {
	f := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f.manager.options.RebindFenceCheck = f.repository.CheckGatewayRebindFence
	inspection := gatewayRebindAdmissionProcessInspection(t, f)
	completeCrossStoreRepositoryFixture(t, f, inspection.Roster[0])
	beforeSQL, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}

	entered, delegating, continueClaim := make(chan struct{}), make(chan struct{}), make(chan struct{})
	holderContext, cancelHolder := context.WithCancel(ctx)
	holderFinished := false
	var continueOnce sync.Once
	repository := &gatewayRebindAdmissionProcessRepository{
		Repository: f.repository, entered: entered, delegating: delegating, continueC: continueClaim,
	}
	holderDone := make(chan error, 1)
	defer func() {
		cancelHolder()
		continueOnce.Do(func() { close(continueClaim) })
		if !holderFinished {
			select {
			case <-holderDone:
			case <-time.After(10 * time.Second):
				t.Errorf("preclaim holder did not stop before test cleanup")
			}
		}
	}()
	go func() {
		releaseEffects, acquireErr := gatewayRebindAcquireDeploymentEffects(holderContext, f.manager.options.WorkingDirectory)
		if acquireErr != nil {
			holderDone <- acquireErr
			return
		}
		releaseGateway, lockErr := f.manager.lockGatewayRaw(holderContext)
		if lockErr != nil {
			holderDone <- errors.Join(lockErr, releaseEffects())
			return
		}
		_, prepareErr := f.manager.prepareGatewayRebindLocked(holderContext, repository,
			gatewayRebindAdmissionProcessInput(inspection))
		holderDone <- errors.Join(prepareErr, releaseGateway(), releaseEffects())
	}()
	gatewayRebindAdmissionAwaitSignal(t, ctx, entered, "preclaim holder did not reach claim creation")

	appRoute := f.state.Apps[inspection.Roster[0].AppID]
	manifest := gatewayRebindAdmissionProcessManifest{
		DataRoot: f.manager.options.DataRoot, WorkingDirectory: f.manager.options.WorkingDirectory,
		DockerExecutable: f.manager.options.DockerExecutable, DockerConfigDirectory: f.manager.options.DockerConfigDirectory,
		HostPort: f.manager.options.HostPort, PinnedTopology: gatewayTopologyExactFinalV2,
		Mutation: generatedruntime.RouteSwitchRequest{AppID: inspection.Roster[0].AppID,
			ToSlot: appRoute.Route.Slot, Endpoints: append([]generatedruntime.RouteEndpoint(nil), appRoute.Route.Endpoints...)},
	}
	manifestPath := gatewayRebindAdmissionWriteManifest(t, f.manager.options.DataRoot, "mutation.json", manifest)
	blocked := gatewayRebindAdmissionStartProcess(t, "mutation-blocked", "", manifestPath)
	gatewayRebindAdmissionReadMarker(t, ctx, blocked, "MUTATION_ATTEMPTED")
	gatewayRebindAdmissionReadMarker(t, ctx, blocked, "MUTATION_BLOCKED")
	gatewayRebindAdmissionWaitSuccess(t, ctx, blocked)

	duringSQL, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	duringFiles, filesErr := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil || filesErr != nil || !reflect.DeepEqual(beforeSQL, duringSQL) ||
		!sameGatewayHistorySnapshot(beforeFiles, duringFiles) || f.repository.CheckGatewayRebindFence(ctx) != nil {
		t.Fatal("contended ordinary mutation moved SQL, protected state, or the fence")
	}

	// Cancel before allowing the wrapper to delegate. The real repository sees
	// the canceled context, so this positive control has no prepared SQL claim.
	cancelHolder()
	continueOnce.Do(func() { close(continueClaim) })
	gatewayRebindAdmissionAwaitSignal(t, ctx, delegating, "canceled holder did not delegate to the real repository")
	select {
	case holderErr := <-holderDone:
		holderFinished = true
		if holderErr == nil || repository.claimErr == nil {
			t.Fatalf("canceled preclaim holder unexpectedly created a claim: holder=%v claim=%v", holderErr, repository.claimErr)
		}
	case <-ctx.Done():
		t.Fatal("canceled preclaim holder did not release its real lock chain")
	}
	if snapshot, err := f.repository.GatewayRebindRecoverySnapshot(ctx); err != nil ||
		!reflect.DeepEqual(beforeSQL, snapshot) || f.repository.CheckGatewayRebindFence(ctx) != nil {
		t.Fatal("canceled preclaim holder left an active claim")
	}

	viable := gatewayRebindAdmissionStartProcess(t, "mutation-success", "", manifestPath)
	gatewayRebindAdmissionReadMarker(t, ctx, viable, "MUTATION_ATTEMPTED")
	gatewayRebindAdmissionReadMarker(t, ctx, viable, "MUTATION_SUCCEEDED")
	gatewayRebindAdmissionWaitSuccess(t, ctx, viable)
	afterSQL, sqlErr := f.repository.GatewayRebindRecoverySnapshot(ctx)
	afterFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil || sqlErr != nil || !reflect.DeepEqual(beforeSQL, afterSQL) ||
		!sameGatewayHistorySnapshot(beforeFiles, afterFiles) || f.repository.CheckGatewayRebindFence(ctx) != nil {
		t.Fatal("viable no-op mutation changed SQL, protected predecessor bytes, or the fence")
	}
}

func TestGatewayRebindAdmissionProcessRecoversCommittedClaim(t *testing.T) {
	f := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	f.manager.options.RebindFenceCheck = f.repository.CheckGatewayRebindFence
	inspection := gatewayRebindAdmissionProcessInspection(t, f)
	completeCrossStoreRepositoryFixture(t, f, inspection.Roster[0])
	beforeSQL, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	beforeCommands := len(f.runner.commands)
	manifest := gatewayRebindAdmissionProcessManifest{
		DataRoot: f.manager.options.DataRoot, WorkingDirectory: f.manager.options.WorkingDirectory,
		DockerExecutable: f.manager.options.DockerExecutable, DockerConfigDirectory: f.manager.options.DockerConfigDirectory,
		HostPort: f.manager.options.HostPort, Input: gatewayRebindAdmissionProcessInput(inspection),
		PredecessorSubnet: f.state.Network.Subnet, PinnedTopology: gatewayTopologyExactFinalV2,
	}
	manifestPath := gatewayRebindAdmissionWriteManifest(t, f.manager.options.DataRoot, "recovery.json", manifest)
	crashed := gatewayRebindAdmissionStartProcess(t, "crash-after-claim", "", manifestPath)
	gatewayRebindAdmissionReadMarker(t, ctx, crashed, "SQL_CLAIM_COMMITTED")
	committed, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || committed.Active == nil || committed.Active.Claim.V2 == nil ||
		committed.Active.Claim.V2.Spec.OperationID != inspection.Spec.OperationID ||
		committed.Phase != appaccess.GatewayRebindPrepared || !committed.RollbackAllowed || committed.DatabaseCommitObserved ||
		!reflect.DeepEqual(committed.CurrentSource, beforeSQL.CurrentSource) ||
		!errors.Is(f.repository.CheckGatewayRebindFence(ctx), appaccess.ErrGatewayRebindActive) {
		t.Fatalf("child signal did not follow the exact prepared SQL barrier: %v", err)
	}
	postClaimFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil || !sameGatewayHistorySnapshot(beforeFiles, postClaimFiles) {
		t.Fatal("post-claim child installed protected successor evidence before its signal")
	}
	gatewayRebindAdmissionKillAndReap(t, crashed)
	appRoute := f.state.Apps[inspection.Roster[0].AppID]
	err = f.manager.Switch(ctx, generatedruntime.RouteSwitchRequest{AppID: inspection.Roster[0].AppID,
		ToSlot: appRoute.Route.Slot, Endpoints: append([]generatedruntime.RouteEndpoint(nil), appRoute.Route.Endpoints...)})
	if err == nil || !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("ordinary mutation crossed the committed prepared fence: %v", err)
	}
	rejectedFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil || !sameGatewayHistorySnapshot(postClaimFiles, rejectedFiles) || len(f.runner.commands) != beforeCommands {
		t.Fatal("rejected ordinary mutation changed protected bytes or attempted an external effect")
	}

	gatewayRebindAdmissionRunRecovery(t, ctx, manifestPath)
	recoveredFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Checkpoints) != 1 || len(history.IntentsV2) != 1 || len(history.Progress) != 1 ||
		len(history.Terminals) != 0 || history.Progress[0].Record.Sequence != 1 ||
		history.Progress[0].Record.Phase != gatewayRebindProgressSuccessorIntent ||
		history.Checkpoints[0].Checkpoint.Digest != inspection.PredecessorCheckpointDigest ||
		history.IntentsV2[0].Intent.Predecessor != inspection.Spec.Predecessor {
		t.Fatalf("fresh recovery did not reconstruct the exact prepared checkpoint: %v", err)
	}
	retained := gatewayHistorySnapshot{files: make(map[string]gatewayHistoryFileFingerprint)}
	for name := range beforeFiles.files {
		if value, ok := recoveredFiles.files[name]; ok {
			retained.files[name] = value
		}
	}
	if !sameGatewayHistorySnapshot(beforeFiles, retained) {
		t.Fatal("fresh recovery rewrote predecessor protected bytes")
	}

	gatewayRebindAdmissionRunRecovery(t, ctx, manifestPath)
	replayedFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	finalSQL, sqlErr := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || sqlErr != nil || !sameGatewayHistorySnapshot(recoveredFiles, replayedFiles) ||
		!reflect.DeepEqual(committed, finalSQL) || !errors.Is(f.repository.CheckGatewayRebindFence(ctx), appaccess.ErrGatewayRebindActive) ||
		len(f.runner.commands) != beforeCommands {
		t.Fatal("prepared replay changed SQL/protected bytes, released the fence, or caused an external effect")
	}
}

func TestGatewayRebindAdmissionProcessHelper(t *testing.T) {
	mode := os.Getenv(gatewayRebindAdmissionProcessModeEnvironment)
	if mode == "" {
		return
	}
	var err error
	switch mode {
	case "boundary-writer":
		err = gatewayRebindAdmissionProcessBoundaryWriter(os.Getenv(gatewayRebindAdmissionProcessRootEnvironment))
	case "mutation-blocked", "mutation-success":
		err = gatewayRebindAdmissionProcessMutation(os.Getenv(gatewayRebindAdmissionProcessManifestEnvironment), mode)
	case "crash-after-claim":
		err = gatewayRebindAdmissionProcessCrashAfterClaim(os.Getenv(gatewayRebindAdmissionProcessManifestEnvironment))
	case "recover":
		err = gatewayRebindAdmissionProcessRecover(os.Getenv(gatewayRebindAdmissionProcessManifestEnvironment))
	default:
		err = errors.New("unknown gateway rebind admission process mode")
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func gatewayRebindAdmissionProcessInspection(t *testing.T, f gatewayRebindPredecessorFixture) GatewayRebindProposalInspection {
	t.Helper()
	inspection, err := f.manager.InspectGatewayRebindProposal(context.Background(), f.repository, GatewayRebindProposalInput{
		OperationID: f.proposal.Spec.OperationID, SuccessorProfileRevisionID: f.proposal.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: f.proposal.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    f.proposal.Spec.SuccessorProfileOperationID, SuccessorProfile: f.proposal.Spec.SuccessorProfile,
	})
	if err != nil || len(inspection.Roster) != 1 {
		t.Fatalf("inspect real admission proposal: %v", err)
	}
	return inspection
}

func gatewayRebindAdmissionProcessInput(inspection GatewayRebindProposalInspection) gatewayRebindCommitInput {
	return gatewayRebindCommitInput{Inspection: inspection,
		RebindApproval: appaccess.Approval{Action: appaccess.ActionRebindGateway,
			SpecDigest: inspection.SpecDigest, ActorID: gatewayRebindTestAdministrator},
		ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
			SpecDigest: inspection.SuccessorProfileSpecDigest, ActorID: gatewayRebindTestAdministrator}}
}

func gatewayRebindAdmissionProcessBoundaryWriter(root string) (resultErr error) {
	if !gatewayRebindAdmissionSafeAbsolutePath(root) {
		return errors.New("boundary writer data root is invalid")
	}
	db, err := database.Open(root)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	connection, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, rollbackErr := connection.ExecContext(context.Background(), `ROLLBACK`)
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()
	var appID, releaseID, planID string
	var planRevision int64
	if err := connection.QueryRowContext(ctx, `SELECT d.app_id,d.release_id,d.deployment_plan_revision_id,d.deployment_plan_revision_number
		FROM deployments d ORDER BY d.id LIMIT 1`).Scan(&appID, &releaseID, &planID, &planRevision); err != nil {
		return err
	}
	jobID, deploymentID := uuid.NewString(), uuid.NewString()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := connection.ExecContext(ctx, `INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,created_at,updated_at)
		VALUES(?,'deploy','application',?,'queued','queued',?,?)`, jobID, appID, stamp, stamp); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO deployments(
		id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,?,?,'preparing','current',1,'generated_node',?,?)`,
		deploymentID, appID, releaseID, jobID, planID, planRevision); err != nil {
		return err
	}
	if err := gatewayRebindAdmissionPrintMarker("BOUNDARY_WRITER_READY"); err != nil {
		return err
	}
	control := make([]byte, 1)
	if _, err := io.ReadFull(os.Stdin, control); err != nil || control[0] != 'c' {
		return errors.New("boundary writer continue signal is invalid")
	}
	if _, err := connection.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return gatewayRebindAdmissionPrintMarker("BOUNDARY_WRITER_COMMITTED")
}

func gatewayRebindAdmissionProcessMutation(path, mode string) error {
	manifest, err := gatewayRebindAdmissionReadManifest(path)
	if err != nil || manifest.PinnedTopology != gatewayTopologyExactFinalV2 ||
		manifest.Mutation.AppID == "" || len(manifest.Mutation.Endpoints) == 0 {
		return errors.New("mutation process manifest is invalid")
	}
	db, err := database.Open(manifest.DataRoot)
	if err != nil {
		return err
	}
	defer db.Close()
	repository := appaccess.New(db)
	runner := &gatewayRebindAdmissionNoEffectRunner{}
	manager, err := New(runner, gatewayRebindAdmissionOptions(manifest, repository))
	if err != nil {
		return err
	}
	manager.gatewayTopologyObserver = func(context.Context, routeState, gatewayV2RouteState,
		gatewayMigrationJournal,
	) gatewayObservedTopology {
		return manifest.PinnedTopology
	}
	if err := gatewayRebindAdmissionPrintMarker("MUTATION_ATTEMPTED"); err != nil {
		return err
	}
	timeout := 20 * time.Second
	if mode == "mutation-blocked" {
		timeout = 250 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err = manager.Switch(ctx, manifest.Mutation)
	if mode == "mutation-blocked" {
		if err == nil || !IsCode(err, DiagnosticCancelled) || runner.calls.Load() != 0 {
			return fmt.Errorf("ordinary mutation was not blocked by the claim lock chain: %w", err)
		}
		return gatewayRebindAdmissionPrintMarker("MUTATION_BLOCKED")
	}
	if err != nil || runner.calls.Load() != 0 {
		return fmt.Errorf("ordinary mutation did not succeed after normal release: %w", err)
	}
	return gatewayRebindAdmissionPrintMarker("MUTATION_SUCCEEDED")
}

func gatewayRebindAdmissionProcessCrashAfterClaim(path string) error {
	manifest, err := gatewayRebindAdmissionReadManifest(path)
	if err != nil || manifest.Input.Inspection.Spec.OperationID == "" {
		return errors.New("crash process manifest is invalid")
	}
	db, err := database.Open(manifest.DataRoot)
	if err != nil {
		return err
	}
	repository := appaccess.New(db)
	runner := &gatewayRebindAdmissionNoEffectRunner{}
	manager, err := New(runner, gatewayRebindAdmissionOptions(manifest, repository))
	if err != nil {
		return err
	}
	manager.gatewayTopologyObserver = func(context.Context, routeState, gatewayV2RouteState,
		gatewayMigrationJournal,
	) gatewayObservedTopology {
		return manifest.PinnedTopology
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, manifest.WorkingDirectory)
	if err != nil {
		return err
	}
	releaseGateway, err := manager.lockGatewayRaw(ctx)
	if err != nil {
		return errors.Join(err, releaseEffects(), db.Close())
	}
	manager.gatewayRebindAfterClaim = func(hookContext context.Context, claim appaccess.GatewayRebindClaimV2) error {
		snapshot, readErr := repository.GatewayRebindRecoverySnapshot(hookContext)
		if readErr != nil || runner.calls.Load() != 0 || snapshot.Active == nil || snapshot.Active.Claim.V2 == nil ||
			snapshot.Active.Claim.V2.RequestDigest != claim.RequestDigest || snapshot.Phase != appaccess.GatewayRebindPrepared ||
			!errors.Is(repository.CheckGatewayRebindFence(hookContext), appaccess.ErrGatewayRebindActive) {
			return errors.New("prepared SQL barrier is not durable")
		}
		if markerErr := gatewayRebindAdmissionPrintMarker("SQL_CLAIM_COMMITTED"); markerErr != nil {
			return markerErr
		}
		control := make([]byte, 1)
		if _, readControlErr := os.Stdin.Read(control); readControlErr != nil {
			return errors.New("crash parent ended control channel without killing child")
		}
		return errors.New("crash parent continued a kill-only child")
	}
	_, err = manager.prepareGatewayRebindLocked(ctx, repository, manifest.Input)
	releaseErr := errors.Join(releaseGateway(), releaseEffects(), db.Close())
	return fmt.Errorf("crash boundary returned: %w", errors.Join(err, releaseErr))
}

func gatewayRebindAdmissionProcessRecover(path string) error {
	manifest, err := gatewayRebindAdmissionReadManifest(path)
	if err != nil {
		return err
	}
	db, err := database.Open(manifest.DataRoot)
	if err != nil {
		return err
	}
	defer db.Close()
	repository := appaccess.New(db)
	runner := &gatewayRebindAdmissionNoEffectRunner{}
	manager, err := New(runner, gatewayRebindAdmissionOptions(manifest, repository))
	if err != nil {
		return err
	}
	manager.gatewayTopologyObserver = func(context.Context, routeState, gatewayV2RouteState,
		gatewayMigrationJournal,
	) gatewayObservedTopology {
		return manifest.PinnedTopology
	}
	installCrossStoreFixtureNetworkObserver(manager, manifest.PredecessorSubnet)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, manifest.WorkingDirectory)
	if err != nil {
		return err
	}
	releaseGateway, err := manager.lockGatewayRaw(ctx)
	if err != nil {
		return errors.Join(err, releaseEffects())
	}
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		return err
	}
	attempt, err := manager.recoverGatewayRebindPreparedAdmissionLocked(ctx, repository, snapshot)
	if err != nil || runner.calls.Load() != 0 || attempt.Progress.Sequence != 1 || attempt.Progress.Phase != gatewayRebindProgressSuccessorIntent ||
		attempt.Intent.Predecessor != attempt.Claim.Spec.Predecessor || attempt.Checkpoint.Digest != attempt.Claim.Spec.Predecessor.PredecessorCheckpointDigest {
		return errors.Join(fmt.Errorf("recover exact prepared admission: %w", err), releaseGateway(), releaseEffects())
	}
	if releaseErr := errors.Join(releaseGateway(), releaseEffects()); releaseErr != nil {
		return fmt.Errorf("release recovered admission locks: %w", releaseErr)
	}
	return gatewayRebindAdmissionPrintMarker("PREPARED_RECOVERED")
}

type gatewayRebindAdmissionNoEffectRunner struct{ calls atomic.Int64 }

func (r *gatewayRebindAdmissionNoEffectRunner) Run(context.Context, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.calls.Add(1)
	return runtimeprocess.CommandResult{}, errors.New("gateway rebind admission process attempted an external effect")
}

func gatewayRebindAdmissionOptions(manifest gatewayRebindAdmissionProcessManifest,
	repository *appaccess.Repository,
) Options {
	return Options{DataRoot: manifest.DataRoot, WorkingDirectory: manifest.WorkingDirectory,
		DockerExecutable: manifest.DockerExecutable, DockerConfigDirectory: manifest.DockerConfigDirectory,
		HostPort: manifest.HostPort, CommandTimeout: time.Second, PullTimeout: time.Second, OutputLimit: 4096,
		RebindFenceCheck: repository.CheckGatewayRebindFence, RebindCurrentStateRepository: repository}
}

func gatewayRebindAdmissionWriteManifest(t *testing.T, root, name string,
	manifest gatewayRebindAdmissionProcessManifest,
) string {
	t.Helper()
	path := filepath.Join(root, name)
	body, err := json.Marshal(manifest)
	if err != nil || len(body) > 1<<20 || os.WriteFile(path, body, 0o600) != nil {
		t.Fatalf("write private process manifest: %v", err)
	}
	return path
}

func gatewayRebindAdmissionReadManifest(path string) (gatewayRebindAdmissionProcessManifest, error) {
	var manifest gatewayRebindAdmissionProcessManifest
	if !gatewayRebindAdmissionSafeAbsolutePath(path) || filepath.Ext(path) != ".json" {
		return manifest, errors.New("process manifest path is invalid")
	}
	body, err := os.ReadFile(path)
	if err != nil || len(body) == 0 || len(body) > 1<<20 || json.Unmarshal(body, &manifest) != nil ||
		!gatewayRebindAdmissionSafeAbsolutePath(manifest.DataRoot) ||
		!gatewayRebindAdmissionSafeAbsolutePath(manifest.WorkingDirectory) ||
		!gatewayRebindAdmissionSafeAbsolutePath(manifest.DockerConfigDirectory) ||
		!gatewayRebindAdmissionSafeAbsolutePath(manifest.DockerExecutable) || manifest.HostPort == 0 {
		return gatewayRebindAdmissionProcessManifest{}, errors.New("process manifest is invalid")
	}
	if manifest.PredecessorSubnet != "" {
		prefix, prefixErr := netip.ParsePrefix(manifest.PredecessorSubnet)
		if prefixErr != nil || prefix != prefix.Masked() {
			return gatewayRebindAdmissionProcessManifest{}, errors.New("process manifest predecessor subnet is invalid")
		}
	}
	for index := range manifest.Input.Inspection.Roster {
		digest, digestErr := appaccess.GatewayRebindRosterEntryV2Digest(manifest.Input.Inspection.Roster[index])
		if digestErr != nil {
			return gatewayRebindAdmissionProcessManifest{}, errors.New("process manifest roster is invalid")
		}
		manifest.Input.Inspection.Roster[index].EntryDigest = digest
	}
	return manifest, nil
}

func gatewayRebindAdmissionSafeAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func gatewayRebindAdmissionStartProcess(t *testing.T, mode, root, manifest string) *gatewayRebindAdmissionProcessCommand {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestGatewayRebindAdmissionProcessHelper$", "-test.count=1")
	command.Env = append(os.Environ(), gatewayRebindAdmissionProcessModeEnvironment+"="+mode)
	if root != "" {
		command.Env = append(command.Env, gatewayRebindAdmissionProcessRootEnvironment+"="+root)
	}
	if manifest != "" {
		command.Env = append(command.Env, gatewayRebindAdmissionProcessManifestEnvironment+"="+manifest)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	process := &gatewayRebindAdmissionProcessCommand{
		command: command, stdin: stdin, stdout: bufio.NewScanner(stdout), waitDone: make(chan struct{}),
	}
	command.Stderr = &process.stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start %s child: %v", mode, err)
	}
	t.Cleanup(func() {
		_ = process.stdin.Close()
		_ = process.kill()
		process.startWait()
		select {
		case <-process.waitDone:
		case <-time.After(10 * time.Second):
			t.Errorf("%s child was not reaped during cleanup", mode)
		}
	})
	return process
}

func (p *gatewayRebindAdmissionProcessCommand) kill() error {
	p.killOnce.Do(func() {
		p.killErr = p.command.Process.Kill()
	})
	return p.killErr
}

func (p *gatewayRebindAdmissionProcessCommand) startWait() {
	p.waitOnce.Do(func() {
		go func() {
			p.waitErr = p.command.Wait()
			close(p.waitDone)
		}()
	})
}

func gatewayRebindAdmissionReadMarker(t *testing.T, ctx context.Context,
	process *gatewayRebindAdmissionProcessCommand, want string,
) {
	t.Helper()
	result := make(chan string, 1)
	go func() {
		if process.stdout.Scan() {
			result <- process.stdout.Text()
			return
		}
		result <- ""
	}()
	select {
	case got := <-result:
		if got != want {
			_ = process.kill()
			gatewayRebindAdmissionAwaitProcess(t, process, 10*time.Second, "marker failure")
			t.Fatalf("child marker=%q want=%q scan=%v stderr=%q", got, want, process.stdout.Err(), process.stderr.String())
		}
	case <-ctx.Done():
		_ = process.kill()
		gatewayRebindAdmissionAwaitProcess(t, process, 10*time.Second, "marker timeout")
		t.Fatalf("timed out waiting for child marker %q", want)
	}
}

func gatewayRebindAdmissionWaitSuccess(t *testing.T, ctx context.Context, process *gatewayRebindAdmissionProcessCommand) {
	t.Helper()
	process.startWait()
	select {
	case <-process.waitDone:
		if process.waitErr != nil {
			t.Fatalf("child failed: %v stderr=%q", process.waitErr, process.stderr.String())
		}
	case <-ctx.Done():
		_ = process.kill()
		gatewayRebindAdmissionAwaitProcess(t, process, 10*time.Second, "success timeout")
		t.Fatalf("timed out waiting for successful child: %v stderr=%q", ctx.Err(), process.stderr.String())
	}
}

func gatewayRebindAdmissionKillAndReap(t *testing.T, process *gatewayRebindAdmissionProcessCommand) {
	t.Helper()
	if err := process.kill(); err != nil {
		t.Fatalf("kill owned admission child: %v", err)
	}
	gatewayRebindAdmissionAwaitProcess(t, process, 10*time.Second, "intentional kill")
	err := process.waitErr
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || !gatewayRebindAdmissionWasProcessKill(process.command.ProcessState) {
		t.Fatalf("child did not exit through the owned OS kill: %v state=%v stderr=%q",
			err, process.command.ProcessState, process.stderr.String())
	}
}

func gatewayRebindAdmissionWasProcessKill(state *os.ProcessState) bool {
	if state == nil {
		return false
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	if runtime.GOOS == "windows" {
		return !status.Signaled() && status.ExitStatus() == 1
	}
	return status.Signaled() && status.Signal() == syscall.Signal(9)
}

func gatewayRebindAdmissionRunRecovery(t *testing.T, ctx context.Context, manifest string) {
	t.Helper()
	process := gatewayRebindAdmissionStartProcess(t, "recover", "", manifest)
	gatewayRebindAdmissionReadMarker(t, ctx, process, "PREPARED_RECOVERED")
	gatewayRebindAdmissionWaitSuccess(t, ctx, process)
}

func gatewayRebindAdmissionAwaitProcess(t *testing.T, process *gatewayRebindAdmissionProcessCommand,
	timeout time.Duration, action string,
) {
	t.Helper()
	process.startWait()
	select {
	case <-process.waitDone:
	case <-time.After(timeout):
		t.Fatalf("child was not reaped after %s", action)
	}
}

func gatewayRebindAdmissionPrintMarker(marker string) error {
	if marker == "" || strings.ContainsAny(marker, "\r\n") {
		return errors.New("process marker is invalid")
	}
	if _, err := fmt.Fprintln(os.Stdout, marker); err != nil {
		return err
	}
	return nil
}

func gatewayRebindAdmissionAwaitSignal(t *testing.T, ctx context.Context, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal(message)
	}
}

var _ gatewayRebindAdmissionRepository = (*gatewayRebindAdmissionProcessRepository)(nil)
