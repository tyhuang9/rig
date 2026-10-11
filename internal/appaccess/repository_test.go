package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/testsupport/databasefixture"
)

const (
	testAdministrator = "11111111-1111-4111-8111-111111111111"
	testViewer        = "22222222-2222-4222-8222-222222222222"
)

var testNow = time.Date(2026, 9, 29, 15, 4, 5, 123456789, time.UTC)

func TestGatewayProfileApprovalCASReplayAndHostAddressBoundary(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	valid := GatewayProfileSpec{SelectedIPv4: "192.168.1.20", InterfaceID: "windows-adapter-guid", PortStart: 8104, PortEnd: 8110}

	for _, invalid := range []GatewayProfileSpec{
		{SelectedIPv4: "127.0.0.1", InterfaceID: "loopback", PortStart: 8100, PortEnd: 8119},
		{SelectedIPv4: "0.0.0.0", InterfaceID: "wildcard", PortStart: 8100, PortEnd: 8119},
		{SelectedIPv4: "169.254.1.2", InterfaceID: "link-local", PortStart: 8100, PortEnd: 8119},
		{SelectedIPv4: "224.0.0.1", InterfaceID: "multicast", PortStart: 8100, PortEnd: 8119},
		{SelectedIPv4: "255.255.255.255", InterfaceID: "broadcast", PortStart: 8100, PortEnd: 8119},
		{SelectedIPv4: "8.8.8.8", InterfaceID: "public-address", PortStart: 8100, PortEnd: 8119},
		{SelectedIPv4: "192.168.1.20", InterfaceID: "bad-pool", PortStart: 8099, PortEnd: 8119},
		{SelectedIPv4: "192.168.1.20", InterfaceID: "bad-pool", PortStart: 8110, PortEnd: 8109},
	} {
		_, _, err := repository.ConfigureGatewayProfile(context.Background(), ConfigureGatewayInput{OperationID: uuid.NewString(), Spec: invalid})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid profile %#v error = %v", invalid, err)
		}
	}

	digest := mustGatewayDigest(t, valid)
	operationID := uuid.NewString()
	input := ConfigureGatewayInput{
		OperationID: operationID,
		Spec:        valid,
		Approval:    Approval{Action: ActionConfigureGateway, SpecDigest: digest, ActorID: testAdministrator},
	}
	badApproval := input
	badApproval.OperationID = uuid.NewString()
	badApproval.Approval.SpecDigest = string(make([]byte, 64))
	if _, _, err := repository.ConfigureGatewayProfile(context.Background(), badApproval); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("changed digest approval error = %v", err)
	}
	wrongAction := input
	wrongAction.OperationID = uuid.NewString()
	wrongAction.Approval.Action = ActionEnableAppAccess
	if _, _, err := repository.ConfigureGatewayProfile(context.Background(), wrongAction); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("wrong gateway action error = %v", err)
	}
	viewerApproval := input
	viewerApproval.OperationID = uuid.NewString()
	viewerApproval.Approval.ActorID = testViewer
	if _, _, err := repository.ConfigureGatewayProfile(context.Background(), viewerApproval); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("non-administrator approval error = %v", err)
	}

	first, created, err := repository.ConfigureGatewayProfile(context.Background(), input)
	if err != nil || !created || first.RevisionNumber != 1 || first.Spec != valid || first.SpecDigest != digest {
		t.Fatalf("first profile = %#v created=%t error=%v", first, created, err)
	}
	replayed, created, err := New(db).ConfigureGatewayProfile(context.Background(), input)
	if err != nil || created || replayed.ID != first.ID || replayed.ApprovedAt != first.ApprovedAt {
		t.Fatalf("profile replay = %#v created=%t error=%v", replayed, created, err)
	}
	changed := input
	changed.Spec.InterfaceID = "other-adapter"
	changed.Approval.SpecDigest = mustGatewayDigest(t, changed.Spec)
	if _, _, err := repository.ConfigureGatewayProfile(context.Background(), changed); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("changed idempotency payload error = %v", err)
	}
	stale := input
	stale.OperationID = uuid.NewString()
	if _, _, err := repository.ConfigureGatewayProfile(context.Background(), stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale profile error = %v", err)
	}

	secondSpec := GatewayProfileSpec{SelectedIPv4: "10.0.0.8", InterfaceID: "ethernet-2", PortStart: 8100, PortEnd: 8119}
	secondInput := approvedGatewayInput(t, secondSpec, 1)
	second, created, err := repository.ConfigureGatewayProfile(context.Background(), secondInput)
	if err != nil || !created || second.RevisionNumber != 2 {
		t.Fatalf("second profile = %#v created=%t error=%v", second, created, err)
	}
	current, err := repository.CurrentGatewayProfile(context.Background())
	if err != nil || current.ID != second.ID {
		t.Fatalf("current profile = %#v error=%v", current, err)
	}

	// Defense in depth: direct writes cannot bypass the administrator trigger.
	_, err = db.Exec(`INSERT INTO lan_gateway_profile_revisions(
		id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,interface_id,port_start,port_end,spec_digest,approved_by,approved_at
	) VALUES(?,3,?,?,?,?,?,?,?,?,?,?)`, uuid.NewString(), uuid.NewString(), digest, ActionConfigureGateway, valid.SelectedIPv4, valid.InterfaceID, valid.PortStart, valid.PortEnd, digest, testViewer, formatTime(testNow))
	if err == nil {
		t.Fatal("direct non-administrator profile insert succeeded")
	}
	if _, err := db.Exec(`UPDATE lan_gateway_profile_revisions SET interface_id='changed' WHERE id=?`, first.ID); err == nil {
		t.Fatal("gateway profile history was mutable")
	}
}

