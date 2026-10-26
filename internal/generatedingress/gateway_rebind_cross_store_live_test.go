package generatedingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
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
	manager := fixture.ingress
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
	options := manager.options
	child := liveCrossStoreAdmissionChild{Controller: liveFinalHandoverChild{
		DataRoot: options.DataRoot, WorkingDirectory: options.WorkingDirectory,
		DockerExecutable: options.DockerExecutable, DockerConfigDirectory: options.DockerConfigDirectory,
		HostPort: options.HostPort,
	}, Input: gatewayRebindCommitInput{Inspection: inspection}}
	path := filepath.Join(fixture.root, liveCrossStoreProposalReplayManifestName)
	if err := writeLiveCrossStoreProposalReplayManifest(path, child); err != nil {
		t.Fatal("write private proposal replay manifest")
	}
	liveRunCrossStoreProposalReplayChild(t, fixture.ctx, path)
	afterCurrent, err := manager.InspectGatewayRebindCurrent(fixture.ctx, f.repository)
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

const (
	liveCrossStoreProposalReplayMode                    = "proposal-replay"
	liveCrossStoreProposalReplayManifestName            = "cross-store-proposal-replay.json"
	liveCrossStoreProposalReplaySuccessMarker           = "CROSS_STORE_PROPOSAL_REPLAY_OK:no-effects"
	liveCrossStoreProposalReplayFailurePrefix           = "CROSS_STORE_PROPOSAL_REPLAY_FAIL:"
	liveCrossStoreProposalReplayCancellationEnvironment = "RIG_TEST_CROSS_STORE_PROPOSAL_REPLAY_CANCEL"
	liveCrossStoreProposalReplayCancellationMarker      = "CROSS_STORE_PROPOSAL_REPLAY_CANCEL_STARTED"
	liveCrossStoreProposalReplayOutputLimit             = 64 << 10
)

type liveCrossStoreAdmissionChild struct {
	Controller liveFinalHandoverChild
	Input      gatewayRebindCommitInput
}

func liveCrossStoreAdmissionChildModeAllowed(mode string) bool {
	switch mode {
	case "exit-after-claim", "recover", liveCrossStoreProposalReplayMode:
		return true
	default:
		return false
	}
}

func validLiveCrossStoreProposalReplayController(value liveFinalHandoverChild, manifestPath string) bool {
	if manifestPath == "" || !filepath.IsAbs(manifestPath) || filepath.Clean(manifestPath) != manifestPath ||
		filepath.Base(manifestPath) != liveCrossStoreProposalReplayManifestName || value.HostPort == 0 ||
		value.DataRoot == "" || !filepath.IsAbs(value.DataRoot) || filepath.Clean(value.DataRoot) != value.DataRoot ||
		value.WorkingDirectory == "" || !filepath.IsAbs(value.WorkingDirectory) || filepath.Clean(value.WorkingDirectory) != value.WorkingDirectory ||
		value.DockerConfigDirectory == "" || !filepath.IsAbs(value.DockerConfigDirectory) || filepath.Clean(value.DockerConfigDirectory) != value.DockerConfigDirectory ||
		value.DockerExecutable == "" || !filepath.IsAbs(value.DockerExecutable) || filepath.Clean(value.DockerExecutable) != value.DockerExecutable {
		return false
	}
	root := filepath.Dir(manifestPath)
	runtimeRoot := filepath.Join(value.DataRoot, "runtime")
	builderRoot := filepath.Join(runtimeRoot, "generated-builder")
	if value.DataRoot != filepath.Join(root, "ingress-state") ||
		value.WorkingDirectory != filepath.Join(builderRoot, "controller-working") ||
		value.DockerConfigDirectory != filepath.Join(builderRoot, "controller-docker-config") {
		return false
	}
	for _, directory := range []string{root, value.DataRoot, runtimeRoot, builderRoot, value.WorkingDirectory, value.DockerConfigDirectory} {
		if !liveGatewayRebindStoppedStageExactDirectory(directory) {
			return false
		}
	}
	docker, err := os.Stat(value.DockerExecutable)
	return err == nil && docker.Mode().IsRegular()
}

