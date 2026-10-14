package controller

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedingress"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	liveLANGatewayRebindRecoveryManifestEnvironment = "RIG_TEST_LAN_GATEWAY_REBIND_RECOVERY_MANIFEST"
	liveLANGatewayRebindRecoveryPermission          = "RIG_RUN_LIVE_LAN_GATEWAY_REBIND_RECOVERY"
	liveLANGatewayRebindRecoveryPurpose             = "hostd/controller/lan-gateway-rebind-recovery-live/v1"
	liveLANGatewayRebindRecoveryStagePrefix         = "LAN_GATEWAY_REBIND_RECOVERY_STAGE:"
	liveLANGatewayRebindRecoveryMarker              = "LAN_GATEWAY_REBIND_RECOVERY_HTTP_OK"
)

type liveLANGatewayRebindRecoveryManifest struct {
	Version               int    `json:"version"`
	Purpose               string `json:"purpose"`
	DataRoot              string `json:"dataRoot"`
	WorkingDirectory      string `json:"workingDirectory"`
	DockerExecutable      string `json:"dockerExecutable"`
	DockerConfigDirectory string `json:"dockerConfigDirectory"`
	HostPort              uint16 `json:"hostPort"`
	OperationID           string `json:"operationId"`
	SessionToken          string `json:"sessionToken"`
	CSRFToken             string `json:"csrfToken"`
}