func TestReservationApprovalTransitionsCleanupAndProfilePin(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t, GatewayProfileSpec{SelectedIPv4: "192.168.50.4", InterfaceID: "adapter-50", PortStart: 8100, PortEnd: 8102}, 0))
	if err != nil {
		t.Fatal(err)
	}
	apps := addApps(t, db, 5)

	firstInput := ReserveAppAccessInput{AppID: apps[0], OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber}
	first, created, err := repository.ReserveAppAccess(context.Background(), firstInput)
	if err != nil || !created || first.Port != 8100 || first.State != AllocationReserved {
		t.Fatalf("first reservation = %#v created=%t error=%v", first, created, err)
	}
	assertArchiveRejected(t, db, first.AppID, "reserved")
	if _, err := db.Exec(`UPDATE lan_port_allocations SET id=? WHERE id=?`, uuid.NewString(), first.ID); err == nil {
		t.Fatal("allocation identity was mutable")
	}
	replayed, created, err := New(db).ReserveAppAccess(context.Background(), firstInput)
	if err != nil || created || replayed.ID != first.ID || replayed.Port != first.Port {
		t.Fatalf("reservation replay = %#v created=%t error=%v", replayed, created, err)
	}
	changedReservation := firstInput
	changedReservation.ExpectedRevisionNumber = 1
	if _, _, err := repository.ReserveAppAccess(context.Background(), changedReservation); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("changed reservation replay error = %v", err)
	}

	approval := approvedAccessInput(t, first, 0)
	viewerApproval := approval
	viewerApproval.Approval.ActorID = testViewer
	if _, _, err := repository.ApproveAppAccess(context.Background(), viewerApproval); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("viewer app approval error = %v", err)
	}
	wrongActionApproval := approval
	wrongActionApproval.Approval.Action = ActionConfigureGateway
	if _, _, err := repository.ApproveAppAccess(context.Background(), wrongActionApproval); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("wrong app access action error = %v", err)
	}
	revision, created, err := repository.ApproveAppAccess(context.Background(), approval)
	if err != nil || !created || revision.RevisionNumber != 1 || revision.Allocation.OwnerRevisionID != revision.ID || revision.Allocation.State != AllocationReserved {
		t.Fatalf("access approval = %#v created=%t error=%v", revision, created, err)
	}
	assertArchiveRejected(t, db, first.AppID, "approved reservation")
	replayedRevision, created, err := New(db).ApproveAppAccess(context.Background(), approval)
	if err != nil || created || replayedRevision.ID != revision.ID {
		t.Fatalf("access approval replay = %#v created=%t error=%v", replayedRevision, created, err)
	}
	changedApproval := approval
	changedApproval.Approval.ActorID = testViewer
	if _, _, err := repository.ApproveAppAccess(context.Background(), changedApproval); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("changed approval replay error = %v", err)
	}
	if _, _, err := repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{AppID: apps[0], OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale app head reservation error = %v", err)
	}
	if _, err := repository.CleanupReserved(context.Background(), AllocationOwner{AllocationID: first.ID, AppID: first.AppID, OperationID: first.OwnerOperationID}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("approved reservation cleanup error = %v", err)
	}
	owner := AllocationOwner{AllocationID: first.ID, AppID: first.AppID, OperationID: first.OwnerOperationID, AccessRevisionID: revision.ID}
	wrongOwner := owner
	wrongOwner.OperationID = uuid.NewString()
	if _, err := repository.Activate(context.Background(), wrongOwner); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong activation owner error = %v", err)
	}
	active, err := repository.Activate(context.Background(), owner)
	if err != nil || active.State != AllocationActive {
		t.Fatalf("active allocation = %#v error=%v", active, err)
	}
	assertArchiveRejected(t, db, first.AppID, "active")
	uncertain, err := repository.MarkUncertain(context.Background(), owner)
	if err != nil || uncertain.State != AllocationUncertain {
		t.Fatalf("uncertain allocation = %#v error=%v", uncertain, err)
	}
	assertArchiveRejected(t, db, first.AppID, "uncertain")
	reconciled, err := repository.Activate(context.Background(), owner)
	if err != nil || reconciled.State != AllocationActive {
		t.Fatalf("reconciled allocation = %#v error=%v", reconciled, err)
	}
	if _, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t, GatewayProfileSpec{SelectedIPv4: "192.168.50.5", InterfaceID: "adapter-51", PortStart: 8100, PortEnd: 8102}, 1)); !errors.Is(err, ErrConflict) {
		t.Fatalf("profile replacement with live allocation error = %v", err)
	}

	second, _, err := repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{AppID: apps[1], OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: 1})
	if err != nil || second.Port != 8101 {
		t.Fatalf("second reservation = %#v error=%v", second, err)
	}
	secondRevision, _, err := repository.ApproveAppAccess(context.Background(), approvedAccessInput(t, second, 0))
	if err != nil {
		t.Fatal(err)
	}
	secondOwner := AllocationOwner{AllocationID: second.ID, AppID: second.AppID, OperationID: second.OwnerOperationID, AccessRevisionID: secondRevision.ID}
	if _, err := repository.MarkUncertain(context.Background(), secondOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CleanupReserved(context.Background(), AllocationOwner{AllocationID: second.ID, AppID: second.AppID, OperationID: second.OwnerOperationID}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("uncertain cleanup error = %v", err)
	}

	failed, _, err := repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{AppID: apps[2], OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: 1})
	if err != nil || failed.Port != 8102 {
		t.Fatalf("failed-operation reservation = %#v error=%v", failed, err)
	}
	bad := approvedAccessInput(t, failed, 0)
	bad.Approval.SpecDigest = fmt.Sprintf("%064d", 0)
	if _, _, err := repository.ApproveAppAccess(context.Background(), bad); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("bad exact approval error = %v", err)
	}
	failedSpecDigest, err := AppAccessSpecDigest(AppAccessSpecFor(failed))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO lan_app_access_revisions(
		id,app_id,revision_number,operation_id,request_digest,approval_action,allocation_id,allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,spec_digest,approved_by,approved_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, uuid.NewString(), failed.AppID, 1, failed.OwnerOperationID, fmt.Sprintf("%064d", 1), ActionEnableAppAccess, failed.ID, failed.Port, failed.GatewayProfileRevisionID, failed.GatewayProfileRevisionNumber, failedSpecDigest, testViewer, formatTime(testNow)); err == nil {
		t.Fatal("direct non-administrator app access revision insert succeeded")
	}
	if _, _, err := repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{AppID: apps[3], OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: 1}); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("pool with active, uncertain, and failed reservation error = %v", err)
	}
	wrongCleanup := AllocationOwner{AllocationID: failed.ID, AppID: failed.AppID, OperationID: uuid.NewString()}
	if _, err := repository.CleanupReserved(context.Background(), wrongCleanup); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong cleanup owner error = %v", err)
	}
	cleaned, err := repository.CleanupReserved(context.Background(), AllocationOwner{AllocationID: failed.ID, AppID: failed.AppID, OperationID: failed.OwnerOperationID})
	if err != nil || !cleaned {
		t.Fatalf("own failed cleanup changed=%t error=%v", cleaned, err)
	}
	historical, created, err := repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{AppID: apps[2], OperationID: failed.OwnerOperationID, ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: 1})
	if err != nil || created || historical.ID != failed.ID || historical.ReleasedAt == nil {
		t.Fatalf("released reservation replay = %#v created=%t error=%v", historical, created, err)
	}
	if _, err := db.Exec(`UPDATE applications SET archived_at=? WHERE id=?`, formatTime(testNow), failed.AppID); err != nil {
		t.Fatalf("archive after reservation release: %v", err)
	}
	if _, _, err := repository.ApproveAppAccess(context.Background(), approvedAccessInput(t, failed, 0)); !errors.Is(err, ErrConflict) {
		t.Fatalf("archived app approval error = %v", err)
	}
	reused, _, err := repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{AppID: apps[3], OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: 1})
	if err != nil || reused.Port != failed.Port {
		t.Fatalf("proved-clean port reuse = %#v error=%v", reused, err)
	}
	var retainedFailed, retainedUncertain int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_port_allocations WHERE id=? AND released_at IS NOT NULL`, failed.ID).Scan(&retainedFailed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_port_allocations WHERE id=? AND state='uncertain' AND released_at IS NULL`, second.ID).Scan(&retainedUncertain); err != nil {
		t.Fatal(err)
	}
	if retainedFailed != 1 || retainedUncertain != 1 {
		t.Fatalf("retained failed=%d uncertain=%d", retainedFailed, retainedUncertain)
	}
}

