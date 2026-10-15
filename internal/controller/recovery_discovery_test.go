package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedingress"
)

type controllerRecoveryDiscoveryFake struct {
	operationID string
	err         error
	calls       int
	onCall      func()
}

func (f *controllerRecoveryDiscoveryFake) ActiveGatewayRebind(context.Context) (string, error) {
	f.calls++
	if f.onCall != nil {
		f.onCall()
	}
	return f.operationID, f.err
}

type controllerRecoveryPresenceRepository struct {
	generatedingress.GatewayRebindRecoveryReadRepository
	snapshots []appaccess.GatewayRebindRecoverySnapshot
	calls     int
}

func (r *controllerRecoveryPresenceRepository) GatewayRebindRecoverySnapshot(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
	r.calls++
	if r.calls > len(r.snapshots) {
		return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("unexpected recovery snapshot read")
	}
	return r.snapshots[r.calls-1], nil
}

type controllerRecoveryRebindInspectorFake struct {
	inspection generatedingress.GatewayRebindOperationInspection
	err        error
	calls      int
	repository generatedingress.GatewayRebindRecoveryReadRepository
	operation  string
}

func (f *controllerRecoveryRebindInspectorFake) InspectGatewayRebindOperation(_ context.Context,
	repository generatedingress.GatewayRebindRecoveryReadRepository, operationID string,
) (generatedingress.GatewayRebindOperationInspection, error) {
	f.calls++
	f.repository, f.operation = repository, operationID
	return f.inspection, f.err
}

type controllerRecoverySnapshotFake struct {
	snapshot  appaccess.HostingGatewayStartupSnapshot
	snapshots []appaccess.HostingGatewayStartupSnapshot
	err       error
	calls     int
	onCall    func(int)
}

func (f *controllerRecoverySnapshotFake) HostingGatewayStartupSnapshot(context.Context) (appaccess.HostingGatewayStartupSnapshot, error) {
	f.calls++
	if f.onCall != nil {
		f.onCall(f.calls)
	}
	if f.calls <= len(f.snapshots) {
		return f.snapshots[f.calls-1], f.err
	}
	return f.snapshot, f.err
}

type controllerRecoveryStartupFake struct {
	upgrade      generatedingress.GatewayV2StartupInspection
	upgradeErr   error
	upgradeCalls int
	access       generatedingress.GatewayV2LANAccessStartupInspection
	accessErr    error
	accessCalls  int
}

func (f *controllerRecoveryStartupFake) InspectGatewayV2Startup(context.Context,
	[]generatedingress.GatewayV2StartupClaim,
) (generatedingress.GatewayV2StartupInspection, error) {
	f.upgradeCalls++
	return f.upgrade, f.upgradeErr
}

func (f *controllerRecoveryStartupFake) InspectGatewayV2LANAccessStartup(context.Context,
	[]generatedingress.GatewayV2LANStartupClaim, []generatedingress.GatewayV2LANDisableStartupClaim,
) (generatedingress.GatewayV2LANAccessStartupInspection, error) {
	f.accessCalls++
	return f.access, f.accessErr
}

