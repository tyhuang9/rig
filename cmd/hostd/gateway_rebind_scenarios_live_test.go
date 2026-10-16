//go:build live_docker

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/secretfile"
)

var liveHostdRebindAdversarialScenarios = map[string]func(*testing.T){
	"literal-unresolved-precommit":          liveHostdRebindRunUnresolvedPrecommit,
	"literal-unresolved-database-committed": liveHostdRebindRunUnresolvedDatabaseCommitted,
	"pending-route-restart":                 liveHostdRebindRunPendingRouteRestart,
	"pending-lan-restart":                   liveHostdRebindRunPendingLANRestart,
	"pending-lan-batch-restart":             liveHostdRebindRunPendingLANBatchRestart,
	"original-role-revocation":              liveHostdRebindRunOriginalRoleRevocation,
	"current-role-revocation":               liveHostdRebindRunCurrentRoleRevocation,
	"ownership-drift":                       liveHostdRebindRunOwnershipDrift,
	"configuration-drift":                   liveHostdRebindRunConfigurationDrift,
	"census-drift":                          liveHostdRebindRunCensusDrift,
	"emergency-withdrawal-failure":          liveHostdRebindRunEmergencyWithdrawalFailure,
	"recovery-worker-isolation":             liveHostdRebindRunRecoveryWorkerIsolation,
	"recovery-background-isolation":         liveHostdRebindRunRecoveryBackgroundIsolation,
}

type liveHostdRebindTerminalReceipt struct {
	Generation              uint64                                     `json:"generation"`
	OperationID             string                                     `json:"operationId"`
	Disposition             appaccess.GatewayRebindTerminalDisposition `json:"disposition"`
	ClaimRequestDigest      string                                     `json:"claimRequestDigest"`
	ClaimSpecDigest         string                                     `json:"claimSpecDigest"`
	Predecessor             appaccess.GatewayRebindSourceRef           `json:"predecessor"`
	ProtectedPhase          string                                     `json:"protectedPhase"`
	ProtectedRecordSequence uint64                                     `json:"protectedRecordSequence"`
	ProtectedRecordDigest   string                                     `json:"protectedRecordDigest"`
	Digest                  string                                     `json:"digest"`
}

func liveHostdRebindReadTerminal(t *testing.T, dataRoot, operationID string) liveHostdRebindTerminalReceipt {
	t.Helper()
	var found *liveHostdRebindTerminalReceipt
	err := filepath.WalkDir(dataRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "gateway-rebind-terminal.v2.g") {
			return nil
		}
		prefix := "gateway-rebind-terminal.v2.g"
		suffix := "." + operationID + ".bundle"
		if !strings.HasSuffix(entry.Name(), suffix) {
			return nil
		}
		token := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), prefix), suffix)
		generation, parseErr := strconv.ParseUint(token, 10, 64)
		purpose := fmt.Sprintf("hostd/generated-ingress/rebind/terminal/v2/generation/%020d/%s",
			generation, operationID)
		body, err := secretfile.Read(path, purpose)
		var candidate liveHostdRebindTerminalReceipt
		if parseErr != nil || err != nil || len(body) == 0 || len(body) > 512<<10 ||
			json.Unmarshal(body, &candidate) != nil {
			return errors.New("invalid protected terminal file")
		}
		if candidate.OperationID != operationID {
			return nil
		}
		if found != nil {
			return errors.New("duplicate protected terminal operation")
		}
		copy := candidate
		found = &copy
		return nil
	})
	if err != nil || found == nil || found.Digest == "" || found.ProtectedRecordDigest == "" {
		t.Fatalf("read exact protected terminal: %v", err)
	}
	return *found
}

func liveHostdRebindApplyUnresolved(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	operationID string, fromDatabaseCommit bool,
) {
	t.Helper()
	db, err := database.Open(fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository := appaccess.New(db)
	snapshot, err := repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil || snapshot.Active.Claim.V2 == nil ||
		snapshot.Active.Claim.V2.Spec.OperationID != operationID || snapshot.CurrentProfile == nil {
		t.Fatalf("read active operation for literal unresolved: %v", err)
	}
	claim := *snapshot.Active.Claim.V2
	receipt := liveHostdRebindReadTerminal(t, fixture.dataRoot, operationID)
	proof := appaccess.GatewayRebindTransitionProof{
		Version: appaccess.GatewayRebindTransitionVersionV1, Purpose: appaccess.GatewayRebindTransitionPurpose,
		OperationID: operationID, ClaimRequestDigest: claim.RequestDigest,
		ClaimSpecDigest: claim.RebindApproval.SpecDigest,
		ExpectedState:   snapshot.Phase, ExpectedSequence: claim.StateSequence,
		NextState:                  appaccess.GatewayRebindUnresolved,
		ExpectedHeadRevisionID:     snapshot.CurrentProfile.ID,
		ExpectedHeadRevisionNumber: snapshot.CurrentProfile.RevisionNumber,
		ExpectedHeadSpecDigest:     snapshot.CurrentProfile.SpecDigest,
		ProtectedGeneration:        receipt.Generation, ProtectedPhase: receipt.ProtectedPhase,
		ProtectedRecordSequence: receipt.ProtectedRecordSequence,
		ProtectedRecordDigest:   receipt.ProtectedRecordDigest,
		TerminalReceiptDigest:   receipt.Digest, TerminalDisposition: receipt.Disposition,
		PredecessorCheckpointDigest: receipt.Predecessor.PredecessorCheckpointDigest,
		SourceStateVersion:          receipt.Predecessor.SourceStateVersion,
		SourceStateRevision:         receipt.Predecessor.SourceStateRevision,
		SourceStateDigest:           receipt.Predecessor.SourceStateDigest,
	}
	if fromDatabaseCommit {
		if snapshot.Phase != appaccess.GatewayRebindDatabaseCommitted || !snapshot.DatabaseCommitObserved ||
			snapshot.DatabaseCommittedEvent == nil || receipt.Disposition != appaccess.GatewayRebindDispositionCommit {
			t.Fatal("database-committed unresolved fixture lacks immutable commit evidence")
		}
	} else if snapshot.Phase != appaccess.GatewayRebindPrepared || snapshot.DatabaseCommitObserved ||
		receipt.Disposition != appaccess.GatewayRebindDispositionAbort {
		t.Fatal("precommit unresolved fixture lacks exact rollback evidence")
	}
	if _, err := repository.ApplyGatewayRebindTransition(context.Background(), proof); err != nil {
		t.Fatal("apply guarded literal unresolved transition:", err)
	}
	confirmed, err := repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || confirmed.Phase != appaccess.GatewayRebindUnresolved ||
		confirmed.DatabaseCommitObserved != fromDatabaseCommit {
		t.Fatalf("literal unresolved did not retain direction evidence: %#v error=%v", confirmed, err)
	}
}

func liveHostdRebindRunUnresolvedPrecommit(t *testing.T) {
	liveHostdRebindRunLiteralUnresolved(t, false)
}

func liveHostdRebindRunUnresolvedDatabaseCommitted(t *testing.T) {
	liveHostdRebindRunLiteralUnresolved(t, true)
}

func liveHostdRebindRunLiteralUnresolved(t *testing.T, databaseCommitted bool) {
	t.Helper()
	fixture, operationID := liveHostdRebindPrepareLiteralUnresolved(t, databaseCommitted)
	recovery := liveHostdRebindStartChild(t, fixture, "", "", "")
	ready := liveHostdRebindWaitReady(t, fixture, recovery)
	if !ready.RecoveryOnly || ready.OperationID != operationID {
		t.Fatal("literal unresolved did not select exact recovery-only startup")
	}
	status := liveHostdRebindRecover(t, fixture, operationID, ready)
	liveHostdRebindAssertTerminal(t, status, operationID)
	if databaseCommitted && (status.Operation == nil || status.Operation.TerminalDisposition != "commit") {
		t.Fatal("immutable database commit did not force forward completion")
	}
	if !databaseCommitted && (status.Operation == nil || status.Operation.TerminalDisposition != "abort") {
		t.Fatal("precommit abort evidence did not retain rollback direction")
	}
	liveHostdRebindKillAndReap(t, recovery)
}

func liveHostdRebindPrepareLiteralUnresolved(t *testing.T, databaseCommitted bool) (
	*liveHostdGatewayRebindFixture, string,
) {
	return liveHostdRebindPrepareLiteralUnresolvedWithSetup(t, databaseCommitted, nil)
}

func liveHostdRebindPrepareLiteralUnresolvedWithSetup(t *testing.T, databaseCommitted bool,
	setup func(*liveHostdGatewayRebindFixture),
) (*liveHostdGatewayRebindFixture, string) {
	t.Helper()
	fixture := newLiveHostdGatewayRebindFixture(t)
	if setup != nil {
		setup(fixture)
	}
	operationID := uuid.NewString()
	fixture.registerOperation(operationID)
	boundary, scenario := "s02-protected-terminal-readback", "literal-unresolved-precommit"
	if databaseCommitted {
		boundary, scenario = "s04-database-committed", "literal-unresolved-database-committed"
	}
	child := liveHostdRebindStartChild(t, fixture, operationID, boundary, scenario)
	if liveHostdRebindWaitReady(t, fixture, child).RecoveryOnly {
		t.Fatal("fresh unresolved fixture entered recovery")
	}
	successor := liveHostdRebindPlanSuccessor(t, fixture, apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	})
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, successor)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		_, _, _ = liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodPost,
			"/api/v1/system/lan-gateway-rebind", commit)
	}()
	liveHostdRebindWaitBoundary(t, fixture, child)
	liveHostdRebindKillAndReap(t, child)
	select {
	case <-requestDone:
	case <-time.After(20 * time.Second):
		t.Fatal("literal unresolved commit request was not released")
	}
	liveHostdRebindApplyUnresolved(t, fixture, operationID, databaseCommitted)
	return fixture, operationID
}