func TestConcurrentAllocationUniquenessExhaustionAndExactCleanup(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t, GatewayProfileSpec{SelectedIPv4: "10.10.0.5", InterfaceID: "ethernet-concurrent", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	apps := addApps(t, db, 21)
	type result struct {
		allocation Allocation
		err        error
	}
	results := make(chan result, 20)
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			allocation, _, err := repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{AppID: apps[index], OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber})
			results <- result{allocation: allocation, err: err}
		}()
	}
	wait.Wait()
	close(results)
	ports := map[uint16]Allocation{}
	for item := range results {
		if item.err != nil {
			t.Fatalf("concurrent reservation: %v", item.err)
		}
		if previous, duplicate := ports[item.allocation.Port]; duplicate {
			t.Fatalf("port %d allocated twice: %#v and %#v", item.allocation.Port, previous, item.allocation)
		}
		ports[item.allocation.Port] = item.allocation
	}
	if len(ports) != 20 {
		t.Fatalf("unique allocated ports = %d", len(ports))
	}
	extraInput := ReserveAppAccessInput{AppID: apps[20], OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber}
	if _, _, err := repository.ReserveAppAccess(context.Background(), extraInput); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("exhausted allocation error = %v", err)
	}
	var released Allocation
	for _, allocation := range ports {
		released = allocation
		break
	}
	if changed, err := repository.CleanupReserved(context.Background(), AllocationOwner{AllocationID: released.ID, AppID: released.AppID, OperationID: released.OwnerOperationID}); err != nil || !changed {
		t.Fatalf("cleanup changed=%t error=%v", changed, err)
	}
	extra, _, err := repository.ReserveAppAccess(context.Background(), extraInput)
	if err != nil || extra.Port != released.Port {
		t.Fatalf("post-cleanup allocation = %#v want port %d error=%v", extra, released.Port, err)
	}
}

