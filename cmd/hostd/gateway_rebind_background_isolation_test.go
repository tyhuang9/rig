//go:build live_docker

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/autodeploy"
	"github.com/hostd/hostd/internal/controllerrelay"
	"github.com/hostd/hostd/internal/database"
)

const (
	liveHostdRebindBackgroundScenario = "recovery-background-isolation"
)

type liveHostdRebindBackgroundChild struct {
	args    []string
	observe hostdBackgroundStartObserver
	verify  func() string
}

type liveHostdRebindBackgroundSentinel struct {
	autoApplicationID string
	autoLeaseToken    string
	autoLeaseExpires  string
	autoLeaseFence    uint64
	relayControllerID string
	relayEpoch        uint64
	relayFence        uint64
}

type liveHostdRebindBackgroundReadback struct {
	autoStatus  autodeploy.Status
	autoToken   string
	autoExpiry  string
	relay       controllerrelay.SessionStatus
	keys        int
	credentials int
	issues      int
}

func liveHostdRebindSeedBackgroundSentinels(t *testing.T,
	fixture *liveHostdGatewayRebindFixture,
) liveHostdRebindBackgroundSentinel {
	t.Helper()
	const (
		connectionID   = "recovery-background-source"
		installationID = int64(101)
		repositoryID   = int64(202)
		relayEpoch     = uint64(7)
		relayFence     = uint64(1)
	)
	seedAt := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC)
	stamp := seedAt.Format(time.RFC3339Nano)
	db, err := database.Open(fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()
	controllerID, bindingID, applicationID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, execErr := db.Exec(query, args...); execErr != nil {
			t.Fatal(execErr)
		}
	}
	mustExec(`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at)
		VALUES(?,?,'github','connected','42','fixture',1,?,?,?,?,?)`, connectionID, fixture.user.ID,
		seedAt.Add(time.Hour).Format(time.RFC3339Nano), seedAt.Add(24*time.Hour).Format(time.RFC3339Nano), stamp, stamp, stamp)
	mustExec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, controllerID, stamp, stamp)
	mustExec(`INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,'authorized',?,?,?)`, bindingID, fixture.user.ID, connectionID, controllerID, installationID, repositoryID, stamp, stamp, stamp)
	mustExec(`INSERT INTO relay_controller_session_state(controller_id,epoch,fence,state,key_id,last_error_code,attempt,next_attempt_at,last_ready_at,last_seen_at,state_changed_at,updated_at)
		VALUES(?,?,?,'disconnected',NULL,NULL,0,NULL,NULL,NULL,?,?)`, controllerID, relayEpoch, relayFence, stamp, stamp)
	mustExec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, applicationID, applicationID, "Recovery background sentinel", stamp, stamp)
	mustExec(`INSERT INTO application_sources(application_id,source_type,connection_id,installation_id,repository_id,repository_owner,repository_name,tracked_branch,tracked_ref,compose_path,resolved_sha,created_at,updated_at)
		VALUES(?,'github',?,?,?,'fixture','hosting-notes','main','refs/heads/main','compose.yaml','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,?)`, applicationID, connectionID, installationID, repositoryID, stamp, stamp)
	repository := autodeploy.NewRepository(db)
	status, err := repository.Configure(context.Background(), autodeploy.ConfigureRequest{
		ApplicationID: applicationID, ActorUserID: fixture.user.ID, ExpectedRevision: 0, Enabled: true,
	}, seedAt)
	if err != nil || !status.Enabled || status.ControllerID != controllerID || status.BindingID != bindingID {
		t.Fatalf("seed enabled auto-deploy sentinel: status=%#v error=%v", status, err)
	}
	_, lease, err := repository.ClaimDue(context.Background(), uuid.NewString(), seedAt, time.Second)
	if err != nil || lease.ApplicationID != applicationID || !lease.ExpiresAt.Before(time.Now().UTC()) {
		t.Fatalf("seed fixed-past auto-deploy lease: lease=%#v error=%v", lease, err)
	}
	return liveHostdRebindBackgroundSentinel{
		autoApplicationID: applicationID, autoLeaseToken: lease.Token,
		autoLeaseExpires: lease.ExpiresAt.Format(time.RFC3339Nano), autoLeaseFence: lease.Fence,
		relayControllerID: controllerID, relayEpoch: relayEpoch, relayFence: relayFence,
	}
}