func liveHostdRebindRunOriginalRoleRevocation(t *testing.T) {
	t.Helper()
	fixture := newLiveHostdGatewayRebindFixture(t)
	child := liveHostdRebindStartChild(t, fixture, "", "", "")
	if liveHostdRebindWaitReady(t, fixture, child).RecoveryOnly {
		t.Fatal("fresh authority fixture entered recovery")
	}
	operationID := uuid.NewString()
	fixture.registerOperation(operationID)
	successor := liveHostdRebindPlanSuccessor(t, fixture, apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	})
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, successor)
	db, err := database.Open(fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, fixture.user.ID); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	status, _, requestErr := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodPost,
		"/api/v1/system/lan-gateway-rebind", commit)
	if _, err := db.Exec(`UPDATE users SET role='administrator' WHERE id=?`, fixture.user.ID); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if requestErr != nil || status != http.StatusForbidden {
		t.Fatalf("revoked original administrator commit: status=%d error=%v", status, requestErr)
	}
	status, _, requestErr = liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodGet,
		"/api/v1/system/lan-gateway-rebind/"+operationID, nil)
	if requestErr != nil || status != http.StatusNotFound {
		t.Fatalf("revoked request crossed A0: status=%d error=%v", status, requestErr)
	}
	liveHostdRebindKillAndReap(t, child)
}

func liveHostdRebindRunCurrentRoleRevocation(t *testing.T) {
	// The literal unresolved helper reaches a pinned recovery process through
	// actual startup. The request actor is revoked immediately before the
	// request-scoped review so no reconcile capability can be issued.
	liveHostdRebindRunRecoveryAuthorityRevocation(t)
}

func liveHostdRebindRunRecoveryAuthorityRevocation(t *testing.T) {
	t.Helper()
	// Implemented as an isolated precommit unresolved fixture so rollback
	// remains the only durable direction even after the interactive actor is
	// revoked. This helper owns its complete setup instead of calling the
	// terminal-producing unresolved test.
	liveHostdRebindRunUnresolvedWithRecoveryCheck(t, true)
}

func liveHostdRebindRunRecoveryWorkerIsolation(t *testing.T) {
	liveHostdRebindRunUnresolvedWithRecoveryCheck(t, false)
}

func liveHostdRebindRunUnresolvedWithRecoveryCheck(t *testing.T, revoke bool) {
	t.Helper()
	fixture, operationID := liveHostdRebindPrepareLiteralUnresolved(t, false)
	var queued jobs.Job
	if !revoke {
		db, err := database.Open(fixture.dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		queued, _, err = jobs.New(db).Create("restart", "application", fixture.app.id, uuid.NewString())
		if closeErr := db.Close(); err != nil || closeErr != nil {
			t.Fatalf("seed recovery-only worker sentinel: create=%v close=%v", err, closeErr)
		}
	}
	recovery := liveHostdRebindStartChild(t, fixture, "", "", "")
	ready := liveHostdRebindWaitReady(t, fixture, recovery)
	if !ready.RecoveryOnly || ready.RecoveryKind != "gateway_rebind" || ready.OperationID != operationID {
		t.Fatal("literal unresolved did not enter exact rebind recovery-only assembly")
	}
	if revoke {
		path := "/api/v1/system/lan-gateway-rebind/" + operationID
		reviewCode, reviewBody, reviewErr := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodPost,
			path+"/recovery-proposal", map[string]any{})
		var review liveHostdRebindRecoveryReview
		if reviewErr != nil || reviewCode != http.StatusOK || json.Unmarshal(reviewBody, &review) != nil ||
			review.OperationID != operationID || review.RecoveryDigest == "" || review.RecoveryApprovalDigest == "" {
			t.Fatalf("obtain recovery approval before role revocation: status=%d error=%v", reviewCode, reviewErr)
		}
		db, err := database.Open(fixture.dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, fixture.user.ID); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		status, _, requestErr := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodPost,
			path+"/reconcile", map[string]any{
				"version": 1, "action": "reconcile", "recoveryDigest": review.RecoveryDigest,
				"recoveryApprovalDigest": review.RecoveryApprovalDigest,
			})
		if _, err = db.Exec(`UPDATE users SET role='administrator' WHERE id=?`, fixture.user.ID); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if closeErr := db.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if requestErr != nil || status != http.StatusForbidden {
			t.Fatalf("revoked current administrator used a previously issued recovery approval: status=%d error=%v",
				status, requestErr)
		}
		retainedCode, retainedBody, retainedErr := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodGet,
			"/api/v1/system/lan-gateway-rebind/"+operationID, nil)
		retained, decodeErr := liveHostdRebindDecodeStatus(retainedBody)
		if retainedErr != nil || decodeErr != nil || retainedCode != http.StatusOK || !retained.Active || retained.Historical {
			t.Fatalf("revoked recovery changed durable operation: status=%d request=%v decode=%v", retainedCode, retainedErr, decodeErr)
		}
	} else {
		liveHostdRebindAssertQueuedWorkerSentinel(t, fixture, queued)
		for _, path := range []string{
			"/api/v1/system/lan-gateway-rebind/" + operationID,
			"/api/v1/system/status",
			"/api/v1/jobs/" + queued.ID,
		} {
			status, _, err := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodGet, path, nil)
			if err != nil || (status != http.StatusOK && status != http.StatusServiceUnavailable) {
				t.Fatalf("recovery-only worker isolation probe %s: status=%d error=%v", path, status, err)
			}
		}
		liveHostdRebindAssertQueuedWorkerSentinel(t, fixture, queued)
	}
	status := liveHostdRebindRecover(t, fixture, operationID, ready)
	liveHostdRebindAssertTerminal(t, status, operationID)
	if !revoke {
		liveHostdRebindAssertQueuedWorkerSentinel(t, fixture, queued)
	}
	liveHostdRebindKillAndReap(t, recovery)
}