func TestControllerRecoveryDiscoveryReadsRealPresenceAndExactInspection(t *testing.T) {
	t.Run("validated absence", func(t *testing.T) {
		root := t.TempDir()
		db, err := database.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		protectedRoot := filepath.Join(root, "runtime", "generated-ingress")
		discovery := NewControllerRecoveryDiscovery(root, appaccess.New(db), nil)
		operationID, err := discovery.ActiveGatewayRebind(context.Background())
		if err != nil || operationID != "" {
			t.Fatalf("validated absence operation=%q error=%v", operationID, err)
		}
		if _, err := os.Lstat(protectedRoot); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read-only absence created protected storage: %v", err)
		}
	})

	t.Run("corrupt presence", func(t *testing.T) {
		root := t.TempDir()
		db, err := database.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		protectedRoot := filepath.Join(root, "runtime", "generated-ingress")
		if err := os.MkdirAll(protectedRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(protectedRoot, "unexpected-recovery-artifact"), []byte("corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		inspector := &controllerRecoveryRebindInspectorFake{}
		discovery := NewControllerRecoveryDiscovery(root, appaccess.New(db), inspector)
		if operationID, err := discovery.ActiveGatewayRebind(context.Background()); err == nil || operationID != "" || inspector.calls != 0 {
			t.Fatalf("corrupt presence operation=%q error=%v inspector_calls=%d", operationID, err, inspector.calls)
		}
	})

	t.Run("active exact inspection", func(t *testing.T) {
		active := controllerRecoveryActiveRebindSnapshot(rebindRequestOperation)
		repository := &controllerRecoveryPresenceRepository{snapshots: []appaccess.GatewayRebindRecoverySnapshot{
			active, active, active, active,
		}}
		inspector := &controllerRecoveryRebindInspectorFake{
			inspection: controllerRecoveryActiveRebindInspection(rebindRequestOperation),
		}
		discovery := NewControllerRecoveryDiscovery(t.TempDir(), repository, inspector)
		operationID, err := discovery.ActiveGatewayRebind(context.Background())
		if err != nil || operationID != rebindRequestOperation || repository.calls != 4 ||
			inspector.calls != 1 || inspector.repository != repository || inspector.operation != rebindRequestOperation {
			t.Fatalf("active operation=%q error=%v snapshot_calls=%d inspector_calls=%d inspector_operation=%q",
				operationID, err, repository.calls, inspector.calls, inspector.operation)
		}
	})

	t.Run("changed presence", func(t *testing.T) {
		first := controllerRecoveryActiveRebindSnapshot(rebindRequestOperation)
		changed := controllerRecoveryActiveRebindSnapshot(rebindProfileOperation)
		repository := &controllerRecoveryPresenceRepository{snapshots: []appaccess.GatewayRebindRecoverySnapshot{
			first, first, changed, changed,
		}}
		inspector := &controllerRecoveryRebindInspectorFake{
			inspection: controllerRecoveryActiveRebindInspection(rebindRequestOperation),
		}
		discovery := NewControllerRecoveryDiscovery(t.TempDir(), repository, inspector)
		if operationID, err := discovery.ActiveGatewayRebind(context.Background()); err == nil || operationID != "" ||
			repository.calls != 4 || inspector.calls != 1 {
			t.Fatalf("changed presence operation=%q error=%v snapshot_calls=%d inspector_calls=%d",
				operationID, err, repository.calls, inspector.calls)
		}
	})
}

func TestControllerRecoveryNormalModeAuthenticationShapeAndAbsence(t *testing.T) {
	discovery := &controllerRecoveryDiscoveryFake{}
	server := &Server{
		Auth:              controllerAuthFake{user: auth.User{ID: rebindRequestRevision, Role: "administrator"}},
		RecoveryDiscovery: discovery, Logger: relayTestLogger(),
	}
	handler := server.Handler()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/system/recovery", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "no-store" || discovery.calls != 0 {
		t.Fatalf("unauthenticated status=%d cache=%q calls=%d", response.Code,
			response.Header().Get("Cache-Control"), discovery.calls)
	}

	server.Auth = controllerAuthFake{user: auth.User{ID: rebindRequestRevision, Role: "viewer"}}
	response = relayAuthenticatedRequest(server.Handler(), http.MethodGet, "/api/v1/system/recovery", "")
	if response.Code != http.StatusForbidden || response.Header().Get("Cache-Control") != "no-store" || discovery.calls != 0 {
		t.Fatalf("viewer status=%d cache=%q calls=%d body=%s", response.Code,
			response.Header().Get("Cache-Control"), discovery.calls, response.Body.String())
	}

	server.Auth = controllerAuthFake{user: auth.User{ID: rebindRequestRevision, Role: "administrator"}}
	for name, mutate := range map[string]func(*http.Request){
		"query":            func(r *http.Request) { r.URL.RawQuery = "kind=gateway_rebind" },
		"empty query mark": func(r *http.Request) { r.URL.ForceQuery = true },
		"body":             func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("{}")) },
		"content encoding": func(r *http.Request) { r.Header.Set("Content-Encoding", "identity") },
	} {
		t.Run(name, func(t *testing.T) {
			request := authenticatedControllerRecoveryRequest()
			mutate(request)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "no-store" || discovery.calls != 0 {
				t.Fatalf("status=%d cache=%q calls=%d body=%s", response.Code,
					response.Header().Get("Cache-Control"), discovery.calls, response.Body.String())
			}
		})
	}

	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authenticatedControllerRecoveryRequest())
	var got apicontract.ControllerRecovery
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
		discovery.calls != 1 || json.Unmarshal(response.Body.Bytes(), &got) != nil ||
		got.Version != 1 || got.Mode != "normal" || got.Recovery != nil {
		t.Fatalf("normal absence status=%d cache=%q calls=%d body=%s", response.Code,
			response.Header().Get("Cache-Control"), discovery.calls, response.Body.String())
	}
}

