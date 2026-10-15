//go:build live_docker

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
)

// TestLiveControllerGatewayRebindCrashMatrix is the one required hosted-Docker
// entry point. Every boundary subtest creates its own real SQL/protected/Docker
// baseline so a failure cannot make a later case appear to pass. Cases are
// deliberately serial because they own host networking and the local daemon.
func TestLiveControllerGatewayRebindCrashMatrix(t *testing.T) {
	if os.Getenv("RIG_RUN_LIVE_GATEWAY_V2") != "1" ||
		os.Getenv("RIG_RUN_LIVE_GATEWAY_REBIND_CROSS_STORE") != "1" ||
		os.Getenv("RIG_RUN_LIVE_GATEWAY_REBIND_RUNTIME") != "1" ||
		os.Getenv(liveHostdGatewayRebindEnvironment) != "1" {
		t.Fatal("set all supported hostd rebind flags on a disposable Linux Docker host")
	}
	binaryDigest := liveHostdRebindExecutableDigest(t)
	for _, entry := range gatewayRebindLiveBoundaries {
		entry := entry
		t.Run(gatewayRebindLiveCaseName("boundary", entry.ID), func(t *testing.T) {
			liveHostdRebindRunCrashCase(t, entry.ID, "")
			if after := liveHostdRebindExecutableDigest(t); after != binaryDigest {
				t.Fatal("full hostd child binary identity changed during boundary case")
			}
		})
	}
	for _, entry := range gatewayRebindLiveScenarios {
		entry := entry
		t.Run(gatewayRebindLiveCaseName("scenario", entry.ID), func(t *testing.T) {
			liveHostdRebindRunScenario(t, entry.ID)
			if after := liveHostdRebindExecutableDigest(t); after != binaryDigest {
				t.Fatal("full hostd child binary identity changed during scenario case")
			}
		})
	}
}

func liveHostdRebindRunCrashCase(t *testing.T, boundaryID, scenarioID string) {
	t.Helper()
	expectedDisposition := liveHostdRebindExpectedDisposition(t, boundaryID, scenarioID)
	fixture := newLiveHostdGatewayRebindFixture(t)
	operationID := uuid.NewString()
	fixture.registerOperation(operationID)
	child := liveHostdRebindStartChild(t, fixture, operationID, boundaryID, scenarioID)
	ready := liveHostdRebindWaitReady(t, fixture, child)
	if ready.RecoveryOnly {
		t.Fatal("fresh fixture entered recovery before rebind admission")
	}
	target := apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	}
	profile := liveHostdRebindConfigureSuccessor(t, fixture, target)
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, profile)
	commitDone := make(chan error, 1)
	go func() {
		status, _, err := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodPost,
			"/api/v1/system/lan-gateway-rebind", commit)
		if err == nil {
			err = errors.New("commit returned before the requested crash boundary: status=" + http.StatusText(status))
		}
		commitDone <- err
	}()
	marker := liveHostdRebindWaitBoundary(t, fixture, child)
	if marker.OperationID != operationID {
		t.Fatal("child did not prove the requested operation boundary")
	}
	if liveHostdRebindLostEffectScenario(scenarioID) {
		if marker.Boundary != "completed_effect_ack_lost:"+scenarioID || boundaryID != "" {
			t.Fatalf("lost acknowledgement proof does not name its completed effect: %#v", marker)
		}
	} else if !liveHostdRebindKnownBoundary(boundaryID) {
		t.Fatal("child did not prove a closed durable boundary")
	}
	select {
	case err := <-commitDone:
		t.Fatalf("commit completed before SIGKILL: %v", err)
	default:
	}
	liveHostdRebindKillAndReap(t, child)
	select {
	case <-commitDone:
	case <-time.After(20 * time.Second):
		t.Fatal("killed commit request did not return")
	}

	recovery := liveHostdRebindStartChild(t, fixture, "", "", "")
	recoveryReady := liveHostdRebindWaitReady(t, fixture, recovery)
	status := liveHostdRebindRecover(t, fixture, operationID, recoveryReady)
	liveHostdRebindAssertTerminal(t, status, operationID)
	liveHostdRebindAssertDisposition(t, status, expectedDisposition)
	liveHostdRebindKillAndReap(t, recovery)

	// A terminal result in a recovery-only process never relaxes that process.
	// This third exact-binary process must independently return to normal mode.
	normal := liveHostdRebindStartChild(t, fixture, "", "", "")
	normalReady := liveHostdRebindWaitReady(t, fixture, normal)
	if normalReady.RecoveryOnly || normalReady.RecoveryKind != "" || normalReady.OperationID != "" {
		t.Fatal("fresh post-terminal hostd did not return to normal startup")
	}
	status = liveHostdRebindRecover(t, fixture, operationID, normalReady)
	liveHostdRebindAssertTerminal(t, status, operationID)
	liveHostdRebindAssertDisposition(t, status, expectedDisposition)
	liveHostdRebindAssertServing(t, fixture, status)
	liveHostdRebindKillAndReap(t, normal)
}