// TestLiveLANGatewayRebindRecoveryChild is selected only by the parent-owned
// generated-ingress live fixture. The parent prepares and cleans real Docker,
// SQL and protected state; this child owns only the independent real HTTP
// request journey through the dormant handlers.
func TestLiveLANGatewayRebindRecoveryChild(t *testing.T) {
	manifestPath := os.Getenv(liveLANGatewayRebindRecoveryManifestEnvironment)
	if manifestPath == "" {
		return
	}
	if os.Getenv("RIG_RUN_LIVE_GATEWAY_V2") != "1" ||
		os.Getenv(liveLANGatewayRebindRecoveryPermission) != "1" ||
		runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("controller recovery child requires explicit disposable local Linux Docker permission")
	}
	manifest, err := readLiveLANGatewayRebindRecoveryManifest(manifestPath)
	if err != nil {
		t.Fatal("read private controller recovery child manifest")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	db, err := database.Open(manifest.DataRoot)
	if err != nil {
		t.Fatal("open controller recovery child database")
	}
	defer db.Close()
	repository := appaccess.New(db)
	manager, err := generatedingress.New(runtimeprocess.ExecRunner{}, generatedingress.Options{
		DataRoot: manifest.DataRoot, WorkingDirectory: manifest.WorkingDirectory,
		DockerExecutable: manifest.DockerExecutable, DockerConfigDirectory: manifest.DockerConfigDirectory,
		HostPort: manifest.HostPort, CommandTimeout: 45 * time.Second, PullTimeout: 5 * time.Minute,
		OutputLimit: 64 << 10, RebindFenceCheck: repository.CheckGatewayRebindFence,
		RebindCurrentStateRepository: repository,
	})
	if err != nil {
		t.Fatal("open controller recovery child manager")
	}
	controller := &Server{
		Auth: auth.New(db), GeneratedRuntime: true,
		GatewayRebindRecoveries: repository, GatewayRebindRecoveryRuntime: manager,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/system/lan-gateway-rebind/{operationId}",
		noStore(controller.requireOperation(operationGetLANGatewayRebind, controller.getLANGatewayRebind)))
	mux.HandleFunc("POST /api/v1/system/lan-gateway-rebind/{operationId}/recovery-proposal",
		noStore(controller.requireOperation(operationInspectLANGatewayRebindRecovery,
			controller.inspectLANGatewayRebindRecovery)))
	mux.HandleFunc("POST /api/v1/system/lan-gateway-rebind/{operationId}/reconcile",
		noStore(controller.requireOperation(operationReconcileLANGatewayRebind,
			controller.reconcileLANGatewayRebind)))
	endpoint := httptest.NewServer(mux)
	defer endpoint.Close()

	request := func(method, suffix string, body []byte) (int, []byte) {
		t.Helper()
		value, requestErr := http.NewRequestWithContext(ctx, method, endpoint.URL+suffix, bytes.NewReader(body))
		if requestErr != nil {
			t.Fatal("build controller recovery HTTP request")
		}
		value.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: manifest.SessionToken})
		if method == http.MethodPost {
			value.Header.Set("Content-Type", "application/json")
			value.Header.Set("X-CSRF-Token", manifest.CSRFToken)
		}
		response, requestErr := endpoint.Client().Do(value)
		if requestErr != nil {
			t.Fatal("execute controller recovery HTTP request")
		}
		defer response.Body.Close()
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
		if readErr != nil || len(responseBody) > 64<<10 || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("read bounded no-store controller recovery response")
		}
		return response.StatusCode, responseBody
	}
	basePath := "/api/v1/system/lan-gateway-rebind/" + manifest.OperationID

	fmt.Println(liveLANGatewayRebindRecoveryStagePrefix + "prepared-status")
	statusCode, body := request(http.MethodGet, basePath, nil)
	initial := decodeLiveLANGatewayRebindRecoveryJSON[lanGatewayRebindStatus](t, statusCode, body)
	if statusCode != http.StatusOK || initial.OperationID != manifest.OperationID || !initial.Active ||
		initial.Availability != string(generatedingress.GatewayRebindOperationAvailable) || initial.Operation == nil ||
		initial.Operation.StoredState != string(appaccess.GatewayRebindPrepared) || initial.Operation.StateSequence != 1 ||
		initial.OperationFence != string(generatedingress.GatewayRebindOperationFenceHeld) {
		t.Fatal("prepared controller status did not preserve the parent fixture identity")
	}
	assertLiveLANGatewayRebindCuratedBody(t, body)

	fmt.Println(liveLANGatewayRebindRecoveryStagePrefix + "prepared-review")
	statusCode, body = request(http.MethodPost, basePath+"/recovery-proposal", []byte(`{}`))
	review := decodeLiveLANGatewayRebindRecoveryJSON[lanGatewayRebindRecoveryReview](t, statusCode, body)
	if statusCode != http.StatusOK || review.OperationID != manifest.OperationID || review.Version != 1 ||
		review.Status.Operation == nil || review.Status.Operation.StoredState != string(appaccess.GatewayRebindPrepared) ||
		review.Status.Protected == nil || review.Status.Protected.Phase != "successor_intent" ||
		review.Status.Protected.RecordSequence != 1 ||
		review.Status.RecoveryPolicy != string(appaccess.GatewayRebindRecoveryEvidenceDecides) ||
		review.Status.CurrentProof != string(generatedingress.GatewayRebindProofVerified) ||
		len(review.Status.AllowedActions) != 1 ||
		review.Status.AllowedActions[0] != string(generatedingress.GatewayRebindRecoveryActionReconcile) ||
		len(review.RecoveryDigest) != 64 || len(review.RecoveryApprovalDigest) != 64 {
		t.Fatal("prepared controller recovery review was not executable")
	}
	assertLiveLANGatewayRebindCuratedBody(t, body)
	if audits := liveLANGatewayRebindRecoveryAuditCount(t, db, manifest.OperationID); audits != 0 {
		t.Fatal("read-only status or recovery review wrote an audit")
	}

	tamperedApproval := review.RecoveryApprovalDigest
	if tamperedApproval[0] == 'a' {
		tamperedApproval = "b" + tamperedApproval[1:]
	} else {
		tamperedApproval = "a" + tamperedApproval[1:]
	}
	tamperedBody := liveLANGatewayRebindReconcileBody(t, review.RecoveryDigest, tamperedApproval)
	fmt.Println(liveLANGatewayRebindRecoveryStagePrefix + "altered-consent")
	statusCode, body = request(http.MethodPost, basePath+"/reconcile", tamperedBody)
	if statusCode != http.StatusForbidden || !bytes.Contains(body, []byte(`"code":"lan_gateway_approval_mismatch"`)) ||
		liveLANGatewayRebindRecoveryAuditCount(t, db, manifest.OperationID) != 0 {
		t.Fatal("actor-bound recovery consent mismatch was not refused before audit")
	}

	fmt.Println(liveLANGatewayRebindRecoveryStagePrefix + "post-refusal-review")
	statusCode, body = request(http.MethodPost, basePath+"/recovery-proposal", []byte(`{}`))
	reviewAfterRefusal := decodeLiveLANGatewayRebindRecoveryJSON[lanGatewayRebindRecoveryReview](t, statusCode, body)
	if statusCode != http.StatusOK || reviewAfterRefusal.RecoveryDigest != review.RecoveryDigest ||
		reviewAfterRefusal.RecoveryApprovalDigest != review.RecoveryApprovalDigest {
		t.Fatal("refused consent changed the executable recovery review")
	}

	approvedBody := liveLANGatewayRebindReconcileBody(t, review.RecoveryDigest, review.RecoveryApprovalDigest)
	fmt.Println(liveLANGatewayRebindRecoveryStagePrefix + "approved-reconcile")
	statusCode, body = request(http.MethodPost, basePath+"/reconcile", approvedBody)
	reconciled := decodeLiveLANGatewayRebindRecoveryJSON[lanGatewayRebindReconcileResponse](t, statusCode, body)
	if statusCode != http.StatusOK || reconciled.Version != generatedingress.GatewayRebindReconcileVersion ||
		reconciled.Status.OperationID != manifest.OperationID || reconciled.Status.Active ||
		!reconciled.Status.HistoricalTerminal || reconciled.Status.Operation == nil ||
		reconciled.Status.Operation.StoredState != string(appaccess.GatewayRebindCommitted) ||
		reconciled.Status.Operation.TerminalDisposition != string(appaccess.GatewayRebindDispositionCommit) ||
		!reconciled.Status.Operation.DatabaseCommitObserved ||
		reconciled.Status.OperationFence != string(generatedingress.GatewayRebindOperationFenceReleased) ||
		reconciled.Status.TerminalProof != string(generatedingress.GatewayRebindProofVerified) ||
		reconciled.Status.CurrentProof != string(generatedingress.GatewayRebindProofVerified) ||
		reconciled.Status.Protected == nil ||
		reconciled.Status.Protected.FixedDisposition != string(appaccess.GatewayRebindDispositionCommit) ||
		reconciled.Status.Protected.TerminalReceiptDigest == "" ||
		reconciled.Status.Protected.TerminalReceiptDigest != reconciled.Status.Operation.TerminalReceiptDigest ||
		len(reconciled.Status.AllowedActions) != 0 {
		t.Fatal("approved HTTP reconciliation did not return exact committed authority")
	}
	assertLiveLANGatewayRebindCuratedBody(t, body)
	if liveLANGatewayRebindRecoveryAuditCount(t, db, manifest.OperationID) != 1 {
		t.Fatal("approved reconciliation did not write exactly one purpose-bound audit")
	}

	fmt.Println(liveLANGatewayRebindRecoveryStagePrefix + "terminal-status")
	statusCode, body = request(http.MethodGet, basePath, nil)
	replayed := decodeLiveLANGatewayRebindRecoveryJSON[lanGatewayRebindStatus](t, statusCode, body)
	if statusCode != http.StatusOK || replayed.Operation == nil || replayed.Active || !replayed.HistoricalTerminal ||
		replayed.Operation.StoredState != reconciled.Status.Operation.StoredState ||
		replayed.Operation.TerminalDisposition != reconciled.Status.Operation.TerminalDisposition ||
		replayed.Operation.TerminalReceiptDigest != reconciled.Status.Operation.TerminalReceiptDigest ||
		replayed.OperationFence != string(generatedingress.GatewayRebindOperationFenceReleased) {
		t.Fatal("terminal status replay changed the committed operation identity")
	}
	assertLiveLANGatewayRebindCuratedBody(t, body)

	fmt.Println(liveLANGatewayRebindRecoveryStagePrefix + "terminal-review")
	statusCode, body = request(http.MethodPost, basePath+"/recovery-proposal", []byte(`{}`))
	terminalReview := decodeLiveLANGatewayRebindRecoveryJSON[lanGatewayRebindRecoveryReview](t, statusCode, body)
	if statusCode != http.StatusOK || terminalReview.RecoveryDigest != "" ||
		terminalReview.RecoveryApprovalDigest != "" || len(terminalReview.Status.AllowedActions) != 0 {
		t.Fatal("terminal recovery review remained executable")
	}
	assertLiveLANGatewayRebindCuratedBody(t, body)

	fmt.Println(liveLANGatewayRebindRecoveryStagePrefix + "stale-reconcile")
	statusCode, body = request(http.MethodPost, basePath+"/reconcile", approvedBody)
	if statusCode != http.StatusConflict ||
		!bytes.Contains(body, []byte(`"code":"lan_gateway_conflict"`)) ||
		liveLANGatewayRebindRecoveryAuditCount(t, db, manifest.OperationID) != 1 {
		t.Fatal("stale terminal recovery consent was replayable")
	}
	fmt.Println(liveLANGatewayRebindRecoveryStagePrefix + "post-stale-status")
	statusCode, body = request(http.MethodGet, basePath, nil)
	finalStatus := decodeLiveLANGatewayRebindRecoveryJSON[lanGatewayRebindStatus](t, statusCode, body)
	if statusCode != http.StatusOK || finalStatus.Operation == nil ||
		finalStatus.Operation.TerminalReceiptDigest != replayed.Operation.TerminalReceiptDigest {
		t.Fatal("refused terminal replay changed the committed receipt")
	}
	fmt.Println(liveLANGatewayRebindRecoveryMarker)
}