func rejectLiveCrossStoreProposalReplayDuplicateJSON(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var readValue func() error
	readValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				key, keyOK := keyToken.(string)
				if keyErr != nil || !keyOK {
					return errors.New("proposal replay manifest key is invalid")
				}
				if _, exists := seen[key]; exists {
					return errors.New("proposal replay manifest key is duplicated")
				}
				seen[key] = struct{}{}
				if err := readValue(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("proposal replay manifest object is invalid")
			}
		case '[':
			for decoder.More() {
				if err := readValue(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("proposal replay manifest array is invalid")
			}
		default:
			return errors.New("proposal replay manifest delimiter is invalid")
		}
		return nil
	}
	if err := readValue(); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("proposal replay manifest has trailing input")
	}
	return nil
}

func decodeLiveCrossStoreProposalReplayManifest(body []byte, path string) (liveCrossStoreAdmissionChild, error) {
	var value liveCrossStoreAdmissionChild
	if len(body) == 0 || len(body) > liveCrossStoreProposalReplayOutputLimit ||
		rejectLiveCrossStoreProposalReplayDuplicateJSON(body) != nil {
		return value, errors.New("proposal replay manifest body is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		!validLiveCrossStoreProposalReplayController(value.Controller, path) {
		return liveCrossStoreAdmissionChild{}, errors.New("proposal replay manifest is invalid")
	}
	input, err := liveCrossStoreAdmissionChildInput(value)
	if err != nil {
		return liveCrossStoreAdmissionChild{}, errors.New("proposal replay manifest input is invalid")
	}
	if review, reviewErr := newGatewayRebindCommitReview(input.Inspection); reviewErr != nil ||
		!reflect.DeepEqual(review.Inspection, input.Inspection) {
		return liveCrossStoreAdmissionChild{}, errors.New("proposal replay manifest inspection is invalid")
	}
	value.Input = input
	return value, nil
}

func writeLiveCrossStoreProposalReplayManifest(path string, value liveCrossStoreAdmissionChild) error {
	if review, reviewErr := newGatewayRebindCommitReview(value.Input.Inspection); reviewErr != nil ||
		!reflect.DeepEqual(review.Inspection, value.Input.Inspection) {
		return errors.New("proposal replay manifest inspection is invalid")
	}
	input, err := liveCrossStoreAdmissionChildInput(value)
	if err != nil || !validLiveCrossStoreProposalReplayController(value.Controller, path) {
		return errors.New("proposal replay manifest is invalid")
	}
	value.Input = input
	body, err := json.Marshal(value)
	if err != nil || len(body) == 0 || len(body) > liveCrossStoreProposalReplayOutputLimit ||
		rejectLiveCrossStoreProposalReplayDuplicateJSON(body) != nil {
		return errors.New("encode proposal replay manifest")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("create proposal replay manifest")
	}
	written, writeErr := file.Write(body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || written != len(body) {
		return errors.New("write proposal replay manifest")
	}
	return nil
}

func readLiveCrossStoreProposalReplayManifest(path string) (liveCrossStoreAdmissionChild, error) {
	var value liveCrossStoreAdmissionChild
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		filepath.Base(path) != liveCrossStoreProposalReplayManifestName ||
		!liveGatewayRebindStoppedStageExactDirectory(filepath.Dir(path)) {
		return value, errors.New("proposal replay manifest path is invalid")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 ||
		generatedIngressPathIsReparsePoint(path) || (runtime.GOOS != "windows" && before.Mode().Perm()&0o077 != 0) {
		return value, errors.New("proposal replay manifest file is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return value, errors.New("open proposal replay manifest")
	}
	opened, statErr := file.Stat()
	body, readErr := io.ReadAll(io.LimitReader(file, liveCrossStoreProposalReplayOutputLimit+1))
	closeErr := file.Close()
	after, afterErr := os.Lstat(path)
	if statErr != nil || !os.SameFile(before, opened) || readErr != nil || closeErr != nil ||
		afterErr != nil || !os.SameFile(before, after) {
		return value, errors.New("proposal replay manifest identity changed")
	}
	return decodeLiveCrossStoreProposalReplayManifest(body, path)
}

func liveCrossStoreProposalReplayEnvironment(base []string, values ...string) []string {
	keys := []string{
		liveCrossStoreChildEnvironment,
		liveCrossStoreChildModeEnvironment,
		liveCrossStoreProposalReplayCancellationEnvironment,
	}
	result := make([]string, 0, len(base)+len(values))
	for _, value := range base {
		keep := true
		for _, key := range keys {
			if strings.HasPrefix(value, key+"=") {
				keep = false
				break
			}
		}
		if keep {
			result = append(result, value)
		}
	}
	return append(result, values...)
}

func liveCrossStoreProposalReplayOutputCompleted(body []byte, overflow bool) bool {
	if overflow || len(body) == 0 || len(body) > liveCrossStoreProposalReplayOutputLimit {
		return false
	}
	lines := make([]string, 0, 4)
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) != 4 || lines[0] != "=== RUN   TestLiveGatewayRebindCrossStoreAdmissionChild" ||
		lines[1] != liveCrossStoreProposalReplaySuccessMarker || lines[3] != "PASS" {
		return false
	}
	const passPrefix = "--- PASS: TestLiveGatewayRebindCrossStoreAdmissionChild ("
	if !strings.HasPrefix(lines[2], passPrefix) || !strings.HasSuffix(lines[2], ")") {
		return false
	}
	duration := strings.TrimSuffix(strings.TrimPrefix(lines[2], passPrefix), ")")
	if parsed, err := time.ParseDuration(duration); err != nil || parsed < 0 {
		return false
	}
	for _, line := range lines {
		if strings.Contains(line, liveCrossStoreProposalReplayFailurePrefix) ||
			(strings.Contains(line, "CROSS_STORE_PROPOSAL_REPLAY_") && line != liveCrossStoreProposalReplaySuccessMarker) ||
			strings.Contains(line, "FAIL") || strings.Contains(line, "SKIP") {
			return false
		}
	}
	return true
}

func liveCrossStoreProposalReplayFailure(body []byte) string {
	allowed := map[string]bool{
		"environment": true, "manifest": true, "database": true, "manager": true,
		"source": true, "replay": true, "history": true, "effects": true, "close": true,
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, liveCrossStoreProposalReplayFailurePrefix) {
			stage := strings.TrimPrefix(line, liveCrossStoreProposalReplayFailurePrefix)
			if allowed[stage] {
				return stage
			}
			return "unknown"
		}
	}
	return "unknown"
}

func liveCrossStoreProposalReplayInspectionMismatch(left, right GatewayRebindProposalInspection) string {
	if !reflect.DeepEqual(left.Spec, right.Spec) {
		return "spec"
	}
	if !reflect.DeepEqual(left.Roster, right.Roster) {
		return "roster"
	}
	if !sameGatewayRebindRuntimeHeads(left.RuntimeHeads, right.RuntimeHeads) {
		return "runtime_heads_value"
	}
	if !reflect.DeepEqual(left.RuntimeHeads, right.RuntimeHeads) {
		return "runtime_heads_representation"
	}
	left.Roster, left.RuntimeHeads = nil, nil
	right.Roster, right.RuntimeHeads = nil, nil
	if !reflect.DeepEqual(left, right) {
		return "projection"
	}
	return "none"
}

func failLiveCrossStoreProposalReplayChild(t *testing.T, stage string) {
	t.Helper()
	if liveCrossStoreProposalReplayFailure([]byte(liveCrossStoreProposalReplayFailurePrefix+stage)) != stage {
		stage = "environment"
	}
	fmt.Println(liveCrossStoreProposalReplayFailurePrefix + stage)
	_ = os.Stdout.Sync()
	t.FailNow()
}

func liveRunCrossStoreProposalReplayChild(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	childContext, cancel := context.WithTimeout(ctx, 11*time.Minute)
	defer cancel()
	command := exec.CommandContext(childContext, os.Args[0],
		"-test.run=^TestLiveGatewayRebindCrossStoreAdmissionChild$", "-test.count=1", "-test.v", "-test.timeout=10m")
	command.Env = liveCrossStoreProposalReplayEnvironment(os.Environ(),
		liveCrossStoreChildEnvironment+"="+path,
		liveCrossStoreChildModeEnvironment+"="+liveCrossStoreProposalReplayMode)
	capture := &liveGatewayRebindStoppedStageOutputCapture{}
	command.Stdout = capture
	command.Stderr = capture
	err := command.Run()
	output, overflow := capture.result()
	if err != nil || !liveCrossStoreProposalReplayOutputCompleted(output, overflow) {
		t.Fatalf("proposal replay child failed at fixed stage %s", liveCrossStoreProposalReplayFailure(output))
	}
}

func liveCrossStoreProposalReplayDockerDigest(ctx context.Context, manager *Manager,
	route routeState, state gatewayV2RouteState, journal gatewayMigrationJournal,
) (string, error) {
	observed, err := manager.inspectGatewayRebindDocker(ctx, route, state, journal)
	defer clearGatewayV2DockerObservation(&observed)
	if err != nil || !validGatewayRebindPredecessorDocker(route, state, journal, observed) {
		return "", errors.New("proposal replay source Docker proof is invalid")
	}
	return gatewayRebindPredecessorDockerDigest(observed, state.Identity)
}

func liveCrossStoreProposalReplayChild(t *testing.T, path string) {
	t.Helper()
	value, err := readLiveCrossStoreProposalReplayManifest(path)
	if err != nil {
		failLiveCrossStoreProposalReplayChild(t, "manifest")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	db, err := database.Open(value.Controller.DataRoot)
	if err != nil {
		failLiveCrossStoreProposalReplayChild(t, "database")
	}
	databaseOpen := true
	defer func() {
		if databaseOpen {
			_ = db.Close()
		}
	}()
	repository := appaccess.New(db)
	audit := &liveGatewayRebindRuntimeReplayRunner{runner: &runtimeprocess.ExecRunner{}}
	manager, err := New(audit, Options{DataRoot: value.Controller.DataRoot, WorkingDirectory: value.Controller.WorkingDirectory,
		DockerExecutable: value.Controller.DockerExecutable, DockerConfigDirectory: value.Controller.DockerConfigDirectory,
		HostPort: value.Controller.HostPort, CommandTimeout: 45 * time.Second, PullTimeout: 5 * time.Minute,
		OutputLimit: liveDockerOutputLimit, RebindFenceCheck: repository.CheckGatewayRebindFence,
		RebindCurrentStateRepository: repository})
	if err != nil {
		failLiveCrossStoreProposalReplayChild(t, "manager")
	}
	input := value.Input
	beforeSQL, sqlErr := repository.GatewayRebindRecoverySnapshot(ctx)
	beforeHeads, headsErr := repository.GatewayRebindRuntimeHeads(ctx)
	beforeRoute, routeErr := manager.store.load()
	store, storeErr := newGatewayUpgradeStateStore(value.Controller.DataRoot)
	var beforeState gatewayV2RouteState
	var beforeJournal gatewayMigrationJournal
	upgradeErr := storeErr
	if storeErr == nil {
		beforeState, beforeJournal, upgradeErr = store.loadBoundUpgrade(input.Inspection.Spec.Predecessor.Lineage.OperationID)
	}
	beforeHistory, historyErr := readGatewayHistorySnapshotMode(manager.store, true)
	if sqlErr != nil || headsErr != nil || routeErr != nil || storeErr != nil || upgradeErr != nil ||
		historyErr != nil {
		failLiveCrossStoreProposalReplayChild(t, "source")
	}
	beforeDocker, dockerErr := liveCrossStoreProposalReplayDockerDigest(ctx, manager, beforeRoute, beforeState, beforeJournal)
	if dockerErr != nil {
		failLiveCrossStoreProposalReplayChild(t, "source")
	}
	spec := input.Inspection.Spec
	proposal, err := manager.InspectGatewayRebindProposal(ctx, repository, GatewayRebindProposalInput{
		OperationID: spec.OperationID, SuccessorProfileRevisionID: spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    spec.SuccessorProfileOperationID, SuccessorProfile: spec.SuccessorProfile,
	})
	if err != nil || !reflect.DeepEqual(input.Inspection, proposal) {
		failLiveCrossStoreProposalReplayChild(t, "replay")
	}
	afterSQL, sqlErr := repository.GatewayRebindRecoverySnapshot(ctx)
	afterHeads, headsErr := repository.GatewayRebindRuntimeHeads(ctx)
	afterRoute, routeErr := manager.store.load()
	afterState, afterJournal, upgradeErr := store.loadBoundUpgrade(input.Inspection.Spec.Predecessor.Lineage.OperationID)
	afterHistory, historyErr := readGatewayHistorySnapshotMode(manager.store, true)
	if sqlErr != nil || headsErr != nil || routeErr != nil || upgradeErr != nil || historyErr != nil {
		failLiveCrossStoreProposalReplayChild(t, "history")
	}
	afterDocker, dockerErr := liveCrossStoreProposalReplayDockerDigest(ctx, manager, afterRoute, afterState, afterJournal)
	if dockerErr != nil ||
		!reflect.DeepEqual(beforeSQL, afterSQL) || !reflect.DeepEqual(beforeHeads, afterHeads) ||
		!reflect.DeepEqual(beforeRoute, afterRoute) || !reflect.DeepEqual(beforeState, afterState) ||
		!reflect.DeepEqual(beforeJournal, afterJournal) || beforeDocker != afterDocker ||
		!sameGatewayHistorySnapshot(beforeHistory, afterHistory) {
		failLiveCrossStoreProposalReplayChild(t, "history")
	}
	if len(audit.effects) != 0 {
		failLiveCrossStoreProposalReplayChild(t, "effects")
	}
	if err := db.Close(); err != nil {
		failLiveCrossStoreProposalReplayChild(t, "close")
	}
	databaseOpen = false
	fmt.Println(liveCrossStoreProposalReplaySuccessMarker)
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
	manager := fixture.ingress
	options := manager.options
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

func TestLiveGatewayRebindCrossStoreProposalReplayCancellationChild(t *testing.T) {
	if os.Getenv(liveCrossStoreProposalReplayCancellationEnvironment) != "1" {
		t.Skip("proposal replay cancellation child requires its private parent")
	}
	fmt.Println(liveCrossStoreProposalReplayCancellationMarker)
	_ = os.Stdout.Sync()
	time.Sleep(time.Hour)
}

func TestLiveGatewayRebindCrossStoreProposalReplayProtocolIsClosed(t *testing.T) {
	_, _, _, inspection := gatewayRebindCommitReviewFixture(t)
	input := gatewayRebindCommitInput{Inspection: inspection}
	stage, docker := newLiveGatewayRebindStoppedStageManifestFixture(t)
	controller := liveFinalHandoverChild{
		DataRoot: stage.DataRoot, WorkingDirectory: stage.WorkingDirectory, DockerExecutable: docker,
		DockerConfigDirectory: stage.DockerConfigDirectory, HostPort: stage.HostPort,
	}
	path := filepath.Join(stage.FixtureRoot, liveCrossStoreProposalReplayManifestName)
	value := liveCrossStoreAdmissionChild{Controller: controller, Input: input}

	t.Run("finite mode and private manifest", func(t *testing.T) {
		for _, mode := range []string{"exit-after-claim", "recover", liveCrossStoreProposalReplayMode} {
			if !liveCrossStoreAdmissionChildModeAllowed(mode) {
				t.Fatalf("refused fixed child mode %q", mode)
			}
		}
		for _, mode := range []string{"", "proposal", "unknown"} {
			if liveCrossStoreAdmissionChildModeAllowed(mode) {
				t.Fatalf("accepted unknown child mode %q", mode)
			}
		}
		if err := writeLiveCrossStoreProposalReplayManifest(path, value); err != nil {
			t.Fatal("write valid private replay manifest")
		}
		read, err := readLiveCrossStoreProposalReplayManifest(path)
		if err != nil {
			t.Fatal("private replay manifest read failed")
		}
		if !reflect.DeepEqual(value.Controller, read.Controller) {
			t.Fatal("private replay manifest mismatch: controller")
		}
		if mismatch := liveCrossStoreProposalReplayInspectionMismatch(input.Inspection, read.Input.Inspection); mismatch != "none" {
			t.Fatalf("private replay manifest mismatch: %s", mismatch)
		}
	})

	t.Run("malformed and tainted manifest refusal", func(t *testing.T) {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal("encode manifest control")
		}
		unknown := append(append([]byte(nil), body[:len(body)-1]...), []byte(`,"Unknown":true}`)...)
		duplicate := bytes.Replace(body, []byte(`"DataRoot":`), []byte(`"DataRoot":"duplicate","DataRoot":`), 1)
		crossed := value
		crossed.Controller.WorkingDirectory = crossed.Controller.DockerConfigDirectory
		crossedBody, err := json.Marshal(crossed)
		if err != nil {
			t.Fatal("encode crossed manifest control")
		}
		tainted := value
		tainted.Input.Inspection.SourceStateRevision++
		taintedBody, err := json.Marshal(tainted)
		if err != nil {
			t.Fatal("encode tainted inspection control")
		}
		for _, control := range []struct {
			name string
			body []byte
		}{
			{name: "empty"}, {name: "malformed", body: []byte("{")},
			{name: "trailing", body: append(append([]byte(nil), body...), []byte("{}")...)},
			{name: "unknown", body: unknown}, {name: "duplicate", body: duplicate},
			{name: "crossed directory", body: crossedBody},
			{name: "tainted inspection", body: taintedBody},
			{name: "oversized", body: bytes.Repeat([]byte{'x'}, liveCrossStoreProposalReplayOutputLimit+1)},
		} {
			t.Run(control.name, func(t *testing.T) {
				if _, err := decodeLiveCrossStoreProposalReplayManifest(control.body, path); err == nil {
					t.Fatal("accepted malformed or tainted proposal replay manifest")
				}
			})
		}
	})

	t.Run("closed output grammar", func(t *testing.T) {
		valid := []byte("=== RUN   TestLiveGatewayRebindCrossStoreAdmissionChild\n" +
			liveCrossStoreProposalReplaySuccessMarker + "\n" +
			"--- PASS: TestLiveGatewayRebindCrossStoreAdmissionChild (0.01s)\nPASS\n")
		if !liveCrossStoreProposalReplayOutputCompleted(valid, false) {
			t.Fatal("refused exact proposal replay output")
		}
		for _, control := range []struct {
			name     string
			body     []byte
			overflow bool
		}{
			{name: "empty"},
			{name: "missing run", body: bytes.Replace(valid, []byte("=== RUN   TestLiveGatewayRebindCrossStoreAdmissionChild\n"), nil, 1)},
			{name: "duplicate run", body: append([]byte("=== RUN   TestLiveGatewayRebindCrossStoreAdmissionChild\n"), valid...)},
			{name: "duplicate", body: append(append([]byte(nil), valid...), []byte(liveCrossStoreProposalReplaySuccessMarker+"\n")...)},
			{name: "duplicate pass", body: append(append([]byte(nil), valid...), []byte("--- PASS: TestLiveGatewayRebindCrossStoreAdmissionChild (0.01s)\n")...)},
			{name: "tainted", body: bytes.Replace(valid, []byte(liveCrossStoreProposalReplaySuccessMarker), []byte(liveCrossStoreProposalReplaySuccessMarker+"-tainted"), 1)},
			{name: "indented marker", body: bytes.Replace(valid, []byte(liveCrossStoreProposalReplaySuccessMarker), []byte("  "+liveCrossStoreProposalReplaySuccessMarker), 1)},
			{name: "embedded marker", body: bytes.Replace(valid, []byte(liveCrossStoreProposalReplaySuccessMarker), []byte("prefix "+liveCrossStoreProposalReplaySuccessMarker), 1)},
			{name: "crossed child", body: bytes.Replace(valid, []byte("TestLiveGatewayRebindCrossStoreAdmissionChild"), []byte("TestLiveGatewayRebindCrossStoreAdmissionChildSuffix"), 1)},
			{name: "crossed parent pass", body: bytes.Replace(valid, []byte("--- PASS: TestLiveGatewayRebindCrossStoreAdmissionChild"), []byte("--- PASS: TestLiveGatewayRebindCrossStoreProposalReplayProtocolIsClosed"), 1)},
			{name: "failure", body: []byte(liveCrossStoreProposalReplayFailurePrefix + "source\nFAIL\n")},
			{name: "unknown marker", body: []byte("CROSS_STORE_PROPOSAL_REPLAY_UNKNOWN\nPASS\n")},
			{name: "overflow", body: valid, overflow: true},
		} {
			t.Run(control.name, func(t *testing.T) {
				if liveCrossStoreProposalReplayOutputCompleted(control.body, control.overflow) {
					t.Fatal("accepted ambiguous proposal replay output")
				}
			})
		}
		if liveCrossStoreProposalReplayFailure([]byte(liveCrossStoreProposalReplayFailurePrefix+"source\n")) != "source" ||
			liveCrossStoreProposalReplayFailure([]byte(liveCrossStoreProposalReplayFailurePrefix+"raw-detail\n")) != "unknown" {
			t.Fatal("proposal replay failure grammar exposed an open failure reason")
		}
	})

	t.Run("cancellation reaps child", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0],
			"-test.run=^TestLiveGatewayRebindCrossStoreProposalReplayCancellationChild$", "-test.count=1", "-test.v")
		command.Env = liveCrossStoreProposalReplayEnvironment(os.Environ(),
			liveCrossStoreProposalReplayCancellationEnvironment+"=1")
		capture := &liveGatewayRebindStoppedStageOutputCapture{}
		ready := make(chan struct{}, 1)
		writer := &liveGatewayRebindStoppedStageCancellationOutputWriter{
			capture: capture, marker: []byte(liveCrossStoreProposalReplayCancellationMarker), ready: ready,
		}
		command.Stdout = writer
		command.Stderr = capture
		if err := command.Start(); err != nil {
			t.Fatal("start proposal replay cancellation child")
		}
		select {
		case <-ready:
			cancel()
		case <-time.After(10 * time.Second):
			cancel()
			_ = command.Wait()
			t.Fatal("proposal replay cancellation child did not reach its fixed marker")
		}
		err := command.Wait()
		output, overflow := capture.result()
		if err == nil || !errors.Is(ctx.Err(), context.Canceled) || overflow ||
			bytes.Count(output, []byte(liveCrossStoreProposalReplayCancellationMarker)) != 1 {
			t.Fatal("proposal replay cancellation did not terminate and reap its child")
		}
	})
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
	mode := os.Getenv(liveCrossStoreChildModeEnvironment)
	if !liveCrossStoreAdmissionChildModeAllowed(mode) {
		t.Fatal("unknown admission child mode")
	}
	if mode == liveCrossStoreProposalReplayMode {
		liveCrossStoreProposalReplayChild(t, path)
		return
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
