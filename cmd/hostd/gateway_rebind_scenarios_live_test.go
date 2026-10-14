//go:build live_docker

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	t.Helper()
	fixture := newLiveHostdGatewayRebindFixture(t)
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
	profile := liveHostdRebindConfigureSuccessor(t, fixture, apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	})
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, profile)
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
	profile := liveHostdRebindConfigureSuccessor(t, fixture, apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	})
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, profile)
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
	profile := liveHostdRebindConfigureSuccessor(t, fixture, apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	})
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, profile)
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
	profile := liveHostdRebindConfigureSuccessor(t, fixture, apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	})
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, profile)
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
	defer cancelRequests()
	requestDone := make(chan struct{}, 2)
	pendingRequests := 0
	var recoveryOperations []string
	var secondDisableOperation string
	var secondDisableRequest apicontract.DisableLANAppAccessRequest
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
	case "lan", "lan-batch":
		firstID, firstRequest := liveHostdRebindDisableRequest(t, fixture, fixture.app.id)
		recoveryOperations = append(recoveryOperations, firstID)
		if kind == "lan-batch" {
			// Obtain both reviews before the first true current mutation can
			// hold the runtime lock at its post-effect lost-ack barrier.
			secondDisableOperation, secondDisableRequest = liveHostdRebindDisableRequest(t, fixture, fixture.secondApp.id)
			recoveryOperations = append(recoveryOperations, secondDisableOperation)
		}
		go func() {
			defer func() { requestDone <- struct{}{} }()
			_, _, _ = liveHostdRebindHTTP(requestContext, fixture, http.MethodPost,
				"/api/v1/apps/"+fixture.app.id+"/lan-access/disables", firstRequest)
		}()
		pendingRequests++
	default:
		t.Fatalf("unknown pending current scenario %q", kind)
	}
	liveHostdRebindWaitBoundary(t, fixture, child)
	if kind == "lan-batch" {
		go func() {
			defer func() { requestDone <- struct{}{} }()
			_, _, _ = liveHostdRebindHTTP(requestContext, fixture, http.MethodPost,
				"/api/v1/apps/"+fixture.secondApp.id+"/lan-access/disables", secondDisableRequest)
		}()
		pendingRequests++
		liveHostdRebindWaitDisableClaims(t, fixture, recoveryOperations)
	}
	liveHostdRebindKillAndReap(t, child)
	cancelRequests()
	for range pendingRequests {
		select {
		case <-requestDone:
		case <-time.After(20 * time.Second):
			t.Fatal("pending current request did not stop with its killed hostd")
		}
	}
	fresh := liveHostdRebindStartChild(t, fixture, "", "", "")
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
		liveHostdRebindWaitDisableClaims(t, fixture, recoveryOperations)
	}
	liveHostdRebindKillAndReap(t, fresh)
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

func liveHostdRebindWaitDisableClaims(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	operationIDs []string,
) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		db, err := database.Open(fixture.dataRoot)
		if err == nil {
			snapshot, snapshotErr := appaccess.New(db).AppAccessDisableStartupSnapshot(context.Background())
			closeErr := db.Close()
			if snapshotErr == nil && closeErr == nil {
				found := make(map[string]bool, len(operationIDs))
				for _, claim := range snapshot.Claims {
					found[claim.Claim.OperationID] = claim.Claim.State != appaccess.AppAccessDisableCommitted
				}
				complete := true
				for _, operationID := range operationIDs {
					complete = complete && found[operationID]
				}
				if complete {
					return
				}
			}
		}
		select {
		case <-fixture.ctx.Done():
			t.Fatal("pending LAN claims were not durably retained")
		case <-ticker.C:
		}
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
	profile := liveHostdRebindConfigureSuccessor(t, fixture, apicontract.LANGatewayProfileSpec{
		InterfaceID: fixture.successor.InterfaceID, SelectedIpv4: fixture.successor.IPv4,
		PortStart: int(fixture.ports[0]), PortEnd: int(fixture.ports[2]),
	})
	commit := liveHostdRebindPrepareRequest(t, fixture, operationID, profile)
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
	liveHostdRebindAssertStartupRefusal(t, fixture)
	liveHostdRebindAssertContainerRetained(t, fixture, containerID)
	db, err := database.Open(fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	fenceErr := appaccess.New(db).CheckGatewayRebindFence(context.Background())
	closeErr := db.Close()
	if fenceErr == nil || closeErr != nil {
		t.Fatalf("drift startup released the rebind fence: fence=%v close=%v", fenceErr, closeErr)
	}
}

func liveHostdRebindAssertContainerRetained(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	containerID string,
) {
	t.Helper()
	body, err := controllerJourneyDocker(fixture.ctx, fixture.docker, nil,
		"container", "inspect", "--format", "{{.Id}}", containerID)
	got := strings.TrimPrefix(strings.TrimSpace(string(body)), "sha256:")
	want := strings.TrimPrefix(containerID, "sha256:")
	if err != nil || got != want {
		t.Fatalf("drift refusal mutated an exact retained container: got=%q want=%q error=%v", got, want, err)
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
	child.startWait()
	select {
	case <-child.waitDone:
		if child.waitErr == nil {
			t.Fatal("drifted full hostd reported successful startup")
		}
	case <-time.After(2 * time.Minute):
		liveHostdRebindKillAndReap(t, child)
		t.Fatal("drifted full hostd did not refuse startup")
	}
	if _, err := os.Stat(child.manifest.ReadyMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("drifted full hostd assembled an API handler")
	}
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
	liveHostdRebindPendingCurrentScenario(t, "lan-batch")
}
func liveHostdRebindRunOwnershipDrift(t *testing.T) { liveHostdRebindDriftScenario(t, "ownership") }
func liveHostdRebindRunConfigurationDrift(t *testing.T) {
	liveHostdRebindDriftScenario(t, "configuration")
}
func liveHostdRebindRunCensusDrift(t *testing.T) { liveHostdRebindDriftScenario(t, "census") }
func liveHostdRebindRunEmergencyWithdrawalFailure(t *testing.T) {
	liveHostdRebindEmergencyWithdrawalScenario(t)
}