func TestIndependentDatabaseHandlesSerializeAllocationContention(t *testing.T) {
	dataRoot := t.TempDir()
	firstDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstDB.Close() })
	secondDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondDB.Close() })
	if _, err := secondDB.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	addUsers(t, firstDB)
	apps := addApps(t, firstDB, 2)
	firstRepository := testRepository(firstDB)
	secondRepository := testRepository(secondDB)
	profile, _, err := firstRepository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t, GatewayProfileSpec{SelectedIPv4: "192.168.90.8", InterfaceID: "contention-adapter", PortStart: 8107, PortEnd: 8107}, 0))
	if err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	firstRepository.afterReservationLock = func() {
		close(locked)
		<-release
	}
	type result struct {
		allocation Allocation
		err        error
	}
	firstResult := make(chan result, 1)
	secondResult := make(chan result, 1)
	secondInput := ReserveAppAccessInput{AppID: apps[1], OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber}
	go func() {
		allocation, _, err := firstRepository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{AppID: apps[0], OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber})
		firstResult <- result{allocation: allocation, err: err}
	}()
	waitForSignal(t, locked, "first reservation write lock")
	go func() {
		allocation, _, err := secondRepository.ReserveAppAccess(context.Background(), secondInput)
		secondResult <- result{allocation: allocation, err: err}
	}()
	second := waitForResult(t, secondResult, "second reservation")
	if !errors.Is(second.err, ErrConflict) {
		t.Fatalf("contended allocation = %#v error=%v", second.allocation, second.err)
	}
	close(release)
	first := waitForResult(t, firstResult, "first reservation")
	if first.err != nil || first.allocation.Port != 8107 {
		t.Fatalf("first contended allocation = %#v error=%v", first.allocation, first.err)
	}
	if allocation, _, err := secondRepository.ReserveAppAccess(context.Background(), secondInput); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("post-contention allocation = %#v error=%v", allocation, err)
	}
	var live int
	if err := firstDB.QueryRow(`SELECT COUNT(*) FROM lan_port_allocations WHERE released_at IS NULL`).Scan(&live); err != nil || live != 1 {
		t.Fatalf("live allocations=%d error=%v", live, err)
	}
}