func liveHostdRebindAssertQueuedWorkerSentinel(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	want jobs.Job,
) {
	t.Helper()
	db, err := database.Open(fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	store := jobs.New(db)
	got, getErr := store.Get(want.ID)
	events, eventsErr := store.Events(want.ID, 0)
	closeErr := db.Close()
	if getErr != nil || eventsErr != nil || closeErr != nil || got.Status != string(jobs.Queued) ||
		got.Phase != want.Phase || len(events) != 1 || events[0].Code != "job_queued" {
		t.Fatalf("recovery-only process touched ordinary worker sentinel: job=%#v events=%#v errors=(%v,%v,%v)",
			got, events, getErr, eventsErr, closeErr)
	}
}

func liveHostdRebindCompleteHistorical(t *testing.T, fixture *liveHostdGatewayRebindFixture) string {
	t.Helper()
	operationID := uuid.NewString()
	fixture.registerOperation(operationID)
	child := liveHostdRebindStartChild(t, fixture, operationID, "", "")
	ready := liveHostdRebindWaitReady(t, fixture, child)
	if ready.RecoveryOnly {
		t.Fatal("fresh historical fixture entered recovery")
	}
	successor := liveHostdRebindPlanSuccessor(t, fixture, apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	})
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, successor)
	statusCode, _, err := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodPost,
		"/api/v1/system/lan-gateway-rebind", commit)
	if err != nil || (statusCode != http.StatusCreated && statusCode != http.StatusOK) {
		t.Fatalf("complete historical rebind: status=%d error=%v", statusCode, err)
	}
	status := liveHostdRebindRecover(t, fixture, operationID, ready)
	liveHostdRebindAssertTerminal(t, status, operationID)
	liveHostdRebindAssertServing(t, fixture, status)
	liveHostdRebindKillAndReap(t, child)
	return operationID
}

func liveHostdRebindRunTerminalReplay(t *testing.T) {
	t.Helper()
	fixture := newLiveHostdGatewayRebindFixture(t)
	operationID := uuid.NewString()
	fixture.registerOperation(operationID)
	child := liveHostdRebindStartChild(t, fixture, operationID, "s11-response-loss", "terminal-replay")
	if liveHostdRebindWaitReady(t, fixture, child).RecoveryOnly {
		t.Fatal("fresh replay fixture entered recovery")
	}
	successor := liveHostdRebindPlanSuccessor(t, fixture, apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	})
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, successor)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		_, _, _ = liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodPost,
			"/api/v1/system/lan-gateway-rebind", commit)
	}()
	marker := liveHostdRebindWaitBoundary(t, fixture, child)
	if marker.Boundary != "gateway_release" || marker.OperationID != operationID {
		t.Fatalf("terminal replay did not reach post-release response loss: %#v", marker)
	}
	before := liveHostdRebindOperationResourceCensus(t, fixture, operationID)
	initialEffects := liveHostdRebindReadEffectLedger(t, child)
	if len(initialEffects) == 0 {
		t.Fatal("initial rebind recorded no real Docker mutations")
	}
	liveHostdRebindKillAndReap(t, child)
	select {
	case <-requestDone:
	case <-time.After(20 * time.Second):
		t.Fatal("response-loss request did not stop with its hostd")
	}
	fresh := liveHostdRebindStartChild(t, fixture, operationID, "", "terminal-replay")
	ready := liveHostdRebindWaitReady(t, fixture, fresh)
	if ready.RecoveryOnly {
		t.Fatal("retained terminal replay reopened recovery")
	}
	statusCode, body, requestErr := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodPost,
		"/api/v1/system/lan-gateway-rebind", commit)
	var replay apicontract.LANGatewayRebindMutation
	if requestErr != nil || statusCode != http.StatusOK || json.Unmarshal(body, &replay) != nil || replay.Created ||
		replay.Operation.OperationID != operationID || replay.Operation.TerminalDisposition != "commit" ||
		!replay.FenceReleased {
		t.Fatalf("identical terminal commit did not replay retained result: status=%d replay=%#v error=%v",
			statusCode, replay, requestErr)
	}
	status := liveHostdRebindRecover(t, fixture, operationID, ready)
	liveHostdRebindAssertTerminal(t, status, operationID)
	liveHostdRebindAssertServing(t, fixture, status)
	after := liveHostdRebindOperationResourceCensus(t, fixture, operationID)
	for kind, want := range before {
		if after[kind] != want {
			t.Fatalf("terminal replay repeated or replaced a %s effect: before=%q after=%q", kind, want, after[kind])
		}
	}
	if replayEffects := liveHostdRebindReadEffectLedger(t, fresh); len(replayEffects) != 0 {
		t.Fatalf("terminal replay repeated Docker mutations: %v", replayEffects)
	}
	liveHostdRebindKillAndReap(t, fresh)
}

func liveHostdRebindReadEffectLedger(t *testing.T, child *liveHostdRebindChild) []string {
	t.Helper()
	body, err := os.ReadFile(child.manifest.EffectLedger)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || len(body) > 64<<10 {
		t.Fatalf("read bounded effect ledger: %v", err)
	}
	return strings.Fields(strings.TrimSpace(string(body)))
}

func liveHostdRebindRunSelectedCurrentRestoration(t *testing.T) {
	t.Helper()
	fixture := newLiveHostdGatewayRebindFixture(t)
	operationID := liveHostdRebindCompleteHistorical(t, fixture)
	containerID, _ := liveHostdRebindOperationContainer(t, fixture, operationID, "final")
	if _, err := controllerJourneyDocker(fixture.ctx, fixture.docker, nil,
		"container", "stop", "--time", "10", containerID); err != nil {
		t.Fatal("stop exact selected-current container:", err)
	}
	fresh := liveHostdRebindStartChild(t, fixture, "", "", "")
	ready := liveHostdRebindWaitReady(t, fixture, fresh)
	if ready.RecoveryOnly {
		t.Fatal("selected-current restoration did not finish before normal admission")
	}
	status := liveHostdRebindRecover(t, fixture, operationID, ready)
	liveHostdRebindAssertTerminal(t, status, operationID)
	liveHostdRebindAssertServing(t, fixture, status)
	restoredID, _ := liveHostdRebindOperationContainer(t, fixture, operationID, "final")
	body, err := controllerJourneyDocker(fixture.ctx, fixture.docker, nil,
		"container", "inspect", "--format", "{{.State.Running}}", restoredID)
	if err != nil || restoredID != containerID || strings.TrimSpace(string(body)) != "true" {
		t.Fatalf("selected-current restoration replaced or failed to start the exact container: before=%q after=%q running=%q error=%v",
			containerID, restoredID, strings.TrimSpace(string(body)), err)
	}
	liveHostdRebindKillAndReap(t, fresh)
}

func liveHostdRebindOperationResourceCensus(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	operationID string,
) map[string]string {
	t.Helper()
	result := make(map[string]string, 3)
	for _, kind := range []string{"container", "network", "volume"} {
		args := []string{kind, "ls", "-q", "--filter", "label=io.rig.operation-id=" + operationID}
		if kind == "container" {
			args = []string{"ps", "-aq", "--no-trunc", "--filter", "label=io.rig.operation-id=" + operationID}
		}
		body, err := controllerJourneyDocker(fixture.ctx, fixture.docker, nil, args...)
		ids := strings.Fields(string(body))
		sort.Strings(ids)
		if err != nil || len(ids) == 0 {
			t.Fatalf("read exact terminal %s census: ids=%v error=%v", kind, ids, err)
		}
		result[kind] = strings.Join(ids, "\n")
	}
	return result
}