func TestControllerRecoveryNormalModeDistinguishesActiveUnavailableAndRevokedSession(t *testing.T) {
	discovery := &controllerRecoveryDiscoveryFake{operationID: rebindRequestOperation}
	authentication := &gatewayUpgradeSessionAuth{
		controllerAuthFake: controllerAuthFake{user: auth.User{ID: rebindRequestRevision, Role: "administrator"}},
	}
	server := &Server{Auth: authentication, RecoveryDiscovery: discovery, Logger: relayTestLogger()}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authenticatedControllerRecoveryRequest())
	var got apicontract.ControllerRecovery
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil ||
		got.Recovery == nil || got.Recovery.Kind != RecoveryGatewayRebind ||
		got.Recovery.OperationID != rebindRequestOperation || discovery.calls != 1 {
		t.Fatalf("active status=%d calls=%d body=%s", response.Code, discovery.calls, response.Body.String())
	}

	discovery.err = errors.New("durable read failed")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authenticatedControllerRecoveryRequest())
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"lan_gateway_unavailable"`) {
		t.Fatalf("unavailable status=%d body=%s", response.Code, response.Body.String())
	}

	discovery.err = nil
	discovery.operationID = "not-canonical"
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authenticatedControllerRecoveryRequest())
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("malformed locator status=%d body=%s", response.Code, response.Body.String())
	}

	discovery.operationID = rebindRequestOperation
	authentication.authenticateCalls = 0
	authentication.failAuthAt = 3
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authenticatedControllerRecoveryRequest())
	if response.Code != http.StatusForbidden || discovery.calls != 4 ||
		strings.Contains(response.Body.String(), rebindRequestOperation) {
		t.Fatalf("revoked session status=%d calls=%d body=%s", response.Code, discovery.calls, response.Body.String())
	}
}

func TestControllerRecoveryRecoveryOnlyValidatesPinBeforeEvidenceAndRetainsRebindTerminal(t *testing.T) {
	runtime := &rebindRecoveryHandlerRuntime{inspection: rebindRecoveryTerminalInspection()}
	server := &Server{
		Auth:         controllerAuthFake{user: auth.User{ID: rebindRequestRevision, Role: "administrator"}},
		RecoveryOnly: true, RecoveryKind: RecoveryGatewayRebind, RecoveryOperationID: "not-canonical",
		GatewayRebindRecoveries: &rebindRecoveryHandlerRepository{}, GatewayRebindRecoveryRuntime: runtime,
		Logger: relayTestLogger(),
	}
	response := relayAuthenticatedRequest(server.Handler(), http.MethodGet, "/api/v1/system/recovery", "")
	if response.Code != http.StatusServiceUnavailable || runtime.calls != 0 {
		t.Fatalf("invalid pin status=%d calls=%d body=%s", response.Code, runtime.calls, response.Body.String())
	}

	server.RecoveryOperationID = rebindRequestOperation
	response = relayAuthenticatedRequest(server.Handler(), http.MethodGet, "/api/v1/system/recovery", "")
	var got apicontract.ControllerRecovery
	if response.Code != http.StatusOK || runtime.calls != 1 || json.Unmarshal(response.Body.Bytes(), &got) != nil ||
		got.Mode != "recovery_only" || got.Recovery == nil || got.Recovery.Kind != RecoveryGatewayRebind ||
		got.Recovery.OperationID != rebindRequestOperation {
		t.Fatalf("terminal latch status=%d calls=%d body=%s", response.Code, runtime.calls, response.Body.String())
	}

	runtime.inspection = rebindRecoveryAbsentInspection()
	response = relayAuthenticatedRequest(server.Handler(), http.MethodGet, "/api/v1/system/recovery", "")
	if response.Code != http.StatusServiceUnavailable || runtime.calls != 2 {
		t.Fatalf("absent pinned evidence status=%d calls=%d body=%s", response.Code, runtime.calls, response.Body.String())
	}
}

func TestControllerRecoveryLANUsesExistingHeadAndTerminalFallbackWithoutEffects(t *testing.T) {
	head := validLANRecoveryHeadFixture()
	headService := &lanRecoveryHeadServiceFake{head: head}
	snapshots := &controllerRecoverySnapshotFake{}
	startup := &controllerRecoveryStartupFake{}
	server := lanRecoveryControllerFixture(headService, "administrator")
	server.RecoverySnapshots, server.RecoveryStartupRuntime = snapshots, startup
	response := relayAuthenticatedRequest(server.Handler(), http.MethodGet, "/api/v1/system/recovery", "")
	var got apicontract.ControllerRecovery
	if response.Code != http.StatusOK || headService.calls != 1 || snapshots.calls != 0 ||
		startup.upgradeCalls != 0 || startup.accessCalls != 0 ||
		json.Unmarshal(response.Body.Bytes(), &got) != nil || got.Recovery == nil ||
		got.Recovery.Kind != RecoveryLANGrant || got.Recovery.OperationID != head.OperationID {
		t.Fatalf("active LAN status=%d head=%d snapshots=%d startup=%d/%d body=%s", response.Code,
			headService.calls, snapshots.calls, startup.upgradeCalls, startup.accessCalls, response.Body.String())
	}

	headService.err = errors.New("head advanced after terminal response")
	snapshots.snapshot = controllerRecoveryLANSnapshot(head.OperationID, head.AppID, appaccess.AppAccessGrantCommitted)
	startup.upgrade = generatedingress.GatewayV2StartupInspection{
		Disposition: generatedingress.GatewayV2StartupNormalV2, OperationID: rebindRequestRevision,
	}
	startup.access = generatedingress.GatewayV2LANAccessStartupInspection{
		Disposition: generatedingress.GatewayV2LANStartupNormal,
	}
	response = relayAuthenticatedRequest(server.Handler(), http.MethodGet, "/api/v1/system/recovery", "")
	if response.Code != http.StatusOK || snapshots.calls != 2 || startup.upgradeCalls != 1 || startup.accessCalls != 1 ||
		json.Unmarshal(response.Body.Bytes(), &got) != nil || got.Recovery == nil ||
		got.Recovery.OperationID != head.OperationID {
		t.Fatalf("terminal LAN status=%d snapshots=%d startup=%d/%d body=%s", response.Code,
			snapshots.calls, startup.upgradeCalls, startup.accessCalls, response.Body.String())
	}

	// A completed batch advances its protected head. This process remains
	// latched to the original startup pin and must never publish the next item.
	nextOperation := "66666666-6666-4666-8666-666666666666"
	nextApp := "77777777-7777-4777-8777-777777777777"
	startup.access = generatedingress.GatewayV2LANAccessStartupInspection{
		Disposition:  generatedingress.GatewayV2LANStartupRecoveryOnly,
		RecoveryKind: RecoveryLANDisable, OperationID: nextOperation, AppID: nextApp,
	}
	response = relayAuthenticatedRequest(server.Handler(), http.MethodGet, "/api/v1/system/recovery", "")
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil ||
		got.Recovery == nil || got.Recovery.OperationID != head.OperationID ||
		strings.Contains(response.Body.String(), nextOperation) {
		t.Fatalf("advanced head replaced startup pin: status=%d body=%s", response.Code, response.Body.String())
	}

	snapshots.calls = 0
	startup.access = generatedingress.GatewayV2LANAccessStartupInspection{
		Disposition: generatedingress.GatewayV2LANStartupRecoveryOnly,
		Recoveries: []generatedingress.GatewayV2LANAccessStartupRecovery{{
			Kind: RecoveryLANGrant, OperationID: "changed", AppID: head.AppID,
		}},
	}
	response = relayAuthenticatedRequest(server.Handler(), http.MethodGet, "/api/v1/system/recovery", "")
	if response.Code != http.StatusServiceUnavailable || snapshots.calls != 1 {
		t.Fatalf("corrupt LAN status=%d snapshots=%d body=%s", response.Code, snapshots.calls, response.Body.String())
	}

	startup.access = generatedingress.GatewayV2LANAccessStartupInspection{
		Disposition: generatedingress.GatewayV2LANStartupNormal,
	}
	snapshots.calls = 0
	snapshots.snapshots = []appaccess.HostingGatewayStartupSnapshot{
		controllerRecoveryLANSnapshot(head.OperationID, head.AppID, appaccess.AppAccessGrantCommitted),
		controllerRecoveryLANSnapshot(head.OperationID, head.AppID, appaccess.AppAccessGrantPrepared),
	}
	response = relayAuthenticatedRequest(server.Handler(), http.MethodGet, "/api/v1/system/recovery", "")
	if response.Code != http.StatusServiceUnavailable || snapshots.calls != 2 {
		t.Fatalf("changed snapshot status=%d snapshots=%d body=%s", response.Code, snapshots.calls, response.Body.String())
	}
}

func TestControllerRecoveryUpgradeTerminalLatchUsesStrongStartupAndExactObservation(t *testing.T) {
	snapshots := &controllerRecoverySnapshotFake{snapshot: appaccess.HostingGatewayStartupSnapshot{
		Upgrades: appaccess.GatewayUpgradeStartupSnapshot{Claims: []appaccess.GatewayUpgradeStartupClaim{{
			Claim: appaccess.GatewayProfileUpgradeClaim{
				OperationID: rebindRequestOperation, State: appaccess.GatewayProfileUpgradeRolledBack,
			},
		}}},
	}}
	startup := &controllerRecoveryStartupFake{upgrade: generatedingress.GatewayV2StartupInspection{
		Disposition: generatedingress.GatewayV2StartupNormalV1,
	}}
	runtime := &gatewayUpgradeRuntimeFake{observed: generatedingress.GatewayV2OperationStatus{
		OperationID: rebindRequestOperation, Availability: generatedingress.GatewayV2OperationUnavailable,
	}}
	server := &Server{
		Auth:         controllerAuthFake{user: auth.User{ID: rebindRequestRevision, Role: "administrator"}},
		RecoveryOnly: true, RecoveryKind: RecoveryGatewayUpgrade, RecoveryOperationID: rebindRequestOperation,
		RecoverySnapshots: snapshots, RecoveryStartupRuntime: startup, GatewayUpgradeRuntime: runtime,
		Logger: relayTestLogger(),
	}
	response := relayAuthenticatedRequest(server.Handler(), http.MethodGet, "/api/v1/system/recovery", "")
	var got apicontract.ControllerRecovery
	if response.Code != http.StatusOK || snapshots.calls != 2 || startup.upgradeCalls != 1 ||
		startup.accessCalls != 0 || runtime.observeCalls != 1 || runtime.upgradeCalls != 0 || runtime.mutationCalls != 0 ||
		json.Unmarshal(response.Body.Bytes(), &got) != nil || got.Recovery == nil ||
		got.Recovery.Kind != RecoveryGatewayUpgrade || got.Recovery.OperationID != rebindRequestOperation {
		t.Fatalf("upgrade latch status=%d snapshots=%d startup=%d observe=%d mutation=%d body=%s", response.Code,
			snapshots.calls, startup.upgradeCalls, runtime.observeCalls, runtime.mutationCalls, response.Body.String())
	}
}

func authenticatedControllerRecoveryRequest() *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system/recovery", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "session"})
	return request
}

func controllerRecoveryActiveRebindSnapshot(operationID string) appaccess.GatewayRebindRecoverySnapshot {
	entry := appaccess.GatewayRebindHistoryEntry{Claim: appaccess.GatewayRebindClaimRecord{
		SpecVersion: 1,
		Legacy: &appaccess.GatewayRebindClaim{Spec: appaccess.GatewayRebindSpec{
			OperationID: operationID,
		}},
	}}
	profile := appaccess.GatewayProfileRevision{}
	source := appaccess.GatewayCurrentAuthorityRef{}
	return appaccess.GatewayRebindRecoverySnapshot{
		History: []appaccess.GatewayRebindHistoryEntry{entry}, Active: &entry,
		CurrentProfile: &profile, CurrentSource: &source, Phase: appaccess.GatewayRebindPrepared,
	}
}

func controllerRecoveryActiveRebindInspection(operationID string) generatedingress.GatewayRebindOperationInspection {
	return generatedingress.GatewayRebindOperationInspection{
		Version:     generatedingress.GatewayRebindOperationInspectionVersion,
		OperationID: operationID, Availability: generatedingress.GatewayRebindOperationAvailable,
		Operation: generatedingress.GatewayRebindOperationStatus{
			OperationID: operationID, StoredState: appaccess.GatewayRebindPrepared,
			StateSequence: 1, TerminalDisposition: appaccess.GatewayRebindDispositionNone,
		},
		Active: true, TerminalProof: generatedingress.GatewayRebindProofUnknown,
		CurrentProof:   generatedingress.GatewayRebindProofUnknown,
		OperationFence: generatedingress.GatewayRebindOperationFenceHeld,
	}
}

func controllerRecoveryLANSnapshot(operationID, appID string,
	state appaccess.AppAccessGrantState,
) appaccess.HostingGatewayStartupSnapshot {
	return appaccess.HostingGatewayStartupSnapshot{
		Grants: appaccess.AppAccessGrantStartupSnapshot{Claims: []appaccess.AppAccessGrantStartupClaim{{
			Claim: appaccess.AppAccessGrantClaim{
				AttemptID: operationID, State: state,
				Spec: appaccess.AppAccessGrantSpec{AppID: appID},
			},
		}}},
	}
}