func TestIndependentDatabaseHandlesSerializeGatewayCASAndReplay(t *testing.T) {
	dataRoot := t.TempDir()
	firstDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstDB.Close() })
	secondDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondDB.Close() })
	if _, err := secondDB.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	addUsers(t, firstDB)
	firstRepository := testRepository(firstDB)
	secondRepository := testRepository(secondDB)
	firstInput := approvedGatewayInput(t, GatewayProfileSpec{SelectedIPv4: "192.168.93.8", InterfaceID: "gateway-cas-first", PortStart: 8100, PortEnd: 8109}, 0)
	competingInput := approvedGatewayInput(t, GatewayProfileSpec{SelectedIPv4: "10.93.0.8", InterfaceID: "gateway-cas-second", PortStart: 8110, PortEnd: 8119}, 0)

	locked := make(chan struct{})
	release := make(chan struct{})
	firstRepository.afterGatewayLock = func() {
		close(locked)
		<-release
	}
	type result struct {
		profile GatewayProfileRevision
		created bool
		err     error
	}
	firstResult := make(chan result, 1)
	secondResult := make(chan result, 1)
	go func() {
		profile, created, err := firstRepository.ConfigureGatewayProfile(context.Background(), firstInput)
		firstResult <- result{profile: profile, created: created, err: err}
	}()
	waitForSignal(t, locked, "gateway profile write lock")
	go func() {
		profile, created, err := secondRepository.ConfigureGatewayProfile(context.Background(), competingInput)
		secondResult <- result{profile: profile, created: created, err: err}
	}()
	second := waitForResult(t, secondResult, "contended gateway profile")
	close(release)
	if !errors.Is(second.err, ErrConflict) {
		t.Fatalf("contended gateway profile = %#v created=%t error=%v", second.profile, second.created, second.err)
	}
	first := waitForResult(t, firstResult, "first gateway profile")
	if first.err != nil || !first.created || first.profile.RevisionNumber != 1 {
		t.Fatalf("first gateway profile = %#v created=%t error=%v", first.profile, first.created, first.err)
	}
	if profile, created, err := secondRepository.ConfigureGatewayProfile(context.Background(), competingInput); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale gateway CAS = %#v created=%t error=%v", profile, created, err)
	}
	replayed, created, err := secondRepository.ConfigureGatewayProfile(context.Background(), firstInput)
	if err != nil || created || replayed.ID != first.profile.ID {
		t.Fatalf("cross-handle gateway replay = %#v created=%t error=%v", replayed, created, err)
	}
}

