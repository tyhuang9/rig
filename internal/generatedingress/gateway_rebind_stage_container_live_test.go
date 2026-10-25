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
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
)

const (
	liveGatewayRebindStoppedStageReplayEnvironment         = "RIG_TEST_STOPPED_STAGE_REPLAY_CHILD"
	liveGatewayRebindStoppedStageReplayTest                = "TestLiveGatewayRebindStoppedStageContainerReplayChild"
	liveGatewayRebindStoppedStageReplayPurpose             = "hostd/test/generated-ingress/stopped-stage-replay/v1"
	liveGatewayRebindStoppedStageReplayMarker              = "GATEWAY_REBIND_STOPPED_STAGE_REPLAY_OK:sequence=6:no-create"
	liveGatewayRebindDataVolumeReplayTest                  = "TestLiveGatewayRebindSuccessorDataVolumeReplayChild"
	liveGatewayRebindDataVolumeReplayMarker                = "GATEWAY_REBIND_DATA_VOLUME_REPLAY_OK:sequence=5:no-create"
	liveGatewayRebindStageConfigAdoptionTest               = "TestLiveGatewayRebindSuccessorStageConfigCopyAdoptionChild"
	liveGatewayRebindStageConfigAdoptionMarker             = "GATEWAY_REBIND_STAGE_CONFIG_ADOPTION_OK:sequence=8:no-copy"
	liveGatewayRebindStageConfigReplayTest                 = "TestLiveGatewayRebindSuccessorStageConfigCopyReplayChild"
	liveGatewayRebindStageConfigReplayMarker               = "GATEWAY_REBIND_STAGE_CONFIG_REPLAY_OK:sequence=8:no-copy"
	liveGatewayRebindFinalConfigAdoptionTest               = "TestLiveGatewayRebindSuccessorFinalConfigCopyAdoptionChild"
	liveGatewayRebindFinalConfigAdoptionMarker             = "GATEWAY_REBIND_FINAL_CONFIG_ADOPTION_OK:sequence=12:no-copy"
	liveGatewayRebindFinalConfigReplayTest                 = "TestLiveGatewayRebindSuccessorFinalConfigCopyReplayChild"
	liveGatewayRebindFinalConfigReplayMarker               = "GATEWAY_REBIND_FINAL_CONFIG_REPLAY_OK:sequence=12:no-copy"
	liveGatewayRebindStageStartAdoptionTest                = "TestLiveGatewayRebindSuccessorStageStartAdoptionChild"
	liveGatewayRebindStageStartAdoptionMarker              = "GATEWAY_REBIND_STAGE_START_ADOPTION_OK:sequence=10:no-start"
	liveGatewayRebindStageStartReplayTest                  = "TestLiveGatewayRebindSuccessorStageStartReplayChild"
	liveGatewayRebindStageStartReplayMarker                = "GATEWAY_REBIND_STAGE_START_REPLAY_OK:sequence=10:no-start"
	liveGatewayRebindStoppedStageCancellationTest          = "TestGatewayRebindStoppedStageReplayCancellationChild"
	liveGatewayRebindStoppedStageCancellationStartedMarker = "GATEWAY_REBIND_STOPPED_STAGE_REPLAY_CANCEL_STARTED"
	liveGatewayRebindStoppedStageCancellationSuccessMarker = "GATEWAY_REBIND_STOPPED_STAGE_REPLAY_CANCEL_OK"
	liveGatewayRebindStoppedStageFailurePrefix             = "GATEWAY_REBIND_STOPPED_STAGE_REPLAY_FAILED:"
	liveGatewayRebindStoppedStageManifestLimit             = 8 << 10
	liveGatewayRebindStoppedStageOutputLimit               = 64 << 10
)

type liveGatewayRebindStoppedStageReplayPair struct {
	testName      string
	successMarker string
}

func liveGatewayRebindStoppedStageReplayPairAllowed(pair liveGatewayRebindStoppedStageReplayPair) bool {
	switch pair {
	case liveGatewayRebindStoppedStageReplayPair{
		testName: liveGatewayRebindStoppedStageReplayTest, successMarker: liveGatewayRebindStoppedStageReplayMarker,
	}, liveGatewayRebindStoppedStageReplayPair{
		testName: liveGatewayRebindDataVolumeReplayTest, successMarker: liveGatewayRebindDataVolumeReplayMarker,
	}, liveGatewayRebindStoppedStageReplayPair{
		testName: liveGatewayRebindStageConfigAdoptionTest, successMarker: liveGatewayRebindStageConfigAdoptionMarker,
	}, liveGatewayRebindStoppedStageReplayPair{
		testName: liveGatewayRebindStageConfigReplayTest, successMarker: liveGatewayRebindStageConfigReplayMarker,
	}, liveGatewayRebindStoppedStageReplayPair{
		testName: liveGatewayRebindFinalConfigAdoptionTest, successMarker: liveGatewayRebindFinalConfigAdoptionMarker,
	}, liveGatewayRebindStoppedStageReplayPair{
		testName: liveGatewayRebindFinalConfigReplayTest, successMarker: liveGatewayRebindFinalConfigReplayMarker,
	}, liveGatewayRebindStoppedStageReplayPair{
		testName: liveGatewayRebindStageStartAdoptionTest, successMarker: liveGatewayRebindStageStartAdoptionMarker,
	}, liveGatewayRebindStoppedStageReplayPair{
		testName: liveGatewayRebindStageStartReplayTest, successMarker: liveGatewayRebindStageStartReplayMarker,
	}:
		return true
	default:
		return false
	}
}

func liveGatewayRebindStoppedStageReplayPairAllowedForLaunch(pair liveGatewayRebindStoppedStageReplayPair) bool {
	return liveGatewayRebindStoppedStageReplayPairAllowed(pair) || pair == (liveGatewayRebindStoppedStageReplayPair{
		testName:      liveGatewayRebindStoppedStageCancellationTest,
		successMarker: liveGatewayRebindStoppedStageCancellationSuccessMarker,
	})
}

type liveGatewayRebindStoppedStageReplayManifest struct {
	Version               int    `json:"version"`
	Purpose               string `json:"purpose"`
	FixtureRoot           string `json:"fixtureRoot"`
	ControlDirectory      string `json:"controlDirectory"`
	DataRoot              string `json:"dataRoot"`
	WorkingDirectory      string `json:"workingDirectory"`
	DockerExecutable      string `json:"dockerExecutable"`
	DockerConfigDirectory string `json:"dockerConfigDirectory"`
	OperationID           string `json:"operationId"`
	HostPort              uint16 `json:"hostPort"`
}

type liveGatewayRebindStoppedStageReplayDriver struct {
	managerGatewayRebindStageContainerDriver
	createCalls int
}

type liveGatewayRebindStoppedStageOutputCapture struct {
	mu       sync.Mutex
	body     []byte
	overflow bool
}

func (c *liveGatewayRebindStoppedStageOutputCapture) Write(body []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	remaining := liveGatewayRebindStoppedStageOutputLimit - len(c.body)
	if remaining > 0 {
		count := len(body)
		if count > remaining {
			count = remaining
		}
		c.body = append(c.body, body[:count]...)
	}
	if len(body) > remaining {
		c.overflow = true
	}
	return len(body), nil
}

func (c *liveGatewayRebindStoppedStageOutputCapture) result() ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.body...), c.overflow
}

type liveGatewayRebindStoppedStageCancellationOutputWriter struct {
	capture  *liveGatewayRebindStoppedStageOutputCapture
	marker   []byte
	ready    chan<- struct{}
	notified bool
}

func (w *liveGatewayRebindStoppedStageCancellationOutputWriter) Write(body []byte) (int, error) {
	written, err := w.capture.Write(body)
	if !w.notified {
		captured, _ := w.capture.result()
		if bytes.Contains(captured, w.marker) {
			select {
			case w.ready <- struct{}{}:
			default:
			}
			w.notified = true
		}
	}
	return written, err
}

func (d *liveGatewayRebindStoppedStageReplayDriver) create(context.Context,
	gatewayRebindProtectedIntent, gatewayRebindStageIntent,
) (string, error) {
	d.createCalls++
	return "", errors.New("stopped-stage replay attempted container creation")
}

func liveGatewayRebindStoppedStageExactDirectory(path string) bool {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		(runtime.GOOS != "windows" && securetemp.ValidatePrivateDirectory(path) != nil) {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 &&
		!generatedIngressPathIsReparsePoint(path)
}

func validLiveGatewayRebindStoppedStageReplayManifest(value liveGatewayRebindStoppedStageReplayManifest,
	manifestPath, expectedDocker string,
) bool {
	if value.Version != 1 || value.Purpose != liveGatewayRebindStoppedStageReplayPurpose ||
		!validCanonicalUUID(value.OperationID) || value.HostPort == 0 ||
		manifestPath == "" || !filepath.IsAbs(manifestPath) || filepath.Clean(manifestPath) != manifestPath ||
		filepath.Base(manifestPath) != "manifest.json" || filepath.Dir(manifestPath) != value.ControlDirectory ||
		value.DockerExecutable == "" || value.DockerExecutable != expectedDocker ||
		!filepath.IsAbs(expectedDocker) || filepath.Clean(expectedDocker) != expectedDocker {
		return false
	}
	if filepath.Dir(value.ControlDirectory) != value.FixtureRoot ||
		!strings.HasPrefix(filepath.Base(value.ControlDirectory), "stopped-stage-replay-") ||
		value.DataRoot != filepath.Join(value.FixtureRoot, "ingress-state") {
		return false
	}
	runtimeRoot := filepath.Join(value.DataRoot, "runtime")
	builderRoot := filepath.Join(runtimeRoot, "generated-builder")
	if value.WorkingDirectory != filepath.Join(builderRoot, "controller-working") ||
		value.DockerConfigDirectory != filepath.Join(builderRoot, "controller-docker-config") {
		return false
	}
	for _, directory := range []string{
		value.FixtureRoot, value.ControlDirectory, value.DataRoot, runtimeRoot, builderRoot,
		value.WorkingDirectory, value.DockerConfigDirectory,
	} {
		if !liveGatewayRebindStoppedStageExactDirectory(directory) {
			return false
		}
	}
	dockerInfo, err := os.Stat(expectedDocker)
	return err == nil && dockerInfo.Mode().IsRegular()
}

func rejectLiveGatewayRebindStoppedStageManifestKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("stopped-stage replay manifest is not an object")
	}
	expected := map[string]struct{}{
		"version": {}, "purpose": {}, "fixtureRoot": {}, "controlDirectory": {}, "dataRoot": {},
		"workingDirectory": {}, "dockerExecutable": {}, "dockerConfigDirectory": {},
		"operationId": {}, "hostPort": {},
	}
	seen := make(map[string]struct{}, len(expected))
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return errors.New("stopped-stage replay manifest key is invalid")
		}
		if _, ok := expected[key]; !ok {
			return errors.New("stopped-stage replay manifest key is unknown")
		}
		if _, ok := seen[key]; ok {
			return errors.New("stopped-stage replay manifest key is duplicated")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return errors.New("stopped-stage replay manifest value is invalid")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != len(expected) {
		return errors.New("stopped-stage replay manifest object is incomplete")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("stopped-stage replay manifest has trailing input")
	}
	return nil
}