func liveHostdRebindReadBackgroundSentinel(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	sentinel liveHostdRebindBackgroundSentinel,
) liveHostdRebindBackgroundReadback {
	t.Helper()
	db, err := database.Open(fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()
	autoStatus, err := autodeploy.NewRepository(db).Get(context.Background(), sentinel.autoApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	result := liveHostdRebindBackgroundReadback{autoStatus: autoStatus}
	if err = db.QueryRow(`SELECT COALESCE(lease_token,''),COALESCE(lease_expires_at,'') FROM github_auto_deploy_heads WHERE application_id=?`, sentinel.autoApplicationID).Scan(&result.autoToken, &result.autoExpiry); err != nil {
		t.Fatal(err)
	}
	result.relay, err = controllerrelay.NewRepository(db).SessionStatus(context.Background(), sentinel.relayControllerID)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM relay_controller_keys WHERE controller_id=? AND state IN ('active','pending')`, sentinel.relayControllerID).Scan(&result.keys); err != nil {
		t.Fatal(err)
	}
	credentials, err := controllerrelay.NewFileCredentialStore(fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	page, err := credentials.ControllerKeyCredentials("", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Complete {
		t.Fatal("relay credential inventory did not complete")
	}
	result.credentials, result.issues = len(page.Credentials), len(page.Issues)
	return result
}

func liveHostdRebindAssertBackgroundSentinelUnchanged(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	sentinel liveHostdRebindBackgroundSentinel,
) {
	t.Helper()
	got := liveHostdRebindReadBackgroundSentinel(t, fixture, sentinel)
	if !got.autoStatus.Enabled || got.autoStatus.LeaseFence != sentinel.autoLeaseFence ||
		got.autoToken != sentinel.autoLeaseToken || got.autoExpiry != sentinel.autoLeaseExpires ||
		got.relay.Epoch != sentinel.relayEpoch || got.relay.Fence != sentinel.relayFence ||
		got.relay.State != controllerrelay.SessionDisconnected || got.keys != 0 || got.credentials != 0 || got.issues != 0 {
		t.Fatalf("recovery process changed background sentinels: %#v", got)
	}
}

func liveHostdRebindBackgroundReadbackAdvanced(got liveHostdRebindBackgroundReadback,
	sentinel liveHostdRebindBackgroundSentinel,
) bool {
	return got.autoStatus.Enabled && got.autoToken == "" && got.autoExpiry == "" &&
		got.relay.Epoch == sentinel.relayEpoch+1 && got.relay.Fence == sentinel.relayFence+1 &&
		got.relay.State == controllerrelay.SessionNeedsAttention && got.relay.ErrorCode == controllerrelay.ErrorKeyRevoked &&
		got.relay.KeyID == "" && got.relay.Attempt == 0 && got.relay.NextAttemptAt == nil &&
		got.keys == 0 && got.credentials == 0 && got.issues == 0
}

// liveHostdRebindBackgroundChildControls is child-only test wiring. It does
// not enter the production command line or change authority: the observer can
// record only one of the finite startup component identifiers, while the
// network guard rejects any accidental use of the GitHub default transport.
func liveHostdRebindBackgroundChildControls(manifest liveHostdRebindChildManifest,
	args []string,
) (liveHostdRebindBackgroundChild, func(), error) {
	result := liveHostdRebindBackgroundChild{args: append([]string(nil), args...), verify: func() string { return "" }}
	if manifest.ScenarioID != liveHostdRebindBackgroundScenario {
		return result, func() {}, nil
	}
	starts := &liveHostdRebindBoundedLedger{path: manifest.BackgroundLedger, limit: liveHostdRebindBackgroundLedgerLimit}
	denied := &liveHostdRebindBoundedLedger{path: manifest.NetworkLedger, limit: liveHostdRebindBackgroundLedgerLimit}
	relayAccepts := &liveHostdRebindBoundedLedger{path: manifest.RelayLedger, limit: liveHostdRebindBackgroundLedgerLimit}
	origin, closeOrigin, err := liveHostdRebindBackgroundLoopbackHTTPSOrigin(relayAccepts)
	if err != nil {
		return liveHostdRebindBackgroundChild{}, func() {}, err
	}
	http.DefaultTransport = liveHostdRebindDenyTransport{ledger: denied}
	result.args = []string{
		"--data-root", manifest.DataRoot, "--listen", manifest.Listen,
		"--generated-runtime", "--github-connections=true",
		"--github-client-id", "fixture-public-client-id", "--github-app-slug", "fixture-relay",
		"--controller-relay", "--relay-origin", origin,
	}
	result.observe = func(component hostdBackgroundComponent) {
		name, ok := liveHostdRebindBackgroundComponentName(component)
		if !ok {
			panic("rebind background child observed an unknown component")
		}
		if err := starts.append(name); err != nil {
			panic("rebind background child could not record component start")
		}
	}
	result.verify = func() string {
		if starts.err() != nil {
			return fmt.Sprintf("check=background_start_ledger count=%d", starts.eventCount())
		}
		if denied.err() != nil {
			return fmt.Sprintf("check=network_ledger count=%d", denied.eventCount())
		}
		if relayAccepts.err() != nil {
			return fmt.Sprintf("check=relay_ledger count=%d", relayAccepts.eventCount())
		}
		return ""
	}
	return result, closeOrigin, nil
}

func liveHostdRebindBackgroundComponentName(component hostdBackgroundComponent) (string, bool) {
	switch component {
	case hostdBackgroundAutoDeploy:
		return "auto_deploy", true
	case hostdBackgroundControllerRelay:
		return "controller_relay", true
	default:
		return "", false
	}
}

func liveHostdRebindBackgroundLoopbackHTTPSOrigin(relayAccepts *liveHostdRebindBoundedLedger) (string, func(), error) {
	if relayAccepts == nil {
		return "", func() {}, errors.New("missing relay accept ledger")
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state != http.StateNew {
			return
		}
		if err := relayAccepts.append("relay_accept"); err != nil {
			panic("rebind background child could not record relay accept")
		}
	}
	host, _, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil || host != "127.0.0.1" {
		server.Close()
		return "", func() {}, errors.New("non-loopback TLS listener")
	}
	server.StartTLS()
	parsed, err := url.Parse(server.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Path != "" || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.User != nil {
		server.Close()
		return "", func() {}, errors.New("invalid loopback TLS origin")
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || host != "127.0.0.1" || port == "" {
		server.Close()
		return "", func() {}, errors.New("non-loopback TLS origin")
	}
	return server.URL, server.Close, nil
}

func TestLiveGatewayRebindBackgroundLoopbackRejectedTLSRecordsAccept(t *testing.T) {
	ledger := &liveHostdRebindBoundedLedger{
		path: t.TempDir() + "/relay-accepts.log", limit: liveHostdRebindBackgroundLedgerLimit,
	}
	origin, closeOrigin, err := liveHostdRebindBackgroundLoopbackHTTPSOrigin(ledger)
	if err != nil {
		t.Fatal(err)
	}
	defer closeOrigin()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, requestErr := client.Get(origin)
	if response != nil {
		_ = response.Body.Close()
	}
	if requestErr == nil {
		t.Fatal("loopback TLS client accepted the test certificate")
	}
	events, readErr := liveHostdRebindReadBackgroundLedger(ledger.path)
	if readErr != nil || strings.Join(events, ",") != "relay_accept" {
		t.Fatalf("rejected TLS accept ledger events=%v error=%v", events, readErr)
	}
}

func liveHostdRebindAssertBackgroundLedgers(t *testing.T, child *liveHostdRebindChild,
	wantStarts []string,
) {
	t.Helper()
	starts, err := liveHostdRebindReadBackgroundLedger(child.manifest.BackgroundLedger)
	if err != nil || strings.Join(starts, ",") != strings.Join(wantStarts, ",") {
		t.Fatalf("background start ledger: starts=%v want=%v error=%v", starts, wantStarts, err)
	}
	network, err := liveHostdRebindReadBackgroundLedger(child.manifest.NetworkLedger)
	if err != nil || len(network) != 0 {
		t.Fatalf("background GitHub default-transport ledger: events=%v error=%v", network, err)
	}
	relayAccepts, err := liveHostdRebindReadBackgroundLedger(child.manifest.RelayLedger)
	if err != nil || len(relayAccepts) != 0 {
		t.Fatalf("background relay accept ledger: events=%v error=%v", relayAccepts, err)
	}
}

func liveHostdRebindWaitBackgroundSentinelAdvanced(t *testing.T,
	fixture *liveHostdGatewayRebindFixture, child *liveHostdRebindChild,
	sentinel liveHostdRebindBackgroundSentinel,
) {
	t.Helper()
	deadline, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last liveHostdRebindBackgroundReadback
	for {
		liveHostdRebindAssertBackgroundLedgers(t, child, []string{"auto_deploy", "controller_relay"})
		last = liveHostdRebindReadBackgroundSentinel(t, fixture, sentinel)
		if liveHostdRebindBackgroundReadbackAdvanced(last, sentinel) {
			return
		}
		child.startWait()
		select {
		case <-child.waitDone:
			t.Fatalf("normal background child exited before durable recovery: %v stderr=%q", child.waitErr,
				liveHostdRebindClosedDiagnostic(child.stderr.String()))
		case <-ticker.C:
		case <-deadline.Done():
			t.Fatalf("normal background durable recovery exceeded 30s: %s", liveHostdRebindBackgroundReadbackDiagnostic(last))
		}
	}
}

func liveHostdRebindBackgroundReadbackDiagnostic(value liveHostdRebindBackgroundReadback) string {
	return fmt.Sprintf("auto_enabled=%t auto_lease_present=%t auto_expiry_present=%t auto_fence=%d relay_epoch=%d relay_fence=%d relay_state=%s keys=%d credentials=%d credential_issues=%d",
		value.autoStatus.Enabled, value.autoToken != "", value.autoExpiry != "", value.autoStatus.LeaseFence,
		value.relay.Epoch, value.relay.Fence, value.relay.State, value.keys, value.credentials, value.issues)
}

func liveHostdRebindRunRecoveryBackgroundIsolation(t *testing.T) {
	t.Helper()
	var sentinel liveHostdRebindBackgroundSentinel
	fixture, operationID := liveHostdRebindPrepareLiteralUnresolvedWithSetup(t, false,
		func(value *liveHostdGatewayRebindFixture) {
			sentinel = liveHostdRebindSeedBackgroundSentinels(t, value)
		})
	recovery := liveHostdRebindStartChild(t, fixture, "", "", liveHostdRebindBackgroundScenario)
	ready := liveHostdRebindWaitReady(t, fixture, recovery)
	if !ready.RecoveryOnly || ready.RecoveryKind != "gateway_rebind" || ready.OperationID != operationID {
		t.Fatal("background isolation fixture did not enter exact recovery-only startup")
	}
	liveHostdRebindAssertBackgroundLedgers(t, recovery, nil)
	liveHostdRebindAssertBackgroundSentinelUnchanged(t, fixture, sentinel)
	for _, path := range []string{
		"/api/v1/system/lan-gateway-rebind/" + operationID,
		"/api/v1/system/status",
	} {
		status, _, err := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodGet, path, nil)
		if err != nil || (status != http.StatusOK && status != http.StatusServiceUnavailable) {
			t.Fatalf("background recovery authenticated probe %s: status=%d error=%v", path, status, err)
		}
	}
	liveHostdRebindAssertBackgroundLedgers(t, recovery, nil)
	liveHostdRebindAssertBackgroundSentinelUnchanged(t, fixture, sentinel)
	status := liveHostdRebindRecover(t, fixture, operationID, ready)
	liveHostdRebindAssertTerminal(t, status, operationID)
	liveHostdRebindAssertBackgroundLedgers(t, recovery, nil)
	liveHostdRebindAssertBackgroundSentinelUnchanged(t, fixture, sentinel)
	liveHostdRebindKillAndReap(t, recovery)
	liveHostdRebindAssertBackgroundLedgers(t, recovery, nil)
	liveHostdRebindAssertBackgroundSentinelUnchanged(t, fixture, sentinel)

	normal := liveHostdRebindStartChild(t, fixture, "", "", liveHostdRebindBackgroundScenario)
	if normal.manifest.BackgroundLedger == recovery.manifest.BackgroundLedger ||
		normal.manifest.NetworkLedger == recovery.manifest.NetworkLedger ||
		normal.manifest.RelayLedger == recovery.manifest.RelayLedger {
		t.Fatal("background process ledgers do not have distinct child identities")
	}
	normalReady := liveHostdRebindWaitReady(t, fixture, normal)
	if normalReady.RecoveryOnly || normalReady.RecoveryKind != "" || normalReady.OperationID != "" {
		t.Fatal("fresh background fixture did not return to normal startup")
	}
	liveHostdRebindWaitBackgroundSentinelAdvanced(t, fixture, normal, sentinel)
	liveHostdRebindKillAndReap(t, normal)
	liveHostdRebindAssertBackgroundLedgers(t, normal, []string{"auto_deploy", "controller_relay"})
	if final := liveHostdRebindReadBackgroundSentinel(t, fixture, sentinel); !liveHostdRebindBackgroundReadbackAdvanced(final, sentinel) {
		t.Fatalf("normal background durable recovery changed after ready: %s", liveHostdRebindBackgroundReadbackDiagnostic(final))
	}
}