func TestIndependentDatabaseHandlesSerializeAppApprovalReplay(t *testing.T) {
	dataRoot := t.TempDir()
	firstDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstDB.Close() })
	secondDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondDB.Close() })
	addUsers(t, firstDB)
	appID := addApps(t, firstDB, 1)[0]
	firstRepository := testRepository(firstDB)
	secondRepository := testRepository(secondDB)
	profile, _, err := firstRepository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t, GatewayProfileSpec{SelectedIPv4: "172.20.93.8", InterfaceID: "approval-cas", PortStart: 8103, PortEnd: 8103}, 0))
	if err != nil {
		t.Fatal(err)
	}
	allocation, _, err := firstRepository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{AppID: appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber})
	if err != nil {
		t.Fatal(err)
	}
	input := approvedAccessInput(t, allocation, 0)
	if _, err := secondDB.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}

	locked := make(chan struct{})
	release := make(chan struct{})
	firstRepository.afterApprovalLock = func() {
		close(locked)
		<-release
	}
	type result struct {
		revision AppAccessRevision
		created  bool
		err      error
	}
	firstResult := make(chan result, 1)
	secondResult := make(chan result, 1)
	go func() {
		revision, created, err := firstRepository.ApproveAppAccess(context.Background(), input)
		firstResult <- result{revision: revision, created: created, err: err}
	}()
	waitForSignal(t, locked, "app approval write lock")
	go func() {
		revision, created, err := secondRepository.ApproveAppAccess(context.Background(), input)
		secondResult <- result{revision: revision, created: created, err: err}
	}()
	second := waitForResult(t, secondResult, "contended app approval")
	close(release)
	if !errors.Is(second.err, ErrConflict) {
		t.Fatalf("contended app approval = %#v created=%t error=%v", second.revision, second.created, second.err)
	}
	first := waitForResult(t, firstResult, "first app approval")
	if first.err != nil || !first.created || first.revision.RevisionNumber != 1 {
		t.Fatalf("first app approval = %#v created=%t error=%v", first.revision, first.created, first.err)
	}
	replayed, created, err := secondRepository.ApproveAppAccess(context.Background(), input)
	if err != nil || created || replayed.ID != first.revision.ID {
		t.Fatalf("cross-handle app approval replay = %#v created=%t error=%v", replayed, created, err)
	}
}