func liveHostdRebindPendingCurrentScenario(t *testing.T, kind string) {
	t.Helper()
	fixture := newLiveHostdGatewayRebindFixture(t)
	historicalID := liveHostdRebindCompleteHistorical(t, fixture)
	markerID := uuid.NewString()
	child := liveHostdRebindStartChild(t, fixture, markerID, "", "pending-"+kind+"-restart")
	if liveHostdRebindWaitReady(t, fixture, child).RecoveryOnly {
		t.Fatal("terminal rebind fixture entered recovery before a current operation")
	}
	requestContext, cancelRequests := context.WithCancel(fixture.ctx)
	requests := liveHostdRebindNewDisableRequests()
	activeChild := child
	scenarioComplete := false
	joiner := liveHostdRebindNewDisableRequestJoiner(requests, liveHostdRebindRequestJoinTimeout)
	cleanup := liveHostdRebindPendingCurrentCleanup{
		cancel: cancelRequests,
		reap:   func() error { return liveHostdRebindKillAndReapError(activeChild) },
		join:   joiner.join,
	}
	defer func() {
		if scenarioComplete {
			return
		}
		reapErr, joinErr := cleanup.run()
		if reapErr != nil || joinErr != nil {
			t.Errorf("pending current cleanup reaping=%s request_join=%s",
				liveHostdRebindFailureCategory(reapErr), liveHostdRebindFailureCategory(joinErr))
		}
	}()
	fail := func(cause error, census liveHostdRebindDisableClaimCensus) {
		reapErr, joinErr := cleanup.run()
		childFacts := "unavailable"
		if reapErr == nil {
			childFacts = liveHostdRebindClosedChildFacts(fixture, activeChild)
		}
		t.Fatalf("pending current %s failed: %v claims=%s requests=%s child=%s reaping=%s request_join=%s",
			kind, cause, census, requests.summary(), childFacts,
			liveHostdRebindFailureCategory(reapErr), liveHostdRebindFailureCategory(joinErr))
	}

	testDeadline, hasTestDeadline := t.Deadline()
	var disableSnapshot liveHostdRebindDisableSnapshot
	if kind == "lan" {
		if _, err := liveHostdRebindClaimObservationBudget(time.Now(), fixture.ctx, testDeadline, hasTestDeadline); err != nil {
			fail(fmt.Errorf("reserve pending LAN claim observation before requests: %w", err), liveHostdRebindDisableClaimCensus{})
			return
		}
		observerDB, err := database.Open(fixture.dataRoot)
		if err != nil {
			fail(errors.New("open pending LAN claim observer failed"), liveHostdRebindDisableClaimCensus{})
			return
		}
		observer := appaccess.New(observerDB)
		// The observer is opened once before either public mutation. Its snapshot
		// method uses a read-only transaction; close it only after every snapshot
		// call in this scenario has returned.
		defer func() {
			if err := observerDB.Close(); err != nil {
				t.Error("close pending LAN claim observer")
			}
		}()
		disableSnapshot = observer.AppAccessDisableStartupSnapshot
	}

	var recoveryOperations []string
	switch kind {
	case "route":
		var configuration apicontract.ApplicationConfiguration
		status, body, err := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodPut,
			"/api/v1/apps/"+fixture.app.id+"/scoped-configuration",
			apicontract.ReplaceScopedApplicationConfigurationRequest{
				ExpectedRevisionNumber: fixture.app.config.RevisionNumber,
				PlanRevisionID:         fixture.app.plan.RevisionID, PlanRevisionNumber: fixture.app.plan.RevisionNumber,
				PublicBuildDisclosureAcknowledged: true,
				Entries:                           lanTwoAppConfig("supported-rebind-a-v2"), Remove: []apicontract.ScopedConfigurationKey{},
			})
		if err != nil || status != http.StatusOK || json.Unmarshal(body, &configuration) != nil ||
			configuration.RevisionID == fixture.app.config.RevisionID {
			t.Fatalf("prepare pending current route: status=%d error=%v", status, err)
		}
		requestStatus, requestBody, requestErr := liveHostdRebindHTTP(requestContext, fixture, http.MethodPost,
			"/api/v1/apps/"+fixture.app.id+"/deployments", apicontract.DeployApplicationRequest{
				ExpectedPlanRevisionID:              fixture.app.plan.RevisionID,
				ExpectedPlanRevisionNumber:          fixture.app.plan.RevisionNumber,
				ExpectedConfigurationRevisionID:     configuration.RevisionID,
				ExpectedConfigurationRevisionNumber: configuration.RevisionNumber,
			})
		var mutation apicontract.JobMutationResponse
		if requestErr != nil || requestStatus != http.StatusAccepted ||
			json.Unmarshal(requestBody, &mutation) != nil || !mutation.Created || mutation.Job.ID == "" {
			t.Fatalf("enqueue local-source route replacement: status=%d error=%v body=%s",
				requestStatus, requestErr, string(requestBody))
		}
		// Acceptance precedes the barrier wait. The child must have admitted a
		// real durable deployment job that can progress without a GitHub provider.
		liveHostdRebindAssertDeploymentStarted(t, fixture, mutation.Job.ID)
	case "lan":
		firstID, firstRequest := liveHostdRebindDisableRequest(t, fixture, fixture.app.id)
		recoveryOperations = append(recoveryOperations, firstID)
		if err := requests.register(firstID, "first"); err != nil {
			fail(err, liveHostdRebindDisableClaimCensus{})
			return
		}
		go func() {
			status, _, err := liveHostdRebindHTTP(requestContext, fixture, http.MethodPost,
				"/api/v1/apps/"+fixture.app.id+"/lan-access/disables", firstRequest)
			requests.complete(liveHostdRebindDisableRequestOutcomeFor(firstID, "first", status, err))
		}()
	default:
		t.Fatalf("unknown pending current scenario %q", kind)
	}
	liveHostdRebindWaitBoundary(t, fixture, child)
	if err := liveHostdRebindKillAndReapError(child); err != nil {
		fail(errors.New("reap original child before pending request join"), liveHostdRebindDisableClaimCensus{})
		return
	}
	cancelRequests()
	if err := joiner.join(); err != nil {
		fail(errors.New("pending current requests did not stop with their killed hostd"), liveHostdRebindDisableClaimCensus{})
		return
	}
	fresh := liveHostdRebindStartChild(t, fixture, "", "", "")
	activeChild = fresh
	ready := liveHostdRebindWaitReady(t, fixture, fresh)
	if kind == "route" {
		if ready.RecoveryOnly {
			t.Fatal("ordinary pending route was not recovered before normal admission")
		}
		status := liveHostdRebindRecover(t, fixture, historicalID, ready)
		liveHostdRebindAssertTerminal(t, status, historicalID)
		liveHostdRebindAssertServing(t, fixture, status)
	} else {
		if !ready.RecoveryOnly || ready.RecoveryKind != "lan_disable" ||
			!liveHostdRebindContains(recoveryOperations, ready.OperationID) {
			t.Fatalf("pending LAN recovery lost dedicated precedence: %#v", ready)
		}
		fresh.startWait()
		census, err := liveHostdRebindWaitDisableClaimsResult(fixture.ctx, testDeadline, hasTestDeadline,
			fresh.waitDone, recoveryOperations, requests, disableSnapshot)
		if err != nil {
			fail(err, census)
			return
		}
	}
	if err := liveHostdRebindKillAndReapError(fresh); err != nil {
		fail(errors.New("reap fresh child after pending recovery"), liveHostdRebindDisableClaimCensus{})
		return
	}
	scenarioComplete = true
}

func liveHostdRebindMigratedPendingLANBatchScenario(t *testing.T) {
	t.Helper()
	fixture, batch := newLiveHostdGatewayRebindMigratedPendingLANBatchFixture(t)
	if len(batch.operationIDs) != 2 {
		t.Fatal("migrated pending LAN batch did not retain exactly two operations")
	}
	child := liveHostdRebindStartChild(t, fixture, batch.operationIDs[0], "", "pending-lan-batch-restart")
	activeChild := child
	complete := false
	defer func() {
		if !complete {
			liveHostdRebindCleanupChild(t, activeChild)
		}
	}()

	boundary := liveHostdRebindWaitBoundary(t, fixture, child)
	if boundary.Boundary != "current_effect_ack_lost" {
		t.Fatalf("migrated pending LAN batch stopped at wrong boundary %q", boundary.Boundary)
	}
	for _, operationID := range batch.operationIDs {
		access := batch.claims[operationID]
		lanTwoAppProbe(t, fixture.ctx, fixture.predecessor.IPv4, access.allocation.Port,
			fixture.predecessor.IPv4, "/api/version", http.StatusNotFound, "")
	}
	beforeCrash := liveHostdRebindReadNativeBatchIdentity(t, fixture, batch)
	if err := liveHostdRebindKillAndReapError(child); err != nil {
		t.Fatal("reap native batch child at physical quarantine boundary:", err)
	}
	liveHostdRebindAssertMigratedNativeBatchSQL(t, fixture, batch)

	fresh := liveHostdRebindStartChild(t, fixture, "", "", "")
	activeChild = fresh
	ready := liveHostdRebindWaitReady(t, fixture, fresh)
	if !ready.RecoveryOnly || ready.RecoveryKind != "lan_disable" ||
		ready.OperationID != beforeCrash.Items[beforeCrash.Head].OperationID {
		t.Fatalf("fresh process lost exact migrated native batch head: recovery=%t kind=%q expected_head=%t",
			ready.RecoveryOnly, ready.RecoveryKind, ready.OperationID == beforeCrash.Items[beforeCrash.Head].OperationID)
	}
	afterRestart := liveHostdRebindReadNativeBatchIdentity(t, fixture, batch)
	liveHostdRebindAssertSameNativeBatch(t, beforeCrash, afterRestart)
	liveHostdRebindAssertMigratedNativeBatchSQL(t, fixture, batch)
	if err := liveHostdRebindKillAndReapError(fresh); err != nil {
		t.Fatal("reap fresh native batch recovery child:", err)
	}
	complete = true
}