func writeLiveGatewayRebindStoppedStageManifestFile(path string, body []byte) error {
	if len(body) == 0 || len(body) > liveGatewayRebindStoppedStageManifestLimit {
		return errors.New("stopped-stage replay manifest body is invalid")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("create stopped-stage replay manifest")
	}
	written, writeErr := file.Write(body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || written != len(body) {
		return errors.New("write stopped-stage replay manifest")
	}
	return nil
}

func writeLiveGatewayRebindStoppedStageReplayManifest(value liveGatewayRebindStoppedStageReplayManifest,
	expectedDocker string,
) (string, error) {
	path := filepath.Join(value.ControlDirectory, "manifest.json")
	if !validLiveGatewayRebindStoppedStageReplayManifest(value, path, expectedDocker) {
		return "", errors.New("stopped-stage replay manifest is invalid")
	}
	body, err := json.Marshal(value)
	if err != nil || rejectLiveGatewayRebindStoppedStageManifestKeys(body) != nil {
		return "", errors.New("encode stopped-stage replay manifest")
	}
	if err := writeLiveGatewayRebindStoppedStageManifestFile(path, body); err != nil {
		return "", err
	}
	return path, nil
}

func readLiveGatewayRebindStoppedStageReplayManifest(path, expectedDocker string) (
	liveGatewayRebindStoppedStageReplayManifest, error,
) {
	var value liveGatewayRebindStoppedStageReplayManifest
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		filepath.Base(path) != "manifest.json" {
		return value, errors.New("stopped-stage replay manifest path is invalid")
	}
	directory := filepath.Dir(path)
	if !liveGatewayRebindStoppedStageExactDirectory(directory) {
		return value, errors.New("stopped-stage replay control directory is invalid")
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return value, errors.New("stopped-stage replay control directory is unavailable")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 ||
		generatedIngressPathIsReparsePoint(path) ||
		(runtime.GOOS != "windows" && pathInfo.Mode().Perm()&0o077 != 0) {
		return value, errors.New("stopped-stage replay manifest file is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return value, errors.New("stopped-stage replay manifest cannot be opened")
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil || !os.SameFile(pathInfo, openedInfo) {
		_ = file.Close()
		return value, errors.New("stopped-stage replay manifest identity changed")
	}
	body, readErr := io.ReadAll(io.LimitReader(file, liveGatewayRebindStoppedStageManifestLimit+1))
	closeErr := file.Close()
	currentInfo, currentErr := os.Lstat(path)
	currentDirectoryInfo, directoryErr := os.Lstat(directory)
	if readErr != nil || closeErr != nil || len(body) == 0 || len(body) > liveGatewayRebindStoppedStageManifestLimit ||
		currentErr != nil || !os.SameFile(pathInfo, currentInfo) || directoryErr != nil ||
		!os.SameFile(directoryInfo, currentDirectoryInfo) || rejectLiveGatewayRebindStoppedStageManifestKeys(body) != nil {
		return value, errors.New("stopped-stage replay manifest read is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		!validLiveGatewayRebindStoppedStageReplayManifest(value, path, expectedDocker) {
		return liveGatewayRebindStoppedStageReplayManifest{}, errors.New("stopped-stage replay manifest is invalid")
	}
	return value, nil
}

func liveGatewayRebindStoppedStageChildEnvironment(manifest string) []string {
	values := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key != liveGatewayRebindStoppedStageReplayEnvironment {
			values = append(values, value)
		}
	}
	if manifest != "" {
		values = append(values, liveGatewayRebindStoppedStageReplayEnvironment+"="+manifest)
	}
	return values
}

func liveGatewayRebindStoppedStageReplayChildCommand(ctx context.Context,
	pair liveGatewayRebindStoppedStageReplayPair, manifest string,
) (*exec.Cmd, error) {
	if !liveGatewayRebindStoppedStageReplayPairAllowedForLaunch(pair) {
		return nil, errors.New("unknown stopped-stage replay selector and marker pair")
	}
	command := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^"+pair.testName+"$", "-test.count=1", "-test.v", "-test.timeout=10m")
	command.Env = liveGatewayRebindStoppedStageChildEnvironment(manifest)
	return command, nil
}

func TestGatewayRebindStoppedStageReplayCancellationChild(t *testing.T) {
	path := os.Getenv(liveGatewayRebindStoppedStageReplayEnvironment)
	if path == "" {
		t.Skip("stopped-stage cancellation child requires a parent-owned manifest")
	}
	body, err := os.ReadFile(path)
	if err != nil || len(body) == 0 || len(body) > liveGatewayRebindStoppedStageManifestLimit {
		failLiveGatewayRebindStoppedStageChild(t, "manifest", "read stopped-stage cancellation manifest")
	}
	var value liveGatewayRebindStoppedStageReplayManifest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		failLiveGatewayRebindStoppedStageChild(t, "manifest", "decode stopped-stage cancellation manifest")
	}
	if _, err := readLiveGatewayRebindStoppedStageReplayManifest(path, value.DockerExecutable); err != nil {
		failLiveGatewayRebindStoppedStageChild(t, "manifest", "validate stopped-stage cancellation manifest")
	}
	fmt.Println(liveGatewayRebindStoppedStageCancellationStartedMarker)
	select {}
}

func liveGatewayRebindStoppedStageChildCompleted(output []byte) bool {
	return liveGatewayRebindStoppedStageChildCompletedForPair(output, liveGatewayRebindStoppedStageReplayPair{
		testName: liveGatewayRebindStoppedStageReplayTest, successMarker: liveGatewayRebindStoppedStageReplayMarker,
	})
}

func liveGatewayRebindStoppedStageChildCompletedForPair(output []byte,
	pair liveGatewayRebindStoppedStageReplayPair,
) bool {
	if !liveGatewayRebindStoppedStageReplayPairAllowed(pair) {
		return false
	}
	if len(output) == 0 || len(output) > liveGatewayRebindStoppedStageOutputLimit {
		return false
	}
	lines := bytes.Split(output, []byte{'\n'})
	if len(lines) > 64 {
		return false
	}
	markerCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(string(line))
		if trimmed == pair.successMarker {
			markerCount++
		} else if strings.Contains(trimmed, liveGatewayRebindStoppedStageFailurePrefix) ||
			liveGatewayRebindStoppedStageReplayMarkerNamespaceContains(trimmed) {
			return false
		}
	}
	return markerCount == 1 &&
		bytes.Contains(output, []byte("--- PASS: "+pair.testName+" ")) &&
		!bytes.Contains(output, []byte("--- FAIL:")) && !bytes.Contains(output, []byte("--- SKIP:"))
}

func liveGatewayRebindStoppedStageReplayMarkerNamespaceContains(line string) bool {
	for _, marker := range []string{
		liveGatewayRebindStoppedStageReplayMarker,
		liveGatewayRebindDataVolumeReplayMarker,
		liveGatewayRebindStageConfigAdoptionMarker,
		liveGatewayRebindStageConfigReplayMarker,
		liveGatewayRebindFinalConfigAdoptionMarker,
		liveGatewayRebindFinalConfigReplayMarker,
		liveGatewayRebindStageStartAdoptionMarker,
		liveGatewayRebindStageStartReplayMarker,
		liveGatewayRebindStoppedStageCancellationSuccessMarker,
	} {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

func liveGatewayRebindStoppedStageChildFailure(output []byte) string {
	if len(output) == 0 || len(output) > liveGatewayRebindStoppedStageOutputLimit {
		return "unknown"
	}
	lines := bytes.Split(output, []byte{'\n'})
	if len(lines) > 64 {
		return "unknown"
	}
	known := map[string]struct{}{
		"environment": {}, "manifest": {}, "database": {}, "manager": {}, "source": {},
		"replay": {}, "create": {}, "history": {}, "physical": {}, "receipt": {},
	}
	result := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(string(line))
		stage, found := strings.CutPrefix(trimmed, liveGatewayRebindStoppedStageFailurePrefix)
		if !found {
			continue
		}
		if _, ok := known[stage]; !ok {
			return "unknown"
		}
		if result != "" {
			return "ambiguous"
		}
		result = stage
	}
	if result == "" {
		return "unknown"
	}
	return result
}

func failLiveGatewayRebindStoppedStageChild(t *testing.T, stage, message string) {
	t.Helper()
	fmt.Fprintln(os.Stderr, liveGatewayRebindStoppedStageFailurePrefix+stage)
	t.Fatal(message)
}

func readLiveGatewayRebindStoppedStageProgressBytes(history gatewayRebindProtectedIntentHistory) ([][]byte, error) {
	result := make([][]byte, len(history.Progress))
	for index, progress := range history.Progress {
		body, err := os.ReadFile(progress.Store.path)
		if err != nil {
			return nil, errors.New("read stopped-stage protected progress")
		}
		result[index] = body
	}
	return result, nil
}

func equalGatewayRebindStageContainerSnapshot(intent gatewayRebindProtectedIntent,
	stage gatewayRebindStageIntent, binding *gatewayRebindStageContainerBinding,
	physical, replayed gatewayRebindStageContainerObservation,
) bool {
	if !validGatewayRebindStageContainerObservation(intent, stage, physical, binding) ||
		!validGatewayRebindStageContainerObservation(intent, stage, replayed, binding) {
		return false
	}
	physical.StageContainer.Mounts = canonicalGatewayRebindTypedMounts(physical.StageContainer.Mounts)
	replayed.StageContainer.Mounts = canonicalGatewayRebindTypedMounts(replayed.StageContainer.Mounts)
	return reflect.DeepEqual(physical, replayed)
}

func runLiveGatewayRebindStoppedStageReplayChild(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent,
) {
	runLiveGatewayRebindStoppedStageReplayChildWithPair(t, fixture, intent, liveGatewayRebindStoppedStageReplayPair{
		testName: liveGatewayRebindStoppedStageReplayTest, successMarker: liveGatewayRebindStoppedStageReplayMarker,
	})
}

func runLiveGatewayRebindStoppedStageReplayChildWithPair(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, pair liveGatewayRebindStoppedStageReplayPair,
) {
	t.Helper()
	if !liveGatewayRebindStoppedStageReplayPairAllowed(pair) {
		t.Fatal("unknown stopped-stage replay selector and marker pair")
	}
	control, err := os.MkdirTemp(fixture.root, "stopped-stage-replay-")
	if err != nil || os.Chmod(control, 0o700) != nil {
		t.Fatal("create private stopped-stage replay control directory")
	}
	value := liveGatewayRebindStoppedStageReplayManifest{
		Version: 1, Purpose: liveGatewayRebindStoppedStageReplayPurpose,
		FixtureRoot: fixture.root, ControlDirectory: control,
		DataRoot: fixture.stateRoot, WorkingDirectory: fixture.ingress.options.WorkingDirectory,
		DockerExecutable: fixture.docker, DockerConfigDirectory: fixture.dockerConfig,
		OperationID: intent.OperationID, HostPort: fixture.ingress.options.HostPort,
	}
	manifest, err := writeLiveGatewayRebindStoppedStageReplayManifest(value, fixture.docker)
	if err != nil {
		t.Fatal("write private stopped-stage replay manifest")
	}
	command, err := liveGatewayRebindStoppedStageReplayChildCommand(fixture.ctx, pair, manifest)
	if err != nil {
		t.Fatal("create stopped-stage replay child command")
	}
	capture := &liveGatewayRebindStoppedStageOutputCapture{}
	command.Stdout, command.Stderr = capture, capture
	runErr := command.Run()
	output, overflow := capture.result()
	if runErr != nil || overflow || !liveGatewayRebindStoppedStageChildCompletedForPair(output, pair) {
		t.Fatalf("fresh-process stopped-stage replay did not complete its exact closed protocol: stage=%s",
			liveGatewayRebindStoppedStageChildFailure(output))
	}
}

type liveGatewayRebindStoppedStageReplayChildProcess struct {
	ctx        context.Context
	value      liveGatewayRebindStoppedStageReplayManifest
	repository *appaccess.Repository
	manager    *Manager
}

type liveGatewayRebindStoppedStageReplayChildOutcome struct {
	stage   string
	message string
}

func runLiveGatewayRebindStoppedStageReplayChildProcess(t *testing.T,
	pair liveGatewayRebindStoppedStageReplayPair,
	run func(*liveGatewayRebindStoppedStageReplayChildProcess) liveGatewayRebindStoppedStageReplayChildOutcome,
) {
	t.Helper()
	if !liveGatewayRebindStoppedStageReplayPairAllowed(pair) {
		failLiveGatewayRebindStoppedStageChild(t, "environment", "unknown stopped-stage replay selector and marker pair")
	}
	path := os.Getenv(liveGatewayRebindStoppedStageReplayEnvironment)
	if path == "" {
		t.Skip("stopped-stage replay child requires a parent-owned manifest")
	}
	if os.Getenv("RIG_RUN_LIVE_GATEWAY_V2") != "1" || runtime.GOOS != "linux" ||
		os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		failLiveGatewayRebindStoppedStageChild(t, "environment",
			"stopped-stage replay child requires explicit local Linux Docker permission")
	}
	docker, err := exec.LookPath("docker")
	if err == nil {
		docker, err = filepath.Abs(docker)
	}
	value, manifestErr := readLiveGatewayRebindStoppedStageReplayManifest(path, docker)
	if err != nil || manifestErr != nil {
		failLiveGatewayRebindStoppedStageChild(t, "manifest", "read exact private stopped-stage replay manifest")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	db, err := database.Open(value.DataRoot)
	if err != nil {
		failLiveGatewayRebindStoppedStageChild(t, "database", "open real SQLite for stopped-stage replay")
	}
	databaseOpen := true
	defer func() {
		if databaseOpen {
			_ = db.Close()
		}
	}()
	repository := appaccess.New(db)
	runner := &runtimeprocess.ExecRunner{}
	manager, err := New(runner, Options{
		DataRoot: value.DataRoot, WorkingDirectory: value.WorkingDirectory,
		DockerExecutable: value.DockerExecutable, DockerConfigDirectory: value.DockerConfigDirectory,
		HostPort: value.HostPort, CommandTimeout: 45 * time.Second, PullTimeout: 5 * time.Minute,
		OutputLimit: liveDockerOutputLimit, RebindFenceCheck: repository.CheckGatewayRebindFence,
		RebindCurrentStateRepository: repository,
	})
	if err != nil {
		failLiveGatewayRebindStoppedStageChild(t, "manager", "construct fresh-process stopped-stage replay manager")
	}
	outcome := run(&liveGatewayRebindStoppedStageReplayChildProcess{
		ctx: ctx, value: value, repository: repository, manager: manager,
	})
	if outcome.stage != "" {
		failLiveGatewayRebindStoppedStageChild(t, outcome.stage, outcome.message)
	}
	if err := db.Close(); err != nil {
		failLiveGatewayRebindStoppedStageChild(t, "database", "close stopped-stage replay database")
	}
	databaseOpen = false
	fmt.Println(pair.successMarker)
}

func TestLiveGatewayRebindStoppedStageContainerReplayChild(t *testing.T) {
	path := os.Getenv(liveGatewayRebindStoppedStageReplayEnvironment)
	if path == "" {
		t.Skip("stopped-stage replay child requires a parent-owned manifest")
	}
	if os.Getenv("RIG_RUN_LIVE_GATEWAY_V2") != "1" || runtime.GOOS != "linux" ||
		os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		failLiveGatewayRebindStoppedStageChild(t, "environment",
			"stopped-stage replay child requires explicit local Linux Docker permission")
	}
	docker, err := exec.LookPath("docker")
	if err == nil {
		docker, err = filepath.Abs(docker)
	}
	value, manifestErr := readLiveGatewayRebindStoppedStageReplayManifest(path, docker)
	if err != nil || manifestErr != nil {
		failLiveGatewayRebindStoppedStageChild(t, "manifest", "read exact private stopped-stage replay manifest")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	db, err := database.Open(value.DataRoot)
	if err != nil {
		failLiveGatewayRebindStoppedStageChild(t, "database", "open real SQLite for stopped-stage replay")
	}
	databaseOpen := true
	defer func() {
		if databaseOpen {
			_ = db.Close()
		}
	}()
	repository := appaccess.New(db)
	runner := &runtimeprocess.ExecRunner{}
	manager, err := New(runner, Options{
		DataRoot: value.DataRoot, WorkingDirectory: value.WorkingDirectory,
		DockerExecutable: value.DockerExecutable, DockerConfigDirectory: value.DockerConfigDirectory,
		HostPort: value.HostPort, CommandTimeout: 45 * time.Second, PullTimeout: 5 * time.Minute,
		OutputLimit: liveDockerOutputLimit, RebindFenceCheck: repository.CheckGatewayRebindFence,
		RebindCurrentStateRepository: repository,
	})
	if err != nil {
		failLiveGatewayRebindStoppedStageChild(t, "manager", "construct fresh-process stopped-stage replay manager")
	}
	before, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(before.Intents) != 1 || len(before.Progress) != 6 ||
		before.Intents[0].Intent.OperationID != value.OperationID ||
		before.Progress[5].Record.OperationID != value.OperationID || before.Progress[5].Record.Sequence != 6 ||
		before.Progress[5].Record.Stage == nil || before.Progress[5].Record.Stage.StageContainer == nil {
		failLiveGatewayRebindStoppedStageChild(t, "source",
			"stopped-stage replay child did not select the exact sequence-six source")
	}
	beforeBytes, err := readLiveGatewayRebindStoppedStageProgressBytes(before)
	if err != nil {
		failLiveGatewayRebindStoppedStageChild(t, "history",
			"read stopped-stage replay protected history bytes")
	}
	intent := before.Intents[0].Intent
	stage := *before.Progress[5].Record.Stage
	binding := stage.StageContainer
	driver := &liveGatewayRebindStoppedStageReplayDriver{
		managerGatewayRebindStageContainerDriver: managerGatewayRebindStageContainerDriver{manager: manager},
	}
	reads := liveGatewayRebindStageProductionReads(manager)
	if err := manager.stageGatewayRebindSuccessorContainerWithDriver(ctx, repository, reads,
		manager.inspectGatewayRebindDocker, driver, time.Now().UTC(), nil); err != nil {
		failLiveGatewayRebindStoppedStageChild(t, "replay",
			"production stopped-stage replay refused the exact sequence-six source")
	}
	if driver.createCalls != 0 {
		failLiveGatewayRebindStoppedStageChild(t, "create",
			"sequence-six stopped-stage replay dispatched a container create")
	}
	after, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	afterBytes, bytesErr := readLiveGatewayRebindStoppedStageProgressBytes(after)
	if err != nil || bytesErr != nil || !reflect.DeepEqual(before, after) ||
		!reflect.DeepEqual(beforeBytes, afterBytes) {
		failLiveGatewayRebindStoppedStageChild(t, "history",
			"fresh-process stopped-stage replay changed protected history")
	}
	physical, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageContainerObservation(intent, stage, physical, binding) ||
		!validOwnedNameSet(physical.OwnedContainers, intent.Intent.Identity.StageContainer) {
		failLiveGatewayRebindStoppedStageChild(t, "physical",
			"fresh-process stopped-stage replay did not retain exact physical proof")
	}
	delta, err := readGatewayRebindStageContainerPhysicalObservation(ctx, reads, driver, intent, stage, binding)
	if err != nil || delta.StageContainer == nil || *delta.StageContainer != *binding {
		failLiveGatewayRebindStoppedStageChild(t, "receipt",
			"fresh-process stopped-stage replay did not retain its exact physical receipt")
	}
	if err := db.Close(); err != nil {
		failLiveGatewayRebindStoppedStageChild(t, "database", "close stopped-stage replay database")
	}
	databaseOpen = false
	fmt.Println(liveGatewayRebindStoppedStageReplayMarker)
}

func newLiveGatewayRebindStoppedStageManifestFixture(t *testing.T) (
	liveGatewayRebindStoppedStageReplayManifest, string,
) {
	t.Helper()
	root := newLiveGatewayV2FixtureRoot(t)
	dataRoot := filepath.Join(root, "ingress-state")
	if err := os.Mkdir(dataRoot, 0o700); err != nil {
		t.Fatal("create stopped-stage protocol data root")
	}
	builder, err := securetemp.NewGeneratedBuilderDirectory(dataRoot)
	if err != nil {
		t.Fatal("create stopped-stage protocol builder root")
	}
	dockerConfig, err := builder.EnsureDirectory("controller-docker-config")
	if err != nil {
		t.Fatal("create stopped-stage protocol Docker config")
	}
	working, err := builder.EnsureDirectory("controller-working")
	if err != nil {
		t.Fatal("create stopped-stage protocol working directory")
	}
	docker := filepath.Join(root, "docker")
	if err := os.WriteFile(docker, []byte("fixture"), 0o700); err != nil {
		t.Fatal("create stopped-stage protocol Docker executable")
	}
	control, err := os.MkdirTemp(root, "stopped-stage-replay-")
	if err != nil || os.Chmod(control, 0o700) != nil {
		t.Fatal("create stopped-stage protocol control directory")
	}
	return liveGatewayRebindStoppedStageReplayManifest{
		Version: 1, Purpose: liveGatewayRebindStoppedStageReplayPurpose,
		FixtureRoot: root, ControlDirectory: control, DataRoot: dataRoot,
		WorkingDirectory: working, DockerExecutable: docker, DockerConfigDirectory: dockerConfig,
		OperationID: "d3333333-3333-4333-8333-333333333333", HostPort: 23456,
	}, docker
}

func TestGatewayRebindStoppedStageReplayChildProtocolIsClosed(t *testing.T) {
	t.Run("finite selector marker pairing", func(t *testing.T) {
		known := []liveGatewayRebindStoppedStageReplayPair{
			{testName: liveGatewayRebindStoppedStageReplayTest, successMarker: liveGatewayRebindStoppedStageReplayMarker},
			{testName: liveGatewayRebindDataVolumeReplayTest, successMarker: liveGatewayRebindDataVolumeReplayMarker},
			{testName: liveGatewayRebindStageConfigAdoptionTest, successMarker: liveGatewayRebindStageConfigAdoptionMarker},
			{testName: liveGatewayRebindStageConfigReplayTest, successMarker: liveGatewayRebindStageConfigReplayMarker},
			{testName: liveGatewayRebindFinalConfigAdoptionTest, successMarker: liveGatewayRebindFinalConfigAdoptionMarker},
			{testName: liveGatewayRebindFinalConfigReplayTest, successMarker: liveGatewayRebindFinalConfigReplayMarker},
			{testName: liveGatewayRebindStageStartAdoptionTest, successMarker: liveGatewayRebindStageStartAdoptionMarker},
			{testName: liveGatewayRebindStageStartReplayTest, successMarker: liveGatewayRebindStageStartReplayMarker},
		}
		for _, pair := range known {
			if !liveGatewayRebindStoppedStageReplayPairAllowed(pair) {
				t.Fatalf("known selector-marker pair was refused: %+v", pair)
			}
		}
		for _, pair := range []liveGatewayRebindStoppedStageReplayPair{
			{testName: liveGatewayRebindDataVolumeReplayTest, successMarker: liveGatewayRebindStageConfigReplayMarker},
			{testName: "TestLiveGatewayRebindUnknownReplayChild", successMarker: liveGatewayRebindDataVolumeReplayMarker},
			{testName: liveGatewayRebindFinalConfigReplayTest, successMarker: "GATEWAY_REBIND_UNKNOWN_OK"},
		} {
			if liveGatewayRebindStoppedStageReplayPairAllowed(pair) {
				t.Fatalf("crossed or unknown selector-marker pair was accepted: %+v", pair)
			}
		}
	})

	t.Run("exact private manifest", func(t *testing.T) {
		value, docker := newLiveGatewayRebindStoppedStageManifestFixture(t)
		path := filepath.Join(value.ControlDirectory, "manifest.json")
		if value.Version != 1 || value.Purpose != liveGatewayRebindStoppedStageReplayPurpose ||
			!validCanonicalUUID(value.OperationID) || value.HostPort == 0 || !filepath.IsAbs(docker) {
			t.Fatal("exact stopped-stage replay scalar fixture is invalid")
		}
		for index, directory := range []string{
			value.FixtureRoot, value.ControlDirectory, value.DataRoot,
			filepath.Join(value.DataRoot, "runtime"), filepath.Join(value.DataRoot, "runtime", "generated-builder"),
			value.WorkingDirectory, value.DockerConfigDirectory,
		} {
			if !liveGatewayRebindStoppedStageExactDirectory(directory) {
				t.Fatalf("exact stopped-stage replay directory %d is invalid", index)
			}
		}
		var err error
		path, err = writeLiveGatewayRebindStoppedStageReplayManifest(value, docker)
		got, readErr := readLiveGatewayRebindStoppedStageReplayManifest(path, docker)
		if err != nil || readErr != nil || got != value {
			t.Fatalf("exact stopped-stage replay manifest did not round trip: write=%v read=%v", err, readErr)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "unknown field", mutate: func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"hostPort":23456}`), []byte(`"hostPort":23456,"unknown":true}`), 1)
		}},
		{name: "duplicate field", mutate: func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
		}},
		{name: "case folded field", mutate: func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"version":1`), []byte(`"Version":1`), 1)
		}},
		{name: "wrong field type", mutate: func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"hostPort":23456`), []byte(`"hostPort":"23456"`), 1)
		}},
		{name: "trailing input", mutate: func(body []byte) []byte { return append(body, []byte(` {}`)...) }},
		{name: "malformed input", mutate: func([]byte) []byte { return []byte(`{"version":`) }},
		{name: "oversized input", mutate: func([]byte) []byte {
			return bytes.Repeat([]byte{'x'}, liveGatewayRebindStoppedStageManifestLimit+1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, docker := newLiveGatewayRebindStoppedStageManifestFixture(t)
			body, err := json.Marshal(value)
			path := filepath.Join(value.ControlDirectory, "manifest.json")
			candidate := test.mutate(body)
			writeErr := writeLiveGatewayRebindStoppedStageManifestFile(path, candidate)
			if test.name == "oversized input" {
				writeErr = os.WriteFile(path, candidate, 0o600)
			}
			if err != nil || writeErr != nil {
				t.Fatal("write invalid stopped-stage replay manifest fixture")
			}
			if _, err := readLiveGatewayRebindStoppedStageReplayManifest(path, docker); err == nil {
				t.Fatal("invalid stopped-stage replay manifest was accepted")
			}
		})
	}

	t.Run("crossed data root", func(t *testing.T) {
		value, docker := newLiveGatewayRebindStoppedStageManifestFixture(t)
		value.DataRoot = t.TempDir()
		body, err := json.Marshal(value)
		path := filepath.Join(value.ControlDirectory, "manifest.json")
		if err != nil || writeLiveGatewayRebindStoppedStageManifestFile(path, body) != nil {
			t.Fatal("write crossed stopped-stage replay manifest fixture")
		}
		if _, err := readLiveGatewayRebindStoppedStageReplayManifest(path, docker); err == nil {
			t.Fatal("crossed stopped-stage replay manifest was accepted")
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("broad fixture root is refused without repair", func(t *testing.T) {
			value, docker := newLiveGatewayRebindStoppedStageManifestFixture(t)
			if err := os.Chmod(value.FixtureRoot, 0o755); err != nil {
				t.Fatal("broaden stopped-stage replay fixture root")
			}
			path := filepath.Join(value.ControlDirectory, "manifest.json")
			if _, err := writeLiveGatewayRebindStoppedStageReplayManifest(value, docker); err == nil {
				t.Fatal("broad stopped-stage replay fixture root was accepted")
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("refused stopped-stage replay manifest was created")
			}
			info, err := os.Stat(value.FixtureRoot)
			if err != nil || info.Mode().Perm() != 0o755 {
				t.Fatal("stopped-stage replay manifest writer repaired a broad fixture root")
			}
		})

		t.Run("broad control directory is refused without repair", func(t *testing.T) {
			value, docker := newLiveGatewayRebindStoppedStageManifestFixture(t)
			path, err := writeLiveGatewayRebindStoppedStageReplayManifest(value, docker)
			if err != nil || os.Chmod(value.ControlDirectory, 0o755) != nil {
				t.Fatal("broaden stopped-stage replay control directory")
			}
			if _, err := readLiveGatewayRebindStoppedStageReplayManifest(path, docker); err == nil {
				t.Fatal("broad stopped-stage replay control directory was accepted")
			}
			info, err := os.Stat(value.ControlDirectory)
			if err != nil || info.Mode().Perm() != 0o755 {
				t.Fatal("stopped-stage replay manifest reader repaired a broad control directory")
			}
		})
	}

	pass := "=== RUN   " + liveGatewayRebindStoppedStageReplayTest + "\n" +
		liveGatewayRebindStoppedStageReplayMarker + "\n--- PASS: " +
		liveGatewayRebindStoppedStageReplayTest + " (0.01s)\nPASS\n"
	for _, test := range []struct {
		name   string
		output []byte
		want   bool
	}{
		{name: "exact", output: []byte(pass), want: true},
		{name: "missing marker", output: []byte(strings.Replace(pass, liveGatewayRebindStoppedStageReplayMarker+"\n", "", 1))},
		{name: "duplicate marker", output: []byte(strings.Replace(pass, liveGatewayRebindStoppedStageReplayMarker+"\n", liveGatewayRebindStoppedStageReplayMarker+"\n"+liveGatewayRebindStoppedStageReplayMarker+"\n", 1))},
		{name: "tainted marker", output: []byte(strings.Replace(pass, liveGatewayRebindStoppedStageReplayMarker, liveGatewayRebindStoppedStageReplayMarker+":private", 1))},
		{name: "skip", output: []byte(strings.Replace(pass, "--- PASS:", "--- SKIP:", 1))},
		{name: "fail", output: []byte(strings.Replace(pass, "--- PASS:", "--- FAIL:", 1))},
		{name: "failure token", output: []byte(strings.Replace(pass, liveGatewayRebindStoppedStageReplayMarker,
			liveGatewayRebindStoppedStageFailurePrefix+"manifest", 1))},
		{name: "oversized", output: bytes.Repeat([]byte{'x'}, liveGatewayRebindStoppedStageOutputLimit+1)},
	} {
		t.Run("marker "+test.name, func(t *testing.T) {
			if got := liveGatewayRebindStoppedStageChildCompleted(test.output); got != test.want {
				t.Fatalf("closed stopped-stage replay marker accepted=%t want=%t", got, test.want)
			}
		})
	}
	for _, test := range []struct {
		name   string
		output string
		want   string
	}{
		{name: "exact", output: liveGatewayRebindStoppedStageFailurePrefix + "manifest\n", want: "manifest"},
		{name: "crossed", output: liveGatewayRebindStoppedStageFailurePrefix + "manifest\n" +
			liveGatewayRebindStoppedStageFailurePrefix + "database\n", want: "ambiguous"},
		{name: "tainted", output: liveGatewayRebindStoppedStageFailurePrefix + "manifest:private\n", want: "unknown"},
		{name: "malformed", output: liveGatewayRebindStoppedStageFailurePrefix + "private\n", want: "unknown"},
		{name: "absent", output: "private\n", want: "unknown"},
	} {
		t.Run("failure "+test.name, func(t *testing.T) {
			if got := liveGatewayRebindStoppedStageChildFailure([]byte(test.output)); got != test.want {
				t.Fatalf("closed stopped-stage replay failure=%q want=%q", got, test.want)
			}
		})
	}
	t.Run("capture refuses overflow", func(t *testing.T) {
		capture := &liveGatewayRebindStoppedStageOutputCapture{}
		first := bytes.Repeat([]byte{'x'}, liveGatewayRebindStoppedStageOutputLimit)
		if written, err := capture.Write(first); err != nil || written != len(first) {
			t.Fatal("bounded stopped-stage child capture refused in-bound output")
		}
		if written, err := capture.Write([]byte{'x'}); err != nil || written != 1 {
			t.Fatal("bounded stopped-stage child capture did not consume overflow")
		}
		body, overflow := capture.result()
		if !overflow || len(body) != liveGatewayRebindStoppedStageOutputLimit {
			t.Fatal("bounded stopped-stage child capture did not retain a closed overflow state")
		}
	})
	t.Run("new marker pair requires its exact child pass", func(t *testing.T) {
		pair := liveGatewayRebindStoppedStageReplayPair{
			testName: liveGatewayRebindDataVolumeReplayTest, successMarker: liveGatewayRebindDataVolumeReplayMarker,
		}
		pass := "=== RUN   " + pair.testName + "\n" + pair.successMarker + "\n--- PASS: " + pair.testName + " (0.01s)\nPASS\n"
		if !liveGatewayRebindStoppedStageChildCompletedForPair([]byte(pass), pair) {
			t.Fatal("exact new selector-marker child pass was rejected")
		}
		crossed := pass + liveGatewayRebindStageConfigReplayMarker + "\n"
		if liveGatewayRebindStoppedStageChildCompletedForPair([]byte(crossed), pair) {
			t.Fatal("selected child pass with another replay marker was accepted")
		}
		tainted := pass + pair.successMarker + ":tainted\n"
		if liveGatewayRebindStoppedStageChildCompletedForPair([]byte(tainted), pair) {
			t.Fatal("selected child pass with a tainted replay marker was accepted")
		}
	})
	t.Run("cancellation child is observed and reaped", func(t *testing.T) {
		value, docker := newLiveGatewayRebindStoppedStageManifestFixture(t)
		manifest, err := writeLiveGatewayRebindStoppedStageReplayManifest(value, docker)
		if err != nil {
			t.Fatal("write stopped-stage cancellation manifest")
		}
		pair := liveGatewayRebindStoppedStageReplayPair{
			testName:      liveGatewayRebindStoppedStageCancellationTest,
			successMarker: liveGatewayRebindStoppedStageCancellationSuccessMarker,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command, err := liveGatewayRebindStoppedStageReplayChildCommand(ctx, pair, manifest)
		if err != nil {
			t.Fatal("create stopped-stage cancellation child command")
		}
		stdoutCapture := &liveGatewayRebindStoppedStageOutputCapture{}
		stderrCapture := &liveGatewayRebindStoppedStageOutputCapture{}
		started := make(chan struct{}, 1)
		command.Stdout = &liveGatewayRebindStoppedStageCancellationOutputWriter{
			capture: stdoutCapture, marker: []byte(liveGatewayRebindStoppedStageCancellationStartedMarker), ready: started,
		}
		command.Stderr = stderrCapture
		if err := command.Start(); err != nil {
			t.Fatal("start stopped-stage cancellation child")
		}
		waitDone := make(chan error, 1)
		go func() { waitDone <- command.Wait() }()
		waitForExit := func() (error, bool) {
			cancel()
			select {
			case waitErr := <-waitDone:
				return waitErr, true
			case <-time.After(5 * time.Second):
				_ = command.Process.Kill()
				select {
				case waitErr := <-waitDone:
					return waitErr, true
				case <-time.After(5 * time.Second):
					return nil, false
				}
			}
		}
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			waitErr, reaped := waitForExit()
			if !reaped {
				t.Fatal("stopped-stage cancellation child did not report readiness or reap")
			}
			t.Fatalf("stopped-stage cancellation child readiness timed out: wait=%v", waitErr)
		}
		waitErr, reaped := waitForExit()
		if !reaped {
			t.Fatal("stopped-stage cancellation child did not return after cancellation")
		}
		if waitErr == nil || command.ProcessState == nil {
			t.Fatal("stopped-stage cancellation child did not return a cancellation error with a waited process state")
		}
		output, outputOverflow := stdoutCapture.result()
		errorOutput, errorOverflow := stderrCapture.result()
		if outputOverflow || errorOverflow {
			t.Fatal("stopped-stage cancellation child output exceeded the bounded capture")
		}
		combined := append(append([]byte(nil), output...), errorOutput...)
		if bytes.Count(combined, []byte(liveGatewayRebindStoppedStageCancellationStartedMarker)) != 1 ||
			bytes.Contains(combined, []byte(liveGatewayRebindStoppedStageCancellationSuccessMarker)) ||
			bytes.Contains(combined, []byte("--- PASS: "+pair.testName+" ")) {
			t.Fatal("stopped-stage cancellation child emitted an invalid completion protocol")
		}
	})
}

// TestLiveGatewayRebindSuccessorStoppedStageContainer proves that the fourth
// private successor effect creates one exact, stopped container on a
// disposable default-local Linux Docker daemon. It must remain inert until a
// later, separately guarded cutover stage.
func TestLiveGatewayRebindSuccessorStoppedStageContainer(t *testing.T) {
	fixture := newLiveGatewayV2Fixture(t, liveGatewayV2FixtureSpec{
		appID:            "d1111111-1111-4111-8111-111111111111",
		planID:           "d2222222-2222-4222-8222-222222222222",
		operationID:      "d3333333-3333-4333-8333-333333333333",
		profileRevision:  "d4444444-4444-4444-8444-444444444444",
		approvedBy:       gatewayRebindTestAdministrator,
		imageTag:         "rig-generated-gateway-v2-live:rebind-stopped-stage-container",
		applicationReply: "gateway-v2-rebind-stopped-stage-container",
		countRequests:    true,
	})
	db, repository := fixture.db, fixture.repository
	profile := prepareLiveGatewayRebindStagePredecessor(t, fixture, db, repository)
	proposal := seedLiveGatewayRebindPublicPassiveLineage(t, fixture, db, repository, profile,
		appaccess.GatewayProfileSpec{
			SelectedIPv4: fixture.request.Profile.SelectedIPv4,
			InterfaceID:  fixture.request.Profile.InterfaceID,
			PortStart:    fixture.port,
			PortEnd:      fixture.port,
		})
	predecessorState, predecessorJournal, predecessorStore := liveGatewayV2LoadDurableOperation(t, fixture)
	preclaim, err := repository.GatewayRebindPreclaimSnapshot(fixture.ctx, proposal)
	if err != nil || !gatewayRebindPreclaimMatches(preclaim, predecessorState, predecessorJournal) {
		t.Fatal("live stopped-container successor did not pass zero-claim admission")
	}
	insertLiveGatewayRebindPublicPassiveClaim(t, db, preclaim)
	prepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !gatewayRebindPredecessorMatches(prepared, predecessorState, predecessorJournal) {
		t.Fatal("live stopped-container prepared claim did not bind predecessor")
	}

	reads := liveGatewayRebindStageProductionReads(fixture.ingress)
	publicPreflight, err := fixture.ingress.InspectGatewayRebindSuccessorPreflight(fixture.ctx, repository)
	if err != nil {
		t.Fatal("production stopped-container successor preflight rejected present host interface")
	}
	observation, err := readGatewayRebindSuccessorPreflightObservation(fixture.ctx, repository, reads)
	if err != nil || !reflect.DeepEqual(publicPreflight, observation.result) {
		t.Fatal("stopped-container protected-intent observation differed from production preflight")
	}
	predecessor := gatewayUpgradeGenerationSelection{
		Store: predecessorStore, Generation: predecessorStore.generation,
		State: predecessorState, Journal: predecessorJournal, Existing: true,
		operationID: predecessorJournal.OperationID,
	}
	selection, err := fixture.ingress.resolveGatewayRebindProtectedIntentLocked(proposal.Spec.OperationID)
	if err != nil {
		t.Fatal("reserve stopped-container successor intent generation")
	}
	intent, err := newGatewayRebindProtectedIntent(prepared, predecessor, observation, selection.Generation)
	if err != nil || selection.Store.installExact(intent) != nil {
		t.Fatal("install exact stopped-container successor protected intent")
	}

	networkDriver := managerGatewayRebindStageNetworkDriver{manager: fixture.ingress}
	image, found, err := networkDriver.inspectImage(fixture.ctx)
	if err != nil || !validGatewayPinnedImage(image, found) {
		t.Fatal("inspect exact pinned live gateway image")
	}
	baseTime := time.Now().UTC()
	first, err := newGatewayRebindSuccessorIntentProgress(intent, baseTime)
	if err != nil {
		t.Fatal("construct live stopped-container successor intent progress")
	}
	firstStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, first.Sequence)
	if err != nil || firstStore.installExact(fixture.ctx, first) != nil {
		t.Fatal("install live stopped-container successor intent progress")
	}
	second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindStageIntentObservation{
		OccurredAt:            baseTime.Add(time.Nanosecond),
		ObservedDockerImageID: normalizeID(image.ID),
		NetworkTopologyDigest: intent.NetworkObservationDigest,
	})
	if err != nil {
		t.Fatal("construct live stopped-container stage intent progress")
	}
	secondStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, second.Sequence)
	if err != nil || secondStore.installExact(fixture.ctx, second) != nil {
		t.Fatal("install live stopped-container stage intent progress")
	}

	var networkID string
	var configBinding *gatewayRebindStageConfigVolumeBinding
	var dataBinding *gatewayRebindStageDataVolumeBinding
	var containerBinding *gatewayRebindStageContainerBinding
	// This must be registered before the first Docker mutation. If an effect
	// becomes ambiguous, cleanup retains it and the CI residue gate fails.
	t.Cleanup(func() {
		cleanupLiveGatewayRebindStoppedStageContainerChain(t, fixture, intent, networkID,
			configBinding, dataBinding, containerBinding)
	})

	beforeRoute, err := fixture.ingress.store.load()
	if err != nil {
		t.Fatal("load predecessor route before stopped-container stage")
	}
	beforeDocker, err := fixture.ingress.inspectGatewayRebindDocker(
		fixture.ctx, beforeRoute, predecessorState, predecessorJournal)
	if err != nil || !validGatewayRebindPredecessorDocker(beforeRoute, predecessorState, predecessorJournal, beforeDocker) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("inspect exact predecessor Docker state before stopped-container stage")
	}
	beforeDockerDigest, err := gatewayRebindEffectBoundaryDockerDigest(beforeDocker, predecessorState.Identity)
	clearGatewayV2DockerObservation(&beforeDocker)
	if err != nil {
		t.Fatal("digest exact predecessor Docker state before stopped-container stage")
	}
	baseline := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	settled := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	if baseline.Routed == 0 || baseline.Routed != settled.Routed {
		t.Fatal("counted predecessor requests did not settle before stopped-container stage")
	}

	if err := fixture.ingress.stageGatewayRebindSuccessorNetwork(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(2*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind successor network before stopped container", err)
	}
	networkHistory, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(networkHistory.Progress) != 3 || networkHistory.Progress[2].Record.Stage == nil ||
		networkHistory.Progress[2].Record.Stage.Network == nil {
		t.Fatal("live successor network did not bind exact sequence three")
	}
	networkID = networkHistory.Progress[2].Record.Stage.Network.ID
	if err := fixture.ingress.stageGatewayRebindSuccessorConfigVolume(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(3*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind successor config volume before stopped container", err)
	}
	configHistory, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(configHistory.Progress) != 4 || configHistory.Progress[3].Record.Stage == nil ||
		configHistory.Progress[3].Record.Stage.ConfigVolume == nil {
		t.Fatal("live successor config volume did not bind exact sequence four")
	}
	configBinding = configHistory.Progress[3].Record.Stage.ConfigVolume
	if err := fixture.ingress.stageGatewayRebindSuccessorDataVolume(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(4*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind successor data volume before stopped container", err)
	}
	beforeContainer, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(beforeContainer.Progress) != 5 || beforeContainer.Progress[4].Record.Stage == nil ||
		beforeContainer.Progress[4].Record.Stage.DataVolume == nil {
		t.Fatal("live successor data volume did not bind exact sequence five")
	}
	dataBinding = beforeContainer.Progress[4].Record.Stage.DataVolume
	beforeContainerBytes := liveGatewayRebindProgressBytes(t, beforeContainer)
	dataDriver := managerGatewayRebindStageDataVolumeDriver{manager: fixture.ingress}
	dataObservation, err := dataDriver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageDataVolumeObservation(intent, dataObservation, networkID,
		*configBinding, dataBinding) {
		t.Fatal("live successor data volume was not exact before stopped-container stage")
	}
	dataDelta, err := readGatewayRebindStageDataVolumePhysicalObservation(
		fixture.ctx, reads, dataDriver, intent, networkID, *configBinding, dataBinding)
	if err != nil || dataDelta.DataVolume == nil || *dataDelta.DataVolume != *dataBinding {
		t.Fatal("live successor data volume lacked its exact physical receipt")
	}
	containerDriver := managerGatewayRebindStageContainerDriver{manager: fixture.ingress}
	absent, err := containerDriver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageContainerObservation(intent,
		*beforeContainer.Progress[4].Record.Stage, absent, nil) {
		t.Fatal("stopped container or another successor container existed before sequence six")
	}

	if err := fixture.ingress.stageGatewayRebindSuccessorContainer(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(5*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind stopped successor container", err)
	}
	boundHistory, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(boundHistory.Progress) != 6 {
		t.Fatal("live stopped successor container did not install sequence six")
	}
	for i := range beforeContainer.Progress {
		if !reflect.DeepEqual(beforeContainer.Progress[i], boundHistory.Progress[i]) {
			t.Fatal("stopped-container stage changed an earlier protected record")
		}
	}
	boundBytes := liveGatewayRebindProgressBytes(t, boundHistory)
	if !reflect.DeepEqual(beforeContainerBytes, boundBytes[:len(beforeContainerBytes)]) {
		t.Fatal("stopped-container stage changed earlier protected record bytes")
	}
	bound := boundHistory.Progress[5].Record
	ownershipDigest, ownershipErr := gatewayRebindStageContainerOwnershipDigest(intent)
	configurationDigest, configurationErr := gatewayRebindStageContainerConfigurationDigest(intent,
		*beforeContainer.Progress[4].Record.Stage)
	if ownershipErr != nil || configurationErr != nil || bound.PreviousDigest != beforeContainer.Progress[4].Record.Digest ||
		bound.Stage == nil || bound.Stage.StageContainer == nil ||
		bound.Stage.StageContainer.OwnershipDigest != ownershipDigest ||
		bound.Stage.StageContainer.ConfigurationDigest != configurationDigest ||
		!validContainerID(bound.Stage.StageContainer.ID) {
		t.Fatal("live stopped-container progress has an invalid exact binding")
	}
	withoutContainer := *bound.Stage
	withoutContainer.StageContainer = nil
	if !reflect.DeepEqual(withoutContainer, *beforeContainer.Progress[4].Record.Stage) {
		t.Fatal("stopped-container binding changed the prepared successor network or volumes")
	}
	containerBinding = bound.Stage.StageContainer
	physical, err := containerDriver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageContainerObservation(intent, *bound.Stage, physical, containerBinding) ||
		physical.StageContainer.Running || physical.StageContainer.Restarting || physical.StageRuntime.Paused ||
		physical.StageRuntime.Dead || physical.StageRuntime.RestartCount != 0 ||
		gatewayV2HasEffectivePortBinding(physical.StageRuntime.EffectivePortBindings) {
		t.Fatal("production Docker did not expose an exact inert stopped successor container")
	}
	configured, configuredOK := physical.StageRuntime.ConfiguredNetworks[intent.Intent.Identity.IngressNetwork]
	if !configuredOK || configured.EndpointID != "" || configured.IPAddress != "" ||
		configured.IPAMConfig == nil || configured.IPAMConfig.IPv4Address != intent.Intent.Network.ContainerIPv4 ||
		!validGatewayRebindStageContainerPortBindings(physical.StageContainer.PortBindings, intent) {
		t.Fatal("stopped successor container lost its exact configured network or port settings")
	}
	createdDelta, err := readGatewayRebindStageContainerPhysicalObservation(
		fixture.ctx, reads, containerDriver, intent, *bound.Stage, containerBinding)
	if err != nil || createdDelta.StageContainer == nil || *createdDelta.StageContainer != *containerBinding ||
		createdDelta.ConfigVolume != *configBinding || createdDelta.DataVolume != *dataBinding ||
		createdDelta.NetworkID != dataDelta.NetworkID ||
		createdDelta.NetworkOwnershipDigest != dataDelta.NetworkOwnershipDigest ||
		!reflect.DeepEqual(createdDelta.Candidates, dataDelta.Candidates) ||
		!reflect.DeepEqual(createdDelta.HostRoutes, dataDelta.HostRoutes) ||
		!reflect.DeepEqual(createdDelta.HostInterfaces, dataDelta.HostInterfaces) ||
		!reflect.DeepEqual(createdDelta.DockerIDs, dataDelta.DockerIDs) ||
		!reflect.DeepEqual(createdDelta.DockerPrefixes, dataDelta.DockerPrefixes) {
		t.Fatal("stopped-container creation changed the successor host topology or volume receipts")
	}

	runLiveGatewayRebindStoppedStageReplayChild(t, fixture, intent)
	restartReads := liveGatewayRebindStageProductionReads(fixture.ingress)
	replayedHistory, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !reflect.DeepEqual(boundHistory, replayedHistory) ||
		!reflect.DeepEqual(boundBytes, liveGatewayRebindProgressBytes(t, replayedHistory)) {
		t.Fatal("live stopped-container replay changed protected records or bytes")
	}
	replayedDriver := managerGatewayRebindStageContainerDriver{manager: fixture.ingress}
	replayed, err := replayedDriver.inspect(fixture.ctx, intent)
	if err != nil {
		t.Fatal("fresh-process stopped-container replay inspection failed")
	}
	if !equalGatewayRebindStageContainerSnapshot(intent, *bound.Stage, containerBinding, physical, replayed) {
		t.Fatal("fresh-process stopped-container replay physical snapshot changed")
	}
	if !validGatewayRebindStageContainerObservation(intent, *bound.Stage, replayed, containerBinding) {
		t.Fatal("fresh-process stopped-container replay exact binding proof failed")
	}
	if !validOwnedNameSet(replayed.OwnedContainers, intent.Intent.Identity.StageContainer) {
		t.Fatal("fresh-process stopped-container replay owned-container census changed")
	}
	replayedDelta, err := readGatewayRebindStageContainerPhysicalObservation(
		fixture.ctx, restartReads, replayedDriver, intent, *bound.Stage, containerBinding)
	if err != nil || !reflect.DeepEqual(createdDelta, replayedDelta) {
		t.Fatal("fresh-process replay changed the stopped-container physical observation")
	}

	afterPrepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !reflect.DeepEqual(prepared, afterPrepared) {
		t.Fatal("stopped-container stage changed prepared SQLite claim")
	}
	afterState, afterJournal, _ := liveGatewayV2LoadDurableOperation(t, fixture)
	if !reflect.DeepEqual(predecessorState, afterState) || !reflect.DeepEqual(predecessorJournal, afterJournal) {
		t.Fatal("stopped-container stage changed protected predecessor")
	}
	afterRoute, err := fixture.ingress.store.load()
	if err != nil || !reflect.DeepEqual(beforeRoute, afterRoute) {
		t.Fatal("stopped-container stage changed active application route")
	}
	afterDocker, err := fixture.ingress.inspectGatewayRebindDocker(
		fixture.ctx, afterRoute, afterState, afterJournal)
	if err != nil || !validGatewayRebindPredecessorDocker(afterRoute, afterState, afterJournal, afterDocker) {
		clearGatewayV2DockerObservation(&afterDocker)
		t.Fatal("stopped-container stage changed predecessor Docker resources")
	}
	afterDockerDigest, err := gatewayRebindEffectBoundaryDockerDigest(afterDocker, afterState.Identity)
	clearGatewayV2DockerObservation(&afterDocker)
	if err != nil || afterDockerDigest != beforeDockerDigest {
		t.Fatal("stopped-container stage changed predecessor Docker resources")
	}
	if got := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); got.Routed != baseline.Routed {
		t.Fatal("stopped-container stage or replay forwarded an application request")
	}
}

func cleanupLiveGatewayRebindStoppedStageContainerChain(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding, container *gatewayRebindStageContainerBinding,
) {
	t.Helper()
	if container == nil {
		cleanupLiveGatewayRebindStoppedStagePreContainer(t, fixture, intent, networkID, config, data)
		return
	}
	if !cleanupLiveGatewayRebindStoppedStageContainer(t, fixture, intent, networkID, config, data, container) {
		return
	}
	if !cleanupLiveGatewayRebindStoppedStageDataVolume(t, fixture, intent, networkID, config, data, container) {
		return
	}
	if !cleanupLiveGatewayRebindStoppedStageConfigVolume(t, fixture, intent, networkID, config, data, container) {
		return
	}
	cleanupLiveGatewayRebindStoppedStageNetwork(t, fixture, intent, networkID, config, data, container)
}

// Cleanup may run after a failure in one of the three earlier, fully bound
// stages. Those histories are already covered by their own exact cleanup
// helpers. A container not bound into sequence six is never adopted or
// cleaned: it is an ambiguous create-before-bind outcome and must remain for
// the residue gate to expose.
func cleanupLiveGatewayRebindStoppedStagePreContainer(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding,
) {
	t.Helper()
	if networkID == "" {
		return
	}
	containerDriver := managerGatewayRebindStageContainerDriver{manager: fixture.ingress}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	observed, err := containerDriver.inspect(ctx, intent)
	if err != nil || observed.StageContainerFound || len(observed.OwnedContainers) != 0 {
		t.Error("live stopped-container cleanup found an unbound or uncertain stage container; retaining resources")
		return
	}
	if data != nil {
		cleanupLiveGatewayRebindStageDataVolumeChain(t, fixture, intent, networkID, config, data)
		return
	}
	if config != nil {
		if observed.DataVolumeFound || !validGatewayRebindStageConfigVolumeObservation(intent,
			gatewayRebindStageConfigVolumeObservation{
				Network: observed.Network, NetworkID: observed.NetworkID, NetworkFound: observed.NetworkFound,
				ConfigVolume: observed.ConfigVolume, ConfigVolumeIdentity: observed.ConfigVolumeIdentity,
				ConfigVolumeFound: observed.ConfigVolumeFound, OwnedVolumes: observed.OwnedVolumes,
				OwnedNetworks: observed.OwnedNetworks,
			}, networkID, config) {
			t.Error("live stopped-container cleanup found uncertain sequence-four resources; retaining resources")
			return
		}
		cleanupLiveGatewayRebindStageConfigVolume(t, fixture, intent, networkID, config)
		postConfig, postConfigErr := containerDriver.inspect(ctx, intent)
		if postConfigErr != nil || postConfig.ConfigVolumeFound || postConfig.DataVolumeFound ||
			postConfig.StageContainerFound || len(postConfig.OwnedContainers) != 0 || len(postConfig.OwnedVolumes) != 0 {
			t.Error("live stopped-container cleanup retained config-volume residue; retaining network")
			return
		}
		cleanupLiveGatewayRebindStageNetwork(t, fixture, intent, networkID)
		return
	}
	if observed.ConfigVolumeFound || observed.DataVolumeFound || len(observed.OwnedVolumes) != 0 ||
		!validGatewayRebindStageNetworkObservation(intent, gatewayRebindStageNetworkObservation{
			Network: observed.Network, ID: observed.NetworkID, Found: observed.NetworkFound,
			OwnedNetworks: observed.OwnedNetworks,
		}, networkID) {
		t.Error("live stopped-container cleanup found uncertain sequence-three resources; retaining network")
		return
	}
	cleanupLiveGatewayRebindStageNetwork(t, fixture, intent, networkID)
}

func liveGatewayRebindStoppedStageCleanupLineage(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding, container *gatewayRebindStageContainerBinding,
) (gatewayRebindStageIntent, bool) {
	t.Helper()
	history, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || (len(history.Progress) != 6 && len(history.Progress) != 8 &&
		len(history.Progress) != 9 && len(history.Progress) != 10 && len(history.Progress) != 11 && len(history.Progress) != 12) ||
		!reflect.DeepEqual(history.Intents[0].Intent, intent) || !validContainerID(networkID) ||
		config == nil || data == nil || container == nil {
		t.Error("live stopped-container cleanup has no exact protected stage lineage; retaining resources")
		return gatewayRebindStageIntent{}, false
	}
	fifth := history.Progress[4].Record.Stage
	sixth := history.Progress[5].Record.Stage
	if fifth == nil || sixth == nil || fifth.Network == nil || fifth.ConfigVolume == nil || fifth.DataVolume == nil ||
		fifth.Network.ID != networkID || *fifth.ConfigVolume != *config || *fifth.DataVolume != *data ||
		sixth.StageContainer == nil || *sixth.StageContainer != *container ||
		history.Progress[5].Record.PreviousDigest != history.Progress[4].Record.Digest {
		t.Error("live stopped-container cleanup protected receipt does not match resource bindings; retaining resources")
		return gatewayRebindStageIntent{}, false
	}
	withoutContainer := *sixth
	withoutContainer.StageContainer = nil
	if !reflect.DeepEqual(withoutContainer, *fifth) {
		t.Error("live stopped-container cleanup found a sequence-six stage that changed prior receipts; retaining resources")
		return gatewayRebindStageIntent{}, false
	}
	if len(history.Progress) >= 8 {
		seventh := history.Progress[6].Record
		eighth := history.Progress[7].Record
		sixthRecord := history.Progress[5].Record
		intentBinding, intentErr := gatewayRebindStageConfigIntentBindingFor(intent, sixthRecord)
		copyBinding, copyErr := gatewayRebindStageConfigCopyBindingFor(intent, seventh)
		if intentErr != nil || copyErr != nil || seventh.PreviousDigest != sixthRecord.Digest ||
			seventh.Stage == nil || seventh.Stage.StageConfigIntent == nil ||
			*seventh.Stage.StageConfigIntent != intentBinding ||
			eighth.PreviousDigest != seventh.Digest || eighth.Stage == nil ||
			eighth.Stage.StageConfigCopy == nil || *eighth.Stage.StageConfigCopy != copyBinding {
			t.Error("live stopped-container cleanup has no exact protected sequence-seven and sequence-eight receipts; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
		withoutIntent := *seventh.Stage
		withoutIntent.StageConfigIntent = nil
		if !reflect.DeepEqual(withoutIntent, *sixth) {
			t.Error("live stopped-container cleanup found a sequence-seven stage that changed prior receipts; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
		withoutCopy := *eighth.Stage
		withoutCopy.StageConfigCopy = nil
		if !reflect.DeepEqual(withoutCopy, *seventh.Stage) {
			t.Error("live stopped-container cleanup found a sequence-eight stage that changed prior receipts; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
	}
	if len(history.Progress) >= 9 {
		eighth := history.Progress[7].Record
		ninth := history.Progress[8].Record
		binding, bindingErr := gatewayRebindStageStartIntentBindingFor(intent, eighth)
		if bindingErr != nil || ninth.PreviousDigest != eighth.Digest || ninth.Stage == nil ||
			ninth.Stage.StageStartIntent == nil || *ninth.Stage.StageStartIntent != binding {
			t.Error("live stopped-container cleanup has no exact protected sequence-nine start intent; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
		withoutStart := *ninth.Stage
		withoutStart.StageStartIntent = nil
		if !reflect.DeepEqual(withoutStart, *eighth.Stage) {
			t.Error("live stopped-container cleanup found a sequence-nine stage that changed prior receipts; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
	}
	if len(history.Progress) >= 10 {
		ninth := history.Progress[8].Record
		tenth := history.Progress[9].Record
		if tenth.PreviousDigest != ninth.Digest || tenth.Stage == nil || tenth.Stage.StageServing == nil ||
			!validGatewayRebindStageServingBinding(intent, ninth, *tenth.Stage.StageServing) {
			t.Error("live stopped-container cleanup has no exact protected sequence-ten serving receipt; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
		withoutServing := *tenth.Stage
		withoutServing.StageServing = nil
		if !reflect.DeepEqual(withoutServing, *ninth.Stage) {
			t.Error("live stopped-container cleanup found a sequence-ten stage that changed prior receipts; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
	}
	if len(history.Progress) >= 11 {
		tenth := history.Progress[9].Record
		eleventh := history.Progress[10].Record
		if eleventh.Stage == nil || eleventh.Stage.FinalConfigIntent == nil ||
			!validGatewayRebindFinalConfigIntentBinding(intent, tenth, *eleventh.Stage.FinalConfigIntent) {
			t.Error("live cleanup lacks exact sequence-eleven final config intent; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
		withoutFinalIntent := *eleventh.Stage
		withoutFinalIntent.FinalConfigIntent = nil
		if !reflect.DeepEqual(withoutFinalIntent, *tenth.Stage) {
			t.Error("live cleanup final config intent changed prior stage receipts; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
		if len(history.Progress) == 12 {
			twelfth := history.Progress[11].Record
			if twelfth.Stage == nil || twelfth.Stage.FinalConfigCopy == nil ||
				!validGatewayRebindFinalConfigCopyBinding(intent, eleventh, *twelfth.Stage.FinalConfigCopy) {
				t.Error("live cleanup lacks exact sequence-twelve copy receipt; retaining resources")
				return gatewayRebindStageIntent{}, false
			}
			withoutFinalCopy := *twelfth.Stage
			withoutFinalCopy.FinalConfigCopy = nil
			if !reflect.DeepEqual(withoutFinalCopy, *eleventh.Stage) {
				t.Error("live cleanup copy receipt changed prior stage receipts; retaining resources")
				return gatewayRebindStageIntent{}, false
			}
		}
	}
	return *sixth, true
}

func cleanupLiveGatewayRebindStoppedStageContainer(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding, binding *gatewayRebindStageContainerBinding,
) bool {
	t.Helper()
	stage, ok := liveGatewayRebindStoppedStageCleanupLineage(t, fixture, intent, networkID, config, data, binding)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	history, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Error("live stopped-container cleanup cannot reread protected lineage; retaining resources")
		return false
	}
	if len(history.Progress) >= 11 {
		if !liveGatewayRebindFinalConfigCleanupInventory(t, fixture, ctx, intent, history) {
			return false
		}
	} else if len(history.Progress) >= 8 {
		expected, expectedErr := gatewayRebindStageConfigBytes(intent)
		if expectedErr != nil || history.Progress[7].Record.Stage == nil {
			clear(expected)
			t.Error("live stopped-container cleanup cannot regenerate exact protected stage config; retaining resources")
			return false
		}
		defer clear(expected)
		inventoryStage := *history.Progress[7].Record.Stage
		if len(history.Progress) >= 9 {
			if history.Progress[8].Record.Stage == nil {
				t.Error("live stopped-container cleanup lacks its protected start intent; retaining resources")
				return false
			}
			// A proved stop can retain Caddy's exact autosave after a start.
			// The production reader requires the unchanged durable start intent
			// before allowing that snapshot alongside the approved stage file.
			inventoryStage = *history.Progress[8].Record.Stage
		}
		copyDriver := managerGatewayRebindStageStartIntentDriver{manager: fixture.ingress}
		inventory, inventoryErr := copyDriver.configVolumeInventory(ctx, intent, inventoryStage, expected)
		if inventoryErr != nil || inventory != gatewayRebindStageConfigInventoryExact {
			t.Error("live stopped-container cleanup cannot prove the exact authorized stage configuration; retaining resources")
			return false
		}
	}
	driver := managerGatewayRebindStageContainerDriver{manager: fixture.ingress}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageContainerObservation(intent, stage, observed, binding) ||
		observed.StageContainer.Running || observed.StageContainer.Restarting || observed.StageRuntime.Paused ||
		observed.StageRuntime.Dead || observed.StageRuntime.RestartCount != 0 ||
		gatewayV2HasEffectivePortBinding(observed.StageRuntime.EffectivePortBindings) {
		t.Error("live stopped-container cleanup cannot prove exact inert bound container; retaining resources")
		return false
	}
	result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
		45*time.Second, "container", "rm", binding.ID)
	clearLiveResult(&result)
	if removeErr != nil {
		t.Error("remove exact live stopped successor container without force")
		return false
	}
	withoutContainer := stage
	withoutContainer.StageContainer = nil
	residue, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageContainerObservation(intent, withoutContainer, residue, nil) {
		t.Error("exact live stopped-container cleanup left owned container or endpoint residue")
		return false
	}
	return true
}

func cleanupLiveGatewayRebindStoppedStageDataVolume(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding, container *gatewayRebindStageContainerBinding,
) bool {
	t.Helper()
	stage, ok := liveGatewayRebindStoppedStageCleanupLineage(t, fixture, intent, networkID, config, data, container)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	containerDriver := managerGatewayRebindStageContainerDriver{manager: fixture.ingress}
	withoutContainer := stage
	withoutContainer.StageContainer = nil
	containerObservation, err := containerDriver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageContainerObservation(intent, withoutContainer, containerObservation, nil) {
		t.Error("live data-volume cleanup cannot prove stopped container is absent; retaining resources")
		return false
	}
	driver := managerGatewayRebindStageDataVolumeDriver{manager: fixture.ingress}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageDataVolumeObservation(intent, observed, networkID, *config, data) {
		t.Error("live data-volume cleanup cannot prove exact protected ownership; retaining resources")
		return false
	}
	result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
		45*time.Second, "volume", "rm", data.Name)
	clearLiveResult(&result)
	if removeErr != nil {
		t.Error("remove exact live successor data volume after stopped container")
		return false
	}
	residue, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageDataVolumeObservation(intent, residue, networkID, *config, nil) {
		t.Error("exact live data-volume cleanup left owned residue")
		return false
	}
	return true
}

func cleanupLiveGatewayRebindStoppedStageConfigVolume(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding, container *gatewayRebindStageContainerBinding,
) bool {
	t.Helper()
	if _, ok := liveGatewayRebindStoppedStageCleanupLineage(t, fixture, intent, networkID, config, data, container); !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	containerDriver := managerGatewayRebindStageContainerDriver{manager: fixture.ingress}
	containerObservation, err := containerDriver.inspect(ctx, intent)
	if err != nil || containerObservation.StageContainerFound || len(containerObservation.OwnedContainers) != 0 ||
		containerObservation.DataVolumeFound {
		t.Error("live config-volume cleanup cannot prove container and data volume are absent; retaining resources")
		return false
	}
	driver := managerGatewayRebindStageConfigVolumeDriver{manager: fixture.ingress}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageConfigVolumeObservation(intent, observed, networkID, config) {
		t.Error("live config-volume cleanup cannot prove exact protected ownership; retaining resources")
		return false
	}
	result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
		45*time.Second, "volume", "rm", config.Name)
	clearLiveResult(&result)
	if removeErr != nil {
		t.Error("remove exact live successor config volume after stopped container")
		return false
	}
	residue, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageConfigVolumeObservation(intent, residue, networkID, nil) {
		t.Error("exact live config-volume cleanup left owned residue")
		return false
	}
	return true
}

func cleanupLiveGatewayRebindStoppedStageNetwork(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding, container *gatewayRebindStageContainerBinding,
) {
	t.Helper()
	if _, ok := liveGatewayRebindStoppedStageCleanupLineage(t, fixture, intent, networkID, config, data, container); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	containerDriver := managerGatewayRebindStageContainerDriver{manager: fixture.ingress}
	containerObservation, err := containerDriver.inspect(ctx, intent)
	if err != nil || containerObservation.StageContainerFound || len(containerObservation.OwnedContainers) != 0 ||
		containerObservation.ConfigVolumeFound || containerObservation.DataVolumeFound {
		t.Error("live network cleanup cannot prove successor container and volumes are absent; retaining network")
		return
	}
	driver := managerGatewayRebindStageNetworkDriver{manager: fixture.ingress}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageNetworkObservation(intent, observed, networkID) {
		t.Error("live network cleanup cannot prove exact protected ownership; retaining network")
		return
	}
	result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
		45*time.Second, "network", "rm", networkID)
	clearLiveResult(&result)
	if removeErr != nil {
		t.Error("remove exact live successor network after stopped container")
		return
	}
	residue, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageNetworkAbsentObservation(intent, residue) {
		t.Error("exact live stopped-container network cleanup left owned residue")
	}
}