func TestReservationCommitCancellationRollsBackAndReleasesConnection(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t, GatewayProfileSpec{SelectedIPv4: "192.168.91.8", InterfaceID: "cancel-adapter", PortStart: 8108, PortEnd: 8108}, 0))
	if err != nil {
		t.Fatal(err)
	}
	appID := addApps(t, db, 1)[0]
	input := ReserveAppAccessInput{AppID: appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber}
	ctx, cancel := context.WithCancel(context.Background())
	repository.beforeReservationCommit = cancel
	if _, _, err := repository.ReserveAppAccess(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled commit error = %v", err)
	}
	repository.beforeReservationCommit = nil
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_port_allocations WHERE owner_operation_id=?`, input.OperationID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rolled-back allocation rows=%d error=%v", rows, err)
	}
	retryContext, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer retryCancel()
	allocation, created, err := repository.ReserveAppAccess(retryContext, input)
	if err != nil || !created || allocation.Port != 8108 {
		t.Fatalf("post-cancel reservation = %#v created=%t error=%v", allocation, created, err)
	}
}

func TestActivationRequiresCurrentUnarchivedHeadsButUncertaintySurvivesDrift(t *testing.T) {
	t.Run("archived application", func(t *testing.T) {
		db, repository, owner := approvedReservationFixture(t)
		if _, err := db.Exec(`DROP TRIGGER lan_app_access_archive_locked`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE applications SET archived_at=? WHERE id=?`, formatTime(testNow), owner.AppID); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.Activate(context.Background(), owner); !errors.Is(err, ErrConflict) {
			t.Fatalf("archived activation error = %v", err)
		}
		allocation, err := repository.MarkUncertain(context.Background(), owner)
		if err != nil || allocation.State != AllocationUncertain {
			t.Fatalf("post-archive uncertainty = %#v error=%v", allocation, err)
		}
		if _, err := repository.Activate(context.Background(), owner); !errors.Is(err, ErrConflict) {
			t.Fatalf("archived reconciliation error = %v", err)
		}
	})
	t.Run("stale access head", func(t *testing.T) {
		db, repository, owner := approvedReservationFixture(t)
		if _, err := db.Exec(`DELETE FROM lan_app_access_heads WHERE app_id=?`, owner.AppID); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.Activate(context.Background(), owner); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale access head activation error = %v", err)
		}
		if allocation, err := repository.MarkUncertain(context.Background(), owner); err != nil || allocation.State != AllocationUncertain {
			t.Fatalf("stale access head uncertainty = %#v error=%v", allocation, err)
		}
	})
	t.Run("stale gateway head", func(t *testing.T) {
		db, repository, owner := approvedReservationFixture(t)
		if _, err := db.Exec(`DELETE FROM lan_gateway_profile_heads WHERE singleton=1`); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.Activate(context.Background(), owner); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale gateway head activation error = %v", err)
		}
		if allocation, err := repository.MarkUncertain(context.Background(), owner); err != nil || allocation.State != AllocationUncertain {
			t.Fatalf("stale gateway head uncertainty = %#v error=%v", allocation, err)
		}
	})
}

func TestIdempotencyReplaySurvivesDatabaseRestart(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	addUsers(t, db)
	appID := addApps(t, db, 1)[0]
	repository := testRepository(db)
	profileInput := approvedGatewayInput(t, GatewayProfileSpec{SelectedIPv4: "172.20.1.9", InterfaceID: "restart-adapter", PortStart: 8105, PortEnd: 8106}, 0)
	profile, _, err := repository.ConfigureGatewayProfile(context.Background(), profileInput)
	if err != nil {
		t.Fatal(err)
	}
	reserveInput := ReserveAppAccessInput{AppID: appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber}
	allocation, _, err := repository.ReserveAppAccess(context.Background(), reserveInput)
	if err != nil {
		t.Fatal(err)
	}
	approveInput := approvedAccessInput(t, allocation, 0)
	revision, _, err := repository.ApproveAppAccess(context.Background(), approveInput)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	restarted := New(db)
	gotProfile, created, err := restarted.ConfigureGatewayProfile(context.Background(), profileInput)
	if err != nil || created || gotProfile.ID != profile.ID {
		t.Fatalf("restarted profile replay = %#v created=%t error=%v", gotProfile, created, err)
	}
	gotAllocation, created, err := restarted.ReserveAppAccess(context.Background(), reserveInput)
	if err != nil || created || gotAllocation.ID != allocation.ID || gotAllocation.Port != allocation.Port {
		t.Fatalf("restarted reservation replay = %#v created=%t error=%v", gotAllocation, created, err)
	}
	gotRevision, created, err := restarted.ApproveAppAccess(context.Background(), approveInput)
	if err != nil || created || gotRevision.ID != revision.ID || gotRevision.Allocation.ID != allocation.ID {
		t.Fatalf("restarted approval replay = %#v created=%t error=%v", gotRevision, created, err)
	}
}

func appAccessDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := databasefixture.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	addUsers(t, db)
	return db
}

func addUsers(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES
		(?,'administrator','hash','administrator',datetime('now'),datetime('now')),
		(?,'viewer','hash','viewer',datetime('now'),datetime('now'))`, testAdministrator, testViewer); err != nil {
		t.Fatal(err)
	}
}

func addApps(t *testing.T, db *sql.DB, count int) []string {
	t.Helper()
	apps := make([]string, 0, count)
	for index := 0; index < count; index++ {
		id := uuid.NewString()
		if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`, id, "lan-app-"+id, fmt.Sprintf("LAN App %d", index)); err != nil {
			t.Fatal(err)
		}
		apps = append(apps, id)
	}
	return apps
}

func approvedReservationFixture(t *testing.T) (*sql.DB, *Repository, AllocationOwner) {
	t.Helper()
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t, GatewayProfileSpec{SelectedIPv4: "192.168.92.8", InterfaceID: "activation-adapter", PortStart: 8109, PortEnd: 8109}, 0))
	if err != nil {
		t.Fatal(err)
	}
	appID := addApps(t, db, 1)[0]
	allocation, _, err := repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{AppID: appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber})
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := repository.ApproveAppAccess(context.Background(), approvedAccessInput(t, allocation, 0))
	if err != nil {
		t.Fatal(err)
	}
	return db, repository, AllocationOwner{AllocationID: allocation.ID, AppID: appID, OperationID: allocation.OwnerOperationID, AccessRevisionID: revision.ID}
}

func assertArchiveRejected(t *testing.T, db *sql.DB, appID, state string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE applications SET archived_at=? WHERE id=?`, formatTime(testNow), appID); err == nil {
		t.Fatalf("application with %s LAN allocation was archived", state)
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForResult[T any](t *testing.T, result <-chan T, description string) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}

func testRepository(db *sql.DB) *Repository {
	repository := New(db)
	repository.now = func() time.Time { return testNow }
	return repository
}

func approvedGatewayInput(t *testing.T, spec GatewayProfileSpec, expected int64) ConfigureGatewayInput {
	t.Helper()
	return ConfigureGatewayInput{
		OperationID:            uuid.NewString(),
		ExpectedRevisionNumber: expected,
		Spec:                   spec,
		Approval:               Approval{Action: ActionConfigureGateway, SpecDigest: mustGatewayDigest(t, spec), ActorID: testAdministrator},
	}
}

func approvedAccessInput(t *testing.T, allocation Allocation, expected int64) ApproveAppAccessInput {
	t.Helper()
	digest, err := AppAccessSpecDigest(AppAccessSpecFor(allocation))
	if err != nil {
		t.Fatal(err)
	}
	return ApproveAppAccessInput{
		AppID:                  allocation.AppID,
		OperationID:            allocation.OwnerOperationID,
		AllocationID:           allocation.ID,
		ExpectedRevisionNumber: expected,
		Approval:               Approval{Action: ActionEnableAppAccess, SpecDigest: digest, ActorID: testAdministrator},
	}
}

func mustGatewayDigest(t *testing.T, spec GatewayProfileSpec) string {
	t.Helper()
	digest, err := GatewayProfileSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