func liveHostdRebindAssertDeploymentStarted(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	jobID string,
) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(20 * time.Millisecond) {
		db, err := database.Open(fixture.dataRoot)
		if err == nil {
			job, getErr := jobs.New(db).Get(jobID)
			closeErr := db.Close()
			if getErr == nil && closeErr == nil {
				if job.Status == string(jobs.Failed) || job.Status == string(jobs.NeedsAttention) ||
					job.Status == string(jobs.WaitingUser) || job.Status == string(jobs.Succeeded) {
					t.Fatalf("route replacement reached terminal status before the publication barrier: %#v", job)
				}
				if job.Status != string(jobs.Queued) {
					return
				}
			}
		}
		if time.Now().After(deadline) || fixture.ctx.Err() != nil {
			t.Fatal("accepted route replacement did not start before barrier wait")
		}
	}
}

func liveHostdRebindDisableRequest(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	appID string,
) (string, apicontract.DisableLANAppAccessRequest) {
	t.Helper()
	status, body, err := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodGet,
		"/api/v1/apps/"+appID+"/lan-access", nil)
	var read apicontract.LANAppAccessRead
	if err != nil || status != http.StatusOK || json.Unmarshal(body, &read) != nil || read.DisableReview == nil {
		t.Fatalf("review LAN disable for pending restart: status=%d error=%v", status, err)
	}
	operationID := uuid.NewString()
	fixture.registerOperation(operationID)
	return operationID, apicontract.DisableLANAppAccessRequest{
		OperationID: operationID, AccessRevisionID: read.DisableReview.AccessRevisionID,
		AccessRevisionNumber: read.DisableReview.AccessRevisionNumber,
		AllocationID:         read.DisableReview.AllocationID, ApprovalDigest: read.DisableReview.ApprovalDigest,
	}
}

const liveHostdRebindRequestJoinTimeout = 20 * time.Second

// liveHostdRebindDisableSnapshot is the deliberately narrow polling seam. The
// live scenario binds it once to an observer repository opened before either
// public request; polling itself cannot reopen or migrate SQLite.
type liveHostdRebindDisableSnapshot func(context.Context) (appaccess.AppAccessDisableStartupSnapshot, error)

type liveHostdRebindDisableRequestOutcome struct {
	operationID string
	phase       string
	status      int
	response    string
	transport   string
}

func liveHostdRebindDisableRequestOutcomeFor(operationID, phase string, status int, err error) liveHostdRebindDisableRequestOutcome {
	outcome := liveHostdRebindDisableRequestOutcome{operationID: operationID, phase: phase, status: status}
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			outcome.transport = "canceled"
		case errors.Is(err, context.DeadlineExceeded):
			outcome.transport = "deadline_exceeded"
		default:
			outcome.transport = "transport_error"
		}
		return outcome
	}
	switch status {
	case http.StatusOK:
		outcome.response = "retained"
	case http.StatusCreated:
		outcome.response = "created"
	default:
		outcome.response = "unexpected_status"
	}
	return outcome
}

func (o liveHostdRebindDisableRequestOutcome) String() string {
	return fmt.Sprintf("phase=%s status=%d response=%s transport=%s", o.phase, o.status, o.response, o.transport)
}

// liveHostdRebindDisableRequests retains every expected request outcome. Its
// channel capacity covers both current LAN requests, so a request goroutine
// cannot become the stranded sender when the first observation fails.
type liveHostdRebindDisableRequests struct {
	done        chan liveHostdRebindDisableRequestOutcome
	expected    map[string]string
	outcomes    map[string]liveHostdRebindDisableRequestOutcome
	afterRetain func(liveHostdRebindDisableRequestOutcome)
}

// liveHostdRebindDisableRequestJoiner keeps the single request-join deadline
// shared by ordinary shutdown, failure reporting, and the deferred fallback.
// A failed join therefore cannot spend another full deadline while t.Fatalf
// unwinds the scenario defer.
type liveHostdRebindDisableRequestJoiner struct {
	requests *liveHostdRebindDisableRequests
	timeout  time.Duration

	attempted bool
	err       error
}

func liveHostdRebindNewDisableRequestJoiner(requests *liveHostdRebindDisableRequests,
	timeout time.Duration,
) *liveHostdRebindDisableRequestJoiner {
	return &liveHostdRebindDisableRequestJoiner{requests: requests, timeout: timeout}
}

func (j *liveHostdRebindDisableRequestJoiner) join() error {
	if j == nil || j.requests == nil || j.timeout <= 0 {
		return errors.New("invalid pending LAN request join")
	}
	if j.attempted {
		return j.err
	}
	j.attempted = true
	ctx, cancel := context.WithTimeout(context.Background(), j.timeout)
	defer cancel()
	j.err = j.requests.join(ctx)
	return j.err
}

// liveHostdRebindPendingCurrentCleanup preserves the shutdown order and caches
// both results. The failure path and its defer can call run independently
// without a second exact-child reap or a second request-join budget.
type liveHostdRebindPendingCurrentCleanup struct {
	cancel context.CancelFunc
	reap   func() error
	join   func() error

	attempted bool
	reapErr   error
	joinErr   error
}

func (c *liveHostdRebindPendingCurrentCleanup) run() (error, error) {
	if c == nil || c.cancel == nil || c.reap == nil || c.join == nil {
		return errors.New("invalid pending LAN cleanup"), errors.New("invalid pending LAN cleanup")
	}
	if c.attempted {
		return c.reapErr, c.joinErr
	}
	c.attempted = true
	c.cancel()
	c.reapErr = c.reap()
	c.joinErr = c.join()
	return c.reapErr, c.joinErr
}

func liveHostdRebindNewDisableRequests() *liveHostdRebindDisableRequests {
	return &liveHostdRebindDisableRequests{
		done:     make(chan liveHostdRebindDisableRequestOutcome, 2),
		expected: make(map[string]string), outcomes: make(map[string]liveHostdRebindDisableRequestOutcome),
	}
}

func (r *liveHostdRebindDisableRequests) register(operationID, phase string) error {
	if r == nil || operationID == "" || phase == "" || len(r.expected) >= cap(r.done) {
		return errors.New("invalid pending LAN request registration")
	}
	if _, exists := r.expected[operationID]; exists {
		return errors.New("duplicate pending LAN request registration")
	}
	r.expected[operationID] = phase
	return nil
}

func (r *liveHostdRebindDisableRequests) complete(outcome liveHostdRebindDisableRequestOutcome) {
	// Every scenario registers no more than the two known requests before its
	// goroutine starts. The fixed buffer therefore accepts both completions even
	// if cleanup has already moved on to reaping the child.
	r.done <- outcome
}

func (r *liveHostdRebindDisableRequests) retain(outcome liveHostdRebindDisableRequestOutcome) error {
	if r == nil || r.expected[outcome.operationID] != outcome.phase || outcome.phase == "" {
		return errors.New("unbound pending LAN request outcome")
	}
	if _, exists := r.outcomes[outcome.operationID]; exists {
		return errors.New("duplicate pending LAN request outcome")
	}
	r.outcomes[outcome.operationID] = outcome
	if r.afterRetain != nil {
		r.afterRetain(outcome)
	}
	return nil
}