func readLiveLANGatewayRebindRecoveryManifest(path string) (liveLANGatewayRebindRecoveryManifest, error) {
	var value liveLANGatewayRebindRecoveryManifest
	if !liveLANGatewayRebindRecoverySafePath(path) || filepath.Ext(path) != ".json" {
		return value, appaccess.ErrInvalidInput
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 ||
		info.Size() <= 0 || info.Size() > 16<<10 {
		return value, appaccess.ErrInvalidInput
	}
	body, err := os.ReadFile(path)
	if err != nil || len(body) == 0 || len(body) > 16<<10 {
		return value, appaccess.ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return liveLANGatewayRebindRecoveryManifest{}, appaccess.ErrInvalidInput
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return liveLANGatewayRebindRecoveryManifest{}, appaccess.ErrInvalidInput
	}
	if value.Version != 1 || value.Purpose != liveLANGatewayRebindRecoveryPurpose ||
		!liveLANGatewayRebindRecoverySafePath(value.DataRoot) ||
		!liveLANGatewayRebindRecoverySafePath(value.WorkingDirectory) ||
		!liveLANGatewayRebindRecoverySafePath(value.DockerExecutable) ||
		!liveLANGatewayRebindRecoverySafePath(value.DockerConfigDirectory) ||
		value.HostPort == 0 || !validCanonicalUUID(value.OperationID) ||
		value.SessionToken == "" || len(value.SessionToken) > 256 ||
		value.CSRFToken == "" || len(value.CSRFToken) > 256 {
		return liveLANGatewayRebindRecoveryManifest{}, appaccess.ErrInvalidInput
	}
	return value, nil
}

func liveLANGatewayRebindRecoverySafePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func liveLANGatewayRebindReconcileBody(t *testing.T, recoveryDigest, approvalDigest string) []byte {
	t.Helper()
	body, err := json.Marshal(struct {
		Version                int    `json:"version"`
		Action                 string `json:"action"`
		RecoveryDigest         string `json:"recoveryDigest"`
		RecoveryApprovalDigest string `json:"recoveryApprovalDigest"`
	}{
		Version: generatedingress.GatewayRebindReconcileVersion, Action: "reconcile",
		RecoveryDigest: recoveryDigest, RecoveryApprovalDigest: approvalDigest,
	})
	if err != nil {
		t.Fatal("encode controller recovery consent")
	}
	return body
}

func decodeLiveLANGatewayRebindRecoveryJSON[T any](t *testing.T, _ int, body []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal("decode controller recovery response")
	}
	return value
}

func liveLANGatewayRebindRecoveryAuditCount(t *testing.T, db *sql.DB, operationID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events
		WHERE resource_type='lan_gateway_rebind' AND action=? AND resource_id=?`,
		appaccess.GatewayRebindReconcilePurpose, operationID).Scan(&count); err != nil {
		t.Fatal("count controller recovery audit rows")
	}
	return count
}

func assertLiveLANGatewayRebindCuratedBody(t *testing.T, body []byte) {
	t.Helper()
	for _, forbidden := range []string{
		"databaseCommittedEvent", "scanDigest", "intentDigest", "recordDigest", "snapshot",
		"authorization", "auditEventId", "auditedAt", "createdAt", "sessionToken", "csrfToken",
	} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("controller recovery response exposed forbidden field %q", forbidden)
		}
	}
}