func liveHostdRebindExpectedDisposition(t *testing.T, boundaryID, scenarioID string) string {
	t.Helper()
	if liveHostdRebindLostEffectScenario(scenarioID) {
		return "abort"
	}
	for _, entry := range gatewayRebindLiveBoundaries {
		if entry.ID != boundaryID {
			continue
		}
		switch entry.Direction {
		case "rollback":
			return "abort"
		case "forward":
			return "commit"
		default:
			t.Fatalf("unknown recovery direction %q for %s", entry.Direction, boundaryID)
		}
	}
	t.Fatalf("no expected recovery direction for boundary=%q scenario=%q", boundaryID, scenarioID)
	return ""
}

func liveHostdRebindAssertDisposition(t *testing.T, status liveHostdRebindStatus, want string) {
	t.Helper()
	if status.Operation == nil || status.Operation.TerminalDisposition != want {
		t.Fatalf("terminal disposition=%v want=%q", status.Operation, want)
	}
}

func liveHostdRebindAssertServing(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	status liveHostdRebindStatus,
) {
	t.Helper()
	address := fixture.predecessor.IPv4
	if status.Operation != nil && status.Operation.TerminalDisposition == "commit" {
		address = fixture.successor.IPv4
	}
	lanTwoAppProbe(t, fixture.ctx, address, fixture.ports[0], address,
		"/api/version", http.StatusOK, "supported-rebind-a-v1")
	lanTwoAppProbe(t, fixture.ctx, address, fixture.ports[1], address,
		"/api/version", http.StatusOK, "supported-rebind-b-v1")
}

func liveHostdRebindExecutableDigest(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func liveHostdRebindRunScenario(t *testing.T, scenarioID string) {
	t.Helper()
	switch scenarioID {
	case "lost-network-create-ack", "lost-config-volume-create-ack", "lost-data-volume-create-ack",
		"lost-stage-container-create-ack", "lost-stage-config-copy-ack", "lost-stage-start-ack",
		"lost-final-config-copy-ack", "lost-final-container-create-ack", "lost-cutover-ack",
		"lost-successor-serving-ack":
		liveHostdRebindRunCrashCase(t, "", scenarioID)
	case "terminal-replay":
		liveHostdRebindRunTerminalReplay(t)
	case "selected-current-restoration":
		liveHostdRebindRunSelectedCurrentRestoration(t)
	case "cancellation-after-write":
		liveHostdRebindRunCancellationAfterWrite(t)
	case "effects-release-fault", "gateway-release-fault":
		liveHostdRebindRunReleaseFault(t, scenarioID)
	case "competing-controller":
		liveHostdRebindRunCompetingController(t)
	case "cancellation-before-write":
		liveHostdRebindRunCancellationBeforeWrite(t)
	case "literal-unresolved-precommit", "literal-unresolved-database-committed",
		"pending-route-restart", "pending-lan-restart", "pending-lan-batch-restart",
		"original-role-revocation", "current-role-revocation", "ownership-drift",
		"configuration-drift", "census-drift", "emergency-withdrawal-failure",
		"recovery-worker-isolation":
		liveHostdRebindRunAdversarialScenario(t, scenarioID)
	default:
		t.Fatalf("unknown required rebind scenario %q", scenarioID)
	}
}

// Release-fault cases run the true release once and inject only the additive
// post-release error. The initial HTTP result must be non-success, and a fresh
// process must replay the retained terminal without executing the release hook.
func liveHostdRebindRunReleaseFault(t *testing.T, scenarioID string) {
	t.Helper()
	fixture := newLiveHostdGatewayRebindFixture(t)
	operationID := uuid.NewString()
	fixture.registerOperation(operationID)
	child := liveHostdRebindStartChild(t, fixture, operationID, "", scenarioID)
	if liveHostdRebindWaitReady(t, fixture, child).RecoveryOnly {
		t.Fatal("fresh fixture entered recovery before release-fault admission")
	}
	profile := liveHostdRebindConfigureSuccessor(t, fixture, apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	})
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, profile)
	statusCode, _, err := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodPost,
		"/api/v1/system/lan-gateway-rebind", commit)
	if err != nil || statusCode < 500 {
		t.Fatalf("post-release ambiguity returned success: status=%d error=%v", statusCode, err)
	}
	liveHostdRebindKillAndReap(t, child)
	fresh := liveHostdRebindStartChild(t, fixture, "", "", "")
	ready := liveHostdRebindWaitReady(t, fixture, fresh)
	status := liveHostdRebindRecover(t, fixture, operationID, ready)
	liveHostdRebindAssertTerminal(t, status, operationID)
	liveHostdRebindKillAndReap(t, fresh)
}