func (r *liveHostdRebindDisableRequests) drain() error {
	for {
		select {
		case outcome := <-r.done:
			if err := r.retain(outcome); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func (r *liveHostdRebindDisableRequests) join(ctx context.Context) error {
	if r == nil {
		return errors.New("pending LAN request outcomes are missing")
	}
	for len(r.outcomes) < len(r.expected) {
		if err := r.drain(); err != nil {
			return err
		}
		if len(r.outcomes) == len(r.expected) {
			break
		}
		select {
		case outcome := <-r.done:
			if err := r.retain(outcome); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (r *liveHostdRebindDisableRequests) summary() string {
	if r == nil || len(r.expected) == 0 {
		return "none"
	}
	values := make([]string, 0, len(r.expected))
	for operationID, phase := range r.expected {
		outcome, ok := r.outcomes[operationID]
		if !ok {
			values = append(values, "phase="+phase+" outcome=pending")
			continue
		}
		values = append(values, outcome.String())
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

func (r *liveHostdRebindDisableRequests) incompatible(census liveHostdRebindDisableClaimCensus) *liveHostdRebindDisableRequestOutcome {
	if r == nil {
		return nil
	}
	for operationID := range r.expected {
		outcome, exists := r.outcomes[operationID]
		if !exists || census.pending(operationID) {
			continue
		}
		if outcome.transport != "" || outcome.response == "unexpected_status" {
			copy := outcome
			return &copy
		}
	}
	return nil
}

type liveHostdRebindDisableClaimCensus struct {
	expected  int
	pendingN  int
	missing   int
	committed int
	invalid   int
	duplicate int
	foreign   int
	counts    map[string]int
	pendingBy map[string]bool
}

func liveHostdRebindDisableClaimCensusFor(snapshot appaccess.AppAccessDisableStartupSnapshot,
	operationIDs []string,
) liveHostdRebindDisableClaimCensus {
	census := liveHostdRebindDisableClaimCensus{expected: len(operationIDs), counts: make(map[string]int, len(operationIDs)),
		pendingBy: make(map[string]bool, len(operationIDs))}
	expected := make(map[string]struct{}, len(operationIDs))
	for _, operationID := range operationIDs {
		expected[operationID] = struct{}{}
	}
	states := make(map[string]appaccess.AppAccessDisableState, len(operationIDs))
	for _, entry := range snapshot.Claims {
		operationID := entry.Claim.OperationID
		if _, wanted := expected[operationID]; !wanted {
			census.foreign++
			continue
		}
		census.counts[operationID]++
		states[operationID] = entry.Claim.State
	}
	for _, operationID := range operationIDs {
		count := census.counts[operationID]
		switch {
		case count == 0:
			census.missing++
		case count != 1:
			census.duplicate++
		case states[operationID] == appaccess.AppAccessDisableCommitted:
			census.committed++
		case states[operationID] == appaccess.AppAccessDisablePrepared ||
			states[operationID] == appaccess.AppAccessDisableWithdrawing ||
			states[operationID] == appaccess.AppAccessDisableUncertain:
			census.pendingN++
			census.pendingBy[operationID] = true
		default:
			census.invalid++
		}
	}
	return census
}

func (c liveHostdRebindDisableClaimCensus) pending(operationID string) bool {
	return c.pendingBy[operationID]
}

func (c liveHostdRebindDisableClaimCensus) complete() bool {
	return c.expected > 0 && c.pendingN == c.expected && c.missing == 0 && c.committed == 0 &&
		c.invalid == 0 && c.duplicate == 0
}

func (c liveHostdRebindDisableClaimCensus) strictError() error {
	switch {
	case c.duplicate != 0:
		return errors.New("pending LAN claim snapshot contains duplicate required operation")
	case c.committed != 0:
		return errors.New("pending LAN claim snapshot contains committed required operation")
	case c.invalid != 0:
		return errors.New("pending LAN claim snapshot contains invalid required operation state")
	default:
		return nil
	}
}

func (c liveHostdRebindDisableClaimCensus) String() string {
	return fmt.Sprintf("expected=%d pending=%d missing=%d committed=%d invalid=%d duplicate=%d foreign=%d",
		c.expected, c.pendingN, c.missing, c.committed, c.invalid, c.duplicate, c.foreign)
}

func liveHostdRebindClaimObservationBudget(now time.Time, parent context.Context,
	testDeadline time.Time, hasTestDeadline bool,
) (time.Duration, error) {
	deadlines := make([]time.Time, 0, 2)
	if hasTestDeadline {
		deadlines = append(deadlines, testDeadline)
	}
	if parent != nil {
		if deadline, ok := parent.Deadline(); ok {
			deadlines = append(deadlines, deadline)
		}
	}
	return liveHostdRebindBoundedBudget(now, liveHostdRebindClaimObservationCap, deadlines...)
}

// liveHostdRebindWaitDisableClaimsResult observes only the supplied
// repository snapshot. It uses the caller's bounded context for every read,
// retains request completions, and always obtains a fresh snapshot before an
// incompatible completed request is classified as missing its durable claim.
func liveHostdRebindWaitDisableClaimsResult(parent context.Context, testDeadline time.Time, hasTestDeadline bool,
	childDone <-chan struct{}, operationIDs []string, requests *liveHostdRebindDisableRequests,
	snapshot liveHostdRebindDisableSnapshot,
) (liveHostdRebindDisableClaimCensus, error) {
	var census liveHostdRebindDisableClaimCensus
	if parent == nil || snapshot == nil || requests == nil || len(operationIDs) == 0 || len(operationIDs) != len(requests.expected) {
		return census, errors.New("invalid pending LAN claim observation")
	}
	seen := make(map[string]struct{}, len(operationIDs))
	for _, operationID := range operationIDs {
		if operationID == "" {
			return census, errors.New("unbound pending LAN claim observation")
		}
		if _, duplicate := seen[operationID]; duplicate {
			return census, errors.New("duplicate pending LAN claim observation")
		}
		seen[operationID] = struct{}{}
		if requests.expected[operationID] == "" {
			return census, errors.New("unbound pending LAN claim observation")
		}
	}
	budget, err := liveHostdRebindClaimObservationBudget(time.Now(), parent, testDeadline, hasTestDeadline)
	if err != nil {
		return census, fmt.Errorf("pending LAN claim observation budget: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return census, fmt.Errorf("pending LAN claim observation ended: %s", liveHostdRebindFailureCategory(err))
		}
		if err := requests.drain(); err != nil {
			return census, fmt.Errorf("pending LAN request outcome retention: %s", liveHostdRebindFailureCategory(err))
		}
		if childDone != nil {
			select {
			case <-childDone:
				return census, errors.New("full hostd child exited during pending LAN claim observation")
			default:
			}
		}
		value, snapshotErr := snapshot(ctx)
		if snapshotErr != nil {
			return census, fmt.Errorf("pending LAN claim snapshot failed: %s", liveHostdRebindFailureCategory(snapshotErr))
		}
		census = liveHostdRebindDisableClaimCensusFor(value, operationIDs)
		if err := ctx.Err(); err != nil {
			return census, fmt.Errorf("pending LAN claim observation ended: %s", liveHostdRebindFailureCategory(err))
		}
		if childDone != nil {
			select {
			case <-childDone:
				return census, errors.New("full hostd child exited during pending LAN claim observation")
			default:
			}
		}
		if err := census.strictError(); err != nil {
			return census, err
		}
		if census.complete() {
			return census, nil
		}
		if outcome := requests.incompatible(census); outcome != nil {
			return census, fmt.Errorf("completed pending LAN request lacks its durable claim: %s", outcome)
		}
		select {
		case outcome := <-requests.done:
			if err := requests.retain(outcome); err != nil {
				return census, fmt.Errorf("pending LAN request outcome retention: %s", liveHostdRebindFailureCategory(err))
			}
			// Loop before classification so the just-retained response/error is
			// compared with a fresh context-bounded durable snapshot.
		case <-childDone:
			return census, errors.New("full hostd child exited during pending LAN claim observation")
		case <-ticker.C:
		case <-ctx.Done():
			return census, fmt.Errorf("pending LAN claim observation ended: %s", liveHostdRebindFailureCategory(ctx.Err()))
		}
	}
}

func liveHostdRebindFailureCategory(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "failed"
	}
}

func liveHostdRebindContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func liveHostdRebindDriftScenario(t *testing.T, kind string) {
	t.Helper()
	fixture := newLiveHostdGatewayRebindFixture(t)
	operationID := uuid.NewString()
	fixture.registerOperation(operationID)
	boundary := "p10-stage-serving"
	role := "stage"
	if kind == "configuration" {
		boundary = "p08-stage-config-copied"
	} else if kind == "census" {
		boundary, role = "p16-successor-serving", "final"
	} else if kind != "ownership" {
		t.Fatalf("unknown rebind drift scenario %q", kind)
	}
	child := liveHostdRebindStartChild(t, fixture, operationID, boundary, "")
	if liveHostdRebindWaitReady(t, fixture, child).RecoveryOnly {
		t.Fatal("fresh drift fixture entered recovery")
	}
	// Open the observer before the first public mutation. The refusal path below
	// reuses it for its bounded fence observation and never opens or migrates a
	// database after the child has failed.
	observerDB, err := database.Open(fixture.dataRoot)
	if err != nil {
		t.Fatal("open drift refusal observer")
	}
	defer func() {
		if err := observerDB.Close(); err != nil {
			t.Error("close drift refusal observer")
		}
	}()
	observer := appaccess.New(observerDB)
	successor := liveHostdRebindPlanSuccessor(t, fixture, apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	})
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, successor)
	requestContext, cancelRequest := context.WithCancel(fixture.ctx)
	defer cancelRequest()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		_, _, _ = liveHostdRebindHTTP(requestContext, fixture, http.MethodPost,
			"/api/v1/system/lan-gateway-rebind", commit)
	}()
	liveHostdRebindWaitBoundary(t, fixture, child)
	liveHostdRebindKillAndReap(t, child)
	cancelRequest()
	select {
	case <-requestDone:
	case <-time.After(20 * time.Second):
		t.Fatal("drift setup commit request did not stop")
	}
	containerID, containerName := liveHostdRebindOperationContainer(t, fixture, operationID, role)
	switch kind {
	case "ownership":
		drifted := containerName + "-ownership-drift"
		if _, err := controllerJourneyDocker(fixture.ctx, fixture.docker, nil,
			"container", "rename", containerID, drifted); err != nil {
			t.Fatal("rename exact-owned stage container for ownership drift:", err)
		}
	case "configuration":
		body := filepath.Join(fixture.root, "unexpected-stage-config.json")
		if err := os.WriteFile(body, []byte(`{"drift":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := controllerJourneyDocker(fixture.ctx, fixture.docker, nil,
			"container", "cp", body, containerID+":/config/unexpected-drift.json"); err != nil {
			t.Fatal("write test-owned stage configuration drift:", err)
		}
	case "census":
		liveHostdRebindAttachCensusDrift(t, fixture, containerID)
	}
	refusalChild := liveHostdRebindStartChild(t, fixture, "", "", "")
	testDeadline, hasTestDeadline := t.Deadline()
	refusal, refusalErr := liveHostdRebindStartupRefusalResult(fixture, refusalChild, testDeadline, hasTestDeadline,
		liveHostdRebindKillAndReapError)
	// A closed refusal failure does not make these read-only observations pass
	// the scenario. They record whether its exact retained container and fence
	// can still be inspected before the final assertion reports the failure.
	retained := liveHostdRebindContainerRetentionObservation(fixture, containerID)
	fence := liveHostdRebindFenceObservation(fixture.ctx, observer)
	if refusalErr != nil {
		t.Fatalf("drifted full hostd startup refusal failed: %v result=%s retained=%s fence=%s",
			refusalErr, refusal, retained, fence)
	}
	if retained != "retained" {
		t.Fatalf("drift refusal mutated an exact retained container: observation=%s", retained)
	}
	if fence != "held" {
		t.Fatalf("drift startup released or could not inspect the rebind fence: observation=%s", fence)
	}
}

func liveHostdRebindAssertContainerRetained(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	containerID string,
) {
	t.Helper()
	if observation := liveHostdRebindContainerRetentionObservation(fixture, containerID); observation != "retained" {
		t.Fatalf("drift refusal mutated an exact retained container: observation=%s", observation)
	}
}

func liveHostdRebindContainerRetentionObservation(fixture *liveHostdGatewayRebindFixture, containerID string) string {
	if fixture == nil || containerID == "" {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(fixture.ctx, liveHostdRebindChildReapTimeout)
	defer cancel()
	body, err := controllerJourneyDocker(ctx, fixture.docker, nil,
		"container", "inspect", "--format", "{{.Id}}", containerID)
	if err != nil {
		return "unknown"
	}
	got := strings.TrimPrefix(strings.TrimSpace(string(body)), "sha256:")
	want := strings.TrimPrefix(containerID, "sha256:")
	if got != want {
		return "missing"
	}
	return "retained"
}

func liveHostdRebindFenceObservation(parent context.Context, observer *appaccess.Repository) string {
	if parent == nil || observer == nil {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(parent, liveHostdRebindChildReapTimeout)
	defer cancel()
	err := observer.CheckGatewayRebindFence(ctx)
	switch {
	case errors.Is(err, appaccess.ErrGatewayRebindActive):
		return "held"
	case err == nil:
		return "released"
	default:
		return "unknown"
	}
}

func liveHostdRebindOperationContainer(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	operationID, role string,
) (string, string) {
	t.Helper()
	body, err := controllerJourneyDocker(fixture.ctx, fixture.docker, nil, "ps", "-aq", "--no-trunc",
		"--filter", "label=io.rig.operation-id="+operationID,
		"--filter", "label=io.rig.gateway-role="+role)
	ids := strings.Fields(string(body))
	if err != nil || len(ids) != 1 {
		t.Fatalf("locate exact %s container for operation %s: ids=%v error=%v", role, operationID, ids, err)
	}
	name, err := controllerJourneyDocker(fixture.ctx, fixture.docker, nil,
		"container", "inspect", "--format", "{{.Name}}", ids[0])
	cleanName := strings.TrimPrefix(strings.TrimSpace(string(name)), "/")
	if err != nil || cleanName == "" {
		t.Fatalf("inspect exact %s container name: %v", role, err)
	}
	return ids[0], cleanName
}

func liveHostdRebindAttachCensusDrift(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	containerID string,
) {
	t.Helper()
	name := "rig-rebind-census-drift-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	body, err := controllerJourneyDocker(fixture.ctx, fixture.docker, nil, "network", "create", name)
	networkID := strings.TrimSpace(string(body))
	if err != nil || networkID == "" {
		t.Fatalf("create test-owned census drift network: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = controllerJourneyDocker(ctx, fixture.docker, nil,
			"network", "disconnect", "--force", networkID, containerID)
		inspected, inspectErr := controllerJourneyDocker(ctx, fixture.docker, nil,
			"network", "inspect", "--format", "{{.Name}}", networkID)
		if inspectErr != nil || strings.TrimSpace(string(inspected)) != name {
			t.Error("retain census drift network because exact identity changed")
			return
		}
		if _, err := controllerJourneyDocker(ctx, fixture.docker, nil, "network", "rm", networkID); err != nil {
			t.Error("remove exact test-owned census drift network")
		}
	})
	if _, err := controllerJourneyDocker(fixture.ctx, fixture.docker, nil,
		"network", "connect", networkID, containerID); err != nil {
		t.Fatal("attach test-owned census drift network:", err)
	}
}

func liveHostdRebindAssertStartupRefusal(t *testing.T, fixture *liveHostdGatewayRebindFixture) {
	t.Helper()
	child := liveHostdRebindStartChild(t, fixture, "", "", "")
	deadline, hasDeadline := t.Deadline()
	result, err := liveHostdRebindStartupRefusalResult(fixture, child, deadline, hasDeadline,
		liveHostdRebindKillAndReapError)
	if err != nil {
		t.Fatalf("drifted full hostd startup refusal failed: %v result=%s", err, result)
	}
}

type liveHostdRebindStartupRefusalObservation struct {
	exit       string
	marker     string
	diagnostic string
}

func (r liveHostdRebindStartupRefusalObservation) String() string {
	return fmt.Sprintf("exit=%s marker=%s stderr=%s", r.exit, r.marker, r.diagnostic)
}

func liveHostdRebindStartupRefusalBudget(now time.Time, parent context.Context,
	testDeadline time.Time, hasTestDeadline bool,
) (time.Duration, error) {
	deadlines := make([]time.Time, 0, 2)
	if hasTestDeadline {
		deadlines = append(deadlines, testDeadline)
	}
	if parent != nil {
		if deadline, ok := parent.Deadline(); ok {
			deadlines = append(deadlines, deadline)
		}
	}
	return liveHostdRebindBoundedBudget(now, liveHostdRebindStartupRefusalCap, deadlines...)
}

// liveHostdRebindStartupRefusalResult closes the exact child before it reads
// stderr or a ready marker. Its returned fields are all bounded categories or
// the existing closed stderr digest, so a refusal failure cannot expose raw
// child output or a marker body.
func liveHostdRebindStartupRefusalResult(fixture *liveHostdGatewayRebindFixture,
	child *liveHostdRebindChild, testDeadline time.Time, hasTestDeadline bool,
	reap func(*liveHostdRebindChild) error,
) (liveHostdRebindStartupRefusalObservation, error) {
	result := liveHostdRebindStartupRefusalObservation{exit: "unknown", marker: "unread", diagnostic: "unread"}
	if fixture == nil || child == nil || reap == nil {
		return result, errors.New("invalid full hostd startup refusal control")
	}
	budget, budgetErr := liveHostdRebindStartupRefusalBudget(time.Now(), fixture.ctx, testDeadline, hasTestDeadline)
	if budgetErr == nil {
		ctx, cancel := context.WithTimeout(fixture.ctx, budget)
		child.startWait()
		if err := ctx.Err(); err != nil {
			result.exit = liveHostdRebindFailureCategory(err)
			budgetErr = err
		} else {
			select {
			case <-ctx.Done():
				result.exit = liveHostdRebindFailureCategory(ctx.Err())
				budgetErr = ctx.Err()
			case <-child.waitDone:
				if err := ctx.Err(); err != nil {
					result.exit = liveHostdRebindFailureCategory(err)
					budgetErr = err
				} else if child.waitErr == nil {
					result.exit = "zero"
				} else {
					result.exit = "nonzero"
				}
			}
		}
		cancel()
	}
	if reapErr := reap(child); reapErr != nil {
		// Wait did not complete, so child.stderr may still be receiving bytes.
		// Do not race that buffer merely to decorate a failed reaping result.
		return result, fmt.Errorf("full hostd startup refusal child cleanup failed: %s",
			liveHostdRebindFailureCategory(reapErr))
	}
	result.diagnostic = liveHostdRebindClosedDiagnostic(child.stderr.String())
	result.marker = liveHostdRebindClosedReadyMarkerCategory(fixture, child)
	if budgetErr != nil {
		return result, fmt.Errorf("full hostd startup refusal wait failed: %s", liveHostdRebindFailureCategory(budgetErr))
	}
	if child.waitErr == nil {
		return result, errors.New("drifted full hostd reported successful startup")
	}
	if result.marker != "absent" {
		return result, errors.New("drifted full hostd assembled an API handler")
	}
	return result, nil
}

func liveHostdRebindClosedChildFacts(fixture *liveHostdGatewayRebindFixture, child *liveHostdRebindChild) string {
	if fixture == nil || child == nil || !liveHostdRebindChildWaited(child) {
		return "unavailable"
	}
	exit := "nonzero"
	if child.waitErr == nil {
		exit = "zero"
	}
	return fmt.Sprintf("exit=%s marker=%s stderr=%s", exit,
		liveHostdRebindClosedReadyMarkerCategory(fixture, child), liveHostdRebindClosedDiagnostic(child.stderr.String()))
}

func liveHostdRebindClosedReadyMarkerCategory(fixture *liveHostdGatewayRebindFixture,
	child *liveHostdRebindChild,
) string {
	if fixture == nil || child == nil || child.manifest.ReadyMarker == "" {
		return "invalid"
	}
	file, err := os.Open(child.manifest.ReadyMarker)
	if errors.Is(err, os.ErrNotExist) {
		return "absent"
	}
	if err != nil {
		return "invalid"
	}
	body, readErr := io.ReadAll(io.LimitReader(file, 8193))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(body) == 0 || len(body) > 8192 {
		return "invalid"
	}
	var marker liveHostdRebindReadyMarker
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || json.Unmarshal(body, &marker) != nil || marker.Address != fixture.listen {
		return "invalid"
	}
	for field := range fields {
		if field != "address" && field != "recoveryOnly" && field != "recoveryKind" && field != "operationId" {
			return "invalid"
		}
	}
	if !marker.RecoveryOnly {
		if marker.RecoveryKind != "" || marker.OperationID != "" {
			return "invalid"
		}
		return "normal"
	}
	if marker.RecoveryKind == "" || uuid.Validate(marker.OperationID) != nil {
		return "invalid"
	}
	return "recovery-only"
}

func liveHostdRebindEmergencyWithdrawalScenario(t *testing.T) {
	t.Helper()
	fixture := newLiveHostdGatewayRebindFixture(t)
	operationID := liveHostdRebindCompleteHistorical(t, fixture)
	containerID, _ := liveHostdRebindOperationContainer(t, fixture, operationID, "final")
	child := liveHostdRebindStartChild(t, fixture, operationID, "", "emergency-withdrawal-failure")
	marker := liveHostdRebindWaitBoundary(t, fixture, child)
	if marker.Boundary != "emergency_withdrawal_failed" {
		t.Fatalf("emergency withdrawal marker=%q", marker.Boundary)
	}
	child.startWait()
	select {
	case <-child.waitDone:
		if child.waitErr == nil {
			t.Fatal("failed emergency withdrawal reported successful startup")
		}
	case <-time.After(2 * time.Minute):
		liveHostdRebindKillAndReap(t, child)
		t.Fatal("failed emergency withdrawal did not fail-stop")
	}
	if _, err := os.Stat(child.manifest.ReadyMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed emergency withdrawal exposed an API handler")
	}
	body, err := controllerJourneyDocker(fixture.ctx, fixture.docker, nil,
		"container", "inspect", "--format", "{{.State.Running}}", containerID)
	if err != nil || strings.TrimSpace(string(body)) != "true" {
		t.Fatalf("failed emergency withdrawal guessed success: running=%q error=%v", strings.TrimSpace(string(body)), err)
	}
}

// These functions are intentionally present as named executable checkpoints.
// Their concrete mutation bodies are kept below the shared unresolved helpers
// so the closed manifest and CI discovery cannot silently omit a case.
func liveHostdRebindRunPendingRouteRestart(t *testing.T) {
	liveHostdRebindPendingCurrentScenario(t, "route")
}
func liveHostdRebindRunPendingLANRestart(t *testing.T) {
	liveHostdRebindPendingCurrentScenario(t, "lan")
}
func liveHostdRebindRunPendingLANBatchRestart(t *testing.T) {
	liveHostdRebindMigratedPendingLANBatchScenario(t)
}
func liveHostdRebindRunOwnershipDrift(t *testing.T) { liveHostdRebindDriftScenario(t, "ownership") }
func liveHostdRebindRunConfigurationDrift(t *testing.T) {
	liveHostdRebindDriftScenario(t, "configuration")
}
func liveHostdRebindRunCensusDrift(t *testing.T) { liveHostdRebindDriftScenario(t, "census") }
func liveHostdRebindRunEmergencyWithdrawalFailure(t *testing.T) {
	liveHostdRebindEmergencyWithdrawalScenario(t)
}