func liveHostdRebindRunCompetingController(t *testing.T) {
	t.Helper()
	fixture := newLiveHostdGatewayRebindFixture(t)
	operationID := uuid.NewString()
	fixture.registerOperation(operationID)
	owner := liveHostdRebindStartChild(t, fixture, operationID, "p10-stage-serving", "")
	if liveHostdRebindWaitReady(t, fixture, owner).RecoveryOnly {
		t.Fatal("fresh fixture entered recovery before contender case")
	}
	profile := liveHostdRebindConfigureSuccessor(t, fixture, apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	})
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, profile)
	requestContext, cancel := context.WithCancel(fixture.ctx)
	defer cancel()
	commitDone := make(chan struct{})
	go func() {
		defer close(commitDone)
		_, _, _ = liveHostdRebindHTTP(requestContext, fixture, http.MethodPost,
			"/api/v1/system/lan-gateway-rebind", commit)
	}()
	liveHostdRebindWaitBoundary(t, fixture, owner)

	// Same data root and binary with a distinct listener: it must fail on the
	// real controller owner lease before assembling another HTTP handler.
	contenderListen := fixture.listen
	fixture.listen = liveHostdGatewayRebindListenAddress(t)
	contender := liveHostdRebindStartChild(t, fixture, "", "", "")
	fixture.listen = contenderListen
	contender.startWait()
	select {
	case <-contender.waitDone:
		if contender.waitErr == nil {
			t.Fatal("competing full hostd unexpectedly acquired the owner lease")
		}
	case <-time.After(20 * time.Second):
		liveHostdRebindKillAndReap(t, contender)
		t.Fatal("competing full hostd did not fail closed")
	}
	if _, err := os.Stat(contender.manifest.ReadyMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("competing controller assembled an API handler")
	}
	liveHostdRebindKillAndReap(t, owner)
	cancel()
	select {
	case <-commitDone:
	case <-time.After(20 * time.Second):
		t.Fatal("competing-controller commit request did not stop")
	}
	recovery := liveHostdRebindStartChild(t, fixture, "", "", "")
	ready := liveHostdRebindWaitReady(t, fixture, recovery)
	status := liveHostdRebindRecover(t, fixture, operationID, ready)
	liveHostdRebindAssertTerminal(t, status, operationID)
	liveHostdRebindKillAndReap(t, recovery)
}

func liveHostdRebindRunCancellationBeforeWrite(t *testing.T) {
	t.Helper()
	fixture := newLiveHostdGatewayRebindFixture(t)
	operationID := uuid.NewString()
	fixture.registerOperation(operationID)
	child := liveHostdRebindStartChild(t, fixture, operationID, "", "cancellation-before-write")
	if liveHostdRebindWaitReady(t, fixture, child).RecoveryOnly {
		t.Fatal("fresh fixture entered recovery before cancellation case")
	}
	profile := liveHostdRebindConfigureSuccessor(t, fixture, apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	})
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, profile)
	if err := liveHostdRebindWriteExclusiveJSON(child.manifest.RequestArm, map[string]string{
		"operationId": operationID,
	}); err != nil {
		t.Fatal("arm preclaim cancellation barrier:", err)
	}
	requestContext, cancelRequest := context.WithCancel(fixture.ctx)
	requestDone := make(chan error, 1)
	go func() {
		_, _, err := liveHostdRebindHTTP(requestContext, fixture, http.MethodPost,
			"/api/v1/system/lan-gateway-rebind", commit)
		requestDone <- err
	}()
	marker := liveHostdRebindWaitBoundary(t, fixture, child)
	if marker.Boundary != "preclaim_read_blocked" {
		t.Fatalf("pre-write cancellation did not reach server admission: %#v", marker)
	}
	cancelRequest()
	select {
	case err := <-requestDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pre-write HTTP cancellation error=%v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("pre-write canceled request did not return")
	}
	child.startWait()
	select {
	case <-child.waitDone:
		t.Fatalf("pre-write request cancellation terminated hostd: %v", child.waitErr)
	default:
	}
	statusCode, _, err := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodGet,
		"/api/v1/system/lan-gateway-rebind/"+operationID, nil)
	if err != nil || statusCode != http.StatusNotFound {
		t.Fatalf("cancelled request wrote a claim: status=%d error=%v", statusCode, err)
	}
	liveHostdRebindKillAndReap(t, child)
}

func liveHostdRebindRunCancellationAfterWrite(t *testing.T) {
	t.Helper()
	fixture := newLiveHostdGatewayRebindFixture(t)
	operationID := uuid.NewString()
	fixture.registerOperation(operationID)
	child := liveHostdRebindStartChild(t, fixture, operationID, "a0-sql-claim", "cancellation-after-write")
	if liveHostdRebindWaitReady(t, fixture, child).RecoveryOnly {
		t.Fatal("fresh fixture entered recovery before post-write cancellation")
	}
	profile := liveHostdRebindConfigureSuccessor(t, fixture, apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	})
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, profile)
	requestContext, cancelRequest := context.WithCancel(fixture.ctx)
	requestDone := make(chan error, 1)
	go func() {
		_, _, err := liveHostdRebindHTTP(requestContext, fixture, http.MethodPost,
			"/api/v1/system/lan-gateway-rebind", commit)
		requestDone <- err
	}()
	marker := liveHostdRebindWaitBoundary(t, fixture, child)
	if marker.Boundary != "sql_claim_readback" || marker.SQLPhase != "prepared" {
		t.Fatalf("post-write cancellation did not retain A0: %#v", marker)
	}
	cancelRequest()
	select {
	case err := <-requestDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("post-write HTTP cancellation error=%v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("canceled post-write request did not return while hostd remained alive")
	}
	child.startWait()
	select {
	case <-child.waitDone:
		t.Fatalf("request cancellation terminated hostd: %v", child.waitErr)
	default:
	}
	statusCode, body, err := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodGet,
		"/api/v1/system/lan-gateway-rebind/"+operationID, nil)
	status, decodeErr := liveHostdRebindDecodeStatus(body)
	if err != nil || decodeErr != nil || statusCode != http.StatusOK || !status.Active || status.Historical ||
		status.Operation == nil || status.Operation.StoredState != "prepared" || status.OperationFence != "held" ||
		status.TerminalDigest != "" {
		t.Fatalf("canceled A0 was not retained safely: code=%d status=%#v request=%v decode=%v",
			statusCode, status, err, decodeErr)
	}
	liveHostdRebindKillAndReap(t, child)
	recovery := liveHostdRebindStartChild(t, fixture, "", "", "")
	ready := liveHostdRebindWaitReady(t, fixture, recovery)
	status = liveHostdRebindRecover(t, fixture, operationID, ready)
	liveHostdRebindAssertTerminal(t, status, operationID)
	liveHostdRebindAssertDisposition(t, status, "abort")
	liveHostdRebindAssertServing(t, fixture, status)
	liveHostdRebindKillAndReap(t, recovery)
}

// The remaining adversarial cases use scenario-specific durable mutations in
// gateway_rebind_scenarios_live_test.go. Keeping this dispatch separate makes
// the independent required-ID manifest fail compilation if an implementation
// is removed or renamed.
func liveHostdRebindRunAdversarialScenario(t *testing.T, scenarioID string) {
	t.Helper()
	run, ok := liveHostdRebindAdversarialScenarios[scenarioID]
	if !ok || run == nil {
		t.Fatalf("required adversarial rebind scenario %q has no implementation", scenarioID)
	}
	run(t)
}

func liveHostdRebindDecodeStatus(body []byte) (liveHostdRebindStatus, error) {
	var status liveHostdRebindStatus
	if len(body) == 0 || len(body) > 512<<10 || json.Unmarshal(body, &status) != nil {
		return liveHostdRebindStatus{}, errors.New("invalid rebind status")
	}
	return status, nil
}
